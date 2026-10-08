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

// TestCheckSeesAChangedLaunchVariable is the launch's variables, which no file of the
// home holds: a compose on a module exporting CORE_SCRIPTS, setting LOG_LEVEL in its
// Claude Code fragment, and a qory.yaml setting PROFILE, then one change to each. The
// check exits 6 with the stale message and a row for the variable, and a compose brings
// the home up to date again.
func TestCheckSeesAChangedLaunchVariable(t *testing.T) {
	cases := []struct {
		name, file, content, variable string
	}{
		{"a module's export", "modules/core/qory-module.yaml", "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  CORE_SCRIPTS: tools\n", "CORE_SCRIPTS"},
		{"qory.yaml's env", "qory.yaml", envCheckout + "env:\n  PROFILE: two\n", "PROFILE"},
		{"a fragment's env", "modules/core/settings/claude/settings.json", `{"env": {"LOG_LEVEL": "trace"}}`, "LOG_LEVEL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := newCheckout(t)
			writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  CORE_SCRIPTS: scripts\n")
			writeFile(t, filepath.Join(root, "modules", "core", "scripts", "run.sh"), "#!/bin/sh\n")
			writeFile(t, filepath.Join(root, "modules", "core", "tools", "run.sh"), "#!/bin/sh\n")
			writeFile(t, filepath.Join(root, "modules", "core", "settings", "claude", "settings.json"), `{"env": {"LOG_LEVEL": "debug"}}`)
			writeFile(t, filepath.Join(root, "qory.yaml"), envCheckout+"env:\n  PROFILE: one\n")
			if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if out, err := run(t, "harness", "compose", "--check"); err != nil {
				t.Fatalf("check after a compose: %v\n%s", err, out)
			}

			writeFile(t, filepath.Join(root, c.file), c.content)
			out, err := run(t, "harness", "compose", "--check")
			if err == nil {
				t.Fatalf("check after a change to %s: no error\n%s", c.name, out)
			}
			if got := cmd.ExitCode(err); got != cmd.ExitStale {
				t.Errorf("exit %d for %v, want %d", got, err, cmd.ExitStale)
			}
			wants(t, err.Error(), "stale: run qory harness compose")
			wantsRow(t, out, "launch_env/claude/"+c.variable, "changed")

			if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if out, err := run(t, "harness", "compose", "--check"); err != nil {
				t.Fatalf("check after the second compose: %v\n%s", err, out)
			}
		})
	}
}

// envCheckout is a checkout's qory.yaml composing module core for claude, without env.
const envCheckout = "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n"
