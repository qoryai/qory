// Package claude renders for Claude Code, which reads a project's .claude directory: the
// skills, agents, commands, output styles and hooks under it, its settings.json, and
// CLAUDE.md beside them. The checkout gets one link, .claude, pointing at the runtime's
// directory in the home, and the target model is written into that settings.json as
// model. Claude Code has a place for every entry kind, so nothing is skipped.
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

// Links is the single hard link .claude, which stands for the whole runtime directory:
// the checkout may hold no real .claude of its own.
func (claude) Links(*compose.Result) []render.Link {
	return []render.Link{{Checkout: ".claude", Home: Runtime}}
}

// Skips is empty: Claude Code has a place for every kind.
func (claude) Skips() []string { return nil }

// Render links every atomic kind, writes settings.json with env.QORY_HARNESS_HOME and the
// model, and the instructions as CLAUDE.md. The instructions are written in full rather
// than imported from AGENTS.md: Claude Code resolves the .claude link to its real path
// and treats an import found through it as external, which it asks about on every start.
func (claude) Render(res *compose.Result, dir, home string) error {
	if err := render.LinkEntries(res, dir, "skills", "agents", "commands", "output-styles", "hooks"); err != nil {
		return err
	}
	err := render.WriteSettings(res, Runtime, dir, home, []string{"settings.json"}, func(file string, m map[string]any) {
		if file != "settings.json" {
			return
		}
		env, _ := m["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		env["QORY_HARNESS_HOME"] = home
		m["env"] = env
		if res.Profile.Target.Model != "" {
			m["model"] = res.Profile.Target.Model
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
