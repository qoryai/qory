package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	return checkSettingsOn(t, shell, dir, "")
}

// checkSettingsOn is [checkOn] for the script of a Codex run, with settings in place of
// /etc/codex.
func checkSettingsOn(t *testing.T, shell, dir, settings string) (string, string, int) {
	t.Helper()
	c := exec.Command(shell, "-c", sessionCheckScript(dir, settings))
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

// TestSessionCheckLine is what qory prints of the output of the step: the lines the script prints
// on an image, with git in a folder of the session's PATH.
func TestSessionCheckLine(t *testing.T) {
	old := sessionGitOld("/usr/bin/git", "git version 2.24.0")
	for line, want := range map[string]bool{
		noSessionGit():                          true,
		noCodexSettings():                       true,
		noCodexSettings() + " ":                 false,
		noSessionTool("tr", toolWhy(t, "tr")):   true,
		old:                                     true,
		sessionGitOld("/usr/local/bin/git", ""): true,
		sessionGitFailed("/bin/git", "exit status 3"):           true,
		noSessionTool("tr", "x"):                                false,
		noSessionTool("curl", "x"):                              false,
		sessionGitOld("/a/git", "git version 2.24.0"):           false,
		sessionGitFailed("/bin/git", "run this"):                false,
		sessionGitFailed("/bin/git", "exit status 1234"):        false,
		sessionGitFailed("/bin/git", "exit status "):            false,
		"install git in /elsewhere":                             false,
		"install git in /usr/local/bin, /usr/bin, /bin: run it": false,
		"something else":                                        false,
		noSessionGit() + "\x1b[2J":                              false,
		old + "\x1b[2J":                                         false,
		old + "\xc2\x9b[2J":                                     false,
		old + "\x9b[2J":                                         false,
		old + "\u202e":                                          false,
		old + strings.Repeat("x", 201-len(old)):                 false,
		old + strings.Repeat("x", 200-len(old)):                 true,
		"":                                                      false,
	} {
		if _, ok := SessionCheckLine(line); ok != want {
			t.Errorf("%q: %v, want %v", line, ok, want)
		}
	}
}

// toolWhy is what the git session runs the tool for.
func toolWhy(t *testing.T, name string) string {
	t.Helper()
	for _, tool := range sessionTools {
		if tool.name == name {
			return tool.why
		}
	}
	t.Fatalf("no tool %s", name)
	return ""
}

// TestSessionCheckFixesKeepsAtMostOnePerCheck is more lines than the script can print.
func TestSessionCheckFixesKeepsAtMostOnePerCheck(t *testing.T) {
	got := SessionCheckFixes(strings.Repeat(noSessionGit()+"\nanother line\n", 1000))
	if want := len(sessionTools) + 2; len(got) != want {
		t.Errorf("%d lines, want %d", len(got), want)
	}
}

// TestTheSessionCheckTakesNoFolder is a folder with the name of a program, and no such program.
func TestTheSessionCheckTakesNoFolder(t *testing.T) {
	for _, shell := range shells(t) {
		dir := standIns(t, "git version 2.47.3")
		if err := os.Remove(filepath.Join(dir, "comm")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "comm"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, errOut, code := checkOn(t, shell, dir); !strings.HasPrefix(errOut, "install comm in ") || code != 1 {
			t.Errorf("%s: %q, exit %d", shell, errOut, code)
		}
	}
}

// TestTheSessionCheckGoesPastAFileThatDoesNotRun is a folder of the session's PATH with a tr that is
// not executable, and one with a folder named tr, ahead of a folder with a real tr. Dash's command -v
// returns the first file.
func TestTheSessionCheckGoesPastAFileThatDoesNotRun(t *testing.T) {
	for _, shell := range shells(t) {
		first, second := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(first, "tr"), []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(second, "tr"), 0o755); err != nil {
			t.Fatal(err)
		}
		dirs := first + ":" + second + ":" + standIns(t, "git version 2.47.3")
		if _, errOut, code := checkOn(t, shell, dirs); errOut != "" || code != 0 {
			t.Errorf("%s: %q, exit %d", shell, errOut, code)
		}
	}
}

// etcCodexPlaces makes, in a new temp folder, what an image can have at /etc/codex: the
// paths of a link that leads nowhere and of a link to a folder, which the checks of a Codex
// run stop, and of a file, a folder and nothing, which they pass.
func etcCodexPlaces(t *testing.T) (links, others []string) {
	t.Helper()
	base := t.TempDir()
	file, folder := filepath.Join(base, "file"), filepath.Join(base, "folder")
	nowhere, toFolder := filepath.Join(base, "link-nowhere"), filepath.Join(base, "link-folder")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "absent"), nowhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(folder, toFolder); err != nil {
		t.Fatal(err)
	}
	return []string{nowhere, toFolder}, []string{file, folder, filepath.Join(base, "absent")}
}

// TestTheCodexCheckStopsALinkAtEtcCodex is the script of a Codex run on an image with every
// program: with a link at the place of /etc/codex, one that leads nowhere or one to a folder,
// it prints the line for /etc/codex, the fix first, and exits 1; with a file, a folder or
// nothing there it passes and prints nothing. CodexSessionCheckScript checks /etc/codex
// itself.
func TestTheCodexCheckStopsALinkAtEtcCodex(t *testing.T) {
	for _, shell := range shells(t) {
		dir := standIns(t, "git version 2.47.3")
		links, others := etcCodexPlaces(t)
		for _, at := range links {
			out, errOut, code := checkSettingsOn(t, shell, dir, at)
			if want := noCodexSettings() + "\n"; out != "" || errOut != want || code != 1 {
				t.Errorf("%s, %s: %q, %q, exit %d, want %q", shell, at, out, errOut, code, want)
			}
		}
		for _, at := range others {
			if out, errOut, code := checkSettingsOn(t, shell, dir, at); out != "" || errOut != "" || code != 0 {
				t.Errorf("%s, %s: %q, %q, exit %d, want a pass", shell, at, out, errOut, code)
			}
		}
	}
	if want := sessionCheckScript(strings.Join(gitSessionDirs, ":"), "/etc/codex"); CodexSessionCheckScript() != want {
		t.Errorf("CodexSessionCheckScript checks another place:\n%s", CodexSessionCheckScript())
	}
}

// TestTheLineForALinkAtEtcCodex is the line qory prints for an image with a link at
// /etc/codex: the fix first, and at most 100 characters after "qory run: ".
func TestTheLineForALinkAtEtcCodex(t *testing.T) {
	want := "Use an image with no link at /etc/codex: qory's bind of the Codex hooks follows a link."
	if got := noCodexSettings(); got != want || CodexSettingsFix() != want {
		t.Errorf("the line: %q and %q, want %q", got, CodexSettingsFix(), want)
	}
	if n := utf8.RuneCountInString("qory run: " + want); n > 100 {
		t.Errorf("the line has %d characters after qory run:, want at most 100", n)
	}
}

// TestTheCodexSettingsCheckAlone is the step of a Codex run with no git session, with an
// empty PATH: with a link at the place of /etc/codex, the line for /etc/codex and exit 1;
// with a file, a folder or nothing there, no output and exit 0.
// CodexSettingsCheckScript checks /etc/codex itself.
func TestTheCodexSettingsCheckAlone(t *testing.T) {
	links, others := etcCodexPlaces(t)
	run := func(shell, settings string) (string, string, int) {
		c := exec.Command(shell, "-c", codexSettingsCheckScript(settings))
		c.Env = []string{}
		var stdout, stderr strings.Builder
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		if _, exited := err.(*exec.ExitError); err != nil && !exited {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), c.ProcessState.ExitCode()
	}
	for _, shell := range shells(t) {
		for _, at := range links {
			out, errOut, code := run(shell, at)
			if want := noCodexSettings() + "\n"; out != "" || errOut != want || code != 1 {
				t.Errorf("%s, %s: %q, %q, exit %d, want %q", shell, at, out, errOut, code, want)
			}
		}
		for _, at := range others {
			if out, errOut, code := run(shell, at); out != "" || errOut != "" || code != 0 {
				t.Errorf("%s, %s: %q, %q, exit %d, want a pass", shell, at, out, errOut, code)
			}
		}
	}
	if CodexSettingsCheckScript() != codexSettingsCheckScript("/etc/codex") {
		t.Errorf("CodexSettingsCheckScript checks another place:\n%s", CodexSettingsCheckScript())
	}
}

// TestTheCodexSettingsCheckInside is the step a walled Codex run with no setup runs in
// the agent's container: with a link at the place of /etc/codex, no output and exit
// [CodexSettingsInsideExit]; with a file, a folder or nothing there, no output and exit 0.
// CodexSettingsInsideScript checks /etc/codex itself.
func TestTheCodexSettingsCheckInside(t *testing.T) {
	links, others := etcCodexPlaces(t)
	run := func(shell, settings string) (string, int) {
		c := exec.Command(shell, "-c", codexSettingsInsideScript(settings))
		c.Env = []string{}
		out, err := c.CombinedOutput()
		if _, exited := err.(*exec.ExitError); err != nil && !exited {
			t.Fatal(err)
		}
		return string(out), c.ProcessState.ExitCode()
	}
	for _, shell := range shells(t) {
		for _, at := range links {
			if out, code := run(shell, at); out != "" || code != CodexSettingsInsideExit {
				t.Errorf("%s, %s: %q, exit %d", shell, at, out, code)
			}
		}
		for _, at := range others {
			if out, code := run(shell, at); out != "" || code != 0 {
				t.Errorf("%s, %s: %q, exit %d, want a pass", shell, at, out, code)
			}
		}
	}
	if CodexSettingsInsideScript() != codexSettingsInsideScript("/etc/codex") {
		t.Errorf("CodexSettingsInsideScript checks another place:\n%s", CodexSettingsInsideScript())
	}
}
