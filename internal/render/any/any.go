// Package any renders for a program that follows the AGENTS.md convention and has no
// package of its own, reading the instructions from AGENTS.md at the checkout root and
// skills from .agents/skills. Both are linked into the checkout, and the package writes
// nothing of its own, because the home's root already holds them. The convention names no
// place for a model, so the target model is not written, and none for agents, commands,
// output styles, hooks or MCP servers, so those five kinds are skipped.
package any

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for a program with no package of its own.
const Runtime = "any"

// runtime implements [render.Runtime] for the AGENTS.md convention.
type runtime struct{}

func init() { render.Register(runtime{}) }

// Name is the target.runtime value.
func (runtime) Name() string { return Runtime }

// Links are a hard .agents/skills and a soft AGENTS.md at the checkout root, the second
// left out when the compose produced no instructions.
func (runtime) Links(res *compose.Result) []render.Link {
	links := []render.Link{{Checkout: ".agents/skills", Home: "skills"}}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are the five kinds the convention names no place for: agents, commands, output
// styles, hooks and MCP servers.
func (runtime) Skips() []string {
	return []string{"agents", "commands", "output-styles", "hooks", "mcp"}
}

// Render writes nothing of its own, because the home's root already holds AGENTS.md and
// the skills the links point at.
func (runtime) Render(*compose.Result, string, string) error { return nil }
