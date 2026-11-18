package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
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

// linkedStack is a stack for Codex over module core, whose skill review has a SKILL.md
// and links into the module's lib folder: lib with a script, a private file and a .git
// folder.
var linkedStack = map[string]string{
	"qory.yaml":                           "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n",
	"modules/core/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: core\n",
	"modules/core/skills/review/SKILL.md": "---\nname: review\ndescription: review\n---\nRead lib.\n",
	"modules/core/lib/check.sh":           "#!/bin/sh\n",
	"modules/core/lib/private.txt":        "mine\n",
	"modules/core/lib/.git/HEAD":          "ref: refs/heads/main\n",
}

// composeLinked writes files and then links, each a path in the stack's folder to the
// target as written, and composes the stack for Codex with opts. It returns the result,
// a home in a fresh checkout, and the compose's error.
func composeLinked(t *testing.T, files, links map[string]string, opts compose.Options) (*compose.Result, string, error) {
	t.Helper()
	hermetic(t)
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range map[string]os.FileMode{"modules/core/lib/check.sh": 0o775, "modules/core/lib/private.txt": 0o600} {
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	p, err := config.Compose(filepath.Join(dir, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = []string{"codex"}
	res, err := compose.ComposeWith(p, opts)
	if res != nil {
		t.Cleanup(res.Close)
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return res, filepath.Join(root, ".qory", "harness"), err
}

// TestCodexHomeCopiesALinkInsideTheModule composes a skill with a link to a folder
// elsewhere in its module, skills/review/lib -> ../../lib, for a home that links the
// module and for one that holds a copy of it: the Codex home has lib as a folder of
// regular files, each with its permission bits without write for group and others, and
// without the .git folder.
func TestCodexHomeCopiesALinkInsideTheModule(t *testing.T) {
	for _, c := range []struct {
		name string
		opts compose.Options
	}{
		{"linked", compose.Options{}},
		{"copied", compose.Options{Copies: func(string) bool { return true }}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, home, err := composeLinked(t, linkedStack, map[string]string{"modules/core/skills/review/lib": "../../lib"}, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Modules[0].Copy; got != (c.opts.Copies != nil) {
				t.Fatalf("module core Copy %v", got)
			}
			if err := render.Build(res, home, lookup(t, "codex")); err != nil {
				t.Fatal(err)
			}
			lib := filepath.Join(home, "codex", "skills", "review", "lib")
			if info, err := os.Lstat(lib); err != nil || !info.IsDir() {
				t.Fatalf("%s: %v, want a folder", lib, err)
			}
			for name, mode := range map[string]os.FileMode{"check.sh": 0o755, "private.txt": 0o600} {
				info, err := os.Lstat(filepath.Join(lib, name))
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
					t.Errorf("lib/%s: %v %v, want a regular file of mode %v", name, info, err, mode)
				}
			}
			if _, err := os.Lstat(filepath.Join(lib, ".git")); !os.IsNotExist(err) {
				t.Errorf("lib/.git: %v, want it left out", err)
			}
		})
	}
}

// TestComposeRefusesALinkIntoAnotherModule composes two modules, where a file of the
// skill review in core links to a file of the module shared: the read of core refuses
// the link, before any home is built.
func TestComposeRefusesALinkIntoAnotherModule(t *testing.T) {
	files := map[string]string{
		"qory.yaml":                           "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n    - name: shared\n      source: {path: modules/shared}\n",
		"modules/core/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"modules/core/skills/review/SKILL.md": "---\nname: review\ndescription: review\n---\n",
		"modules/shared/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: shared\n",
		"modules/shared/docs/style.md":        "shared style\n",
	}
	_, _, err := composeLinked(t, files, map[string]string{"modules/core/skills/review/style.md": "../../../shared/docs/style.md"}, compose.Options{})
	want := "module core: Remove the link skills/review/style.md or point it inside the module."
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("compose: %v, want %q", err, want)
	}
}

// TestCodexRefusesALinkThatContainsItself composes a skill with a link to the skill's folder
// and one with a link to the module's folder, which the read of the module accepts: the
// build for Codex fails, since the copy would never end.
func TestCodexRefusesALinkThatContainsItself(t *testing.T) {
	for name, target := range map[string]string{"self": ".", "root": "../.."} {
		t.Run(name, func(t *testing.T) {
			res, home, err := composeLinked(t, linkedStack, map[string]string{"modules/core/skills/review/" + name: target}, compose.Options{})
			if err != nil {
				t.Fatal(err)
			}
			err = render.Build(res, home, lookup(t, "codex"))
			if err == nil || !strings.HasSuffix(err.Error(), "for the Codex home: the folder contains itself through a link") {
				t.Errorf("Build: %v, want the refusal of a folder that contains itself", err)
			}
		})
	}
}

// TestCodexSkillCheckFollowsTheModule builds a Codex home, then changes the module's
// SKILL.md and removes another file of the skill: Diff of a new build and the home
// reports the copy of SKILL.md changed and the removed file's copy extra, as
// qory harness compose --check does. A build after the change leaves no difference.
func TestCodexSkillCheckFollowsTheModule(t *testing.T) {
	files := withFile("modules/core/skills/review/SKILL.md", "---\nname: review\ndescription: review\n---\nbody\n")
	files["modules/core/skills/review/notes.md"] = "notes\n"
	res, home := composeEnv(t, files, nil, "codex")
	codexRT := lookup(t, "codex")
	if err := render.Build(res, home, codexRT); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(res.Modules[0].Dir, "skills", "review")
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: review\ndescription: review\n---\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(skill, "notes.md")); err != nil {
		t.Fatal(err)
	}
	diff := func() []render.Difference {
		t.Helper()
		stage := t.TempDir()
		if err := render.BuildAt(res, stage, home, codexRT); err != nil {
			t.Fatal(err)
		}
		d, err := render.Diff(stage, home, "")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	want := []render.Difference{
		{Path: "codex/skills/review/SKILL.md", What: "changed"},
		{Path: "codex/skills/review/notes.md", What: "extra"},
	}
	if got := diff(); !reflect.DeepEqual(got, want) {
		t.Errorf("Diff after the change: %v, want %v", got, want)
	}
	if err := render.Build(res, home, codexRT); err != nil {
		t.Fatal(err)
	}
	if got := diff(); len(got) != 0 {
		t.Errorf("Diff after a new build: %v, want none", got)
	}
}

// TestCodexRefusesALinkOutOfTheModules composes a harness and then makes a file of its
// skill review a link to /etc/hosts: the build for Codex fails with the line that
// contains the link and its target, and writes no home. A real compose refuses such a
// link when it reads the module, see [TestComposeRefusesALinkIntoAnotherModule]; this
// test covers a link that changed after that read.
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
	want := "Remove the link `skills/review/hosts` from the harness.\n  It leads to `" + target + "`, outside the harness's modules, and a Codex run copies only files inside them."
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
