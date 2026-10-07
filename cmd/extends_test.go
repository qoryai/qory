package cmd_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/ui"
)

// baseStack is the stack of a publisher's harness repository: two modules, closed
// except for skills, agents and permission allow rules, and no target, since the
// checkout extending it says what it composes the stack for.
const baseStack = `apiVersion: qory.dev/v1alpha1
name: nextjs-15
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

// consumerTarget is the target the consumer's qory.yaml composes the base for.
const consumerTarget = "  target: {runtime: claude, model: opus}\n"

// consumerCompose is the consumer's qory.yaml: under harness, the base at a branch, the
// target it is composed for and their own module, with extra lines of that section first.
func consumerCompose(url string, extra ...string) string {
	return "apiVersion: qory.dev/v1alpha1\nharness:\n" + strings.Join(extra, "") + "  extends: {git: " + url + ", ref: main, path: nextjs-15}\n" + consumerTarget + "  modules:\n    - name: app\n  extensions:\n    consumer: {team: web}\n"
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
// a hook in their module, an entry colliding with the base's and a deny rule in their
// fragment.
func TestExtendsRefusesWhatTheBaseCloses(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
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
}

// TestExtendsMergesExtensionsByKey is a checkout setting a key the base sets, beside a
// scalar and a list of its own: its value replaces the base's whole, and the rest of
// both files is carried.
func TestExtendsMergesExtensionsByKey(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	writeFile(t, filepath.Join(root, "qory.yaml"), strings.Replace(consumerCompose(url), "    consumer: {team: web}\n", "    acme: {team: web}\n    sweep_floor: 40\n    corpus_roots: [scripts]\n", 1))
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("compose: %v\n%s", err, out)
	}
	rep := readReport(t, root)
	want := map[string]any{"acme": map[string]any{"team": "web"}, "sweep_floor": float64(40), "corpus_roots": []any{"scripts"}}
	if !reflect.DeepEqual(rep.Extensions, want) {
		t.Errorf("extensions = %v, want %v", rep.Extensions, want)
	}
}

// TestExtendsTakesTheTargetFromTheDocument is where the target lives under a base: the
// document sets it beside extends, since the base carries none; the machine's qory.yaml
// stands over the document's target and the flags over both; and the runtime and model
// keys of the checkout's own qory.yaml are still not read under extends.
func TestExtendsTakesTheTargetFromTheDocument(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	compose := filepath.Join(root, "qory.yaml")
	composed := func(name, want string, args ...string) {
		t.Helper()
		out, err := run(t, append([]string{"harness", "compose"}, args...)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		wants(t, out, ui.Mark+" acme/app · "+want)
	}
	composed("the document's target", "claude opus")
	if rep := readReport(t, root); !slices.Equal(rep.Target.Runtimes, []string{"claude"}) || rep.Target.Model != "opus" {
		t.Errorf("report target = %+v", rep.Target)
	}
	composed("--runtime over the document", "codex opus", "--runtime", "codex")
	composed("--model over the document", "claude sonnet", "--model", "sonnet")
	writeFile(t, compose, consumerCompose(url, "  runtime: codex\n  model: sonnet\n"))
	composed("the checkout's own keys, not read", "claude opus")
	machineConfig(t, "harness:\n  runtime: codex\n  model: sonnet\n")
	composed("the machine's file over the document", "codex sonnet")
	composed("the flags over the machine's file", "claude opus", "--runtime", "claude", "--model", "opus")
}

// TestExtendsNeedsARuntimeFromSomewhere is a document that extends a base and names no
// target, on a machine whose configuration names no runtime: the base carries none
// either, so the compose is an input error saying where a runtime comes from, and
// --runtime supplies one.
func TestExtendsNeedsARuntimeFromSomewhere(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	writeFile(t, filepath.Join(root, "qory.yaml"), strings.Replace(consumerCompose(url), consumerTarget, "", 1))
	out, err := run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, err.Error(), "target.runtime is required; the base stack nextjs-15@", " defines no target, so the document sets one beside extends, or the configuration or --runtime does")
	gone(t, root, ".qory", ".claude")
	out, err = run(t, "harness", "compose", "--runtime", "claude")
	if err != nil {
		t.Fatalf("with --runtime: %v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude")
	if rep := readReport(t, root); !slices.Equal(rep.Target.Runtimes, []string{"claude"}) || rep.Target.Model != "" {
		t.Errorf("report target = %+v", rep.Target)
	}
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

// TestExtendsReadsTheOwnFileAndNamesWhatItLeavesOut is the checkout's qory.yaml under
// extends: the compose says what it read from the file, its document and its worktree
// keys, and names the machine keys the file sets that it left out. A file that sets
// none gets no ignored row.
func TestExtendsReadsTheOwnFileAndNamesWhatItLeavesOut(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url, "  model: sonnet\n")+"worktree:\n  base: main\n  dir: ../wt\ngit:\n  timeout: 2m\nenv:\n  REGION: eu\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "app")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "read", "qory.yaml  (target claude opus, 1 module, 1 extension, worktree.base main, worktree.dir)")
	wantsRow(t, out, "ignored", "qory.yaml: harness.model, git, env  (under extends, only ~/.config/qory/qory.yaml and the files above the checkout set these)")
	wantsNoRow(t, out, "skipped")

	writeFile(t, filepath.Join(root, "qory.yaml"), consumerCompose(url))
	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "read", "qory.yaml  (target claude opus, 1 module, 1 extension)")
	wantsNoRow(t, out, "ignored")
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
	wantsRow(t, out, "read", "qory.yaml  (target claude opus, 1 module, 1 extension)")
	wantsRow(t, out, "ignored", "qory.yaml: harness.runtime  (under extends, only ~/.config/qory/qory.yaml and the files above the checkout set these)")
	out, err = run(t, "harness", "compose", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "read", "qory.yaml  (target claude opus, 1 module, 1 extension)")
	wantsRow(t, out, "ignored", "qory.yaml: harness.runtime  (under extends, only ~/.config/qory/qory.yaml and the files above the checkout set these)")
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
	if err == nil || !strings.Contains(err.Error(), "module core belongs to the base stack; a checkout's qory.yaml cannot list a base module, exclude from it or replace it") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a base module named: err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestExtendsFollowsTheBasesBranch is a base at main after a push: the next compose takes
// the new commit for the base and its modules alike, without --update, and prints the
// move on the extends row alone.
func TestExtendsFollowsTheBasesBranch(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := readReport(t, root).Base.Pin
	dir := strings.TrimPrefix(url, "file://")
	writeFile(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core rules, second\n")
	runGit(t, dir, "commit", "-q", "-am", "second")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rep := readReport(t, root)
	if rep.Base.Pin == first {
		t.Fatalf("the base stayed on %s after a push", first)
	}
	for _, l := range rep.Modules[:2] {
		if l.Pin != rep.Base.Pin {
			t.Errorf("base module %s on %s, want the base's %s", l.Name, l.Pin, rep.Base.Pin)
		}
	}
	wantsRow(t, out, "extends", "nextjs-15  main "+first+" → "+rep.Base.Pin)
	wantsNoRow(t, out, "module")
	instructions, _ := os.ReadFile(filepath.Join(root, ".claude", "CLAUDE.md"))
	if !strings.HasPrefix(string(instructions), "# Core rules, second\n") {
		t.Errorf("instructions after the push:\n%s", instructions)
	}
}

// TestExtendsUpdatesABaseModuleFromAnotherRepository is a base listing a module from a
// second repository at tag v1: the tag moves, a compose stays on the old commit, and
// compose --update fetches the module again and lands on the new one.
func TestExtendsUpdatesABaseModuleFromAnotherRepository(t *testing.T) {
	other := tempDir(t)
	runGit(t, other, "init", "-q", "-b", "main")
	runGit(t, other, "config", "user.name", "Publisher")
	runGit(t, other, "config", "user.email", "op@example.com")
	writeManifest(t, other, "extra")
	writeFile(t, filepath.Join(other, "AGENTS.md"), "# Extra v1\n")
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-q", "-m", "first")
	runGit(t, other, "tag", "v1")
	url := baseRepoWith(t, strings.Replace(baseStack, "  - name: tools\n", "  - name: extra\n    source: {git: file://"+other+", ref: v1}\n  - name: tools\n", 1))
	root := consumerCheckout(t, url)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pinOf := func() string {
		for _, l := range readReport(t, root).Modules {
			if l.Name == "extra" {
				return l.Pin
			}
		}
		t.Fatal("the report lists no module extra")
		return ""
	}
	first := pinOf()
	writeFile(t, filepath.Join(other, "AGENTS.md"), "# Extra v2\n")
	runGit(t, other, "commit", "-q", "-am", "second")
	runGit(t, other, "tag", "-f", "v1")
	if out, err := run(t, "harness", "compose"); err != nil || pinOf() != first {
		t.Fatalf("a compose after the tag moved: pin %s (%v), want %s\n%s", pinOf(), err, first, out)
	}
	out, err := run(t, "harness", "compose", "--update")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if pinOf() == first {
		t.Errorf("compose --update left the base's module from another repository on %s", first)
	}
}
