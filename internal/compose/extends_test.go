package compose_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestExtendKeepsTheDocumentsTarget is a checkout whose qory.yaml extends a stack and
// sets a target beside extends: the stack carries none, so the composed stack is for the
// document's target, model included, and the compose records the base.
func TestExtendKeepsTheDocumentsTarget(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:              "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [skills]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"base/modules/a/commands/ship.md":     "ship\n",
		"app/" + config.FileName:              "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: codex\n    model: gpt-5\n",
		"app/modules/b/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: b\n",
		"app/modules/b/skills/greet/SKILL.md": "# greet\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if base == nil || base.Name != "core" {
		t.Fatalf("base = %+v, want core", base)
	}
	if merged.Target.Runtimes.String() != "codex" || merged.Target.Model != "gpt-5" {
		t.Fatalf("target = %+v, want the document's, codex with gpt-5", merged.Target)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stack.Target.Runtimes.String() != "codex" || res.Stack.Target.Model != "gpt-5" {
		t.Errorf("composed target = %+v, want the document's", res.Stack.Target)
	}
	if len(res.Entries) != 1 || res.Entries[0].Module != "a" {
		t.Errorf("entries = %+v, want the base's command alone", res.Entries)
	}
}

// TestLoadBaseStopsABaseLinkedOutOfItsClone is a checkout's qory.yaml that extends a
// git base whose qory-stack.yaml, or whose repository's qory.yaml, is a link to a file
// outside the repository: the base is not read, and the error shows the base by its
// address, since the harness is shown.
func TestLoadBaseStopsABaseLinkedOutOfItsClone(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"stacks/web/" + stack.FileName, ": qory-stack.yaml links outside the repository"},
		{config.FileName, ": qory.yaml at the root of its repository links outside the repository"},
	} {
		t.Run(c.link, func(t *testing.T) {
			isolateAll(t)
			outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
			if err := os.WriteFile(outside, []byte("apiVersion: qory.dev/v1alpha1\nname: elsewhere\nextending: {}\nmodules:\n  - name: core\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			url := gitRepo(t, "", map[string]string{
				"stacks/web/" + stack.FileName:  "apiVersion: qory.dev/v1alpha1\nname: web\nextending: {}\nmodules:\n  - name: core\n",
				"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\n",
			})
			dir := strings.TrimPrefix(url, "file://")
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(c.link))); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, filepath.FromSlash(c.link))); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "link"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v\n%s", err, out)
				}
			}
			checkout := writeTree(t, map[string]string{
				config.FileName: "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {git: \"" + url + "\", ref: main, path: stacks/web}\n",
			})
			p, err := config.Compose(filepath.Join(checkout, config.FileName))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = compose.LoadBase(p, "", compose.Options{Cache: t.TempDir()})
			if want := "extends " + url + "#main:stacks/web" + c.want; err == nil || err.Error() != want {
				t.Fatalf("got %v\nwant %s", err, want)
			}
		})
	}
}

// excludeBaseTree is a base whose module core ships the skill explore-a-ticket and a
// command that references it, and a checkout whose module app ships its version of the
// skill, with lines of the checkout's modules list after app.
func excludeBaseTree(t *testing.T, modules string) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"base/" + stack.FileName:                             "apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
		"base/modules/core/qory-module.yaml":                 "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"base/modules/core/skills/explore-a-ticket/SKILL.md": "# the base's\n",
		"base/modules/core/commands/ship.md":                 "Run ${qory:skills/explore-a-ticket}.\n",
		"app/" + config.FileName:                             "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n  modules:\n    - name: app\n" + modules,
		"app/modules/app/qory-module.yaml":                   "apiVersion: qory.dev/v1alpha1\nname: app\n",
		"app/modules/app/skills/explore-a-ticket/SKILL.md":   "# the repository's\n",
	})
}

// composeExtending composes the checkout's qory.yaml in dir on its base.
func composeExtending(t *testing.T, dir string) (*compose.Result, error) {
	t.Helper()
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		return nil, err
	}
	return compose.ComposeWith(merged, compose.Options{Base: base})
}

// TestExtendExcludesABaseEntry is the checkout's qory.yaml listing the base's module
// core by its name with an exclude of explore-a-ticket: the repository's skill is
// composed, the base's command references it, and the report lists the base's skill as
// left out of core.
func TestExtendExcludesABaseEntry(t *testing.T) {
	dir := excludeBaseTree(t, "    - name: core\n      exclude: {skills: [explore-a-ticket]}\n")
	res, err := composeExtending(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	from := map[string]string{}
	for _, e := range res.Entries {
		from[e.Kind+"/"+e.Name] = e.Module
	}
	if from["skills/explore-a-ticket"] != "app" || from["commands/ship"] != "core" || len(from) != 2 {
		t.Fatalf("entries by module = %v, want the skill from app and the command from core", from)
	}
	if len(res.Excludes) != 1 || res.Excludes[0].Module != "core" || res.Excludes[0].Kind != "skills" || res.Excludes[0].Name != "explore-a-ticket" || !strings.HasSuffix(res.Excludes[0].Path, filepath.Join("modules", "core", "skills", "explore-a-ticket")) {
		t.Fatalf("excludes = %+v, want core's skills/explore-a-ticket", res.Excludes)
	}
	if len(res.Modules) != 2 || res.Modules[0].Name != "core" || !res.Modules[0].Base || res.Modules[1].Name != "app" {
		t.Fatalf("modules = %+v, want the base's core, then app", res.Modules)
	}
}

// TestExtendSuggestsTheExcludeOfABaseEntry is the checkout's skill colliding with the
// base's: the message and Suggest give the exclude for core, the base's module, and say
// it goes in qory.yaml.
func TestExtendSuggestsTheExcludeOfABaseEntry(t *testing.T) {
	_, err := composeExtending(t, excludeBaseTree(t, ""))
	var collision *compose.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want a collision", err)
	}
	want := "skills/explore-a-ticket is provided by 2 modules: core@working-tree, app@working-tree\n" +
		"  keep one and exclude the others, for example\n" +
		"    core: exclude: {skills: [explore-a-ticket]}\n" +
		"  write the exclude for a module of the base stack web@working-tree in qory.yaml, under harness.modules"
	if err.Error() != want {
		t.Fatalf("got\n%s\nwant\n%s", err, want)
	}
	modules, excludes := collision.Suggest(collision.Order)
	if len(modules) != 1 || modules[0] != "core" || len(excludes["core"]["skills"]) != 1 || excludes["core"]["skills"][0] != "explore-a-ticket" {
		t.Fatalf("suggest = %v, %v; want core excluding skills/explore-a-ticket", modules, excludes)
	}
	if !collision.InBase["core"] || collision.InBase["app"] {
		t.Fatalf("in base = %v, want core alone", collision.InBase)
	}
}

// TestExtendRefusesAnExcludeOfNoBaseEntry is the checkout's qory.yaml excluding from core
// a skill core does not compose: one it does not ship, and one the base's exclude
// already leaves out. Each message starts with the fix.
func TestExtendRefusesAnExcludeOfNoBaseEntry(t *testing.T) {
	dir := excludeBaseTree(t, "    - name: core\n      exclude: {skills: [explore-a-ticket, triage]}\n")
	_, err := composeExtending(t, dir)
	want := "remove skills/triage from the exclude of module core in qory.yaml: the base stack web@working-tree composes no skills/triage from module core"
	if err == nil || err.Error() != want {
		t.Fatalf("a skill core does not ship: got %v\nwant %s", err, want)
	}
	stackFile := filepath.Join(dir, "base", stack.FileName)
	if err := os.WriteFile(stackFile, []byte("apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n  - name: core\n    source:\n      path: modules/core\n    exclude: {skills: [explore-a-ticket]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", config.FileName), []byte("apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n  modules:\n    - name: core\n      exclude: {skills: [explore-a-ticket]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = composeExtending(t, dir)
	want = "remove skills/explore-a-ticket from the exclude of module core in qory.yaml: the base stack web@working-tree composes no skills/explore-a-ticket from module core"
	if err == nil || err.Error() != want {
		t.Fatalf("a skill the base leaves out: got %v\nwant %s", err, want)
	}
}

// baseStackWith is the base stack of [excludeBaseTree] with core listed by the given
// lines in place of its name and path.
func baseStackWith(core string) string {
	return "apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n" + core
}

// TestExtendKeepsRenameForABaseModuleByPath is a base that lists core by a path alone:
// the checkout's qory.yaml cannot list core, so the collision message is to rename the
// entry, with no exclude suggested.
func TestExtendKeepsRenameForABaseModuleByPath(t *testing.T) {
	dir := excludeBaseTree(t, "")
	if err := os.WriteFile(filepath.Join(dir, "base", stack.FileName), []byte(baseStackWith("  - source: {path: modules/core}\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := composeExtending(t, dir)
	var collision *compose.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want a collision", err)
	}
	want := "skills/explore-a-ticket is provided by 2 modules: core@working-tree, app@working-tree\n" +
		"  it belongs to the base stack web@working-tree; rename yours"
	if err.Error() != want {
		t.Fatalf("got\n%s\nwant\n%s", err, want)
	}
	if modules, _ := collision.Suggest(collision.Order); len(modules) != 0 || collision.InBase["core"] || !collision.Collisions[0].Rename {
		t.Fatalf("suggest = %v, in base = %v, rename = %v; want no exclude for core", modules, collision.InBase, collision.Collisions[0].Rename)
	}
}

// TestExtendSuggestsTheExportNameOfABaseModule is a base that lists its module by the
// export core of a git source whose manifest declares core-x: the suggested exclude and
// the error of an exclude that composes nothing use core, the name qory.yaml lists it by.
func TestExtendSuggestsTheExportNameOfABaseModule(t *testing.T) {
	isolateAll(t)
	url := gitRepo(t, "", map[string]string{
		"qory.yaml":                     "apiVersion: qory.dev/v1alpha1\nexports:\n  modules: [core]\n",
		"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core-x\n",
		"modules/core/skills/explore-a-ticket/SKILL.md": "# the base's\n",
	})
	dir := excludeBaseTree(t, "")
	core := "  - source: {git: \"" + url + "\", ref: main, module: core}\n"
	if err := os.WriteFile(filepath.Join(dir, "base", stack.FileName), []byte(baseStackWith(core)), 0o644); err != nil {
		t.Fatal(err)
	}
	composeIt := func() error {
		t.Helper()
		p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
		if err != nil {
			t.Fatal(err)
		}
		opts := compose.Options{Cache: t.TempDir()}
		merged, base, err := compose.LoadBase(p, "", opts)
		if err != nil {
			t.Fatal(err)
		}
		opts.Base = base
		_, err = compose.ComposeWith(merged, opts)
		return err
	}
	err := composeIt()
	var collision *compose.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want a collision", err)
	}
	if !strings.Contains(err.Error(), "\n    core: exclude: {skills: [explore-a-ticket]}\n") || collision.ExcludeName("core-x") != "core" {
		t.Fatalf("the exclude for core-x is not written under core:\n%s", err)
	}
	app := "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n  modules:\n    - name: app\n    - name: core\n      exclude: {skills: [explore-a-ticket, triage]}\n"
	if err := os.WriteFile(filepath.Join(dir, "app", config.FileName), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "remove skills/triage from the exclude of module core in qory.yaml: the base stack web@working-tree composes no skills/triage from module core-x"
	if err := composeIt(); err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", config.FileName), []byte(strings.Replace(app, ", triage", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := composeIt(); err != nil {
		t.Fatalf("the exclude under the export's name: %v", err)
	}
}

// TestExtendSaysWhichExcludeLeftARequirementOut is the checkout's qory.yaml excluding
// explore-a-ticket from core with no module of the repository shipping it, while core's
// command ship references it: the message starts with the fix and states that the
// exclude in qory.yaml left it out.
func TestExtendSaysWhichExcludeLeftARequirementOut(t *testing.T) {
	dir := excludeBaseTree(t, "    - name: core\n      exclude: {skills: [explore-a-ticket]}\n")
	if err := os.RemoveAll(filepath.Join(dir, "app", "modules", "app", "skills")); err != nil {
		t.Fatal(err)
	}
	_, err := composeExtending(t, dir)
	want := "remove skills/explore-a-ticket from the exclude of module core in qory.yaml, or ship it in a module of the repository: module core: command ship references skill explore-a-ticket, which the exclude of module core in qory.yaml leaves out"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v\nwant %s", err, want)
	}
}

// TestExtendSuggestsTheWholeExcludeOfAListedBaseModule is the checkout's qory.yaml
// listing core with an exclude already when its skill collides with core's: the
// suggestion is the whole exclude, the listed entry and the colliding one, to write in
// place of the one qory.yaml lists.
func TestExtendSuggestsTheWholeExcludeOfAListedBaseModule(t *testing.T) {
	dir := excludeBaseTree(t, "    - name: core\n      exclude: {skills: [triage]}\n")
	triage := filepath.Join(dir, "base", "modules", "core", "skills", "triage")
	if err := os.MkdirAll(triage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(triage, "SKILL.md"), []byte("# triage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := composeExtending(t, dir)
	var collision *compose.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want a collision", err)
	}
	want := "skills/explore-a-ticket is provided by 2 modules: core@working-tree, app@working-tree\n" +
		"  keep one and exclude the others, for example\n" +
		"    core: exclude: {skills: [triage, explore-a-ticket]}\n" +
		"  write the exclude for a module of the base stack web@working-tree in qory.yaml, under harness.modules, in place of the exclude qory.yaml lists for core"
	if err.Error() != want {
		t.Fatalf("got\n%s\nwant\n%s", err, want)
	}
}
