package render_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// threeTiers maps each tier to a model and an effort, as a session's tier map does.
var threeTiers = map[string]stack.Tier{
	"light":    {Model: "example-small-model", Effort: "low", From: "--tier"},
	"fast":     {Model: "example-mid-model", Effort: "medium", From: "--tier"},
	"thinking": {Model: "example-big-model", Effort: "high", From: "--tier"},
}

// TestBuildWritesThreeTiersIntoAgentFiles composes the tiers-three-agents fixture, an
// agent per tier, with a model and an effort for each tier, and builds it for claude,
// copilot, cursor, opencode, goose and gemini. claude gets each tier's model and effort, copilot the model
// and reasoning-effort; cursor and opencode get the model and no effort, since they take
// one only inside model; goose gets the model and no effort, and gemini neither.
func TestBuildWritesThreeTiersIntoAgentFiles(t *testing.T) {
	cases := []struct {
		runtime string
		paths   []string
		want    map[string]string
	}{
		{"claude", []string{"claude/agents/%s.md", "claude/plugin/agents/%s.md"}, map[string]string{
			"explorer": "---\nname: explorer\ndescription: Finds the files a change touches.\nmodel: example-small-model\neffort: low\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\nname: reviewer\ndescription: Reviews a change for defects.\nmodel: example-mid-model\neffort: medium\n---\n\nReview the change and list defects.\n",
			"planner":  "---\nname: planner\ndescription: Plans a change.\nmodel: example-big-model\neffort: high\n---\n\nPlan the change.\n",
		}},
		{"copilot", []string{"copilot/agents/%s.agent.md", "copilot/workspace/.github/agents/%s.agent.md"}, map[string]string{
			"explorer": "---\ndescription: Finds the files a change touches.\nmodel: example-small-model\nname: explorer\nreasoning-effort: low\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\ndescription: Reviews a change for defects.\nmodel: example-mid-model\nname: reviewer\nreasoning-effort: medium\n---\n\nReview the change and list defects.\n",
			"planner":  "---\ndescription: Plans a change.\nmodel: example-big-model\nname: planner\nreasoning-effort: high\n---\n\nPlan the change.\n",
		}},
		{"cursor", []string{"cursor/agents/%s.md", "cursor/plugin/agents/%s.md"}, map[string]string{
			"explorer": "---\ndescription: Finds the files a change touches.\nmodel: example-small-model\nname: explorer\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\ndescription: Reviews a change for defects.\nmodel: example-mid-model\nname: reviewer\n---\n\nReview the change and list defects.\n",
			"planner":  "---\ndescription: Plans a change.\nmodel: example-big-model\nname: planner\n---\n\nPlan the change.\n",
		}},
		{"opencode", []string{"opencode/agents/%s.md"}, map[string]string{
			"explorer": "---\ndescription: Finds the files a change touches.\nmodel: example-small-model\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\ndescription: Reviews a change for defects.\nmodel: example-mid-model\n---\n\nReview the change and list defects.\n",
			"planner":  "---\ndescription: Plans a change.\nmodel: example-big-model\n---\n\nPlan the change.\n",
		}},
		{"goose", []string{"goose/agents/%s.md"}, map[string]string{
			"explorer": "---\ndescription: Finds the files a change touches.\nmodel: example-small-model\nname: explorer\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\ndescription: Reviews a change for defects.\nmodel: example-mid-model\nname: reviewer\n---\n\nReview the change and list defects.\n",
			"planner":  "---\ndescription: Plans a change.\nmodel: example-big-model\nname: planner\n---\n\nPlan the change.\n",
		}},
		{"gemini", []string{"gemini/agents/%s.md"}, map[string]string{
			"explorer": "---\ndescription: Finds the files a change touches.\nname: explorer\n---\n\nFind the files the change touches.\n",
			"reviewer": "---\ndescription: Reviews a change for defects.\nname: reviewer\n---\n\nReview the change and list defects.\n",
			"planner":  "---\ndescription: Plans a change.\nname: planner\n---\n\nPlan the change.\n",
		}},
	}
	for _, c := range cases {
		t.Run(c.runtime, func(t *testing.T) {
			hermetic(t)
			file, err := filepath.Abs("../../contracts/harness/v1/fixtures/tiers-three-agents/qory.yaml")
			if err != nil {
				t.Fatal(err)
			}
			p, err := config.Compose(file)
			if err != nil {
				t.Fatal(err)
			}
			p.Target.Runtimes = stack.Runtimes{c.runtime}
			res, err := compose.ComposeWith(p, compose.Options{Tiers: threeTiers})
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(t.TempDir(), ".qory", "harness")
			if err := render.Build(res, home, lookup(t, c.runtime)); err != nil {
				t.Fatal(err)
			}
			for _, path := range c.paths {
				for name, want := range c.want {
					rel := fmt.Sprintf(path, name)
					got, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
					if err != nil {
						t.Fatal(err)
					}
					if string(got) != want {
						t.Errorf("%s:\n%s\nwant:\n%s", rel, got, want)
					}
				}
			}
		})
	}
}
