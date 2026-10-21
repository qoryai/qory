package compose_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// TestAModuleNameWithASecretIsLeftOutOfAVariantRefusal is a module whose name contains
// an access key secret and that has no variant for the runtime: the refusal prints the
// phrase in place of the module's name.
func TestAModuleNameWithASecretIsLeftOutOfAVariantRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	_, err := composeTree(t, map[string]string{
		stack.FileName:                   "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n    source:\n      path: modules/qak_m\n",
		"modules/qak_m/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: qak_m\nvariants:\n  codex:\n    agents: agents/codex\n  default: fail\n",
	})
	want := "module (a name that contains an access key secret): the module has no variant for runtime claude and its default is fail; variants: codex"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// isolateAll sets HOME, the XDG directories and git's search to directories of the test,
// so a test reads no file of the machine it runs on.
func isolateAll(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
}

// TestAModuleNameWithASecretIsLeftOutOfAManifestRefusal is a module listed by a name
// that contains an access key secret, whose qory-module.yaml qory refuses, and one
// whose directory is not there: the refusal prints the phrase in place of the module's
// name, and in place of the directory's name in the manifest's path.
func TestAModuleNameWithASecretIsLeftOutOfAManifestRefusal(t *testing.T) {
	isolateAll(t)
	const access = "(a name that contains an access key secret)"
	for _, c := range []struct{ manifest, want string }{
		{"apiVersion: qory.dev/v1alpha1\nname: qak_m\nenv:\n  QAK_X: ../bin\n", ": env." + access + ": ../bin is not a path inside the module"},
		{"apiVersion: qory.dev/v1alpha1\nname: qak_m\nvariants:\n  codex:\n    agents: agents/codex\n  default: qrk_v\n", ": variants.default selects (a name that contains a run key), which is not a variant"},
	} {
		dir := writeTree(t, map[string]string{
			stack.FileName:                   "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n",
			"modules/qak_m/qory-module.yaml": c.manifest,
		})
		p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
		if err != nil {
			t.Fatal(err)
		}
		_, err = compose.Compose(p)
		want := "module " + access + ": " + filepath.Join(p.Root, "modules", access, "qory-module.yaml") + c.want
		if err == nil || err.Error() != want {
			t.Errorf("%v, want %q", err, want)
		}
		if err != nil && strings.Contains(err.Error(), "qak_m") {
			t.Errorf("the refusal contains the module's name: %v", err)
		}
	}
	dir := writeTree(t, map[string]string{stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n"})
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.Compose(p)
	if err == nil || !strings.HasPrefix(err.Error(), "module "+access+": ") || strings.Contains(err.Error(), "qak_m") {
		t.Errorf("a module directory that is not there: %v", err)
	}
}

// composeWritten writes files, makes the directories dirs, and composes the stack for
// claude.
func composeWritten(t *testing.T, files map[string]string, dirs ...string) error {
	t.Helper()
	dir := writeTree(t, files)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.Compose(p)
	return err
}

// TestASecretIsLeftOutOfAModuleSourceRefusal is a module listed by a name with a space
// that contains an access key secret, with a directory that has no qory-module.yaml and
// with no directory, and a path source asked for an export whose name contains one: each
// refusal prints the phrase in place of the word with the secret, and keeps the error
// it wraps.
func TestASecretIsLeftOutOfAModuleSourceRefusal(t *testing.T) {
	isolateAll(t)
	const access = "(a name that contains an access key secret)"
	listed := map[string]string{stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: a qak_m\n"}
	err := composeWritten(t, listed, "modules/a qak_m")
	if want := "/modules/a " + access + " has no qory-module.yaml; a module has one that defines its name"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("no manifest: %v, want the suffix %q", err, want)
	}
	noSecretIn(t, err)
	err = composeWritten(t, map[string]string{stack.FileName: listed[stack.FileName]})
	if want := "/modules/a " + access + ": no such file or directory"; err == nil || !strings.HasSuffix(err.Error(), want) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no directory: %v, want the suffix %q and fs.ErrNotExist", err, want)
	}
	noSecretIn(t, err)
	err = composeWritten(t, map[string]string{
		stack.FileName:   "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: repo, module: qak_x}\n",
		"repo/qory.yaml": "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: harness\n  modules: [core]\n",
		"repo/harness/modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\n",
	})
	if want := " exports no module named " + access + "; modules: core"; err == nil || !strings.HasPrefix(err.Error(), "module core: ") || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("export: %v, want the suffix %q", err, want)
	}
	noSecretIn(t, err)
}

// TestASourceFailureWithASecretKeepsItsError is a module directory qory cannot read and
// a git source that cannot be fetched, each with an access key secret: the refusal
// prints no secret and still matches the error under it.
func TestASourceFailureWithASecretKeepsItsError(t *testing.T) {
	isolateAll(t)
	err := composeWritten(t, map[string]string{
		stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {git: file:///nonexistent/qak_repo.git, ref: main}\n",
	})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) {
		t.Errorf("fetch: %v, want a *source.FetchError", err)
	}
	noSecretIn(t, err)
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory of mode 000")
	}
	dir := writeTree(t, map[string]string{stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n", "modules/qak_m/AGENTS.md": "# M\n"})
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(dir, "modules")
	if err := os.Chmod(modules, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(modules, 0o755) })
	_, err = compose.Compose(p)
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("unreadable: %v, want a *fs.PathError", err)
	}
	noSecretIn(t, err)
}

// noSecretIn fails the test when err contains an access key secret.
func noSecretIn(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "qak_") {
		t.Errorf("the refusal contains the secret: %v", err)
	}
}
