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

// claimed claims the run srv offers, which must be there.
func claimed(t *testing.T, srv *provisionertest.Server, c *provisioner.Client, run provisionertest.Run) provisioner.Claimed {
	t.Helper()
	srv.Offer(run)
	cl, ok, err := c.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
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

// TestARunGoesThroughEveryRequest claims an explore run, says it started, takes its
// handover and releases it, and a prebuild run through to prebuilt, each request signed
// with the pool's key, with the headers the contract names, a body its schema takes,
// and the clock's time, and each answer verified under the pin.
func TestARunGoesThroughEveryRequest(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	cl := claimed(t, srv, c, provisionertest.Run{})
	if cl.Step != provisioner.StepExplore || cl.PoolID != provisionertest.PoolID || cl.WorkItem == nil || !cl.LeaseUntil.Equal(testTime.Add(provisionertest.Lease)) {
		t.Errorf("claimed %+v", cl)
	}
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := srv.Dispatch(cl.DispatchID)
	if h.RunID != cl.RunID || h.Step != provisioner.StepExplore || h.WorkItem == nil || !sameJSON(t, h.Inputs, d.Run.Inputs) {
		t.Errorf("handed %+v", h)
	}
	if got := written(t, h.RunKey); got != d.Run.RunKey {
		t.Error("the run key is not the one handed over")
	}
	if err := c.Release(ctx, cl.DispatchID, "the run could not be started on this machine", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := srv.Dispatch(cl.DispatchID); d.State != provisionertest.StateEnded || d.Release == nil || !d.Release.Retry {
		t.Errorf("after the release: %+v", d)
	}

	pb := claimed(t, srv, c, provisionertest.Run{Step: provisioner.StepPrebuild})
	if pb.WorkItem != nil {
		t.Error("a prebuild names a work item")
	}
	if err := c.Dispatched(ctx, pb.DispatchID); err != nil {
		t.Fatal(err)
	}
	ph, err := c.Handover(ctx, pb.DispatchID, pb.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, ph.Inputs, []byte(`{"version":1,"environment_ref":"env-3f2a91c4"}`)) {
		t.Errorf("a prebuild's inputs: %s", ph.Inputs)
	}
	img := provisioner.Image{
		Image: "localhost:5001/qory-env/0123456789abcdef@sha256:3b9d0a5f7c1e2d4b6a8f0c2e4d6b8a0f1c3e5d7b9a1f3c5e7d9b1a3f5c7e9d1b",
		Size:  1234, EnvironmentRef: "env-3f2a91c4", Commit: "9c1e4a7b2d5f8e0a3c6b9d2f5a8c1e4b7d0a3f6c",
	}
	if err := c.Prebuilt(ctx, pb.DispatchID, pb.RunID, img); err != nil {
		t.Fatal(err)
	}
	if d, _ := srv.Dispatch(pb.DispatchID); d.Prebuilt == nil || d.Prebuilt.Image != img.Image || d.Prebuilt.Size != img.Size {
		t.Errorf("after prebuilt: %+v", d)
	}

	for _, call := range srv.Calls() {
		h := call.Header
		switch {
		case !call.Verified || call.Replayed || !call.Signed:
			t.Errorf("%s: verified %v, replayed %v, answer signed %v", call.Route, call.Verified, call.Replayed, call.Signed)
		case h.Get(accesskey.HeaderAccessKeyID) != provisionertest.KeyID || h.Get(accesskey.HeaderInstanceID) != provisionertest.InstanceID:
			t.Errorf("%s: the key's headers %v", call.Route, h)
		case h.Get("X-Qory-Contract-Version") != "3" || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "qory-test":
			t.Errorf("%s: the headers %v", call.Route, h)
		case call.Time.Before(testTime) || call.Time.After(clk.read()) || call.Time.Nanosecond() != 0:
			t.Errorf("%s: the time %s", call.Route, call.Time)
		}
	}
	want := []provisionertest.Route{"claim", "dispatched", "handover", "release", "claim", "dispatched", "handover", "prebuilt"}
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

// TestTheBodiesAreTheFixtures builds each request whose fixture the contract holds at
// the fixture's time, and checks the body sent is the fixture: the claim waits 25
// seconds and names no node pool, and dispatched names no job.
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
	cl := claimed(t, srv, c, provisionertest.Run{Step: provisioner.StepPrebuild, RunID: "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"})
	if b := fixture(t, "claim/without-pool-id.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("the claim\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	at("2026-10-15T12:00:20Z")
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	if b := fixture(t, "dispatched/without-external.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("dispatched\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	var hf contracts.Handover
	if err := json.Unmarshal(fixture(t, "handover/handover.json"), &hf); err != nil {
		t.Fatal(err)
	}
	at(hf.Time)
	h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	h.RunKey.Clear()
	if b := fixture(t, "handover/handover.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("the handover\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	var pf contracts.Prebuilt
	if err := json.Unmarshal(fixture(t, "prebuilt/prebuilt.json"), &pf); err != nil {
		t.Fatal(err)
	}
	at(pf.Time)
	if err := c.Prebuilt(ctx, cl.DispatchID, pf.RunID, provisioner.Image{Image: pf.Image, Size: pf.Size, EnvironmentRef: pf.EnvironmentRef, Commit: pf.Commit}); err != nil {
		t.Fatal(err)
	}
	if b := fixture(t, "prebuilt/prebuilt.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("prebuilt\n%s\nwant\n%s", last(t, srv).Body, b)
	}
	var rf contracts.Release
	if err := json.Unmarshal(fixture(t, "release/retry.json"), &rf); err != nil {
		t.Fatal(err)
	}
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: http.StatusNoContent})
	at(rf.Time)
	if err := c.Release(ctx, cl.DispatchID, rf.Reason, rf.Retry); err != nil {
		t.Fatal(err)
	}
	if b := fixture(t, "release/retry.json"); !sameJSON(t, last(t, srv).Body, b) {
		t.Errorf("the release\n%s\nwant\n%s", last(t, srv).Body, b)
	}
}

// atRevision3 is an answer fixture, which names revision 3.
func atRevision3(t *testing.T, name string) string {
	t.Helper()
	doc := string(fixture(t, name))
	if !strings.Contains(doc, `"revision": 3,`) {
		t.Fatalf("the fixture %s names no revision 3", name)
	}
	return doc
}

// TestTheAnswersAreTheFixtures answers a claim and a handover with each answer fixture,
// signed, at revision 3, and checks what the client read.
func TestTheAnswersAreTheFixtures(t *testing.T) {
	for _, step := range []string{provisioner.StepExplore, provisioner.StepPrebuild} {
		t.Run(step, func(t *testing.T) {
			srv, c, _, _ := setup(t)
			ctx := context.Background()
			srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: http.StatusOK, Body: atRevision3(t, "claim-answer/"+step+".json")})
			cl, ok, err := c.Claim(ctx)
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			var ca contracts.ClaimAnswer
			_ = json.Unmarshal(fixture(t, "claim-answer/"+step+".json"), &ca)
			if cl.DispatchID != ca.DispatchID || cl.RunID != ca.RunID || cl.PoolID != ca.PoolID || cl.Step != step ||
				cl.Access != ca.Access || cl.Target == nil || *cl.Target != *ca.Target || contracts.FormatTime(cl.LeaseUntil) != ca.LeaseUntil ||
				(cl.WorkItem == nil) != (ca.WorkItem == nil) || cl.WorkItem != nil && *cl.WorkItem != *ca.WorkItem {
				t.Errorf("claimed %+v, want %+v", cl, ca)
			}
			body := atRevision3(t, "handover-answer/"+step+".json")
			srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: http.StatusOK, Body: body})
			h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var ha struct {
				contracts.HandoverAnswer
				Inputs json.RawMessage `json:"inputs"`
			}
			_ = json.Unmarshal([]byte(body), &ha)
			if h.RunID != ha.RunID || h.Step != step || h.Domain != ha.Domain || h.Access != ha.Access ||
				h.Target == nil || *h.Target != *ha.Target || !bytes.Equal(h.Inputs, ha.Inputs) || written(t, h.RunKey) != ha.RunKey {
				t.Errorf("handed %+v", h)
			}
			if !strings.Contains(body, string(h.Inputs)) {
				t.Error("the inputs are not the answer's bytes")
			}
		})
	}
}

// TestEveryHandoverFixtureIsRead answers a handover with each of the contract's
// handover answers, signed: each is read, with its domain, its target or none, the
// access only with a target, and its run settings as sent.
func TestEveryHandoverFixtureIsRead(t *testing.T) {
	names, err := fs.Glob(contracts.ProvisionerFS, "fixtures/handover-answer/*.json")
	if err != nil || len(names) < 6 {
		t.Fatalf("the fixtures %v: %v", names, err)
	}
	for _, name := range names {
		name = strings.TrimPrefix(name, "fixtures/")
		t.Run(name, func(t *testing.T) {
			body := atRevision3(t, name)
			var want contracts.HandoverAnswer
			if err := json.Unmarshal([]byte(body), &want); err != nil {
				t.Fatal(err)
			}
			srv, c, _, _ := setup(t)
			cl := claimed(t, srv, c, provisionertest.Run{RunID: want.RunID, Step: want.Step})
			_ = c.Dispatched(context.Background(), cl.DispatchID)
			srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: http.StatusOK, Body: body})
			h, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if h.Domain != want.Domain || h.Access != want.Access || (h.Target == nil) != (want.Target == nil) ||
				h.Target != nil && *h.Target != *want.Target || (h.Target == nil) != (h.Access == "") {
				t.Errorf("handed %+v", h)
			}
			got, _ := json.Marshal(h.RunSettings)
			sent, _ := json.Marshal(want.RunSettings)
			if string(got) != string(sent) {
				t.Errorf("the run settings are %s, want %s", got, sent)
			}
		})
	}
}

// TestTheFakeOffersARunWithNoTarget claims and hands over a run the fake offers with no
// target and with run settings: neither answer has a target or an access, and the
// handover has the fixture's domain and the run settings offered.
func TestTheFakeOffersARunWithNoTarget(t *testing.T) {
	srv, c, _, _ := setup(t)
	rs := &contracts.RunSettings{
		Runtime: "claude", Model: "example-large", Effort: "high",
		ProviderModels: []contracts.ProviderModel{{ID: "example-large", Effort: []string{"high"}}},
		Tiers:          &contracts.Tiers{Light: &contracts.Tier{Model: "example-large"}},
	}
	cl := claimed(t, srv, c, provisionertest.Run{NoTarget: true, RunSettings: rs})
	if cl.Target != nil || cl.Access != "" {
		t.Errorf("claimed %+v", cl)
	}
	if err := c.Dispatched(context.Background(), cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	h, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(h.RunSettings)
	sent, _ := json.Marshal(rs)
	if h.Target != nil || h.Access != "" || h.Domain != "marketing" || string(got) != string(sent) {
		t.Errorf("handed %+v, run settings %s", h, got)
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
			err := c.Release(context.Background(), cl.DispatchID, "the pool program stopped before the run started", true)
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
			if n := strings.Count(log.String(), "Qory Apiary's answer could not be used, so the request is sent again after a wait"); n != 4 {
				t.Errorf("%d lines logged:\n%s", n, log)
			}

			srv2, c2, _, _ := setup(t)
			cl2 := claimed(t, srv2, c2, provisionertest.Run{})
			srv2.Script(provisionertest.RouteRelease, f.reply)
			if err := c2.Release(context.Background(), cl2.DispatchID, "the pool program stopped before the run started", true); err != nil {
				t.Errorf("a failure then a 204: %v", err)
			}
		})
	}
}

// TestASignedAnswerIsNotSentAgain answers a release with each signed refusal below 500
// and a handover with the signed 503 unavailable: one try each, returned as it is.
func TestASignedAnswerIsNotSentAgain(t *testing.T) {
	srv, c, _, _ := setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	for _, rep := range []provisionertest.Reply{{Status: 403, Code: "forbidden"}, {Status: 400, Code: "invalid_request"}, {Status: 429, Code: "rate_limited"}, {Status: 413, Code: "payload_too_large"}} {
		srv.Script(provisionertest.RouteRelease, rep)
		before := len(srv.Calls())
		var r *provisioner.Refusal
		if err := c.Release(context.Background(), cl.DispatchID, "a reason", false); !errors.As(err, &r) || !r.Signed || r.Status != rep.Status || r.Code != rep.Code || r.Resent {
			t.Errorf("%d: %v", rep.Status, err)
		}
		if n := len(srv.Calls()) - before; n != 1 {
			t.Errorf("%d: %d tries", rep.Status, n)
		}
	}
	if err := c.Dispatched(context.Background(), cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: 503, Code: "unavailable"})
	before := len(srv.Calls())
	if _, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID); !errors.Is(err, provisioner.ErrUnavailable) {
		t.Errorf("503: %v", err)
	}
	if n := len(srv.Calls()) - before; n != 1 {
		t.Errorf("503: %d tries", n)
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
	if err := c.Release(ctx, cl.DispatchID, "a reason", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(ctx, cl.DispatchID, "another reason", true); err != nil {
		t.Fatal(err)
	}
	if w := clk.slept(); len(w) != 0 {
		t.Errorf("a distinct request waited %v", w)
	}
	if err := c.Release(ctx, cl.DispatchID, "a reason", true); err != nil {
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
	codes := []struct {
		err    error
		status int
		code   string
		on     []provisionertest.Route
	}{
		{provisioner.ErrForbidden, 403, "forbidden", []provisionertest.Route{"claim", "dispatched", "handover", "release", "prebuilt"}},
		{provisioner.ErrNotFound, 404, "not_found", []provisionertest.Route{"claim", "dispatched", "handover", "release", "prebuilt"}},
		{provisioner.ErrGone, 410, "gone", []provisionertest.Route{"dispatched", "handover", "release", "prebuilt"}},
		{provisioner.ErrHandedOver, 409, "handed_over", []provisionertest.Route{"handover"}},
		{provisioner.ErrLeaseExpired, 409, "lease_expired", []provisionertest.Route{"dispatched", "handover", "release", "prebuilt"}},
		{provisioner.ErrNotDispatched, 409, "not_dispatched", []provisionertest.Route{"handover"}},
		{provisioner.ErrNoGateway, 409, "no_gateway", []provisionertest.Route{"claim", "handover"}},
		{provisioner.ErrNotPrebuild, 409, "not_prebuild", []provisionertest.Route{"prebuilt"}},
		{provisioner.ErrNotHandedOver, 409, "not_handed_over", []provisionertest.Route{"prebuilt"}},
		{provisioner.ErrRunMismatch, 409, "run_mismatch", []provisionertest.Route{"prebuilt"}},
		{provisioner.ErrUnavailable, 503, "unavailable", []provisionertest.Route{"handover"}},
		{provisioner.ErrRateLimited, 429, "rate_limited", []provisionertest.Route{"claim", "dispatched", "handover", "release", "prebuilt"}},
	}
	routes := []provisionertest.Route{"claim", "dispatched", "handover", "release", "prebuilt"}
	for _, code := range codes {
		for _, route := range routes {
			srv, c, _, _ := setup(t)
			cl := claimed(t, srv, c, provisionertest.Run{Step: provisioner.StepPrebuild})
			srv.Script(route, provisionertest.Reply{Status: code.status, Code: code.code})
			err := send(c, route, cl)
			if want := slices.Contains(code.on, route); errors.Is(err, code.err) != want {
				t.Errorf("%s to %s: %v, matches %v, want %v", code.code, route, err, !want, want)
			}
			if code.status >= 500 && route != "handover" {
				continue
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

// send sends route's request for the claimed run.
func send(c *provisioner.Client, route provisionertest.Route, cl provisioner.Claimed) error {
	ctx := context.Background()
	switch route {
	case "claim":
		_, _, err := c.Claim(ctx)
		return err
	case "dispatched":
		return c.Dispatched(ctx, cl.DispatchID)
	case "handover":
		h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
		h.RunKey.Clear()
		return err
	case "release":
		return c.Release(ctx, cl.DispatchID, "a reason", true)
	}
	return c.Prebuilt(ctx, cl.DispatchID, cl.RunID, provisioner.Image{
		Image: "localhost:5001/qory-env/0123456789abcdef@sha256:3b9d0a5f7c1e2d4b6a8f0c2e4d6b8a0f1c3e5d7b9a1f3c5e7d9b1a3f5c7e9d1b",
		Size:  1, EnvironmentRef: "env-3f2a91c4", Commit: "9c1e4a7b2d5f8e0a3c6b9d2f5a8c1e4b7d0a3f6c",
	})
}

// TestTheModelsRefusals walks the fake's dispatches through the contract's refusals,
// each read as its error: a handover before dispatched, a second handover, a request
// after the release, a stale dispatch, an unknown one, and a prebuilt for an explore
// run, before its handover, and for another run.
func TestTheModelsRefusals(t *testing.T) {
	srv, c, clk, _ := setup(t)
	ctx := context.Background()
	cl := claimed(t, srv, c, provisionertest.Run{})
	if _, err := c.Handover(ctx, cl.DispatchID, cl.RunID); !errors.Is(err, provisioner.ErrNotDispatched) {
		t.Errorf("before dispatched: %v", err)
	}
	if err := send(c, "prebuilt", cl); !errors.Is(err, provisioner.ErrNotPrebuild) {
		t.Errorf("prebuilt of an explore run: %v", err)
	}
	_ = c.Dispatched(ctx, cl.DispatchID)
	h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
	h.RunKey.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Handover(ctx, cl.DispatchID, cl.RunID); !errors.Is(err, provisioner.ErrHandedOver) {
		t.Errorf("a second handover: %v", err)
	}
	if err := c.Release(ctx, cl.DispatchID, "a reason", true); err != nil {
		t.Errorf("a release after the handover: %v", err)
	}
	if err := c.Release(ctx, cl.DispatchID, "a reason again", true); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("a release after the end: %v", err)
	}

	stale := claimed(t, srv, c, provisionertest.Run{Lease: 30 * time.Second})
	clk.set(clk.read().Add(30 * time.Second))
	if err := c.Dispatched(ctx, stale.DispatchID); !errors.Is(err, provisioner.ErrLeaseExpired) {
		t.Errorf("a lapsed lease: %v", err)
	}
	if err := c.Release(ctx, "dp_0000000000000000", "a reason", true); !errors.Is(err, provisioner.ErrForbidden) {
		t.Errorf("an unknown dispatch: %v", err)
	}

	pb := claimed(t, srv, c, provisionertest.Run{Step: provisioner.StepPrebuild})
	_ = c.Dispatched(ctx, pb.DispatchID)
	if err := send(c, "prebuilt", pb); !errors.Is(err, provisioner.ErrNotHandedOver) {
		t.Errorf("prebuilt before the handover: %v", err)
	}
	h, _ = c.Handover(ctx, pb.DispatchID, pb.RunID)
	h.RunKey.Clear()
	other := pb
	other.RunID = "0192f0c1-7d4e-7a2b-8c3d-000000000000"
	if err := send(c, "prebuilt", other); !errors.Is(err, provisioner.ErrRunMismatch) {
		t.Errorf("prebuilt for another run: %v", err)
	}
	srv.Expire(pb.DispatchID)
	if err := send(c, "prebuilt", pb); err != nil {
		t.Errorf("a handed-over dispatch does not go stale: %v", err)
	}
}

// TestARetryAfterIsBounded reads a signed 429's Retry-After from a second to an hour,
// in seconds or as a date; an unsigned 429's is not read.
func TestARetryAfterIsBounded(t *testing.T) {
	for _, c := range []struct {
		header string
		want   time.Duration
	}{
		{"7", 7 * time.Second}, {"0", time.Second}, {"999999", time.Hour}, {"soon", time.Second},
		{testTime.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
	} {
		srv, cl, _, _ := setup(t)
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: c.header})
		_, _, err := cl.Claim(context.Background())
		var r *provisioner.Refusal
		if !errors.As(err, &r) || r.RetryAfter != c.want || !strings.Contains(err.Error(), "asks to wait") {
			t.Errorf("Retry-After %s: %v", c.header, err)
		}
	}
}

// TestAnAnswerThatBreaksTheContractIsNotUsed answers a claim and a handover with a
// signed 200 of another revision, a member the schema does not define, a member twice,
// and for the handover another run and a run key outside its shape, and dispatched
// with a 200: each is ErrAnswerUnusable after one try, and its text quotes no value.
func TestAnAnswerThatBreaksTheContractIsNotUsed(t *testing.T) {
	edits := map[string]func(map[string]any){
		"revision 1":   func(d map[string]any) { d["revision"] = 1 },
		"revision 2":   func(d map[string]any) { d["revision"] = 2 },
		"revision 4":   func(d map[string]any) { d["revision"] = 4 },
		"a new member": func(d map[string]any) { d["extra"] = "the title of the ticket" },
		"another step": func(d map[string]any) { d["step"] = "build" },
	}
	for name, edit := range edits {
		srv, c, _, _ := setup(t)
		srv.Offer(provisionertest.Run{})
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Edit: edit})
		_, ok, err := c.Claim(context.Background())
		if name == "another step" {
			if err != nil || !ok {
				t.Errorf("a claim of another step is the caller's to refuse: %v", err)
			}
			continue
		}
		if !errors.Is(err, provisioner.ErrAnswerUnusable) || strings.Contains(err.Error(), "ticket") || len(srv.Calls()) != 1 {
			t.Errorf("a claim with %s: %v", name, err)
		}
	}
	twice := atRevision3(t, "claim-answer/explore.json")
	twice = strings.Replace(twice, `"version": 1,`, `"version": 1, "version": 1,`, 1)
	srv, c, _, _ := setup(t)
	srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: twice})
	if _, _, err := c.Claim(context.Background()); !errors.Is(err, provisioner.ErrAnswerUnusable) {
		t.Errorf("a member twice: %v", err)
	}

	handover := map[string]func(map[string]any){
		"revision 1":        func(d map[string]any) { d["revision"] = 1 },
		"revision 2":        func(d map[string]any) { d["revision"] = 2 },
		"another run":       func(d map[string]any) { d["run_id"] = "0192f0c1-7d4e-7a2b-8c3d-000000000000" },
		"a run key's shape": func(d map[string]any) { d["run_key"] = "qrk_" + d["run_key"].(string)[4:] + "!" },
		"no run key":        func(d map[string]any) { delete(d, "run_key") },
		"a number as key":   func(d map[string]any) { d["run_key"] = 7 },
		"a new member":      func(d map[string]any) { d["extra"] = 1 },
		"inputs too large": func(d map[string]any) {
			d["inputs"] = map[string]any{"version": 1, "x": strings.Repeat("a", contracts.MaxInputs)}
		},
		"a prebuild's input": func(d map[string]any) { d["step"] = "prebuild"; delete(d, "work_item") },
	}
	for name, edit := range handover {
		srv, c, _, log := setup(t)
		cl := claimed(t, srv, c, provisionertest.Run{})
		_ = c.Dispatched(context.Background(), cl.DispatchID)
		srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Edit: edit})
		h, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID)
		d, _ := srv.Dispatch(cl.DispatchID)
		if !errors.Is(err, provisioner.ErrAnswerUnusable) || h.RunKey.Len() != 0 {
			t.Errorf("a handover with %s: %v", name, err)
		}
		if strings.Contains(err.Error(), d.Run.RunKey[4:20]) || accesskey.ContainsSecret(err.Error()) || accesskey.ContainsSecret(log.String()) {
			t.Errorf("a handover with %s: the error or the log holds the key: %v", name, err)
		}
	}

	srv, c, _, _ = setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 200, Body: `{}`})
	if err := c.Dispatched(context.Background(), cl.DispatchID); !errors.Is(err, provisioner.ErrAnswerUnusable) {
		t.Errorf("a 200 to dispatched: %v", err)
	}
}

// TestAnswersAreReadUpToTheirBound answers a claim's 200 and a handover's 200 one byte
// over their bounds, and each just within: over is ErrTooLarge, never read as signed;
// within is used.
func TestAnswersAreReadUpToTheirBound(t *testing.T) {
	pad := func(body string, limit int) int { return limit - len(body) }
	srv, c, _, _ := setup(t)
	claim := atRevision3(t, "claim-answer/explore.json")
	for range 5 {
		srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: claim, Pad: pad(claim, provisioner.MaxClaimAnswer) + 1})
	}
	if _, _, err := c.Claim(context.Background()); !errors.Is(err, provisioner.ErrTooLarge) || errors.Is(err, provisioner.ErrUnsigned) {
		t.Errorf("a claim's 200 over its bound: %v", err)
	}
	srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Status: 200, Body: claim, Pad: pad(claim, provisioner.MaxClaimAnswer)})
	if _, ok, err := c.Claim(context.Background()); err != nil || !ok {
		t.Errorf("a claim's 200 at its bound: %v", err)
	}

	srv, c, _, _ = setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{RunID: "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"})
	_ = c.Dispatched(context.Background(), cl.DispatchID)
	handover := atRevision3(t, "handover-answer/explore.json")
	for range 5 {
		srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: 200, Body: handover, Pad: pad(handover, contracts.MaxHandoverAnswer) + 1})
	}
	if _, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID); !errors.Is(err, provisioner.ErrTooLarge) {
		t.Errorf("a handover's 200 over its bound: %v", err)
	}
	// Every try gets the answer at its bound, so no try reaches the model's.
	for range 5 {
		srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: 200, Body: handover, Pad: pad(handover, contracts.MaxHandoverAnswer)})
	}
	if h, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID); err != nil || h.RunKey.Len() == 0 {
		t.Errorf("a handover's 200 at its bound: %v", err)
	}
	refusal := `{"error":"gone"}`
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 410, Body: refusal, Pad: pad(refusal, provisioner.MaxAnswer)})
	if err := c.Release(context.Background(), cl.DispatchID, "a reason", true); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("a refusal at its bound: %v", err)
	}
}

// TestARequestThatBreaksItsSchemaIsNotSent refuses, before anything is sent, a release
// whose reason holds a run key or an access key secret, is empty or too long, a
// prebuilt of an image with a tag, and a dispatch id or a run id outside its shape. No
// error quotes the secret.
func TestARequestThatBreaksItsSchemaIsNotSent(t *testing.T) {
	srv, c, _, _ := setup(t)
	ctx := context.Background()
	const dp = "dp_0123456789abcdef"
	const run = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	img := provisioner.Image{Image: "localhost:5001/qory-env/x:latest", Size: 1, EnvironmentRef: "env-1", Commit: "c"}
	for name, err := range map[string]error{
		"a run key":           c.Release(ctx, dp, "the key is qrk_abc.def", true),
		"a secret":            c.Release(ctx, dp, "QAK_abcdef", true),
		"an empty reason":     c.Release(ctx, dp, "", true),
		"a long reason":       c.Release(ctx, dp, strings.Repeat("a", 201), true),
		"an image with a tag": c.Prebuilt(ctx, dp, run, img),
		"a dispatch id":       c.Dispatched(ctx, "dp_../../v1/pools/claim"),
		"a run id":            c.Prebuilt(ctx, dp, "../x", img),
	} {
		if err == nil || accesskey.ContainsSecret(err.Error()) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.Handover(ctx, dp, "x"); err == nil {
		t.Error("a handover of a run id outside its shape")
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
	if err := c.Release(context.Background(), cl.DispatchID, "a reason", true); !errors.As(err, &r) || r.Status != 307 {
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

// TestTheRunKeyLeaksNowhere hands over a run after tries that fail, with the client's
// log on, and refusals whose bodies hold the run key: no log line, no error and no
// printout of what the client returned holds the run key or what looks like a secret.
func TestTheRunKeyLeaksNowhere(t *testing.T) {
	srv, c, _, log := setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	_ = c.Dispatched(context.Background(), cl.DispatchID)
	d, _ := srv.Dispatch(cl.DispatchID)
	key := d.Run.RunKey
	srv.Script(provisionertest.RouteHandover,
		provisionertest.Reply{Status: 204, Drop: true},
		provisionertest.Reply{Status: 409, Body: `{"error":"` + key + `"}`, Unsigned: true},
		provisionertest.Reply{Status: 500, Body: `{"error":"` + key + `"}`},
	)
	h, err := c.Handover(context.Background(), cl.DispatchID, cl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if written(t, h.RunKey) != key {
		t.Fatal("not the run key")
	}
	srv.Script(provisionertest.RouteRelease, provisionertest.Reply{Status: 409, Code: "qrk_x"})
	relErr := c.Release(context.Background(), cl.DispatchID, "a reason", true)
	var r *provisioner.Refusal
	if !errors.As(relErr, &r) || r.Code != "" {
		t.Errorf("a code that looks like a secret: %v", relErr)
	}
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Info("handed", "handed", h, "key", h.RunKey)
	slog.New(slog.NewJSONHandler(&out, nil)).Info("handed", "handed", h, "key", h.RunKey)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out.WriteString(sprintf(verb, h) + sprintf(verb, h.RunKey) + sprintf(verb, &h))
	}
	b, _ := json.Marshal(h)
	out.Write(b)
	out.WriteString(log.String() + relErr.Error())
	for _, s := range []string{key, key[4:], strings.ToLower(key[4:20])} {
		if strings.Contains(strings.ToLower(out.String()), strings.ToLower(s)) {
			t.Errorf("the run key is in the output:\n%s", out.String())
		}
	}
	if accesskey.ContainsSecret(out.String()) {
		t.Errorf("what looks like a secret is in the output:\n%s", out.String())
	}
	h.RunKey.Clear()
	if h.RunKey.Len() != 0 || written(t, h.RunKey) != "" {
		t.Error("the run key is held after Clear")
	}
}

// sprintf is fmt.Sprintf of one value.
func sprintf(format string, v any) string { return fmt.Sprintf(format, v) }
