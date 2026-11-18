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

// The reasons Forager no longer ends a run with and keeps reserved, so a record written
// before still reads: a run credential that could not be checked or had expired, and a
// run that went quiet.
const (
	oldReasonCredentialUnreachable = "credential_check_unreachable"
	oldReasonCredentialInvalid     = "credential_check_invalid"
	oldReasonCredentialExpired     = "credential_expired"
	oldReasonQuiet                 = "quiet"
)

// The reasons of Forager's contract for a model key the model provider rejected with a
// 401: model_key_switch, with paused, when another key of the run is usable, and
// model_key_unauthorized, with failed, when no key is left.
const (
	reasonModelKeySwitch       = event.ReasonModelKeySwitch
	reasonModelKeyUnauthorized = event.ReasonModelKeyUnauthorized
)

// outcome is how qory says a run ended, by the state and the reason Forager gives: the
// outcome, as qory says it after "the run", the reason in qory's words, empty when there
// is none to say, and what can happen next, empty when there is nothing to say.
type outcome struct {
	// lost is the outcome of a run whose session went silent or whose end was never
	// recorded, failed with session_lost or gateway_lost, as Qory Apiary shows it.
	completed, failed, cancelled, lost bool
	// paused is the outcome of a run in the state paused: every model key of the run
	// reached its limit, the reason model_key_limit, or the model provider rejected a
	// model key and another is usable, model_key_switch.
	paused bool
	// keysRejected is a run failed because the model provider rejected every model key,
	// model_key_unauthorized. Forager stopped the runtime, so the runtime's exit does not
	// say the outcome.
	keysRejected bool
	// noOutcome is a run whose starter ended it and gave no outcome, stopped.
	noOutcome bool
	// known is false when neither the state nor the reason says how the run ended.
	known  bool
	reason string
	// next is what can happen to the run next, said after the reason, such as "the run
	// can continue on another key".
	next string
}

// outcomeOf is how the run ended, by state and reason. Forager's own reasons have the
// outcome they always end a run with, and are said in qory's words; the time limit's is
// said with its limit by the caller, so here it has no words, nor has a run stopped from
// where it was started, interrupted, or a quiet one. A run in the state paused stopped
// after the model provider rejected a model key, with model_key_switch, and otherwise at
// the model key limit. Any other reason is the run's starter's, said as it gave it with
// spaces for underscores, under the state.
func outcomeOf(state, reason string) outcome {
	o := outcome{known: true}
	if state == event.StatePaused {
		o.paused, o.reason = true, "every model key reached its limit"
		if reason == reasonModelKeySwitch {
			o.reason, o.next = "the model provider rejected a model key", "the run can continue on another key"
		}
		return o
	}
	switch reason {
	case reasonModelKeyUnauthorized:
		o.failed, o.keysRejected, o.reason = true, true, "the model provider rejected every model key"
	case event.ReasonSessionLost:
		o.lost, o.reason = true, fmt.Sprintf("it lost contact with the gateway for %s", runQuiet)
	case event.ReasonGatewayLost:
		o.lost, o.reason = true, "its end was never recorded"
	case event.ReasonBatchRefused:
		o.failed, o.reason = true, "its events could not be recorded"
	case event.ReasonRunClosed:
		o.failed, o.reason = true, "the gateway stopped during the run"
	case oldReasonCredentialUnreachable, oldReasonCredentialInvalid, oldReasonUnreachable, oldReasonInvalid:
		o.failed, o.reason = true, "its run credential could not be checked"
	case oldReasonCredentialExpired:
		o.cancelled, o.reason = true, "the run credential expired"
	case event.ReasonStopped, oldReasonStopped:
		o.cancelled, o.noOutcome = true, true
	case event.ReasonTimeout, oldReasonQuiet, event.ReasonInterrupted:
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

// word is the outcome as qory says it in a resend's line: completed, failed, cancelled,
// paused or lost.
func (o outcome) word() string {
	switch {
	case o.completed:
		return "completed"
	case o.paused:
		return "paused"
	case o.cancelled:
		return "cancelled"
	case o.lost:
		return "lost"
	}
	return "failed"
}

// phrase is the outcome after "the run": completed, failed, was cancelled, was paused,
// was lost.
func (o outcome) phrase() string {
	switch {
	case o.completed:
		return "completed"
	case o.paused:
		return "was paused"
	case o.cancelled:
		return "was cancelled"
	case o.lost:
		return "was lost"
	}
	return "failed"
}

// said is the run's end as qory says it: "the run <outcome>: <reason>", ": <reason>"
// left out when there is none to say, and ", with no outcome given" for a run its
// starter ended without one. A next step follows the reason after ", and ".
func (o outcome) said() string {
	s := "the run " + o.phrase()
	switch {
	case o.noOutcome:
		s += ", with no outcome given"
	case o.reason != "":
		s += ": " + o.because()
	}
	return s
}

// because is the reason, and the next step after ", and " when there is one.
func (o outcome) because() string {
	if o.next == "" {
		return o.reason
	}
	return o.reason + ", and " + o.next
}

// stopped is the run's end as qory says it when the gateway closed the run and the
// runtime was stopped: [outcome.said] with ", and <runtime> was stopped" after it, or, for
// an outcome with a next step, before that step: "<reason>, <runtime> was stopped, and
// <next>".
func (o outcome) stopped(runtime string) string {
	if o.next == "" || o.noOutcome {
		return o.said() + ", and " + runtime + " was stopped"
	}
	return "the run " + o.phrase() + ": " + o.reason + ", " + runtime + " was stopped, and " + o.next
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
// does: completed for 0, failed for any other exit or a signal. A run failed because the
// model provider rejected every model key agrees with no exit: Forager stopped the
// runtime.
func (o outcome) agrees(exitedZero bool) bool {
	if o.keysRejected {
		return false
	}
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
	return o.word() + ": " + o.because()
}
