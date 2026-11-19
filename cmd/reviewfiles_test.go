package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestReviewFilesIsHidden is the MCP server that a reviewer's Codex starts: in the tree,
// hidden from the help, and taking one folder.
func TestReviewFilesIsHidden(t *testing.T) {
	if c, _, _ := cmd.Root().Find([]string{"review-files"}); c == nil || c.Name() != "review-files" || !c.Hidden {
		t.Fatal("qory review-files is not a hidden verb")
	}
	out, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "review-files")
	for _, args := range [][]string{{"review-files"}, {"review-files", "a", "b"}} {
		if _, err := run(t, args...); err == nil || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v, want an input error", args, err)
		}
	}
}

// TestReviewFilesServesTheCopy runs the server on a folder: standard output has the
// responses alone, one JSON line each.
func TestReviewFilesServesTheCopy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"README.md"}}}
`
	out, err := runIn(t, in, "review-files", dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"name":"qory_review"`) ||
		!strings.Contains(lines[1], `"text":"1\tone\n"`) {
		t.Errorf("the output is %q", out)
	}
}
