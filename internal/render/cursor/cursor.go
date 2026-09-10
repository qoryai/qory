// Package cursor renders for Cursor and the Cursor CLI, which read a project's .cursor
// directory for agents, hooks and settings files such as hooks.json, cli.json and
// mcp.json, AGENTS.md at the checkout root, and skills from .agents/skills. All three are
// linked into the checkout. The model is a global CLI setting in Cursor and is not
// written, and Cursor folded commands into skills and has no output styles, so both kinds
// are skipped.
package cursor

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Cursor, and the name of its directory in the
// home.
const Runtime = "cursor"

// cursor implements [render.Runtime] for Cursor and the Cursor CLI.
type cursor struct{}

func init() { render.Register(cursor{}) }

// Name is the target.runtime value.
func (cursor) Name() string { return Runtime }

// Links are the hard links .cursor, for the agents, hooks and settings files, and
// .agents/skills, plus a soft AGENTS.md at the checkout root, left out when the compose
// produced no instructions.
func (cursor) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".cursor", Home: Runtime},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are commands and output styles, since Cursor folded commands into skills and has
// no output styles.
func (cursor) Skips() []string { return []string{"commands", "output-styles"} }

// Render links the hook scripts, writes agents/<name>.md with the agent's name,
// description and model, and writes the cursor settings files such as hooks.json,
// cli.json and mcp.json. The target model reaches no file, because Cursor keeps the model
// in a global CLI setting.
func (cursor) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "hooks"); err != nil {
		return err
	}
	if err := render.WriteAgents(res, dir, "agents", ".md", "name", "description", "model"); err != nil {
		return err
	}
	return render.WriteSettings(res, Runtime, dir, home, nil, nil)
}
