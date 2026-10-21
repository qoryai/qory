package hostgit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
)

// linkedGit writes target/bin/git, a git that touches target/ran and then runs the git
// the PATH finds, and puts first on the PATH a folder outside target whose git is a
// link to it. It returns the PATH it set.
func linkedGit(t *testing.T, target string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on the PATH")
	}
	bin := filepath.Join(target, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ntouch '" + filepath.Join(target, "ran") + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(bin, "git"), filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	path := dir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	return path
}

// ranIn reports whether the git linkedGit wrote under target ran.
func ranIn(t *testing.T, target string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(target, "ran"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// runBoth runs git status through Command in repo, and git version through Unchecked
// with path as its PATH.
func runBoth(t *testing.T, repo, path string) {
	t.Helper()
	ctx := context.Background()
	c, err := hostgit.Command(ctx, repo, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Command: %v\n%s", err, out)
	}
	u, err := hostgit.Unchecked(ctx, t.TempDir(), []string{"PATH=" + path}, "version")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := u.CombinedOutput(); err != nil {
		t.Fatalf("Unchecked: %v\n%s", err, out)
	}
}

// TestALinkedGitIntoAPlaceIsSkipped puts first on the PATH a folder outside every place
// whose git is a link to a git in a place a walled run used: neither Command nor
// Unchecked runs it, and each runs the git of a later PATH folder.
func TestALinkedGitIntoAPlaceIsSkipped(t *testing.T) {
	hermetic(t)
	place, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), place, places.KindMount, "run-1"); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	path := linkedGit(t, place)
	runBoth(t, repo, path)
	if ranIn(t, place) {
		t.Error("the git a link leads to in a recorded place ran")
	}
}

// TestALinkedGitOutsideEveryPlaceRuns puts first on the PATH a git that is a link to a
// git outside every place: Command and Unchecked run it.
func TestALinkedGitOutsideEveryPlaceRuns(t *testing.T) {
	hermetic(t)
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	path := linkedGit(t, target)
	ctx := context.Background()
	c, err := hostgit.Command(ctx, repo, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Command: %v\n%s", err, out)
	}
	if !ranIn(t, target) {
		t.Error("Command: the linked git outside every place did not run")
	}
	if err := os.Remove(filepath.Join(target, "ran")); err != nil {
		t.Fatal(err)
	}
	u, err := hostgit.Unchecked(ctx, t.TempDir(), []string{"PATH=" + path}, "version")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := u.CombinedOutput(); err != nil {
		t.Fatalf("Unchecked: %v\n%s", err, out)
	}
	if !ranIn(t, target) {
		t.Error("Unchecked: the linked git outside every place did not run")
	}
}

// TestALinkedGitIntoAPlaceAloneFallsBack has on the PATH only a folder whose git is a
// link into a place a walled run used: Command passes it over and runs the git of
// /usr/bin or /bin.
func TestALinkedGitIntoAPlaceAloneFallsBack(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/git"); err != nil {
		if _, err := exec.LookPath("/bin/git"); err != nil {
			t.Skip("no git in /usr/bin or /bin")
		}
	}
	hermetic(t)
	place, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), place, places.KindMount, "run-1"); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	path := linkedGit(t, place)
	alone, _, _ := strings.Cut(path, string(os.PathListSeparator))
	t.Setenv("PATH", alone)
	c, err := hostgit.Command(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Command: %v\n%s", err, out)
	}
	if dir := filepath.Dir(c.Path); dir != "/usr/bin" && dir != "/bin" {
		t.Errorf("Command ran %s, want the git of /usr/bin or /bin", c.Path)
	}
	if ranIn(t, place) {
		t.Error("the git a link leads to in a recorded place ran")
	}
}

// TestAGitLinkRepointedAfterTheCheckDoesNotRun puts first on the PATH a folder outside
// every place whose git is a link to a link inside a place a walled run used, which
// leads to git outside every place. After Command and Unchecked made git, the link in
// the place is pointed at a file in the place, as the walled run could: neither runs
// that file.
func TestAGitLinkRepointedAfterTheCheckDoesNotRun(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on the PATH")
	}
	if real, err = filepath.EvalSymlinks(real); err != nil {
		t.Fatal(err)
	}
	hermetic(t)
	repo := newRepo(t)
	place, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), place, places.KindMount, "run-1"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	evil := filepath.Join(place, "evil")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(place, "git-out")
	if err := os.Symlink(real, inside); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(inside, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	path := dir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	repoint := func() {
		t.Helper()
		if err := os.Remove(inside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(evil, inside); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	c, err := hostgit.Command(ctx, repo, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	repoint()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("Command: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("Command ran the file the link in the place was pointed at after the check")
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, inside); err != nil {
		t.Fatal(err)
	}
	u, err := hostgit.Unchecked(ctx, t.TempDir(), []string{"PATH=" + path}, "version")
	if err != nil {
		t.Fatal(err)
	}
	repoint()
	if out, err := u.CombinedOutput(); err != nil {
		t.Fatalf("Unchecked: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("Unchecked ran the file the link in the place was pointed at after the check")
	}
}

// TestUnreadableRecordsRunOnlyTheSystemGit makes the records of places unreadable: the
// git that a link leads to in a place does not run, since git then gets only
// /usr/bin:/bin, and gitIn does not check a git against the records it cannot read.
func TestUnreadableRecordsRunOnlyTheSystemGit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 0")
	}
	hermetic(t)
	place, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), place, places.KindMount, "run-1"); err != nil {
		t.Fatal(err)
	}
	path := linkedGit(t, place)
	dir, err := places.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	places.Forget()
	if _, err := places.Cached(); err == nil {
		t.Fatal("the records are readable")
	}
	u, err := hostgit.Unchecked(context.Background(), t.TempDir(), []string{"PATH=" + path}, "version")
	if err != nil {
		t.Fatal(err)
	}
	if d := filepath.Dir(u.Path); d != "/usr/bin" && d != "/bin" && u.Err == nil {
		t.Errorf("Unchecked takes %s, want a git of /usr/bin or /bin", u.Path)
	}
	_ = u.Run()
	if ranIn(t, place) {
		t.Error("the git a link leads to in a recorded place ran")
	}
}
