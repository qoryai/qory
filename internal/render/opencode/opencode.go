// Package opencode renders for OpenCode, which reads a project's .opencode directory for
// agents, commands and hooks, opencode.json for its config, AGENTS.md at the checkout
// root, and skills from .agents/skills. All four are linked into the checkout,
// opencode.json only when a module ships one, the stack sets a model, or the compose
// contains an MCP server. The model is written into it as model and the servers as mcp,
// in OpenCode's own shape. OpenCode has no output styles, so that kind is skipped. A
// files entry named opencode/<path> lands at .opencode/<path>.
//
// The paths under .opencode a files entry may not take, see [render.Reserved]:
//
//	opencode.json  qory writes it
//	commands       commands are linked there; ship it as commands/<name>
//	hooks          hooks are linked there; ship it as hooks/<name>
//	agents         agents are linked there; ship it as agents/<name>
//	skills         qory links them there for a launch; ship it as skills/<name>
//
// OpenCode also reads the directory at OPENCODE_CONFIG_DIR the way it reads a
// project's .opencode, and the runtime's directory is one: opencode.json, the agents,
// the commands, the hooks and the skills linked under skills/. [Template] sets the
// variable. The instructions are read from the checkout alone. The checkout's .opencode
// never links the skills, which it reads from .agents/skills.
package opencode

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for OpenCode, and the name of its directory in the
// home.
const Runtime = "opencode"

// opencode implements [render.Runtime] for OpenCode.
type opencode struct{}

func init() { render.Register(opencode{}) }

// Name is the target.runtime value.
func (opencode) Name() string { return Runtime }

// Links are the hard links .opencode, for the agents, commands and hooks, and
// .agents/skills, plus a soft opencode.json and a soft AGENTS.md at the checkout root.
// opencode.json is linked only when the runtime writes it, see [Render], and AGENTS.md
// only when the compose produced instructions.
func (opencode) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".opencode", Home: Runtime, Except: []string{"skills"}},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || writesConfig(res) {
		links = append(links, render.Link{Checkout: "opencode.json", Home: Runtime + "/opencode.json", Soft: true})
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are output styles, the one kind OpenCode has no place for.
func (opencode) Skips() []string { return []string{"output-styles"} }

// Reserved are opencode.json and the three kind directories Render links or writes.
func (opencode) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "opencode.json", Why: "qory writes it"},
		{Path: "commands", Why: "commands are linked there; ship it as commands/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "skills", Why: "qory links them there for a launch; ship it as skills/<name>"},
	}
}

// Render links the commands and hooks as they are, since OpenCode reads the same Markdown
// with frontmatter, writes agents/<name>.md with the agent's description, mode and model,
// and writes opencode.json when a module ships the file, the stack sets a model or the
// compose contains an MCP server: the model as model, and each server under mcp as
// OpenCode reads it, {type: remote, url} for a server with a url, else {type: local,
// command: [command, args...], environment: env}.
func (opencode) Render(res *compose.Result, dir, home string) error {
	if err := render.PlaceEntries(res, dir, render.Bare, "commands", "hooks", "skills"); err != nil {
		return err
	}
	if err := render.WriteAgents(res, render.Bare, dir, "agents", ".md", "description", "mode", "model"); err != nil {
		return err
	}
	var ensure []string
	if writesConfig(res) {
		ensure = []string{"opencode.json"}
	}
	return render.WriteSettings(res, Runtime, dir, home, ensure, func(file string, m map[string]any) {
		if file != "opencode.json" {
			return
		}
		if res.Stack.Target.Model != "" {
			m["model"] = res.Stack.Target.Model
		}
		servers := res.MCPFor(home)
		if servers == nil {
			return
		}
		own := map[string]any{}
		for name, v := range servers {
			own[name] = server(v.(map[string]any))
		}
		render.PutServers(m, "mcp", own)
	})
}

// writesConfig reports whether the runtime writes opencode.json for this compose.
func writesConfig(res *compose.Result) bool {
	return res.Settings[Runtime]["opencode.json"] != nil || res.Stack.Target.Model != "" || len(res.MCP) > 0
}

// server rewrites one MCP server object into OpenCode's shape.
func server(in map[string]any) map[string]any {
	if url, ok := in["url"].(string); ok {
		out := map[string]any{"type": "remote", "url": url}
		if h, ok := in["headers"]; ok {
			out["headers"] = h
		}
		return out
	}
	command := []any{}
	if c, ok := in["command"].(string); ok {
		command = append(command, c)
	}
	if args, ok := in["args"].([]any); ok {
		command = append(command, args...)
	}
	out := map[string]any{"type": "local", "command": command}
	if env, ok := in["env"]; ok {
		out["environment"] = env
	}
	return out
}

// Template starts OpenCode with the runtime's directory as its configuration directory.
func (opencode) Template() render.Template {
	return render.Template{Command: "opencode", Env: map[string]string{"OPENCODE_CONFIG_DIR": "${dir}"}}
}
