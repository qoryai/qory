package cmd

import (
	"fmt"
	"strings"

	"github.com/qoryai/forager/event"
)

// The reasons Forager wrote before the run's starter could give an outcome, which a
// record written then may still hold: they read as the codes that took their place.
const (
	oldReasonStopped     = "run_ended_at_issuer"
	oldReasonUnreachable = "issuer_unreachable"
	oldReasonInvalid     = "issuer_answer_invalid"
)

// outcome is how qory says a run ended, by the state and the reason Forager gives: the
// outcome, as qory says it after "the run", and the reason in qory's words, empty when
// there is none to say.
type outcome struct {
	// lost is the outcome of a run whose session went silent or whose end was never
	// recorded, failed with session_lost or gateway_lost, as Qory Apiary shows it.
	completed, failed, cancelled, lost bool
	// noOutcome is a run whose starter ended it and gave no outcome, stopped.
	noOutcome bool
	// known is false when neither the state nor the reason says how the run ended.
	known  bool
	reason string
}

// outcomeOf is how the run ended, by state and reason. Forager's own reasons have the
// outcome they always end a run with, and are said in qory's words; the time limit's is
// said with its limit by the caller, so here it has no words. Any other reason is the
// run's starter's, said as it gave it with spaces for underscores, under the state.
func outcomeOf(state, reason string) outcome {
	o := outcome{known: true}
	switch reason {
	case event.ReasonSessionLost:
		o.lost, o.reason = true, fmt.Sprintf("it lost contact with the gateway for %s", runQuiet)
	case event.ReasonGatewayLost:
		o.lost, o.reason = true, "its end was never recorded"
	case event.ReasonBatchRefused:
		o.failed, o.reason = true, "its events could not be recorded"
	case event.ReasonRunClosed:
		o.failed, o.reason = true, "the gateway stopped during the run"
	case event.ReasonCredentialCheckUnreachable, event.ReasonCredentialCheckInvalid, oldReasonUnreachable, oldReasonInvalid:
		o.failed, o.reason = true, "its run credential could not be checked"
	case event.ReasonCredentialExpired:
		o.cancelled, o.reason = true, "the run credential expired"
	case event.ReasonStopped, oldReasonStopped:
		o.cancelled, o.noOutcome = true, true
	case event.ReasonTimeout, event.ReasonQuiet:
		o.cancelled = true
	default:
		switch state {
		case event.StateSucceeded:
			o.completed = true
		case event.StateFailed:
			o.failed = true
		case event.StateCancelled:
			o.cancelled = true
		default:
			o.known = false
		}
		o.reason = strings.ReplaceAll(reason, "_", " ")
	}
	return o
}

// word is the outcome as qory says it in a resend's line: completed, failed, cancelled
// or lost.
func (o outcome) word() string {
	switch {
	case o.completed:
		return "completed"
	case o.cancelled:
		return "cancelled"
	case o.lost:
		return "lost"
	}
	return "failed"
}

// phrase is the outcome after "the run": completed, failed, was cancelled, was lost.
func (o outcome) phrase() string {
	switch {
	case o.completed:
		return "completed"
	case o.cancelled:
		return "was cancelled"
	case o.lost:
		return "was lost"
	}
	return "failed"
}

// said is the run's end as qory says it: "the run <outcome>: <reason>", ": <reason>"
// left out when there is none to say, and ", with no outcome given" for a run its
// starter ended without one.
func (o outcome) said() string {
	s := "the run " + o.phrase()
	switch {
	case o.noOutcome:
		s += ", with no outcome given"
	case o.reason != "":
		s += ": " + o.reason
	}
	return s
}

// exitCode is qory's exit status for a run with this outcome: 0 when it completed, 1
// otherwise.
func (o outcome) exitCode() int {
	if o.completed {
		return 0
	}
	return 1
}

// agrees says whether the run's own exit, its runtime's status, says what the outcome
// does: completed for 0, failed for any other exit or a signal.
func (o outcome) agrees(exitedZero bool) bool {
	if exitedZero {
		return o.completed
	}
	return o.failed
}

// endsAs is the end a resend recorded for a record that had none, as qory run resend says
// it: the outcome's word, and its reason after a colon when it has one. An end with no
// known outcome is the gateway's, gateway_lost.
func endsAs(state, reason string) string {
	o := outcomeOf(state, reason)
	if !o.known {
		o = outcomeOf("", event.ReasonGatewayLost)
	}
	if o.reason == "" {
		return o.word()
	}
	return o.word() + ": " + o.reason
}
