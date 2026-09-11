package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

const stackDoc = "apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n"

const composeDoc = "apiVersion: qory.ai/v1alpha1\nharness:\n  extends: {path: ../base}\n  modules:\n    - name: app\n"

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
	writeRaw(t, filepath.Join(checkout, "qory.yaml"), "apiVersion: qory.ai/v1alpha1\nharness: {force: true}\n")
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
	writeRaw(t, file, "apiVersion: qory.ai/v1alpha1\nmodules: []\n")
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
	if err == nil || !strings.HasPrefix(err.Error(), "no qory-stack.yaml, and no qory.yaml with a harness section naming modules, in ") {
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
	writeRaw(t, path, "apiVersion: qory.ai/v1alpha1\nharness:\n  modules:\n    - name: app\n")
	_, err = config.LoadStack(path)
	if err == nil || !strings.Contains(err.Error(), "harness names modules and no target.runtime; the section holds this repository's own stack") {
		t.Fatalf("without extends or a target: %v", err)
	}
	writeRaw(t, path, "apiVersion: qory.ai/v1alpha1\nharness:\n  target: {runtime: codex, model: o3}\n  modules:\n    - name: app\n")
	p, err = config.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Target.Runtimes.First() != "codex" || p.Target.Model != "o3" || len(p.Modules) != 1 || p.Extends.Path != "" {
		t.Fatalf("own stack: %+v", p)
	}
	writeRaw(t, path, "apiVersion: qory.ai/v1alpha1\nharness: {force: true}\n")
	_, err = config.LoadStack(path)
	if err == nil || !strings.Contains(err.Error(), "the harness section names no modules and no stack to extend") {
		t.Fatalf("without a document: %v", err)
	}
}
