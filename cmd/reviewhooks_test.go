package cmd_test

import (
	"os"
	"slices"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/hookrun"
)

// reviewHook is a required check hook at the end of the workflow review-changes, which
// runs scripts/check.sh of the repository.
var reviewHook = compose.Hook{
	Workflow: "review-changes", At: "after-review-changes", PointAt: hookrun.PointEnd, Kind: "check", Name: "check.sh",
	Run: []string{"scripts/check.sh"}, Required: true, TimeoutSeconds: 60, Index: 0,
}

// reviewHookFiles are the files of the repository with the program of [reviewHook].
var reviewHookFiles = map[string]string{"scripts/check.sh": "#!/bin/sh\n"}

// reviewHookRuns makes the runs of the test run [reviewHook] at the end of each review
// session, and returns the text of the program of each run of it, in order.
func reviewHookRuns(t *testing.T) *[]string {
	t.Helper()
	var programs []string
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{reviewHook}, nil }))
	t.Cleanup(cmd.SetHookMounts(func(mounts []wall.Mount) int {
		data, err := os.ReadFile(mounts[len(mounts)-1].Path)
		if err != nil {
			t.Errorf("the hook's program cannot be read: %v", err)
		}
		programs = append(programs, string(data))
		return 0
	}))
	return &programs
}

// TestAReviewSessionsEndHooksRunOnlyAfterItSucceeded is a write run of a write session, a
// review session with a hook at its end, and a write session. After a review session that
// succeeded, its hook runs once. After one whose agent failed, no hook at its end runs:
// the review session pushes nothing, so its hooks get no push to run for. Session 3
// succeeds, so the run closes succeeded in both.
func TestAReviewSessionsEndHooksRunOnlyAfterItSucceeded(t *testing.T) {
	for name, c := range map[string]struct{ failed, ran int }{
		"succeeded": {0, 1},
		"failed":    {2, 0},
	} {
		t.Run(name, func(t *testing.T) {
			programs := reviewHookRuns(t)
			r, joins, _ := reviewRunWith(t, reviewCfg{files: reviewHookFiles, failed: c.failed}, answerFor(2, "review-changes"), answerFor(3, "hello"))
			if len(*programs) != c.ran {
				t.Errorf("the hook ran %d times, want %d\n%s", len(*programs), c.ran, r.out)
			}
			if len(joins) < 2 || !joins[1].spec.WorkspaceReadOnly {
				t.Fatalf("%d joins, want a review join second\n%s", len(joins), r.out)
			}
			r.wantsClose(t, event.StateSucceeded, 0)
		})
	}
}

// TestAReviewSessionsEndHookRunsTheProgramOfTheRunsStart is a write run whose session 1
// changes the program of the hook at the end of the review session after it: the hook
// runs the program of the run's start commit, not the one that session 1 committed.
func TestAReviewSessionsEndHookRunsTheProgramOfTheRunsStart(t *testing.T) {
	programs := reviewHookRuns(t)
	cfg := reviewCfg{files: reviewHookFiles, extra: func(dir string, n int) {
		if n == 1 {
			writeFile(t, dir+"/scripts/check.sh", "#!/bin/sh\necho changed by session 1\n")
		}
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"), answerFor(3, "hello"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if want := []string{"#!/bin/sh\n"}; !slices.Equal(*programs, want) {
		t.Errorf("the hook ran the programs %q, want %q\n%s", *programs, want, r.out)
	}
}
