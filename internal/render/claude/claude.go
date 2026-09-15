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
// Claude Code also takes a harness from outside the checkout, and the runtime's directory
// holds the launch spec for that: plugin/ is a plugin in Claude Code's own layout, the
// skills, agents, commands and output styles under a .claude-plugin/plugin.json, which
// --plugin-dir loads for one session; settings.json goes to --settings, mcp.json to
// --mcp-config and CLAUDE.md to --append-system-prompt-file. [Template] holds those
// arguments, and the plugin is never linked into the checkout.
//
// The paths under .claude a files entry may not take, see [render.Reserved]:
//
//	CLAUDE.md            qory writes it
//	settings.json        qory writes it
//	mcp.json             qory writes it
//	plugin               qory writes the plugin there
//	settings.local.json  Claude Code reads it as settings; a module sets those through settings/claude/settings.json
//	skills               skills are linked there; ship it as skills/<name>
//	agents               agents are linked there; ship it as agents/<name>
//	commands             commands are linked there; ship it as commands/<name>
//	hooks                hooks are linked there; ship it as hooks/<name>
//	output-styles        output-styles are linked there; ship it as output-styles/<name>
package claude

import (
	"path/filepath"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Claude Code, and the name of its directory in
// the home.
const Runtime = "claude"

// Plugin is the plugin's directory under the runtime's directory in the home, what
// --plugin-dir names.
const Plugin = "plugin"

// claude implements [render.Runtime] for Claude Code.
type claude struct{}

func init() { render.Register(claude{}) }

// Name is the target.runtime value.
func (claude) Name() string { return Runtime }

// Links are the hard link .claude, a directory link so the checkout's own files in it
// stay, without the plugin, which a checkout has no use for, and a soft .mcp.json at the
// checkout root, left out when the runtime writes no mcp.json, see [Render].
func (claude) Links(res *compose.Result) []render.Link {
	links := []render.Link{{Checkout: ".claude", Home: Runtime, Except: []string{Plugin}}}
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

// Reserved are the three files Render writes, the plugin directory, the settings file
// Claude Code writes and reads beside them, and the five kind directories Render links.
func (claude) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "CLAUDE.md", Why: "qory writes it"},
		{Path: "settings.json", Why: "qory writes it"},
		{Path: "mcp.json", Why: "qory writes it"},
		{Path: Plugin, Why: "qory writes the plugin there"},
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
// on every start. Then it writes the plugin, see [Plugin].
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
	return renderPlugin(res, filepath.Join(dir, Plugin))
}

// renderPlugin writes the plugin into dir: .claude-plugin/plugin.json naming it, and the
// skills, agents and commands linked under the directories Claude Code reads in a plugin,
// with the output styles under output-styles and the manifest pointing there when the
// compose holds one. The hooks, the servers, the settings and the instructions are not
// in the plugin: they reach the session through the settings and files [Template] names,
// the same ones the checkout's links point at, so nothing is rendered twice.
func renderPlugin(res *compose.Result, dir string) error {
	extra := map[string]any{}
	for _, e := range res.Entries {
		if e.Kind == "output-styles" {
			extra["outputStyles"] = "./output-styles"
		}
	}
	if extra["outputStyles"] != nil {
		if err := render.LinkEntries(res, dir, "output-styles"); err != nil {
			return err
		}
	}
	return render.WritePlugin(res, dir, ".claude-plugin", extra)
}

// Template starts Claude Code with the harness in a home: the plugin for the skills,
// agents, commands and output styles, settings.json for the permissions, the hooks, the
// environment and the model, mcp.json for the servers when the compose wrote one,
// CLAUDE.md appended to the system prompt when the compose produced instructions, and
// the setting sources cut to the user's, so no .claude of the checkout or a directory
// above it is read.
func (claude) Template() render.Template {
	return render.Template{
		Command: "claude",
		Args: [][]string{
			{"--plugin-dir", "${dir}/" + Plugin},
			{"--settings", "${dir}/settings.json"},
			{"--mcp-config", "${dir}/mcp.json"},
			{"--append-system-prompt-file", "${dir}/CLAUDE.md"},
			{"--setting-sources", "user"},
		},
	}
}
