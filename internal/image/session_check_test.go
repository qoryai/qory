package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// standIns is a folder with each program of the git session, and git as a script that prints
// version, as the folder of a session's PATH.
func standIns(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range SessionTools() {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := "#!/bin/sh\nprintf '%s\\nsecond line\\n' '" + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// checkOn runs the script under shell with dir as the session's PATH, with an empty environment,
// and returns what it printed on stdout, on stderr, and its exit status.
func checkOn(t *testing.T, shell, dir string) (string, string, int) {
	t.Helper()
	c := exec.Command(shell, "-c", sessionCheckScript(dir))
	c.Env = []string{}
	var stdout, stderr strings.Builder
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	if _, exited := err.(*exec.ExitError); err != nil && !exited {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), c.ProcessState.ExitCode()
}

// shells are the shells the script runs under here: sh and dash, where this machine has it.
func shells(t *testing.T) []string {
	t.Helper()
	all := []string{"/bin/sh"}
	if _, err := os.Stat("/bin/dash"); err == nil {
		all = append(all, "/bin/dash")
	}
	return all
}

// TestTheSessionCheckPassesSilently is an image with every program: no output, exit 0.
func TestTheSessionCheckPassesSilently(t *testing.T) {
	for _, shell := range shells(t) {
		for _, version := range []string{"git version 2.25.0", "git version 2.47.3", "git version 3.0.0"} {
			out, errOut, code := checkOn(t, shell, standIns(t, version))
			if out != "" || errOut != "" || code != 0 {
				t.Errorf("%s, %s: %q, %q, exit %d", shell, version, out, errOut, code)
			}
		}
	}
}

// TestTheSessionCheckNamesEachMissingProgram is an image without one program of [SessionTools], a
// list a new tool joins: the line of qory image check on stderr, the fix first, and exit 1.
func TestTheSessionCheckNamesEachMissingProgram(t *testing.T) {
	for _, shell := range shells(t) {
		for _, tool := range sessionTools {
			dir := standIns(t, "git version 2.47.3")
			if err := os.Remove(filepath.Join(dir, tool.name)); err != nil {
				t.Fatal(err)
			}
			out, errOut, code := checkOn(t, shell, dir)
			want := noSessionTool(tool.name, tool.why) + "\n"
			if out != "" || errOut != want || code != 1 {
				t.Errorf("%s, no %s: %q, %q, exit %d, want %q", shell, tool.name, out, errOut, code, want)
			}
		}
	}
}

// TestTheSessionCheckPrintsEveryMissingProgram is an image without two programs and git.
func TestTheSessionCheckPrintsEveryMissingProgram(t *testing.T) {
	for _, shell := range shells(t) {
		dir := standIns(t, "git version 2.47.3")
		for _, name := range []string{"comm", "tr", "git"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		_, errOut, code := checkOn(t, shell, dir)
		for _, want := range []string{"install comm in ", "install tr in ", noSessionGit() + "\n"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%s: %q has no %q", shell, errOut, want)
			}
		}
		if code != 1 {
			t.Errorf("%s: exit %d", shell, code)
		}
	}
}

// TestTheSessionCheckTakesNoFileThatDoesNotRun is a program that is not executable, which dash's
// command -v lists.
func TestTheSessionCheckTakesNoFileThatDoesNotRun(t *testing.T) {
	for _, shell := range shells(t) {
		dir := standIns(t, "git version 2.47.3")
		if err := os.Chmod(filepath.Join(dir, "comm"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, errOut, code := checkOn(t, shell, dir); !strings.HasPrefix(errOut, "install comm in ") || code != 1 {
			t.Errorf("%s: %q, exit %d", shell, errOut, code)
		}
	}
}

// TestTheSessionCheckHasTheFloorOfQoryImageCheck is each git line under the script and under
// [gitAtLeast225]: both take the same lines, and the script prints the line of qory image check.
func TestTheSessionCheckHasTheFloorOfQoryImageCheck(t *testing.T) {
	for _, line := range []string{
		"git version 2.24.0", "git version 2.25.0", "git version 2.25", "git version 2.9.5", "git version 2.100.1",
		"git version 1.99.0", "git version 3.0.0", "git version 2.39.5 (Apple Git-154)", "git version 2", "git version 2.",
		"git version x.y", "git version 2.x.1", "git version 2x.30", "hub version 3.0", "",
	} {
		for _, shell := range shells(t) {
			dir := standIns(t, line)
			at := filepath.Join(dir, "git")
			_, errOut, code := checkOn(t, shell, dir)
			if gitAtLeast225(line) {
				if errOut != "" || code != 0 {
					t.Errorf("%s, %q: %q, exit %d", shell, line, errOut, code)
				}
				continue
			}
			if want := sessionGitOld(at, line) + "\n"; errOut != want || code != 1 {
				t.Errorf("%s, %q: %q, exit %d, want %q", shell, line, errOut, code, want)
			}
		}
	}
}

// TestTheSessionCheckReportsAGitThatFails is a git that exits 3.
func TestTheSessionCheckReportsAGitThatFails(t *testing.T) {
	for _, shell := range shells(t) {
		dir := standIns(t, "")
		at := filepath.Join(dir, "git")
		if err := os.WriteFile(at, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, errOut, code := checkOn(t, shell, dir)
		if want := sessionGitFailed(at, "exit status 3") + "\n"; errOut != want || code != 1 {
			t.Errorf("%s: %q, exit %d, want %q", shell, errOut, code, want)
		}
	}
}

// TestSessionCheckLine is what qory prints of the output of the step.
func TestSessionCheckLine(t *testing.T) {
	for line, want := range map[string]bool{
		noSessionGit():              true,
		noSessionTool("tr", "x"):    true,
		sessionGitOld("/a", "b"):    true,
		"install git in /elsewhere": false,
		"something else":            false,
		noSessionGit() + "\x1b[2J":  false,
		"":                          false,
	} {
		if _, ok := SessionCheckLine(line); ok != want {
			t.Errorf("%q: %v, want %v", line, ok, want)
		}
	}
}
