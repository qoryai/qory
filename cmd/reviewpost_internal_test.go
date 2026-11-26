package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
)

// postOutcome is what the fake does with one POST. The zero value stores the batch and
// returns the answer. With err set, the batch is stored first when store is set, and
// stored at the next call of land when later is set, as a request that is still in flight
// at the gateway; body is the answer's body in place of the real one, with no error.
type postOutcome struct {
	err   error
	store bool
	later bool
	body  string
}

// fakeApiary is Qory Apiary behind the two findings calls: it stores the events of each
// POST, lists the findings as a GET lists them (at most 500 findings, the newest 100
// events of each), and fails the calls that a test scripts. It counts the calls and the
// calls in flight at one time.
type fakeApiary struct {
	mu          sync.Mutex
	bases       []contracts.FindingBase
	findings    []contracts.Finding
	next        int
	gets, posts int
	bodies      []contracts.FindingsPost
	pending     []contracts.FindingsPost
	inflight    int
	maxInflight int
	// getFate and postFate return the error of GET n or the outcome of POST n, from 1.
	getFate  func(n int) error
	postFate func(n int) postOutcome
	// gate, when set, stops each POST until it is closed; entered gets one value when a
	// POST starts.
	gate    chan struct{}
	entered chan struct{}
	// onPost, when set, gets the context of each POST as it starts.
	onPost func(ctx context.Context)
}

func newFakeApiary() *fakeApiary {
	return &fakeApiary{entered: make(chan struct{}, 100)}
}

// stray ends a test that sends without end: the 31st call and each later one fail.
func (f *fakeApiary) stray() error {
	if f.gets+f.posts > 30 {
		return &accesskey.Refusal{Status: http.StatusBadRequest, Code: "stray", From: accesskey.FromGateway}
	}
	return nil
}

func (f *fakeApiary) enter() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight++
	f.maxInflight = max(f.maxInflight, f.inflight)
}

func (f *fakeApiary) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight--
}

// Findings is the GET.
func (f *fakeApiary) Findings(context.Context) ([]byte, error) {
	f.enter()
	defer f.leave()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if err := f.stray(); err != nil {
		return nil, err
	}
	if f.getFate != nil {
		if err := f.getFate(f.gets); err != nil {
			return nil, err
		}
	}
	listed := contracts.Findings{Bases: slices.Clone(f.bases), Findings: []contracts.Finding{}}
	for _, found := range f.findings[:min(len(f.findings), maxListedFindings)] {
		found.Events = found.Events[max(0, len(found.Events)-maxListedEvents):]
		listed.Findings = append(listed.Findings, found)
	}
	return json.Marshal(listed)
}

// PostFindings is the POST.
func (f *fakeApiary) PostFindings(ctx context.Context, body []byte) ([]byte, error) {
	f.enter()
	defer f.leave()
	if f.onPost != nil {
		f.onPost(ctx)
	}
	var post contracts.FindingsPost
	if err := json.Unmarshal(body, &post); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.posts++
	n := f.posts
	f.bodies = append(f.bodies, post)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		f.entered <- struct{}{}
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.stray(); err != nil {
		return nil, err
	}
	var out postOutcome
	if f.postFate != nil {
		out = f.postFate(n)
	}
	if out.err != nil {
		if out.store {
			f.apply(post)
		}
		if out.later {
			f.pending = append(f.pending, post)
		}
		return nil, out.err
	}
	answer := f.apply(post)
	if out.body != "" {
		return []byte(out.body), nil
	}
	return json.Marshal(answer)
}

// land stores the batches that a request in flight has not stored yet.
func (f *fakeApiary) land() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, post := range f.pending {
		f.apply(post)
	}
	f.pending = nil
}

// apply stores the events of post, as Qory Apiary does, and returns its answer.
func (f *fakeApiary) apply(post contracts.FindingsPost) contracts.FindingsPostAnswer {
	var answer contracts.FindingsPostAnswer
	for _, b := range post.Bases {
		if !slices.ContainsFunc(f.bases, func(have contracts.FindingBase) bool { return have.Target.Collection == b.Target.Collection }) {
			f.bases = append(f.bases, b)
		}
	}
	for _, e := range post.Events {
		actor := contracts.FindingActor{Kind: "qory", Name: e.By}
		if e.Kind == eventRaise {
			f.next++
			id := fmt.Sprintf("f%d", f.next)
			f.findings = append(f.findings, contracts.Finding{ID: id, Target: *e.Target, Stage: e.Stage,
				Source: *e.Source, Severity: e.Severity, Place: *e.Place, Point: e.Point, Problem: e.Problem,
				Fix: e.Fix, Proof: e.Proof, State: stateOpen,
				Events: []contracts.FindingEvent{{Kind: storedRaised, Round: 1, Stage: e.Stage, Actor: actor}}})
			answer.Created = append(answer.Created, contracts.FindingCreated{Ref: e.Ref, ID: id})
			continue
		}
		for i := range f.findings {
			if f.findings[i].ID == e.ID {
				f.findings[i].Events = append(f.findings[i].Events, contracts.FindingEvent{
					Kind: storedKinds[e.Kind][0], Round: 1, Stage: f.findings[i].Stage, Actor: actor, Reason: e.Reason})
			}
		}
	}
	counts := map[string]int{}
	for _, found := range f.findings {
		if found.State == stateOpen && found.Severity == reviewround.Blocking {
			counts[found.Stage]++
		}
	}
	for stage, n := range counts {
		answer.OpenBlocking = append(answer.OpenBlocking, contracts.FindingsOpenBlocking{Stage: stage, Count: n})
	}
	return answer
}

// seed stores n open findings of the reviewer other.
func (f *fakeApiary) seed(n int, other string) {
	for range n {
		f.next++
		f.findings = append(f.findings, apiaryFinding(fmt.Sprintf("f%d", f.next), sourceReviewer, other, stateOpen))
	}
}

// seedRepeats stores an open finding of the reviewer by, with repeats events of the
// repeated kind by the actor by and no other event, and returns its id.
func (f *fakeApiary) seedRepeats(by string, repeats int) string {
	f.next++
	id := fmt.Sprintf("f%d", f.next)
	found := apiaryFinding(id, sourceReviewer, by, stateOpen)
	found.Events = nil
	for range repeats {
		found.Events = append(found.Events, stored(storedRepeated, by, 1, ""))
	}
	f.findings = append(f.findings, found)
	return id
}

func (f *fakeApiary) counts() (gets, posts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, f.posts
}

func (f *fakeApiary) stored() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.findings)
}

// noWaits replaces the wait of the submissions in the test: it records each wait, calls
// hook with the number of the wait from 1 when there is one, and sleeps never. It returns
// the recorded waits.
func noWaits(t *testing.T, hook func(n int)) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	waits := &[]time.Duration{}
	before := submissionWait
	submissionWait = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		*waits = append(*waits, d)
		n := len(*waits)
		mu.Unlock()
		if hook != nil {
			hook(n)
		}
		return nil
	}
	t.Cleanup(func() { submissionWait = before })
	return waits
}

func gatewayRefusal(status int, code string) error {
	return &accesskey.Refusal{Status: status, Code: code, From: accesskey.FromGateway}
}

func apiaryRefusal(status int, code string) error {
	return &accesskey.Refusal{Status: status, Code: code, From: accesskey.FromApiary}
}

// retryIn is the refusal err with a Retry-After of d.
func retryIn(err error, d time.Duration) error {
	err.(*accesskey.Refusal).RetryAfter = d
	return err
}

// raisesOf is n raise events of the reviewer code-reviewer, each with a different point.
func raisesOf(n int) []contracts.FindingsPostEvent {
	var r refs
	var events []contracts.FindingsPostEvent
	for i := 1; i <= n; i++ {
		events = append(events, ledgerWhere.raiseOf(&r, "code-reviewer", sourceReviewer, "code-reviewer",
			reviewround.Blocking, contracts.FindingPlace{Quote: fmt.Sprintf("quote %d", i)},
			fmt.Sprintf("point %d", i), "problem", "fix", "proof"))
	}
	return events
}

// postBases is the bases of the tests: one target.
var postBases = []contracts.FindingBase{{Target: ledgerTarget, Base: strings.Repeat("a", 40)}}

// testWhat is what the caller of a post or a read did not do when it fails, here.
const testWhat = "the review did not end"

func wantCalls(t *testing.T, name string, f *fakeApiary, gets, posts int) {
	t.Helper()
	if g, p := f.counts(); g != gets || p != posts {
		t.Errorf("%s: %d GETs and %d POSTs, want %d and %d", name, g, p, gets, posts)
	}
}

func wantWaits(t *testing.T, name string, got *[]time.Duration, want ...time.Duration) {
	t.Helper()
	if len(*got) != len(want) {
		t.Errorf("%s: waits %v, want %v", name, *got, want)
		return
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("%s: waits %v, want %v", name, *got, want)
			return
		}
	}
}

// TestFindingsPostSendsTheBatchesInOrder is 250 events: three posts of 100, 100 and 50
// events in order, the bases in the first only, one GET before each, no wait, and the
// answer of the last batch.
func TestFindingsPostSendsTheBatchesInOrder(t *testing.T) {
	waits := noWaits(t, nil)
	fake := newFakeApiary()
	answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, postBases, raisesOf(250))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, "250 events", fake, 3, 3)
	wantWaits(t, "250 events", waits)
	var sizes []int
	for i, b := range fake.bodies {
		sizes = append(sizes, len(b.Events))
		if (len(b.Bases) == 1) != (i == 0) {
			t.Errorf("batch %d has %d bases, want them in the first only", i+1, len(b.Bases))
		}
	}
	if fmt.Sprint(sizes) != "[100 100 50]" {
		t.Errorf("the batches have %v events, want [100 100 50]", sizes)
	}
	if fake.bodies[2].Events[0].Point != "point 201" {
		t.Errorf("the third batch starts at %q, want point 201", fake.bodies[2].Events[0].Point)
	}
	if answer == nil || len(found.Findings) != 0 || len(answer.Created) != 50 || answer.OpenBlocking[0].Count != 250 {
		t.Errorf("answer %+v, found %+v, want the answer of batch 3 with 250 open blocking", answer, found)
	}
	if fake.stored() != 250 {
		t.Errorf("%d findings are stored, want 250", fake.stored())
	}
}

// TestFindingsPostAfterALostResponse is a batch whose POST ends with no answer: when the
// batch is stored qory sends it no second time, and when it is not stored qory sends it
// once more. In both, one GET follows the loss, after a wait of 1 second.
func TestFindingsPostAfterALostResponse(t *testing.T) {
	timeout := context.DeadlineExceeded
	for _, c := range []struct {
		name string
		err  error
	}{
		{"timeout", timeout},
		{"no connection", errors.New("connection reset")},
		{"gateway 502 control_plane_unavailable", gatewayRefusal(http.StatusBadGateway, "control_plane_unavailable")},
		{"gateway 503 with no code", &server.StatusError{What: "POST findings", Status: http.StatusServiceUnavailable}},
		{"Qory Apiary 503", apiaryRefusal(http.StatusServiceUnavailable, "unavailable")},
	} {
		for _, storedFirst := range []bool{true, false} {
			name := fmt.Sprintf("%s, stored %t", c.name, storedFirst)
			waits := noWaits(t, nil)
			fake := newFakeApiary()
			fake.postFate = func(n int) postOutcome {
				if n == 2 {
					return postOutcome{err: c.err, store: storedFirst}
				}
				return postOutcome{}
			}
			answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(150))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			wantWaits(t, name, waits, time.Second)
			if fake.stored() != 150 {
				t.Errorf("%s: %d findings are stored, want 150", name, fake.stored())
			}
			if storedFirst {
				wantCalls(t, name, fake, 3, 2)
				if answer != nil || len(found.Findings) != 150 {
					t.Errorf("%s: answer %v and %d found, want no answer and the GET", name, answer, len(found.Findings))
				}
			} else {
				wantCalls(t, name, fake, 3, 3)
				if answer == nil || len(found.Findings) != 0 {
					t.Errorf("%s: answer %v and %d found, want the answer of the second send", name, answer, len(found.Findings))
				}
			}
		}
	}
}

// TestFindingsPostAfterALostResponseOfAnEarlierBatch is a lost, stored first batch of two:
// the second batch goes out next, and its answer is the answer.
func TestFindingsPostAfterALostResponseOfAnEarlierBatch(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.postFate = func(n int) postOutcome {
		if n == 1 {
			return postOutcome{err: context.DeadlineExceeded, store: true}
		}
		return postOutcome{}
	}
	answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(150))
	if err != nil || answer == nil || len(found.Findings) != 0 || len(answer.Created) != 50 {
		t.Fatalf("answer %+v, found %+v, error %v", answer, found, err)
	}
	wantCalls(t, "lost first batch", fake, 3, 2)
}

// TestFindingsPostATimedOutPostThenAGateway429 is a POST that times out while it is still
// in flight at the gateway: the resend gets the gateway's 429 with nothing sent, the first
// POST lands during the wait, and the GET after the wait shows the batch, so no second
// POST is sent.
func TestFindingsPostATimedOutPostThenAGateway429(t *testing.T) {
	fake := newFakeApiary()
	waits := noWaits(t, func(n int) {
		if n == 2 {
			fake.land()
		}
	})
	fake.postFate = func(n int) postOutcome {
		if n == 1 {
			return postOutcome{err: context.DeadlineExceeded, later: true}
		}
		return postOutcome{err: retryIn(gatewayRefusal(http.StatusTooManyRequests, "rate_limited"), 3*time.Second)}
	}
	answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(3))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, "timed out, then 429", fake, 3, 2)
	wantWaits(t, "timed out, then 429", waits, time.Second, 3*time.Second)
	if fake.stored() != 3 || answer != nil || len(found.Findings) != 3 {
		t.Errorf("%d stored, answer %v, %d found, want 3, no answer and the GET", fake.stored(), answer, len(found.Findings))
	}
}

// TestFindingsPostStopsWhenALostBatchCannotBeFound is a lost POST that cannot be told from
// the GETs: 500 findings before the POST, so a new one may be hidden, or a finding with
// 100 events, so a new event may be hidden. qory stops with errNotKnown and sends no
// second POST.
func TestFindingsPostStopsWhenALostBatchCannotBeFound(t *testing.T) {
	for _, c := range []struct {
		name   string
		seed   func(f *fakeApiary) []contracts.FindingsPostEvent
		stored bool
		known  bool
	}{
		{"500 findings, stored", func(f *fakeApiary) []contracts.FindingsPostEvent { f.seed(500, "docs-reviewer"); return raisesOf(1) }, true, false},
		{"500 findings, not stored", func(f *fakeApiary) []contracts.FindingsPostEvent { f.seed(500, "docs-reviewer"); return raisesOf(1) }, false, false},
		{"100 events, stored", repeatOf(100), true, false},
		{"100 events, not stored", repeatOf(100), false, false},
		{"99 events, stored", repeatOf(99), true, true},
	} {
		noWaits(t, nil)
		fake := newFakeApiary()
		events := c.seed(fake)
		fake.postFate = func(int) postOutcome {
			return postOutcome{err: context.DeadlineExceeded, store: c.stored}
		}
		answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, events)
		_, posts := fake.counts()
		switch {
		case c.known && (err != nil || answer != nil || len(found.Findings) == 0):
			t.Errorf("%s: answer %v, %d found, error %v, want the GET", c.name, answer, len(found.Findings), err)
		case !c.known && !errors.Is(err, errNotKnown):
			t.Errorf("%s: error %v, want errNotKnown", c.name, err)
		case posts != 1:
			t.Errorf("%s: %d POSTs, want 1", c.name, posts)
		}
	}
}

// repeatOf is the seed of a finding with the given number of repeats by the actor
// code-reviewer, and the repeat event of it.
func repeatOf(events int) func(f *fakeApiary) []contracts.FindingsPostEvent {
	return func(f *fakeApiary) []contracts.FindingsPostEvent {
		id := f.seedRepeats("code-reviewer", events)
		return []contracts.FindingsPostEvent{{Kind: eventRepeat, By: "code-reviewer", ID: id}}
	}
}

// TestFindingsPostStops is a POST that fails with an error after which qory sends it no
// second time and waits for nothing: the run is over (a closed run, the gateway's 410,
// Qory Apiary's 409 run_ended), the gateway sent nothing (400, 404, 413), Qory Apiary
// stored nothing (400, 404, base_changed), or the status is another one below 500. The
// error keeps its cause, so runEndedLine still reads the end of a run.
func TestFindingsPostStops(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		ended bool
	}{
		{"run closed", fmt.Errorf("Open another run to send the POST findings request: %w.", session.ErrRunClosed), true},
		{"gateway 410", gatewayRefusal(http.StatusGone, "stopped"), true},
		{"Qory Apiary run_ended", apiaryRefusal(http.StatusConflict, runapi.CodeRunEnded), true},
		{"gateway 400", gatewayRefusal(http.StatusBadRequest, "bad_request"), false},
		{"gateway 404", gatewayRefusal(http.StatusNotFound, "not_found"), false},
		{"gateway 413", gatewayRefusal(http.StatusRequestEntityTooLarge, "payload_too_large"), false},
		{"gateway 401", gatewayRefusal(http.StatusUnauthorized, "unauthorized"), false},
		{"Qory Apiary 400", apiaryRefusal(http.StatusBadRequest, "invalid_request"), false},
		{"Qory Apiary 404", apiaryRefusal(http.StatusNotFound, "not_found"), false},
		{"Qory Apiary base_changed", apiaryRefusal(http.StatusConflict, "base_changed"), false},
	} {
		waits := noWaits(t, nil)
		fake := newFakeApiary()
		fake.postFate = func(int) postOutcome { return postOutcome{err: c.err} }
		_, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, postBases, raisesOf(2))
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		wantCalls(t, c.name, fake, 1, 1)
		wantWaits(t, c.name, waits)
		if _, over := runEndedLine(err); over != c.ended {
			t.Errorf("%s: runEndedLine says %t, want %t", c.name, over, c.ended)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: the error %q does not wrap its cause", c.name, err)
		}
		first, rest, _ := strings.Cut(err.Error(), "\n")
		if first != "Findings batch 1 of 1 is not stored, so the review did not end" || len("qory run: "+first) > 100 ||
			!strings.HasPrefix(rest, "  ") || !strings.HasSuffix(err.Error(), codeUsed) {
			t.Errorf("%s: the error is %q, want the cause first, the detail on line 2 and the code is used at the end", c.name, err)
		}
	}
}

// TestFindingsPostStopsOnAGETThatFails is a GET before the batch that fails with a stop:
// nothing is posted.
func TestFindingsPostStopsOnAGETThatFails(t *testing.T) {
	noWaits(t, nil)
	for _, err := range []error{
		fmt.Errorf("Open another run to send the GET findings request: %w.", session.ErrRunClosed),
		gatewayRefusal(http.StatusGone, "stopped"),
		gatewayRefusal(http.StatusNotFound, "not_found"),
	} {
		fake := newFakeApiary()
		fake.getFate = func(int) error { return err }
		if _, _, got := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(1)); !errors.Is(got, err) {
			t.Errorf("error %v, want %v", got, err)
		}
		wantCalls(t, err.Error(), fake, 1, 0)
	}
}

// TestFindingsPostWaitsOutA429 is a 429 on a POST, from the gateway or from Qory Apiary:
// qory waits Retry-After, one second with none, and sends the batch again. After 5 sends
// it stops with nothing stored.
func TestFindingsPostWaitsOutA429(t *testing.T) {
	for _, from := range []func(int, string) error{gatewayRefusal, apiaryRefusal} {
		waits := noWaits(t, nil)
		fake := newFakeApiary()
		fake.postFate = func(n int) postOutcome {
			switch n {
			case 1:
				return postOutcome{err: retryIn(from(http.StatusTooManyRequests, "rate_limited"), 7*time.Second)}
			case 2:
				return postOutcome{err: from(http.StatusTooManyRequests, "rate_limited")}
			}
			return postOutcome{}
		}
		answer, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(2))
		if err != nil || answer == nil {
			t.Fatalf("answer %v, error %v", answer, err)
		}
		wantCalls(t, "429 twice", fake, 1, 3)
		wantWaits(t, "429 twice", waits, 7*time.Second, time.Second)
		if fake.stored() != 2 {
			t.Errorf("%d findings are stored, want 2", fake.stored())
		}
	}

	waits := noWaits(t, nil)
	fake := newFakeApiary()
	fake.postFate = func(int) postOutcome {
		return postOutcome{err: retryIn(gatewayRefusal(http.StatusTooManyRequests, "rate_limited"), 2*time.Minute)}
	}
	_, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(2))
	wantCalls(t, "429 always", fake, 1, maxSubmissionTries)
	wantWaits(t, "429 always", waits, time.Minute, time.Minute, time.Minute, time.Minute)
	if err == nil || fake.stored() != 0 || !strings.HasPrefix(err.Error(), "Findings batch 1 of 1 is not stored, so the review did not end") {
		t.Errorf("error %v with %d stored, want the stop with nothing stored", err, fake.stored())
	}
}

// TestFindingsAGetIsSentAgain is a GET that fails: with no response, a status of 500 or
// above or a 429 it is sent again after a wait, up to 5 times; with a stop or a body that
// fails the contract it is not.
func TestFindingsAGetIsSentAgain(t *testing.T) {
	for _, c := range []struct {
		name  string
		err   error
		gets  int
		fails bool
	}{
		{"no response", errors.New("connection reset"), 3, false},
		{"timeout", context.DeadlineExceeded, 3, false},
		{"gateway 502", gatewayRefusal(http.StatusBadGateway, "control_plane_unavailable"), 3, false},
		{"gateway 503 with no code", &server.StatusError{What: "GET findings", Status: 503}, 3, false},
		{"gateway 429", retryIn(gatewayRefusal(http.StatusTooManyRequests, "rate_limited"), 4*time.Second), 3, false},
		{"no response for ever", errors.New("connection reset"), maxSubmissionTries, true},
		{"gateway 429 for ever", gatewayRefusal(http.StatusTooManyRequests, "rate_limited"), maxSubmissionTries, true},
		{"gateway 404", gatewayRefusal(http.StatusNotFound, "not_found"), 1, true},
		{"410", gatewayRefusal(http.StatusGone, "stopped"), 1, true},
	} {
		waits := noWaits(t, nil)
		fake := newFakeApiary()
		fake.getFate = func(n int) error {
			if c.fails || n <= 2 {
				return c.err
			}
			return nil
		}
		_, err := newFindingsPoster(fake).read(context.Background(), testWhat)
		if (err != nil) != c.fails {
			t.Errorf("%s: error %v, want failure %t", c.name, err, c.fails)
		}
		wantCalls(t, c.name, fake, c.gets, 0)
		if err != nil && !errors.Is(err, c.err) {
			t.Errorf("%s: error %q does not wrap its cause", c.name, err)
		}
		if c.gets == 3 && c.name == "gateway 429" {
			wantWaits(t, c.name, waits, 4*time.Second, 4*time.Second)
		}
	}
}

// TestFindingsAnUnreadableGetOrAnswerIsNotSentAgain is a GET with a body that fails the
// contract, and a POST that Qory Apiary stored and whose answer is no answer.
func TestFindingsAnUnreadableGetOrAnswerIsNotSentAgain(t *testing.T) {
	noWaits(t, nil)
	for _, body := range []string{`nope`, `{"bases":[],"findings":[],"must_understand":["/new"]}`,
		`{"findings":[{"id":"f1"},{"id":"f1"}]}`} {
		calls := &bodyCalls{get: body}
		_, err := newFindingsPoster(calls).read(context.Background(), testWhat)
		if err == nil || calls.gets != 1 || !strings.HasPrefix(err.Error(), "The findings of the work item are not read, so the review did not end\n  The list of the findings of the work item does not pass qory's checks: ") {
			t.Errorf("GET %q: error %v after %d GETs, want the stop with the failed check", body, err, calls.gets)
		}
	}
	for _, body := range []string{`nope`, `{"created":[],"open_blocking":[],"must_understand":["/new"]}`,
		`{"created":[],"open_blocking":[{"stage":"build","count":1},{"stage":"build","count":2}]}`} {
		fake := newFakeApiary()
		fake.postFate = func(int) postOutcome { return postOutcome{body: body} }
		_, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(1))
		wantCalls(t, body, fake, 1, 1)
		if err == nil || !strings.HasPrefix(err.Error(), "Findings batch 1 of 1 is stored, so the review did not end\n  The answer to the findings post does not pass qory's checks: ") {
			t.Errorf("POST answer %q: error %v, want the stop, with the batch stored", body, err)
		}
	}
}

// bodyCalls returns the body get for each GET, and counts the GETs.
type bodyCalls struct {
	get  string
	gets int
}

func (b *bodyCalls) Findings(context.Context) ([]byte, error) { b.gets++; return []byte(b.get), nil }

func (b *bodyCalls) PostFindings(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("no POST")
}

// TestFindingsPostAFailedBatchLeavesTheEarlierOnes is a round of 3 batches whose second
// POST fails with a 400: the first batch stays stored, the third is not sent, the error
// text lists the stored batch, and a second call for the round gets the same error with
// no call.
func TestFindingsPostAFailedBatchLeavesTheEarlierOnes(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.postFate = func(n int) postOutcome {
		if n == 2 {
			return postOutcome{err: apiaryRefusal(http.StatusBadRequest, "invalid_request")}
		}
		return postOutcome{}
	}
	poster := newFindingsPoster(fake)
	answer, found, err := poster.post(context.Background(), 1, testWhat, postBases, raisesOf(250))
	if err == nil || answer != nil || len(found.Findings) != 0 {
		t.Fatalf("answer %v, found %v, error %v, want the error only", answer, found, err)
	}
	wantCalls(t, "batch 2 of 3 fails", fake, 2, 2)
	if fake.stored() != 100 {
		t.Errorf("%d findings are stored, want the 100 of batch 1", fake.stored())
	}
	for _, want := range []string{"Findings batch 2 of 3 is not stored, so the review did not end\n  ", "\n  " + textFindingsStored + codeUsed} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error is %q, want %q in it", err, want)
		}
	}
	again, _, second := poster.post(context.Background(), 1, testWhat, postBases, raisesOf(250))
	if again != nil || second != err {
		t.Errorf("the second call got %v and %v, want the first call's error", again, second)
	}
	wantCalls(t, "second call", fake, 2, 2)
	// Batch 3 fails: the message lists the two stored batches.
	fake = newFakeApiary()
	fake.postFate = func(n int) postOutcome {
		if n == 3 {
			return postOutcome{err: apiaryRefusal(http.StatusConflict, "base_changed")}
		}
		return postOutcome{}
	}
	_, _, err = newFindingsPoster(fake).post(context.Background(), 1, testWhat, postBases, raisesOf(250))
	if err == nil || !strings.Contains(err.Error(), "\n  Batches 1 to 2 are stored"+codeUsed) {
		t.Errorf("batch 3 fails with %v, want batches 1 to 2 stored in the text", err)
	}
}

// TestFindingsPostPostsARoundOnce is two callers of one round: the second waits for the
// first, which is in its POST, and gets its answer. One POST is sent, and no two host
// calls are in flight, a read included. Another round posts again.
func TestFindingsPostPostsARoundOnce(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.gate = make(chan struct{})
	poster := newFindingsPoster(fake)
	type result struct {
		answer *contracts.FindingsPostAnswer
		err    error
	}
	results := make(chan result, 2)
	post := func() {
		answer, _, err := poster.post(context.Background(), 1, testWhat, nil, raisesOf(2))
		results <- result{answer, err}
	}
	go post()
	<-fake.entered
	go post()
	read := make(chan error, 1)
	go func() {
		_, err := poster.read(context.Background(), testWhat)
		read <- err
	}()
	close(fake.gate)
	var got []result
	for range 2 {
		select {
		case r := <-results:
			got = append(got, r)
		case <-time.After(30 * time.Second):
			t.Fatal("a caller did not return")
		}
	}
	if err := <-read; err != nil {
		t.Errorf("read: %v", err)
	}
	if got[0].err != nil || got[1].err != nil || got[0].answer == nil || got[0].answer != got[1].answer {
		t.Errorf("the callers got %+v and %+v, want one answer", got[0], got[1])
	}
	if _, posts := fake.counts(); posts != 1 || fake.stored() != 2 {
		t.Errorf("%d POSTs and %d findings, want 1 and 2", posts, fake.stored())
	}
	if fake.maxInflight != 1 {
		t.Errorf("%d host calls were in flight together, want 1", fake.maxInflight)
	}
	if _, _, err := poster.post(context.Background(), 2, testWhat, nil, raisesOf(1)); err != nil {
		t.Fatal(err)
	}
	if _, posts := fake.counts(); posts != 2 || fake.stored() != 3 {
		t.Errorf("round 2: %d POSTs and %d findings, want 2 and 3", posts, fake.stored())
	}
}

// TestFindingsACallerStopsWaitingWhenItsContextEnds is a read, behind a POST that has not
// returned, whose context is cancelled.
func TestFindingsACallerStopsWaitingWhenItsContextEnds(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.gate = make(chan struct{})
	poster := newFindingsPoster(fake)
	done := make(chan struct{})
	go func() {
		defer close(done)
		poster.post(context.Background(), 1, testWhat, nil, raisesOf(1))
	}()
	<-fake.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := poster.read(ctx, testWhat); !errors.Is(err, context.Canceled) {
		t.Errorf("read: %v, want context.Canceled", err)
	}
	close(fake.gate)
	<-done
}

// TestFindingsPostWithNoEventsReadsTheFindings is a round with no events: no POST, one
// GET, and the GET stands in for the answer.
func TestFindingsPostWithNoEventsReadsTheFindings(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.seed(2, "docs-reviewer")
	answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, postBases, nil)
	if err != nil || answer != nil || len(found.Findings) != 2 {
		t.Errorf("answer %v, %d found, error %v, want the GET", answer, len(found.Findings), err)
	}
	wantCalls(t, "no events", fake, 1, 0)
}

// TestFindingsAGetThatStandsInForTheAnswerStopsAtFiveHundredFindings is a post whose GET
// stands in for the answer, since it has no events or its response was lost and the batch
// is stored. A GET of 500 findings may hide an open finding, so qory returns the error
// with the cause and no findings; a GET of 499 findings is the result.
func TestFindingsAGetThatStandsInForTheAnswerStopsAtFiveHundredFindings(t *testing.T) {
	const line = "The GET of the findings lists 500, its limit, so " + testWhat
	// stored is the stop for a GET that shows a batch that is stored.
	const stored = "Findings batch 1 of 1 is stored, so " + testWhat + "\n  The GET of the findings lists 500, its limit, and the work item may have more findings"
	for _, c := range []struct {
		name   string
		seed   int
		events int
		wantOK bool
		want   string
	}{
		{"no events, 500 findings", 500, 0, false, line},
		{"no events, 499 findings", 499, 0, true, ""},
		{"a lost response, 500 findings after the batch", 499, 1, false, stored},
		{"a lost response, 499 findings after the batch", 498, 1, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			noWaits(t, nil)
			fake := newFakeApiary()
			fake.seed(c.seed, "docs-reviewer")
			fake.postFate = func(int) postOutcome { return postOutcome{err: context.DeadlineExceeded, store: true} }
			answer, found, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(c.events))
			switch {
			case c.wantOK && (err != nil || answer != nil || len(found.Findings) != c.seed+c.events):
				t.Errorf("answer %v, %d found, error %v, want the GET of %d findings", answer, len(found.Findings), err, c.seed+c.events)
			case !c.wantOK && (err == nil || !strings.Contains(err.Error(), c.want) || answer != nil || len(found.Findings) != 0):
				t.Errorf("answer %v, %d found, error %v, want the error %q and no findings", answer, len(found.Findings), err, c.want)
			}
			if _, posts := fake.counts(); posts != c.events {
				t.Errorf("%d POSTs, want %d", posts, c.events)
			}
		})
	}
}

// TestFindingsACancelledRunStopsAtTheWait is a lost POST, then a context that ends during
// the wait: qory returns at once, with the batch marked as maybe stored.
func TestFindingsACancelledRunStopsAtTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.postFate = func(int) postOutcome {
		cancel()
		return postOutcome{err: context.Canceled, store: true}
	}
	_, _, err := newFindingsPoster(fake).post(ctx, 1, testWhat, nil, raisesOf(1))
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "Findings batch 1 of 1 may be stored, so the review did not end\n  ") {
		t.Errorf("error %v, want the cancel with the cause of a batch that may be stored", err)
	}
	wantCalls(t, "cancelled", fake, 1, 1)
}

// TestFindingsNoControlPlaneStopsAfterTheGETTries is the error of a run with no control
// plane, which has no type: it is read as a call with no response, so the GET before the
// batch is sent 5 times, and nothing is posted.
func TestFindingsNoControlPlaneStopsAfterTheGETTries(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	fake.getFate = func(int) error {
		return errors.New("Open the run with a control plane to send the GET findings request: this run has none.")
	}
	_, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, raisesOf(1))
	if err == nil {
		t.Fatal("no error")
	}
	wantCalls(t, "no control plane", fake, maxSubmissionTries, 0)
}

// TestRetryAfterReadsARefusalOfTheLink is the wait of retryAfter for a run API error, a
// refusal of the gateway's link and an error with neither: the Retry-After, 1 second with
// none, at most one minute.
func TestRetryAfterReadsARefusalOfTheLink(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want time.Duration
	}{
		{"run API error", &runapi.Error{Status: 429, RetryAfter: 4}, 4 * time.Second},
		{"refusal", retryIn(gatewayRefusal(429, "rate_limited"), 9*time.Second), 9 * time.Second},
		{"wrapped refusal", fmt.Errorf("batch: %w", retryIn(apiaryRefusal(429, "rate_limited"), 2*time.Second)), 2 * time.Second},
		{"refusal with none", gatewayRefusal(429, "rate_limited"), time.Second},
		{"refusal over the cap", retryIn(gatewayRefusal(429, "rate_limited"), time.Hour), maxSubmissionWait},
		{"run API error over the cap", &runapi.Error{Status: 429, RetryAfter: 3600}, maxSubmissionWait},
		{"neither", errors.New("connection reset"), time.Second},
	} {
		if got := retryAfter(c.err); got != c.want {
			t.Errorf("%s: retryAfter = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFindingsPostMasksTheKeysOfTheTextThatAnAgentFileHad is a raise, a close and a reply
// whose texts, as qory read them from a report and from the build ledger, have a key of a
// known shape: the POST has *** in each text and the key in none.
func TestFindingsPostMasksTheKeysOfTheTextThatAnAgentFileHad(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	has := func(label string) string { return label + " " + key + " end" }
	raise := ledgerWhere.raiseOf(&refs{}, "code-reviewer", sourceReviewer, "code-reviewer", reviewround.Blocking,
		contracts.FindingPlace{Quote: has("quote"), File: "keys/" + key + ".txt", Line: 3},
		has("point"), has("problem"), has("fix"), has("proof"))
	events := []contracts.FindingsPostEvent{raise,
		{Kind: eventCloseFixed, By: "code-reviewer", ID: "f1", Reason: has("close")},
		{Kind: eventReplyFixed, By: "implement-changes", ID: "f2", Reason: has("reply")}}
	fake := newFakeApiary()
	if _, _, err := newFindingsPoster(fake).post(context.Background(), 1, testWhat, nil, events); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(fake.bodies)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abcdefghijklmnopqrstuvwxyz") || strings.Count(string(data), "***") != 8 {
		t.Errorf("the POST is %s, want *** for the key in each of the 8 texts and the key in none", data)
	}
	// The events of the caller stay as they were.
	if !strings.Contains(events[0].Proof, key) || !strings.Contains(events[0].Place.File, key) {
		t.Errorf("the caller's event is %+v, want it unchanged", events[0])
	}
}

// TestFindingsRoundPostsAnOccasionOnceAndPostedSeesIt is a round that two callers make for
// one occasion, as the check of a session's verdict and the end of the session do: the
// first reads the findings and posts the events that build makes, the second gets the
// first's answer with no read and no POST, and posted reports the occasion as posted. An
// error of build is not the occasion's result, so the next call builds again, and another
// occasion posts again.
func TestFindingsRoundPostsAnOccasionOnceAndPostedSeesIt(t *testing.T) {
	noWaits(t, nil)
	fake := newFakeApiary()
	poster := newFindingsPoster(fake)
	builds := 0
	build := func(contracts.Findings) ([]contracts.FindingBase, []contracts.FindingsPostEvent, error) {
		builds++
		return nil, raisesOf(2), nil
	}
	if done, err := poster.posted(context.Background(), 1); done || err != nil {
		t.Fatalf("posted before any round is %v with %v, want false", done, err)
	}
	failed := errors.New("no events")
	if _, _, err := poster.round(context.Background(), 1, testWhat, func(contracts.Findings) ([]contracts.FindingBase, []contracts.FindingsPostEvent, error) {
		return nil, nil, failed
	}); !errors.Is(err, failed) {
		t.Fatalf("a round whose build fails returned %v", err)
	}
	if done, _ := poster.posted(context.Background(), 1); done {
		t.Error("posted is true after a build that failed")
	}
	first, _, err := poster.round(context.Background(), 1, testWhat, build)
	if err != nil {
		t.Fatal(err)
	}
	getsAfter, postsAfter := fake.counts()
	second, _, err := poster.round(context.Background(), 1, testWhat, build)
	if err != nil || second != first || builds != 1 {
		t.Errorf("the second call returned %p (%v) after %d builds, want the first answer %p after 1", second, err, builds, first)
	}
	if gets, posts := fake.counts(); gets != getsAfter || posts != postsAfter || posts != 1 || fake.stored() != 2 {
		t.Errorf("%d reads and %d POSTs after the second call, want %d and %d, with 2 findings stored", gets, posts, getsAfter, postsAfter)
	}
	if done, err := poster.posted(context.Background(), 1); !done || err != nil {
		t.Errorf("posted after the round is %v with %v, want true", done, err)
	}
	if done, _ := poster.posted(context.Background(), 2); done {
		t.Error("posted is true for an occasion that was not posted")
	}
	if _, _, err := poster.round(context.Background(), 2, testWhat, build); err != nil || fake.stored() != 4 {
		t.Errorf("occasion 2 returned %v with %d findings stored, want 4", err, fake.stored())
	}
}
