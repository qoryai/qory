package cmd_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/ui"
)

// policyBase is [baseStack] saying what it is written for under extending.target: claude,
// with opus or sonnet.
var policyBase = strings.Replace(baseStack, "  settings: [permissions.allow]\n", "  settings: [permissions.allow]\n  target:\n    runtime: [claude]\n    model: [opus, sonnet]\n", 1)

// TestExtendingTargetHoldsTheCheckoutsTarget is a base written for claude with opus or
// sonnet, said under extending.target: a checkout composing it for another runtime,
// another model, or no model at all is an input error naming the base and what it is
// written for, whether the document names the base under extends or -f does, and a dry
// run is refused the same way and writes nothing. The flags are held to the lists too,
// and a target inside them composes on both paths.
func TestExtendingTargetHoldsTheCheckoutsTarget(t *testing.T) {
	url := baseRepoWith(t, policyBase)
	file := filepath.Join(strings.TrimPrefix(url, "file://"), "nextjs-15", "qory-stack.yaml")
	root := consumerCheckout(t, url)
	compose := filepath.Join(root, "qory.yaml")
	document := func(target string) {
		writeFile(t, compose, strings.Replace(consumerCompose(url), consumerTarget, "  target: "+target+"\n", 1))
	}
	refuse := func(name, want, rest string, flags ...string) {
		t.Helper()
		for _, path := range [][]string{nil, {"-f", file}, {"--dry-run"}} {
			args := slices.Concat([]string{"harness", "compose"}, flags, path)
			out, err := run(t, args...)
			if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%s, %s: err = %v, exit %d\n%s", name, strings.Join(args, " "), err, cmd.ExitCode(err), out)
				continue
			}
			wants(t, err.Error(), want+"nextjs-15@", rest)
			gone(t, root, ".qory", ".claude", ".codex")
		}
	}
	document("{runtime: codex, model: opus}")
	refuse("a runtime outside", "runtime codex is not one the base stack ", " is written for; runtimes: claude")
	document("{runtime: claude, model: haiku}")
	refuse("a model outside", "model haiku is not one the base stack ", " is written for; models: opus, sonnet")
	document("{runtime: claude}")
	refuse("no model", "the target sets no model, and the base stack ", " is written for one of these; models: opus, sonnet")
	document("{runtime: claude, model: sonnet}")
	refuse("--runtime outside", "runtime codex is not one the base stack ", " is written for; runtimes: claude", "--runtime", "codex")
	refuse("--model outside", "model haiku is not one the base stack ", " is written for; models: opus, sonnet", "--model", "haiku")
	for _, args := range [][]string{{"harness", "compose"}, {"harness", "compose", "-f", file}} {
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		wants(t, out, ui.Mark+" acme/app · claude sonnet")
	}
}

// TestAStackFileCarriesNoTarget is a qory-stack.yaml that sets a target: it is refused as
// it is read, naming the file and where the target goes, whether -f names it or a
// document extends it, and nothing is written.
func TestAStackFileCarriesNoTarget(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "qory-stack.yaml")
	writeFile(t, file, strings.Replace(baseStack, "name: nextjs-15\n", "name: nextjs-15\ntarget: {runtime: claude}\n", 1))
	writeManifest(t, filepath.Join(dir, "modules", "core"), "core")
	writeManifest(t, filepath.Join(dir, "modules", "tools"), "tools")
	root := newCheckout(t)
	want := "qory-stack.yaml: target is not a stack's; a stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends"
	out, err := run(t, "harness", "compose", "-f", file, "--runtime", "claude")
	if err == nil || !strings.Contains(err.Error(), file+": target is not a stack's") || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("-f: err = %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: "+dir+"}\n  target: {runtime: claude}\n  modules:\n    - name: app\n")
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	out, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("extends: err = %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	gone(t, root, ".qory", ".claude")
}
