package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
)

// TestRunIDUsedSaysTheIDAndWhatToDo words the gateway's run_id_used for the person, with
// the run's id, and unwraps to the refusal; any other error is not its.
func TestRunIDUsedSaysTheIDAndWhatToDo(t *testing.T) {
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	ref := &session.Refusal{Code: refusal.RunIDUsed, Status: 409, From: accesskey.FromGateway, Text: "the gateway refused the run: run_id_used"}
	err := runIDUsed(ref, id)
	want := "the run id " + id + " is already used by another run; leave out --run-id, or give a new one"
	if err == nil || err.Error() != want {
		t.Fatalf("run_id_used: %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "the server closed the run") {
		t.Errorf("run_id_used says the server closed the run: %v", err)
	}
	var got *session.Refusal
	if !errors.As(err, &got) || got != ref {
		t.Errorf("run_id_used does not unwrap to the refusal: %v", err)
	}
	for _, other := range []error{
		&session.Refusal{Code: accesskey.CodeRunClosed, Status: 410, From: accesskey.FromGateway},
		errors.New("the gateway refused the run: run_id_used"),
	} {
		if got := runIDUsed(other, id); got != nil {
			t.Errorf("%v: %v, want none", other, got)
		}
	}
}
