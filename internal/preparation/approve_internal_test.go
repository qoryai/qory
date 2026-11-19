package preparation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/session/runtimes"
)

// envFor is a getenv with CLAUDE_CONFIG_DIR at home and ANTHROPIC_API_KEY at key, or
// unset when key is empty.
func envFor(home, key string) func(string) string {
	return func(name string) string {
		switch name {
		case EnvAgentHome:
			return home
		case envAPIKey:
			return key
		}
		return ""
	}
}

// TestTheApprovedKeyIsTheStandInsLast20 is the approval of Forager's stand-in: its last
// 20 characters once the space around it is trimmed, as approved() in Forager's
// session/runtimes/claude/approve.go has it.
func TestTheApprovedKeyIsTheStandInsLast20(t *testing.T) {
	got := approvedKey()
	if len(got) != 20 || !strings.HasSuffix(strings.TrimSpace(runtimes.Placeholder), got) {
		t.Errorf("the approved key is %q, want the last 20 characters of %q", got, runtimes.Placeholder)
	}
}

// TestApproveStandInWritesTheApproval is a walled run of Claude Code whose API key is
// Forager's stand-in, interactive or not: the fresh home gets .claude.json with the
// approval alone, mode 0600, whatever the umask.
func TestApproveStandInWritesTheApproval(t *testing.T) {
	home := filepath.Join(realTemp(t), "qory-agent-home")
	if err := freshHome(home); err != nil {
		t.Fatal(err)
	}
	if err := approveStandIn(envFor(home, runtimes.Placeholder), home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"customApiKeyResponses":{"approved":["` + approvedKey() + `"],"rejected":[]}}`
	if string(data) != want {
		t.Errorf(".claude.json is %s, want %s", data, want)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf(".claude.json is %v (%v), want a file of mode 0600", fi, err)
	}
}

// TestApproveStandInNeedsTheStandIn is a run whose API key is a real one, an empty one or
// unset, or whose CLAUDE_CONFIG_DIR is not the agent's home: no file is written.
func TestApproveStandInNeedsTheStandIn(t *testing.T) {
	home := filepath.Join(realTemp(t), "qory-agent-home")
	if err := freshHome(home); err != nil {
		t.Fatal(err)
	}
	for name, getenv := range map[string]func(string) string{
		"a real key":     envFor(home, "real-key-example"),
		"an unset key":   envFor(home, ""),
		"another folder": envFor(home+"-other", runtimes.Placeholder),
		"a padded key":   envFor(home, " "+runtimes.Placeholder+" "),
	} {
		if err := approveStandIn(getenv, home); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	wantFreshHome(t, home)
}

// TestApproveStandInRefusesAFileThere is .claude.json in the home before the approval is
// written, as a program the image or the setup commands left running may write it: the
// run stops with the line of a home that held something, and the file stays as it was.
func TestApproveStandInRefusesAFileThere(t *testing.T) {
	home := filepath.Join(realTemp(t), "qory-agent-home")
	if err := freshHome(home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := approveStandIn(envFor(home, runtimes.Placeholder), home)
	want := "leave no program running that writes into " + home + "\n  The agent's home held something once qory made it, so the run does not start; a program that the image or the setup commands started can write there"
	if err == nil || err.Error() != want {
		t.Errorf("err %v\nwant %s", err, want)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Errorf(".claude.json is %q (%v), want {}", data, err)
	}
}

// TestApproveStandInWritesNothingWithoutAHome is a home that is not there: no file, and
// no error, as Forager's script leaves it; Claude Code then asks for the approval.
func TestApproveStandInWritesNothingWithoutAHome(t *testing.T) {
	home := filepath.Join(realTemp(t), "qory-agent-home")
	if err := approveStandIn(envFor(home, runtimes.Placeholder), home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the home was made: %v", err)
	}
}
