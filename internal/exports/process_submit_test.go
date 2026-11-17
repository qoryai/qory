package exports_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/exports"
)

// moduleWith is a module folder whose process.yaml is body.
func moduleWith(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exports.ProcessFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestProcessSubmitReadsTheTypeAndSendOfAWellFormedSubmit is a process.yaml whose submit
// has type reply-draft and send end_of_run, or send after_gate: ProcessSubmit returns
// both values and true.
func TestProcessSubmitReadsTheTypeAndSendOfAWellFormedSubmit(t *testing.T) {
	for _, send := range []string{"end_of_run", "after_gate"} {
		typ, got, ok := exports.ProcessSubmit(moduleWith(t, "summary: a desk\nsubmit:\n  type: reply-draft\n  send: "+send+"\n"))
		if !ok || typ != "reply-draft" || got != send {
			t.Errorf("ProcessSubmit is %q, %q, %v, want reply-draft, %s, true", typ, got, ok, send)
		}
	}
}

// TestProcessSubmitFindsNoSubmitInAFileThatBreaksARule is a module folder with no
// process.yaml, and a process.yaml with an alias, a type that is no artifact type name,
// a type that is a number, a send that is neither end_of_run nor after_gate, a submit
// that is null, or no submit: ProcessSubmit returns false for each.
func TestProcessSubmitFindsNoSubmitInAFileThatBreaksARule(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"an alias", "x: &a [1]\ny: *a\nsubmit:\n  type: reply-draft\n  send: end_of_run\n"},
		{"a type that is no artifact type name", "submit:\n  type: Reply Draft\n  send: end_of_run\n"},
		{"a type that is a number", "submit:\n  type: 5\n  send: end_of_run\n"},
		{"a send that is not allowed", "submit:\n  type: reply-draft\n  send: sometimes\n"},
		{"a null submit", "submit: null\n"},
		{"no submit", "summary: a desk\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if typ, send, ok := exports.ProcessSubmit(moduleWith(t, c.body)); ok {
				t.Errorf("ProcessSubmit is %q, %q, true, want false", typ, send)
			}
		})
	}
	t.Run("no file", func(t *testing.T) {
		if typ, send, ok := exports.ProcessSubmit(t.TempDir()); ok {
			t.Errorf("ProcessSubmit is %q, %q, true, want false", typ, send)
		}
	})
}

// TestProcessSubmitReadsNoProcessFileThatLinksOutOfTheModule is a process.yaml that is a
// link to a file outside the module folder, whose submit is well formed: ProcessSubmit
// returns false and reads nothing outside the folder.
func TestProcessSubmitReadsNoProcessFileThatLinksOutOfTheModule(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("submit:\n  type: reply-draft\n  send: end_of_run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, exports.ProcessFileName)); err != nil {
		t.Skipf("no link: %v", err)
	}
	if typ, send, ok := exports.ProcessSubmit(dir); ok {
		t.Errorf("ProcessSubmit is %q, %q, true, want false", typ, send)
	}
}
