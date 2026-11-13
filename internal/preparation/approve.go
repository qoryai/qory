package preparation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoryai/forager/session/runtimes"

	"github.com/qoryai/qory/internal/integration"
)

// envAPIKey is the variable Claude Code reads an API key from.
const envAPIKey = "ANTHROPIC_API_KEY"

// approvedKey is the part of Forager's stand-in for a model key that Claude Code keeps
// as the key's approval: its last 20 characters, after the space around it is trimmed.
// It copies the rule of approved() in Forager's session/runtimes/claude/approve.go and
// becomes claude.ApprovedKey() once Forager exports it.
func approvedKey() string {
	s := strings.TrimSpace(runtimes.Placeholder)
	return s[len(s)-20:]
}

// ApproveStandIn writes .claude.json into the agent's home, [AgentHome], with the
// approval of Forager's stand-in as Claude Code's API key, when getenv sets
// [EnvAgentHome] to the agent's home and ANTHROPIC_API_KEY to the stand-in, and does
// nothing otherwise. Forager writes the same approval for an interactive walled run, but
// before qory run agent-env makes the home, so that write finds no folder; without the
// approval, Claude Code asks the person to approve the stand-in. qory run agent-env
// calls it after [FreshHome], so the home is empty. A file already at the path, which a
// program of the image or the setup commands left running wrote, stops the run, as in
// [FreshHome]. Any other failure leaves no file, as Forager's script does, and Claude
// Code then asks for the approval.
func ApproveStandIn(getenv func(string) string) error {
	return approveStandIn(getenv, AgentHome)
}

// approveStandIn writes the approval into the agent's home at home, see
// [ApproveStandIn].
func approveStandIn(getenv func(string) string, home string) error {
	if getenv(EnvAgentHome) != home || getenv(envAPIKey) != runtimes.Placeholder {
		return nil
	}
	data := `{"customApiKeyResponses":{"approved":["` + approvedKey() + `"],"rejected":[]}}`
	path := filepath.Join(home, ".claude.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf(textHomeNotEmpty, integration.Printable(home))
	}
	if err != nil {
		return nil
	}
	_, err = f.WriteString(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return nil
}
