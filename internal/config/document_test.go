package config_test

import (
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestDocumentIsTheRootsOwnComposeFile is what compose -f names a base for: nothing when
// the root holds no file or one of machine keys alone, the file when its harness section
// composes, and the error of a file that does not read, so a mistake in it is reported
// where it is.
func TestDocumentIsTheRootsOwnComposeFile(t *testing.T) {
	root := t.TempDir()
	got, err := config.Document(root)
	if err != nil || got != "" {
		t.Fatalf("no file: got %q, %v", got, err)
	}
	path := filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness: {runtime: codex}\n")
	got, err = config.Document(root)
	if err != nil || got != "" {
		t.Fatalf("machine keys alone: got %q, %v", got, err)
	}
	writeRaw(t, path, "harness:\n  modules:\n    - name: app\n")
	got, err = config.Document(root)
	if err != nil || got != path {
		t.Fatalf("a document: got %q, %v", got, err)
	}
	writeRaw(t, path, "harness: {nope: 1}\n")
	_, err = config.Document(root)
	want := path + `: line 1: key "nope" is not one harness.yaml reads`
	if err == nil || err.Error() != want {
		t.Fatalf("a file that does not read: err = %v, want %q", err, want)
	}
}

// TestOnBaseTakesTheBaseFromTheFlag is the checkout's document composed on the stack -f
// named: the base's directory, relative to the document, stands where extends would,
// whether the document left extends out or named a source of its own, and what it named
// comes back beside the stack so the compose can say what -f replaced. A document of
// extensions alone composes with no module of its own.
func TestOnBaseTakesTheBaseFromTheFlag(t *testing.T) {
	tmp := t.TempDir()
	base := filepath.Join(tmp, "tree", "nextjs-15", stack.FileName)
	writeRaw(t, base, stackDoc)
	path := filepath.Join(tmp, "app", "qory.yaml")
	rel := filepath.Join("..", "tree", "nextjs-15")
	writeRaw(t, path, "harness:\n  modules:\n    - name: app\n")
	p, named, err := config.OnBase(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends.Path != rel || named != (stack.Source{}) || len(p.Modules) != 1 || p.Modules[0].Name != "app" || p.File != path {
		t.Fatalf("without extends: %+v, named %+v", p, named)
	}
	writeRaw(t, path, "harness:\n  extends: {git: https://git.example.com/acme/harness, ref: main, path: x}\n  modules:\n    - name: app\n")
	p, named, err = config.OnBase(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if want := (stack.Source{Git: "https://git.example.com/acme/harness", Ref: "main", Path: "x"}); p.Extends != (stack.Source{Path: rel}) || named != want {
		t.Fatalf("with extends: %+v, named %+v, want %+v", p.Extends, named, want)
	}
	writeRaw(t, path, "harness:\n  extensions:\n    consumer: {team: web}\n")
	p, named, err = config.OnBase(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends.Path != rel || named != (stack.Source{}) || len(p.Modules) != 0 || p.Extensions["consumer"]["team"] != "web" {
		t.Fatalf("extensions alone: %+v", p)
	}
}

// TestOnBaseRefusesWhatHasNoBase is a document holding this repository's own stack, a
// target, in which -f has nothing to replace; a section of machine keys alone, which is
// no document; and a harness.yaml with a key it does not read, whose message names the
// file by that name.
func TestOnBaseRefusesWhatHasNoBase(t *testing.T) {
	tmp := t.TempDir()
	base := filepath.Join(tmp, "tree", stack.FileName)
	path := filepath.Join(tmp, "app", "harness.yaml")
	for _, c := range []struct{ body, want string }{
		{"harness:\n  target: {runtime: claude}\n  modules:\n    - name: app\n", path + ": harness sets a target, this repository's own stack, and -f names a base for a document that extends one; a document takes its base under extends or from -f, in place of a target"},
		{"harness: {runtime: codex}\n", path + ": the harness section names no modules, no stack to extend and no extensions"},
		{"harness:\n  nope: 1\n", path + `: line 2: key "nope" is not one harness.yaml reads`},
	} {
		writeRaw(t, path, c.body)
		_, _, err := config.OnBase(path, base)
		if err == nil || err.Error() != c.want {
			t.Errorf("%q: err = %v, want %q", c.body, err, c.want)
		}
	}
}
