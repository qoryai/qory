// Package goose renders for Goose, which reads AGENTS.md at the checkout root, skills
// from .agents/skills and agents from .agents/agents. All three are linked into the
// checkout, and the agents directory is the only thing Goose gets of its own. Goose has
// no project settings file, so the target model is not written, and it has recipes
// instead of commands and no output styles, and configures MCP servers in the user's
// home, so those three kinds are skipped. Goose has no directory of its own in the
// checkout, so a files entry named goose/<path> has no place and is skipped too.
//
// The paths under goose a files entry may not take, see [render.Reserved]:
//
//	agents  agents are linked there; ship it as agents/<name>
package goose

import (
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Goose, and the name of its directory in the
// home.
const Runtime = "goose"

// goose implements [render.Runtime] for Goose.
type goose struct{}

func init() { render.Register(goose{}) }

// Name is the target.runtime value.
func (goose) Name() string { return Runtime }

// Links are the hard links .agents/skills and .agents/agents, the second pointing at the
// agents directory the runtime writes, plus a soft AGENTS.md at the checkout root, left
// out when the compose produced no instructions.
func (goose) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".agents/skills", Home: "skills"},
		{Checkout: ".agents/agents", Home: Runtime + "/agents"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are commands, output styles, MCP servers and files: Goose has recipes instead of
// commands, no output styles, configures its extensions in the user's home, and has no
// directory in the checkout for a file to land in.
func (goose) Skips() []string { return []string{"commands", "output-styles", "mcp", "files"} }

// Reserved is the agents directory Render writes.
func (goose) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
	}
}

// Render writes agents/<name>.md per agent with the agent's name, description and model,
// and creates that directory even when there are no agents, so its link resolves. The
// target model reaches no file, because Goose has no project settings file.
func (goose) Render(res *compose.Result, dir, home string) error {
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		return err
	}
	return render.WriteAgents(res, dir, "agents", ".md", "name", "description", "model")
}
