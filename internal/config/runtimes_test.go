package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// TestOnlyTheCheckoutsFilePinsARuntime pins codex in the checkout root's qory.yaml,
// which Load takes and Compose reads as the document's pin from that file. A pin of claude
// in an ancestor's qory.yaml is left unread: Load passes, and the document's pins stay the
// checkout's. A pin in the user's file is refused by Load, the fix first and the file
// last, with the checkout's file, read or not, and by LoadUser.
func TestOnlyTheCheckoutsFilePinsARuntime(t *testing.T) {
	home := isolated(t)
	root := filepath.Join(t.TempDir(), "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const pins = "harness:\n  runtimes:\n    claude:\n      version: 2.1.300\n"
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness:\n  extends: {path: ../base}\n  runtimes:\n    codex:\n      version: 0.167.2\n")
	write(t, filepath.Join(filepath.Dir(root), "qory.yaml"), pins)
	for _, readOwn := range []bool{true, false} {
		if _, err := config.Load(root, readOwn); err != nil {
			t.Fatalf("own %v: %v", readOwn, err)
		}
	}
	p, err := config.Compose(own)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.RuntimePins["codex"]; len(p.RuntimePins) != 1 || got.Version != "0.167.2" || got.From != own {
		t.Fatalf("the document's pins are %+v, want codex 0.167.2 from %s alone", p.RuntimePins, own)
	}
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, pins)
	want := "Move runtimes to the checkout's qory.yaml or to the base qory-stack.yaml.\n" +
		"  Only the checkout's qory.yaml and the base stack pin a runtime's version.\n" +
		"  harness.runtimes is in " + user + "."
	for _, readOwn := range []bool{true, false} {
		if _, err := config.Load(root, readOwn); err == nil || err.Error() != want {
			t.Errorf("own %v:\n%v\nwant:\n%s", readOwn, err, want)
		}
	}
	if _, err := config.LoadUser(); err == nil || err.Error() != want {
		t.Errorf("LoadUser:\n%v\nwant:\n%s", err, want)
	}
}

// TestANestedCheckoutLeavesTheOuterPinUnread is a checkout inside another repository,
// such as a git submodule, each with a qory.yaml, the outer one pinning codex: Load of
// the inner checkout passes, DiscoverStack finds the inner qory.yaml, and its compose has
// no pin.
func TestANestedCheckoutLeavesTheOuterPinUnread(t *testing.T) {
	isolated(t)
	outer := filepath.Join(t.TempDir(), "outer")
	inner := filepath.Join(outer, "vendor", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outer, "qory.yaml"), "harness:\n  extends: {path: ../base}\n  runtimes:\n    codex:\n      version: 0.167.2\n")
	own := filepath.Join(inner, "qory.yaml")
	write(t, own, "harness:\n  extends: {path: ../base}\n")
	for _, readOwn := range []bool{true, false} {
		if _, err := config.Load(inner, readOwn); err != nil {
			t.Fatalf("own %v: %v", readOwn, err)
		}
	}
	found, err := config.DiscoverStack(inner)
	if err != nil || found != own {
		t.Fatalf("DiscoverStack returned %q, %v, want %s", found, err, own)
	}
	p, err := config.Compose(found)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.RuntimePins) != 0 {
		t.Fatalf("the inner checkout's pins are %+v, want none", p.RuntimePins)
	}
}

// TestDiscoverStackRefusesAnAncestorsPin is a checkout with no document under an ancestor
// whose qory.yaml pins a runtime and defines no stack: DiscoverStack refuses the pin, the fix
// first and the file last, before any compose reads the ancestor's file.
func TestDiscoverStackRefusesAnAncestorsPin(t *testing.T) {
	isolated(t)
	root := filepath.Join(t.TempDir(), "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(filepath.Dir(root), "qory.yaml")
	write(t, ancestor, "harness:\n  runtimes:\n    claude:\n      version: 2.1.300\n")
	want := "Move runtimes to the checkout's qory.yaml or to the base qory-stack.yaml.\n" +
		"  Only the checkout's qory.yaml and the base stack pin a runtime's version.\n" +
		"  harness.runtimes is in " + ancestor + "."
	if found, err := config.DiscoverStack(root); err == nil || err.Error() != want {
		t.Fatalf("DiscoverStack returned %q, %v\nwant:\n%s", found, err, want)
	}
}

// TestComposeRefusesAPinWrittenAsANumber pins codex in the checkout's qory.yaml to an
// unquoted 2.1, a YAML number: Compose refuses it, the fix first and the file last.
func TestComposeRefusesAPinWrittenAsANumber(t *testing.T) {
	isolated(t)
	root := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness:\n  extends: {path: ../base}\n  runtimes:\n    codex:\n      version: 2.1\n")
	want := "Put runtimes.codex.version in quotes, such as version: \"2.1.320\".\n" +
		"  Without quotes, YAML reads the version as a number or a date, and an exact version is a string.\n" +
		"  The value is 2.1.\n" +
		"  runtimes.codex is in " + own + "."
	if _, err := config.Compose(own); err == nil || err.Error() != want {
		t.Fatalf("Compose returned %v\nwant:\n%s", err, want)
	}
}
