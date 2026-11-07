package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/compose"
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
