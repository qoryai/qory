package cmd

import "fmt"

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
	// textLaterCodexHome has the folder of the Codex home that stayed, the number of the
	// session before and of the next session, the removal's error, and [codeUsed].
	textLaterCodexHome = "remove %s by hand, then take the work item in again: qory could not remove the Codex home of session %d before session %d: %s%s"
	// textNoStopProof and textContainerKept have what qory did not do, such as "qory pushed
	// nothing", and [codeUsed].
	textNoStopProof   = "run the stage behind a wall: without one, a process of the session could still write .git/config, so %s%s"
	textContainerKept = "take the work item in again in Qory Apiary: a container of the session may still run and write the working folder, so %s%s"
)

// laterHarnessDir is the folder of the run directory that a later session's harness document
// goes in, with the session's number.
const laterHarnessDir = "harness-%d"

// agentStopProof is the error of a push between two sessions of a run, or of the steps
// before a later session, when no proof shows that the session's processes stopped, as
// [preparation.Push] requires: a process left could write .git/config or the working folder,
// and the push, the next compose and every later session would read them. skipped is what
// qory did not do, for the line. walled
// is whether the run has a wall, whose join removes the agent's container; kept is
// [openedRun.ContainerKept], whether a container of the run may still run. A run with no
// wall has no proof. qory run stops a write run from a run code with no wall before its join,
// so a read run with no wall reaches that branch, before its session 2.
func agentStopProof(walled, kept bool, skipped string) error {
	switch {
	case !walled:
		return &runLineError{fmt.Errorf(textNoStopProof, skipped, codeUsed)}
	case kept:
		return &runLineError{fmt.Errorf(textContainerKept, skipped, codeUsed)}
	}
	return nil
}
