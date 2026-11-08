package provisioner_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestAMachineEndIsTheFixture releases a dispatch, then sends gone at the time of the
// contract's gone fixture: the body sent is the fixture, to
// /v1/dispatches/<dispatch_id>/machine-end, and gets the 204. A second gone gets the 204
// too, and the fake recorded both, in order.
func TestAMachineEndIsTheFixture(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	cl := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Release(ctx, cl.DispatchID, "a reason"); err != nil {
		t.Fatal(err)
	}
	var f contracts.MachineEnd
	if err := json.Unmarshal(fixture(t, "machine-end/gone.json"), &f); err != nil {
		t.Fatal(err)
	}
	tm, err := time.Parse(time.RFC3339, f.Time)
	if err != nil {
		t.Fatal(err)
	}
	clk.set(tm)
	if err := c.MachineEnd(ctx, cl.DispatchID, contracts.MachineEndGone); err != nil {
		t.Fatal(err)
	}
	if got := last(t, srv); got.Route != provisionertest.RouteMachineEnd || got.Target != "/v1/dispatches/"+cl.DispatchID+"/machine-end" ||
		!sameJSON(t, got.Body, fixture(t, "machine-end/gone.json")) {
		t.Errorf("sent %s to %s\nwant\n%s", got.Body, got.Target, fixture(t, "machine-end/gone.json"))
	}
	if err := c.MachineEnd(ctx, cl.DispatchID, contracts.MachineEndGone); err != nil {
		t.Errorf("a second gone: %v", err)
	}
	if d, _ := srv.Dispatch(cl.DispatchID); !slices.Equal(d.MachineEnds, []string{"gone", "gone"}) {
		t.Errorf("the fake recorded %q", d.MachineEnds)
	}
}

// TestWhichMachineEndErrorsEndTheAttempts sends gone into each answer: a signed 400, 403
// or 404 ends the attempts; no answer, an unsigned 401, an answer over its bound, a
// signed 429 and a signed 503 unavailable on every try do not. The 503 is
// ErrUnavailable, with the request and the code in its text. An error that is no
// refusal does not end them either.
func TestWhichMachineEndErrorsEndTheAttempts(t *testing.T) {
	five := func(r provisionertest.Reply) []provisionertest.Reply {
		return slices.Repeat([]provisionertest.Reply{r}, 5)
	}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		ends    bool
	}{
		{"a signed 400 invalid_request", []provisionertest.Reply{{Status: 400, Code: "invalid_request"}}, true},
		{"a signed 400 unsupported_contract_version", []provisionertest.Reply{{Status: 400, Code: "unsupported_contract_version"}}, true},
		{"a signed 403", []provisionertest.Reply{{Status: 403, Code: "forbidden"}}, true},
		{"a signed 404", []provisionertest.Reply{{Status: 404, Code: "not_found"}}, true},
		{"no answer", five(provisionertest.Reply{Status: 204, Drop: true}), false},
		{"an unsigned 401", five(provisionertest.Reply{Status: 401, Code: "unauthorized", Unsigned: true}), false},
		{"an unsigned 403", five(provisionertest.Reply{Status: 403, Code: "forbidden", Unsigned: true}), false},
		{"over its bound", five(provisionertest.Reply{Status: 403, Code: "forbidden", Pad: provisioner.MaxAnswer}), false},
		{"a signed 429", []provisionertest.Reply{{Status: 429, Code: "rate_limited", RetryAfter: "7"}}, false},
		{"a signed 503", five(provisionertest.Reply{Status: 503, Code: "unavailable"}), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, cl, _, _ := setup(t)
			d := claimed(t, srv, cl, provisionertest.Run{})
			srv.Script(provisionertest.RouteMachineEnd, c.replies...)
			err := cl.MachineEnd(context.Background(), d.DispatchID, contracts.MachineEndGone)
			if err == nil || provisioner.EndsAttempts(err) != c.ends {
				t.Errorf("%v: ends the attempts %v, want %v", err, !c.ends, c.ends)
			}
			if c.name == "a signed 503" {
				want := "the machine end of " + d.DispatchID + ": Qory Apiary returned 503 unavailable"
				if !errors.Is(err, provisioner.ErrUnavailable) || err.Error() != want {
					t.Errorf("%v, want %s", err, want)
				}
			}
		})
	}
	if provisioner.EndsAttempts(errors.New("400")) || provisioner.EndsAttempts(nil) {
		t.Error("an error that is no refusal ends the attempts")
	}
}

// TestTheModelTakesMachineEnds walks the fake's machine ends: an unknown dispatch is 403
// forbidden, which ends the attempts; a dispatch claimed, dispatched or ended takes any
// state, any number of times, recorded in order.
func TestTheModelTakesMachineEnds(t *testing.T) {
	srv, c, _, _ := setup(t)
	ctx := context.Background()
	err := c.MachineEnd(ctx, "dp_0000000000000000", contracts.MachineEndGone)
	if !errors.Is(err, provisioner.ErrForbidden) || !provisioner.EndsAttempts(err) {
		t.Errorf("an unknown dispatch: %v", err)
	}
	cl := claimed(t, srv, c, provisionertest.Run{})
	if err := c.MachineEnd(ctx, cl.DispatchID, contracts.MachineEndDeleting); err != nil {
		t.Errorf("deleting while claimed: %v", err)
	}
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	srv.End(cl.DispatchID)
	for _, state := range []string{contracts.MachineEndDeleteFailed, contracts.MachineEndGone, contracts.MachineEndGone} {
		if err := c.MachineEnd(ctx, cl.DispatchID, state); err != nil {
			t.Errorf("%s after the end: %v", state, err)
		}
	}
	if d, _ := srv.Dispatch(cl.DispatchID); !slices.Equal(d.MachineEnds, []string{"deleting", "delete_failed", "gone", "gone"}) {
		t.Errorf("the fake recorded %q", d.MachineEnds)
	}
}
