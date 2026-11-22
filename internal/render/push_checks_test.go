package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/render"
)

// TestBuildWritesThePushChecks is a harness whose modules declare push checks: the home
// has push-checks.json with each check, sorted by type, with the types it reads, and each
// program at its absolute path in the home, a file the home reaches. A harness without push checks has no file.
func TestBuildWritesThePushChecks(t *testing.T) {
	res, _, home := composeFixtureNamed(t, "push-checks-declared", "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, render.PushChecksFile))
	if err != nil {
		t.Fatal(err)
	}
	var got render.PushChecks
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []compose.PushCheck{
		{Type: "mockup", Module: "web", Command: []string{home + "/modules/web/bin/check", "--strict"}},
		{Type: "review-comments", Module: "web", Command: []string{home + "/modules/web/bin/check"}},
		{Type: "spec", Module: "core", Command: []string{home + "/modules/core/checks/check.js", "push", "spec"}, Reads: []string{"acceptance-criteria", "questions"}},
	}
	if !reflect.DeepEqual(got.PushChecks, want) {
		t.Errorf("push checks:\n%s\nwant %v", data, want)
	}
	for _, c := range got.PushChecks {
		if info, err := os.Stat(c.Command[0]); err != nil || !info.Mode().IsRegular() {
			t.Errorf("the home has no file at %s: %v", c.Command[0], err)
		}
	}

	res, _, home = composeFixture(t, "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, render.PushChecksFile)); !os.IsNotExist(err) {
		t.Errorf("a harness without push checks has %s: %v", render.PushChecksFile, err)
	}
}

// TestBuildWritesTheScopeChecks is a harness whose module declares the scope check of a
// plan type: the home has scope-checks.json with the check by type, its program at its
// absolute path in the home, which pushcheck.LoadScope reads back. A harness without scope
// checks has no file, and LoadScope reads none.
func TestBuildWritesTheScopeChecks(t *testing.T) {
	res, _, home := composeFixtureNamed(t, "workflow-defined-plan", "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	got, err := pushcheck.LoadScope(home)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]compose.ScopeCheck{"change-plan": {Module: "core", Command: []string{home + "/modules/core/checks/check.js", "scope", "--plan"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scope checks %+v, want %+v", got, want)
	}
	if info, err := os.Stat(got["change-plan"].Command[0]); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the home has no file at %s: %v", got["change-plan"].Command[0], err)
	}

	res, _, home = composeFixture(t, "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, render.ScopeChecksFile)); !os.IsNotExist(err) {
		t.Errorf("a harness without scope checks has %s: %v", render.ScopeChecksFile, err)
	}
	if got, err := pushcheck.LoadScope(home); got != nil || err != nil {
		t.Errorf("LoadScope of a home with none: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(home, render.ScopeChecksFile), []byte(`{"scope_checks":{"plan":{"command":["check.js"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pushcheck.LoadScope(home); err == nil {
		t.Error("LoadScope took a program with no absolute path")
	}
}
