package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// baseStack is the stack of a publisher's harness repository: two modules, closed
// except for skills, agents and permission allow rules, rendered for claude with a model.
const baseStack = `apiVersion: qory.dev/v1alpha1
name: nextjs-15
target: {runtime: claude, model: opus}
modules:
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

// baseRepo makes the base stack's repository as a git remote: the stack under
// nextjs-15/, the modules under modules/, with a skill, a hook and a deny rule in core, and
// returns its file URL.
func baseRepo(t *testing.T) string {
	t.Helper()
	return baseRepoWith(t, baseStack)
}

// baseRepoWith is [baseRepo] with the given base stack.
func baseRepoWith(t *testing.T, stack string) string {
	t.Helper()
	dir := tempDir(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Publisher")
	runGit(t, dir, "config", "user.email", "op@example.com")
	writeFile(t, filepath.Join(dir, "nextjs-15", "qory-stack.yaml"), stack)
	writeManifest(t, filepath.Join(dir, "modules", "core"), "core")
	writeFile(t, filepath.Join(dir, "modules", "core", "skills", "review", "SKILL.md"), "# review\n")
	writeFile(t, filepath.Join(dir, "modules", "core", "hooks", "guard.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, "modules", "core", "settings", "claude", "settings.json"), `{"permissions": {"deny": ["Bash(git push:*)"]}, "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "$QORY_HARNESS_HOME/hooks/guard.sh"}]}]}}`)
	writeFile(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core rules\n")
	writeManifest(t, filepath.Join(dir, "modules", "tools"), "tools")
	writeFile(t, filepath.Join(dir, "modules", "tools", "scripts", "check.sh"), "#!/bin/sh\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "harness")
	runGit(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	return "file://" + dir
}

// consumerCompose is the consumer's qory.yaml: under harness, the base at a branch and
// their own module, with extra lines of that section first.
func consumerCompose(url string, extra ...string) string {
	return "apiVersion: qory.dev/v1alpha1\nharness:\n" + strings.Join(extra, "") + "  extends: {git: " + url + ", ref: main, path: nextjs-15}\n  modules:\n    - name: app\n  extensions:\n    consumer: {team: web}\n"
}

// consumerCheckout is a product repository with the consumer's qory.yaml and an app module
// shipping a skill, an agent, an AGENTS.md and an allow rule.
func consumerCheckout(t *testing.T, url string) string {
	t.Helper()
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url))
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
	writeFile(t, filepath.Join(root, "modules", "app", "agents", "planner.md"), "---\nname: planner\n---\nPlan.\n")
	writeFile(t, filepath.Join(root, "modules", "app", "AGENTS.md"), "# App rules\n")
	writeFile(t, filepath.Join(root, "modules", "app", "settings", "claude", "settings.json"), `{"permissions": {"allow": ["Bash(npm test)"]}}`)
	return root
}

// TestExtendsComposesTheBaseFirstAndClosed is the consumer's checkout: the base's modules
// come first with the base's target, the consumer's module is appended, the base's hook and
// deny rule are in the settings beside the consumer's allow rule, the base's module link is
// written, the report names the base with its pin, and inspect prints it.
func TestExtendsComposesTheBaseFirstAndClosed(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "claude opus")
	wantsRow(t, out, "link", "harness  (module tools)")
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Base == nil || rep.Base.Name != "nextjs-15" || rep.Base.Source != url+"#main:nextjs-15" || len(rep.Base.Pin) != 12 {
		t.Fatalf("base: %+v", rep.Base)
	}
	if len(rep.Modules) != 3 || rep.Modules[0].Name != "core" || !rep.Modules[0].Base || rep.Modules[1].Name != "tools" || rep.Modules[2].Name != "app" || rep.Modules[2].Base {
		t.Fatalf("modules: %+v", rep.Modules)
	}
	if rep.Extensions["acme"] == nil || rep.Extensions["consumer"] == nil {
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

// TestExtendsRefusesWhatTheBaseCloses is the consumer changing what the base decides:
// a hook in their module, an entry colliding with the base's, a deny rule in their
// fragment, a runtime the base does not render for, a model from the command line or
// from the checkout's own qory.yaml, a target in that file, and a base namespace in
// their extensions.
func TestExtendsRefusesWhatTheBaseCloses(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	compose := filepath.Join(root, "qory.yaml")
	refuse := func(name, want string, exit int, args ...string) {
		t.Helper()
		_, err := run(t, append([]string{"harness", "compose"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != exit {
			t.Errorf("%s: err = %v, exit %d; want %q with exit %d", name, err, cmd.ExitCode(err), want, exit)
		}
	}
	writeFile(t, filepath.Join(root, "modules", "app", "hooks", "leak.sh"), "#!/bin/sh\n")
	refuse("a hook", "module app ships hooks/leak.sh, and the base stack nextjs-15@", cmd.ExitInput)
	os.Remove(filepath.Join(root, "modules", "app", "hooks", "leak.sh"))
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "review", "SKILL.md"), "# mine\n")
	refuse("a collision with the base", "skills/review is provided by 2 modules", cmd.ExitCollision)
	os.RemoveAll(filepath.Join(root, "modules", "app", "skills", "review"))
	writeFile(t, filepath.Join(root, "modules", "app", "settings", "claude", "settings.json"), `{"permissions": {"deny": ["Read"]}}`)
	refuse("a deny rule", "module app sets permissions.deny in settings/claude/settings.json, and the base stack nextjs-15@", cmd.ExitInput)
	writeFile(t, filepath.Join(root, "modules", "app", "settings", "claude", "settings.json"), `{"permissions": {"allow": ["Bash(npm test)"]}}`)
	refuse("another runtime", "runtime codex is not one the base stack nextjs-15@", cmd.ExitInput, "--runtime", "codex")
	refuse("another model", "the model is the base stack nextjs-15@", cmd.ExitInput, "--model", "sonnet")
	writeFile(t, compose, consumerCompose(url, "  runtime: codex\n"))
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Errorf("the checkout's own runtime was read under extends: %v", err)
	}
	writeFile(t, compose, consumerCompose(url, "  target: {runtime: claude}\n"))
	refuse("a target of its own", "target is the base stack's; a harness section that extends a stack does not set it", cmd.ExitInput)
	writeFile(t, compose, strings.Replace(consumerCompose(url), "consumer:", "acme:", 1))
	refuse("the base's namespace", "extensions.acme is the base stack's", cmd.ExitInput)
}

// TestExtendsNamesABaseThatIsNotReachable is a base at a remote that cannot be fetched:
// the message says the stack is not reachable from here, with exit 1.
func TestExtendsNamesABaseThatIsNotReachable(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose("file:///nowhere/at/all"))
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	_, err := run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "the stack file:///nowhere/at/all#main:nextjs-15 is not reachable from here") || cmd.ExitCode(err) != 1 {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestExtendsRefusesAClosedBase is a base stack without an extending block: nothing
// may extend it, and the message says so.
func TestExtendsRefusesAClosedBase(t *testing.T) {
	base := tempDir(t)
	writeFile(t, filepath.Join(base, "qory-stack.yaml"), strings.Replace(baseStack, "extending:\n  kinds: [skills, agents]\n  instructions: true\n  settings: [permissions.allow]\n", "", 1))
	writeManifest(t, filepath.Join(base, "modules", "core"), "core")
	writeManifest(t, filepath.Join(base, "modules", "tools"), "tools")
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: "+base+"}\n  modules:\n    - name: app\n")
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	_, err := run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "is closed: it declares no extending block") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestExtendsGovernsFilesByPrefix is the base opening claude/rules to consumers: a rule
// file lands in .claude/rules through the link, a file under another path is refused
// naming the prefixes, a consumer naming a base module to exclude from it is told the
// module belongs to the base, and the machine keys of the checkout's qory.yaml are
// announced as not read.
func TestExtendsGovernsFilesByPrefix(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	compose := filepath.Join(root, "qory.yaml")
	_, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "modules", "app", "files", "claude", "rules", "web.md"), "# web\n")
	_, err = run(t, "harness", "compose")
	want := "module app ships files/claude/rules/web.md, and the base stack nextjs-15@"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "ship files under no path") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("closed files: err = %v, exit %d", err, cmd.ExitCode(err))
	}
	openBase := baseRepoWith(t, strings.Replace(baseStack, "  settings: [permissions.allow]\n", "  settings: [permissions.allow]\n  files: [claude/rules/]\n", 1))
	writeFile(t, compose, consumerCompose(openBase, "  runtime: codex\n"))
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "skipped", "qory.yaml  (its harness, git and env keys; the base stack decides under extends)")
	out, err = run(t, "harness", "compose", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "skipped", "qory.yaml  (its harness, git and env keys; the base stack decides under extends)")
	if data, err := os.ReadFile(filepath.Join(root, ".claude", "rules", "web.md")); err != nil || string(data) != "# web\n" {
		t.Fatalf("the rule through the link: %q, %v", data, err)
	}
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "files/claude/rules/web.md  app")
	writeFile(t, filepath.Join(root, "modules", "app", "files", "claude", "rules-private", "x.md"), "# x\n")
	_, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "module app ships files/claude/rules-private/x.md, and the base stack nextjs-15@") || !strings.Contains(err.Error(), "ship files under claude/rules/") {
		t.Fatalf("a longer segment: err = %v", err)
	}
	os.RemoveAll(filepath.Join(root, "modules", "app", "files", "claude", "rules-private"))
	writeFile(t, compose, strings.Replace(consumerCompose(openBase), "    - name: app\n", "    - name: app\n    - name: core\n      exclude: {skills: [review]}\n", 1))
	_, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "module core belongs to the base stack; a checkout's qory.yaml cannot name a base module, exclude from it or replace it") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a base module named: err = %v, exit %d", err, cmd.ExitCode(err))
	}
}
