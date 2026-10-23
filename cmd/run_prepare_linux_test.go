//go:build linux

package cmd_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
)

// agentHomeFree skips the test unless qory run agent-env can make the agent's home at
// [preparation.AgentHome] on this machine: nothing is there yet, no folder above it is
// a symbolic link, and Claude Code's managed settings are not at /etc/claude-code. It
// sets CLAUDE_CONFIG_DIR to the agent's home, as a dispatched walled run of Claude Code
// has it, and removes the home once the test is over.
func agentHomeFree(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(preparation.AgentHome); !errors.Is(err, os.ErrNotExist) {
		t.Skipf("%s is in use: %v", preparation.AgentHome, err)
	}
	if _, err := os.Lstat("/etc/claude-code"); !errors.Is(err, os.ErrNotExist) {
		t.Skipf("/etc/claude-code is in use: %v", err)
	}
	for dir := filepath.Dir(preparation.AgentHome); ; dir = filepath.Dir(dir) {
		fi, err := os.Lstat(dir)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Skipf("%s cannot hold the agent's home: %v", dir, err)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(preparation.AgentHome); err != nil {
			t.Error(err)
		}
	})
	t.Setenv(preparation.EnvAgentHome, preparation.AgentHome)
}

// TestRunPrepareLeavesTheAgentHome is a dispatched walled run of Claude Code whose
// repository qory run fetched on the host: qory run prepare runs postCreateCommand and
// postStartCommand and makes no agent's home, which qory run agent-env makes later.
func TestRunPrepareLeavesTheAgentHome(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": "echo create", "postStartCommand": "echo start"}`,
	})
	fetchedOnHost(t, dir)
	agentHomeFree(t)
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "create\nstart\n" +
		"qory run prepare: ran postCreateCommand and postStartCommand in Xs\n" + setupLogLine
	if got := shownPrepare(out); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if _, err := os.Lstat(preparation.AgentHome); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the agent's home was made: %v", err)
	}
}

// agentEnv runs qory run agent-env as a separate process with CLAUDE_CONFIG_DIR at the
// agent's home and a launch that lists the home, and returns its stdout, its stderr and
// its error.
func agentEnv(t *testing.T) (string, string, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(file, preparation.KeptNames([]string{preparation.EnvAgentHome}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	c := exec.Command(os.Args[0], "-test.run=^$", "--", file, "--", "ls", "-A", preparation.AgentHome)
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Env = []string{"QORY_TEST_AGENT_ENV=1", "PATH=" + os.Getenv("PATH"), preparation.EnvAgentHome + "=" + preparation.AgentHome}
	err := c.Run()
	return stdout.String(), stderr.String(), err
}

// TestAgentEnvMakesTheAgentHomeFirst is the agent of a dispatched walled run of Claude
// Code started through qory run agent-env, after the setup commands: the verb makes the
// agent's home, an empty folder of mode 0700, before the launch starts and finds it.
func TestAgentEnvMakesTheAgentHomeFirst(t *testing.T) {
	agentHomeFree(t)
	stdout, stderr, err := agentEnv(t)
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("%v, printed %q and %q", err, stdout, stderr)
	}
	fi, err := os.Lstat(preparation.AgentHome)
	if err != nil {
		t.Fatalf("the agent's home was not made: %v", err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the agent's home is %s, want a folder of mode 0700", fi.Mode())
	}
}

// TestAgentEnvRefusesAnAgentHomeTheSetupMade is a file or a folder at the agent's home
// before qory run agent-env starts, as a setup command or the image may leave one: the
// verb prints one line, exits non-zero, and the launch does not start.
func TestAgentEnvRefusesAnAgentHomeTheSetupMade(t *testing.T) {
	for name, make := range map[string]func() error{
		"a file":   func() error { return os.WriteFile(preparation.AgentHome, nil, 0o644) },
		"a folder": func() error { return os.Mkdir(preparation.AgentHome, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			agentHomeFree(t)
			if err := make(); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := agentEnv(t)
			want := "qory run agent-env: remove " + preparation.AgentHome + " from the image and from the setup commands: qory makes the agent's home only where nothing is, so a run given a run key does not start\n"
			if stderr != want || stdout != "" {
				t.Errorf("printed %q and %q, want %q", stdout, stderr, want)
			}
			if err == nil {
				t.Error("qory run agent-env exited 0")
			}
		})
	}
}
