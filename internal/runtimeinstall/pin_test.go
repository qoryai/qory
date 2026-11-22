package runtimeinstall_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// pinSource is a runtime's npm stub or registry stub with two versions, the older one
// and the newest, which is latest on npm and newest in the registry. start starts the
// stub and returns a Fetch that sends its requests with client, the stub's request
// paths, and the stub's host and port.
type pinSource struct {
	name, runtime, older, newest string
	npm                          bool
	start                        func(t *testing.T, client *http.Client) (runtimeinstall.Fetch, func() []string, string)
}

// pinSources are claude and codex, each from npm and from the registry's runtime cache.
func pinSources() []pinSource {
	npm := func(codex bool, versions ...string) func(t *testing.T, client *http.Client) (runtimeinstall.Fetch, func() []string, string) {
		return func(t *testing.T, client *http.Client) (runtimeinstall.Fetch, func() []string, string) {
			start := rt.StartNpm
			if codex {
				start = rt.StartCodexNpm
			}
			n := start(t)
			for _, v := range versions {
				n.Publish(v)
			}
			return runtimeinstall.NpmFetch(client, n.URL(), "amd64"), n.Paths, strings.TrimPrefix(n.URL(), "http://")
		}
	}
	registry := func(codex bool, versions ...string) func(t *testing.T, client *http.Client) (runtimeinstall.Fetch, func() []string, string) {
		return func(t *testing.T, client *http.Client) (runtimeinstall.Fetch, func() []string, string) {
			start := rt.StartRegistry
			if codex {
				start = rt.StartCodexRegistry
			}
			r := start(t)
			for _, v := range versions {
				r.PublishPlain(v)
			}
			return runtimeinstall.RegistryFetch(client, r.Host(), "amd64", r.Instance()), r.Paths, r.Host()
		}
	}
	return []pinSource{
		{"claude from npm", "claude", "2.1.300", "2.1.320", true, npm(false, "2.1.300", "2.1.320")},
		{"claude from the registry", "claude", "2.1.300", "2.1.320", false, registry(false, "2.1.300", "2.1.320")},
		{"codex from npm", "codex", "0.167.2", "0.170.0", true, npm(true, "0.167.2", "0.170.0")},
		{"codex from the registry", "codex", "0.167.2", "0.170.0", false, registry(true, "0.167.2", "0.170.0")},
	}
}

// program is the program of runtime's build of version for amd64 in the stubs' tarballs.
func program(runtime, version string) string {
	if runtime == "codex" {
		return rt.CodexProgram(version + " amd64")
	}
	return rt.Program(version + " amd64")
}

// TestAPinnedVersionInstallsThatVersion installs claude and codex, from npm and from the
// registry's runtime cache, each with an older version and a newer one that is latest
// or newest: a pinned older version installs that version and reads neither npm's
// dist-tags nor the registry's newest, and no pin installs the newest.
func TestAPinnedVersionInstallsThatVersion(t *testing.T) {
	for _, s := range pinSources() {
		for _, pin := range []string{s.older, ""} {
			want := pin
			if pin == "" {
				want = s.newest
			}
			t.Run(s.name+" "+want, func(t *testing.T) {
				fetch, paths, _ := s.start(t, http.DefaultClient)
				got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
					Keys: rt.Keys(), Fetch: fetch, Runtime: s.runtime, Version: pin, Folder: t.TempDir(),
				})
				if err != nil {
					t.Fatal(err)
				}
				defer got.Release()
				if b, _ := os.ReadFile(got.Program()); got.Version != want || string(b) != program(s.runtime, want) {
					t.Fatalf("installed %s with the program %q, want %s", got.Version, b, want)
				}
				read := strings.Join(paths(), " ")
				newest := strings.Contains(read, "/dist-tags") || strings.Contains(read, "/manifests/newest")
				if newest != (pin == "") {
					t.Fatalf("the requests %q: reading the newest is %v, want %v", read, newest, pin == "")
				}
			})
		}
	}
}

// TestAPinnedVersionTheRegistryDoesNotHaveIsNotPublished pins claude and codex to 9.9.9,
// which neither npm nor the registry's runtime cache has: the install returns a
// *NotPublishedError of the runtime and the version, whose text is the cause alone.
func TestAPinnedVersionTheRegistryDoesNotHaveIsNotPublished(t *testing.T) {
	for _, s := range pinSources() {
		t.Run(s.name, func(t *testing.T) {
			fetch, _, _ := s.start(t, http.DefaultClient)
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: fetch, Runtime: s.runtime, Version: "9.9.9", Folder: t.TempDir(),
			})
			var missing *runtimeinstall.NotPublishedError
			if !errors.As(err, &missing) || missing.Runtime != s.runtime || missing.Version != "9.9.9" {
				t.Fatalf("the install returned %v, want a NotPublishedError of %s 9.9.9", err, s.runtime)
			}
			want := "the registry has no " + s.runtime + " 9.9.9"
			if err.Error() != want {
				t.Fatalf("the error is %q, want %q", err, want)
			}
		})
	}
}

// statusFor is a transport that sends each request on, and replaces a 200 response to a
// request of the version's document or tag, a path with /<version> that is no tarball,
// with an empty response of status. The registry's first request of the tag has no
// token and gets 401 from the stub, so status replaces only the request with the token.
type statusFor struct {
	version string
	status  int
}

func (s statusFor) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(req.URL.Path, "/"+s.version) || strings.HasSuffix(req.URL.Path, ".tgz") {
		return resp, err
	}
	resp.Body.Close()
	return &http.Response{StatusCode: s.status, Status: http.StatusText(s.status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{}, Body: http.NoBody, Request: req}, nil
}

// TestOnlyA404MakesAPinNotPublished pins claude and codex to a version that npm and the
// registry's runtime cache have, and has npm return 500 for the version's document, and
// the registry 500 and then 401 for the version's tag: the install returns no
// *NotPublishedError, and the first line of its error is the fix for that status.
func TestOnlyA404MakesAPinNotPublished(t *testing.T) {
	for _, s := range pinSources() {
		statuses := []int{http.StatusInternalServerError}
		if !s.npm {
			statuses = append(statuses, http.StatusUnauthorized)
		}
		for _, status := range statuses {
			t.Run(fmt.Sprintf("%s %d", s.name, status), func(t *testing.T) {
				fetch, _, host := s.start(t, &http.Client{Transport: statusFor{version: s.older, status: status}})
				_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
					Keys: rt.Keys(), Fetch: fetch, Runtime: s.runtime, Version: s.older, Folder: t.TempDir(),
				})
				fix := "Check that this machine reaches " + host
				switch {
				case !s.npm && status == http.StatusUnauthorized:
					fix = "Check that Qory Apiary lets the run pull shared/runtimes/" + s.runtime
				case !s.npm:
					fix = "Check that the run's proxy reaches the registry at " + host
				}
				var missing *runtimeinstall.NotPublishedError
				if err == nil || errors.As(err, &missing) || !strings.HasPrefix(err.Error(), fix+"\n") {
					t.Fatalf("the install returned %v, want an error whose first line is %q", err, fix)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("returned %d", status)) {
					t.Fatalf("the error %q has no status %d", err, status)
				}
			})
		}
	}
}

// TestTheRegistryRefusesAPinnedTagThatIsNotThePin pins claude to 2.1.300 in a registry
// whose tag 2.1.300 has an image manifest in place of an image index, or the index of
// 2.1.320: the install stops with the fix first and the tag and its fault after it.
func TestTheRegistryRefusesAPinnedTagThatIsNotThePin(t *testing.T) {
	const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	for _, c := range []struct {
		name string
		tag  func(r *rt.Registry) []byte
		want string
	}{
		{"an image manifest", func(*rt.Registry) []byte { return []byte(manifest) },
			"shared/runtimes/claude:2.1.300 is not a runtime's image index:\n" +
				"  its media type is \"application/vnd.oci.image.manifest.v1+json\", not application/vnd.oci.image.index.v1+json"},
		{"the index of another version", func(r *rt.Registry) []byte { return r.Tag("2.1.320") },
			"shared/runtimes/claude:2.1.300 has the version \"2.1.320\""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := rt.StartRegistry(t)
			r.PublishPlain("2.1.300")
			r.PublishPlain("2.1.320")
			r.SetTag("2.1.300", c.tag(r))
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: runtimeinstall.RegistryFetch(http.DefaultClient, r.Host(), "amd64", r.Instance()),
				Runtime: "claude", Version: "2.1.300", Folder: t.TempDir(),
			})
			want := "Ask Qory Apiary's instance admin to check the registry's runtime cache\n" + c.want
			if err == nil || err.Error() != want {
				t.Fatalf("the install returned %v, want %q", err, want)
			}
		})
	}
}

// TestAPinnedVersionWithABadSignatureStops pins claude 2.1.320, from npm and from the
// registry's runtime cache, whose signature is npm's signature of 2.1.319: the install
// stops because the signature does not verify, and reads no tarball and no layer.
func TestAPinnedVersionWithABadSignatureStops(t *testing.T) {
	const want = "Check the source of claude's releases.\n  npm's signature of claude 2.1.320 does not verify.\n  The key is " + rt.KeyID + "."
	const other = "@anthropic-ai/claude-code-linux-x64@2.1.319"
	t.Run("npm", func(t *testing.T) {
		npm := rt.StartNpm(t)
		npm.Publish("2.1.320")
		npm.Publish("2.1.321")
		npm.SignWith(func(_, i string) any { return rt.Signatures(other, i) })
		_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
			Keys: rt.Keys(), Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "amd64"), Runtime: "claude", Version: "2.1.320", Folder: t.TempDir(),
		})
		if err == nil || err.Error() != want || npm.TarballGets() != 0 {
			t.Fatalf("the install returned %v after %d tarballs, want %q", err, npm.TarballGets(), want)
		}
	})
	t.Run("the registry", func(t *testing.T) {
		f := newFromRegistry(t)
		tarball := rt.PlainTarball(t, "2.1.320")
		bad := rt.SignaturesJSON(other, rt.Integrity(tarball))
		f.reg.Publish("2.1.320", false, map[string]rt.Layer{"amd64": {Tarball: tarball, Signatures: &bad}})
		f.reg.PublishPlain("2.1.321")
		client, err := runtimeinstall.ProxyClient(f.proxy.URL(), "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtimeinstall.Install(context.Background(), runtimeinstall.Options{
			Keys: rt.Keys(), Fetch: runtimeinstall.RegistryFetch(client, f.reg.Host(), "amd64", f.reg.Instance()), Runtime: "claude", Version: "2.1.320", Folder: f.folder,
		})
		if err == nil || err.Error() != want || f.reg.BlobGets() != 0 {
			t.Fatalf("the install returned %v after %d layers, want %q", err, f.reg.BlobGets(), want)
		}
	})
}

// TestInstallRefusesAPinThatIsNotAnExactVersion pins versions that are not exact: a
// dist-tag, a path, a first character that is not a digit, a +, which no OCI tag has,
// and a version of 33 characters. Install refuses each before it fetches.
func TestInstallRefusesAPinThatIsNotAnExactVersion(t *testing.T) {
	for _, pin := range []string{"latest", "newest", "../2.1.320", "-1", "2.1.320/x", "1.0.0+build.1", "1" + strings.Repeat("0", 32)} {
		t.Run(pin, func(t *testing.T) {
			src := &rt.Source{}
			src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: src.Fetch, Runtime: "claude", Version: pin, Folder: t.TempDir(),
			})
			if err == nil || !strings.HasPrefix(err.Error(), "Pin an exact version of claude, such as 2.1.320\n") || src.Fetches() != 0 {
				t.Fatalf("the install returned %v after %d fetches", err, src.Fetches())
			}
		})
	}
}

// TestInstallRefusesAReleaseThatIsNotThePin has a source that returns claude 2.1.320, a
// version with a line break, or "..", for the pin 2.1.300: Install refuses each with the
// text of a release that is not the pin, never the text of the newest's version, with a
// version that is not a version's shape in quotes, and opens no tarball.
func TestInstallRefusesAReleaseThatIsNotThePin(t *testing.T) {
	for returned, shown := range map[string]string{"2.1.320": "2.1.320", "2.1.320\nx": `"2.1.320\nx"`, "..": `".."`} {
		src := &rt.Source{}
		src.Publish(returned, rt.PlainTarball(t, "2.1.320"))
		_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
			Keys: rt.Keys(), Fetch: src.Fetch, Runtime: "claude", Version: "2.1.300", Folder: t.TempDir(),
		})
		want := "Check the source of claude's releases.\n  The source returned claude " + shown + ", and the pinned version is 2.1.300."
		if err == nil || err.Error() != want || src.Opens() != 0 {
			t.Errorf("the install returned %v after %d opens, want %q", err, src.Opens(), want)
		}
	}
}

// TestValidVersion takes an exact version, a digit first, and refuses a dist-tag, an
// empty version, a path, a version with + and a version of more than 32 characters.
func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"2.1.320": true, "0.167.2": true, "0.99.0-alpha.20": true, "1.0.0+build.1": false,
		"latest": false, "": false, "v1.0.0": false, "../1": false, "1/2": false,
		"1" + strings.Repeat("0", 31): true, "1" + strings.Repeat("0", 32): false,
	} {
		if got := runtimeinstall.ValidVersion(v); got != want {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, got, want)
		}
	}
}
