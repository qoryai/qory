package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// TestBuildWritesTheRoleNotes is a harness with a note in a module of the base stack and
// one in a module of the checkout: role-notes.json is the file of the fixture, one row per
// note in the order of the entries, with layer 1 for the base and 0 for the checkout.
func TestBuildWritesTheRoleNotes(t *testing.T) {
	hermetic(t)
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/notes-two-layers/qory.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.Compose(file)
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), ".qory", "harness")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, render.RoleNotesFile))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../contracts/harness/v1/fixtures/notes-two-layers/expected/role-notes.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("role-notes.json:\n%s\nwant:\n%s", got, want)
	}
}

// TestBuildWritesAnEmptyListWithoutANote is a harness with no note: role-notes.json is
// there, with an empty list.
func TestBuildWritesAnEmptyListWithoutANote(t *testing.T) {
	res, _, home := composeFixtureNamed(t, "provides-fills-role", "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, render.RoleNotesFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"notes\": []\n}\n"; string(got) != want {
		t.Errorf("role-notes.json = %q, want %q", got, want)
	}
}
