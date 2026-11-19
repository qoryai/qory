package cmd

import "errors"

// The lines of the loop over the sessions of a run whose documents set shared_run. Each
// line that ends a run starts with its fix, as qory run prints it after "qory run: ".
const (
	textLaterStarts = "session %d starts the workflow %s on %s"
	textLaterEnd    = "Qory Apiary starts no session after session %d, so the run ends"
	// textLaterAccess has the later session's workflow, its access, session 1's access and
	// [codeUsed].
	textLaterAccess = "set one access for every workflow of the stage in Qory Apiary: the workflow %s has %s, and session 1 has %s%s"
	// textLaterCheckoutHarness is the line of a later session of a run whose harness is the
	// checkout's file, which the branch may have changed since session 1's compose.
	textLaterCheckoutHarness = "set the workspace's harness in Qory Apiary: a later session composes no harness file of the branch"
	// textSessionNumber has the gateway's number of the session, the number qory served on
	// the session's route, and [codeUsed].
	textSessionNumber = "update qory or Forager: the gateway numbered the session %d, and qory served session %d on its route%s"
	textNoStopProof   = "run the stage behind a wall: without one, a process of the session could still write .git/config, so qory pushed nothing" + codeUsed
	textContainerKept = "take the work item in again in Qory Apiary: a container of the session may still run and write the working folder, so qory pushed nothing" + codeUsed
)

// laterHarnessDir is the folder of the run directory that a later session's harness document
// goes in, with the session's number.
const laterHarnessDir = "harness-%d"

// agentStopProof is the error of a push between two sessions of a run when no proof shows
// that the session's processes stopped, as [preparation.Push] requires: a process left could
// write .git/config, and the push and every later session would run its git settings. walled
// is whether the run has a wall, whose join removes the agent's container; kept is
// [openedRun.ContainerKept], whether a container of the run may still run. A run with no
// wall has no proof. qory run stops a write run from a run code with no wall before its join,
// so only a test reaches that branch.
func agentStopProof(walled, kept bool) error {
	switch {
	case !walled:
		return &runLineError{errors.New(textNoStopProof)}
	case kept:
		return &runLineError{errors.New(textContainerKept)}
	}
	return nil
}
