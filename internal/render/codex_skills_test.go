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
// skill review the shared skills folder of the home links to: the Codex home has a
// folder and a regular SKILL.md with the module's text, since Codex passes over a
// SKILL.md that is a link, and the walled home's copy has the same. With no module
// folder to follow a link into, the walled copy still succeeds: the Codex home has no
// link.
func TestWalledCodexCopiesLinkedSkillsAsFiles(t *testing.T) {
	const skill = "---\nname: review\ndescription: review\n---\nbody\n"
	res, home := composeEnv(t, withFile("modules/core/skills/review/SKILL.md", skill), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(home, "skills", "review")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the composed home's skills/review: %v, want a link", err)
	}
	if info, err := os.Lstat(filepath.Join(home, "codex", "skills", "review")); err != nil || !info.IsDir() {
		t.Fatalf("the Codex home's skills/review: %v, want a folder", err)
	}
	fixed := filepath.Join(t.TempDir(), "fixed")
	if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), fixed, codex.WalledRun{LinkRoots: res.SourceDirs()}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(home, "codex", "skills", "review", "SKILL.md"), filepath.Join(fixed, "skills", "review", "SKILL.md")} {
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s: %v %v, want a regular file", p, info, err)
		}
		if got := read(t, p); got != skill {
			t.Errorf("%s %q, want %q", p, got, skill)
		}
	}
	if _, err := codex.WriteWalledHome(filepath.Join(home, "codex"), filepath.Join(t.TempDir(), "fixed"), codex.WalledRun{}); err != nil {
		t.Errorf("WriteWalledHome with no module folder: %v, want no error", err)
	}
}

// TestCodexHomeHasSkillsAsRegularFiles composes a skill whose guide.md has a reference
// and whose SKILL.md and script have none. The shared skills folder of the home has
// SKILL.md and the script as links, as for every runtime; the Codex home has them as
// regular files, with the module's text and the script's executable bits, and
// guide.md with its reference resolved. Nothing under the Codex home's skills is a link,
// so Codex 0.167.2 loads SKILL.md.
func TestCodexHomeHasSkillsAsRegularFiles(t *testing.T) {
	const skill = "---\nname: review\ndescription: review\n---\nRead guide.md.\n"
	files := withFile("modules/core/skills/review/SKILL.md", skill)
	files["modules/core/skills/review/guide.md"] = "Then run ${qory:skills/review}.\n"
	files["modules/core/skills/review/scripts/check.sh"] = "#!/bin/sh\n"
	res, home := composeEnv(t, files, nil, "codex")
	script := filepath.Join(res.Modules[0].Dir, "skills", "review", "scripts", "check.sh")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(home, "skills", "review", "SKILL.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the composed home's skills/review/SKILL.md: %v, want a link", err)
	}
	skills := filepath.Join(home, "codex", "skills")
	err := filepath.WalkDir(skills, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			t.Errorf("%s is a link, want a folder or a regular file", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(skills, "review", "SKILL.md")); got != skill {
		t.Errorf("SKILL.md %q, want %q", got, skill)
	}
	if got := read(t, filepath.Join(skills, "review", "guide.md")); got != "Then run review.\n" {
		t.Errorf("guide.md %q, want the reference resolved", got)
	}
	if info, err := os.Stat(filepath.Join(skills, "review", "scripts", "check.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("scripts/check.sh: %v %v, want mode 0755", info, err)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "codex")); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".skills-") {
				t.Errorf("the Codex home keeps the scratch folder %s", e.Name())
			}
		}
	}
}

// TestCodexHomeCopiesALinkIntoAnotherModule composes two modules, where a file of the
// skill review in core links to a file of the module shared: the Codex home has the file
// as a regular file with the text of shared's file.
func TestCodexHomeCopiesALinkIntoAnotherModule(t *testing.T) {
	files := map[string]string{
		"qory.yaml":                           "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n    - name: shared\n      source: {path: modules/shared}\n",
		"modules/core/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"modules/core/skills/review/SKILL.md": "---\nname: review\ndescription: review\n---\n",
		"modules/shared/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: shared\n",
		"modules/shared/docs/style.md":        "shared style\n",
	}
	res, home := composeEnv(t, files, nil, "codex")
	var core, shared string
	for _, m := range res.Modules {
		switch m.Name {
		case "core":
			core = m.Dir
		case "shared":
			shared = m.Dir
		}
	}
	if err := os.Symlink(filepath.Join(shared, "docs", "style.md"), filepath.Join(core, "skills", "review", "style.md")); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "codex", "skills", "review", "style.md")
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s: %v, want a regular file", p, err)
	}
	if got := read(t, p); got != "shared style\n" {
		t.Errorf("style.md %q, want the text of shared's file", got)
	}
}

// TestCodexRefusesALinkOutOfTheModules composes a harness whose skill review has a file
// that links to /etc/hosts: the compose for Codex fails with the line that contains the
// link and its target, and writes no home.
func TestCodexRefusesALinkOutOfTheModules(t *testing.T) {
	res, home := composeEnv(t, withFile("modules/core/skills/review/SKILL.md", "---\nname: review\ndescription: review\n---\n"), nil, "codex")
	if err := os.Symlink("/etc/hosts", filepath.Join(res.Modules[0].Dir, "skills", "review", "hosts")); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks("/etc/hosts")
	if err != nil {
		t.Skip("no /etc/hosts:", err)
	}
	err = render.Build(res, home, lookup(t, "codex"))
	want := "Remove the link `skills/review/hosts` from the harness: it leads to `" + target + "`, outside the harness's modules, and a walled Codex run copies only files inside them."
	if err == nil || err.Error() != want {
		t.Errorf("Build: %v, want %q", err, want)
	}
	if _, err := os.Lstat(home); err == nil {
		t.Errorf("Build wrote the home %s", home)
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
