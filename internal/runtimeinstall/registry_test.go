package runtimeinstall_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// fromRegistry installs claude from a stub registry through a stub host proxy.
type fromRegistry struct {
	reg    *rt.Registry
	proxy  *rt.Proxy
	folder string
}

func newFromRegistry(t *testing.T) *fromRegistry {
	return &fromRegistry{reg: rt.StartRegistry(t), proxy: rt.StartProxy(t), folder: t.TempDir()}
}

func (f *fromRegistry) install(t *testing.T, arch string) (runtimeinstall.Installed, error) {
	t.Helper()
	client, err := runtimeinstall.ProxyClient(f.proxy.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	return runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch: runtimeinstall.RegistryFetch(client, f.reg.Host(), arch), Runtime: "claude", Folder: f.folder,
	})
}

// TestTheRegistryFetchResolvesNewestOnceThenPullsByDigest installs claude 2.1.320 from
// the registry for arm64: qory reads newest, the arm64 manifest by its digest, the
// version's tag, the arm64 manifest again, and the layer by its digest, all through the
// proxy with its password.
// A second install reads the same manifests and no layer.
func TestTheRegistryFetchResolvesNewestOnceThenPullsByDigest(t *testing.T) {
	f := newFromRegistry(t)
	f.reg.PublishPlain("2.1.320")
	got, err := f.install(t, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got.Program()); string(b) != rt.Program("2.1.320 arm64") {
		t.Fatalf("the program is %q, want the arm64 build", b)
	}
	var v2 []string
	for _, p := range f.reg.Paths() {
		if rest, ok := strings.CutPrefix(p, "/v2/shared/runtimes/claude/"); ok {
			if i := strings.Index(rest, "sha256:"); i >= 0 {
				rest = rest[:i] + "<digest>"
			}
			v2 = append(v2, rest)
		}
	}
	// The first request gets the 401 that sends qory to the token realm.
	want := []string{"manifests/newest", "manifests/newest", "manifests/<digest>", "manifests/2.1.320", "manifests/<digest>", "blobs/<digest>"}
	if !slices.Equal(v2, want) {
		t.Fatalf("the requests: %q, want %q", v2, want)
	}
	if _, err := f.install(t, "arm64"); err != nil {
		t.Fatal(err)
	}
	if f.reg.BlobGets() != 1 || f.proxy.Passed() == 0 || f.proxy.Refusals() != 0 {
		t.Fatalf("%d blobs, the proxy passed %d and refused %d", f.reg.BlobGets(), f.proxy.Passed(), f.proxy.Refusals())
	}
}

// TestTheRegistryFetchRefuses checks each mismatch in the registry's runtime cache: the
// error starts with its fix, and nothing is installed.
func TestTheRegistryFetchRefuses(t *testing.T) {
	cases := []struct {
		name    string
		publish func(t *testing.T, r *rt.Registry)
		want    string
	}{
		{"layer digest", func(t *testing.T, r *rt.Registry) {
			good := rt.PlainTarball(t, "2.1.320")
			bad := bytes.Clone(good)
			bad[len(bad)/2] ^= 0xff
			r.Publish("2.1.320", true, map[string]rt.Layer{"amd64": {Tarball: good, Served: bad}})
		}, "ask the instance admin to check the registry's runtime cache: the layer of claude 2.1.320 does not match its digest sha256:"},
		{"sha512 checksum", func(t *testing.T, r *rt.Registry) {
			r.Publish("2.1.320", true, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "2.1.320"), Integrity: rt.Integrity([]byte("another tarball"))}})
		}, "the tarball of claude 2.1.320 does not match its checksum sha512-"},
		{"version tag differs from newest", func(t *testing.T, r *rt.Registry) {
			r.PublishPlain("2.1.320")
			newest := r.Tag("newest")
			r.Publish("2.1.320", false, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "other")}})
			r.SetTag("newest", newest)
		}, "ask the instance admin to check the registry's runtime cache: shared/runtimes/claude:2.1.320 has the digest sha256:"},
		{"version that climbs", func(t *testing.T, r *rt.Registry) {
			r.Publish("..", true, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "x")}})
		}, `shared/runtimes/claude:newest has the version "..", and a version is 1 to 64 letters`},
		{"no manifest for the platform", func(t *testing.T, r *rt.Registry) {
			r.Publish("2.1.320", true, map[string]rt.Layer{"arm64": {Tarball: rt.PlainTarball(t, "2.1.320")}})
		}, "shared/runtimes/claude:2.1.320 has 0 image manifests for linux/amd64, not one"},
		{"malicious tarball", func(t *testing.T, r *rt.Registry) {
			r.Publish("2.1.320", true, map[string]rt.Layer{"amd64": {Tarball: rt.Tarball(t, rt.Entry{Name: "package/claude", Kind: '2', Link: "/bin/sh"})}})
		}, "has package/claude as another kind of entry than a regular file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFromRegistry(t)
			c.publish(t, f.reg)
			_, err := f.install(t, "amd64")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one that contains %q", err, c.want)
			}
			if !strings.HasPrefix(err.Error(), "ask the instance admin") && !strings.HasPrefix(err.Error(), "check the source") {
				t.Fatalf("err = %v, want one that starts with its fix", err)
			}
			if got := files(t, f.folder); len(got) > 1 {
				t.Fatalf("files = %q, want nothing installed", got)
			}
		})
	}
}

// TestANewestTheRegistryNeverHeldStartsWithTheFix has no runtime at the registry: the
// error is ErrNotCached, and its first line is the fix, at most 100 characters.
func TestANewestTheRegistryNeverHeldStartsWithTheFix(t *testing.T) {
	f := newFromRegistry(t)
	_, err := f.install(t, "amd64")
	if !errors.Is(err, runtimeinstall.ErrNotCached) {
		t.Fatalf("err = %v, want ErrNotCached", err)
	}
	first, rest, _ := strings.Cut(err.Error(), "\n")
	if want := "check that the instance's registry reaches the publisher of claude, then run again"; first != want || len(first) > 100 {
		t.Fatalf("first line = %q, want %q", first, want)
	}
	if want := "The registry has never held claude and could not fetch it: shared/runtimes/claude:newest returned 404."; rest != want {
		t.Fatalf("second line = %q, want %q", rest, want)
	}
}

// fakeClock replaces the retries' clock until the test ends: each wait moves the clock
// on by its length at once. It returns the waits.
func fakeClock(t *testing.T, limit time.Duration) *[]time.Duration {
	var waits []time.Duration
	t.Cleanup(runtimeinstall.SetRetryClock(limit, func(d time.Duration) { waits = append(waits, d) }))
	return &waits
}

// TestA503WhileTheCacheFillsIsRetried has the registry answer 503 several times, as
// the registry does while its cache fills, then serve claude 2.1.320. With no
// Retry-After, as a failed fill answers, qory waits 2, 4 and 8 seconds, then 15. With
// Retry-After: 15 it waits 15 seconds each time, and a Retry-After above 30 seconds
// waits 30. The fake clock keeps each install under a second.
func TestA503WhileTheCacheFillsIsRetried(t *testing.T) {
	const s = time.Second
	for _, c := range []struct {
		name, retryAfter string
		n                int
		want             []time.Duration
	}{
		{"no Retry-After, twice", "", 2, []time.Duration{2 * s, 4 * s}},
		{"no Retry-After, five times", "", 5, []time.Duration{2 * s, 4 * s, 8 * s, 15 * s, 15 * s}},
		{"Retry-After 15", "15", 2, []time.Duration{15 * s, 15 * s}},
		{"Retry-After 120", "120", 2, []time.Duration{30 * s, 30 * s}},
	} {
		t.Run(c.name, func(t *testing.T) {
			waits := fakeClock(t, 25*time.Minute)
			f := newFromRegistry(t)
			f.reg.PublishPlain("2.1.320")
			f.reg.Unavailable(c.n, c.retryAfter)
			start := time.Now()
			if _, err := f.install(t, "amd64"); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*waits, c.want) {
				t.Fatalf("waits %v, want %v", *waits, c.want)
			}
			if time.Since(start) > time.Second {
				t.Fatalf("the install took %s", time.Since(start))
			}
		})
	}
}

// TestACacheThatStaysUnavailableEndsWithTheFix has the registry answer 503 to every
// request, with no Retry-After and with Retry-After: 15: after 25 minutes of waits on
// the fake clock, the error's first line starts with the fix, names the registry, the
// runtime and newest, and has at most 100 characters.
func TestACacheThatStaysUnavailableEndsWithTheFix(t *testing.T) {
	for _, retryAfter := range []string{"", "15"} {
		t.Run("Retry-After "+retryAfter, func(t *testing.T) {
			waits := fakeClock(t, 25*time.Minute)
			f := newFromRegistry(t)
			f.reg.PublishPlain("2.1.320")
			f.reg.Unavailable(-1, retryAfter)
			_, err := f.install(t, "amd64")
			if err == nil {
				t.Fatal("the install succeeded")
			}
			first, rest, _ := strings.Cut(err.Error(), "\n")
			if want := "run again later: " + f.reg.Host() + " is still filling its cache of claude newest"; first != want || len(first) > 100 {
				t.Fatalf("first line = %q, want %q", first, want)
			}
			if want := "The registry's cache had no shared/runtimes/claude:newest after 25m0s: it answered 503."; rest != want {
				t.Fatalf("second line = %q, want %q", rest, want)
			}
			var total time.Duration
			for _, w := range *waits {
				total += w
			}
			if total > 25*time.Minute || total < 25*time.Minute-30*time.Second {
				t.Fatalf("the waits add up to %s", total)
			}
		})
	}
}

// TestAnotherStatusIsNotRetried has the registry answer 500: qory waits for nothing and
// stops with the fix.
func TestAnotherStatusIsNotRetried(t *testing.T) {
	waits := fakeClock(t, 25*time.Minute)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(reg.Close)
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(http.DefaultClient, strings.TrimPrefix(reg.URL, "http://"), "amd64"),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "check that the run's proxy reaches the registry at ") || len(*waits) != 0 {
		t.Fatalf("err = %v, waits = %v", err, *waits)
	}
}

// TestATokenRealmElsewhereIsRefused has the registry's challenge point at a realm on
// another host with another path: qory asks it for no token.
func TestATokenRealmElsewhereIsRefused(t *testing.T) {
	asked := false
	realm := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	t.Cleanup(realm.Close)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+strings.Replace(realm.URL, "127.0.0.1", "localhost", 1)+`/api/token"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(reg.Close)
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(http.DefaultClient, strings.TrimPrefix(reg.URL, "http://"), "amd64"),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "is neither on the registry's host nor at /registry/token") || asked {
		t.Fatalf("err = %v, asked = %v; want the realm refused and not asked", err, asked)
	}
}

// TestAProxyThatRefusesIsNotRetried sends through a proxy URL with another password:
// the proxy answers 407, and the error starts with the fix.
func TestAProxyThatRefusesIsNotRetried(t *testing.T) {
	waits := fakeClock(t, 25*time.Minute)
	f := newFromRegistry(t)
	f.reg.PublishPlain("2.1.320")
	client, err := runtimeinstall.ProxyClient(strings.Replace(f.proxy.URL(), rt.ProxyPassword, "wrong", 1), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch: runtimeinstall.RegistryFetch(client, f.reg.Host(), "amd64"), Runtime: "claude", Folder: f.folder,
	})
	if err == nil || !strings.HasPrefix(err.Error(), "check that the run's proxy reaches the registry at "+f.reg.Host()) || f.proxy.Refusals() == 0 || len(*waits) != 0 {
		t.Fatalf("err = %v, refusals = %d, waits = %v", err, f.proxy.Refusals(), *waits)
	}
}

// TestTheProgramFromTheRegistryIsMode0755 checks the program installed from the
// registry: bin/claude, mode 0755.
func TestTheProgramFromTheRegistryIsMode0755(t *testing.T) {
	f := newFromRegistry(t)
	f.reg.PublishPlain("2.1.320")
	got, err := f.install(t, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(got.Dir, "bin", "claude"))
	if err != nil || info.Mode() != 0o755 {
		t.Fatalf("bin/claude: %v, %v", info, err)
	}
}
