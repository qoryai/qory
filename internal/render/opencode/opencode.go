// Package opencode renders for OpenCode, which reads a project's .opencode directory for
// agents, commands and hooks, opencode.json for its config, AGENTS.md at the checkout
// root, and skills from .agents/skills. All four are linked into the checkout,
// opencode.json only when a layer ships one, the profile names a model, or the compose
// holds an MCP server. The model is written into it as model and the servers as mcp, in
// OpenCode's own shape. OpenCode has no output styles, so that kind is skipped.
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
		{Checkout: ".opencode", Home: Runtime},
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

// Render links the commands and hooks as they are, since OpenCode reads the same Markdown
// with frontmatter, writes agents/<name>.md with the agent's description, mode and model,
// and writes opencode.json when a layer ships the file, the profile names a model or the
// compose holds an MCP server: the model as model, and each server under mcp as OpenCode
// reads it, {type: remote, url} for a server with a url, else {type: local, command:
// [command, args...], environment: env}.
func (opencode) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "commands", "hooks"); err != nil {
		return err
	}
	if err := render.WriteAgents(res, dir, "agents", ".md", "description", "mode", "model"); err != nil {
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
		if res.Profile.Target.Model != "" {
			m["model"] = res.Profile.Target.Model
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
	return res.Settings[Runtime]["opencode.json"] != nil || res.Profile.Target.Model != "" || len(res.MCP) > 0
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
