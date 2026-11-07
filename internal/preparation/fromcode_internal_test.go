package preparation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/branchformat"
)

// TestAStopHasALineForEachKindOfRun is each stop of the fetch that says "run again", with
// a value for each of its verbs: a run with no code has the line as it is, a run from a
// run code sends the person to a new take-in, and a prebuild from a run code to a new run
// code. Each line ends with the code being used.
func TestAStopHasALineForEachKindOfRun(t *testing.T) {
	const (
		takeIn = "take the work item in again in Qory Apiary"
		used   = "; the code is used"
		tree   = "start the run in a new empty git working tree"
		newRun = "start the run again with a new run code"
	)
	for _, c := range []struct {
		text              string
		args              []any
		plain, code, pool string
	}{
		{textNotFetched, []any{"h", "p", "p", "c"},
			"add a credential for h that reads p to the run configuration, then run again. The fetch of p through the run's gateway failed: c",
			"add a credential for h that reads p to the run configuration, then " + takeIn + ": the fetch of p through the run's gateway failed: c" + used,
			"add a credential for h that reads p to the run configuration, then " + newRun + ": the fetch of p through the run's gateway failed: c" + used},
		{textNotReached, []any{"h", "p", "c"},
			"check that the run's policy allows h and that h is reachable, then run again. The fetch of p through the run's gateway failed: c",
			"check that the run's policy allows h and that h is reachable, then " + takeIn + ": the fetch of p through the run's gateway failed: c" + used,
			"check that the run's policy allows h and that h is reachable, then " + newRun + ": the fetch of p through the run's gateway failed: c" + used},
		{textNoBranch, []any{"p", "h", "c"},
			"give p on h a default branch with a commit, then run again. The fetch through the run's gateway failed: c",
			"give p on h a default branch with a commit, then " + takeIn + ": the fetch through the run's gateway failed: c" + used,
			"give p on h a default branch with a commit, then " + newRun + ": the fetch through the run's gateway failed: c" + used},
		{textTreeFailed, []any{"c"},
			"check git on this machine, the working folder's .git, and each repository and .git above it, then run again. git failed: c",
			"check git on this machine, the working folder's .git, and each repository and .git above it, then " + takeIn + ": git failed: c" + used,
			"check git on this machine, the working folder's .git, and each repository and .git above it, then " + newRun + ": git failed: c" + used},
		{textAboveFailed, []any{"c"},
			"check git on this machine, the working folder, and each repository and .git above it, then run again. git failed: c",
			"check git on this machine, the working folder, and each repository and .git above it, then " + takeIn + ": git failed: c" + used,
			"check git on this machine, the working folder, and each repository and .git above it, then " + newRun + ": git failed: c" + used},
		{textGitFailed, []any{"c"},
			"check that git on this machine can read the working folder, then run again. git failed: c",
			"check that git on this machine can read the working folder, then " + takeIn + ": git failed: c" + used,
			"check that git on this machine can read the working folder, then " + newRun + ": git failed: c" + used},
		{textLocal, []any{"c"},
			"check that this machine can read and write the working folder, then run again. The step failed: c",
			"check that this machine can read and write the working folder, then " + takeIn + ": the step failed: c" + used,
			"check that this machine can read and write the working folder, then " + newRun + ": the step failed: c" + used},
		{textConfigKey, []any{"k", "f"},
			tree + ", or remove k from f, then run again. qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify",
			takeIn + ", and start its run in a new empty git working tree, or remove k from f first: qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify" + used,
			newRun + ", in a new empty git working tree, or remove k from f first: qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify" + used},
		{textConfigNotFile, []any{"f"},
			tree + ", then run again: the working folder's f is not a regular file, and qory fetches only into a repository whose configuration it can read",
			takeIn + ", and start its run in a new empty git working tree: the working folder's f is not a regular file, and qory fetches only into a repository whose configuration it can read" + used,
			newRun + ", in a new empty git working tree: the working folder's f is not a regular file, and qory fetches only into a repository whose configuration it can read" + used},
		{textAlternates, []any{"f"},
			tree + ", then run again: the working folder's .git has f, which makes git read objects from outside the working folder",
			takeIn + ", and start its run in a new empty git working tree: the working folder's .git has f, which makes git read objects from outside the working folder" + used,
			newRun + ", in a new empty git working tree: the working folder's .git has f, which makes git read objects from outside the working folder" + used},
		{textGitLink, []any{"l"},
			tree + ", or remove the link l, then run again: git would read and write through that link",
			takeIn + ", and start its run in a new empty git working tree, or remove the link l first: git would read and write through that link" + used,
			newRun + ", in a new empty git working tree, or remove the link l first: git would read and write through that link" + used},
		{textHardLink, []any{"l"},
			tree + ", or remove the hard link l, then run again: a write to it would change another file",
			takeIn + ", and start its run in a new empty git working tree, or remove the hard link l first: a write to it would change another file" + used,
			newRun + ", in a new empty git working tree, or remove the hard link l first: a write to it would change another file" + used},
		{textGrafts, nil,
			tree + ", then run again: the working folder's .git has info/grafts, which gives commits other parents",
			takeIn + ", and start its run in a new empty git working tree: the working folder's .git has info/grafts, which gives commits other parents" + used,
			newRun + ", in a new empty git working tree: the working folder's .git has info/grafts, which gives commits other parents" + used},
		{textCommonDir, nil,
			tree + ", then run again: the working folder's .git has a commondir file, which can point git at another repository",
			takeIn + ", and start its run in a new empty git working tree: the working folder's .git has a commondir file, which can point git at another repository" + used,
			newRun + ", in a new empty git working tree: the working folder's .git has a commondir file, which can point git at another repository" + used},
	} {
		err := lineOf(c.text, c.args...)
		for _, form := range []struct {
			at   Target
			want string
		}{
			{Target{}, c.plain},
			{Target{Version: "v"}, c.plain},
			{Target{FromCode: true}, c.code},
			{Target{FromCode: true, Prebuild: true}, c.pool},
		} {
			if got := form.at.stop(err).Error(); got != "qory run: "+form.want {
				t.Errorf("%+v:\n got %q\nwant %q", form.at, got, "qory run: "+form.want)
			}
		}
	}
	if len(fromCodeLines) != 14 {
		t.Errorf("fromCodeLines has %d lines; this test covers 14", len(fromCodeLines))
	}
	// A line with no form for a run from a run code stays, and so does an error of no line.
	for _, err := range []error{lineOf(textNotEmpty), errors.New(textNotEmpty)} {
		if got := (Target{FromCode: true}).stop(err); got != err {
			t.Errorf("%v became %v", err, got)
		}
	}
	if (Target{FromCode: true}).stop(nil) != nil {
		t.Error("no stop became a stop")
	}
}

// TestAFetchFromARunCodeStopsWithTheLineOfAUsedCode is [RunAt] for a run from a run code,
// with no version and with one: a repository the host does not have, git that fails to
// read the folder's repository through the stop of [safeRepository], and a failure on
// the machine. Each line sends the person to a new take-in; the same stop of a run from
// no run code keeps its line.
func TestAFetchFromARunCodeStopsWithTheLineOfAUsedCode(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	first := run(t, filepath.Join(base, "acme", "shop.git"), "rev-parse", "main")
	const (
		noRepository = "add a credential for git.example.com that reads acme/gone to the run configuration, then "
		fetchEnd     = ": the fetch of acme/gone through the run's gateway failed: "
	)
	fetch := func(at Target, list string, dir string) error {
		_, err := fetchListAt(dir, list, at)
		return err
	}
	shop := Target{System: "git.example.com", Path: "acme/shop"}
	code := shop
	code.FromCode = true
	atCommit := code
	atCommit.Version = first
	for name, at := range map[string]Target{"no version": code, "a version": atCommit} {
		err := fetch(at, "acme/shop,acme/gone", emptyFolder(t))
		if err == nil || !strings.HasPrefix(err.Error(), "qory run: "+noRepository+"take the work item in again in Qory Apiary"+fetchEnd) || !strings.HasSuffix(err.Error(), "; the code is used") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := fetch(shop, "acme/shop,acme/gone", emptyFolder(t)); err == nil || !strings.HasPrefix(err.Error(), "qory run: "+noRepository+"run again. The fetch of acme/gone") {
		t.Errorf("no run code: %v", err)
	}
	common := emptyFolder(t)
	write(t, filepath.Join(common, ".git", "commondir"), t.TempDir()+"\n")
	want := "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree: the working folder's .git has a commondir file, which can point git at another repository; the code is used"
	if err := fetch(code, "acme/shop", common); err == nil || err.Error() != want {
		t.Errorf("a commondir file: %v", err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	err := fetch(code, "acme/shop", gone)
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: check that this machine can read and write the working folder, then take the work item in again in Qory Apiary: the step failed: ") || !strings.HasSuffix(err.Error(), "; the code is used") {
		t.Errorf("no working folder: %v", err)
	}
}

// TestASwitchThatFailsHasTheLineOfAUsedCode is a work branch, which only a run from a run
// code has, whose target commit the repository does not have: the check of .qory stops,
// and the line sends the person to a new take-in.
func TestASwitchThatFailsHasTheLineOfAUsedCode(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	head := run(t, dir, "rev-parse", "HEAD")
	b.Ref = "qory/other"
	b.Version = []branchformat.Repository{{System: "git.example.com", Path: "acme/shop", Commit: head}}
	b.Repositories[0].Commit = strings.Repeat("1", 40)
	_, err := switchTo(dir, b)
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: check that git on this machine can read the working folder, then take the work item in again in Qory Apiary: git failed: ") || !strings.HasSuffix(err.Error(), "; the code is used") {
		t.Errorf("%v", err)
	}
}
