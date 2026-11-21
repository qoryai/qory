package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/reviewround"
)

func TestOpenFindingsReplacesTheOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-findings.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenFindings(path, reviewround.OpenFindings{Round: 2, Reviewers: []reviewround.ReviewerFindings{}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"round": 2`) {
		t.Errorf("file = %s", data)
	}
	if _, err := reviewround.ReadOpenFindings(data); err != nil {
		t.Errorf("file does not read back: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != openFindingsMode {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(openFindingsMode))
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("folder has %d files, want only open-findings.json", len(entries))
	}
	if err := writeOpenFindings(filepath.Join(path, "x", "y"), reviewround.OpenFindings{Round: 1}); err == nil || !strings.HasPrefix(err.Error(), "Give qory a folder") {
		t.Errorf("a missing folder returned %v", err)
	}
}
