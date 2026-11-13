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
// Code settings allow two of its four skills, and a checkout with skills in
// .codex/skills and .agents/skills, one of them in a plugin and one named as an allowed
// harness skill. The walled config.toml disables every other name Codex could load them
// under, and no allowed one, and then every skill of the checkout by its path in the
// wall, the one with an allowed name included.
func TestWalledCodexDisablesSkillsTheHarnessDoesNotAllow(t *testing.T) {
	files := withFile("modules/core/settings/claude/settings.json",
		`{"permissions": {"allow": ["Read", "Skill(harness:review)", "Skill(harness:visuals)", "Skill(other:notes)", "Skill(harness:dep*)", "Agent(harness:deploying)"]}}`)
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
	fixed := filepath.Join(t.TempDir(), "fixed")
	if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, codex.WalledRun{Projects: []string{"/workspace"}, Checkouts: []string{checkout}, Skills: allowed, LinkRoots: res.SourceDirs()}); err != nil {
		t.Fatal(err)
	}
	disabled, paths, bundled, cloud := walledSkills(t, fixed)
	want := []string{"broken", "broken: yes", "deploying", "hidden", "notes", "pack:packed", "packed", "repo tool", "repo-only"}
	if !reflect.DeepEqual(disabled, want) {
		t.Errorf("disabled skills %v, want %v", disabled, want)
	}
	wantPaths := []string{
		"/workspace/.agents/skills/broken/SKILL.md",
		"/workspace/.agents/skills/group/.hidden/deep/hidden/SKILL.md",
		"/workspace/.agents/skills/pack/skills/packed/SKILL.md",
		"/workspace/.agents/skills/visuals/SKILL.md",
		"/workspace/.codex/skills/repo-only/SKILL.md",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("disabled skill paths %v, want %v", paths, wantPaths)
	}
	if bundled || cloud {
		t.Error("the skills Codex ships or a cloud's skills are on")
	}
}

// TestWalledCodexSkillsUnderAPatternOrNoRule composes a harness with two skills and a
// checkout with one skill. Skill(harness:*) allows both harness skills, and the
// checkout's skill is still disabled; a harness with no Skill(harness:...) rule disables
// all three. Either way the skills Codex ships and a cloud's skills are off.
func TestWalledCodexSkillsUnderAPatternOrNoRule(t *testing.T) {
	for _, c := range []struct {
		name, allow string
		allowed     []string
		disabled    []string
	}{
		{"every harness skill", `["Read", "Skill(harness:*)"]`, []string{codex.AllSkills}, []string{"repo-skill"}},
		{"no skill rule", `["Read", "Agent(harness:review)"]`, nil, []string{"repo-skill", "review", "visuals"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := withFile("modules/core/settings/claude/settings.json", `{"permissions": {"allow": `+c.allow+`}}`)
			for _, name := range []string{"review", "visuals"} {
				files["modules/core/skills/"+name+"/SKILL.md"] = "---\nname: " + name + "\ndescription: " + name + "\n---\n"
			}
			res, home := composeEnv(t, files, nil, "codex")
			if err := render.Build(res, home, lookup(t, "codex")); err != nil {
				t.Fatal(err)
			}
			allowed := codex.AllowedSkills(res)
			if !reflect.DeepEqual(allowed, c.allowed) {
				t.Fatalf("allowed skills %v, want %v", allowed, c.allowed)
			}
			checkout := t.TempDir()
			p := filepath.Join(checkout, ".agents/skills/repo-skill/SKILL.md")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("---\nname: repo-skill\ndescription: d\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			fixed := filepath.Join(t.TempDir(), "fixed")
			run := codex.WalledRun{Projects: []string{"/workspace"}, Checkouts: []string{checkout}, Skills: allowed, LinkRoots: res.SourceDirs()}
			if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, run); err != nil {
				t.Fatal(err)
			}
			disabled, paths, bundled, cloud := walledSkills(t, fixed)
			if !reflect.DeepEqual(disabled, c.disabled) {
				t.Errorf("disabled skills %v, want %v", disabled, c.disabled)
			}
			if want := []string{"/workspace/.agents/skills/repo-skill/SKILL.md"}; !reflect.DeepEqual(paths, want) {
				t.Errorf("disabled skill paths %v, want %v", paths, want)
			}
			if bundled || cloud {
				t.Error("the skills Codex ships or a cloud's skills are on")
			}
		})
	}
}

// TestWalledCodexCopiesLinkedSkillsAsFiles composes a harness from a local path whose
// skill review the home links to, with its SKILL.md: the walled home's copy has a folder
// and a regular SKILL.md with the module's text, since Codex passes over a SKILL.md that
// is a link. A copy that may follow no link into the module fails with the line that
// says so.
func TestWalledCodexCopiesLinkedSkillsAsFiles(t *testing.T) {
	const skill = "---\nname: review\ndescription: review\n---\nbody\n"
	res, home := composeEnv(t, withFile("modules/core/skills/review/SKILL.md", skill), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(home, "codex", "skills", "review")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the composed home's skills/review: %v, want a link", err)
	}
	fixed := filepath.Join(t.TempDir(), "fixed")
	if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, codex.WalledRun{LinkRoots: res.SourceDirs()}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(fixed, "skills", "review", "SKILL.md")
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s: %v %v, want a regular file", p, info, err)
	}
	if got := read(t, p); got != skill {
		t.Errorf("SKILL.md %q, want %q", got, skill)
	}
	_, err = codex.WriteWalledHome(filepath.Join(home, "codex"), filepath.Join(t.TempDir(), "fixed"), codex.WalledRun{})
	if err == nil || !strings.HasPrefix(err.Error(), "Remove the link `skills/review` from the harness: it leads to `") {
		t.Errorf("WriteWalledHome with no module folder: %v, want the refusal of a link outside", err)
	}
}

// TestWalledCodexRefusesALinkOutOfTheModules composes a harness whose skill review has
// a file that links to /etc/hosts: the walled home's copy fails with the line that
// contains the link and its target, though the module's folder is a link root.
func TestWalledCodexRefusesALinkOutOfTheModules(t *testing.T) {
	res, home := composeEnv(t, withFile("modules/core/skills/review/SKILL.md", "---\nname: review\ndescription: review\n---\n"), nil, "codex")
	if err := os.Symlink("/etc/hosts", filepath.Join(res.Modules[0].Dir, "skills", "review", "hosts")); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks("/etc/hosts")
	if err != nil {
		t.Skip("no /etc/hosts:", err)
	}
	_, err = codex.WriteWalledHome(filepath.Join(home, "codex"), filepath.Join(t.TempDir(), "fixed"), codex.WalledRun{LinkRoots: res.SourceDirs()})
	want := "Remove the link `skills/review/hosts` from the harness: it leads to `" + target + "`, outside the harness's modules, and a walled Codex run copies only files inside them."
	if err == nil || err.Error() != want {
		t.Errorf("WriteWalledHome: %v, want %q", err, want)
	}
}

// walledSkills reads the config.toml that WriteWalledHome wrote into fixed: the names
// and then the paths that skills.config disables, each in order, and whether bundled and
// cloud skills are on. Every name entry comes before every path entry.
func walledSkills(t *testing.T, fixed string) (disabled, paths []string, bundled, cloud bool) {
	t.Helper()
	var config struct {
		Skills struct {
			Bundled struct{ Enabled bool }
			Config  []struct {
				Name    string
				Path    string
				Enabled bool
			}
		}
		Cloud struct{ Skills struct{ Enabled bool } }
	}
	if _, err := toml.DecodeFile(filepath.Join(fixed, "config.toml"), &config); err != nil {
		t.Fatal(err)
	}
	for _, e := range config.Skills.Config {
		if e.Enabled {
			t.Errorf("skills.config enables %s%s", e.Name, e.Path)
		}
		if e.Path != "" {
			paths = append(paths, e.Path)
			continue
		}
		if len(paths) > 0 {
			t.Errorf("skills.config disables the name %s after a path", e.Name)
		}
		disabled = append(disabled, e.Name)
	}
	return disabled, paths, config.Skills.Bundled.Enabled, config.Cloud.Skills.Enabled
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
			fixed := filepath.Join(t.TempDir(), "fixed")
			run := codex.WalledRun{Projects: []string{"/workspace"}, Access: c.access, ReadRules: res.CodexReadRules, LinkRoots: res.SourceDirs()}
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
