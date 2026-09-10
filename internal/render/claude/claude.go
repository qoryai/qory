// Package claude renders for Claude Code, which reads a project's .claude directory: the
// skills, agents, commands, output styles and hooks under it, its settings.json, and
// CLAUDE.md beside them, and .mcp.json at the checkout root for the MCP servers. The
// checkout gets .claude, a real directory holding one link per file of the runtime's
// directory in the home, so Claude Code's own settings.local.json stays beside them, and
// a soft .mcp.json when the compose holds a server. The target model is written into
// settings.json as model. Claude Code has a place for every entry kind, so nothing is
// skipped, and a files entry named claude/<path> lands at .claude/<path>, which is how a
// module ships .claude/rules/nextjs-15.md.
//
// The paths under .claude a files entry may not take, see [render.Reserved]:
//
//	CLAUDE.md            qory writes it
//	settings.json        qory writes it
//	mcp.json             qory writes it
//	settings.local.json  Claude Code reads it as settings; a module sets those through settings/claude/settings.json
//	skills               skills are linked there; ship it as skills/<name>
//	agents               agents are linked there; ship it as agents/<name>
//	commands             commands are linked there; ship it as commands/<name>
//	hooks                hooks are linked there; ship it as hooks/<name>
//	output-styles        output-styles are linked there; ship it as output-styles/<name>
package claude

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Claude Code, and the name of its directory in
// the home.
const Runtime = "claude"

// claude implements [render.Runtime] for Claude Code.
type claude struct{}

func init() { render.Register(claude{}) }

// Name is the target.runtime value.
func (claude) Name() string { return Runtime }

// Links are the hard link .claude, a directory link so the checkout's own files in it
// stay, and a soft .mcp.json at the checkout root, left out when the runtime writes no
// mcp.json, see [Render].
func (claude) Links(res *compose.Result) []render.Link {
	links := []render.Link{{Checkout: ".claude", Home: Runtime}}
	if res == nil || writesMCP(res) {
		links = append(links, render.Link{Checkout: ".mcp.json", Home: Runtime + "/mcp.json", Soft: true})
	}
	return links
}

// writesMCP reports whether the runtime writes mcp.json for this compose: when the compose
// holds a server, or a module ships a settings/claude/mcp.json fragment.
func writesMCP(res *compose.Result) bool {
	return len(res.MCP) > 0 || res.Settings[Runtime]["mcp.json"] != nil
}

// Skips is empty: Claude Code has a place for every kind.
func (claude) Skips() []string { return nil }

// Reserved are the three files Render writes, the settings file Claude Code writes and
// reads beside them, and the five kind directories Render links.
func (claude) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "CLAUDE.md", Why: "qory writes it"},
		{Path: "settings.json", Why: "qory writes it"},
		{Path: "mcp.json", Why: "qory writes it"},
		{Path: "settings.local.json", Why: "Claude Code reads it as settings; a module sets those through settings/claude/settings.json"},
		{Path: "skills", Why: "skills are linked there; ship it as skills/<name>"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "commands", Why: "commands are linked there; ship it as commands/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
		{Path: "output-styles", Why: "output-styles are linked there; ship it as output-styles/<name>"},
	}
}

// Render links every atomic kind, writes settings.json with the exported variables and
// QORY_HARNESS_HOME under env and the model, mcp.json with the MCP servers under mcpServers on top of any
// settings/claude/mcp.json fragment, and the instructions as CLAUDE.md. The instructions
// are written in full rather than imported from AGENTS.md: Claude Code resolves a link to
// its real path and treats an import found through it as external, which it asks about
// on every start.
func (claude) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "skills", "agents", "commands", "output-styles", "hooks"); err != nil {
		return err
	}
	ensure := []string{"settings.json"}
	if writesMCP(res) {
		ensure = append(ensure, "mcp.json")
	}
	err := render.WriteSettings(res, Runtime, dir, home, ensure, func(file string, m map[string]any) {
		if file == "mcp.json" {
			render.PutServers(m, "mcpServers", res.MCPFor(home))
		}
		if file != "settings.json" {
			return
		}
		m["env"] = render.Env(m["env"], res, home)
		if res.Stack.Target.Model != "" {
			m["model"] = res.Stack.Target.Model
		}
	})
	if err != nil {
		return err
	}
	if res.Instructions != "" {
		if err := render.WriteFile(dir, "CLAUDE.md", []byte(res.Instructions)); err != nil {
			return err
		}
	}
	return nil
}
