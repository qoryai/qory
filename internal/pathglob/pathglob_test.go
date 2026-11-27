package pathglob_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pathglob"
)

// TestMatchFixture runs every case of contracts/harness/v1/path-cases.json.
func TestMatchFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "harness", "v1", "path-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Pattern string `json:"pattern"`
			File    string `json:"file"`
			Match   bool   `json:"match"`
			Why     string `json:"why"`
		} `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 30 {
		t.Fatalf("the fixture has %d cases, want at least 30", len(fixture.Cases))
	}
	for _, c := range fixture.Cases {
		if got := pathglob.Match(c.Pattern, c.File); got != c.Match {
			t.Errorf("Match(%q, %q) = %v, want %v: %s", c.Pattern, c.File, got, c.Match, c.Why)
		}
	}
}

// TestMatchPathological checks that a pattern which makes a backtracking matcher take
// seconds finishes at once, on a match and on a mismatch.
func TestMatchPathological(t *testing.T) {
	start := time.Now()
	long := strings.Repeat("a", 5000)
	if pathglob.Match(strings.Repeat("*a", 40)+"*b", long) {
		t.Error("a pattern that needs a b matched a path of a only")
	}
	if !pathglob.Match(strings.Repeat("*a", 40)+"*b", long+"b") {
		t.Error("a pattern that ends in b did not match a path that ends in b")
	}
	deep := strings.Repeat("d/", 2000)
	if pathglob.Match(strings.Repeat("**/d/", 40)+"x", deep+"y") {
		t.Error("a pattern that needs x matched a path that ends in y")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the matches took %v, want under 2s", elapsed)
	}
}
