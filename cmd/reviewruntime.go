package cmd

import (
	"fmt"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/render/claude"
	"github.com/qoryai/qory/internal/runapi"
)

// reviewerRuntimes are the runtimes that can start a named reviewer as the session's main
// agent with its tools removed, so a run on one of them can have a review round. Neither
// a render.Runtime nor Forager's descriptor of a runtime has a field for that, so the list
// follows what qory builds a launch for: Claude Code, see [claudeReviewArgs].
var reviewerRuntimes = []string{claude.Runtime}

// reviewerRuntimeEscalation is the point of the escalate verdict for a run on runtime that
// has a stage with reviewers, nil when runtime is one of [reviewerRuntimes]. A run on
// another runtime has no review round, and qory sends the verdict before the first join.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func reviewerRuntimeEscalation(runtime string) *runapi.Point {
	for _, name := range reviewerRuntimes {
		if name == runtime {
			return nil
		}
	}
	shown := accesskey.ShownNames(reviewerRuntimes)
	return &runapi.Point{
		Text: fmt.Sprintf("The review round did not run: qory starts a reviewer as the main agent with its tools removed only on %s, and this run uses %s.",
			listOf(shown), accesskey.ShownName(runtime, false)),
		Action: runapi.ActionRunAgain,
		Fix:    "Run the workflow again on " + strings.Join(shown, " or ") + ".",
	}
}
