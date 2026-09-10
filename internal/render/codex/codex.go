// Package codex renders for Codex CLI, which reads a project's .codex directory, skills
// from .agents/skills, and AGENTS.override.md ahead of AGENTS.md. The checkout gets a
// link for each of those three, and .codex holds config.toml with the target model as
// model plus one TOML file per agent. Codex reads prompt files from the user's home only
// and has no output styles, so commands and output styles are skipped.
package codex

import (
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/layer"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Codex CLI, and the name of its directory in the
// home.
const Runtime = "codex"

// codex implements [render.Runtime] for Codex CLI.
type codex struct{}

func init() { render.Register(codex{}) }

// Name is the target.runtime value.
func (codex) Name() string { return Runtime }

// Links are the hard links .codex, for config.toml and the agents, and .agents/skills,
// plus a soft AGENTS.override.md for the instructions. Codex reads the override file
// ahead of AGENTS.md, which leaves a repository's own AGENTS.md in place beside it. The
// instructions link is left out when the compose produced none.
func (codex) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".codex", Home: Runtime},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.override.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are commands and output styles, since Codex reads prompt files from the user's
// home only and has no output styles.
func (codex) Skips() []string { return []string{"commands", "output-styles"} }

// Render writes config.toml, with the target model as model, and any other codex settings
// fragment, then one agents/<name>.toml per agent carrying the agent's name, description
// and its body as developer_instructions. Codex reads a project .codex only in a project
// the user has marked trusted.
func (codex) Render(res *compose.Result, dir, home string) error {
	err := render.WriteSettings(res, Runtime, dir, home, []string{"config.toml"}, func(file string, m map[string]any) {
		if file == "config.toml" && res.Profile.Target.Model != "" {
			m["model"] = res.Profile.Target.Model
		}
	})
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		if e.Kind != "agents" {
			continue
		}
		doc, err := layer.ReadDocument(e.Path)
		if err != nil {
			return err
		}
		name := doc.String("name")
		if name == "" {
			name = e.Name
		}
		data, err := render.EncodeTOML(map[string]any{
			"name":                   name,
			"description":            doc.String("description"),
			"developer_instructions": strings.TrimRight(doc.Body, "\n"),
		})
		if err != nil {
			return err
		}
		if err := render.WriteFile(dir, filepath.Join("agents", e.Name+".toml"), data); err != nil {
			return err
		}
	}
	return nil
}
