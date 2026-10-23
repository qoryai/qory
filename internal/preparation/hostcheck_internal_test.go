package preparation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
)

// isolatedPlaces keeps the records of the places a walled run used in a folder of the
// test alone, read afresh.
func isolatedPlaces(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	hostgit.Forget()
	places.Forget()
	t.Cleanup(func() { hostgit.Forget(); places.Forget() })
}

// smudging plants in dir's repository a filter whose smudge program writes marker, and
// attributes that give every file the filter, as a walled run could.
func smudging(t *testing.T, dir, marker string) {
	t.Helper()
	program := filepath.Join(t.TempDir(), "smudge")
	write(t, program, "#!/bin/sh\necho smudge >> '"+marker+"'\ncat\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "config", "filter.x.smudge", program)
	write(t, filepath.Join(dir, ".git", "info", "attributes"), "* filter=x\n")
}

// linkOut moves path to a new folder outside the working folder and puts a link to it
// in its place, as a run could, and returns where it went.
func linkOut(t *testing.T, path string) string {
	t.Helper()
	moved := filepath.Join(t.TempDir(), filepath.Base(path))
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, path); err != nil {
		t.Fatal(err)
	}
	return moved
}

// filesUnder is how many regular files are under dir.
func filesUnder(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// fetchedNothing fails the test when dir's repository has a ref or marker exists.
func fetchedNothing(t *testing.T, name, dir, marker string) {
	t.Helper()
	if out, _ := exec.Command("git", "-C", dir, "for-each-ref").Output(); len(out) != 0 {
		t.Errorf("%s: refs were fetched: %s", name, out)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("%s: a program of the repository's configuration ran: %v\n%s", name, err, got)
	}
}

// TestStopsOnAWorkingFolderGitWouldActOn is a working folder, in no place a walled run
// used, whose .git a run could have written: a filter that runs a program on checkout,
// a proxy for origin, a check of the host's certificate turned off, an include, a
// config.worktree, a commondir, a config that is a link or a folder, a link anywhere
// under .git, and alternates. The fetch stops on each before git fetches, with a line
// that starts with its fix, no program runs, and git writes nothing through a link.
func TestStopsOnAWorkingFolderGitWouldActOn(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", map[string]string{"README.md": "shop\n"})
	const configKey = "qory run: start the run in a new empty git working tree, or remove %s from .git/%s, then run again. qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify"
	for _, c := range []struct {
		name  string
		plant func(dir, marker string)
		want  string
	}{
		{"a filter", func(dir, marker string) { smudging(t, dir, marker) }, strings.Replace(strings.Replace(configKey, "%s", "filter.x.smudge", 1), "%s", "config", 1)},
		{"a proxy for origin", func(dir, _ string) { run(t, dir, "config", "remote.origin.proxy", "http://127.0.0.1:9") }, textHasRemote},
		{"no certificate check", func(dir, _ string) { run(t, dir, "config", "http.sslVerify", "false") }, strings.Replace(strings.Replace(configKey, "%s", "http.sslverify", 1), "%s", "config", 1)},
		{"an include", func(dir, _ string) { run(t, dir, "config", "include.path", "/dev/null") }, strings.Replace(strings.Replace(configKey, "%s", "include.path", 1), "%s", "config", 1)},
		{"a config.worktree", func(dir, _ string) {
			write(t, filepath.Join(dir, ".git", "config.worktree"), "[core]\n\tbare = false\n")
		}, strings.Replace(strings.Replace(configKey, "%s", "core.bare", 1), "%s", "config.worktree", 1)},
		{"a commondir", func(dir, _ string) {
			write(t, filepath.Join(dir, ".git", "commondir"), t.TempDir()+"\n")
		}, textCommonDir},
		{"a linked config", func(dir, _ string) { linkOut(t, filepath.Join(dir, ".git", "config")) }, strings.Replace(textGitLink, "%s", ".git/config", 1)},
		{"a config that is a folder", func(dir, _ string) {
			config := filepath.Join(dir, ".git", "config")
			if err := os.Rename(config, filepath.Join(t.TempDir(), "config")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(config, 0o755); err != nil {
				t.Fatal(err)
			}
		}, strings.Replace(textConfigNotFile, "%s", ".git/config", 1)},
		{"linked objects", func(dir, marker string) {
			outside := linkOut(t, filepath.Join(dir, ".git", "objects"))
			write(t, marker+".outside", outside)
		}, strings.Replace(textGitLink, "%s", ".git/objects", 1)},
		{"linked refs", func(dir, _ string) { linkOut(t, filepath.Join(dir, ".git", "refs")) }, strings.Replace(textGitLink, "%s", ".git/refs", 1)},
		{"a link deep inside", func(dir, _ string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".git", "refs", "heads", "main")); err != nil {
				t.Fatal(err)
			}
		}, strings.Replace(textGitLink, "%s", ".git/refs/heads/main", 1)},
		{"alternates", func(dir, _ string) {
			write(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), t.TempDir()+"\n")
		}, strings.Replace(textAlternates, "%s", ".git/objects/info/alternates", 1)},
		{"http alternates", func(dir, _ string) {
			write(t, filepath.Join(dir, ".git", "objects", "info", "http-alternates"), "https://elsewhere.invalid/objects\n")
		}, strings.Replace(textAlternates, "%s", ".git/objects/info/http-alternates", 1)},
	} {
		dir := emptyFolder(t)
		marker := filepath.Join(t.TempDir(), "ran")
		c.plant(dir, marker)
		if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop")); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.name, err, c.want)
		}
		fetchedNothing(t, c.name, dir, marker)
		if outside, err := os.ReadFile(marker + ".outside"); err == nil {
			if n := filesUnder(t, string(outside)); n != 0 {
				t.Errorf("%s: git wrote %d files outside the working folder", c.name, n)
			}
		}
	}
	// The same folder with no key planted is fetched into, and its checkout runs no
	// filter of the remote's attributes alone.
	dir := emptyFolder(t)
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop")); err != nil {
		t.Fatal(err)
	}
}

// TestStopsOnARecordedPlaceWithAFilterKey is a working folder a walled run used, whose
// repository's configuration has a filter key the run wrote after the place was
// recorded: hostgit's settings check stops the fetch before git runs there, and the line
// starts with its fix and has the key.
func TestStopsOnARecordedPlaceWithAFilterKey(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", map[string]string{"README.md": "shop\n"})
	dir, err := filepath.EvalSymlinks(emptyFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), dir, places.KindCheckout, "run-1"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	smudging(t, dir, marker)
	_, err = prepare(context.Background(), dir, env("github.com", "acme/shop"))
	want := "qory run: start the run in a new empty git working tree that no walled run used, then run again. The check of the working folder's git configuration failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "filter.x.smudge") {
		t.Errorf("%v\nwant %s… with filter.x.smudge", err, want)
	}
	fetchedNothing(t, "a recorded place", dir, marker)
}

// TestAKeyWithAPasswordIsShownWithoutIt is a working folder whose configuration has
// http.<url>.sslVerify for a URL with a password: the line has the key without the
// password, and the rest of the key.
func TestAKeyWithAPasswordIsShownWithoutIt(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	dir := emptyFolder(t)
	config := filepath.Join(dir, ".git", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	write(t, config, string(data)+"[http \"https://u:sekritpw@h.example/\"]\n\tsslVerify = false\n")
	_, err = prepare(context.Background(), dir, env("github.com", "acme/shop"))
	if err == nil || strings.Contains(err.Error(), "sekritpw") {
		t.Fatalf("%v, want a stop without the password", err)
	}
	for _, part := range []string{"or remove http.https://", "h.example/.sslverify from .git/config"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("%v\nwant %q in it", err, part)
		}
	}
}
