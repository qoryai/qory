package acting_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/acting"
	"github.com/qoryai/qory/internal/places"
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

// record records dir as a place whose qory section is the fingerprint of file.
func record(t *testing.T, dir, file string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	places.Forget()
	t.Cleanup(places.Forget)
	err := places.Update(dir, places.KindCheckout, func(r *places.Record) error {
		r.Run = "run-1"
		r.Qory = acting.Fingerprint(r.Path, []string{file})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCheckRefusesAChangedActingKey passes a file as it was recorded and one that changes
// a key qory does not act on, and refuses one that adds a command or changes a variable,
// with the approved line naming the key.
func TestCheckRefusesAChangedActingKey(t *testing.T) {
	dir, file := place(t, document)
	if err := acting.Check(file, []byte(document)); err != nil {
		t.Fatalf("no place recorded: %v", err)
	}
	record(t, dir, file)
	if err := acting.Check(file, []byte(document)); err != nil {
		t.Errorf("as recorded: %v", err)
	}
	other := document + "# a comment\n"
	other = replace(t, other, "model: a-model", "model: another-model")
	if err := acting.Check(file, []byte(other)); err != nil {
		t.Errorf("a key qory does not act on: %v", err)
	}
	for key, changed := range map[string]string{
		"worktree.run.add": replace(t, document, "add: [make setup]", "add: [make setup, make more]"),
		"env.TOKEN_DIR":    replace(t, document, "TOKEN_DIR: /opt/tokens", "TOKEN_DIR: /opt/other"),
		"env.EXTRA":        replace(t, document, "\nenv:\n", "\nenv:\n  EXTRA: one\n"),
	} {
		err := acting.Check(file, []byte(changed))
		want := file + " changes " + key + " since a walled run began here; qory does not act on it, since the run could have written it. qory setup repo --accept-checkout accepts it"
		if err == nil || err.Error() != want || !acting.Refused(err) {
			t.Errorf("%s: %v", key, err)
		}
	}
}

// TestTheRefusalNamesTheFile is a stack file and a harness.yaml in a recorded place, each
// changing an acting key: the refusal names the file, not a qory.yaml.
func TestTheRefusalNamesTheFile(t *testing.T) {
	dir, file := place(t, "apiVersion: qory.dev/v1alpha1\n")
	stack := filepath.Join(dir, "qory-stack.yaml")
	harness := filepath.Join(dir, "harness.yaml")
	record(t, dir, file)
	for f, content := range map[string]string{
		stack:   "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      git: https://git.example.com/acme/core.git\n",
		harness: "apiVersion: qory.dev/v1alpha1\nworktree:\n  run:\n    add: [make]\n",
	} {
		key := "modules[0].source.git"
		if f == harness {
			key = "worktree.run.add"
		}
		want := f + " changes " + key + " since a walled run began here; qory does not act on it, since the run could have written it. qory setup repo --accept-checkout accepts it"
		if err := acting.Check(f, []byte(content)); err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %q", filepath.Base(f), err, want)
		}
	}
}

// TestAFileTheRecordDoesNotHoldIsRefused is a qory file in the place that was not there
// when the place was recorded: any acting key it sets is refused, and one without any
// passes.
func TestAFileTheRecordDoesNotHoldIsRefused(t *testing.T) {
	dir, file := place(t, "apiVersion: qory.dev/v1alpha1\n")
	record(t, dir, file)
	added := filepath.Join(dir, "sub", "qory.yaml")
	if err := acting.Check(added, []byte("worktree:\n  run:\n    add: [make]\n")); !acting.Refused(err) {
		t.Errorf("a new file with a command: %v", err)
	}
	if err := acting.Check(added, []byte("harness:\n  model: a-model\n")); err != nil {
		t.Errorf("a new file without an acting key: %v", err)
	}
}

// TestTheRecordHoldsHashesOnly records a file whose values a person would not want
// written elsewhere: none of them is in the record.
func TestTheRecordHoldsHashesOnly(t *testing.T) {
	dir, file := place(t, document)
	record(t, dir, file)
	r, err := places.Get(dir)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places", places.Name(dir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"make setup", "/opt/tokens", "git.example.com", "debug"} {
		if strings.Contains(string(data), value) {
			t.Errorf("the record holds %q", value)
		}
	}
}

func replace(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("%q is not in the document", old)
	}
	return strings.Replace(s, old, new, 1)
}

// TestALinkThatLeadsOutIsPointedElsewhere records a place whose qory.yaml extends a
// path that leads out of the place through a link in it, then points the link at
// another folder, a folder of keys say: the value is the same, but the hash is not, so
// the change is listed, with where the path leads now.
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
	rec := places.Record{Path: dir, Qory: acting.Fingerprint(dir, []string{file})}
	if changed, err := acting.Changes(rec, file, []byte(content)); err != nil || len(changed) != 0 {
		t.Fatalf("as recorded: %+v, %v", changed, err)
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
	changed, err := acting.Changes(rec, file, []byte(content))
	if err != nil || len(changed) != 1 || changed[0].Key != "harness.extends.path" || changed[0].Value != `"./base"` || changed[0].Leads != keys {
		t.Errorf("pointed elsewhere: %+v, %v, want harness.extends.path leading to %s", changed, err, keys)
	}
}
