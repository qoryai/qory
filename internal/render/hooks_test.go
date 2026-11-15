package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// composeHooksFixture composes the fixture workflow-hooks-declared on the stack it
// extends, with the hooks of its qory.yaml, or with none when hooks is false.
func composeHooksFixture(t *testing.T, hooks bool) *compose.Result {
	t.Helper()
	hermetic(t)
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/workflow-hooks-declared/qory.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.Compose(file)
	if err != nil {
		t.Fatal(err)
	}
	if !hooks {
		p.Hooks = nil
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(res.Close)
	return res
}

// TestBuildWritesTheHooks is a checkout that lists workflow hooks at an end, a start and
// an inside point: the home has hooks.json with each hook, in the order of qory.yaml,
// with its workflow, point, place in the session, kind, name, what it runs, whether it
// is required and its timeout. A checkout without hooks has no file.
func TestBuildWritesTheHooks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := render.BuildAt(composeHooksFixture(t, true), home, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, render.HooksFile))
	if err != nil {
		t.Fatal(err)
	}
	var got render.Hooks
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []compose.Hook{
		{Workflow: "review-changes", At: "after-review-round", PointAt: "end", Kind: "check", Name: "check-licences.sh", Run: []string{"scripts/check-licences.sh"}, Required: true, TimeoutSeconds: 600, Index: 0},
		{Workflow: "review-changes", At: "after-review-round", PointAt: "end", Kind: "review", Name: "licence-reviewer", Agent: "licence-reviewer", Required: false, TimeoutSeconds: 600, Index: 1},
		{Workflow: "plan-the-build", At: "after-plan", PointAt: "end", Kind: "enrich", Name: "notes", Run: []string{"scripts/write-notes.sh", "--all"}, Required: true, TimeoutSeconds: 120, Index: 2},
		{Workflow: "verify", At: "before-matrix", PointAt: "start", Kind: "check", Name: "check-licences.sh", Run: []string{"scripts/check-licences.sh", "--matrix"}, Required: true, TimeoutSeconds: 600, Index: 3},
		{Workflow: "plan-the-build", At: "after-code-map", PointAt: "inside", Kind: "enrich", Name: "code-map-notes", Agent: "licence-reviewer", Required: true, TimeoutSeconds: 90, Index: 4},
	}
	if !reflect.DeepEqual(got.Hooks, want) {
		t.Errorf("hooks:\n%s\nwant %+v", data, want)
	}

	plain := filepath.Join(t.TempDir(), "home")
	if err := render.BuildAt(composeHooksFixture(t, false), plain, plain, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plain, render.HooksFile)); !os.IsNotExist(err) {
		t.Errorf("a checkout without hooks has %s: %v", render.HooksFile, err)
	}
}

// TestHooksLeaveTheWorkflowsAlone builds one home from the checkout with its hooks and
// one without them, at one address: the two trees differ only in hooks.json, so every
// composed workflow file is byte-equal and the hooks change no workflow of the base.
func TestHooksLeaveTheWorkflowsAlone(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	with, without := filepath.Join(dir, "with"), filepath.Join(dir, "without")
	if err := render.BuildAt(composeHooksFixture(t, true), with, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if err := render.BuildAt(composeHooksFixture(t, false), without, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	diff, err := render.Diff(without, with, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []render.Difference{{Path: render.HooksFile, What: "extra"}}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("the homes differ at %+v, want only %+v", diff, want)
	}
	for _, skill := range []string{"workflow-plan-the-build", "workflow-review-changes", "workflow-verify"} {
		a, errA := os.ReadFile(filepath.Join(with, "claude", "skills", skill, "SKILL.md"))
		b, errB := os.ReadFile(filepath.Join(without, "claude", "skills", skill, "SKILL.md"))
		if errA != nil || errB != nil || string(a) != string(b) {
			t.Errorf("%s: with hooks %q (%v), without %q (%v)", skill, a, errA, b, errB)
		}
	}
}
