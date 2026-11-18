package runtimeinstall_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// installCodex installs codex into folder through fetch.
func installCodex(t *testing.T, fetch runtimeinstall.Fetch, folder string) runtimeinstall.Installed {
	t.Helper()
	got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: fetch, Runtime: "codex", Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(got.Release)
	return got
}

// onlyProgram fails the test unless the version's folder of got contains bin/codex, the
// program of version for arch, and the checksum file, and nothing else of the tarball.
func onlyProgram(t *testing.T, got runtimeinstall.Installed, version, arch string) {
	t.Helper()
	if got.Program() != filepath.Join(got.Dir, "bin", "codex") || got.Version != version {
		t.Fatalf("the install is %+v", got)
	}
	b, err := os.ReadFile(got.Program())
	if err != nil || string(b) != rt.CodexProgram(version+" "+arch) {
		t.Fatalf("the program is %q (%v), want the %s build", b, err, arch)
	}
	var files []string
	err = filepath.WalkDir(got.Dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(got.Dir, p)
			files = append(files, rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if want := []string{"bin/codex", "integrity"}; !slices.Equal(files, want) {
		t.Fatalf("the folder has %q, want %q", files, want)
	}
}

// TestTheNpmFetchInstallsCodexFromItsPlatformVersion installs codex 0.169.1 from npm for
// amd64 and for arm64: qory reads the dist-tags of @openai/codex, the document of the
// version 0.169.1-linux-x64 or -linux-arm64 of the same package, and its tarball, and
// extracts only package/vendor/<x86_64 or aarch64>-unknown-linux-musl/bin/codex to
// bin/codex.
func TestTheNpmFetchInstallsCodexFromItsPlatformVersion(t *testing.T) {
	for arch, cpu := range map[string]string{"amd64": "x64", "arm64": "arm64"} {
		t.Run(arch, func(t *testing.T) {
			npm := rt.StartCodexNpm(t)
			npm.Publish("0.169.1")
			got := installCodex(t, runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), arch), t.TempDir())
			onlyProgram(t, got, "0.169.1", arch)
			want := []string{
				"/-/package/@openai/codex/dist-tags",
				"/@openai/codex/0.169.1-linux-" + cpu,
				"/@openai/codex/-/codex-0.169.1-linux-" + cpu + ".tgz",
			}
			if p := npm.Paths(); !slices.Equal(p, want) {
				t.Fatalf("the requests: %q\nwant %q", p, want)
			}
		})
	}
}

// TestACodexTarballWithoutItsProgramIsRefused publishes a Codex tarball whose program is
// at package/codex, as Claude Code's is, and not under vendor/: the install fails with
// the fix first and names the entry it looked for.
func TestACodexTarballWithoutItsProgramIsRefused(t *testing.T) {
	npm := rt.StartCodexNpm(t)
	b := rt.PlainTarball(t, "0.169.1")
	npm.PublishWith("arm64", "0.169.1", b, rt.Integrity(b), "")
	npm.SetLatest("0.169.1")
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{
		Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "arm64"), Runtime: "codex", Folder: t.TempDir(),
	})
	if err == nil {
		t.Fatal("no refusal")
	}
	first, second, _ := strings.Cut(err.Error(), "\n")
	if first != "Check the source of the runtime's releases" || !strings.Contains(second, "package/vendor/aarch64-unknown-linux-musl/bin/codex") {
		t.Fatalf("the error is %q", err)
	}
}

// TestTheRegistryFetchInstallsCodex installs codex 0.169.1 from the repository
// shared/runtimes/codex of the instance's registry for arm64, whose layer is the
// tarball of Codex's arm64 build: the install extracts the program at its vendor path.
func TestTheRegistryFetchInstallsCodex(t *testing.T) {
	reg, proxy := rt.StartCodexRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("0.169.1")
	client, err := runtimeinstall.ProxyClient(proxy.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := installCodex(t, runtimeinstall.RegistryFetch(client, reg.Host(), "arm64", reg.Instance()), t.TempDir())
	onlyProgram(t, got, "0.169.1", "arm64")
	if reg.BlobGets() != 1 {
		t.Fatalf("%d blobs, want 1", reg.BlobGets())
	}
}
