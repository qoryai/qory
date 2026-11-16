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
		Fetch: runtimeinstall.RegistryFetch(client, f.reg.Host(), arch, f.reg.Instance()), Runtime: "claude", Folder: f.folder,
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

// TestTheRegistryFetchRefuses checks each mismatch in the registry's runtime cache\nthe
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
		}, "ask the instance admin to check the registry's runtime cache\nThe layer of claude 2.1.320 does not match its digest sha256:"},
		{"sha512 checksum", func(t *testing.T, r *rt.Registry) {
			r.Publish("2.1.320", true, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "2.1.320"), Integrity: rt.Integrity([]byte("another tarball"))}})
		}, "The tarball of claude 2.1.320 does not match the checksum sha512-"},
		{"version tag differs from newest", func(t *testing.T, r *rt.Registry) {
			r.PublishPlain("2.1.320")
			newest := r.Tag("newest")
			r.Publish("2.1.320", false, map[string]rt.Layer{"amd64": {Tarball: rt.PlainTarball(t, "other")}})
			r.SetTag("newest", newest)
		}, "ask the instance admin to check the registry's runtime cache\nshared/runtimes/claude:2.1.320 has the digest sha256:"},
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
			if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
				t.Fatalf("the first line has %d characters, want at most 100: %s", len(first), first)
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
			if want := "The registry's cache had no shared/runtimes/claude:newest after 25m0s: the registry returned 503."; rest != want {
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
		Fetch:   runtimeinstall.RegistryFetch(http.DefaultClient, strings.TrimPrefix(reg.URL, "http://"), "amd64", ""),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "check that the run's proxy reaches the registry at ") || len(*waits) != 0 {
		t.Fatalf("err = %v, waits = %v", err, *waits)
	}
}

// TestATokenRealmElsewhereIsRefused has the registry's challenge point at a realm. The
// instance is Qory Apiary's address, http://localhost:<port>: qory takes the realm on the
// instance's scheme and host with the path /registry/token, though it is not the
// registry's host, and asks no other realm for a token. The refused realms are the
// registry's host, another host, http when the instance is https, another port, another
// path, and any realm when the run has no address of the instance.
func TestATokenRealmElsewhereIsRefused(t *testing.T) {
	t.Run("the instance's route is taken", func(t *testing.T) {
		f := newFromRegistry(t)
		_, port, _ := strings.Cut(f.reg.Host(), ":")
		f.reg.RealmHost("localhost:" + port)
		f.reg.PublishPlain("2.1.320")
		// The stand-in proxy forwards only to loopback addresses, so the install is direct.
		got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
			Fetch: runtimeinstall.RegistryFetch(http.DefaultClient, f.reg.Host(), "amd64", f.reg.Instance()), Runtime: "claude", Folder: f.folder,
		})
		if err != nil {
			t.Fatal(err)
		}
		got.Release()
		if f.reg.Instance() != "http://localhost:"+port {
			t.Fatalf("the instance is %s", f.reg.Instance())
		}
	})
	for _, c := range []struct {
		name     string
		instance string
		realm    func(registry, port string) string
		want     string
	}{
		{"the registry's host", "http://localhost:%PORT%", func(registry, _ string) string { return "http://" + registry + "/registry/token" },
			"ask the instance admin to put the registry's token realm at http://localhost:%PORT%/registry/token\nIt is http://%REGISTRY%/registry/token"},
		{"another host", "http://localhost:%PORT%", func(_, port string) string { return "http://127.0.0.1:" + port + "/registry/token" },
			"ask the instance admin to put the registry's token realm at http://localhost:%PORT%/registry/token\nIt is http://127.0.0.1:%PORT%/registry/token"},
		{"another port", "http://localhost:%PORT%", func(_, _ string) string { return "http://localhost:1/registry/token" },
			"ask the instance admin to put the registry's token realm at http://localhost:%PORT%/registry/token\nIt is http://localhost:1/registry/token"},
		{"http when the instance is https", "https://localhost:%PORT%", func(_, port string) string { return "http://localhost:" + port + "/registry/token" },
			"ask the instance admin to put the registry's token realm at https://localhost:%PORT%/registry/token\nIt is http://localhost:%PORT%/registry/token"},
		{"another path", "http://localhost:%PORT%", func(_, port string) string { return "http://localhost:" + port + "/api/token" },
			"ask the instance admin to put the registry's token realm at http://localhost:%PORT%/registry/token\nIt is http://localhost:%PORT%/api/token"},
		{"a longer path", "http://localhost:%PORT%", func(_, port string) string { return "http://localhost:" + port + "/registry/token/x" },
			"ask the instance admin to put the registry's token realm at http://localhost:%PORT%/registry/token\nIt is http://localhost:%PORT%/registry/token/x"},
		{"no address of the instance", "", func(_, port string) string { return "http://localhost:" + port + "/registry/token" },
			"give this run the address of Qory Apiary, whose token route is the registry's realm\nThe realm is http://localhost:%PORT%/registry/token, and the run has no address to check it against."},
	} {
		t.Run(c.name, func(t *testing.T) {
			asked := false
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
			t.Cleanup(other.Close)
			_, port, _ := strings.Cut(strings.TrimPrefix(other.URL, "http://"), ":")
			var realm string
			reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`"`)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(reg.Close)
			host := strings.TrimPrefix(reg.URL, "http://")
			realm = c.realm(host, port)
			fill := strings.NewReplacer("%PORT%", port, "%REGISTRY%", host)
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Fetch:   runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", fill.Replace(c.instance)),
				Runtime: "claude", Folder: t.TempDir(),
			})
			if want := fill.Replace(c.want); err == nil || err.Error() != want || asked {
				t.Fatalf("err = %v, asked = %v; want %q and the realm not asked", err, asked, want)
			}
		})
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
		Fetch: runtimeinstall.RegistryFetch(client, f.reg.Host(), "amd64", f.reg.Instance()), Runtime: "claude", Folder: f.folder,
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

// TestANewNewestInTheRegistryInstallsTheNewVersion moves the registry's newest from
// 2.1.320 to 2.1.321 between two installs: qory keeps no record of the newest version, and the
// second install reads newest again and installs 2.1.321.
func TestANewNewestInTheRegistryInstallsTheNewVersion(t *testing.T) {
	f := newFromRegistry(t)
	f.reg.PublishPlain("2.1.320")
	first, err := f.install(t, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	f.reg.PublishPlain("2.1.321")
	got, err := f.install(t, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Release()
	if got.Version != "2.1.321" || !got.Downloaded {
		t.Fatalf("the second install = %+v, want 2.1.321 downloaded", got)
	}
	if b, _ := os.ReadFile(got.Program()); string(b) != rt.Program("2.1.321 amd64") {
		t.Fatalf("the program is %q", b)
	}
}

// TestARegistryThatNeverRespondsEndsWithTheCause has no registry at the host: each
// request gets no response, and after the retry limit the second line of the error says
// so, with the cause of the last try.
func TestARegistryThatNeverRespondsEndsWithTheCause(t *testing.T) {
	fakeClock(t, 25*time.Minute)
	gone := httptest.NewServer(http.NotFoundHandler())
	host := strings.TrimPrefix(gone.URL, "http://")
	gone.Close()
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", ""),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil {
		t.Fatal("the install succeeded")
	}
	first, rest, _ := strings.Cut(err.Error(), "\n")
	if want := "run again later: " + host + " is still filling its cache of claude newest"; first != want {
		t.Fatalf("first line = %q, want %q", first, want)
	}
	if want := "The registry's cache had no shared/runtimes/claude:newest after 25m0s: the request got no response ("; !strings.HasPrefix(rest, want) {
		t.Fatalf("second line = %q, want it to start with %q", rest, want)
	}
}

// TestAConnectTheProxyRefusesIsNotRetried reads from a registry on a host
// that is not on loopback, so the request is a CONNECT, through a proxy that returns 403
// to it: qory sends it once, and the error says what the proxy returned.
func TestAConnectTheProxyRefusesIsNotRetried(t *testing.T) {
	waits := fakeClock(t, 25*time.Minute)
	proxy := rt.StartProxy(t)
	proxy.Deny(http.StatusForbidden)
	client, err := runtimeinstall.ProxyClient(proxy.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(client, "registry.example", "amd64", ""),
		Runtime: "claude", Folder: t.TempDir(),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "check that the run's proxy reaches the registry at registry.example\n") ||
		!strings.Contains(err.Error(), "the run's proxy returned 403 to the CONNECT to registry.example:443") || proxy.Refusals() != 1 || len(*waits) != 0 {
		t.Fatalf("err = %v, refusals = %d, waits = %v", err, proxy.Refusals(), *waits)
	}
}
