package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscover finds the profile in an ancestor directory when the checkout has none.
func TestDiscover(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(checkout); err == nil {
		t.Fatal("found a profile where there is none")
	}
	want := filepath.Join(base, "code", FileName)
	if err := os.WriteFile(want, []byte("apiVersion: qory.ai/v1alpha1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
