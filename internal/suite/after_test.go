package suite_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

// gitEnv is the environment of the git calls of a test: no configuration file of the machine.
func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir, cmd.Env = dir, gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// workspace is a repository with one commit that has tracked.txt and a committed result file
// junit.xml with a failed test. It returns the folder and the commit.
func workspace(t *testing.T) (dir, first string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "--quiet")
	write(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
	write(t, filepath.Join(dir, "junit.xml"), `<testsuite><testcase name="old"><failure/></testcase></testsuite>`)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--quiet", "-m", "first")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

// commit adds a commit to dir with the file name and content, and returns its id.
func commit(t *testing.T, dir, name, content string) string {
	t.Helper()
	write(t, filepath.Join(dir, name), content)
	git(t, dir, "add", name)
	git(t, dir, "commit", "--quiet", "-m", "more")
	return git(t, dir, "rev-parse", "HEAD")
}

// runScript runs the after command of start and full in dir, with the environment that
// Forager gives After: git's configuration of the machine off, a home folder that nothing
// can write, and git's settings through GIT_CONFIG_COUNT that allow no transport but HTTPS.
// It returns the exit status and the output. The files extra are names and values of
// further variables.
func runScript(t *testing.T, dir, start string, full suite.Full, extra ...string) (int, string) {
	t.Helper()
	words, err := suite.Command(start, full)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o500); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=protocol.allow", "GIT_CONFIG_VALUE_1=never",
		"GIT_CONFIG_KEY_2=protocol.https.allow", "GIT_CONFIG_VALUE_2=always",
	}
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir, cmd.Env = dir, append(env, extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, out.String()
}

func readAll(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(file string) bool {
	_, err := os.Lstat(file)
	return err == nil
}

// TestASessionThatLeftHeadWhereItStartedRunsNoSuite is the skipped end: the script exits 0, the
// status file says skipped, the command does not run, and Judge says Skipped.
func TestASessionThatLeftHeadWhereItStartedRunsNoSuite(t *testing.T) {
	dir, first := workspace(t)
	full := suite.Full{Command: "touch " + filepath.Join(dir, "ran-marker")}
	exit, out := runScript(t, dir, first, full)
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	if got := readAll(t, filepath.Join(dir, suite.StateFolder, "status")); got != "skipped\n" {
		t.Errorf("status %q", got)
	}
	if exists(filepath.Join(dir, "ran-marker")) {
		t.Error("the command ran for a session whose HEAD did not move")
	}
	if got := suite.Judge(dir, full, suite.End{Succeeded: true}).State; got != suite.Skipped {
		t.Errorf("Judge = %q, want skipped", got)
	}
	// A skipped status with a step that did not succeed is no skip.
	if got := suite.Judge(dir, full, suite.End{ExitCode: 1}).State; got != suite.Failed {
		t.Errorf("Judge of a skipped status with exit 1 = %q, want failed", got)
	}
}

// TestASessionWithNoCommitAtStartAndNoneAtEndRunsNoSuite is a workspace with no commit.
func TestASessionWithNoCommitAtStartAndNoneAtEndRunsNoSuite(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	if exit, out := runScript(t, dir, "", suite.Full{Command: "true"}); exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	if got := suite.Judge(dir, suite.Full{}, suite.End{Succeeded: true}).State; got != suite.Skipped {
		t.Errorf("Judge = %q, want skipped", got)
	}
}

// TestTheSuiteRunsInAFreshCloneOfHead: after the session moved HEAD, the command runs on a
// clone of the new commit: a file that the agent left untracked or ignored, or planted
// where the script writes, is not in it, and the committed result file does not count.
func TestTheSuiteRunsInAFreshCloneOfHead(t *testing.T) {
	dir, first := workspace(t)
	head := commit(t, dir, "second.txt", "second\n")
	write(t, filepath.Join(dir, "untracked.txt"), "x")
	write(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	write(t, filepath.Join(dir, "ignored.txt"), "x")
	git(t, dir, "add", ".gitignore")
	git(t, dir, "commit", "--quiet", "-m", "ignore")
	head = git(t, dir, "rev-parse", "HEAD")
	// A status file that the agent planted before After runs.
	write(t, filepath.Join(dir, suite.StateFolder, "status"), "skipped\n")
	write(t, filepath.Join(dir, suite.StateFolder, "result"), `<testsuite><testcase/></testsuite>`)

	out := filepath.Join(t.TempDir(), "seen")
	command := "{ git rev-parse HEAD; ls -A; pwd; } > " + out
	exit, log := runScript(t, dir, first, suite.Full{Command: command, ResultFile: "junit.xml"})
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, log)
	}
	seen := readAll(t, out)
	if !strings.HasPrefix(seen, head+"\n") {
		t.Errorf("the suite's HEAD is not %s:\n%s", head, seen)
	}
	for _, name := range []string{"tracked.txt", "second.txt", ".gitignore"} {
		if !strings.Contains(seen, name+"\n") {
			t.Errorf("the clone has no %s:\n%s", name, seen)
		}
	}
	for _, name := range []string{"untracked.txt", "ignored.txt"} {
		if strings.Contains(seen, name) {
			t.Errorf("the clone has the agent's %s:\n%s", name, seen)
		}
	}
	if strings.Contains(seen, dir) {
		t.Errorf("the suite ran in the workspace %s:\n%s", dir, seen)
	}
	if got := readAll(t, filepath.Join(dir, suite.StateFolder, "status")); got != "ran "+head+"\n" {
		t.Errorf("status %q", got)
	}
	// The command wrote no result file, and the one the commit has was deleted in the clone.
	if exists(filepath.Join(dir, suite.StateFolder, "result")) {
		t.Error("a result file of the planted folder or of the commit stays")
	}
	got := suite.Judge(dir, suite.Full{ResultFile: "junit.xml"}, suite.End{Succeeded: true})
	if got.State != suite.Passed || got.Counts != nil || got.Commit != head {
		t.Errorf("Judge = %+v, want passed with no counts on %s", got, head)
	}
}

// TestTheCloneIsIndependentOfTheWorkspace: nothing in the clone points back to the workspace.
func TestTheCloneIsIndependentOfTheWorkspace(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	out := filepath.Join(t.TempDir(), "seen")
	command := "{ cat .git/objects/info/alternates; git config --get-all core.hooksPath; git config --get-all core.alternateRefsCommand; } > " + out + " 2>&1; true"
	if exit, log := runScript(t, dir, first, suite.Full{Command: command}); exit != 0 {
		t.Fatalf("exit %d\n%s", exit, log)
	}
	if seen := readAll(t, out); strings.Contains(seen, dir) {
		t.Errorf("the clone points to the workspace:\n%s", seen)
	}
}

// TestTheScriptRunsNothingTheWorkspaceConfigures: a filter, a hook, an alternates file with
// an alternateRefsCommand, and an fsmonitor in the workspace's .git/config run no program.
func TestTheScriptRunsNothingTheWorkspaceConfigures(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	marker := filepath.Join(t.TempDir(), "ran")
	other, _ := workspace(t)
	write(t, filepath.Join(dir, ".git/objects/info/alternates"), filepath.Join(other, ".git/objects")+"\n")
	write(t, filepath.Join(dir, ".gitattributes"), "*.txt filter=evil\n")
	cfg := "[core]\n\talternateRefsCommand = touch " + marker + "\n\tfsmonitor = touch " + marker + "\n" +
		"[filter \"evil\"]\n\tsmudge = touch " + marker + "\n\tclean = touch " + marker + "\n"
	f, err := os.OpenFile(filepath.Join(dir, ".git/config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cfg)
	f.Close()
	write(t, filepath.Join(dir, ".git/hooks/post-checkout"), "#!/bin/sh\ntouch "+marker+"\n")
	os.Chmod(filepath.Join(dir, ".git/hooks/post-checkout"), 0o755)
	if exit, log := runScript(t, dir, first, suite.Full{Command: "true"}); exit != 0 {
		t.Fatalf("exit %d\n%s", exit, log)
	}
	if exists(marker) {
		t.Error("the script ran a program that the workspace's configuration names")
	}
}

// TestTheCommandExitStatusIsTheScriptsAndItsResultFileIsCopied: the script exits with the command's
// status and copies the result file that the command wrote.
func TestTheCommandExitStatusIsTheScriptsAndItsResultFileIsCopied(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	full := suite.Full{
		Command:    `printf '%s' '<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>' > reports/junit.xml; exit 3`,
		ResultFile: "reports/junit.xml",
	}
	full.Command = "mkdir reports && " + full.Command
	exit, log := runScript(t, dir, first, full)
	if exit != 3 {
		t.Fatalf("exit %d, want 3\n%s", exit, log)
	}
	got := suite.Judge(dir, full, suite.End{ExitCode: 3})
	if got.State != suite.Failed || !got.Started || got.Counts == nil || *got.Counts != (suite.Counts{Tests: 2, Failed: 1}) {
		t.Errorf("Judge = %+v", got)
	}
}

// TestTheCommandRunsWithAWritableHomeAndNoTransportLimit: the home folder is writable, and the
// settings that allow only HTTPS are not in the command's environment.
func TestTheCommandRunsWithAWritableHomeAndNoTransportLimit(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	out := filepath.Join(t.TempDir(), "seen")
	command := `touch "$HOME/x" && echo "writable" > ` + out + `; echo "count=${GIT_CONFIG_COUNT-unset}" >> ` + out
	if exit, log := runScript(t, dir, first, suite.Full{Command: command}); exit != 0 {
		t.Fatalf("exit %d\n%s", exit, log)
	}
	if seen := readAll(t, out); seen != "writable\ncount=unset\n" {
		t.Errorf("seen %q", seen)
	}
}

// TestACloneThatFailsLeavesAFailedSuiteThatNeverStarted: the clone fails (the workspace's
// HEAD is gone), the script exits 71, and Judge says Failed with Started false.
func TestACloneThatFailsLeavesAFailedSuiteThatNeverStarted(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	// Objects of HEAD that the clone cannot read.
	if err := os.Chmod(filepath.Join(dir, ".git/objects"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, ".git/objects"), 0o755) })
	full := suite.Full{Command: "true"}
	exit, log := runScript(t, dir, first, full)
	if exit == 0 {
		t.Skipf("the objects can be read as this user: %s", log)
	}
	got := suite.Judge(dir, full, suite.End{ExitCode: exit})
	if got.State != suite.Failed || got.Started {
		t.Errorf("Judge = %+v, want failed, not started", got)
	}
}

func TestCommandRefusesWhatTheScriptCannotTake(t *testing.T) {
	_, first := workspace(t)
	for name, c := range map[string]struct {
		start string
		full  suite.Full
	}{
		"a NUL":           {first, suite.Full{Command: "a\x00b"}},
		"too long":        {first, suite.Full{Command: strings.Repeat("a", suite.MaxCommandBytes+1)}},
		"a short start":   {"abc123", suite.Full{Command: "true"}},
		"an upper start":  {strings.ToUpper(first), suite.Full{Command: "true"}},
		"a result path":   {first, suite.Full{Command: "true", ResultFile: "../x.xml"}},
		"a result option": {first, suite.Full{Command: "true", ResultFile: "-x.xml"}},
	} {
		t.Run(name, func(t *testing.T) {
			if words, err := suite.Command(c.start, c.full); err == nil {
				t.Errorf("Command = %q, want an error", words)
			}
		})
	}
	words, err := suite.Command("", suite.Full{Command: "make test; echo '$(x)'", ResultFile: "junit.xml"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sh", "-c"}; words[0] != want[0] || words[1] != want[1] || words[3] != "sh" || words[4] != "make test; echo '$(x)'" || words[5] != "junit.xml" || words[6] != "" {
		t.Errorf("the command and the result file are not arguments: %q", words)
	}
	if strings.Contains(words[2], "make test") || strings.Contains(words[2], "junit.xml") {
		t.Error("the script's text has the command or the result file")
	}
}

// TestJudge is the table of ends the script and the step can have.
func TestJudge(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	full := suite.Full{Command: "x", ResultFile: "junit.xml"}
	red := `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`
	green := `<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`
	skippedOnly := `<testsuite><testcase name="a"><skipped/></testcase><testcase name="b"><skipped/></testcase></testsuite>`
	for _, c := range []struct {
		name         string
		status       string
		result       string
		end          suite.End
		state        suite.State
		started      bool
		counts       *suite.Counts
		mismatch     bool
		resultAsLink bool
	}{
		{name: "green", status: "ran " + sha, result: green, end: suite.End{Succeeded: true}, state: suite.Passed, started: true, counts: &suite.Counts{Tests: 2}},
		{name: "red tests", status: "ran " + sha, result: red, end: suite.End{ExitCode: 1}, state: suite.Failed, started: true, counts: &suite.Counts{Tests: 2, Failed: 1}},
		{name: "a green file and exit 1", status: "ran " + sha, result: green, end: suite.End{ExitCode: 1}, state: suite.Failed, started: true, counts: &suite.Counts{Tests: 2}},
		{name: "a red file and exit 0", status: "ran " + sha, result: red, end: suite.End{Succeeded: true}, state: suite.Failed, started: true, counts: &suite.Counts{Tests: 2, Failed: 1}},
		{name: "0 tests", status: "ran " + sha, result: `<testsuite tests="0"/>`, end: suite.End{Succeeded: true}, state: suite.Failed, started: true, counts: &suite.Counts{}},
		{name: "skipped tests do not fail", status: "ran " + sha, result: skippedOnly, end: suite.End{Succeeded: true}, state: suite.Passed, started: true, counts: &suite.Counts{Tests: 2, Skipped: 2}},
		{name: "unreadable counts are nil", status: "ran " + sha, result: "tests: 4 passed", end: suite.End{Succeeded: true}, state: suite.Passed, started: true},
		{name: "no result file", status: "ran " + sha, end: suite.End{Succeeded: true}, state: suite.Passed, started: true},
		{name: "no result file and exit 2", status: "ran " + sha, end: suite.End{ExitCode: 2}, state: suite.Failed, started: true},
		{name: "totals that do not match", status: "ran " + sha, result: `<testsuite tests="9"><testcase/></testsuite>`, end: suite.End{Succeeded: true}, state: suite.Failed, started: true, mismatch: true},
		{name: "a result file that is a link", status: "ran " + sha, result: green, resultAsLink: true, end: suite.End{Succeeded: true}, state: suite.Passed, started: true},
		{name: "cut at the cap", status: "ran " + sha, end: suite.End{Cancelled: true, TimedOut: true, ExitCode: 143}, state: suite.Timeout, started: true},
		{name: "cut by a stop", status: "ran " + sha, end: suite.End{Cancelled: true, ExitCode: -1}, state: suite.Stopped, started: true},
		{name: "the script never got to the command", end: suite.End{ExitCode: 71}, state: suite.Failed},
		{name: "a status of another form", status: "ran nothing", end: suite.End{Succeeded: true}, state: suite.Failed},
		{name: "skipped", status: "skipped", end: suite.End{Succeeded: true}, state: suite.Skipped},
		{name: "skipped with text after it", status: "skipped now", end: suite.End{Succeeded: true}, state: suite.Failed},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.status != "" {
				write(t, filepath.Join(dir, suite.StateFolder, "status"), c.status+"\n")
			}
			switch {
			case c.resultAsLink:
				outside := filepath.Join(t.TempDir(), "outside.xml")
				write(t, outside, c.result)
				if err := os.MkdirAll(filepath.Join(dir, suite.StateFolder), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, suite.StateFolder, "result")); err != nil {
					t.Fatal(err)
				}
			case c.result != "":
				write(t, filepath.Join(dir, suite.StateFolder, "result"), c.result)
			}
			got := suite.Judge(dir, full, c.end)
			if got.State != c.state || got.Started != c.started || got.Mismatch != c.mismatch {
				t.Errorf("Judge = %+v, want state %s, started %v, mismatch %v", got, c.state, c.started, c.mismatch)
			}
			switch {
			case c.counts == nil && got.Counts != nil, c.counts != nil && (got.Counts == nil || *got.Counts != *c.counts):
				t.Errorf("counts %+v, want %+v", got.Counts, c.counts)
			}
		})
	}
}

// TestClearRemovesTheFolderAndNotWhatALinkLeadsTo: Clear removes a link to a folder and keeps
// the folder.
func TestClearRemovesTheFolderAndNotWhatALinkLeadsTo(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "keep"), "x")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, suite.StateFolder)); err != nil {
		t.Fatal(err)
	}
	if err := suite.Clear(dir); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, suite.StateFolder)) || !exists(filepath.Join(outside, "keep")) {
		t.Error("Clear did not remove the link, or removed what it led to")
	}
	write(t, filepath.Join(dir, suite.StateFolder, "status"), "ran")
	if err := suite.Clear(dir); err != nil || exists(filepath.Join(dir, suite.StateFolder)) {
		t.Errorf("Clear = %v, folder exists %v", err, exists(filepath.Join(dir, suite.StateFolder)))
	}
	if err := suite.Clear(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("Clear of a missing workspace = %v", err)
	}
}

// TestAResultFileThatIsALinkIsNotCopied: the command makes its result file a link to a file
// with green counts, and the script copies nothing.
func TestAResultFileThatIsALinkIsNotCopied(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	full := suite.Full{
		Command:    `printf '%s' '<testsuite><testcase name="a"/></testsuite>' > real.xml && ln -s real.xml link.xml`,
		ResultFile: "link.xml",
	}
	if exit, log := runScript(t, dir, first, full); exit != 0 {
		t.Fatalf("exit %d\n%s", exit, log)
	}
	if exists(filepath.Join(dir, suite.StateFolder, "result")) {
		t.Error("the script copied a result file that is a link")
	}
}

// TestAStateFolderThatIsALinkOutOfTheWorkspaceIsNotRead: the folder of the script's files is a
// link to a folder with a status and a green result file elsewhere: Judge reads none of them.
func TestAStateFolderThatIsALinkOutOfTheWorkspaceIsNotRead(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "status"), "ran 0123456789abcdef0123456789abcdef01234567\n")
	write(t, filepath.Join(elsewhere, "result"), `<testsuite><testcase name="a"/></testsuite>`)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, suite.StateFolder)); err != nil {
		t.Fatal(err)
	}
	got := suite.Judge(dir, suite.Full{ResultFile: "junit.xml"}, suite.End{Succeeded: true})
	if got.State != suite.Failed || got.Started || got.Counts != nil {
		t.Errorf("Judge = %+v, want failed and not started, with no counts", got)
	}
}

// TestAnUnreadableHeadStopsTheScript: git that cannot read HEAD, or a HEAD with no commit after a
// session that started at one, ends the script with 72 and no status; a HEAD with no commit and
// no start commit is skipped.
func TestAnUnreadableHeadStopsTheScript(t *testing.T) {
	_, first := workspace(t)
	full := suite.Full{Command: "true"}
	for _, c := range []struct {
		name  string
		start string
		setup func(t *testing.T, dir string)
		exit  int
	}{
		{"a HEAD that is garbage, no start commit", "", func(t *testing.T, dir string) { write(t, filepath.Join(dir, ".git/HEAD"), "garbage\n") }, suite.ExitNoHead},
		{"a HEAD that is garbage, a start commit", first, func(t *testing.T, dir string) { write(t, filepath.Join(dir, ".git/HEAD"), "garbage\n") }, suite.ExitNoHead},
		{"a branch with no commit after a start commit", first, func(t *testing.T, dir string) {
			git(t, dir, "checkout", "--quiet", "--orphan", "empty")
		}, suite.ExitNoHead},
		{"a branch with no commit and no start commit", "", func(t *testing.T, dir string) {
			git(t, dir, "checkout", "--quiet", "--orphan", "empty")
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := workspace(t)
			c.setup(t, dir)
			exit, log := runScript(t, dir, c.start, full)
			if exit != c.exit {
				t.Fatalf("exit %d, want %d\n%s", exit, c.exit, log)
			}
			if c.exit != 0 {
				if !strings.Contains(log, "qory: git could not read HEAD of the workspace.") {
					t.Errorf("the log has no line about HEAD:\n%s", log)
				}
				if got := suite.Judge(dir, full, suite.End{ExitCode: exit}); got.State != suite.Failed || got.Started {
					t.Errorf("Judge = %+v, want failed and not started", got)
				}
			}
		})
	}
}

// TestTheCloneNeedsTheFileProtocolThatTheScriptAllowsForIt: with Forager's settings in the
// environment, which allow no protocol but HTTPS, a plain clone of the workspace fails and the
// script's clone works.
func TestTheCloneNeedsTheFileProtocolThatTheScriptAllowsForIt(t *testing.T) {
	dir, first := workspace(t)
	commit(t, dir, "second.txt", "second\n")
	plain := exec.Command("git", "clone", "--quiet", "--no-local", "--no-checkout", "--", dir, filepath.Join(t.TempDir(), "c"))
	plain.Env = append(gitEnv(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.allow", "GIT_CONFIG_VALUE_0=never")
	if out, err := plain.CombinedOutput(); err == nil || !strings.Contains(string(out), "transport 'file' not allowed") {
		t.Fatalf("a plain clone under protocol.allow=never: %v\n%s", err, out)
	}
	if exit, log := runScript(t, dir, first, suite.Full{Command: "true"}); exit != 0 {
		t.Fatalf("the script's clone failed: exit %d\n%s", exit, log)
	}
}
