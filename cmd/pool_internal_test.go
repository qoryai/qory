package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestTheRecordDirIsQoryRuns checks that where the pool's launcher looks for a run's
// record is where qory run keeps it: explore.RecordDir of a run directory's state/ is
// runsDir of qory's state directory when XDG_STATE_HOME is that state/, as each of the
// run's commands gets it, for a checkout reached through a link too.
func TestTheRecordDirIsQoryRuns(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(work, link); err != nil {
		t.Fatal(err)
	}
	const runID = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	t.Setenv("XDG_STATE_HOME", state)
	qoryState, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{work, link} {
		want, err := runsDir(qoryState, dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := explore.RecordDir(state, dir, runID)
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(want, runID) {
			t.Errorf("explore.RecordDir is %s, qory run's record is under %s", got, want)
		}
	}
}

// syncBuffer is a buffer written from more than one goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunPoolRunsTheLoop is qory run pool's loop as runPool wires it: it claims from
// forager.yaml's server with the pool's key, signed as the pool's instance, releases a
// run of a step it does not run, starts a build with the prebuild launcher given the
// runs' wall, whose agent's image here it refuses, logs to stderr, and returns nil once
// its context ends.
func TestRunPoolRunsTheLoop(t *testing.T) {
	srv := provisionertest.New(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	s := &poolSettings{
		Forager: &config.Forager{Server: &config.ForagerServer{URL: srv.URL + "/", AccessKeyID: provisionertest.KeyID, Pin: srv.Pin()}},
		Pool:    &config.ForagerPool{Runs: 1, Prebuilds: 1, Registry: "localhost:5001/qory-env", HarnessRef: "v1.2.3", RunConfig: filepath.Join(root, "runs")},
		Runs:    &config.Forager{Wall: &config.ForagerWall{Image: "agent", User: "1000:1000", Helper: filepath.Join(root, "qory-linux")}},
		State:   filepath.Join(root, "qory", "pool"), InstanceID: provisionertest.InstanceID, InstanceName: "pool-test", Stderr: &stderr,
	}
	srv.Offer(provisionertest.Run{Step: "review", Inputs: json.RawMessage(`{"version":1}`)}, provisionertest.Run{Step: "prebuild"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runPool(ctx, s, srv.Pool, srv.Pin()) }()
	deadline := time.Now().Add(20 * time.Second)
	for len(srv.Dispatches()) < 2 || srv.Dispatches()[1].Release == nil {
		if time.Now().After(deadline) {
			t.Fatalf("no release; the fake was sent %v", srv.Routes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ds := srv.Dispatches()
	if r := ds[0].Release; r.Retry || r.Reason != "the pool program runs only the explore and prebuild steps" {
		t.Errorf("the other step was released %+v", r)
	}
	if r := ds[1].Release; r.Retry || r.Reason != "the run could not be started on this machine" {
		t.Errorf("the build was released %+v", r)
	}
	for _, c := range srv.Calls() {
		if !c.Verified || c.Header.Get(accesskey.HeaderInstanceID) != provisionertest.InstanceID {
			t.Errorf("%s was not signed as the pool's instance", c.Route)
		}
	}
	if out := stderr.String(); !strings.Contains(out, `msg="released the run"`) || !strings.Contains(out, `msg="claimed a run"`) ||
		!strings.Contains(out, `msg="`+ds[1].Run.RunID+`: the agent image is not pinned by digest"`) || accesskey.ContainsSecret(out) {
		t.Errorf("stderr is %q", out)
	}
}

// TestThePoolPostsOutcomesDirectly is the outcome of a run the pool handed over, posted
// with the loop qory run pool runs: it reaches the server directly, and a gateway that
// gateway.server.via names is sent nothing, though [loadPool] refuses that setting first.
func TestThePoolPostsOutcomesDirectly(t *testing.T) {
	srv := provisionertest.New(t)
	var viaCalls int
	var mu sync.Mutex
	via := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		viaCalls++
		mu.Unlock()
		http.Error(w, "a gateway", http.StatusBadGateway)
	}))
	t.Cleanup(via.Close)
	ctx := context.Background()
	runID := srv.Offer(provisionertest.Run{})[0]
	c := srv.Client()
	cl, _, err := c.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	h, err := c.Handover(ctx, cl.DispatchID, cl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	h.RunKey.Clear()
	root := t.TempDir()
	s := &poolSettings{
		Forager: &config.Forager{Server: &config.ForagerServer{URL: srv.URL + "/", AccessKeyID: provisionertest.KeyID, Pin: srv.Pin(),
			Via: &config.ForagerVia{URL: via.URL}}},
		Pool:  &config.ForagerPool{Runs: 1, Prebuilds: 1, Registry: "localhost:5001/qory-env", HarnessRef: "v1.2.3", RunConfig: filepath.Join(root, "runs")},
		Runs:  &config.Forager{Wall: &config.ForagerWall{Image: agentImageOf, User: "1000:1000", Helper: filepath.Join(root, "qory-linux")}},
		State: filepath.Join(root, "qory", "pool"), InstanceID: provisionertest.InstanceID, InstanceName: "pool-test",
	}
	p := poolLoop(s, srv.Pool, srv.Pin(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	conf, _, err := p.Outcomes.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := server.Outcome{State: "failed", Reason: "time_limit", Time: time.Now().UTC().Format(time.RFC3339)}
	if err := p.Outcomes.PostOutcome(ctx, conf.Run.URL, runID, o); err != nil {
		t.Fatal(err)
	}
	if got := srv.Outcomes()[runID]; got.State != "failed" || got.Reason != "time_limit" {
		t.Errorf("the server recorded %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if viaCalls != 0 {
		t.Errorf("the gateway was sent %d requests", viaCalls)
	}
}

// agentImageOf is an agent's image pinned by its digest.
const agentImageOf = "registry.example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"
