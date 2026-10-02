package cmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// linkedCheckout is the two-modules fixture with the core module linked as harness and a
// harness directory of the checkout's own at that path, committed.
func linkedCheckout(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, linkedStack)
	writeFile(t, filepath.Join(root, "harness", "own.txt"), "mine\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "own harness")
	return root
}

// refusal runs a compose with args that has to be refused for a path qory did not write,
// with the foreign exit status, and returns the refusal's text.
func refusal(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, append([]string{"harness", "compose"}, args...)...)
	if err == nil {
		t.Fatalf("composed over the checkout's own path:\n%s", out)
	}
	if code := cmd.ExitCode(err); code != cmd.ExitForeign {
		t.Fatalf("exit %d, want %d: %v", code, cmd.ExitForeign, err)
	}
	return err.Error()
}

// wantsText fails unless got is want.
func wantsText(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRefusalOfATrackedDirectorySaysForceReplacesIt is the checkout's own harness
// directory, tracked and unmodified, where a module link goes: the refusal names the
// path relative to the checkout and the command that replaces it.
func TestRefusalOfATrackedDirectorySaysForceReplacesIt(t *testing.T) {
	linkedCheckout(t)
	wantsText(t, refusal(t), "harness is a tracked directory, not a link qory wrote.\n"+
		"It has no local changes. To replace it: qory harness compose --force\n"+
		"(git checkout -- harness restores it)")
}

// TestRefusalNamesTheComposeThatReplacedThePath is the harness directory a compose
// replaced under --force and that has been restored since, as a pull does: the refusal
// says the report lists it as replaced, without guessing what restored it.
func TestRefusalNamesTheComposeThatReplacedThePath(t *testing.T) {
	root := linkedCheckout(t)
	if out, err := run(t, "harness", "compose", "--force"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "checkout", "--", "harness")
	wantsText(t, refusal(t), "harness is a tracked directory, not a link qory wrote.\n"+
		"A previous compose replaced it (the report lists it as replaced); it has been restored since.\n"+
		"It has no local changes. To replace it again: qory harness compose --force\n"+
		"(git checkout -- harness restores it)")
}

// TestRefusalNamesTheChangedFiles is the tracked harness directory with a change: the
// refusal says --force would not replace it and why, and names the changed file, with or
// without --force.
func TestRefusalNamesTheChangedFiles(t *testing.T) {
	root := linkedCheckout(t)
	writeFile(t, filepath.Join(root, "harness", "own.txt"), "mine, edited\n")
	wantsText(t, refusal(t), "harness is a tracked directory, not a link qory wrote.\n"+
		"--force would not replace it either: it has uncommitted changes, which git checkout -- would not bring back: harness/own.txt.\n"+
		"Commit or stash the changes, then: qory harness compose --force")
	wantsText(t, refusal(t, "--force"), "harness is a tracked directory, not a link qory wrote.\n"+
		"--force does not replace it: it has uncommitted changes, which git checkout -- would not bring back: harness/own.txt.\n"+
		"Commit or stash the changes, then: qory harness compose --force")
}

// TestRefusalCapsTheFilesItNames is the tracked harness directory with seven files git
// does not track: the refusal names five and counts the rest.
func TestRefusalCapsTheFilesItNames(t *testing.T) {
	root := linkedCheckout(t)
	for i := 1; i <= 7; i++ {
		writeFile(t, filepath.Join(root, "harness", fmt.Sprintf("new%d.txt", i)), "new\n")
	}
	wantsText(t, refusal(t), "harness is a tracked directory, not a link qory wrote.\n"+
		"--force would not replace it either: it contains files git does not track, which git checkout -- would not bring back: "+
		"harness/new1.txt, harness/new2.txt, harness/new3.txt, harness/new4.txt, harness/new5.txt, and 2 more.\n"+
		"Commit them or move them out of harness, then: qory harness compose --force")
}

// TestRefusalOfAnUntrackedDirectorySaysForceRefusesIt is a harness directory git does not
// track: --force would not replace it, since git could not restore it.
func TestRefusalOfAnUntrackedDirectorySaysForceRefusesIt(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, linkedStack)
	writeFile(t, filepath.Join(root, "harness", "own.txt"), "mine\n")
	wantsText(t, refusal(t), "harness is a directory git does not track, not a link qory wrote.\n"+
		"--force would not replace it either: git does not track it, so git checkout -- could not restore it.\n"+
		"Move it out of the way and compose again, or commit it, then: qory harness compose --force")
}

// TestRefusalOfALinkElsewhereNamesItsTarget is a symlink at harness that points somewhere
// else: the refusal names the target, and the untracked link is not one --force replaces.
func TestRefusalOfALinkElsewhereNamesItsTarget(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, linkedStack)
	if err := os.Symlink("docs", filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	wantsText(t, refusal(t), "harness is a link to docs, not a link qory wrote.\n"+
		"--force would not replace it either: git does not track it, so git checkout -- could not restore it.\n"+
		"Move it out of the way and compose again, or commit it, then: qory harness compose --force")
}
