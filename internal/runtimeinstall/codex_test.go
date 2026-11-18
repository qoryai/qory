package runtimeinstall_test

import (
	"archive/tar"
	"context"
	"io/fs"
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
	got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Keys: rt.Keys(), Fetch: fetch, Runtime: "codex", Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(got.Release)
	return got
}

// wholeFolder fails the test unless the version's folder of got is Codex's folder of
// version for arch, as [rt.CodexFolder] has it, with the checksum file, and nothing else
// of the tarball: each file with its body and mode, and each folder mode 0755. The
// program is bin/codex in it.
func wholeFolder(t *testing.T, got runtimeinstall.Installed, version, arch string) {
	t.Helper()
	if got.Program() != filepath.Join(got.Dir, "bin", "codex") || got.Version != version {
		t.Fatalf("the install is %+v", got)
	}
	want := map[string]rt.Entry{"integrity": {Mode: 0o644}}
	for _, f := range rt.CodexFolder(version + " " + arch) {
		want[f.Name] = f
	}
	seen := 0
	err := filepath.WalkDir(got.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(got.Dir, p)
		if d.IsDir() {
			if info.Mode().Perm() != 0o755 {
				t.Errorf("the folder %s has mode %v, want 0755", rel, info.Mode().Perm())
			}
			return nil
		}
		f, ok := want[filepath.ToSlash(rel)]
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != fs.FileMode(f.Mode) {
			t.Errorf("the folder has %s, mode %v, want %v", rel, info.Mode(), fs.FileMode(f.Mode))
			return nil
		}
		seen++
		if rel == "integrity" {
			return nil
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != f.Body {
			t.Errorf("%s has %q (%v), want %q", rel, b, err, f.Body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != len(want) {
		t.Errorf("the folder has %d of the %d files", seen, len(want))
	}
}

// TestTheNpmFetchInstallsCodexFromItsPlatformVersion installs codex 0.170.0 from npm for
// amd64 and for arm64: qory reads the dist-tags of @openai/codex, the document of the
// version 0.170.0-linux-x64 or -linux-arm64 of the same package, and its tarball, and
// extracts the whole folder package/vendor/<x86_64 or aarch64>-unknown-linux-musl/ as
// the version's folder: bin/codex, with bin/codex-code-mode-host, codex-resources/bwrap,
// codex-path/rg and codex-package.json beside it.
func TestTheNpmFetchInstallsCodexFromItsPlatformVersion(t *testing.T) {
	for arch, cpu := range map[string]string{"amd64": "x64", "arm64": "arm64"} {
		t.Run(arch, func(t *testing.T) {
			npm := rt.StartCodexNpm(t)
			npm.Publish("0.170.0")
			got := installCodex(t, runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), arch), t.TempDir())
			wholeFolder(t, got, "0.170.0", arch)
			want := []string{
				"/-/package/@openai/codex/dist-tags",
				"/@openai/codex/0.170.0-linux-" + cpu,
				"/@openai/codex/-/codex-0.170.0-linux-" + cpu + ".tgz",
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
	b := rt.PlainTarball(t, "0.170.0")
	npm.PublishWith("arm64", "0.170.0", b, rt.Integrity(b), "")
	npm.SetLatest("0.170.0")
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Keys: rt.Keys(),
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

// TestTheRegistryFetchInstallsCodex installs codex 0.170.0 from the repository
// shared/runtimes/codex of the instance's registry for amd64 and for arm64, whose layer
// is npm's tarball of Codex's build, unchanged: the install extracts the whole vendor
// folder of the platform, as from npm.
func TestTheRegistryFetchInstallsCodex(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			reg, proxy := rt.StartCodexRegistry(t), rt.StartProxy(t)
			reg.PublishPlain("0.170.0")
			client, err := runtimeinstall.ProxyClient(proxy.URL(), "")
			if err != nil {
				t.Fatal(err)
			}
			got := installCodex(t, runtimeinstall.RegistryFetch(client, reg.Host(), arch, reg.Instance()), t.TempDir())
			wholeFolder(t, got, "0.170.0", arch)
			if reg.BlobGets() != 1 {
				t.Fatalf("%d blobs, want 1", reg.BlobGets())
			}
		})
	}
}

// TestACodexFolderQoryCannotKeepWholeIsRefused publishes Codex tarballs whose vendor
// folder has an entry the install does not write: a path out of the folder, a link, a
// hard link, a file named integrity at its top, the same file twice, a file and a folder
// of one name, and a program of 0 bytes. Each install fails with the fix first, names
// the entry, and leaves no version's folder.
func TestACodexFolderQoryCannotKeepWholeIsRefused(t *testing.T) {
	const vendor = "package/vendor/aarch64-unknown-linux-musl/"
	program := rt.Entry{Name: vendor + "bin/codex", Body: rt.CodexProgram("0.170.0 arm64"), Mode: 0o755}
	for _, c := range []struct {
		name    string
		entries []rt.Entry
		want    string
	}{
		{"a path out of the folder", []rt.Entry{program, {Name: vendor + "../x86_64-unknown-linux-musl/bin/codex", Body: "x"}}, "is not a plain relative path"},
		{"a dot segment", []rt.Entry{program, {Name: vendor + "./codex-path/rg", Body: "x"}}, "is not a plain relative path"},
		{"a link", []rt.Entry{program, {Name: vendor + "codex-path/rg", Kind: tar.TypeSymlink, Link: "/usr/bin/rg"}}, "another kind of entry than a regular file or a folder"},
		{"a hard link", []rt.Entry{program, {Name: vendor + "codex-path/rg", Kind: tar.TypeLink, Link: vendor + "bin/codex"}}, "another kind of entry than a regular file or a folder"},
		{"the checksum file", []rt.Entry{program, {Name: vendor + "integrity", Body: "sha512-x"}}, "qory writes its checksum file there"},
		{"a file twice", []rt.Entry{program, program}, "twice"},
		{"a file and a folder", []rt.Entry{{Name: vendor + "bin", Body: "x"}, program}, "twice"},
		{"an empty program", []rt.Entry{{Name: vendor + "bin/codex", Mode: 0o755}}, "has no " + vendor + "bin/codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			npm := rt.StartCodexNpm(t)
			b := rt.Tarball(t, c.entries...)
			npm.PublishWith("arm64", "0.170.0", b, rt.Integrity(b), "")
			npm.SetLatest("0.170.0")
			folder := t.TempDir()
			_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Keys: rt.Keys(),
				Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "arm64"), Runtime: "codex", Folder: folder,
			})
			if err == nil {
				t.Fatal("no refusal")
			}
			first, second, _ := strings.Cut(err.Error(), "\n")
			if first != "Check the source of the runtime's releases" || !strings.Contains(second, c.want) {
				t.Fatalf("the error is %q, want %q on its second line", err, c.want)
			}
			if _, err := os.Lstat(filepath.Join(folder, "codex", "0.170.0")); !os.IsNotExist(err) {
				t.Fatalf("the version's folder: %v", err)
			}
		})
	}
}

// codexHelpers are the files beside bin/codex that Codex's program runs.
var codexHelpers = []string{"codex-resources/bwrap", "codex-path/rg", "bin/codex-code-mode-host"}

// installCodexFolder publishes, as Codex 0.170.0 for arm64, a tarball whose vendor
// folder is [rt.CodexFolder] with edit applied to each file, without the files for which
// edit returns false, and installs it into folder.
func installCodexFolder(t *testing.T, folder string, edit func(*rt.Entry) bool) (runtimeinstall.Installed, error) {
	t.Helper()
	const vendor = "package/vendor/aarch64-unknown-linux-musl/"
	var entries []rt.Entry
	for _, f := range rt.CodexFolder("0.170.0 arm64") {
		if edit(&f) {
			entries = append(entries, rt.Entry{Name: vendor + f.Name, Body: f.Body, Mode: f.Mode})
		}
	}
	npm := rt.StartCodexNpm(t)
	b := rt.Tarball(t, entries...)
	npm.PublishWith("arm64", "0.170.0", b, rt.Integrity(b), "")
	npm.SetLatest("0.170.0")
	got, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Keys: rt.Keys(),
		Fetch: runtimeinstall.NpmFetch(http.DefaultClient, npm.URL(), "arm64"), Runtime: "codex", Folder: folder,
	})
	if err == nil {
		t.Cleanup(got.Release)
	}
	return got, err
}

// TestACodexFolderWithoutAHelperIsRefused publishes Codex tarballs whose vendor folder
// lacks one of the files that bin/codex runs, or has it with 0 bytes:
// codex-resources/bwrap, codex-path/rg or bin/codex-code-mode-host. Each install fails
// with the fix, to update qory, names the file, and leaves no version's folder.
func TestACodexFolderWithoutAHelperIsRefused(t *testing.T) {
	for _, helper := range codexHelpers {
		for kind, edit := range map[string]func(*rt.Entry) bool{
			"missing": func(f *rt.Entry) bool { return f.Name != helper },
			"empty": func(f *rt.Entry) bool {
				if f.Name == helper {
					f.Body = ""
				}
				return true
			},
		} {
			t.Run(kind+" "+helper, func(t *testing.T) {
				folder := t.TempDir()
				_, err := installCodexFolder(t, folder, edit)
				want := "Update qory.\nThe tarball of codex 0.170.0 has no package/vendor/aarch64-unknown-linux-musl/" + helper + "."
				if err == nil || err.Error() != want {
					t.Fatalf("the error is %v, want %q", err, want)
				}
				if _, err := os.Lstat(filepath.Join(folder, "codex", "0.170.0")); !os.IsNotExist(err) {
					t.Fatalf("the version's folder: %v", err)
				}
			})
		}
	}
}

// TestCodexHelpersAreInstalledRunnable publishes Codex's folder with each helper of
// [codexHelpers] at mode 0644: the install makes each one 0755, as bin/codex, so everyone
// can read and run it.
func TestCodexHelpersAreInstalledRunnable(t *testing.T) {
	got, err := installCodexFolder(t, t.TempDir(), func(f *rt.Entry) bool {
		if slices.Contains(codexHelpers, f.Name) {
			f.Mode = 0o644
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, helper := range codexHelpers {
		info, err := os.Lstat(filepath.Join(got.Dir, filepath.FromSlash(helper)))
		if err != nil || info.Mode() != 0o755 {
			t.Errorf("%s: %v, %v, want mode 0755", helper, info, err)
		}
	}
}

// TestAPartialCodexFolderIsNotTakenAsComplete installs Codex, then removes one helper of
// [codexHelpers], or makes it one nobody can run, as a partial earlier install left it:
// the next install refuses the version's folder, with the fix to remove it, and leaves
// it as it is.
func TestAPartialCodexFolderIsNotTakenAsComplete(t *testing.T) {
	for _, helper := range codexHelpers {
		for kind, spoil := range map[string]func(string) error{
			"removed":      os.Remove,
			"not runnable": func(p string) error { return os.Chmod(p, 0o644) },
		} {
			t.Run(kind+" "+helper, func(t *testing.T) {
				folder := t.TempDir()
				keep := func(*rt.Entry) bool { return true }
				got, err := installCodexFolder(t, folder, keep)
				if err != nil {
					t.Fatal(err)
				}
				if err := spoil(filepath.Join(got.Dir, filepath.FromSlash(helper))); err != nil {
					t.Fatal(err)
				}
				_, err = installCodexFolder(t, folder, keep)
				want := "Remove the version's folder once no run uses it, then run again\n" + got.Dir + " has no " + helper + " that everyone can read and run."
				if err == nil || err.Error() != want {
					t.Fatalf("the error is %v, want %q", err, want)
				}
				if _, err := os.Lstat(filepath.Join(got.Dir, "bin", "codex")); err != nil {
					t.Fatalf("the version's folder was changed: %v", err)
				}
			})
		}
	}
}
