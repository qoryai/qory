package runtimeinstall_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// fromNpm installs claude from an npm stub on loopback.
type fromNpm struct {
	npm    *rt.Npm
	folder string
}

func newFromNpm(t *testing.T) *fromNpm {
	return &fromNpm{npm: rt.StartNpm(t), folder: t.TempDir()}
}

func (f *fromNpm) install(client *http.Client, arch string) (runtimeinstall.Installed, error) {
	return runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch: runtimeinstall.NpmFetch(client, f.npm.URL(), arch), Runtime: "claude", Folder: f.folder,
	})
}

// TestTheNpmFetchReadsLatestThenTheVersionThenTheTarball installs claude 2.1.320 from
// npm for arm64 through the host proxy: qory reads the dist-tags, the document of
// 2.1.320 of the arm64 package, and its tarball, in that order, and the program is the
// arm64 one. A second install reads the dist-tags and the document again, and no
// tarball.
func TestTheNpmFetchReadsLatestThenTheVersionThenTheTarball(t *testing.T) {
	f := newFromNpm(t)
	f.npm.Publish("2.1.320")
	proxy := rt.StartProxy(t)
	client, err := runtimeinstall.ProxyClient(proxy.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.install(client, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Release()
	if b, _ := os.ReadFile(got.Program()); string(b) != rt.Program("2.1.320 arm64") || !got.Downloaded {
		t.Fatalf("the program is %q, %+v", b, got)
	}
	tags, doc := "/-/package/@anthropic-ai/claude-code/dist-tags", "/@anthropic-ai/claude-code-linux-arm64/2.1.320"
	want := []string{tags, doc, "/@anthropic-ai/claude-code-linux-arm64/-/claude-code-linux-arm64-2.1.320.tgz"}
	if p := f.npm.Paths(); !slices.Equal(p, want) {
		t.Fatalf("the requests: %q\nwant %q", p, want)
	}
	if proxy.Passed() != 3 || proxy.Refusals() != 0 {
		t.Fatalf("the proxy passed %d and refused %d", proxy.Passed(), proxy.Refusals())
	}
	again, err := f.install(client, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Release()
	if again.Downloaded || f.npm.TarballGets() != 1 || !slices.Equal(f.npm.Paths()[3:], []string{tags, doc}) {
		t.Fatalf("the second install: %+v, %d tarballs, requests %q", again, f.npm.TarballGets(), f.npm.Paths())
	}
}

// TestANewLatestOnNpmInstallsTheNewVersion moves npm's latest from 2.1.320 to 2.1.321
// between two installs: the second installs 2.1.321.
func TestANewLatestOnNpmInstallsTheNewVersion(t *testing.T) {
	f := newFromNpm(t)
	f.npm.Publish("2.1.320")
	first, err := f.install(http.DefaultClient, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	f.npm.Publish("2.1.321")
	got, err := f.install(http.DefaultClient, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Release()
	if got.Version != "2.1.321" || !got.Downloaded {
		t.Fatalf("the second install = %+v, want 2.1.321 downloaded", got)
	}
}

// TestTheNpmFetchRefuses installs from npm releases that qory refuses: each error starts
// with its fix, no version's folder is written, and a tarball qory refuses before it
// reads it is not requested.
func TestTheNpmFetchRefuses(t *testing.T) {
	other := rt.PlainTarball(t, "other")
	for _, c := range []struct {
		name     string
		publish  func(n *rt.Npm)
		base     string
		want     string
		tarballs int
	}{
		{
			name: "tarball that does not match dist.integrity",
			publish: func(n *rt.Npm) {
				n.PublishWith("x64", "2.1.320", rt.PlainTarball(t, "2.1.320"), rt.Integrity(other), "")
				n.SetLatest("2.1.320")
			},
			want:     "check the source of the runtime's releases: the tarball of claude 2.1.320 does not match its checksum " + rt.Integrity(other),
			tarballs: 1,
		},
		{
			name: "tarball on another host",
			publish: func(n *rt.Npm) {
				b := rt.PlainTarball(t, "2.1.320")
				n.PublishWith("x64", "2.1.320", b, rt.Integrity(b), "https://tarballs.example/claude.tgz")
				n.SetLatest("2.1.320")
			},
			want: "check the source of the runtime's releases: the tarball of @anthropic-ai/claude-code-linux-x64 2.1.320 is not on 127.0.0.1:",
		},
		{
			name:    "latest that is no version",
			publish: func(n *rt.Npm) { n.SetLatest("../2") },
			want:    "check the source of the runtime's releases: the dist-tag latest of @anthropic-ai/claude-code is \"../2\"",
		},
		{
			name:    "no platform package",
			publish: func(n *rt.Npm) { n.SetLatest("2.1.320") },
			want:    "check the source of the runtime's releases: @anthropic-ai/claude-code-linux-x64 2.1.320 returned 404",
		},
		{
			name: "registry over http off loopback",
			base: "http://registry.example",
			want: "set the npm registry to an https URL: \"http://registry.example\" is not one",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFromNpm(t)
			if c.publish != nil {
				c.publish(f.npm)
			}
			base := f.npm.URL()
			if c.base != "" {
				base = c.base
			}
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
				Fetch: runtimeinstall.NpmFetch(http.DefaultClient, base, "amd64"), Runtime: "claude", Folder: f.folder,
			})
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("err = %v\nwant it to start with %s", err, c.want)
			}
			if f.npm.TarballGets() != c.tarballs {
				t.Errorf("%d tarballs read, want %d", f.npm.TarballGets(), c.tarballs)
			}
			if got := files(t, f.folder); len(got) > 1 {
				t.Errorf("files = %q, want nothing installed", got)
			}
		})
	}
}
