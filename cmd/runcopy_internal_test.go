package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes"

	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/runcopy"
)

// TestAFetchPastItsLimitHasItsLine is a fetch that ran past the limit: the line has the fetch,
// and a stop keeps the error for the line of the stop.
func TestAFetchPastItsLimitHasItsLine(t *testing.T) {
	limited, cancel := context.WithCancel(context.Background())
	cancel()
	failed := errors.New("the fetch failed")
	err := limitErr(context.Background(), limited, textCopyFetchLimit, copyStepLimit, failed)
	want := "the fetch of the run's commits ran past its limit of " + copyStepLimit.String()
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
	stop, cancelStop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelStop()
	<-stop.Done()
	if err := limitErr(stop, limited, textCopyFetchLimit, copyStepLimit, failed); !errors.Is(err, failed) {
		t.Errorf("after a stop the error is %v", err)
	}
}

// TestTheCheckLimitGrowsWithTheBundles is the limit of the check and the fetch: the base under
// 1 GiB, a minute more for each full GiB, and at most an hour.
func TestTheCheckLimitGrowsWithTheBundles(t *testing.T) {
	for _, c := range []struct {
		size int64
		want time.Duration
	}{
		{0, copyStepLimit},
		{1<<30 - 1, copyStepLimit},
		{1 << 30, copyStepLimit + time.Minute},
		{runcopy.MaxBundle, copyStepLimit + 8*time.Minute},
		{100 << 30, maxStepLimit},
	} {
		if got := stepLimitFor(c.size); got != c.want {
			t.Errorf("the limit for %d bytes is %s, want %s", c.size, got, c.want)
		}
	}
}

// TestASignalInTheCheckOrFetchHasItsLine is a stop that ended the check of a run's commits or
// their fetch, which share the line: the line is true for both.
func TestASignalInTheCheckOrFetchHasItsLine(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	c := &runCopy{cutBy: func() string { return "qory run got a second signal" }}
	err := c.checkFailed(stopped, errors.New("the fetch failed"))
	want := "qory run got a second signal during the check or the fetch of the run's commits, so those commits did not come back"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
}

// TestALongCauseKeepsNoPartOfAPathOfTheCopy is a cause past the cut of [integration.Printable],
// in a long cache folder, with a path of the copy twice: the step's log has no part of a path of
// the copy, and the line on the terminal keeps the path.
func TestALongCauseKeepsNoPartOfAPathOfTheCopy(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-cache-folder/", 12))
	t.Setenv("HOME", long)
	t.Setenv("XDG_CACHE_HOME", long)
	dir, err := runcopy.New("01a1b738-63b1-76fa-9757-6552cfc987b5")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Remove(); err != nil {
			t.Error(err)
		}
	})
	cause := errors.New("mkdir " + dir.Bundle() + ": no space left on device, and " + dir.Bundle() + " stays")
	const logged = "mkdir the run's copy/bundle: no space left on device, and the run's copy/bundle stays"
	under := errors.New("stat " + filepath.Join(dir.Work(), "sub") + ": no such file")
	for _, c := range []struct {
		name   string
		logged string
		stop   func(*runCopy) *copyStop
	}{
		{"no session", logged, func(c *runCopy) *copyStop { return c.noSession(context.Background(), cause) }},
		{"another cause", logged, func(c *runCopy) *copyStop { return c.stoppedBy("%s", cause) }},
		{"a path under the working folder", "stat the run's copy/sub: no such file", func(c *runCopy) *copyStop { return c.stoppedBy("%s", under) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			stop := c.stop(&runCopy{dir: dir})
			if !strings.Contains(stop.log, c.logged) {
				t.Errorf("the log's line is %q, want %q in it", stop.log, c.logged)
			}
			if strings.Contains(stop.log, "a-long-cache-folder") || strings.Contains(stop.log, "qory/runs") {
				t.Errorf("the log's line has a part of a path: %q", stop.log)
			}
			if !strings.Contains(stop.Error(), dir.Work()) || !strings.Contains(stop.Error(), "...") {
				t.Errorf("the line is %q, want the path of the copy and the cut", stop.Error())
			}
		})
	}
}

// TestAnErrorOfTheCopyKeepsNoPartOfItsPath is a real error of [runcopy.Dir.GitSession], whose path
// is already cut or has its characters replaced: a bundle folder that is there, in a cache folder
// past the cut and in one with a tab in its name. The step's log has no part of the path.
func TestAnErrorOfTheCopyKeepsNoPartOfItsPath(t *testing.T) {
	for _, c := range []struct{ name, cache, part string }{
		{"a long cache folder", strings.Repeat("a-long-cache-folder/", 12), "a-long-cache-folder"},
		{"a tab in the cache folder", "a-cache\tfolder", "a-cache"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), c.cache)
			t.Setenv("HOME", cache)
			t.Setenv("XDG_CACHE_HOME", cache)
			dir, err := runcopy.New("01a1b738-63b1-76fa-9757-6552cfc987b5")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := dir.Remove(); err != nil {
					t.Error(err)
				}
			})
			if err := os.Mkdir(dir.Bundle(), 0o700); err != nil {
				t.Fatal(err)
			}
			_, err = dir.GitSession(context.Background(), "main", "message", nil, nil)
			if err == nil {
				t.Fatal("the session has a bundle folder that is there, and no error")
			}
			stop := (&runCopy{dir: dir}).noSession(context.Background(), err)
			if !strings.Contains(stop.log, textCopyFolder) || strings.Contains(stop.log, c.part) || strings.Contains(stop.log, "qory/runs") {
				t.Errorf("the log's line is %q, from the error %q", stop.log, err)
			}
		})
	}
}

// TestALineOfGitKeepsNoPartOfAPathOfTheCopy is a real line of git with a path of the copy that
// reaches past the cut of [integration.Printable], in a long cache folder: git clone into a
// folder of the copy that is not empty. The step's log has no part of the path, and the line
// that no cut follows has git's line cut as before.
func TestALineOfGitKeepsNoPartOfAPathOfTheCopy(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-cache-folder/", 12))
	t.Setenv("HOME", long)
	t.Setenv("XDG_CACHE_HOME", long)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com",
			"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	// The commit has a file in the folder of a further repository, so the clone of that
	// repository finds its folder in the copy not empty.
	checkout := t.TempDir()
	further := filepath.Join(checkout, ".qory", "repositories", "x")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(further, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(checkout, "init", "--quiet", "-b", "main")
	git(checkout, "add", ".")
	git(checkout, "commit", "--quiet", "-m", "first")
	if err := os.WriteFile(filepath.Join(further, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(further, "init", "--quiet", "-b", "main")
	git(further, "add", "b.txt")
	git(further, "commit", "--quiet", "-m", "first")
	dir, err := runcopy.New("01a1b738-63b1-76fa-9757-6552cfc987b5")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Remove(); err != nil {
			t.Error(err)
		}
	})
	_, err = dir.Clone(context.Background(), checkout, "qory/run-1")
	line := errors.Unwrap(err)
	if line == nil || !strings.HasPrefix(line.Error(), "git clone: fatal: destination path ") {
		t.Fatalf("the clone's error is %v, want git's line", err)
	}
	stop := (&runCopy{dir: dir}).stoppedBy("%s", line)
	if want := "destination path 'the run's copy/.qory/repositories/x' already exists"; !strings.Contains(stop.log, want) {
		t.Errorf("the log's line is %q, want %q in it", stop.log, want)
	}
	if strings.Contains(stop.log, "a-long-cache-folder") || strings.Contains(stop.log, "qory/runs") {
		t.Errorf("the line is %q and the log's line %q, from the error %q", stop.Error(), stop.log, line)
	}
	shown := runcopy.ShownError(line).Error()
	if want := "git clone: " + integration.Printable(strings.TrimPrefix(line.Error(), "git clone: ")); shown != want || !strings.HasSuffix(shown, "...") {
		t.Errorf("the line that no cut follows is %q, want %q", shown, want)
	}
}

// TestNowhereKeepsTheRulesOfACopyStep is the fake run against Forager's rules, which the tests of
// the copy step depend on: an agent's session joins in no host step, a bare one only in a copy
// step, and during a copy step HostProxy, Setup and RemoveSetup return an error.
func TestNowhereKeepsTheRulesOfACopyStep(t *testing.T) {
	ctx := context.Background()
	_, _, restore := OpenNowhereWith(Nowhere{})
	defer restore()
	r, err := openRun(ctx, session.OpenSpec{RunID: "run-01"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := r.StartStep(event.StepPush, "push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{}); err == nil || errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("a bare join during a push step: %v", err)
	}
	if _, err := r.HostProxy(); err != nil {
		t.Errorf("HostProxy during a push step: %v", err)
	}
	if err := step.Exit(event.StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	if step, err = r.StartStep(event.StepCopy, textCopyStep); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{Runtime: runtimes.Bare("agent")}); err == nil || errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("an agent's join during a copy step: %v", err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{}); !errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("a bare join during a copy step: %v", err)
	}
	if _, err := r.HostProxy(); err == nil {
		t.Error("HostProxy during a copy step returned no error")
	}
	if err := r.Setup(ctx, session.SetupSpec{}); err == nil {
		t.Error("Setup during a copy step returned no error")
	}
	if err := r.RemoveSetup(ctx); err == nil {
		t.Error("RemoveSetup during a copy step returned no error")
	}
	if err := step.Log([]byte("line\n")); err != nil {
		t.Errorf("Log during the copy step: %v", err)
	}
	if err := step.Exit(event.StateFailed, 128); err != nil {
		t.Errorf("Exit of the copy step: %v", err)
	}
	if _, err := r.HostProxy(); err != nil {
		t.Errorf("HostProxy after the copy step: %v", err)
	}
}

// TestEachLeftoverHasItsLine is a git session that recorded a repository the run made, other work
// git could not add, a large file with a control character, or changes under the setup's ignore
// rules, and nothing past the base commit: each prints its line, none prints that no action is
// needed, and the copy stays. A list that [runcopy.Dir.Left] cut ends with one ... and no dot.
func TestEachLeftoverHasItsLine(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"nested":    {map[string]string{"0.nested": ""}, []string{textCopyBehind}},
		"not added": {map[string]string{"0.left": ""}, []string{textCopyNotAdded}},
		"large": {map[string]string{"0.large": "data/a\x1bb.bin\n"},
			[]string{"left out of the run's commit, as each has more than 100 MiB: data/a?b.bin. They stay in the run's copy."}},
		"all": {map[string]string{"0.nested": "", "0.left": "", "0.large": "x.bin\n"},
			[]string{textCopyBehind, textCopyNotAdded, "more than 100 MiB: x.bin."}},
		"ignored": {map[string]string{"0.left": "", "0.ignored": "cache/a\x1bb\n.cache/x\n"},
			[]string{textCopyNotAdded, textCopyNotAdded + " Changed under the setup's ignore rules: cache/a?b, .cache/x."}},
		"ignored alone": {map[string]string{"0.ignored": "cache/a\n"},
			[]string{textCopyNotAdded, textCopyNotAdded + " Changed under the setup's ignore rules: cache/a."}},
		"ignored cut": {map[string]string{"0.ignored": strings.Repeat("a", 600<<10) + "\n" + strings.Repeat("b", 600<<10) + "\n"},
			[]string{textCopyNotAdded, textCopyNotAdded + " Changed under the setup's ignore rules: " + integration.Printable(strings.Repeat("a", 600<<10)) + ", ..."}},
		"large cut": {map[string]string{"0.large": strings.Repeat("a", 600<<10) + "\n" + strings.Repeat("b", 600<<10) + "\n"},
			[]string{"more than 100 MiB: " + integration.Printable(strings.Repeat("a", 600<<10)) + ", ... They stay in the run's copy."}},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			for _, v := range []string{"HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
				t.Setenv(v, t.TempDir())
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			dir, err := runcopy.New("run-1")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dir.Remove() })
			if err := os.MkdirAll(dir.Bundle(), 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"0.none": ""}
			maps.Copy(files, c.files)
			for f, text := range files {
				if err := os.WriteFile(filepath.Join(dir.Bundle(), f), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cp := &runCopy{dir: dir, root: t.TempDir(), branch: "qory/run-1", back: "qory/run-1", repos: []runcopy.Repository{{}}}
			var lines []string
			if cp.comeBack(context.Background(), "run-1", func(line string) { lines = append(lines, line) }) {
				t.Error("comeBack reports that everything came back")
			}
			out := strings.Join(lines, "\n")
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("the lines lack %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "....") {
				t.Error("a line ends a cut list with four dots")
			}
			for _, text := range []string{textCopyBehind, textCopyNotAdded} {
				if !slices.Contains(c.want, text) && strings.Contains(out, text) {
					t.Errorf("the lines have %q:\n%s", text, out)
				}
			}
			if strings.Contains(out, "no action needed") || !cp.keep || !strings.Contains(out, "then remove the folder") {
				t.Errorf("keep %v, lines:\n%s", cp.keep, out)
			}
		})
	}
}

// TestALongListShowsItsFirstFiftyEntries is a run that leaves 60 large files out of its commit,
// and 60 entries out of the copy back that it could not read or write, that it skipped, or that
// are folders of builds past their limit: each line lists the first 50 and counts the other 10.
func TestALongListShowsItsFirstFiftyEntries(t *testing.T) {
	var files []string
	for i := range 60 {
		files = append(files, "f"+strconv.Itoa(i))
	}
	want := strings.Join(files[:50], ", ") + ", and 10 more"
	if got := sessionPaths(files); got != want {
		t.Errorf("the large files are listed as %q, want %q", got, want)
	}
	var lines []string
	c := &runCopy{dir: runcopy.Dir{}}
	c.keep = true
	c.failed(files, func(line string) { lines = append(lines, line) })
	if wantLine := "left out of the copy back, as qory could not read or write them: " + want + "."; len(lines) != 1 || lines[0] != wantLine {
		t.Errorf("the lines are %q, want %q", lines, wantLine)
	}
	lines = nil
	c.leftOut(files, func(line string) { lines = append(lines, line) })
	if wantLine := "left out of the copy back to your folder: " + want + "."; len(lines) != 1 || lines[0] != wantLine {
		t.Errorf("the left-out lines are %q, want %q", lines, wantLine)
	}
	lines = nil
	c.generated(files, func(line string) { lines = append(lines, line) })
	if wantLine := fmt.Sprintf(textCopyGenerated, runcopy.MaxGeneratedFiles, shownBytes(runcopy.MaxGeneratedBytes), want); len(lines) != 1 || lines[0] != wantLine {
		t.Errorf("the generated-folder lines are %q, want %q", lines, wantLine)
	}
	if got := sessionPaths(files[:50]); got != strings.Join(files[:50], ", ") {
		t.Errorf("50 large files are listed as %q", got)
	}
}

// TestAFailedFetchPrintsEachLineWhole is a fetch that failed in a submodule with paths of 300
// runes: each joined error is a line, git's cause at its end, and only a line of more than
// [maxFetchLine] runes is cut.
func TestAFailedFetchPrintsEachLineWhole(t *testing.T) {
	long := "/" + strings.Repeat("folder/", 43) + "repo"
	one := fmt.Errorf("fetch refs/qory/run-1/* from %s/quarantine/0-0.git by hand: qory fetched the run's commits there, and the fetch into %s/sub in %s/.git/modules/sub failed: %w", long, long, long, errors.New("git fetch: fatal: the cause"))
	two := fmt.Errorf("fetch refs/heads/qory/run-1 from %s/quarantine/0.git into %s by hand: qory fetches the run's commits into your repository only after the fetch into each submodule, and a fetch into a submodule failed", long, long)
	lines := fetchFailedLines(errors.Join(one, two))
	if len(lines) != 2 || lines[0] != one.Error() || lines[1] != two.Error() {
		t.Fatalf("the lines are %q", lines)
	}
	if !strings.HasSuffix(lines[0], "git fetch: fatal: the cause") || len(lines[0]) <= 200 {
		t.Errorf("the line of the submodule's fetch is %q", lines[0])
	}
	cut := fetchFailedLines(errors.New(strings.Repeat("x", maxFetchLine+1)))
	if want := strings.Repeat("x", maxFetchLine) + "..."; len(cut) != 1 || cut[0] != want {
		t.Errorf("a line of %d runes is %q", maxFetchLine+1, cut)
	}
}

// TestJoinedErrorsAreLinesOfTheirOwn is the error of a fetch that failed in a submodule, which
// joins an error per repository: each is a line. Any other error is one line.
func TestJoinedErrorsAreLinesOfTheirOwn(t *testing.T) {
	one, two := errors.New("fetch a by hand: it failed"), errors.New("fetch b by hand: it did not run")
	if got := joinedErrors(errors.Join(one, two)); len(got) != 2 || got[0] != one || got[1] != two {
		t.Errorf("the joined errors are %v", got)
	}
	wrapped := fmt.Errorf("check git: %w", errors.Join(one, two))
	if got := joinedErrors(wrapped); len(got) != 1 || got[0] != wrapped {
		t.Errorf("the errors of a wrapped error are %v", got)
	}
}
