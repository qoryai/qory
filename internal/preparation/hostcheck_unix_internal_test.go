//go:build unix

package preparation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/places"
)

// TestHoldsTheWorkingFolderUntilTheFetchEnds is a fetch that a stand-in git stops at
// git fetch: a walled run that starts in the working folder meanwhile waits until the
// fetch and the checkout end, so it cannot change the configuration qory checked.
func TestHoldsTheWorkingFolderUntilTheFetchEnds(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", map[string]string{"README.md": "shop\n"})
	dir, err := filepath.EvalSymlinks(emptyFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	flags := t.TempDir()
	started, proceed := filepath.Join(flags, "started"), filepath.Join(flags, "proceed")
	bin := t.TempDir()
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\nfor a; do [ \"$a\" = fetch ] && { touch '"+started+"'; i=0; while [ ! -e '"+proceed+"' ] && [ $i -lt 300 ]; do sleep 0.1; i=$((i+1)); done; break; }; done\nexec "+gitPath+" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	fetched := make(chan error, 1)
	go func() {
		_, err := prepare(context.Background(), dir, env("github.com", "acme/shop"))
		fetched <- err
	}()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fetch did not start")
		}
	}
	walled := make(chan *places.Hold, 1)
	go func() {
		h, err := places.Starting([]string{dir})
		if err != nil {
			t.Error(err)
		}
		walled <- h
	}()
	select {
	case h := <-walled:
		h.Release()
		t.Fatal("a walled run started in the working folder during the fetch")
	case <-time.After(500 * time.Millisecond):
	}
	write(t, proceed, "")
	if err := <-fetched; err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-walled:
		h.Release()
	case <-time.After(10 * time.Second):
		t.Fatal("a walled run still waits after the fetch ended")
	}
}

// TestStopsOnAHardLink is a working folder with a hard link to a file outside it, at
// .git/info/exclude, which qory writes in place for a further repository, at
// .git/FETCH_HEAD, which git writes in place, and under .qory: the run stops before the
// fetch, with a line that starts with its fix, and the outside file is as it was.
func TestStopsOnAHardLink(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "main", nil)
	for _, name := range []string{".git/info/exclude", ".git/FETCH_HEAD", ".qory/notes"} {
		dir := emptyFolder(t)
		outside := filepath.Join(t.TempDir(), "outside")
		write(t, outside, "outside\n")
		at := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Remove(at)
		if err := os.Link(outside, at); err != nil {
			t.Fatal(err)
		}
		_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop,acme/lib"))
		want := "qory run: start the run in a new empty git working tree, or remove the hard link " + name + ", then run again: a write to it would change another file"
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", name, err, want)
		}
		if got, _ := os.ReadFile(outside); string(got) != "outside\n" {
			t.Errorf("%s: the outside file contains %q", name, got)
		}
	}
}

// TestALocalCloneIsNoHardLinkStop is a working folder made with git clone --local,
// whose object files are hard links to the source repository's: the check of the
// files under .git does not stop on them, since git writes each object to a temporary
// file and renames it.
func TestALocalCloneIsNoHardLinkStop(t *testing.T) {
	needGit(t)
	src := t.TempDir()
	run(t, src, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(src, "README.md"), "shop\n")
	run(t, src, "add", "-A")
	run(t, src, "commit", "--quiet", "-m", "first")
	dir := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "clone", "--quiet", "--local", src, dir)
	linked := 0
	filepath.WalkDir(filepath.Join(dir, ".git", "objects"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil && linkCount(info) > 1 {
				linked++
			}
		}
		return nil
	})
	if linked == 0 {
		t.Fatal("git clone --local made no hard link under .git/objects, so the test shows nothing")
	}
	if _, err := gitFiles(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("a clone with %d hard-linked object files: %v", linked, err)
	}
	if err := qoryFiles(dir); err != nil {
		t.Errorf("the clone's .qory: %v", err)
	}
}

// TestAnEmptyFolderBehindALinkStops is a working folder that is a link to a folder with no
// entries: the run stops, and git init does not run through the link.
func TestAnEmptyFolderBehindALinkStops(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := prepare(context.Background(), link, env("git.example.com", "acme/shop"))
	if err == nil || err.Error() != textNotTree {
		t.Errorf("%v, want %s", err, textNotTree)
	}
	if _, err := os.Lstat(filepath.Join(target, ".git")); !os.IsNotExist(err) {
		t.Errorf("the link's target has a .git: %v", err)
	}
}

// TestAnEmptyFolderGitCannotWriteStops is a folder with no entries and no write
// permission: the line starts with the fix and contains git's line.
func TestAnEmptyFolderGitCannotWriteStops(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only folder")
	}
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	want := "qory run: check that this machine can read and write the working folder, then run again. The step failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || len(err.Error()) == len(want) {
		t.Errorf("%v, want a line that starts %s", err, want)
	}
}
