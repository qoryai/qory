package cmd

import (
	"bytes"
	"context"
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

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/pool"
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
// forager.yaml's server directly, with the pool's key, signed as the pool's instance,
// and sends a gateway that gateway.server.via sets nothing, though [loadPool] refuses that
// setting first. It releases a claim that sets a machine type, drops a dispatch whose
// dispatched returned 410, logs to stderr, and returns nil once its context ends. Each
// qory run it would start is this binary, with the pool's folder and the runs'
// configuration directory.
func TestRunPoolRunsTheLoop(t *testing.T) {
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	docker := unreachedProbe(t)
	s := &poolSettings{
		Forager: &config.Forager{Server: &config.ForagerServer{URL: srv.URL + "/", AccessKeyID: provisionertest.KeyID, Pin: srv.Pin(),
			Via: &config.ForagerVia{URL: via.URL}}},
		Pool: &config.ForagerPool{Runs: 2, Registry: "localhost:5001/qory-env", RunConfig: filepath.Join(root, "runs")},
		Runs: &config.Forager{Wall: &config.ForagerWall{Image: agentImageOf, User: "1000:1000", Helper: filepath.Join(root, "qory-linux"),
			Command: docker}},
		State: filepath.Join(root, "qory", "pool"), InstanceID: provisionertest.InstanceID, InstanceName: "pool-test", Stderr: &stderr,
	}
	p := poolLoop(s, srv.Pool, srv.Pin(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	want := pool.Settings{Runs: 2, State: s.State, RunConfig: s.Pool.RunConfig,
		Probe: pool.Probe{Docker: docker, Image: agentImageOf, User: "1000:1000"}}
	if p.Settings != want {
		t.Errorf("the loop's settings are %+v, want %+v", p.Settings, want)
	}
	srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 410, Code: "gone"})
	srv.Offer(provisionertest.Run{Machine: "large"}, provisionertest.Run{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runPool(ctx, s, srv.Pool, srv.Pin()) }()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), `msg="released the run"`) || !strings.Contains(stderr.String(), `msg="Qory Apiary did not record the run's start"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no release; the fake was sent %v; stderr is %q", srv.Routes(), stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var released int
	for _, d := range srv.Dispatches() {
		if r := d.Release; r != nil {
			released++
			if r.Reason != "this pool creates no machine of the type in the claim" {
				t.Errorf("the claim was released %+v", r)
			}
		}
	}
	if released != 1 {
		t.Errorf("%d dispatches were released, want 1", released)
	}
	for _, c := range srv.Calls() {
		if !c.Verified || c.Header.Get(accesskey.HeaderInstanceID) != provisionertest.InstanceID {
			t.Errorf("%s was not signed as the pool's instance", c.Route)
		}
	}
	if out := stderr.String(); !strings.Contains(out, `msg="claimed a run"`) || accesskey.ContainsSecret(out) || strings.Contains(strings.ToLower(out), accesskey.CodePrefixRun) {
		t.Errorf("stderr is %q", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if viaCalls != 0 {
		t.Errorf("the gateway was sent %d requests", viaCalls)
	}
}

// unreachedProbe is a docker program whose run of the pool's check of the cloud
// metadata service exits 0, as when neither address connects.
func unreachedProbe(t *testing.T) string {
	t.Helper()
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return docker
}

// agentImageOf is an agent's image pinned by its digest.
const agentImageOf = "registry.example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"
