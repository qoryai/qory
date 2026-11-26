package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

func TestReadSessionSpec(t *testing.T) {
	base := session.SessionSpec{Args: []string{"claude", "--model", "m"}, WorkspaceReadOnly: false}
	got, err := readSessionSpec(base, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WorkspaceReadOnly {
		t.Error("the workspace is not read-only")
	}
	i := slices.Index(got.Args, "--tools")
	if i < 0 || i+1 >= len(got.Args) {
		t.Fatalf("args %q have no --tools list", got.Args)
	}
	tools := strings.Split(got.Args[i+1], ",")
	want := []string{"Read", "Grep", "Glob", "Skill", "Write", "Agent", "Bash"}
	if !slices.Equal(tools, want) {
		t.Errorf("tools %q, want %q: Bash and no edit tool", tools, want)
	}
	if !slices.Equal(got.Args[:3], base.Args) {
		t.Errorf("args %q do not start with the given ones", got.Args)
	}
	if len(base.Args) != 3 || base.WorkspaceReadOnly {
		t.Errorf("the given spec changed: %+v", base)
	}
}

func TestReadSessionSpecRefusesNestedDockerAndOtherRuntimes(t *testing.T) {
	_, err := readSessionSpec(session.SessionSpec{}, "claude", true)
	var re *reasonError
	if !errors.As(err, &re) || re.code != "review_needs_no_nested_docker" ||
		err.Error() != "The run's image runs an inner Docker daemon, so the review did not start\n  A read-only review session cannot run beside it; the code is used" {
		t.Errorf("nested Docker returned %v", err)
	}
	if _, err := readSessionSpec(session.SessionSpec{}, "codex", false); flatStop(err) != "qory has no argument that limits a review session's tools on codex, so the review did not start; the code is used" {
		t.Errorf("codex returned %v", err)
	}
}

// guardOnly is a runtime with a review guard and no settings flag, so a run cannot apply
// the guard's settings to a session.
type guardOnly struct{ render.Runtime }

// guardOnly must stay a guard, or the test below passes for another reason.
var _ render.CommandGuard = guardOnly{}

func (guardOnly) CheckAgent(render.AgentCheck) error { return nil }
func (guardOnly) CheckEntry(render.EntryCheck) error { return nil }
func (guardOnly) WithoutCommands(data []byte, _, _ string) ([]byte, error) {
	return data, nil
}
func (guardOnly) ReviewArgs() []string   { return []string{"--tools", "Read"} }
func (guardOnly) ProjectPaths() []string { return []string{".fake"} }
func (guardOnly) ReviewSettings(map[string]any, string, render.ReviewPlaces) ([]byte, error) {
	return []byte("{}"), nil
}

// TestReadSessionSpecStopsAGuardThatCannotApplyItsSettings: a guard that is no
// [render.SettingsInliner] stops the session, as a runtime with no guard does, and Claude
// Code passes.
func TestReadSessionSpecStopsAGuardThatCannotApplyItsSettings(t *testing.T) {
	_, err := readSessionSpecOf(session.SessionSpec{}, "fake", guardOnly{}, false)
	if want := "qory has no argument that limits a review session's tools on fake, so the review did not start; the code is used"; flatStop(err) != want {
		t.Errorf("a guard with no settings flag returned %v, want %q", err, want)
	}
	if _, ok := render.ReviewGuard(guardOnly{}); ok {
		t.Error("ReviewGuard accepts a guard with no settings flag")
	}
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readSessionSpecOf(session.SessionSpec{}, "claude", rt, false); err != nil {
		t.Errorf("claude returned %v", err)
	}
}

// TestReviewJoinSpecLeavesTheRunsSpecWritable is the spec of a review join of a run with a
// writable mount of the checkout: the join binds an empty working folder read-only as its
// workspace, the fresh copy read-only in place of the checkout, and the reports folder
// writable, and the run's spec keeps its writable mount of the checkout, its variables
// and its arguments for the session after. A setup inside the agent's container, an image
// with nested Docker and a mount inside the checkout stop the join.
func TestReviewJoinSpecLeavesTheRunsSpecWritable(t *testing.T) {
	spec := runSpec{}
	spec.Wall = &wall.Docker{}
	spec.Args = []string{"claude", "-p"}
	spec.Dir = "/checkout"
	spec.Mounts = []wall.Mount{{Path: "/checkout"}, {Path: "/cache", ReadOnly: true}}
	spec.Written = []string{"/checkout"}
	spec.Variables.Run = []string{"A=1", "B=2"}
	ws := reviewWorkspace{work: "/in/review-work-2", commit: "/in/review-copy-2/commit", reports: wall.Mount{Path: "/in/review-reports-2"}}
	got, err := reviewJoinSpec(spec, "/checkout", "review-changes", "claude", session.Image{}, ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts := []wall.Mount{{Path: "/cache", ReadOnly: true}, {Path: ws.work, ReadOnly: true}, {Path: ws.commit, ReadOnly: true}, ws.reports}
	if !got.WorkspaceReadOnly || got.Dir != ws.work || !slices.Equal(got.Mounts, wantMounts) || !slices.Contains(got.Args, "--tools") {
		t.Errorf("the join is %+v, want the empty working folder as workspace, the mounts %v and a tools list", got, wantMounts)
	}
	if !slices.Equal(got.Variables.Run, []string{"A=1", "B=2"}) || !slices.Equal(spec.Variables.Run, []string{"A=1", "B=2"}) {
		t.Errorf("the join's variables are %q and the run's %q, want the run's for both", got.Variables.Run, spec.Variables.Run)
	}
	if !slices.Contains(got.Written, "/checkout") || !slices.Contains(got.Written, ws.reports.Path) || !slices.Contains(got.Written, ws.work) {
		t.Errorf("the join's written places are %q, want the checkout, the working folder and the reports folder", got.Written)
	}
	if spec.WorkspaceReadOnly || spec.Dir != "/checkout" || spec.Mounts[0] != (wall.Mount{Path: "/checkout"}) || len(spec.Args) != 2 || len(spec.Written) != 1 {
		t.Errorf("the run's spec changed: %+v", spec.SessionSpec)
	}
	inside := spec
	inside.setupWhere = config.SetupInside
	if _, err := reviewJoinSpec(inside, "/checkout", "review-changes", "claude", session.Image{}, ws); err == nil || !strings.Contains(err.Error(), "wall.setup is inside") {
		t.Errorf("a setup inside returned %v", err)
	}
	if _, err := reviewJoinSpec(spec, "/checkout", "review-changes", "claude", session.Image{Docker: true}, ws); err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Errorf("nested Docker returned %v", err)
	}
	within := spec
	for _, path := range []string{"/checkout/sub", "/"} {
		within.Mounts = append(slices.Clone(spec.Mounts), wall.Mount{Path: path, ReadOnly: true})
		if _, err := reviewJoinSpec(within, "/checkout", "review-changes", "claude", session.Image{}, ws); err == nil || !strings.HasPrefix(err.Error(), "A mount contains the checkout or lies inside it, so the review did not start\n  The mount ") {
			t.Errorf("a mount at %s returned %v", path, err)
		}
	}
	if _, err := reviewJoinSpec(spec, "/checkout", "review-changes", "claude", session.Image{}, reviewWorkspace{work: ws.work, commit: "copy", reports: ws.reports}); err == nil || !strings.HasPrefix(err.Error(), "The review has no copy of the commit or no writable reports folder, so it did not start\n  The copy is ") {
		t.Errorf("a relative copy returned %v", err)
	}
	if _, err := reviewJoinSpec(spec, "/checkout", "review-changes", "claude", session.Image{}, reviewWorkspace{work: "work", commit: ws.commit, reports: ws.reports}); err == nil || !strings.HasPrefix(err.Error(), "The review's working folder is not the empty folder qory made, so the review did not start\n  The working folder is ") {
		t.Errorf("a relative working folder returned %v", err)
	}
}
