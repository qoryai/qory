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
// file covering several repositories is: each regular file in the checkout is in
// hooks.json by its path from the checkout's root. Refused are a program in another
// repository under the folder; a link that leads out of the checkout, to a file or to
// nothing; a link inside the checkout, relative, absolute or to nothing; a file in a
// linked folder; a path whose names differ in case from the names on disk; the checkout
// itself; a folder; and a missing file.
func TestHookProgramsStayInTheCheckout(t *testing.T) {
	above := t.TempDir()
	copyHooksFixture(t, above)
	checkout := filepath.Join(above, "app")
	for _, file := range []string{"app/scripts/check.sh", "app/bin/lint.sh", "other/scripts/check.sh", "outside.sh"} {
		path := filepath.Join(above, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"scripts/linked.sh":   "../../outside.sh",
		"scripts/gone.sh":     "../../gone.sh",
		"scripts/inside.sh":   "check.sh",
		"scripts/abs.sh":      filepath.Join(checkout, "scripts", "check.sh"),
		"scripts/dangling.sh": "nothing.sh",
		"up":                  "..",
		"tools":               "scripts",
	} {
		if err := os.Symlink(target, filepath.Join(checkout, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	doc := filepath.Join(above, "qory.yaml")
	write := func(hooks string) {
		t.Helper()
		if err := os.WriteFile(doc, []byte(hooksDoc("stack", hooks)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("    - {at: after-review-round, kind: check, run: app/scripts/check.sh --all}\n" +
		"    - {at: after-review-round, kind: check, run: ./app/bin/../bin/lint.sh}\n")
	res, err := composeHooks(t, doc, checkout)
	if err != nil {
		t.Fatal(err)
	}
	var runs []string
	for _, h := range res.Hooks {
		runs = append(runs, strings.Join(h.Run, " "))
	}
	if got := strings.Join(runs, "; "); got != "scripts/check.sh --all; bin/lint.sh" {
		t.Errorf("runs: %s, want each program from the checkout's root", got)
	}

	// A name the file system keeps decomposed (NFD), such as macOS does, matches the
	// composed name (NFC) that git stores, and hooks.json has the name in NFC.
	if err := os.WriteFile(filepath.Join(checkout, "scripts", "ve\u0301rifier.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("    - {at: after-review-round, kind: check, run: app/scripts/v\u00e9rifier.sh, name: verifier}\n")
	res, err = composeHooks(t, doc, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Hooks[0].Run[0]; got != "scripts/v\u00e9rifier.sh" {
		t.Errorf("run: %q, want the name in NFC", got)
	}

	// The checkout's folder written in another letter case is refused as a case
	// mismatch where the file system ignores case, and is outside the checkout where it
	// does not.
	caseWant := "Move the program into the checkout, or fix hooks[0].run.\nAPP/scripts/check.sh, from the directory of "
	if a, err := os.Stat(filepath.Join(above, "APP")); err == nil {
		if b, err := os.Stat(checkout); err == nil && os.SameFile(a, b) {
			caseWant = "Write hooks[0].run as the file is named.\nhooks[0].run is APP/scripts/check.sh, and the file on disk is app/scripts/check.sh."
		}
	}

	for _, tc := range []struct{ run, want string }{
		{"other/scripts/check.sh", "Move the program into the checkout, or fix hooks[0].run.\nother/scripts/check.sh, from the directory of " + doc + ", is outside the checkout "},
		{"app/scripts/linked.sh", "Set hooks[0].run to a program inside the checkout.\nA link on the way to scripts/linked.sh leads out of the checkout."},
		{"app/scripts/gone.sh", "Set hooks[0].run to a program inside the checkout.\nA link on the way to scripts/gone.sh leads out of the checkout."},
		{"app/up/outside.sh", "Set hooks[0].run to a program inside the checkout.\nA link on the way to up/outside.sh leads out of the checkout."},
		{"app/scripts/inside.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/inside.sh, is a link."},
		{"app/scripts/abs.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/abs.sh, is a link."},
		{"app/scripts/dangling.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/dangling.sh, is a link."},
		{"app/tools/check.sh", "Point run at the file itself, not a link.\nhooks[0].run passes through the link app/tools."},
		{"app/Scripts/check.sh", "Write hooks[0].run as the file is named.\nhooks[0].run is app/Scripts/check.sh, and the file on disk is app/scripts/check.sh."},
		{"APP/scripts/check.sh", caseWant},
		{"app/scripts/Check.sh", "Write hooks[0].run as the file is named.\nhooks[0].run is app/scripts/Check.sh, and the file on disk is app/scripts/check.sh."},
		{"app", "Set hooks[0].run to a program file in the checkout.\nhooks[0].run, app, is the checkout itself."},
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
