// Package cursor renders for Cursor and the Cursor CLI, which read a project's .cursor
// directory for agents, hooks and settings files such as hooks.json, cli.json and
// mcp.json, AGENTS.md at the checkout root, and skills from .agents/skills. All three are
// linked into the checkout, and the MCP servers go into mcp.json as mcpServers. The model
// is a global CLI setting in Cursor and is not written, and Cursor folded commands into
// skills and has no output styles, so both kinds are skipped. A files entry named
// cursor/<path> lands at .cursor/<path>, which is how a module ships a rule as
// .cursor/rules/nextjs-15.mdc.
//
// The paths under .cursor a files entry may not take, see [render.Reserved]:
//
//	mcp.json          Cursor reads it as settings; a module sets those through settings/cursor/mcp.json
//	hooks.json        Cursor reads it as settings; a module sets those through settings/cursor/hooks.json
//	cli.json          Cursor reads it as settings; a module sets those through settings/cursor/cli.json
//	environment.json  Cursor reads it as settings; a module sets those through settings/cursor/environment.json
//	agents            agents are linked there; ship it as agents/<name>
//	hooks             hooks are linked there; ship it as hooks/<name>
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

// Reserved are the four settings files Cursor reads from .cursor, which a settings
// fragment writes, and the agents and hooks directories Render writes.
func (cursor) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "mcp.json", Why: "Cursor reads it as settings; a module sets those through settings/cursor/mcp.json"},
		{Path: "hooks.json", Why: "Cursor reads it as settings; a module sets those through settings/cursor/hooks.json"},
		{Path: "cli.json", Why: "Cursor reads it as settings; a module sets those through settings/cursor/cli.json"},
		{Path: "environment.json", Why: "Cursor reads it as settings; a module sets those through settings/cursor/environment.json"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
	}
}

// Render links the hook scripts, writes agents/<name>.md with the agent's name,
// description and model, and writes the cursor settings files such as hooks.json,
// cli.json and mcp.json, the last with the MCP servers under mcpServers when the compose
// holds any. The target model reaches no file, because Cursor keeps the model in a global
// CLI setting.
func (cursor) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "hooks"); err != nil {
		return err
	}
	if err := render.WriteAgents(res, dir, "agents", ".md", "name", "description", "model"); err != nil {
		return err
	}
	var ensure []string
	if len(res.MCP) > 0 {
		ensure = []string{"mcp.json"}
	}
	return render.WriteSettings(res, Runtime, dir, home, ensure, func(file string, m map[string]any) {
		if file == "mcp.json" {
			render.PutServers(m, "mcpServers", res.MCPFor(home))
		}
	})
}
