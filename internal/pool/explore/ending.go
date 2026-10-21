package explore

import (
	"syscall"

	"github.com/qoryai/qory/internal/pool/child"
)

// The states of a run's outcome, as the Forager contract's run outcome names them.
const (
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// The reasons of a run's outcome: codes, as the run outcome takes them.
const (
	// ReasonTimeLimit is a run qory run stopped at its --timeout. Forager reserves
	// timeout, so the code is another.
	ReasonTimeLimit = "time_limit"
	// ReasonPoolStopped is a run the pool program stopped, as it stopped.
	ReasonPoolStopped = "pool_stopped"
)

// ReasonNotStarted is the release reason of a run that could not be started on this
// machine: it made no record.
const ReasonNotStarted = "the run could not be started on this machine"

// RetryNotStarted is the release's retry for a run that made no record, after its
// handover. It is false, so the run ends: a release with retry true after the handover
// queues a new run, so an engine or a gateway that is down would loop runs. Retry true
// is for a dispatch released before its handover alone.
const RetryNotStarted = false

// exitTimeout is qory run's exit status at its --timeout.
const exitTimeout = 124

// Ending is what the pool program does once a run's qory run ended: it posts an outcome,
// or it releases the dispatch. A run that made a record posts its outcome and is not
// released; one that made none is released, and posts nothing.
type Ending struct {
	// Post reports whether the run's outcome is posted, with State and Reason; Reason
	// is empty for none.
	Post          bool
	State, Reason string
	// Release reports whether the dispatch is released, with Retry and
	// ReleaseReason.
	Release       bool
	Retry         bool
	ReleaseReason string
}

// EndingOf is the ending of a run by its qory run's end:
//
//	record  qory run                                posted / released
//	yes     exit 0                                  succeeded
//	yes     exit 124, its --timeout                 failed, time_limit
//	yes     exit 1 after the program's SIGTERM      cancelled, pool_stopped
//	yes     any other exit, or a signal             failed, no reason
//	no      anything                                released, retry RetryNotStarted, ReasonNotStarted
//
// A run that started and failed is posted failed, and is not released.
func EndingOf(record bool, r child.Result) Ending {
	if !record {
		return Ending{Release: true, Retry: RetryNotStarted, ReleaseReason: ReasonNotStarted}
	}
	switch {
	case r.Signal != syscall.Signal(0):
		return Ending{Post: true, State: StateFailed}
	case r.ExitCode == 0:
		return Ending{Post: true, State: StateSucceeded}
	case r.ExitCode == exitTimeout:
		return Ending{Post: true, State: StateFailed, Reason: ReasonTimeLimit}
	case r.ExitCode == 1 && r.Stopped:
		return Ending{Post: true, State: StateCancelled, Reason: ReasonPoolStopped}
	}
	return Ending{Post: true, State: StateFailed}
}
