// Package copilot renders for GitHub Copilot CLI, which reads agents from .github/agents,
// hook files from .github/hooks, AGENTS.md at the checkout root, and skills from
// .agents/skills. All four are linked into the checkout, the two under .github softly, so
// a repository's own agents and hooks keep their place. The model is a user setting in
// Copilot and is not written, and Copilot CLI reads no project prompt files and has no
// output styles, and reads MCP servers from the user's home, so commands, output styles
// and MCP servers are skipped. .github is the repository's, so a files entry named
// copilot/<path> is not reached through a directory link: each gets a soft link of its
// own at .github/<path>, which is how a module ships .github/copilot-instructions.md or
// .github/instructions/web.instructions.md.
//
// The paths under .github a files entry may not take, see [render.Reserved]:
//
//	agents     agents are linked there; ship it as agents/<name>
//	hooks      hooks are linked there; ship it as hooks/<name>
//	workflows  GitHub runs it on every push
package copilot

import (
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for GitHub Copilot CLI, and the name of its
// directory in the home.
const Runtime = "copilot"

// copilot implements [render.Runtime] for GitHub Copilot CLI.
type copilot struct{}

func init() { render.Register(copilot{}) }

// Name is the target.runtime value.
func (copilot) Name() string { return Runtime }

// Links are soft .github/agents and .github/hooks, into the subdirectories of the
// runtime's directory, a hard .agents/skills, a soft AGENTS.md at the checkout root, and
// one soft .github/<path> per files entry of the runtime, pointing at copilot/<path>.
// Everything under .github is soft because a repository commonly owns it, and AGENTS.md
// is left out when the compose produced no instructions. A nil res lists no files links,
// because only a compose knows them; [render.LinkInto] and [render.Unlink] find the ones
// an earlier compose wrote by their targets.
func (copilot) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".github/agents", Home: Runtime + "/agents", Soft: true},
		{Checkout: ".github/hooks", Home: Runtime + "/hooks", Soft: true},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	if res != nil {
		for _, e := range res.Entries {
			if path, ok := render.FileFor(e, Runtime); ok {
				links = append(links, render.Link{Checkout: ".github/" + path, Home: Runtime + "/" + path, Soft: true})
			}
		}
	}
	return links
}

// Skips are commands, output styles and MCP servers: Copilot CLI reads no project prompt
// files, has no output styles, and reads its MCP configuration from the user's home.
func (copilot) Skips() []string { return []string{"commands", "output-styles", "mcp"} }

// Reserved are the agents and hooks directories Render writes, and workflows, where a
// file would be a GitHub Actions workflow the repository did not commit.
func (copilot) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
		{Path: "workflows", Why: "GitHub runs it on every push"},
	}
}

// Render writes agents/<name>.agent.md per agent with the agent's name, description,
// tools and model, and the settings/copilot/ files into hooks/, where Copilot reads its
// hook files, so settings/copilot/hooks.json reaches .github/hooks/hooks.json. Both
// directories are created even when nothing goes in them. The target model reaches no
// file, because Copilot keeps the model in a user setting.
func (copilot) Render(res *compose.Result, dir, home string) error {
	for _, sub := range []string{"agents", "hooks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := render.WriteAgents(res, dir, "agents", ".agent.md", "name", "description", "tools", "model"); err != nil {
		return err
	}
	return render.WriteSettings(res, Runtime, filepath.Join(dir, "hooks"), home, nil, nil)
}
