package compose_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
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
	if want := "module at " + shown + ": fetching " + shown + ": "; err == nil || !strings.HasPrefix(err.Error(), want) || !errors.As(err, &fetch) {
		t.Errorf("module: %v, want the prefix %q and a *source.FetchError", err, want)
	}
	noPasswordIn(t, err)
	p := &stack.Stack{File: "/nowhere/" + stack.FileName, Extends: stack.Source{Git: url, Ref: "main"}}
	_, _, err = compose.LoadBase(p, "", compose.Options{Cache: t.TempDir()})
	if want := "the stack " + shown + " is not reachable from here: fetching " + shown + ": "; err == nil || !strings.HasPrefix(err.Error(), want) || !errors.As(err, &fetch) {
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
	want := "module (a name that contains an access key secret): the module at (a value that contains an access key secret) has the name (a name that contains a run key) in its qory-module.yaml"
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
		}, "env " + name + " is exported by modules a and b; set it in qory.yaml to decide"},
		{map[string]string{
			stack.FileName:                   head + "  - name: core\n    only:\n      skills: [qak_s]\n",
			"modules/core/skills/a/SKILL.md": "# a\n",
		}, "module core: only skills/" + name + " selects nothing the module ships"},
		{map[string]string{
			stack.FileName:                  head + "  - name: core\n    exclude:\n      env: [QAK_X]\n",
			"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  Y: .\n",
		}, "module core: exclude env " + name + " selects nothing the module ships"},
		{map[string]string{
			stack.FileName:                    head + "  - name: a\n  - name: b\n",
			"modules/a/skills/qak_s/SKILL.md": "# a\n",
			"modules/b/skills/qak_s/SKILL.md": "# b\n",
		}, "skills/" + name + " is provided by 2 modules: a@"},
		{map[string]string{
			stack.FileName: head + "  - name: a\n  - name: b\n",
			"modules/a/settings/claude/settings.json": "{\"qak_k\": 1}\n",
			"modules/b/settings/claude/settings.json": "{\"qak_k\": 2}\n",
		}, "settings/claude/settings.json: " + name + " is set by modules a and b with different values"},
		{map[string]string{
			stack.FileName:                   "apiVersion: qory.dev/v1alpha1\nbind:\n  skills/r: qak_x\nmodules:\n  - name: core\n",
			"modules/core/skills/a/SKILL.md": "# a\n",
		}, "bind skills/r selects skill " + name + ", which no module ships"},
	} {
		err := composeWritten(t, c.files)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v\nwant one that contains %q", err, c.want)
		}
		noSecretIn(t, err)
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
	if want := "module " + name + " ships skills/" + name + ", and the base stack core@"; err == nil || !strings.Contains(err.Error(), want) {
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
	if want := "/" + name + "/" + stack.FileName + ": line 2: "; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v\nwant one that contains %q", err, want)
	}
	noSecretIn(t, err)
}
