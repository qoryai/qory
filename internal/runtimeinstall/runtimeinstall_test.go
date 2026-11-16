package runtimeinstall_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// installer installs claude from src into folder.
type installer struct {
	src    *rt.Source
	folder string
}

func newInstaller(t *testing.T) *installer {
	return &installer{src: &rt.Source{}, folder: t.TempDir()}
}

func (in *installer) install() (runtimeinstall.Installed, error) {
	return runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: in.src.Fetch, Runtime: "claude", Folder: in.folder})
}

// files is every path under dir, relative, with / after each folder's.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// installedFiles are the files of an install of claude 2.1.320.
var installedFiles = []string{"claude/", "claude/2.1.320/", "claude/2.1.320/bin/", "claude/2.1.320/bin/claude", "claude/2.1.320/integrity"}

// TestASecondRunReusesTheVersionFolder installs claude twice from the same newest
// release: the second install fetches the release again and finds the version's folder
// complete, so it opens no tarball.
func TestASecondRunReusesTheVersionFolder(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	first, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Downloaded || first.Version != "2.1.320" || first.Dir != filepath.Join(in.folder, "claude", "2.1.320") {
		t.Fatalf("first install = %+v", first)
	}
	second, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if second.Downloaded || second.Dir != first.Dir || second.Version != first.Version {
		t.Fatalf("second install = %+v, want the first's folder with no download", second)
	}
	if in.src.Fetches() != 2 || in.src.Opens() != 1 {
		t.Fatalf("%d fetches and %d tarballs opened, want 2 and 1", in.src.Fetches(), in.src.Opens())
	}
	if got := in.src.Runtimes(); !slices.Equal(got, []string{"claude", "claude"}) {
		t.Fatalf("fetched %q, want claude twice", got)
	}
}

// TestANewVersionInstallsBesideTheEarlierOne publishes 2.1.321 after 2.1.320: the next
// install extracts it into its folder, and the earlier version's folder stays.
func TestANewVersionInstallsBesideTheEarlierOne(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	if _, err := in.install(); err != nil {
		t.Fatal(err)
	}
	in.src.Publish("2.1.321", rt.PlainTarball(t, "2.1.321"))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2.1.321" || !got.Downloaded {
		t.Fatalf("install = %+v, want 2.1.321 downloaded", got)
	}
	if b, err := os.ReadFile(got.Program()); err != nil || string(b) != rt.Program("2.1.321") {
		t.Fatalf("the program is %q (%v), want 2.1.321's", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(in.folder, "claude", "2.1.320", "bin", "claude")); err != nil || string(b) != rt.Program("2.1.320") {
		t.Fatalf("the earlier version's program is %q (%v)", b, err)
	}
}

// TestTheProgramIsAtBinClaudeMode0755 checks the version's folder: bin/claude, a
// regular file, mode 0755, the folder and bin mode 0755, the checksum file 0644, and no
// other file and no temporary file beside the version.
func TestTheProgramIsAtBinClaudeMode0755(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if got.Program() != filepath.Join(got.Dir, "bin", "claude") {
		t.Fatalf("Program() = %s", got.Program())
	}
	for path, want := range map[string]fs.FileMode{
		filepath.Join(in.folder, "claude"): fs.ModeDir | 0o755, got.Dir: fs.ModeDir | 0o755,
		filepath.Join(got.Dir, "bin"): fs.ModeDir | 0o755, got.Program(): 0o755, filepath.Join(got.Dir, "integrity"): 0o644,
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%s has mode %v, want %v", path, info.Mode(), want)
		}
	}
	if got := files(t, in.folder); !slices.Equal(got, installedFiles) {
		t.Fatalf("files = %q, want %q", got, installedFiles)
	}
}

// TestOnlyPackageClaudeIsExtracted publishes a tarball with entries that climb out of
// the folder, an absolute path, links and a device: only package/claude is written.
func TestOnlyPackageClaudeIsExtracted(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.Tarball(t,
		rt.Entry{Name: "../escaped", Body: "x"},
		rt.Entry{Name: "package/../../escaped", Body: "x"},
		rt.Entry{Name: "/tmp/absolute-escaped", Body: "x"},
		rt.Entry{Name: "package/bin/link", Kind: tar.TypeSymlink, Link: "/etc/passwd"},
		rt.Entry{Name: "package/hard", Kind: tar.TypeLink, Link: "../../escaped"},
		rt.Entry{Name: "package/dev", Kind: tar.TypeChar},
		rt.Entry{Name: "package/claude", Body: rt.Program("2.1.320")},
	))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if got := files(t, in.folder); !slices.Equal(got, installedFiles) {
		t.Fatalf("files = %q, want %q", got, installedFiles)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(in.folder), "escaped")); err == nil {
		t.Fatal("an entry was written outside the folder")
	}
	if b, _ := os.ReadFile(got.Program()); string(b) != rt.Program("2.1.320") {
		t.Fatalf("the program is %q", b)
	}
}

// TestInstallRefuses checks each refusal of a release: the error starts with its fix,
// and no version folder and no temporary file is left.
func TestInstallRefuses(t *testing.T) {
	cases := []struct {
		name    string
		publish func(t *testing.T, s *rt.Source)
		want    string
	}{
		{"wrong integrity", func(t *testing.T, s *rt.Source) {
			s.PublishWith("2.1.320", rt.PlainTarball(t, "2.1.320"), rt.Integrity([]byte("another tarball")))
		}, "check the source of the runtime's releases: the tarball of claude 2.1.320 does not match its checksum sha512-"},
		{"tarball changed after its checksum", func(t *testing.T, s *rt.Source) {
			good := rt.PlainTarball(t, "2.1.320")
			bad := bytes.Clone(good)
			bad[len(bad)/2] ^= 0xff
			s.PublishWith("2.1.320", bad, rt.Integrity(good))
		}, "the tarball of claude 2.1.320 does not match its checksum"},
		{"checksum not sha512", func(t *testing.T, s *rt.Source) {
			s.PublishWith("2.1.320", rt.PlainTarball(t, "2.1.320"), "sha1-AAAA")
		}, "the checksum of claude 2.1.320 is not sha512- and 64 bytes in base64"},
		{"version that climbs", func(t *testing.T, s *rt.Source) {
			s.Publish("..", rt.PlainTarball(t, ".."))
		}, `the newest claude has the version "..", and a version is 1 to 64 letters`},
		{"version with a slash", func(t *testing.T, s *rt.Source) {
			s.Publish("2/../../x", rt.PlainTarball(t, "x"))
		}, `the newest claude has the version "2/../../x"`},
		{"program is a symbolic link", func(t *testing.T, s *rt.Source) {
			s.Publish("2.1.320", rt.Tarball(t, rt.Entry{Name: "package/claude", Kind: tar.TypeSymlink, Link: "/bin/sh"}))
		}, "has package/claude as another kind of entry than a regular file"},
		{"program is a hard link", func(t *testing.T, s *rt.Source) {
			s.Publish("2.1.320", rt.Tarball(t, rt.Entry{Name: "package/x", Body: "x"}, rt.Entry{Name: "package/claude", Kind: tar.TypeLink, Link: "package/x"}))
		}, "has package/claude as another kind of entry than a regular file"},
		{"program twice", func(t *testing.T, s *rt.Source) {
			s.Publish("2.1.320", rt.Tarball(t, rt.Entry{Name: "package/claude", Body: "a"}, rt.Entry{Name: "package/claude", Body: "b"}))
		}, "has package/claude twice"},
		{"no program", func(t *testing.T, s *rt.Source) {
			s.Publish("2.1.320", rt.Tarball(t, rt.Entry{Name: "package/package.json", Body: "{}"}))
		}, "the tarball of claude 2.1.320 has no package/claude"},
		{"not gzip", func(t *testing.T, s *rt.Source) {
			s.Publish("2.1.320", []byte("not a tarball"))
		}, "the tarball of claude 2.1.320 is not gzip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := newInstaller(t)
			c.publish(t, in.src)
			_, err := in.install()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one that contains %q", err, c.want)
			}
			if !strings.HasPrefix(err.Error(), "check the source of the runtime's releases: ") {
				t.Fatalf("err = %v, want one that starts with its fix", err)
			}
			if got := files(t, in.folder); len(got) > 1 {
				t.Fatalf("files = %q, want no version folder and no temporary file", got)
			}
		})
	}
}

// TestAFetchErrorIsReturnedAsItIs has the fetch fail: Install returns its error, whose
// text starts with the fix, and writes nothing.
func TestAFetchErrorIsReturnedAsItIs(t *testing.T) {
	in := newInstaller(t)
	failed := errors.New("check that the run reaches the runtime cache, then run again\nThe cache has never held claude.")
	in.src.Fail(failed)
	_, err := in.install()
	if !errors.Is(err, failed) || err.Error() != failed.Error() {
		t.Fatalf("err = %v, want the fetch's", err)
	}
	if got := files(t, in.folder); len(got) != 0 {
		t.Fatalf("files = %q, want none", got)
	}
}

// TestAFolderOfAnotherTarballIsRefused finds a version folder installed from a tarball
// with another checksum: Install leaves it and opens no tarball, and its error starts
// with the fix to remove the folder.
func TestAFolderOfAnotherTarballIsRefused(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	other := rt.Integrity([]byte("another tarball"))
	if err := os.WriteFile(filepath.Join(got.Dir, "integrity"), []byte(other+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = in.install()
	if err == nil || !strings.HasPrefix(err.Error(), "remove "+got.Dir+" once no run uses it, then run again") {
		t.Fatalf("err = %v, want the fix to remove the folder", err)
	}
	if in.src.Opens() != 1 {
		t.Fatalf("%d tarballs opened, want 1", in.src.Opens())
	}
}

// TestAProgramReplacedByALinkIsRefused replaces an installed program with a link: the
// folder is not taken as complete.
func TestAProgramReplacedByALinkIsRefused(t *testing.T) {
	in := newInstaller(t)
	in.src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(got.Program()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", got.Program()); err != nil {
		t.Fatal(err)
	}
	_, err = in.install()
	if err == nil || !strings.HasPrefix(err.Error(), "remove "+got.Dir+" once no run uses it, then run again: it has no program bin/claude") {
		t.Fatalf("err = %v", err)
	}
}

// TestOversizedTarballsAreRefused lowers the limits and checks each: a tarball larger
// than its limit, a program larger than its limit, and a tarball that unpacks to more
// than its limit, which a small gzip of zeros does.
func TestOversizedTarballsAreRefused(t *testing.T) {
	zeros := strings.Repeat("\x00", 64<<10)
	var noise strings.Builder
	for i := range 256 {
		noise.WriteString(rt.Integrity([]byte{byte(i)}))
	}
	cases := []struct {
		name                     string
		tarball, program, unpack int64
		entries                  []rt.Entry
		want                     string
	}{
		{"tarball", 512, 1 << 20, 1 << 20, []rt.Entry{{Name: "package/claude", Body: noise.String()}}, "the tarball of claude 2.1.320 has more than 512 bytes"},
		{"program", 1 << 20, 1 << 10, 1 << 20, []rt.Entry{{Name: "package/claude", Body: zeros}}, "has package/claude of 65536 bytes, and a program has 1 to 1024"},
		{"unpacked", 1 << 20, 1 << 20, 32 << 10, []rt.Entry{{Name: "package/filler", Body: zeros}, {Name: "package/claude", Body: "x"}}, "the tarball of claude 2.1.320 unpacks to more than 32768 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer runtimeinstall.SetLimits(c.tarball, c.program, c.unpack)()
			in := newInstaller(t)
			in.src.Publish("2.1.320", rt.Tarball(t, c.entries...))
			_, err := in.install()
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "check the source of the runtime's releases: ") {
				t.Fatalf("err = %v, want one that starts with its fix and contains %q", err, c.want)
			}
			if got := files(t, in.folder); len(got) > 1 {
				t.Fatalf("files = %q, want no version folder and no temporary file", got)
			}
		})
	}
}
