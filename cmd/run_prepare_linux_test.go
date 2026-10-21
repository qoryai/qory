//go:build linux

package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/preparation"
)

// agentHomeFree skips the test unless qory run prepare can make the agent's home at
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

// TestRunPrepareMakesTheAgentHomeLast is a dispatched walled run of Claude Code: qory
// run prepare makes the agent's home after postCreateCommand and postStartCommand ran,
// which find nothing at its path, as an empty folder of mode 0700.
func TestRunPrepareMakesTheAgentHomeLast(t *testing.T) {
	prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": "test ! -e ` + preparation.AgentHome + ` && echo create",
			"postStartCommand": "test ! -e ` + preparation.AgentHome + ` && echo start"}`,
	})
	agentHomeFree(t)
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "qory run prepare: fetched acme/shop in Xs\ncreate\nstart\n" +
		"qory run prepare: ran postCreateCommand and postStartCommand in Xs\n"
	if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	fi, err := os.Lstat(preparation.AgentHome)
	if err != nil {
		t.Fatalf("the agent's home was not made: %v", err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the agent's home is %s, want a folder of mode 0700", fi.Mode())
	}
	if names, err := os.ReadDir(preparation.AgentHome); err != nil || len(names) != 0 {
		t.Errorf("the agent's home holds %v, %v", names, err)
	}
}

// TestRunPrepareRefusesAnAgentHomeTheCommandsMade is postCreateCommand or
// postStartCommand putting something at the agent's home: qory run prepare refuses the
// run with one line after the commands' and exits non-zero.
func TestRunPrepareRefusesAnAgentHomeTheCommandsMade(t *testing.T) {
	for name, config := range map[string]string{
		"postCreateCommand": `{"postCreateCommand": "touch ` + preparation.AgentHome + `"}`,
		"postStartCommand":  `{"postStartCommand": "mkdir ` + preparation.AgentHome + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			prepareRemote(t, map[string]string{".devcontainer.json": config})
			agentHomeFree(t)
			out, err := run(t, "run", "prepare")
			want := "qory run prepare: fetched acme/shop in Xs\n" +
				"qory run prepare: ran " + name + " in Xs\n" +
				"qory run prepare: the agent's home " + preparation.AgentHome + " exists already, and qory makes it only where nothing is, so a run given a run key does not start\n"
			if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
				t.Errorf("printed %q, want %q", got, want)
			}
			if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) == 0 {
				t.Errorf("%v exits %d", err, cmd.ExitCode(err))
			}
		})
	}
}
