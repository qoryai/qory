package cmd_test

import (
	"maps"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestCheckSeesAStaleHome is the CI gate: a compose, then an edit to the app module's
// instructions, which the composed AGENTS.md and CLAUDE.md are generated copies of. The
// check exits 6 naming both copies and writes nothing; a compose brings the home up to
// date and the check exits 0.
func TestCheckSeesAStaleHome(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.1.0")
	writeFile(t, filepath.Join(root, "modules", "app", "AGENTS.md"), "# App rules\n")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "compose", "--check")
	if err != nil {
		t.Fatalf("check after a compose: %v\n%s", err, out)
	}
	wants(t, out, "up to date")
	wantsRow(t, out, "home", ".qory/harness")

	writeFile(t, filepath.Join(root, "modules", "app", "AGENTS.md"), "# App rules, revised\n")
	before := snapshot(t, root)
	out, err = run(t, "harness", "compose", "--check")
	if err == nil {
		t.Fatalf("check on a stale home: no error\n%s", out)
	}
	if got := cmd.ExitCode(err); got != cmd.ExitStale {
		t.Errorf("exit %d for %v, want %d", got, err, cmd.ExitStale)
	}
	wants(t, err.Error(), "stale: run qory harness compose")
	wantsRow(t, out, "AGENTS.md", "changed")
	wantsRow(t, out, "claude/CLAUDE.md", "changed")
	lacks(t, out, "up to date", "composed")
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the check changed the checkout")
	}
	// The report still says what the last compose wrote; the check does not touch it.
	if rep := readReport(t, root); len(rep.Entries) == 0 {
		t.Error("the check emptied the report")
	}

	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Fatalf("check after the second compose: %v\n%s", err, out)
	}
}

// TestCheckSeesANewEntry is a skill added to a module and not composed yet: the check
// names the links a compose would write as missing.
func TestCheckSeesANewEntry(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.1.0")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "release", "SKILL.md"), "# release\n")
	out, err := run(t, "harness", "compose", "--check")
	if got := cmd.ExitCode(err); got != cmd.ExitStale {
		t.Fatalf("exit %d for %v, want %d\n%s", got, err, cmd.ExitStale, out)
	}
	wantsRow(t, out, "skills/release", "missing")
	wantsRow(t, out, "claude/skills/release", "missing")
}

// TestCheckWithNothingComposed is a checkout that was never composed: the check exits 6
// and says so, since a CI gate that runs before a compose has nothing to compare.
func TestCheckWithNothingComposed(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.1.0")
	_, err := run(t, "harness", "compose", "--check")
	if got := cmd.ExitCode(err); got != cmd.ExitStale {
		t.Fatalf("exit %d for %v, want %d", got, err, cmd.ExitStale)
	}
	wants(t, err.Error(), "nothing composed here; run qory harness compose")
	gone(t, root, ".qory", ".claude")
}

// TestCheckRefusesForce is --check with --force: the check writes nothing, so there is
// nothing to replace, and the pair is an input error.
func TestCheckRefusesForce(t *testing.T) {
	root := newCheckout(t)
	writeAppCheckout(t, root, ">=0.1.0")
	_, err := run(t, "harness", "compose", "--check", "--force")
	if got := cmd.ExitCode(err); got != cmd.ExitInput {
		t.Fatalf("exit %d for %v, want %d", got, err, cmd.ExitInput)
	}
	wants(t, err.Error(), "--check writes nothing, so --force has nothing to replace")
}
