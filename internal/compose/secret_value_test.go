package compose_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// TestAGitSourceWithAPasswordIsPrintedWithoutIt is a module and a base stack from a git
// URL with a password in its user part, which cannot be fetched: each refusal prints
// the password as (hidden) and still matches the fetch error.
func TestAGitSourceWithAPasswordIsPrintedWithoutIt(t *testing.T) {
	isolateAll(t)
	const url = "https://u:token-123@127.0.0.1:1/h.git"
	const shown = "https://u:(hidden)@127.0.0.1:1/h.git#main"
	err := composeWritten(t, map[string]string{
		stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - source: {git: \"" + url + "\", ref: main}\n",
	})
	var fetch *source.FetchError
	if want := "Check the source of a module in the stack.\n  The module is at " + shown + ".\n  fetching " + shown + ": "; err == nil || !strings.HasPrefix(err.Error(), want) || !errors.As(err, &fetch) {
		t.Errorf("module: %v, want the prefix %q and a *source.FetchError", err, want)
	}
	noPasswordIn(t, err)
	p := &stack.Stack{File: "/nowhere/" + stack.FileName, Extends: stack.Source{Git: url, Ref: "main"}}
	_, _, err = compose.LoadBase(p, "", compose.Options{Cache: t.TempDir()})
	if want := "Run qory on a machine that can reach the base stack.\n  The stack " + shown + " is not reachable from here: fetching " + shown + ": "; err == nil || !strings.HasPrefix(err.Error(), want) || !errors.As(err, &fetch) {
		t.Errorf("base: %v, want the prefix %q and a *source.FetchError", err, want)
	}
	noPasswordIn(t, err)
}

// TestAModuleWithAnotherNameHidesASecretNameAndPath is a module listed by a name with a
// secret whose manifest has another name, and one read from a path with a secret whose
// manifest has a name with a run key: the refusal prints Forager's phrases in their
// place.
func TestAModuleWithAnotherNameHidesASecretNameAndPath(t *testing.T) {
	isolateAll(t)
	err := composeWritten(t, map[string]string{
		stack.FileName:                 "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n    source: {path: lib/qak_dir}\n",
		"lib/qak_dir/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: qrk_other\n",
	})
	want := "Use one name for the module in qory-stack.yaml and in its qory-module.yaml.\n  The module (a name that contains an access key secret) at (a value that contains an access key secret) has the name (a name that contains a run key) in its qory-module.yaml."
	if err == nil || err.Error() != want {
		t.Errorf("%v\nwant %q", err, want)
	}
}

// noPasswordIn fails the test when err contains the password of the test's URL.
func noPasswordIn(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "token-123") {
		t.Errorf("the refusal contains the password: %v", err)
	}
}

// TestAComposeRefusalPrintsNoSecretName is each compose refusal that prints a module,
// entry, variable or settings key name with qak_ in it: the refusal prints Forager's
// phrase in its place.
func TestAComposeRefusalPrintsNoSecretName(t *testing.T) {
	isolateAll(t)
	const name = "(a name that contains an access key secret)"
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n"
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{
			stack.FileName:               "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: a\n  - name: b\n",
			"modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\nenv:\n  QAK_X: bin\n",
			"modules/a/bin/x":            "x",
			"modules/b/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: b\nenv:\n  QAK_X: bin\n",
			"modules/b/bin/x":            "x",
		}, "Set env " + name + " in qory.yaml to decide its value.\n  Modules a and b export " + name + " with different values."},
		{map[string]string{
			stack.FileName:                   head + "  - name: core\n    only:\n      skills: [qak_s]\n",
			"modules/core/skills/a/SKILL.md": "# a\n",
		}, "Remove skills/" + name + " from the only of module core.\n  Module core does not ship skills/" + name + "."},
		{map[string]string{
			stack.FileName:                  head + "  - name: core\n    exclude:\n      env: [QAK_X]\n",
			"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  Y: .\n",
		}, "Remove env " + name + " from the exclude of module core.\n  Module core exports no variable " + name + "."},
		{map[string]string{
			stack.FileName:                    head + "  - name: a\n  - name: b\n",
			"modules/a/skills/qak_s/SKILL.md": "# a\n",
			"modules/b/skills/qak_s/SKILL.md": "# b\n",
		}, "2 modules provide skills/" + name + ": a@"},
		{map[string]string{
			stack.FileName: head + "  - name: a\n  - name: b\n",
			"modules/a/settings/claude/settings.json": "{\"qak_k\": 1}\n",
			"modules/b/settings/claude/settings.json": "{\"qak_k\": 2}\n",
		}, "Set " + name + " to one value in both modules."},
		{map[string]string{
			stack.FileName:                   "apiVersion: qory.dev/v1alpha1\nbind:\n  skills:\n    r: qak_x\nmodules:\n  - name: core\n",
			"modules/core/skills/a/SKILL.md": "# a\n",
		}, "The bind skills.r selects skill " + name + ", and no module ships it."},
		{map[string]string{
			stack.FileName:                   head + "  - name: core\n",
			"modules/core/qory-module.yaml":  "apiVersion: qory.dev/v1alpha1\nname: core\nprovides:\n  skills:\n    r: qak_x\n",
			"modules/core/skills/a/SKILL.md": "# a\n",
		}, "Ship skill " + name + " in a module.\n  You can also remove provides.skills.r from module core.\n  No module ships skill " + name + "."},
	} {
		err := composeWritten(t, c.files)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v\nwant one that contains %q", err, c.want)
		}
		noSecretIn(t, err)
	}
}

// TestTheModuleNameRefusalContainsTheFileThatDeclaresTheModule is a module whose manifest has
// another name than the one listed, listed in the harness section of qory.yaml: line 1
// contains qory.yaml, not qory-stack.yaml.
func TestTheModuleNameRefusalContainsTheFileThatDeclaresTheModule(t *testing.T) {
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:          "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [commands]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"app/" + config.FileName:          "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: m\n      source: {path: modules/m}\n",
		"app/modules/m/qory-module.yaml":  "apiVersion: qory.dev/v1alpha1\nname: acme-core\n",
		"app/modules/m/commands/ship.md":  "ship\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(merged, compose.Options{Base: base})
	const want = "Use one name for the module in qory.yaml and in its qory-module.yaml.\n  The module m at modules/m has the name acme-core in its qory-module.yaml."
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%v\nwant one that starts with %q", err, want)
	}
}

// TestAnUnreadableManifestStartsWithTheFixAndNamesTheModule is a module whose
// qory-module.yaml has mode 000: line 1 of the refusal is the fix with the module's name,
// and the error still matches fs.ErrPermission.
func TestAnUnreadableManifestStartsWithTheFixAndNamesTheModule(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		stack.FileName:               "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\n",
	})
	manifest := filepath.Join(dir, "modules", "a", "qory-module.yaml")
	if err := os.Chmod(manifest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o644) })
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.Compose(p)
	if err == nil {
		t.Fatal("no refusal")
	}
	if first := strings.SplitN(err.Error(), "\n", 2)[0]; first != "Check that you can read qory-module.yaml of module a." {
		t.Errorf("line 1 is %q", first)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%v does not match fs.ErrPermission", err)
	}
}

// TestTheModuleNameRefusalOfABaseModuleContainsTheBaseStackFile is a module listed by the
// base stack, with a manifest that has another name, under a checkout that declares its
// stack in qory.yaml: line 1 contains qory-stack.yaml, the file that lists the module.
func TestTheModuleNameRefusalOfABaseModuleContainsTheBaseStackFile(t *testing.T) {
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:          "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [commands]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: z\n",
		"app/" + config.FileName:          "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(merged, compose.Options{Base: base})
	const want = "Use one name for the module in qory-stack.yaml and in its qory-module.yaml.\n  The module a at "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%v\nwant one that starts with %q", err, want)
	}
}

// TestTheVariantRefusalContainsTheFileThatListsTheModule is a module with variants and no
// variant for the runtime, listed in the harness section of qory.yaml and in
// qory-stack.yaml: line 1 contains the file that lists the module.
func TestTheVariantRefusalContainsTheFileThatListsTheModule(t *testing.T) {
	isolateAll(t)
	const manifest = "apiVersion: qory.dev/v1alpha1\nname: m\nvariants:\n  codex:\n    agents: agents/codex\n  default: fail\n"
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:          "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [commands]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"app/" + config.FileName:          "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: m\n      source: {path: modules/m}\n",
		"app/modules/m/qory-module.yaml":  manifest,
		"app/modules/m/commands/ship.md":  "ship\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(merged, compose.Options{Base: base})
	const want = "Select a variant of module m in qory.yaml.\n  The module has no variant for runtime claude, and its default is `fail`."
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("harness section: %v\nwant one that starts with %q", err, want)
	}
	_, err = composeTree(t, map[string]string{
		stack.FileName:               "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: m\n    source:\n      path: modules/m\n",
		"modules/m/qory-module.yaml": manifest,
	})
	const wantStack = "Select a variant of module m in qory-stack.yaml.\n  The module has no variant for runtime claude, and its default is `fail`."
	if err == nil || !strings.HasPrefix(err.Error(), wantStack) {
		t.Errorf("stack file: %v\nwant one that starts with %q", err, wantStack)
	}
}

// TestAnExtendingModuleRefusalPrintsNoSecretName is an extending module whose name and
// whose skill's name contain a secret, of a kind the base does not let it ship: the
// refusal prints Forager's phrase in place of both.
func TestAnExtendingModuleRefusalPrintsNoSecretName(t *testing.T) {
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:                  "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [commands]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml":         "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"app/" + config.FileName:                  "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: qak_b\n",
		"app/modules/qak_b/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: qak_b\n",
		"app/modules/qak_b/skills/qak_s/SKILL.md": "# s\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.ComposeWith(merged, compose.Options{Base: base})
	const name = "(a name that contains an access key secret)"
	if want := exports.Fit("Remove skills/" + name + " from module " + name + ".\n  Module " + name + " ships skills/" + name + ".\n  The base stack core@"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v\nwant one that contains %q", err, want)
	}
	noSecretIn(t, err)
}

// TestABaseUnderASecretPathPrintsNoSecret is a checkout that extends a base stack from a
// path whose directory name contains an access key secret, and whose file has an unknown
// key: the refusal prints Forager's phrase in place of the directory.
func TestABaseUnderASecretPathPrintsNoSecret(t *testing.T) {
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		"qak_base/" + stack.FileName:     "apiVersion: qory.dev/v1alpha1\nbogus: 1\nmodules:\n  - name: a\n",
		"app/" + config.FileName:         "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../qak_base}\n  target:\n    runtime: claude\n  modules:\n    - name: b\n",
		"app/modules/b/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: b\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = compose.LoadBase(p, "", compose.Options{})
	const name = "(a name that contains an access key secret)"
	if want := "It is at line 2 of "; err == nil || !strings.Contains(err.Error(), "/"+name+"/"+stack.FileName+".") || !strings.Contains(err.Error(), want) {
		t.Errorf("%v\nwant one that names the file and line 2", err)
	}
	noSecretIn(t, err)
}
