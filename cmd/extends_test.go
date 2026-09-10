package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// baseProfile is the profile of an operator's harness repository: two layers, closed
// except for skills, agents and permission allow rules, rendered for claude with a model.
const baseProfile = `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
name: nextjs-15
target: {runtime: claude, model: opus}
layers:
  - name: core
  - name: tools
    link: harness
extending:
  kinds: [skills, agents]
  instructions: true
  settings: [permissions.allow]
extensions:
  acme: {required_check: Harness self-tests}
`

// operatorRepo makes the operator's repository as a git remote: the base profile under
// nextjs-15/, the layers under layers/, with a skill, a hook and a deny rule in core, and
// returns its file URL.
func operatorRepo(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Operator")
	runGit(t, dir, "config", "user.email", "op@example.com")
	writeFile(t, filepath.Join(dir, "nextjs-15", "harness-compose.yaml"), baseProfile)
	writeManifest(t, filepath.Join(dir, "layers", "core"), "core")
	writeFile(t, filepath.Join(dir, "layers", "core", "skills", "review", "SKILL.md"), "# review\n")
	writeFile(t, filepath.Join(dir, "layers", "core", "hooks", "guard.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "layers", "core", "settings", "claude", "settings.json"), `{"permissions": {"deny": ["Bash(git push:*)"]}, "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "$QORY_HARNESS_HOME/hooks/guard.sh"}]}]}}`)
	writeFile(t, filepath.Join(dir, "layers", "core", "AGENTS.md"), "# Core rules\n")
	writeManifest(t, filepath.Join(dir, "layers", "tools"), "tools")
	writeFile(t, filepath.Join(dir, "layers", "tools", "scripts", "check.sh"), "#!/bin/sh\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "harness")
	runGit(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	return "file://" + dir
}

// customerProfile is the customer's file: the base at a branch, plus their own layer.
func customerProfile(url string) string {
	return "apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\nextends: {git: " + url + ", ref: main, path: nextjs-15}\nlayers:\n  - name: app\nextensions:\n  customer: {team: web}\n"
}

// customerCheckout is a product repository with the customer's profile and an app layer
// shipping a skill, an agent, an AGENTS.md and an allow rule.
func customerCheckout(t *testing.T, url string) string {
	t.Helper()
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), customerProfile(url))
	writeManifest(t, filepath.Join(root, "layers", "app"), "app")
	writeFile(t, filepath.Join(root, "layers", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
	writeFile(t, filepath.Join(root, "layers", "app", "agents", "planner.md"), "---\nname: planner\n---\nPlan.\n")
	writeFile(t, filepath.Join(root, "layers", "app", "AGENTS.md"), "# App rules\n")
	writeFile(t, filepath.Join(root, "layers", "app", "settings", "claude", "settings.json"), `{"permissions": {"allow": ["Bash(npm test)"]}}`)
	return root
}

// TestExtendsComposesTheBaseFirstAndClosed is the customer's checkout: the base's layers
// come first with the base's target, the customer's layer is appended, the base's hook and
// deny rule are in the settings beside the customer's allow rule, the base's layer link is
// written, the report names the base with its pin, and inspect prints it.
func TestExtendsComposesTheBaseFirstAndClosed(t *testing.T) {
	url := operatorRepo(t)
	root := customerCheckout(t, url)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "claude opus")
	wantsRow(t, out, "link", "harness  (layer tools)")
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Base == nil || rep.Base.Name != "nextjs-15" || rep.Base.Source != url+"#main:nextjs-15" || len(rep.Base.Pin) != 12 {
		t.Fatalf("base: %+v", rep.Base)
	}
	if len(rep.Layers) != 3 || rep.Layers[0].Name != "core" || !rep.Layers[0].Base || rep.Layers[1].Name != "tools" || rep.Layers[2].Name != "app" || rep.Layers[2].Base {
		t.Fatalf("layers: %+v", rep.Layers)
	}
	if rep.Extensions["acme"] == nil || rep.Extensions["customer"] == nil {
		t.Errorf("extensions: %v", rep.Extensions)
	}
	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(settings), `"Bash(git push:*)"`, `"Bash(npm test)"`, "guard.sh", `"model": "opus"`)
	instructions, _ := os.ReadFile(filepath.Join(root, ".claude", "CLAUDE.md"))
	if !strings.HasPrefix(string(instructions), "# Core rules\n") || !strings.Contains(string(instructions), "# App rules") {
		t.Errorf("instructions:\n%s", instructions)
	}
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "extends", "nextjs-15", "core", "base")
}

// TestExtendsRefusesWhatTheBaseCloses is the customer changing what the base decides:
// a hook in their layer, an entry colliding with the base's, a deny rule in their
// fragment, a runtime the base does not render for, a model from the command line or
// from the checkout's own qory.yaml, a target in their own file, and a base namespace in
// their extensions.
func TestExtendsRefusesWhatTheBaseCloses(t *testing.T) {
	url := operatorRepo(t)
	root := customerCheckout(t, url)
	profile := filepath.Join(root, "harness-compose.yaml")
	refuse := func(name, want string, exit int, args ...string) {
		t.Helper()
		_, err := run(t, append([]string{"harness", "compose"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != exit {
			t.Errorf("%s: err = %v, exit %d; want %q with exit %d", name, err, cmd.ExitCode(err), want, exit)
		}
	}
	writeFile(t, filepath.Join(root, "layers", "app", "hooks", "leak.sh"), "#!/bin/sh\n")
	refuse("a hook", "layer app ships hooks/leak.sh, and the base profile nextjs-15@", cmd.ExitInput)
	os.Remove(filepath.Join(root, "layers", "app", "hooks", "leak.sh"))
	writeFile(t, filepath.Join(root, "layers", "app", "skills", "review", "SKILL.md"), "# mine\n")
	refuse("a collision with the base", "skills/review is provided by 2 layers", cmd.ExitCollision)
	os.RemoveAll(filepath.Join(root, "layers", "app", "skills", "review"))
	writeFile(t, filepath.Join(root, "layers", "app", "settings", "claude", "settings.json"), `{"permissions": {"deny": ["Read"]}}`)
	refuse("a deny rule", "layer app sets permissions.deny in settings/claude/settings.json, and the base profile nextjs-15@", cmd.ExitInput)
	writeFile(t, filepath.Join(root, "layers", "app", "settings", "claude", "settings.json"), `{"permissions": {"allow": ["Bash(npm test)"]}}`)
	refuse("another runtime", "runtime codex is not one the base profile nextjs-15@", cmd.ExitInput, "--runtime", "codex")
	refuse("another model", "the model is the base profile nextjs-15@", cmd.ExitInput, "--model", "sonnet")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.ai/v1alpha1\nkind: QoryConfig\nruntime: codex\n")
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Errorf("the checkout's own qory.yaml was read under extends: %v", err)
	}
	os.Remove(filepath.Join(root, "qory.yaml"))
	writeFile(t, profile, strings.Replace(customerProfile(url), "layers:\n", "target: {runtime: claude}\nlayers:\n", 1))
	refuse("a target of its own", "target is the base profile's", cmd.ExitInput)
	writeFile(t, profile, strings.Replace(customerProfile(url), "customer:", "acme:", 1))
	refuse("the base's namespace", "extensions.acme is the base profile's", cmd.ExitInput)
}

// TestExtendsNamesABaseThatIsNotReachable is a base at a remote that cannot be fetched:
// the message says the profile is not reachable from here, with exit 1.
func TestExtendsNamesABaseThatIsNotReachable(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), customerProfile("file:///nowhere/at/all"))
	writeManifest(t, filepath.Join(root, "layers", "app"), "app")
	_, err := run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "the profile file:///nowhere/at/all#main:nextjs-15 is not reachable from here") || cmd.ExitCode(err) != 1 {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestExtendsRefusesAClosedBase is a base profile without an extending block: nothing
// may extend it, and the message says so.
func TestExtendsRefusesAClosedBase(t *testing.T) {
	base := tempDir(t)
	writeFile(t, filepath.Join(base, "harness-compose.yaml"), strings.Replace(baseProfile, "extending:\n  kinds: [skills, agents]\n  instructions: true\n  settings: [permissions.allow]\n", "", 1))
	writeManifest(t, filepath.Join(base, "layers", "core"), "core")
	writeManifest(t, filepath.Join(base, "layers", "tools"), "tools")
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), "apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\nextends: {path: "+base+"}\nlayers:\n  - name: app\n")
	writeManifest(t, filepath.Join(root, "layers", "app"), "app")
	_, err := run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "is closed: it declares no extending block") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}
