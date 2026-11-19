package runtimeinstall_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// pinSource is a runtime's npm stub or registry stub with two versions, the older one
// and the newest, which is latest on npm and newest in the registry.
type pinSource struct {
	name, runtime, older, newest string
	start                        func(t *testing.T) (runtimeinstall.Fetch, func() []string)
}

// pinSources are claude and codex, each from npm and from the registry's runtime cache.
func pinSources() []pinSource {
	npm := func(codex bool, versions ...string) func(t *testing.T) (runtimeinstall.Fetch, func() []string) {
		return func(t *testing.T) (runtimeinstall.Fetch, func() []string) {
			start := rt.StartNpm
			if codex {
				start = rt.StartCodexNpm
			}
			n := start(t)
			for _, v := range versions {
				n.Publish(v)
			}
			return runtimeinstall.NpmFetch(http.DefaultClient, n.URL(), "amd64"), n.Paths
		}
	}
	registry := func(codex bool, versions ...string) func(t *testing.T) (runtimeinstall.Fetch, func() []string) {
		return func(t *testing.T) (runtimeinstall.Fetch, func() []string) {
			start := rt.StartRegistry
			if codex {
				start = rt.StartCodexRegistry
			}
			r := start(t)
			for _, v := range versions {
				r.PublishPlain(v)
			}
			return runtimeinstall.RegistryFetch(http.DefaultClient, r.Host(), "amd64", r.Instance()), r.Paths
		}
	}
	return []pinSource{
		{"claude from npm", "claude", "2.1.300", "2.1.320", npm(false, "2.1.300", "2.1.320")},
		{"claude from the registry", "claude", "2.1.300", "2.1.320", registry(false, "2.1.300", "2.1.320")},
		{"codex from npm", "codex", "0.167.2", "0.170.0", npm(true, "0.167.2", "0.170.0")},
		{"codex from the registry", "codex", "0.167.2", "0.170.0", registry(true, "0.167.2", "0.170.0")},
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
				fetch, paths := s.start(t)
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
// *NotPublishedError of the runtime and the version, its fix first.
func TestAPinnedVersionTheRegistryDoesNotHaveIsNotPublished(t *testing.T) {
	for _, s := range pinSources() {
		t.Run(s.name, func(t *testing.T) {
			fetch, _ := s.start(t)
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Keys: rt.Keys(), Fetch: fetch, Runtime: s.runtime, Version: "9.9.9", Folder: t.TempDir(),
			})
			var missing *runtimeinstall.NotPublishedError
			if !errors.As(err, &missing) || missing.Runtime != s.runtime || missing.Version != "9.9.9" {
				t.Fatalf("the install returned %v, want a NotPublishedError of %s 9.9.9", err, s.runtime)
			}
			want := "Pin a version of " + s.runtime + " that the registry has\nThe registry has no " + s.runtime + " 9.9.9."
			if err.Error() != want {
				t.Fatalf("the error is %q, want %q", err, want)
			}
		})
	}
}

// TestInstallRefusesAPinThatIsNotAnExactVersion pins versions that are not exact: a
// dist-tag, a path, and a first character that is not a digit. Install refuses each
// before it fetches.
func TestInstallRefusesAPinThatIsNotAnExactVersion(t *testing.T) {
	for _, pin := range []string{"latest", "newest", "../2.1.320", "-1", "2.1.320/x"} {
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

// TestInstallRefusesAReleaseThatIsNotThePin has a source that returns claude 2.1.320 for
// the pin 2.1.300: Install refuses it, and opens no tarball.
func TestInstallRefusesAReleaseThatIsNotThePin(t *testing.T) {
	src := &rt.Source{}
	src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Keys: rt.Keys(), Fetch: src.Fetch, Runtime: "claude", Version: "2.1.300", Folder: t.TempDir(),
	})
	want := "Check the source of the runtime's releases\nThe source returned claude 2.1.320, and the pinned version is 2.1.300"
	if err == nil || err.Error() != want || src.Opens() != 0 {
		t.Fatalf("the install returned %v after %d opens, want %q", err, src.Opens(), want)
	}
}

// TestValidVersion takes an exact version, a digit first, and refuses a dist-tag, an
// empty version, a path and a version of more than 64 characters.
func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"2.1.320": true, "0.167.2": true, "0.99.0-alpha.20": true, "1.0.0+build.1": true,
		"latest": false, "": false, "v1.0.0": false, "../1": false, "1/2": false,
		"1" + strings.Repeat("0", 63): true, "1" + strings.Repeat("0", 64): false,
	} {
		if got := runtimeinstall.ValidVersion(v); got != want {
			t.Errorf("ValidVersion(%q) = %v, want %v", v, got, want)
		}
	}
}
