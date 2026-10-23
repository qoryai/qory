package provisioner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// testTime is when the tests' clock starts: the time of the claim fixture.
var testTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// clock is the client's and the fake's clock: it reads testTime until a wait moves it
// on, and records the waits.
type clock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *clock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	return ctx.Err()
}

func (c *clock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}

// setup is a fake and its client, both on one clock at testTime, the client logging to
// the buffer returned.
func setup(t *testing.T) (*provisionertest.Server, *provisioner.Client, *clock, *bytes.Buffer) {
	t.Helper()
	clk := &clock{now: testTime}
	srv := provisionertest.New(t)
	srv.Now = clk.read
	c := srv.Client()
	c.Now, c.Sleep = clk.read, clk.sleep
	var log bytes.Buffer
	c.Log = slog.New(slog.NewJSONHandler(&log, nil))
	return srv, c, clk, &log
}

// claimed claims the run srv offers, which must be there, and clears its run code.
func claimed(t *testing.T, srv *provisionertest.Server, c *provisioner.Client, run provisionertest.Run) provisioner.Claimed {
	t.Helper()
	srv.Offer(run)
	cl, ok, err := c.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	cl.RunCode.Clear()
	return cl
}

// fixture reads one provisioner/v1 fixture.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(contracts.ProvisionerFS, "fixtures/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON reports whether a and b are the same JSON value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

// last is the last call srv got.
func last(t *testing.T, srv *provisionertest.Server) provisionertest.Call {
	t.Helper()
	calls := srv.Calls()
	if len(calls) == 0 {
		t.Fatal("no call")
	}
	return calls[len(calls)-1]
}

// TestARunGoesThroughEveryRequest claims a dispatch, says it started twice, and claims
// and releases another, each request signed with the pool's key, with the headers of
// the contract, a body its schema takes, and the clock's time, and each answer
// verified under the pin. The claim's run code is the one offered.
func TestARunGoesThroughEveryRequest(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	run := srv.Offer(provisionertest.Run{Machine: "small"})[0]
	cl, ok, err := c.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if cl.PoolID != provisionertest.PoolID || cl.Machine != "small" || cl.GatewayURL != provisionertest.GatewayURL ||
		cl.MaxLifetimeMinutes != provisionertest.MaxLifetimeMinutes || !cl.LeaseUntil.Equal(testTime.Add(provisionertest.Lease)) {
		t.Errorf("claimed %+v", cl)
	}
	if got := written(t, cl.RunCode); got != run.RunCode {
		t.Error("the run code is not the one offered")
	}
	cl.RunCode.Clear()
	if h := last(t, srv).Header; h.Get("Cache-Control") != "" {
		t.Errorf("the request has Cache-Control %q", h.Get("Cache-Control"))
	}
	for range 2 {
		if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := srv.Dispatch(cl.DispatchID); d.State != provisionertest.StateDispatched || d.Dispatched != 2 {
		t.Errorf("after dispatched: %+v", d)
	}
	other := claimed(t, srv, c, provisionertest.Run{Step: provisioner.StepPrebuild})
	if other.Machine != "" {
		t.Errorf("a run with no machine is claimed with %q", other.Machine)
	}
	if err := c.Release(ctx, other.DispatchID, "the pool program stopped before the job started"); err != nil {
		t.Fatal(err)
	}
	if d, _ := srv.Dispatch(other.DispatchID); d.State != provisionertest.StateEnded || d.Release == nil ||
		d.Release.Reason != "the pool program stopped before the job started" {
		t.Errorf("after the release: %+v", d)
	}

	for _, call := range srv.Calls() {
		h := call.Header
		switch {
		case !call.Verified || call.Replayed || !call.Signed:
			t.Errorf("%s: verified %v, replayed %v, answer signed %v", call.Route, call.Verified, call.Replayed, call.Signed)
		case h.Get(accesskey.HeaderAccessKeyID) != provisionertest.KeyID || h.Get(accesskey.HeaderInstanceID) != provisionertest.InstanceID:
			t.Errorf("%s: the key's headers %v", call.Route, h)
		case h.Get("X-Qory-Contract-Version") != "1" || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "qory-test":
			t.Errorf("%s: the headers %v", call.Route, h)
		case call.Time.Before(testTime) || call.Time.After(clk.read()) || call.Time.Nanosecond() != 0:
			t.Errorf("%s: the time %s", call.Route, call.Time)
		}
	}
	want := []provisionertest.Route{"claim", "dispatched", "dispatched", "claim", "release"}
	if got := srv.Routes(); !slices.Equal(got, want) {
		t.Errorf("routes %v, want %v", got, want)
	}
}

// written is what h writes.
func written(t *testing.T, h provisioner.Hidden) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := h.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestTheBodiesAreTheFixtures builds each request whose fixture the contract has at the
// fixture's time, and checks the body sent is the fixture: the claim waits 25 seconds
// and has no node pool, dispatched has no job, and the release has a reason and no
// retry.
func TestTheBodiesAreTheFixtures(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	at := func(s string) {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		clk.set(tm)
	}
	cl := claimed(t, srv, c, provisionertest.Run{})
	if b := fixture(t, "claim/without-pool-id.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("the claim\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	var df contracts.Dispatched
	if err := json.Unmarshal(fixture(t, "dispatched/without-external.json"), &df); err != nil {
		t.Fatal(err)
	}
	at(df.Time)
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	if b := fixture(t, "dispatched/without-external.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("dispatched\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	var rf contracts.Release
	if err := json.Unmarshal(fixture(t, "release/no-capacity.json"), &rf); err != nil {
		t.Fatal(err)
	}
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: http.StatusNoContent})
	at(rf.Time)
	if err := c.Release(ctx, cl.DispatchID, rf.Reason); err != nil {
		t.Fatal(err)
	}
	if b := fixture(t, "release/no-capacity.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("the release\n%s\nwant\n%s", last(t, srv).Body, b)
	}
}

// TestTheAnswersAreTheFixtures answers a claim with each of the contract's claim-answer
// fixtures, signed, and checks what the client read.
func TestTheAnswersAreTheFixtures(t *testing.T) {
	names, err := fs.Glob(contracts.ProvisionerFS, "fixtures/claim-answer/*.json")
	if err != nil || len(names) < 3 {
		t.Fatalf("the fixtures %v: %v", names, err)
	}
	for _, name := range names {
		name = strings.TrimPrefix(name, "fixtures/")
		t.Run(name, func(t *testing.T) {
			srv, c, _, _ := setup(t)
			body := fixture(t, name)
			srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: http.StatusOK, Body: string(body)})
			cl, ok, err := c.Claim(context.Background())
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			var ca contracts.ClaimAnswer
			_ = json.Unmarshal(body, &ca)
			if cl.DispatchID != ca.DispatchID || cl.PoolID != ca.PoolID || contracts.FormatTime(cl.LeaseUntil) != ca.LeaseUntil ||
				cl.Machine != ca.Machine || cl.GatewayURL != ca.GatewayURL || cl.MaxLifetimeMinutes != ca.MaxLifetimeMinutes ||
				written(t, cl.RunCode) != ca.RunCode {
				t.Errorf("claimed %+v, want %+v", cl, ca)
			}
		})
	}
}

// failing is each way a try fails, so it is built anew and sent again: no answer, an
// unsigned answer of each status, one signed under another key, one whose signature
// header is sent twice, an answer over its bound, a signed server error, and an
// unsigned 429, whose Retry-After is not waited.
var failing = []struct {
	name  string
	reply provisionertest.Reply
	is    error
}{
	{"no answer", provisionertest.Reply{Status: 204, Drop: true}, provisioner.ErrUnreachable},
	{"an unsigned 401", provisionertest.Reply{Status: 401, Code: "unauthorized", Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 413", provisionertest.Reply{Status: 413, Code: "payload_too_large", Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 400", provisionertest.Reply{Status: 400, Code: "bad_request", Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 503", provisionertest.Reply{Status: 503, Code: "unavailable", Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 410", provisionertest.Reply{Status: 410, Code: "gone", Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 204", provisionertest.Reply{Status: 204, Unsigned: true}, provisioner.ErrUnsigned},
	{"an unsigned 429", provisionertest.Reply{Status: 429, Code: "rate_limited", Unsigned: true, RetryAfter: "3000"}, provisioner.ErrUnsigned},
	{"another key", provisionertest.Reply{Status: 204, WrongKey: true}, provisioner.ErrUnsigned},
	// The signature and then an empty one, and two empty configuration headers: each
	// joined is what was signed, so only the count refuses them.
	{"the signature twice", provisionertest.Reply{Status: 204, Header: http.Header{accesskey.HeaderSignature: {""}}}, provisioner.ErrUnsigned},
	{"a configuration header twice", provisionertest.Reply{Status: 204, Header: http.Header{accesskey.HeaderConfiguration: {"", ""}}}, provisioner.ErrUnsigned},
	{"over its bound", provisionertest.Reply{Status: 409, Code: "gone", Pad: provisioner.MaxAnswer}, provisioner.ErrTooLarge},
	{"a signed 500", provisionertest.Reply{Status: 500}, provisioner.ErrUnsigned},
}

// TestAFailedTryIsBuiltAnewAndSentAgain answers every try of a release with each
// failure: the client sends five, each with a later time and a new signature after a
// wait doubling from a second, logs a line before each new one, and returns the
// failure. A failure then a signed 204 is the release done, with the refusal's Resent
// unset since there was none.
func TestAFailedTryIsBuiltAnewAndSentAgain(t *testing.T) {
	for _, f := range failing {
		t.Run(f.name, func(t *testing.T) {
			srv, c, clk, log := setup(t)
			cl := claimed(t, srv, c, provisionertest.Run{})
			for range 5 {
				srv.Script(provisionertest.RouteRelease, f.reply)
			}
			err := c.Release(context.Background(), cl.DispatchID, "the pool program stopped before the run started")
			if f.name == "a signed 500" {
				var r *provisioner.Refusal
				if !errors.As(err, &r) || !r.Signed || r.Status != 500 || !r.Resent {
					t.Errorf("err %v", err)
				}
			} else if !errors.Is(err, f.is) {
				t.Errorf("err %v, want %v", err, f.is)
			}
			calls := srv.Calls()[1:]
			if len(calls) != 5 {
				t.Fatalf("%d tries", len(calls))
			}
			for i := 1; i < len(calls); i++ {
				if !calls[i].Time.After(calls[i-1].Time) || calls[i].Header.Get(accesskey.HeaderSignature) == calls[i-1].Header.Get(accesskey.HeaderSignature) || calls[i].Replayed {
					t.Errorf("try %d is not built anew: %s after %s", i+1, calls[i].Time, calls[i-1].Time)
				}
			}
			if w := clk.slept(); !slices.Equal(w, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}) {
				t.Errorf("waits %v", w)
			}
			if n := strings.Count(log.String(), "Qory Apiary's response could not be used, so the request is sent again after a wait"); n != 4 {
				t.Errorf("%d lines logged:\n%s", n, log)
			}

			srv2, c2, _, _ := setup(t)
			cl2 := claimed(t, srv2, c2, provisionertest.Run{})
			srv2.Script(provisionertest.RouteRelease, f.reply)
			if err := c2.Release(context.Background(), cl2.DispatchID, "the pool program stopped before the run started"); err != nil {
				t.Errorf("a failure then a 204: %v", err)
			}
		})
	}
}

// TestASignedAnswerIsNotSentAgain answers a release with each signed refusal below 500:
// one try each, returned as it is.
func TestASignedAnswerIsNotSentAgain(t *testing.T) {
	srv, c, _, _ := setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	for _, rep := range []provisionertest.Reply{
		{Status: 403, Code: "forbidden"}, {Status: 400, Code: "invalid_request"}, {Status: 400, Code: "unsupported_contract_version"},
		{Status: 429, Code: "rate_limited"}, {Status: 413, Code: "payload_too_large"}, {Status: 409, Code: "machine_exists"},
	} {
		srv.Script(provisionertest.RouteRelease, rep)
		before := len(srv.Calls())
		var r *provisioner.Refusal
		if err := c.Release(context.Background(), cl.DispatchID, "a reason"); !errors.As(err, &r) || !r.Signed || r.Status != rep.Status || r.Code != rep.Code || r.Resent {
			t.Errorf("%d: %v", rep.Status, err)
		}
		if n := len(srv.Calls()) - before; n != 1 {
			t.Errorf("%d: %d tries", rep.Status, n)
		}
	}
}

// TestTheSameBytesAreNeverSentTwice sends one release twice within one second of the
// clock: the second waits for the next second, so its time and its bytes differ. A
// distinct request in the same second does not wait.
func TestTheSameBytesAreNeverSentTwice(t *testing.T) {
	srv, c, clk, _ := setup(t)
	clk.set(testTime.Add(300 * time.Millisecond))
	cl := claimed(t, srv, c, provisionertest.Run{})
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 204}, provisionertest.Reply{Status: 204}, provisionertest.Reply{Status: 204})
	ctx := context.Background()
	if err := c.Release(ctx, cl.DispatchID, "a reason"); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(ctx, cl.DispatchID, "another reason"); err != nil {
		t.Fatal(err)
	}
	if w := clk.slept(); len(w) != 0 {
		t.Errorf("a distinct request waited %v", w)
	}
	if err := c.Release(ctx, cl.DispatchID, "a reason"); err != nil {
		t.Fatal(err)
	}
	if w := clk.slept(); !slices.Equal(w, []time.Duration{700 * time.Millisecond}) {
		t.Errorf("waits %v", w)
	}
	calls := srv.Calls()
	if bytes.Equal(calls[1].Body, calls[3].Body) || !calls[3].Time.Equal(testTime.Add(time.Second)) {
		t.Errorf("the same bytes twice: %s %s", calls[1].Body, calls[3].Body)
	}
}

// TestRefusalsMatchTheirRequest answers each request with each signed refusal code:
// errors.Is matches the code's error only for a request whose schema gives it, and
// never an unsigned answer of the same status and body.
func TestRefusalsMatchTheirRequest(t *testing.T) {
	all := []provisionertest.Route{"claim", "dispatched", "release", "machine-log"}
	codes := []struct {
		err    error
		status int
		code   string
		on     []provisionertest.Route
	}{
		{provisioner.ErrForbidden, 403, "forbidden", all},
		{provisioner.ErrNotFound, 404, "not_found", all},
		{provisioner.ErrUnsupportedRevision, 400, "unsupported_contract_version", all},
		{provisioner.ErrGone, 410, "gone", []provisionertest.Route{"dispatched", "release"}},
		{provisioner.ErrLeaseExpired, 409, "lease_expired", []provisionertest.Route{"dispatched", "release"}},
		{provisioner.ErrMachineExists, 409, "machine_exists", []provisionertest.Route{"release"}},
		{provisioner.ErrMachineLogExists, 409, "machine_log_exists", []provisionertest.Route{"machine-log"}},
		{provisioner.ErrNoGateway, 409, "no_gateway", []provisionertest.Route{"claim"}},
		{provisioner.ErrRateLimited, 429, "rate_limited", all},
		{errors.New("409 handed_over"), 409, "handed_over", nil},
	}
	for _, code := range codes {
		for _, route := range all {
			srv, c, _, _ := setup(t)
			cl := claimed(t, srv, c, provisionertest.Run{})
			srv.Script(route, provisionertest.Reply{Status: code.status, Code: code.code})
			err := send(c, route, cl)
			if want := slices.Contains(code.on, route); errors.Is(err, code.err) != want {
				t.Errorf("%s to %s: %v, matches %v, want %v", code.code, route, err, !want, want)
			}
			var r *provisioner.Refusal
			if !errors.As(err, &r) || r.Code != code.code {
				t.Errorf("%s to %s: %v", code.code, route, err)
			}
			srv.Script(route, provisionertest.Reply{Status: code.status, Code: code.code, Unsigned: true},
				provisionertest.Reply{Status: code.status, Code: code.code, Unsigned: true},
				provisionertest.Reply{Status: code.status, Code: code.code, Unsigned: true},
				provisionertest.Reply{Status: code.status, Code: code.code, Unsigned: true},
				provisionertest.Reply{Status: code.status, Code: code.code, Unsigned: true})
			if err := send(c, route, cl); errors.Is(err, code.err) || !errors.Is(err, provisioner.ErrUnsigned) {
				t.Errorf("an unsigned %s to %s: %v", code.code, route, err)
			}
		}
	}
}

// send sends route's request for the claimed dispatch.
func send(c *provisioner.Client, route provisionertest.Route, cl provisioner.Claimed) error {
	ctx := context.Background()
	switch route {
	case "claim":
		got, _, err := c.Claim(ctx)
		got.RunCode.Clear()
		return err
	case "dispatched":
		return c.Dispatched(ctx, cl.DispatchID)
	case "machine-log":
		return c.MachineLog(ctx, cl.DispatchID, "a line\n", false)
	}
	return c.Release(ctx, cl.DispatchID, "a reason")
}

// TestAnotherRevisionHasItsLine answers each request with the signed 400
// unsupported_contract_version: the error starts with its fix, and contains the
// revision qory sends, the request and the code.
func TestAnotherRevisionHasItsLine(t *testing.T) {
	for _, route := range []provisionertest.Route{"claim", "dispatched", "release", "machine-log"} {
		srv, c, _, _ := setup(t)
		cl := claimed(t, srv, c, provisionertest.Run{})
		srv.Script(route, provisionertest.Reply{Status: 400, Code: "unsupported_contract_version"})
		err := send(c, route, cl)
		what := map[provisionertest.Route]string{
			"claim": "the claim", "dispatched": "dispatched for " + cl.DispatchID, "release": "the release of " + cl.DispatchID,
			"machine-log": "the machine log of " + cl.DispatchID,
		}[route]
		want := "update qory: Qory Apiary takes another provisioner revision, and this qory sends revision 1 (" + what + ": 400 unsupported_contract_version)"
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %s", route, err, want)
		}
	}
}

// TestTheModelsRefusals walks the fake's dispatches through the contract's refusals,
// each read as its error: a release after dispatched or after the redemption, a request
// after the release, a stale dispatch, a dispatch whose code was redeemed before its
// lease lapsed, an unknown one, and one whose code was not redeemed within 15 minutes.
func TestTheModelsRefusals(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	cl := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(ctx, cl.DispatchID, "a reason"); !errors.Is(err, provisioner.ErrMachineExists) {
		t.Errorf("a release after dispatched: %v", err)
	}

	redeemed := claimed(t, srv, c, provisionertest.Run{})
	if _, ok := srv.Redeem(redeemed.DispatchID); !ok {
		t.Fatal("the redemption")
	}
	if err := c.Release(ctx, redeemed.DispatchID, "a reason"); !errors.Is(err, provisioner.ErrMachineExists) {
		t.Errorf("a release after the redemption: %v", err)
	}

	released := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Release(ctx, released.DispatchID, "a reason"); err != nil {
		t.Errorf("a release before dispatched: %v", err)
	}
	if err := c.Release(ctx, released.DispatchID, "a reason again"); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("a release after the end: %v", err)
	}
	if err := c.Dispatched(ctx, released.DispatchID); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("dispatched after the end: %v", err)
	}
	if _, ok := srv.Redeem(released.DispatchID); ok {
		t.Error("a released dispatch's code was redeemed")
	}

	stale := claimed(t, srv, c, provisionertest.Run{Lease: 30 * time.Second})
	early := claimed(t, srv, c, provisionertest.Run{Lease: 30 * time.Second})
	if _, ok := srv.Redeem(early.DispatchID); !ok {
		t.Fatal("the redemption before the lease lapsed")
	}
	clk.set(clk.read().Add(30 * time.Second))
	if err := c.Dispatched(ctx, stale.DispatchID); !errors.Is(err, provisioner.ErrLeaseExpired) {
		t.Errorf("dispatched after a lapsed lease: %v", err)
	}
	if err := c.Release(ctx, stale.DispatchID, "a reason"); !errors.Is(err, provisioner.ErrLeaseExpired) {
		t.Errorf("a release after a lapsed lease: %v", err)
	}
	if _, ok := srv.Redeem(stale.DispatchID); ok {
		t.Error("a stale dispatch's code was redeemed")
	}
	if err := c.Dispatched(ctx, early.DispatchID); err != nil {
		t.Errorf("dispatched after the lease, with the code redeemed before it: %v", err)
	}
	if err := c.Release(ctx, "dp_0000000000000000", "a reason"); !errors.Is(err, provisioner.ErrForbidden) {
		t.Errorf("an unknown dispatch: %v", err)
	}

	unredeemed := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Dispatched(ctx, unredeemed.DispatchID); err != nil {
		t.Fatal(err)
	}
	clk.set(clk.read().Add(provisionertest.CodeLife))
	if err := c.Dispatched(ctx, unredeemed.DispatchID); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("dispatched 15 minutes after the claim, the code not redeemed: %v", err)
	}
	if err := c.Dispatched(ctx, early.DispatchID); err != nil {
		t.Errorf("dispatched 15 minutes after the claim, the code redeemed: %v", err)
	}
}

// TestARetryAfterIsBounded reads a signed 429's Retry-After from a second to an hour,
// in seconds or as a date; an unsigned 429's is not read.
func TestARetryAfterIsBounded(t *testing.T) {
	for _, c := range []struct {
		header string
		want   time.Duration
	}{
		{"7", 7 * time.Second}, {"0", time.Second}, {"999999", time.Hour}, {"soon", time.Second}, {"", time.Second},
		{testTime.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
	} {
		srv, cl, _, _ := setup(t)
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: c.header})
		_, _, err := cl.Claim(context.Background())
		var r *provisioner.Refusal
		if !errors.As(err, &r) || r.RetryAfter != c.want || !strings.Contains(err.Error(), "; qory takes a wait of "+c.want.String()+" from the Retry-After header, kept between 1 s and 1 h, or 1 s without a readable header") {
			t.Errorf("Retry-After %s: %v", c.header, err)
		}
	}
}

// TestAnAnswerThatBreaksTheContractIsNotUsed answers a claim with a signed 200 of
// another revision, a member the schema does not define, members of the claim before
// revision 1, and a member twice, and dispatched with a 200: each is ErrAnswerUnusable
// after one try, and its text quotes no value and no run code.
func TestAnAnswerThatBreaksTheContractIsNotUsed(t *testing.T) {
	edits := map[string]func(map[string]any){
		"revision 0":   func(d map[string]any) { d["revision"] = 0 },
		"revision 2":   func(d map[string]any) { d["revision"] = 2 },
		"revision 3":   func(d map[string]any) { d["revision"] = 3 },
		"a new member": func(d map[string]any) { d["extra"] = "the title of the ticket" },
		"a step":       func(d map[string]any) { d["step"] = "explore" },
		"a run id":     func(d map[string]any) { d["run_id"] = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d" },
		"no run code":  func(d map[string]any) { delete(d, "run_code") },
		"a run code of another shape": func(d map[string]any) {
			d["run_code"] = strings.ToLower(d["run_code"].(string))
		},
	}
	for name, edit := range edits {
		srv, c, _, log := setup(t)
		run := srv.Offer(provisionertest.Run{})[0]
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Edit: edit})
		cl, ok, err := c.Claim(context.Background())
		if !errors.Is(err, provisioner.ErrAnswerUnusable) || ok || cl.RunCode.Len() != 0 || len(srv.Calls()) != 1 {
			t.Errorf("a claim with %s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "ticket") || strings.Contains(strings.ToLower(err.Error()+log.String()), strings.ToLower(run.RunCode[5:20])) {
			t.Errorf("a claim with %s: the error or the log contains a value: %v", name, err)
		}
	}
	twice := strings.Replace(string(fixture(t, "claim-answer/machine.json")), `"version": 1,`, `"version": 1, "version": 1,`, 1)
	srv, c, _, _ := setup(t)
	srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: twice})
	if _, _, err := c.Claim(context.Background()); !errors.Is(err, provisioner.ErrAnswerUnusable) {
		t.Errorf("a member twice: %v", err)
	}

	srv, c, _, _ = setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 200, Body: `{}`})
	if err := c.Dispatched(context.Background(), cl.DispatchID); !errors.Is(err, provisioner.ErrAnswerUnusable) {
		t.Errorf("a 200 to dispatched: %v", err)
	}
}

// TestAnswersAreReadUpToTheirBound answers a claim's 200 one byte over its bound, and
// just within: over is ErrTooLarge, never read as signed; within is used. A refusal at
// its bound is read.
func TestAnswersAreReadUpToTheirBound(t *testing.T) {
	pad := func(body string, limit int) int { return limit - len(body) }
	srv, c, _, _ := setup(t)
	claim := string(fixture(t, "claim-answer/machine.json"))
	for range 5 {
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: claim, Pad: pad(claim, provisioner.MaxClaimAnswer) + 1})
	}
	if _, _, err := c.Claim(context.Background()); !errors.Is(err, provisioner.ErrTooLarge) || errors.Is(err, provisioner.ErrUnsigned) {
		t.Errorf("a claim's 200 over its bound: %v", err)
	}
	srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: claim, Pad: pad(claim, provisioner.MaxClaimAnswer)})
	cl, ok, err := c.Claim(context.Background())
	if err != nil || !ok || cl.RunCode.Len() == 0 {
		t.Errorf("a claim's 200 at its bound: %v", err)
	}
	refusal := `{"error":"gone"}`
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 410, Body: refusal, Pad: pad(refusal, provisioner.MaxAnswer)})
	if err := c.Release(context.Background(), cl.DispatchID, "a reason"); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("a refusal at its bound: %v", err)
	}
}

// TestARequestThatBreaksItsSchemaIsNotSent refuses, before anything is sent, a release
// whose reason contains a run key, an access key secret or a run code, is empty or too
// long, and a dispatch id outside its shape. No error quotes the secret.
func TestARequestThatBreaksItsSchemaIsNotSent(t *testing.T) {
	srv, c, _, _ := setup(t)
	ctx := context.Background()
	const dp = "dp_0123456789abcdef"
	for name, err := range map[string]error{
		"a run key":       c.Release(ctx, dp, "the key is qrk_abc.def"),
		"a secret":        c.Release(ctx, dp, "QAK_abcdef"),
		"a run code":      c.Release(ctx, dp, "the code is qecr_ABC"),
		"an empty reason": c.Release(ctx, dp, ""),
		"a long reason":   c.Release(ctx, dp, strings.Repeat("a", 201)),
		"a dispatch id":   c.Dispatched(ctx, "dp_../../v1/pools/claim"),
	} {
		if err == nil || accesskey.ContainsSecret(err.Error()) || strings.Contains(strings.ToLower(err.Error()), "qecr_") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(srv.Calls()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

// TestNoRedirectAndNoCookie answers a release with a signed 307 to another server and
// a claim with a cookie, through an HTTP client with a cookie jar and a redirect policy
// that follows: the 307 is a refusal, nothing reaches the other server, and the next
// request carries no cookie.
func TestNoRedirectAndNoCookie(t *testing.T) {
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(elsewhere.Close)
	srv, c, _, _ := setup(t)
	jar, _ := cookiejar.New(nil)
	c.HTTP = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	srv.Offer(provisionertest.Run{})
	srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Header: http.Header{"Set-Cookie": {"session=abc; Path=/"}}})
	cl, _, err := c.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 307, Header: http.Header{"Location": {elsewhere.URL + "/v1/dispatches/" + cl.DispatchID + "/release"}}})
	var r *provisioner.Refusal
	if err := c.Release(context.Background(), cl.DispatchID, "a reason"); !errors.As(err, &r) || r.Status != 307 {
		t.Errorf("a 307: %v", err)
	}
	if reached.Load() != 0 {
		t.Error("the redirect was followed")
	}
	if h := last(t, srv).Header; h.Get("Cookie") != "" {
		t.Errorf("a cookie was sent: %s", h.Get("Cookie"))
	}
}

// TestAClaimIsHeldAndCancelled claims with nothing queued: the fake holds the claim,
// then answers the signed 204, no run; a run offered while it is held is claimed at
// once; a cancelled context ends a held claim with its error and nothing logged.
func TestAClaimIsHeldAndCancelled(t *testing.T) {
	srv, c, _, log := setup(t)
	srv.Hold = 50 * time.Millisecond
	if _, ok, err := c.Claim(context.Background()); err != nil || ok {
		t.Errorf("an empty claim: %v %v", ok, err)
	}
	srv.Hold = 10 * time.Second
	go func() {
		time.Sleep(50 * time.Millisecond)
		srv.Offer(provisionertest.Run{})
	}()
	if _, ok, err := c.Claim(context.Background()); err != nil || !ok {
		t.Errorf("a run offered while held: %v %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, _, err := c.Claim(ctx); !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Errorf("a cancelled claim: %v after %s", err, time.Since(start))
	}
	if log.Len() != 0 {
		t.Errorf("logged:\n%s", log)
	}
}

// TestTheRunCodeLeaksNowhere claims after tries that fail, with the client's log on, and
// refusals whose bodies contain the run code: no log line, no error and no printout of
// what the client returned contains the run code or what looks like a secret.
func TestTheRunCodeLeaksNowhere(t *testing.T) {
	srv, c, _, log := setup(t)
	run := srv.Offer(provisionertest.Run{})[0]
	code := run.RunCode
	srv.Script(provisionertest.RouteClaim,
		provisionertest.Reply{Status: 204, Drop: true},
		provisionertest.Reply{Status: 409, Body: `{"error":"` + code + `"}`, Unsigned: true},
		provisionertest.Reply{Status: 500, Body: `{"error":"` + code + `"}`},
	)
	cl, ok, err := c.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if written(t, cl.RunCode) != code {
		t.Fatal("not the run code")
	}
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 409, Code: "qecr_x"})
	relErr := c.Release(context.Background(), cl.DispatchID, "a reason")
	var r *provisioner.Refusal
	if !errors.As(relErr, &r) || r.Code != "" {
		t.Errorf("a code that looks like a run code: %v", relErr)
	}
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Info("claimed", "claimed", cl, "code", cl.RunCode)
	slog.New(slog.NewJSONHandler(&out, nil)).Info("claimed", "claimed", cl, "code", cl.RunCode)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out.WriteString(sprintf(verb, cl) + sprintf(verb, cl.RunCode) + sprintf(verb, &cl))
	}
	b, _ := json.Marshal(cl)
	out.Write(b)
	out.WriteString(log.String() + relErr.Error())
	for _, s := range []string{code, code[5:], strings.ToLower(code[5:20]), "qecr_"} {
		if strings.Contains(strings.ToLower(out.String()), strings.ToLower(s)) {
			t.Errorf("the run code is in the output:\n%s", out.String())
		}
	}
	if accesskey.ContainsSecret(out.String()) {
		t.Errorf("what looks like a secret is in the output:\n%s", out.String())
	}
	cl.RunCode.Clear()
	if cl.RunCode.Len() != 0 || written(t, cl.RunCode) != "" {
		t.Error("the run code is kept after Clear")
	}
}

// sprintf is fmt.Sprintf of one value.
func sprintf(format string, v any) string { return fmt.Sprintf(format, v) }
