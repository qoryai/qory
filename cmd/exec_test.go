package cmd_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// execVerb runs qory run exec in a separate process with args and env, and returns its
// stdout, its stderr and its exit status.
func execVerb(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	c := exec.Command(os.Args[0], append([]string{"-test.run=^$", "--"}, args...)...)
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Env = append([]string{"QORY_TEST_EXEC=1", "PATH=" + os.Getenv("PATH")}, env...)
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0
	case errors.As(err, &exit):
		return stdout.String(), stderr.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", "", 0
}

// TestExecStartsTheProgramWithTheNamesUnset is qory run exec in a separate process, as
// Forager starts a walled run's launch through it: it replaces itself with the program
// after --, whose arguments are as given, --unset and words with spaces among them, and
// whose environment holds none of the names each --unset gives, and every other
// variable as it was, an empty one included. The verb prints nothing.
func TestExecStartsTheProgramWithTheNamesUnset(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	stdout, stderr, code := execVerb(t,
		[]string{"ANTHROPIC_BASE_URL=https://elsewhere.example", "CLAUDE_CONFIG_DIR=", "KEPT=stays", "EMPTY_KEPT="},
		"--unset", "ANTHROPIC_BASE_URL", "--unset", "CLAUDE_CONFIG_DIR", "--",
		sh, "-c", `printf '%s|' "$0" "$@"; echo; env`, "arg0", "-p", "two words", "--unset", "KEPT")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if first, _, _ := strings.Cut(stdout, "\n"); first != "arg0|-p|two words|--unset|KEPT|" {
		t.Errorf("the program's arguments are %q", first)
	}
	wants(t, stdout, "\nKEPT=stays\n", "\nEMPTY_KEPT=\n")
	lacks(t, stdout, "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR")
	if stderr != "" {
		t.Errorf("printed %q", stderr)
	}
}

// TestExecExitsAsAShellDoes pins the exit status of qory run exec when it starts
// nothing: 2 for arguments it cannot read, a name that is not a variable's refused
// without being quoted, 127 for a program it does not find, and 126 for one it cannot
// execute, each with its reason on stderr and nothing on stdout.
func TestExecExitsAsAShellDoes(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		args []string
		code int
		lack string
	}{
		{"no --", []string{"--unset", "A", "true"}, 2, ""},
		{"--unset without a name", []string{"--unset"}, 2, ""},
		{"a name that is not a variable's", []string{"--unset", "NOT-A-NAME=value", "--", "true"}, 2, "NOT-A-NAME"},
		{"no program", []string{"--unset", "A", "--"}, 2, ""},
		{"an option before --", []string{"-v", "--", "true"}, 2, ""},
		{"a program not on PATH", []string{"--", "qory-test-no-such-program"}, 127, ""},
		{"a path that does not exist", []string{"--", filepath.Join(dir, "missing")}, 127, ""},
		{"a directory", []string{"--", dir}, 126, ""},
	} {
		stdout, stderr, code := execVerb(t, nil, c.args...)
		if code != c.code || stderr == "" || stdout != "" {
			t.Errorf("%s: exit %d, want %d; stdout %q, stderr %q", c.name, code, c.code, stdout, stderr)
		}
		if c.lack != "" && strings.Contains(stderr, c.lack) {
			t.Errorf("%s: stderr quotes the argument: %q", c.name, stderr)
		}
	}
}

// TestExecIsHidden pins the helper's exec mode as a verb no person runs: it is not in
// the help of qory run.
func TestExecIsHidden(t *testing.T) {
	emptyDir(t)
	root := cmd.Root()
	if c, _, err := root.Find([]string{"run", "exec"}); err != nil || c.Name() != "exec" || !c.Hidden {
		t.Fatalf("run exec is %v, %v", c, err)
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "exec")
}

// serverVariables are the variables qory takes from its environment when a command
// starts, an image's value for each among them, and none of them reaches a program
// qory starts.
var serverVariables = []string{"QORY_ACCESS_KEY_ID", "QORY_ACCESS_KEY_SECRET", "QORY_APIARY_PUBLIC_KEY", "QORY_SERVER_SECRET", "QORY_RUN_KEY"}

// TestExecPassesNoServerVariable is qory run exec with the server's variables in its
// environment, as an image may set them: the program it starts has none of them, though
// no --unset names them.
func TestExecPassesNoServerVariable(t *testing.T) {
	var env []string
	for _, name := range serverVariables {
		env = append(env, name+"=from-the-image")
	}
	stdout, stderr, code := execVerb(t, append(env, "KEPT=stays"), "--", "env")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	wants(t, stdout, "KEPT=stays\n")
	lacks(t, stdout, "from-the-image")
}

// TestAWalledRunStartsThroughTheExecVerb is a walled run whose launch starts through
// the real qory run exec, with the arguments Forager gives it, in a container whose
// image sets the server's variables, a name Claude Code's descriptor denies and its
// configuration folder: the runtime gets none of them, and the image's other
// variables as they are.
func TestAWalledRunStartsThroughTheExecVerb(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	seen := filepath.Join(t.TempDir(), "env")
	runtime := filepath.Join(t.TempDir(), "env-runtime")
	writeFile(t, runtime, "#!/bin/sh\nenv > '"+seen+"'\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	image := filepath.Join(t.TempDir(), "image-env")
	lines := "ANTHROPIC_BASE_URL=https://elsewhere.example\nCLAUDE_CONFIG_DIR=/image/claude\nIMAGE_KEPT=stays\n"
	for _, name := range serverVariables {
		lines += name + "=from-the-image\n"
	}
	writeFile(t, image, lines)
	t.Setenv("QORY_TEST_IMAGE_ENV", image)
	t.Setenv("QORY_RUN_KEY", "")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n"+passingWall(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	env := "\n" + string(data)
	wants(t, env, "\nIMAGE_KEPT=stays\n")
	lacks(t, env, "from-the-image", "\nANTHROPIC_BASE_URL=", "\nCLAUDE_CONFIG_DIR=", "QORY_TEST_EXEC", "QORY_TEST_IMAGE_ENV")
}
