package layer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestParseDocument reads frontmatter and body, and a file without frontmatter as body only.
func TestParseDocument(t *testing.T) {
	d, err := ParseDocument([]byte("---\nname: reviewer\ndescription: Reviews.\n---\n\n# Body\n"), "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.String("name") != "reviewer" || d.String("description") != "Reviews." || d.Body != "# Body\n" {
		t.Fatalf("got %+v", d)
	}
	d, err = ParseDocument([]byte("plain\n"), "b.md")
	if err != nil || d.Front != nil || d.Body != "plain\n" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if _, err := ParseDocument([]byte("---\nname: x\n"), "c.md"); err == nil {
		t.Fatal("unclosed frontmatter accepted")
	}
}

// TestParseDocumentEdgeCases covers a closing fence at the end of the file, an empty
// frontmatter block, a key that is not a string, and frontmatter that is not a mapping.
func TestParseDocumentEdgeCases(t *testing.T) {
	d, err := ParseDocument([]byte("---\nname: reviewer\ncount: 3\n---"), "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.String("name") != "reviewer" || d.Body != "" {
		t.Fatalf("got %+v", d)
	}
	if d.String("count") != "" {
		t.Fatalf("count is %q, want the empty string for a key that is not a string", d.String("count"))
	}
	if d.String("missing") != "" {
		t.Fatalf("missing is %q", d.String("missing"))
	}
	// An empty frontmatter block reads as an unclosed one: the parser looks for the closing
	// fence after a newline, and the two fences share none.
	if _, err := ParseDocument([]byte("---\n---\nbody\n"), "b.md"); err == nil {
		t.Fatal("an empty frontmatter block was accepted")
	}
	d, err = ParseDocument([]byte("---\n\n---\nbody\n"), "c.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.Front == nil || len(d.Front) != 0 || d.Body != "body\n" {
		t.Fatalf("got %+v", d)
	}
	if _, err := ParseDocument([]byte("---\n- one\n- two\n---\n"), "d.md"); err == nil {
		t.Fatal("a frontmatter list was accepted as a mapping")
	}
	if _, err := ParseDocument([]byte("---\nname: [\n---\n"), "e.md"); err == nil {
		t.Fatal("broken frontmatter yaml was accepted")
	}
}

// TestReadDocument reads a file from disk and reports a file that is not there.
func TestReadDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.md")
	if err := os.WriteFile(path, []byte("---\nname: reviewer\n---\n\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := ReadDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.String("name") != "reviewer" || d.Body != "Review.\n" {
		t.Fatalf("got %+v", d)
	}
	if _, err := ReadDocument(filepath.Join(dir, "gone.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %v, want one matching os.ErrNotExist", err)
	}
}
