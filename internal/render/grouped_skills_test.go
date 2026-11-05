package render_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// groupedSkillPaths are the folders of each runtime's home, besides the shared skills
// folder, where a skill is written.
var groupedSkillPaths = map[string][]string{
	"claude":   {"claude/skills", "claude/plugin/skills"},
	"codex":    {"codex/skills"},
	"gemini":   {"gemini/skills"},
	"opencode": {"opencode/skills"},
	"cursor":   {"cursor/plugin/skills"},
	"copilot":  {"copilot/workspace/.github/skills"},
	"amp":      nil,
	"goose":    nil,
	"any":      nil,
}

// composeGrouped composes a module with skills in groups of one and two levels for the
// runtime, into a new home.
func composeGrouped(t *testing.T, runtime string, opts compose.Options, block string) (*compose.Result, string) {
	t.Helper()
	hermetic(t)
	dir := t.TempDir()
	for path, text := range map[string]string{
		stack.FileName:                                  "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n" + block,
		"modules/core/qory-module.yaml":                 "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"modules/core/skills/review/SKILL.md":           "---\nname: review\ndescription: d\n---\nPlain.\n",
		"modules/core/skills/one/deploy/SKILL.md":       "---\nname: deploy\ndescription: d\n---\nOne level.\n",
		"modules/core/skills/one/deploy/lib/notes.txt":  "a file of deploy\n",
		"modules/core/skills/one/two/fix/SKILL.md":      "---\nname: fix\ndescription: d\n---\nTwo levels, ${qory:skills/deploy}.\n",
		"modules/core/skills/one/two/fix/more/guide.md": "See ${qory:skills/review}.\n",
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := stack.Load(filepath.Join(real, stack.FileName))
	if err != nil {
		t.Fatal(err)
	}
	p.Target = stack.Target{Runtimes: []string{runtime}}
	res, err := compose.ComposeWith(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(res.Close)
	rt, err := render.Lookup(runtime)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	return res, home
}

// TestAGroupedSkillLandsFlatOnEveryRuntime composes a module with groups of one and two
// levels for each runtime: each skill's SKILL.md and files are at <folder>/<name>, for the
// shared folder and the runtime's, with references resolved in a skill's documents, and no
// group's name is in a skills folder. modules/core keeps the module's tree.
func TestAGroupedSkillLandsFlatOnEveryRuntime(t *testing.T) {
	for _, runtime := range render.Names() {
		folders, ok := groupedSkillPaths[runtime]
		if !ok {
			t.Fatalf("runtime %s has no expectations here", runtime)
		}
		t.Run(runtime, func(t *testing.T) {
			_, home := composeGrouped(t, runtime, compose.Options{}, "")
			for _, folder := range append([]string{"skills"}, folders...) {
				for _, path := range []string{"review/SKILL.md", "deploy/SKILL.md", "deploy/lib/notes.txt", "fix/SKILL.md", "fix/more/guide.md"} {
					if _, err := os.Stat(filepath.Join(home, folder, path)); err != nil {
						t.Errorf("%s/%s: %v", folder, path, err)
					}
				}
				for _, group := range []string{"one", "two"} {
					if _, err := os.Lstat(filepath.Join(home, folder, group)); !os.IsNotExist(err) {
						t.Errorf("%s/%s: %v, want no such folder", folder, group, err)
					}
				}
				data, err := os.ReadFile(filepath.Join(home, folder, "fix", "SKILL.md"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "${qory:") || !strings.Contains(string(data), "deploy") {
					t.Errorf("%s/fix/SKILL.md:\n%s", folder, data)
				}
			}
			if _, err := os.Stat(filepath.Join(home, "modules", "core", "skills", "one", "deploy", "SKILL.md")); err != nil {
				t.Errorf("modules/core: %v", err)
			}
		})
	}
}

// TestAWalledCopyLeavesOutAGroupedSkillAnExcludeLeftOut is a module the home holds a copy
// of, for a walled run on each runtime, with an exclude of a skill in a group: neither the
// flat folders nor the group in modules/core has the skill, and the other skill of the
// group is a copy in each of them.
func TestAWalledCopyLeavesOutAGroupedSkillAnExcludeLeftOut(t *testing.T) {
	for _, runtime := range render.Names() {
		folders, ok := groupedSkillPaths[runtime]
		if !ok {
			t.Fatalf("runtime %s has no expectations here", runtime)
		}
		t.Run(runtime, func(t *testing.T) {
			opts := compose.Options{Copies: func(string) bool { return true }, Walled: true}
			_, home := composeGrouped(t, runtime, opts, "    exclude: {skills: [fix]}\n")
			gone := []string{"modules/core/skills/one/two/fix"}
			kept := []string{"modules/core/skills/one/deploy/SKILL.md"}
			for _, folder := range append([]string{"skills"}, folders...) {
				gone = append(gone, folder+"/fix")
				kept = append(kept, folder+"/deploy/SKILL.md")
			}
			for _, path := range gone {
				if _, err := os.Lstat(filepath.Join(home, path)); !os.IsNotExist(err) {
					t.Errorf("%s: %v, want it left out", path, err)
				}
			}
			for _, path := range kept {
				info, err := os.Lstat(filepath.Join(home, path))
				if err != nil {
					t.Errorf("%s: %v", path, err)
					continue
				}
				if info.Mode()&fs.ModeSymlink != 0 {
					t.Errorf("%s is a link, want a copy", path)
				}
			}
		})
	}
}
