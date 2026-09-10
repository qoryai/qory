// Package copilot renders for GitHub Copilot CLI, which reads agents from .github/agents,
// hook files from .github/hooks, AGENTS.md at the checkout root, and skills from
// .agents/skills. All four are linked into the checkout, the two under .github softly, so
// a repository's own agents and hooks keep their place. The model is a user setting in
// Copilot and is not written, and Copilot CLI reads no project prompt files and has no
// output styles, so commands and output styles are skipped.
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
// runtime's directory, a hard .agents/skills, and a soft AGENTS.md at the checkout root.
// The two under .github are soft because a repository commonly owns them, and AGENTS.md
// is left out when the compose produced no instructions.
func (copilot) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".github/agents", Home: Runtime + "/agents", Soft: true},
		{Checkout: ".github/hooks", Home: Runtime + "/hooks", Soft: true},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are commands and output styles, since Copilot CLI reads no project prompt files
// and has no output styles.
func (copilot) Skips() []string { return []string{"commands", "output-styles"} }

// Render writes agents/<name>.agent.md per agent with the agent's name, description,
// tools and model, and the copilot settings files, which are the hook files under hooks/.
// Both directories are created even when nothing goes in them, so their links resolve.
// The target model reaches no file, because Copilot keeps the model in a user setting.
func (copilot) Render(res *compose.Result, dir, home string) error {
	for _, sub := range []string{"agents", "hooks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := render.WriteAgents(res, dir, "agents", ".agent.md", "name", "description", "tools", "model"); err != nil {
		return err
	}
	return render.WriteSettings(res, Runtime, dir, home, nil, nil)
}
