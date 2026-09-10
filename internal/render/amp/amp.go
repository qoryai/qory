// Package amp renders for Amp, which reads a project's .amp directory for its settings,
// AGENTS.md at the checkout root, and skills from .agents/skills. All three are linked
// into the checkout. Amp picks the model through its own modes, so the target model is
// not written, and it defines agents, commands and output styles through plugins rather
// than project files, so those three kinds are skipped.
package amp

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Amp, and the name of its directory in the home.
const Runtime = "amp"

// amp implements [render.Runtime] for Amp.
type amp struct{}

func init() { render.Register(amp{}) }

// Name is the target.runtime value.
func (amp) Name() string { return Runtime }

// Links are the hard links .amp, for the settings, and .agents/skills, plus a soft
// AGENTS.md at the checkout root, left out when the compose produced no instructions.
func (amp) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".amp", Home: Runtime},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are agents, commands and output styles, which Amp defines through plugins rather
// than project files.
func (amp) Skips() []string { return []string{"agents", "commands", "output-styles"} }

// Render writes the amp settings files, such as settings.json, and nothing else. The
// target model reaches no file, because Amp picks the model through its own modes.
func (amp) Render(res *compose.Result, dir, home string) error {
	return render.WriteSettings(res, Runtime, dir, home, nil, nil)
}
