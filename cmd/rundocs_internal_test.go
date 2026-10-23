package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/config"
)

// TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet is the line of a run from a run code
// whose time limit came before the runtime started: its fix is a longer limit in Qory
// Apiary or in the workflow, never --timeout, which such a run does not take.
func TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet(t *testing.T) {
	r := &codeRun{workflow: "example", stack: "https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example"}
	want := "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the stack https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example: the run was cancelled at its time limit of 45m0s, before claude started"
	if got := r.timedOutLine(45*time.Minute, "claude"); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestAWalledRunOfANamedStackMountsNothingOfQorysCacheFolder is a walled run from a run
// code whose stack is a name, with a mount from wall.mounts or --mount: a mount that is
// qory's folder in the cache directory, contains it, lies inside it, or is a link to it
// or to a folder above it stops the run, exit 2, before anything is fetched; qory
// writes the harness reports of runs from a run code there. Another mount, and a run of
// a stack reference, pass.
func TestAWalledRunOfANamedStackMountsNothingOfQorysCacheFolder(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	inside := filepath.Join(cache, "homes", "app-1a2b3c4d")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	link, above := filepath.Join(other, "cache"), filepath.Join(other, "above")
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(base), above); err != nil {
		t.Fatal(err)
	}
	named := &codeRun{stack: "example"}
	reference := &codeRun{stack: "https://code.example.com/team/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example"}
	if !named.namedStack() || reference.namedStack() {
		t.Fatalf("namedStack: %v for a name and %v for a reference, want true and false", named.namedStack(), reference.namedStack())
	}
	for _, c := range []struct{ path, how string }{
		{cache, "is"}, {base, "contains"}, {filepath.Dir(base), "contains"}, {inside, "lies inside"}, {link, "is"}, {above, "contains"},
	} {
		for _, from := range []string{"wall.mounts in forager.yaml", "--mount"} {
			err := named.cacheMounted([]givenMount{{path: other, from: "--mount"}, {path: c.path, from: from}})
			want := "remove " + c.path + " from " + from + ": it " + c.how + " " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and a walled run of a named stack mounts no part of " + cache + "; the code is used"
			if err == nil || err.Error() != want {
				t.Errorf("the mount %s from %s: got %v\nwant %s", c.path, from, err, want)
			} else if code := ExitCode(err); code != 2 {
				t.Errorf("the mount %s from %s: exit %d, want 2", c.path, from, code)
			}
			if err := reference.cacheMounted([]givenMount{{path: c.path, from: from}}); err != nil {
				t.Errorf("a stack reference with the mount %s: %v, want none", c.path, err)
			}
		}
	}
	if err := named.cacheMounted([]givenMount{{path: other, from: "--mount"}, {path: filepath.Join(base, "other"), from: "--mount"}}); err != nil {
		t.Errorf("mounts outside %s: %v, want none", cache, err)
	}
}

// TestAMountInAnotherLetterCaseOfQorysCacheFolderStops is a mount of a folder above
// qory's cache folder, and of the folder itself, written in another letter case, on a
// disk that ignores case, as APFS does by default on macOS: each is the same folder, so
// it stops the run. On a disk that keeps case apart, the test is skipped.
func TestAMountInAnotherLetterCaseOfQorysCacheFolderStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(filepath.Dir(base), strings.ToUpper(filepath.Base(base)))
	if upper == base {
		upper = filepath.Join(filepath.Dir(base), strings.ToLower(filepath.Base(base)))
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skipf("the disk of %s keeps letter case apart: %v", base, err)
	}
	named := &codeRun{stack: "example"}
	for _, c := range []struct{ path, how string }{
		{upper, "contains"}, {filepath.Join(upper, "QORY"), "is"}, {filepath.Join(upper, "Qory", "Homes"), "lies inside"},
	} {
		err := named.cacheMounted([]givenMount{{path: c.path, from: "--mount"}})
		want := "remove " + c.path + " from --mount: it " + c.how + " " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and a walled run of a named stack mounts no part of " + cache + "; the code is used"
		if err == nil || err.Error() != want {
			t.Errorf("the mount %s: got %v\nwant %s", c.path, err, want)
		}
	}
}

// TestEncloseListsTheMountsOfWallMountsAndMount is the mounts a walled run's guard of
// qory's cache folder checks: those of wall.mounts and --mount, each with where it is
// set, and not the checkout or the home, which is inside that folder for a run from a
// run code.
func TestEncloseListsTheMountsOfWallMountsAndMount(t *testing.T) {
	fromFile, fromFlag := t.TempDir(), t.TempDir()
	r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux", Mounts: []config.ForagerMount{{Path: fromFile, ReadOnly: true}}}}
	var spec runSpec
	if err := enclose(&spec, r, wallOptions{mounts: []string{fromFlag}}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	want := []givenMount{{path: fromFile, from: "wall.mounts in forager.yaml"}, {path: fromFlag, from: "--mount"}}
	if !reflect.DeepEqual(spec.given, want) {
		t.Errorf("given %v, want %v", spec.given, want)
	}
	var unwalled runSpec
	if err := enclose(&unwalled, &config.Forager{}, wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if unwalled.given != nil {
		t.Errorf("without a wall, given %v, want none", unwalled.given)
	}
}
