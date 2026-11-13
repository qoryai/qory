package render_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/render/codex"
	"github.com/qoryai/qory/internal/stack"
)

// TestWalledCodexDisablesSkillsTheHarnessDoesNotAllow composes a harness whose Claude
// Code settings allow two of its four skills, and a checkout with skills of its own in
// .codex/skills and .agents/skills, one of them in a plugin. The walled config.toml
// disables every other name Codex could load them under, and no allowed one.
func TestWalledCodexDisablesSkillsTheHarnessDoesNotAllow(t *testing.T) {
	files := withFile("modules/core/settings/claude/settings.json",
		`{"permissions": {"allow": ["Read", "Skill(harness:review)", "Skill(harness:visuals)", "Skill(other:notes)", "Skill(harness:*)", "Agent(harness:deploying)"]}}`)
	for _, name := range []string{"review", "visuals", "deploying", "notes"} {
		files["modules/core/skills/"+name+"/SKILL.md"] = "---\nname: " + name + "\ndescription: " + name + "\n---\n"
	}
	res, home := composeEnv(t, files, nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	allowed := codex.AllowedSkills(res)
	if want := []string{"review", "visuals"}; !reflect.DeepEqual(allowed, want) {
		t.Fatalf("allowed skills %v, want %v", allowed, want)
	}
	checkout := t.TempDir()
	for name, content := range map[string]string{
		".codex/skills/repo-only/SKILL.md":                  "---\nname: repo  tool\ndescription: d\n---\n",
		".agents/skills/visuals/SKILL.md":                   "---\nname: visuals\ndescription: d\n---\n",
		".agents/skills/pack/.codex-plugin/plugin.json":     `{"name": "pack"}`,
		".agents/skills/pack/skills/packed/SKILL.md":        "---\ndescription: no name\n---\n",
		".agents/skills/broken/SKILL.md":                    "---\nname: broken: yes\ndescription: d\n---\n",
		".agents/skills/group/.hidden/deep/hidden/SKILL.md": "---\nname: hidden\ndescription: d\n---\n",
	} {
		p := filepath.Join(checkout, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixed := t.TempDir()
	if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, codex.WalledRun{Projects: []string{"/workspace"}, Checkouts: []string{checkout}, Skills: allowed}); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Skills struct {
			Bundled struct{ Enabled bool }
			Config  []struct {
				Name    string
				Enabled bool
			}
		}
		Cloud struct{ Skills struct{ Enabled bool } }
	}
	if _, err := toml.DecodeFile(filepath.Join(fixed, "config.toml"), &config); err != nil {
		t.Fatal(err)
	}
	var disabled []string
	for _, e := range config.Skills.Config {
		if e.Enabled {
			t.Errorf("skills.config enables %s", e.Name)
		}
		disabled = append(disabled, e.Name)
	}
	want := []string{"broken", "broken: yes", "deploying", "hidden", "notes", "pack:packed", "packed", "repo tool", "repo-only"}
	if !reflect.DeepEqual(disabled, want) {
		t.Errorf("disabled skills %v, want %v", disabled, want)
	}
	if config.Skills.Bundled.Enabled || config.Cloud.Skills.Enabled {
		t.Error("the skills Codex ships or a cloud's skills are on")
	}
}

// TestWalledCodexReadRulesOnlyInAReadRun composes a module that ships
// settings/codex/read-access.rules: Build places it nowhere in the home and merges it into
// no config.toml. A walled read run's rules folder contains it, and a write run's does
// not.
func TestWalledCodexReadRulesOnlyInAReadRun(t *testing.T) {
	const rules = "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\", justification=\"read run: rm deletes or moves files\")\n"
	files := withFile("modules/core/settings/codex/read-access.rules", rules)
	res, home := composeEnv(t, files, nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	if string(res.CodexReadRules) != rules {
		t.Fatalf("read rules %q, want %q", res.CodexReadRules, rules)
	}
	err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == codex.ReadRulesName {
			t.Errorf("the home has %s", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if config := read(t, filepath.Join(home, "codex", "config.toml")); strings.Contains(config, "prefix_rule") {
		t.Errorf("config.toml contains the rules:\n%s", config)
	}
	for _, c := range []struct {
		access string
		want   bool
	}{{stack.AccessRead, true}, {stack.AccessWrite, false}} {
		t.Run(c.access, func(t *testing.T) {
			fixed := t.TempDir()
			run := codex.WalledRun{Projects: []string{"/workspace"}, Access: c.access, ReadRules: res.CodexReadRules}
			if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, run); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(fixed, "rules"))
			if err != nil {
				t.Fatal(err)
			}
			if !c.want {
				if len(entries) > 0 {
					t.Errorf("a %s run's rules folder has %v", c.access, entries)
				}
				return
			}
			if got := read(t, filepath.Join(fixed, "rules", codex.ReadRulesName)); got != rules {
				t.Errorf("%s %q, want %q", codex.ReadRulesName, got, rules)
			}
		})
	}
}
