package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestSetupRepoSetsTheRepositoryUp writes a qory.yaml holding the repository's own stack and
// that module into a directory, composes them, reads the configuration back, validates
// the file against the schema, and keeps every file on a second run.
func TestSetupRepoSetsTheRepositoryUp(t *testing.T) {
	root := filepath.Join(emptyDir(t), "My App")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	out, err := run(t, "setup", "repo")
	if err != nil {
		t.Fatal(err, out)
	}
	wants(t, out, "🐝 my-app")
	if got := fieldRows(out)["wrote"]; len(got) != 3 || got[0] != "qory.yaml" || got[1] != "harness/qory-module.yaml" || got[2] != "harness/AGENTS.md" {
		t.Errorf("wrote = %q", got)
	}
	gone(t, root, "qory-stack.yaml")
	validConfig(t, filepath.Join(root, config.FileName))
	wantsRow(t, out, "git", "initialised a repository here; qory composes into a checkout")
	wantsRow(t, out, "next", "qory harness compose")

	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err, out)
	}
	wants(t, out, "🐝 my-app · claude", "composed 0 entries from 1 module")
	data, err := os.ReadFile(filepath.Join(root, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "# my-app")

	conf, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if path := filepath.Join(root, config.FileName); len(conf.Files) != 1 || conf.Files[0] != path {
		t.Errorf("files read = %v, want [%s]", conf.Files, path)
	}
	if conf.Worktree.Dir != config.DefaultWorktreeDir || conf.Worktree.Base != "" || len(conf.Worktree.Link) != 0 {
		t.Errorf("the written file changed a value: %+v", conf.Worktree)
	}

	before := snapshot(t, root)
	out, err = run(t, "setup", "repo")
	if err != nil {
		t.Fatal(err, out)
	}
	wantsRow(t, out, "kept", "qory.yaml  (its harness section names the stack)")
	lacks(t, out, "wrote", "initialised")
	if after := snapshot(t, root); len(after) != len(before) {
		t.Errorf("a second run changed the tree: %d paths, then %d", len(before), len(after))
	}
}

// TestSetupRepoKeepsAStackThatIsThere writes no stack and no module when the qory.yaml that is
// there names one under harness, and beside a qory-stack.yaml writes a qory.yaml with the
// worktree section alone.
func TestSetupRepoKeepsAStackThatIsThere(t *testing.T) {
	root := newCheckout(t)
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(customerCompose("https://git.example.com/acme/harness.git")), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "setup", "repo")
	if err != nil {
		t.Fatal(err, out)
	}
	wantsRow(t, out, "kept", "qory.yaml  (its harness section names the stack)")
	lacks(t, out, "wrote")
	gone(t, root, "qory-stack.yaml", "harness")

	root = newCheckout(t)
	copyFixture(t, "two-modules", root)
	out, err = run(t, "setup", "repo")
	if err != nil {
		t.Fatal(err, out)
	}
	wantsRow(t, out, "kept", "qory-stack.yaml  (the stack is here)")
	wantsRow(t, out, "wrote", "qory.yaml")
	gone(t, root, "harness")
	validConfig(t, filepath.Join(root, config.FileName))
	if _, err := run(t, "harness", "compose", "--dry-run"); err != nil {
		t.Errorf("the stack file and the written qory.yaml do not compose together: %v", err)
	}
}

// validConfig fails the test when the qory.yaml at path does not pass config.schema.json.
func validConfig(t *testing.T, path string) {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join(fixtures, "..", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(doc); err != nil {
		t.Errorf("%s fails the schema: %v", path, err)
	}
}

// TestSetupMachineWritesTheMachinesFile writes the user's qory.yaml, whose keys are the
// machine's, and reads it back: the defaults it spells out are the defaults.
func TestSetupMachineWritesTheMachinesFile(t *testing.T) {
	root := newCheckout(t)
	out, err := run(t, "setup", "machine")
	if err != nil {
		t.Fatal(err, out)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", config.FileName)
	wantsRow(t, out, "wrote", config.FileName)
	wantsRow(t, out, "next", "qory config")
	conf, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(conf.Files) != 1 || conf.Files[0] != path {
		t.Errorf("files read = %v, want [%s]", conf.Files, path)
	}
	want := config.Defaults()
	if conf.Worktree.Dir != want.Worktree.Dir || conf.Worktree.Name != want.Worktree.Name || conf.Git.Timeout != want.Git.Timeout || conf.Runtime != nil || conf.Model != "" {
		t.Errorf("the written file changed a value:\n%+v", conf)
	}
	out, err = run(t, "setup", "machine")
	if err != nil {
		t.Fatal(err, out)
	}
	wantsRow(t, out, "kept", "qory.yaml  (already here)")
}
