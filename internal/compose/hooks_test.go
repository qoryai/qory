package compose_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
)

// hooksDoc is the qory.yaml of the fixture workflow-hooks-declared with the hooks given,
// extending the stack at stack, relative to the file.
func hooksDoc(stack, hooks string) string {
	return "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: " + stack + "}\n  target:\n    runtime: claude\n  hooks:\n" + hooks
}

// copyHooksFixture copies the fixture workflow-hooks-declared to dir.
func copyHooksFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join(fixtures, "workflow-hooks-declared"))); err != nil {
		t.Fatal(err)
	}
}

// composeHooks composes the document at doc for the checkout at checkout.
func composeHooks(t *testing.T, doc, checkout string) (*compose.Result, error) {
	t.Helper()
	p, err := config.Compose(doc)
	if err != nil {
		return nil, err
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		return nil, err
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base, Checkout: checkout})
	if err == nil {
		t.Cleanup(res.Close)
	}
	return res, err
}

// TestHookProgramsStayInTheCheckout is a qory.yaml in a folder above the checkout, as a
// file covering several repositories is: a regular file in the checkout is in hooks.json
// by its path from the checkout's root. A program in another repository under the folder,
// one a link leads out of the checkout to, a link to a file inside the checkout, a file
// in a linked folder inside it, a folder and a missing file are refused.
func TestHookProgramsStayInTheCheckout(t *testing.T) {
	above := t.TempDir()
	copyHooksFixture(t, above)
	checkout := filepath.Join(above, "app")
	for _, file := range []string{"app/scripts/check.sh", "other/scripts/check.sh", "outside.sh"} {
		path := filepath.Join(above, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../outside.sh", filepath.Join(checkout, "scripts", "linked.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("check.sh", filepath.Join(checkout, "scripts", "inside.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(checkout, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("scripts", filepath.Join(checkout, "tools")); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(above, "qory.yaml")
	write := func(hooks string) {
		t.Helper()
		if err := os.WriteFile(doc, []byte(hooksDoc("stack", hooks)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("    - {at: after-review-round, kind: check, run: app/scripts/check.sh --all}\n")
	res, err := composeHooks(t, doc, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Hooks[0].Run, " "); got != "scripts/check.sh --all" {
		t.Errorf("run: %s, want the program from the checkout's root", got)
	}

	for _, tc := range []struct{ run, want string }{
		{"other/scripts/check.sh", "Move the program into the checkout, or fix hooks[0].run.\nother/scripts/check.sh, from the directory of " + doc + ", is outside the checkout "},
		{"app/scripts/linked.sh", "Set hooks[0].run to a program inside the checkout.\nA link on the way to scripts/linked.sh leads out of the checkout."},
		{"app/up/outside.sh", "Set hooks[0].run to a program inside the checkout.\nA link on the way to up/outside.sh leads out of the checkout."},
		{"app/scripts/inside.sh", "Point run at the file itself, not a link.\napp/scripts/inside.sh in " + doc + " is a link."},
		{"app/tools/check.sh", "Point run at the file itself, not a link.\napp/tools/check.sh in " + doc + " is a link."},
		{"app/scripts", "Add the program to the checkout, or fix hooks[0].run.\nscripts in the checkout is not a file."},
		{"app/scripts/missing.sh", "Add the program to the checkout, or fix hooks[0].run.\nNo file is at scripts/missing.sh in the checkout."},
	} {
		write("    - {at: after-review-round, kind: check, run: " + tc.run + "}\n")
		_, err := composeHooks(t, doc, checkout)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s:\nerr = %v\nwant %s", tc.run, err, tc.want)
		}
	}
}

// TestHookTimeoutAtTheLimit is a hook whose timeout is the base's: it composes, with
// that timeout in seconds.
func TestHookTimeoutAtTheLimit(t *testing.T) {
	dir := t.TempDir()
	copyHooksFixture(t, dir)
	doc := filepath.Join(dir, "qory.yaml")
	if err := os.WriteFile(doc, []byte(hooksDoc("stack", "    - {at: after-review-round, kind: check, run: scripts/check-licences.sh, timeout: 10m}\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := composeHooks(t, doc, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hooks) != 1 || res.Hooks[0].TimeoutSeconds != 600 {
		t.Errorf("hooks: %+v", res.Hooks)
	}
}

// TestHookRefusalsFitTheFixLine is every refusal of a workflow hook in the fixtures: a
// fix line of at most 100 characters, then a cause line.
func TestHookRefusalsFitTheFixLine(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtures, "workflow-hooks-*", "expected", "error.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no refusals of workflow hooks: %v", err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) != 2 || utf8.RuneCountInString(lines[0]) > 100 || lines[1] == "" {
			t.Errorf("%s: want a fix line of at most 100 characters and a cause line, got %q", file, lines)
		}
	}
}
