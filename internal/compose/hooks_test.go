package compose_test

import (
	"fmt"
	"os"
	"os/exec"
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

// gitIn runs git with args in dir, with a test identity for a commit.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=Tester", "-c", "user.email=tester@example.com"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// realTemp is a new temporary folder by its path with every link resolved, as git
// prints the root of a checkout in it.
func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// commitAll makes dir a git checkout whose commit at HEAD has every file in dir.
func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "first")
}

// TestHookProgramsStayInTheCheckout is a qory.yaml in a folder above the checkout, as a
// file covering several repositories is: each program committed in the checkout is in
// hooks.json by its path in git from the checkout's root. Refused are a program in
// another repository under the folder; a file that is untracked or ignored; a committed
// link and a file in a committed linked folder; a folder and a submodule; the checkout
// itself; and a checkout that is no git checkout or has no commit.
func TestHookProgramsStayInTheCheckout(t *testing.T) {
	above := realTemp(t)
	copyHooksFixture(t, above)
	checkout := filepath.Join(above, "app")
	write := func(file, text string) {
		t.Helper()
		path := filepath.Join(above, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"app/scripts/check.sh", "app/bin/lint.sh", "other/scripts/check.sh", "outside.sh"} {
		write(file, "#!/bin/sh\n")
	}
	write("app/.gitignore", "*.local.sh\n")
	for link, target := range map[string]string{
		"scripts/inside.sh": "check.sh",
		"scripts/abs.sh":    filepath.Join(checkout, "scripts", "check.sh"),
		"scripts/linked.sh": "../../outside.sh",
		"tools":             "scripts",
	} {
		if err := os.Symlink(target, filepath.Join(checkout, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	commitAll(t, checkout)
	commit := gitIn(t, checkout, "rev-parse", "HEAD")
	gitIn(t, checkout, "update-index", "--add", "--cacheinfo", "160000,"+commit+",vendor/lib")
	gitIn(t, checkout, "commit", "-q", "-m", "submodule")
	write("app/scripts/new.sh", "#!/bin/sh\n")
	write("app/scripts/mine.local.sh", "#!/bin/sh\n")

	doc := filepath.Join(above, "qory.yaml")
	hooks := func(hooks string) {
		t.Helper()
		if err := os.WriteFile(doc, []byte(hooksDoc("stack", hooks)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hooks("    - {at: after-review-round, kind: check, run: app/scripts/check.sh --all}\n" +
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
		t.Errorf("runs: %s, want each program by its path in git from the checkout's root", got)
	}

	commitFix := "Commit scripts/%s: a hook runs the file as committed in git.\nhooks[0].run, app/scripts/%[1]s, has no file in the commit at HEAD of the checkout."
	for _, tc := range []struct{ run, want string }{
		{"other/scripts/check.sh", "Move the program into the checkout, or fix hooks[0].run.\nother/scripts/check.sh, from the directory of " + doc + ", is outside the checkout "},
		{"app/scripts/new.sh", fmt.Sprintf(commitFix, "new.sh")},
		{"app/scripts/mine.local.sh", fmt.Sprintf(commitFix, "mine.local.sh")},
		{"app/scripts/inside.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/inside.sh, is a link."},
		{"app/scripts/abs.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/abs.sh, is a link."},
		{"app/scripts/linked.sh", "Point run at the file itself, not a link.\nhooks[0].run, app/scripts/linked.sh, is a link."},
		{"app/tools/check.sh", "Point run at the file itself, not a link.\nhooks[0].run passes through the link app/tools."},
		{"app/scripts", "Point run at a file.\nhooks[0].run, app/scripts, is a folder in git."},
		{"app/vendor/lib", "Point run at a file.\nhooks[0].run, app/vendor/lib, is a submodule in git."},
		{"app", "Set hooks[0].run to a program file in the checkout.\nhooks[0].run, app, is the checkout itself."},
	} {
		hooks("    - {at: after-review-round, kind: check, run: " + tc.run + "}\n")
		_, err := composeHooks(t, doc, checkout)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s:\nerr = %v\nwant %s", tc.run, err, tc.want)
		}
	}
}

// TestHookProgramsNeedACommit is a checkout that is no git checkout, and one with no
// commit yet: a hook's program fails the compose in both.
func TestHookProgramsNeedACommit(t *testing.T) {
	for _, tc := range []struct {
		name, cause string
		git         []string
	}{
		{"no git checkout", "\nThe folder ", nil},
		{"no commit", "\nThe checkout ", []string{"init", "-q", "-b", "main"}},
	} {
		dir := realTemp(t)
		copyHooksFixture(t, dir)
		if tc.git != nil {
			gitIn(t, dir, tc.git...)
		}
		doc := filepath.Join(dir, "qory.yaml")
		if err := os.WriteFile(doc, []byte(hooksDoc("stack", "    - {at: after-review-round, kind: check, run: scripts/check-licences.sh}\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := composeHooks(t, doc, dir)
		want := "Run qory in a git checkout: a hook runs the file as committed in git." + tc.cause
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, want)
		}
	}
}

// TestHookTimeoutAtTheLimit is a hook whose timeout is the base's: it composes, with
// that timeout in seconds.
func TestHookTimeoutAtTheLimit(t *testing.T) {
	dir := realTemp(t)
	copyHooksFixture(t, dir)
	commitAll(t, dir)
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
