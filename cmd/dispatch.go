package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render/claude"
)

// emptyMCP is the MCP list a dispatched run of Claude Code is given when the harness has
// none: no server.
const emptyMCP = `{"mcpServers":{}}`

// dispatchedArgs are the launch arguments of a dispatched run, one given a run key: for
// Claude Code, the launch's with --strict-mcp-config, so Claude Code loads no MCP server
// but those --mcp-config names, --mcp-config with the harness's list, or an empty one
// when the harness has none, and --setting-sources user, which replaces any other value
// of it, so no settings of the checkout's .claude are read. The flags go before the
// arguments given after --, and the last is one that takes a single value, so
// --mcp-config, which takes several, never takes a word of those. Another runtime's
// arguments are as given.
func dispatchedArgs(runtime string, args []string) []string {
	if !isClaude(runtime) {
		return args
	}
	out := make([]string, 0, len(args)+5)
	hasMCP := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--strict-mcp-config", strings.HasPrefix(a, "--setting-sources="):
			continue
		case a == "--setting-sources":
			i++
			continue
		case a == "--mcp-config", strings.HasPrefix(a, "--mcp-config="):
			hasMCP = true
		}
		out = append(out, a)
	}
	if !hasMCP {
		out = append(out, "--mcp-config", emptyMCP)
	}
	return append(out, "--strict-mcp-config", "--setting-sources", "user")
}

// withAgentHome is the fixed variables of a dispatched walled run of Claude Code, with
// CLAUDE_CONFIG_DIR pointing at [preparation.AgentHome], which qory run prepare makes
// new and empty as the last step before the agent starts. A fixed value wins over every
// other source, and the runtime's deny list leaves it in.
func withAgentHome(fixed []string) []string {
	out := slices.DeleteFunc(slices.Clone(fixed), func(kv string) bool {
		return strings.HasPrefix(kv, preparation.EnvAgentHome+"=")
	})
	out = append(out, preparation.EnvAgentHome+"="+preparation.AgentHome)
	slices.Sort(out)
	return out
}

// harnessInCheckout refuses a dispatched walled run of Claude Code whose harness home is
// inside the checkout, by where both really are: the repository's files, which git
// writes over an ignored file at checkout, and its commands, which run in the checkout
// the wall mounts writable, could change the plugin, the MCP list and the instructions
// the launch names.
func harnessInCheckout(home string) error {
	return input(fmt.Errorf("the harness home %s is inside the checkout, which the repository's files and commands can change, so a run given a run key behind a wall does not start. Compose the harness outside the checkout with --home, such as qory harness compose --home ~/.cache/qory/homes, and run with the same --home", exports.Value(home, false)))
}

// isClaude reports whether runtime is Claude Code, the one runtime a dispatched run
// starts otherwise than a person's.
func isClaude(runtime string) bool { return runtime == claude.Runtime }

// locateRun is [locate] for qory run and qory run resend: given a run key, the checkout's
// qory.yaml sets no machine key, harness.launch and env among them, as under extends, so
// the repository a dispatched run works on does not configure how it starts.
func locateRun(o homeOptions, key *runKey) (places, config.Config, error) {
	if key == nil {
		return locate(o)
	}
	return locateWith(o, func(root string) (config.Config, error) {
		conf, err := config.Load(root, false)
		if err != nil {
			return conf, input(err)
		}
		return conf, nil
	})
}
