package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

const stackDoc = "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n"

const composeDoc = "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  modules:\n    - name: app\n"

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// deliveredDoc is a stack with an extending block: one a harness repository delivers,
// and composes at its own root to test.
const deliveredDoc = stackDoc + "extending:\n  kinds: [skills]\n"

// TestDiscoverStackPrefersTheCheckoutRoot returns the checkout's own stack even when an
// ancestor directory holds one, and a qory.yaml with only machine keys beside it is not
// in the way.
func TestDiscoverStackPrefersTheCheckoutRoot(t *testing.T) {
	hermetic(t)
	base := t.TempDir()
	checkout := filepath.Join(base, "app")
	writeRaw(t, filepath.Join(base, stack.FileName), stackDoc)
	writeRaw(t, filepath.Join(checkout, stack.FileName), deliveredDoc)
	writeRaw(t, filepath.Join(checkout, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness: {force: true}\n")
	got, err := config.DiscoverStack(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(checkout, stack.FileName); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverStackRefusesAClosedStackAtTheRoot is a checkout whose root holds a
// qory-stack.yaml with no extending block: a delivered stack nothing can extend, which is
// a repository's own stack in the wrong file. The same file in an ancestor directory
// composes the checkouts under it and is found as it is.
func TestDiscoverStackRefusesAClosedStackAtTheRoot(t *testing.T) {
	hermetic(t)
	checkout := t.TempDir()
	file := filepath.Join(checkout, stack.FileName)
	writeRaw(t, file, stackDoc)
	_, err := config.DiscoverStack(checkout)
	want := file + ": a stack at a repository root is delivered to be extended, and this one declares no extending block; a repository's own stack goes under harness in qory.yaml, which qory setup repo writes"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	// The load error of a stack that does not read comes first, so the message names
	// the mistake in the file rather than the missing block.
	writeRaw(t, file, "apiVersion: qory.dev/v1alpha1\nmodules: []\n")
	_, err = config.DiscoverStack(checkout)
	if err == nil || !strings.Contains(err.Error(), "target.runtime is required") {
		t.Fatalf("a stack that does not load: %v", err)
	}

	base := t.TempDir()
	writeRaw(t, filepath.Join(base, stack.FileName), stackDoc)
	below := filepath.Join(base, "app")
	if err := os.MkdirAll(below, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := config.DiscoverStack(below)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, stack.FileName); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverStackTakesTheNearestAncestor walks up one directory at a time, and a
// qory.yaml whose harness section names modules counts like a stack file.
func TestDiscoverStackTakesTheNearestAncestor(t *testing.T) {
	hermetic(t)
	base := t.TempDir()
	checkout := filepath.Join(base, "code", "team", "app")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, filepath.Join(base, stack.FileName), stackDoc)
	writeRaw(t, filepath.Join(base, "code", "qory.yaml"), composeDoc)
	got, err := config.DiscoverStack(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "code", "qory.yaml"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverStackRefusesBothAndNamesNone is a directory holding a stack file and a
// qory.yaml whose harness section names modules, and a checkout with nothing above it.
func TestDiscoverStackRefusesBothAndNamesNone(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	writeRaw(t, filepath.Join(dir, stack.FileName), stackDoc)
	writeRaw(t, filepath.Join(dir, "qory.yaml"), composeDoc)
	_, err := config.DiscoverStack(dir)
	want := dir + " holds both qory-stack.yaml and a qory.yaml whose harness section names modules; a directory holds one of the two"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	empty := t.TempDir()
	t.Chdir(empty)
	_, err = config.DiscoverStack(".")
	if err == nil || !strings.HasPrefix(err.Error(), "no qory-stack.yaml, and no qory.yaml or harness.yaml whose harness section names modules or a stack to extend, in ") {
		t.Fatalf("err = %v", err)
	}
}

// TestLoadStackReadsTheHarnessSectionAsTheComposeDocument reads a qory.yaml through its
// harness section: the stack it extends and its modules come out as a compose document,
// a machine key beside them is no error, and a section without extends is refused.
func TestLoadStackReadsTheHarnessSectionAsTheComposeDocument(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "qory.yaml")
	writeRaw(t, path, strings.Replace(composeDoc, "harness:\n", "harness:\n  runtime: codex\n  name: app\n", 1))
	p, err := config.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends.Path != "../base" || len(p.Modules) != 1 || p.Modules[0].Name != "app" || p.Name != "app" || p.File != path {
		t.Fatalf("compose: %+v", p)
	}
	writeRaw(t, path, "apiVersion: qory.dev/v1alpha1\nharness:\n  modules:\n    - name: app\n")
	_, err = config.LoadStack(path)
	if err == nil || !strings.Contains(err.Error(), "harness names no target.runtime and no stack to extend; the section holds this repository's own stack") {
		t.Fatalf("without extends or a target: %v", err)
	}
	writeRaw(t, path, "apiVersion: qory.dev/v1alpha1\nharness:\n  target: {runtime: codex, model: o3}\n  modules:\n    - name: app\n")
	p, err = config.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Target.Runtimes.First() != "codex" || p.Target.Model != "o3" || len(p.Modules) != 1 || p.Extends.Path != "" {
		t.Fatalf("own stack: %+v", p)
	}
	writeRaw(t, path, "apiVersion: qory.dev/v1alpha1\nharness: {force: true}\n")
	_, err = config.LoadStack(path)
	if err == nil || !strings.Contains(err.Error(), "the harness section names no modules, no stack to extend and no extensions") {
		t.Fatalf("without a document: %v", err)
	}
}

// TestFileInHoldsOneOfTwoNames is a directory with neither name, with each name alone,
// and with both: the file found is the one there, and both is an error naming the
// directory, since one file at each level is the rule and neither name comes first.
func TestFileInHoldsOneOfTwoNames(t *testing.T) {
	dir := t.TempDir()
	got, err := config.FileIn(dir)
	if err != nil || got != "" {
		t.Fatalf("no file: got %q, %v", got, err)
	}
	qory := filepath.Join(dir, "qory.yaml")
	writeRaw(t, qory, "")
	if got, err := config.FileIn(dir); err != nil || got != qory {
		t.Fatalf("qory.yaml alone: got %q, %v", got, err)
	}
	harness := filepath.Join(dir, "harness.yaml")
	writeRaw(t, harness, "")
	_, err = config.FileIn(dir)
	want := dir + " holds both qory.yaml and harness.yaml; a directory holds one of the two"
	if err == nil || err.Error() != want {
		t.Fatalf("both: err = %v, want %q", err, want)
	}
	if err := os.Remove(qory); err != nil {
		t.Fatal(err)
	}
	if got, err := config.FileIn(dir); err != nil || got != harness {
		t.Fatalf("harness.yaml alone: got %q, %v", got, err)
	}
}

// TestHarnessYamlIsReadAtEveryLevel is the file under its other name in the user's
// directory, in an ancestor the user owns and in the checkout root: Discover lists the
// three in the order they apply, Load takes the nearest value and keeps what only a
// farther file sets, and a root holding both names is refused.
func TestHarnessYamlIsReadAtEveryLevel(t *testing.T) {
	home := hermetic(t)
	user := filepath.Join(home, ".config", "qory", "harness.yaml")
	write(t, user, "harness: {runtime: gemini, force: true}\n")
	base := t.TempDir()
	root := filepath.Join(base, "code", "app")
	ancestor := filepath.Join(base, "code", "harness.yaml")
	write(t, ancestor, "harness: {runtime: codex}\nenv: {A: ancestor}\n")
	own := filepath.Join(root, "harness.yaml")
	write(t, own, "harness: {runtime: claude}\n")
	files, err := config.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, " ") != user+" "+ancestor+" "+own {
		t.Fatalf("files: %v", files)
	}
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime.String() != "claude" || !c.Force || c.Env["A"] != "ancestor" {
		t.Fatalf("effective: %+v", c)
	}
	write(t, filepath.Join(root, "qory.yaml"), "")
	_, err = config.Load(root, true)
	want := root + " holds both qory.yaml and harness.yaml; a directory holds one of the two"
	if err == nil || err.Error() != want {
		t.Fatalf("both names: err = %v, want %q", err, want)
	}
}

// TestDiscoverStackReadsHarnessYaml is the document under its other name: found in an
// ancestor when the root holds none, passed over in the root when its harness section
// holds machine keys alone, and found in the root when the section composes. Extensions
// alone make a document, since they are what a checkout adds beside its base.
func TestDiscoverStackReadsHarnessYaml(t *testing.T) {
	hermetic(t)
	base := t.TempDir()
	root := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(base, "code", "harness.yaml")
	writeRaw(t, ancestor, composeDoc)
	own := filepath.Join(root, "harness.yaml")
	for _, c := range []struct{ body, want string }{
		{"", ancestor},
		{"harness: {runtime: codex}\n", ancestor},
		{"harness:\n  extensions:\n    customer: {team: web}\n", own},
		{composeDoc, own},
	} {
		if c.body != "" {
			writeRaw(t, own, c.body)
		}
		got, err := config.DiscoverStack(root)
		if err != nil {
			t.Fatalf("%q: %v", c.body, err)
		}
		if got != c.want {
			t.Errorf("%q: got %s, want %s", c.body, got, c.want)
		}
	}
}
