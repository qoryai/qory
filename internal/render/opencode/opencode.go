// Package opencode renders for OpenCode, which reads a project's .opencode directory for
// agents, commands and hooks, opencode.json for its config, AGENTS.md at the checkout
// root, and skills from .agents/skills. All four are linked into the checkout,
// opencode.json only when a layer ships one or the profile names a model, which is
// written into it as model. OpenCode has no output styles, so that kind is skipped.
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
// opencode.json is linked only when a layer ships one or the profile names a model, and
// AGENTS.md only when the compose produced instructions.
func (opencode) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".opencode", Home: Runtime},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Settings[Runtime]["opencode.json"] != nil || res.Profile.Target.Model != "" {
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
// and writes opencode.json with the target model as model when a layer ships the file or
// the profile names a model.
func (opencode) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "commands", "hooks"); err != nil {
		return err
	}
	if err := render.WriteAgents(res, dir, "agents", ".md", "description", "mode", "model"); err != nil {
		return err
	}
	var ensure []string
	if res.Profile.Target.Model != "" {
		ensure = []string{"opencode.json"}
	}
	return render.WriteSettings(res, Runtime, dir, home, ensure, func(file string, m map[string]any) {
		if file == "opencode.json" && res.Profile.Target.Model != "" {
			m["model"] = res.Profile.Target.Model
		}
	})
}
