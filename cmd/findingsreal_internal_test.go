package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
)

// apiaryCall is one request that the fake Qory Apiary got at the findings path of its run
// API.
type apiaryCall struct {
	method, target string
	body           []byte
	signed         bool
}

// serveFindings makes control serve the findings of a run API from fake, as Qory Apiary
// does: it verifies the signature of the gateway's access key over the request (the body
// of a POST, the timestamp of a GET), returns the body of the fake's call with a 200
// signed under the control's key, and records each request. It returns the recorder.
func serveFindings(control *realControl, fake *fakeApiary) func() []apiaryCall {
	var mu sync.Mutex
	var seen []apiaryCall
	control.findings = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		signature := r.Header.Get(server.HeaderSignature)
		call := apiaryCall{method: r.Method, target: r.URL.RequestURI(), body: body}
		signed := accesskey.Request{
			AccessKeyID: r.Header.Get(server.HeaderAccessKeyID), InstanceID: r.Header.Get(server.HeaderInstanceID),
			Method: r.Method, Target: call.target}
		// The gateway signs the body of a POST and the timestamp of a GET.
		if r.Method == http.MethodPost {
			signed.Body = body
		} else {
			signed.Timestamp = r.Header.Get(server.HeaderTimestamp)
		}
		call.signed = control.accessKey.PublicKey().VerifyRequest(signed, signature)
		mu.Lock()
		seen = append(seen, call)
		mu.Unlock()
		var out []byte
		var err error
		switch r.Method {
		case http.MethodGet:
			out, err = fake.Findings(r.Context())
		case http.MethodPost:
			out, err = fake.PostFindings(r.Context(), body)
		default:
			err = errors.New("no such method")
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(server.HeaderSignature, control.signer.SignAnswer(accesskey.Answer{Status: http.StatusOK, RequestSignature: signature, Body: out}))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
	return func() []apiaryCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]apiaryCall(nil), seen...)
	}
}

// TestTheFindingsGoThroughForagersSession is a run that Forager opens at a gateway in this
// process, with a fake Qory Apiary as its control plane that serves the findings of the
// run API from [fakeApiary]. The run goes into [newFindingsPoster] as qory run gives it: a
// [foragerRun], so the calls are [session.Run.Findings] and [session.Run.PostFindings].
// The poster's read, its post of two raises and a second read reach the fake as a GET with
// no body, the GET before the batch, a POST with the body that the poster made, and a
// GET, all signed with the gateway's key at /v1/runs/<run id>/findings, and the second
// read lists the two findings that the post stored. A body over 8 MiB fails in the session
// and the fake gets no request for it.
func TestTheFindingsGoThroughForagersSession(t *testing.T) {
	ctx := context.Background()
	fake := newFakeApiary()
	control := startRealControlWith(t, `{"version":1}`)
	requests := serveFindings(control, fake)
	runs := t.TempDir()
	g, err := gateway.Start(ctx, gateway.Config{
		RunDir:       func(id string) string { return filepath.Join(runs, id) },
		NoLinkSocket: true,
		Report:       func(string) {},
		Server:       control.server(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(ctx)
	runID := event.NewRunID()
	r, err := session.Open(ctx, session.OpenSpec{RunID: runID, RunsDir: runs, Report: func(string) {}, Gateway: session.LocalGateway(g.LocalLink())})
	if err != nil {
		t.Fatal(err)
	}
	var run openedRun = foragerRun{r}
	poster := newFindingsPoster(run)

	before, err := poster.read(ctx, testWhat)
	if err != nil {
		t.Fatalf("the first read failed: %v", err)
	}
	if len(before.Findings) != 0 {
		t.Errorf("the first read lists %d findings, want none", len(before.Findings))
	}
	answer, _, err := poster.post(ctx, 1, testWhat, postBases, raisesOf(2))
	if err != nil {
		t.Fatalf("the post failed: %v", err)
	}
	if answer == nil || len(answer.Created) != 2 {
		t.Errorf("the post's answer is %+v, want two created findings", answer)
	}
	after, err := poster.read(ctx, testWhat)
	if err != nil {
		t.Fatalf("the second read failed: %v", err)
	}
	if len(after.Findings) != 2 || len(after.Bases) != 1 {
		t.Errorf("the second read lists %d findings and %d bases, want 2 and 1", len(after.Findings), len(after.Bases))
	}

	got := requests()
	var methods []string
	for _, c := range got {
		methods = append(methods, c.method)
		if c.target != "/v1/runs/"+runID+"/findings" {
			t.Errorf("the fake Qory Apiary got %s %s, want the findings of run %s", c.method, c.target, runID)
		}
		if !c.signed {
			t.Errorf("the %s at %s has no valid signature of the gateway's access key", c.method, c.target)
		}
		if c.method == http.MethodGet && len(c.body) != 0 {
			t.Errorf("the GET has a body of %d bytes, want none", len(c.body))
		}
	}
	if len(got) != 4 || methods[0] != http.MethodGet || methods[1] != http.MethodGet || methods[2] != http.MethodPost || methods[3] != http.MethodGet {
		t.Fatalf("the fake Qory Apiary got %v, want GET, GET, POST, GET", methods)
	}
	var post contracts.FindingsPost
	if err := json.Unmarshal(got[2].body, &post); err != nil || len(post.Events) != 2 || len(post.Bases) != 1 {
		t.Errorf("the POST body is %q (%v), want 2 events and 1 base", got[2].body, err)
	}

	_, err = run.PostFindings(ctx, make([]byte, server.MaxPostFindingsRequest+1))
	if err == nil {
		t.Error("a body over 8 MiB was sent")
	}
	var refusal *accesskey.Refusal
	if errors.As(err, &refusal) {
		t.Errorf("the error of a body over 8 MiB is the refusal %+v, want an error with no status", refusal)
	}
	if fate := fateOf(err); fate != fateMaybe {
		t.Errorf("the fate of the error %v is %d, want fateMaybe", err, fate)
	}
	if n := len(requests()); n != 4 {
		t.Errorf("the fake Qory Apiary got %d requests after the body over 8 MiB, want 4", n)
	}
	if _, err := r.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Findings(ctx); !errors.Is(err, session.ErrRunClosed) {
		t.Errorf("a read after the close failed with %v, want an error that wraps ErrRunClosed", err)
	}
	if _, err := run.PostFindings(ctx, []byte(`{"events":[]}`)); !errors.Is(err, session.ErrRunClosed) {
		t.Errorf("a post after the close failed with %v, want an error that wraps ErrRunClosed", err)
	}
}
