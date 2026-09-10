// Package gemini renders for Gemini CLI, which reads a project's .gemini directory,
// holding its settings.json and the skills, agents, commands and hooks under it, and
// GEMINI.md at the checkout root. Both are linked into the checkout, and settings.json
// gets the target model as model.name and the MCP servers as mcpServers. Gemini CLI has
// no output styles, so that kind is skipped. A files entry named gemini/<path> lands at
// .gemini/<path>.
//
// The paths under .gemini a files entry may not take, see [render.Reserved]:
//
//	settings.json  qory writes it
//	skills         skills are linked there; ship it as skills/<name>
//	hooks          hooks are linked there; ship it as hooks/<name>
//	agents         agents are linked there; ship it as agents/<name>
//	commands       commands are linked there; ship it as commands/<name>
package gemini

import (
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Gemini CLI, and the name of its directory in
// the home.
const Runtime = "gemini"

// gemini implements [render.Runtime] for Gemini CLI.
type gemini struct{}

func init() { render.Register(gemini{}) }

// Name is the target.runtime value.
func (gemini) Name() string { return Runtime }

// Links are the hard link .gemini, for the settings, skills, hooks, agents and commands,
// and a soft GEMINI.md pointing at the composed instructions, left out when the compose
// produced none.
func (gemini) Links(res *compose.Result) []render.Link {
	links := []render.Link{{Checkout: ".gemini", Home: Runtime}}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "GEMINI.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are output styles, the one kind Gemini CLI has no place for.
func (gemini) Skips() []string { return []string{"output-styles"} }

// Reserved are settings.json, which Render writes on every compose, and the four kind
// directories it links or writes.
func (gemini) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "settings.json", Why: "qory writes it"},
		{Path: "skills", Why: "skills are linked there; ship it as skills/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "commands", Why: "commands are linked there; ship it as commands/<name>"},
	}
}

// Render links skills and hooks, writes settings.json with the target model as
// model.name and the MCP servers as mcpServers, one agents/<name>.md with the agent's
// name and description, and one commands/<name>.toml per command whose prompt has
// $ARGUMENTS rewritten to {{args}}. Gemini reads a project .gemini only in a folder the
// user has marked trusted.
func (gemini) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "skills", "hooks"); err != nil {
		return err
	}
	err := render.WriteSettings(res, Runtime, dir, home, []string{"settings.json"}, func(file string, m map[string]any) {
		if file != "settings.json" {
			return
		}
		render.PutServers(m, "mcpServers", res.MCPFor(home))
		if res.Stack.Target.Model == "" {
			return
		}
		model, _ := m["model"].(map[string]any)
		if model == nil {
			model = map[string]any{}
		}
		model["name"] = res.Stack.Target.Model
		m["model"] = model
	})
	if err != nil {
		return err
	}
	if err := render.WriteAgents(res, dir, "agents", ".md", "name", "description"); err != nil {
		return err
	}
	for _, e := range res.Entries {
		if e.Kind != "commands" {
			continue
		}
		doc, err := module.ReadDocument(e.Path)
		if err != nil {
			return err
		}
		cmd := map[string]any{"prompt": strings.ReplaceAll(strings.TrimRight(doc.Body, "\n"), "$ARGUMENTS", "{{args}}")}
		if d := doc.String("description"); d != "" {
			cmd["description"] = d
		}
		data, err := render.EncodeTOML(cmd)
		if err != nil {
			return err
		}
		if err := render.WriteFile(dir, filepath.Join("commands", e.Name+".toml"), data); err != nil {
			return err
		}
	}
	return nil
}
