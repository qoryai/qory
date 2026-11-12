package acting_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/acting"
)

const document = `apiVersion: qory.dev/v1alpha1
worktree:
  dir: ../trees
  base: main
  link:
    - .env
  copy:
    - from: config/local.yaml
  run:
    add: [make setup]
    remove: [make clean]
harness:
  home: ~/homes
  model: a-model
  launch:
    claude:
      command: claude
      args:
        - [--verbose]
      env:
        LOG: debug
  extends:
    git: https://git.example.com/acme/stack.git
    ref: main
  target:
    runtime: claude
  modules:
    - name: inside
      source:
        path: modules/inside
    - name: outside
      source:
        path: ../shared/outside
    - name: fetched
      source:
        git: https://git.example.com/acme/modules.git
env:
  TOKEN_DIR: /opt/tokens
git:
  cache: /var/cache/qory
  timeout: 5m
`

// place makes a place with a qory.yaml holding content, and returns the place and the
// file.
func place(t *testing.T, content string) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "shop", "qory.yaml")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(file), file
}

// TestEntriesAreTheActingKeys lists the keys qory acts on, each once, and none of the
// rest: a module's path inside the place is no acting key, one outside it is.
func TestEntriesAreTheActingKeys(t *testing.T) {
	dir, file := place(t, document)
	entries, err := acting.Entries(file, []byte(document), dir)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	want := []string{
		"worktree.dir", "worktree.link", "worktree.copy", "worktree.run.add", "worktree.run.remove",
		"harness.home", "harness.launch.claude.command", "harness.launch.claude.args", "harness.launch.claude.env.LOG",
		"harness.extends.git", "harness.modules[1].source.path", "harness.modules[2].source.git",
		"env.TOKEN_DIR", "git.cache",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys\n%q\nwant\n%q", keys, want)
	}
	if entries[3].Value != `["make setup"]` {
		t.Errorf("worktree.run.add's value %s", entries[3].Value)
	}
}

// TestAPathThatLeadsOutThroughALinkIsAnActingKey is a module's path and an extends path
// inside the place as written, each a link to a folder outside it: qory follows the
// link, so each is an acting key, and a path inside the place both ways is none.
func TestAPathThatLeadsOutThroughALinkIsAnActingKey(t *testing.T) {
	content := "harness:\n  extends:\n    path: ./base\n  modules:\n    - name: linked\n      source:\n        path: ./x\n    - name: inside\n      source:\n        path: modules/inside\n"
	dir, file := place(t, content)
	outside := t.TempDir()
	for _, name := range []string{"x", "base"} {
		if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "modules", "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := acting.Entries(file, []byte(content), dir)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	if want := []string{"harness.extends.path", "harness.modules[0].source.path"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys %q, want %q", keys, want)
	}
}

// TestARelativePathResolvesFromTheFolderAsGiven reads a qory.yaml through a link in the
// place to a folder two levels down in it, with a module's path ../../x: compose takes it
// from the folder as given, the link, which leads out of the place, so it is an acting
// key, though from the folder the link leads to it would lie inside.
func TestARelativePathResolvesFromTheFolderAsGiven(t *testing.T) {
	content := "harness:\n  modules:\n    - name: up\n      source:\n        path: ../../x\n"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "place")
	deep := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "qory.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(deep, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := acting.Entries(filepath.Join(dir, "link", "qory.yaml"), []byte(content), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Key != "harness.modules[0].source.path" {
		t.Errorf("entries %+v, want harness.modules[0].source.path", entries)
	}
}

// TestAStackFilesActingKeysAreItsSources lists extends and the modules' sources of a
// stack file, and nothing of a stack's other keys.
func TestAStackFilesActingKeysAreItsSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "qory-stack.yaml")
	content := "apiVersion: qory.dev/v1alpha1\nname: base\nextends:\n  path: /opt/stacks/base\nmodules:\n  - name: core\n    source:\n      git: https://git.example.com/acme/core.git\n"
	entries, err := acting.Entries(file, []byte(content), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "extends.path" || entries[1].Key != "modules[0].source.git" {
		t.Errorf("entries %+v", entries)
	}
}

// TestAModuleMovedInTheListKeepsItsHash is the same modules in another order: the
// hashes are the same.
func TestAModuleMovedInTheListKeepsItsHash(t *testing.T) {
	a := "harness:\n  modules:\n    - name: a\n      source: {git: https://git.example.com/a.git}\n    - name: b\n      source: {git: https://git.example.com/b.git}\n"
	b := "harness:\n  modules:\n    - name: b\n      source: {git: https://git.example.com/b.git}\n    - name: a\n      source: {git: https://git.example.com/a.git}\n"
	dir, file := place(t, a)
	ea, _ := acting.Entries(file, []byte(a), dir)
	eb, _ := acting.Entries(file, []byte(b), dir)
	if !reflect.DeepEqual(acting.Hashes(ea), acting.Hashes(eb)) {
		t.Error("moving a module changed the hashes")
	}
}

// TestALinkThatLeadsOutIsPointedElsewhere reads a place whose qory.yaml extends a path
// that leads out of the place through a link in it, then points the link at another
// folder, a folder of keys say: the value is the same, but the hash is not, and the
// entry has where the path leads now.
func TestALinkThatLeadsOutIsPointedElsewhere(t *testing.T) {
	content := "harness:\n  extends:\n    path: ./base\n"
	dir, file := place(t, content)
	first, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "base")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	before, err := acting.Entries(file, []byte(content), dir)
	if err != nil || len(before) != 1 {
		t.Fatalf("before: %+v, %v", before, err)
	}
	keys, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(keys, link); err != nil {
		t.Fatal(err)
	}
	after, err := acting.Entries(file, []byte(content), dir)
	if err != nil || len(after) != 1 || after[0].Key != "harness.extends.path" || after[0].Value != `"./base"` || after[0].Leads != keys {
		t.Fatalf("pointed elsewhere: %+v, %v, want harness.extends.path leading to %s", after, err, keys)
	}
	if before[0].Hash() == after[0].Hash() {
		t.Error("pointing the link elsewhere kept the hash")
	}
}
