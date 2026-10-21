package pool_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The fake qory's script and reports are in the runs' configuration directory, the one
// folder of the test's its allow-listed environment names.
const (
	scriptFile = "fake.json"
	readyFile  = "ready"
)

// script is what the fake qory run does.
type script struct {
	// RunID is the run whose record qory run makes, with Record.
	RunID string
	// KeySum is the SHA-256 of the key qory run expects on descriptor 3.
	KeySum string
	Exit   int
	Record bool
	// Mode "block" waits for SIGTERM and exits 1, as qory run does.
	Mode string
}

// report is what the fake qory saw, written to report-<verb>.json.
type report struct {
	Env   []string
	Args  []string
	KeyOK bool
}

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "init" {
		if err := os.MkdirAll(filepath.Join(os.Args[len(os.Args)-1], ".git"), 0o700); err != nil {
			os.Exit(96)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && (os.Args[1] == "harness" || os.Args[1] == "run") {
		os.Exit(fakeQory())
	}
	os.Exit(m.Run())
}

// fakeQory is qory harness compose or qory run, as the script in $XDG_CONFIG_HOME says.
func fakeQory() int {
	var st syscall.Stat_t
	fd3 := syscall.Fstat(child.KeyFD, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFIFO
	config := os.Getenv("XDG_CONFIG_HOME")
	b, err := os.ReadFile(filepath.Join(config, scriptFile))
	if err != nil {
		return 90
	}
	var s script
	if err := json.Unmarshal(b, &s); err != nil {
		return 91
	}
	verb := os.Args[1]
	rep := report{Env: os.Environ(), Args: os.Args[1:]}
	if verb == "harness" {
		if i := slices.Index(os.Args, "--home"); i > 0 {
			_ = os.MkdirAll(filepath.Join(os.Args[i+1], "claude"), 0o700)
		}
	} else {
		if fd3 {
			k, _ := io.ReadAll(os.NewFile(uintptr(child.KeyFD), "key"))
			rep.KeyOK = sum(k) == s.KeySum
		}
		if s.Record {
			cwd, _ := os.Getwd()
			real, _ := filepath.EvalSymlinks(cwd)
			record := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runs", filepath.Base(real)+"-"+sum([]byte(real))[:12], s.RunID)
			if err := os.MkdirAll(record, 0o700); err != nil {
				return 94
			}
		}
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(filepath.Join(config, "report-"+verb+".json"), out, 0o600); err != nil {
		return 92
	}
	if verb == "run" && s.Mode == "block" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		_ = os.WriteFile(filepath.Join(config, readyFile), nil, 0o600)
		<-c
		return 1
	}
	if verb == "run" {
		return s.Exit
	}
	return 0
}

// sum is the SHA-256 of b, in hex.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// clock is the test's clock: the time, moved on by every wait the pool and its client
// take, which return at once.
type clock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

// lines is a log the pool writes to, as JSON lines.
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// all is each line logged, decoded.
func (l *lines) all() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func (l *lines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// with is the lines logged with the message msg.
func (l *lines) with(msg string) []map[string]any {
	var out []map[string]any
	for _, m := range l.all() {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// gate stands between the pool and the fake: it holds a request whose path ends in a
// suffix until the test lets it go, and once shut fails every request with no answer.
type gate struct {
	mu      sync.Mutex
	hold    map[string]chan struct{}
	held    []func()
	reached chan string
	shut    bool
}

func newGate() *gate { return &gate{hold: map[string]chan struct{}{}, reached: make(chan string, 16)} }

// holdNext holds the next request whose path ends in suffix, and returns what lets it
// go.
func (g *gate) holdNext(suffix string) (release func()) {
	ch := make(chan struct{})
	g.mu.Lock()
	g.hold[suffix] = ch
	g.mu.Unlock()
	var once sync.Once
	let := func() { once.Do(func() { close(ch) }) }
	g.mu.Lock()
	g.held = append(g.held, let)
	g.mu.Unlock()
	return let
}

// close fails every request from now on, those held among them.
func (g *gate) close() {
	g.mu.Lock()
	g.shut = true
	held := g.held
	g.mu.Unlock()
	for _, let := range held {
		let()
	}
}

func (g *gate) isShut() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shut
}

func (g *gate) RoundTrip(r *http.Request) (*http.Response, error) {
	g.mu.Lock()
	shut := g.shut
	var ch chan struct{}
	for suffix, c := range g.hold {
		if strings.HasSuffix(r.URL.Path, suffix) {
			ch = c
			delete(g.hold, suffix)
			break
		}
	}
	g.mu.Unlock()
	if shut {
		return nil, errors.New("no answer")
	}
	if ch != nil {
		g.reached <- r.URL.Path
		select {
		case <-ch:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		if g.isShut() {
			return nil, errors.New("no answer")
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// launch is one explore run the fake launcher was given: its id, the SHA-256 of its
// key, and whether its holder was cleared once it returned.
type launch struct {
	RunID   string
	KeySum  string
	Inputs  string
	Cleared bool
}

// harness is one pool and its world: the fake Qory Apiary, the clock, the gate, the
// log, and a fake launcher of explore runs whose Ending the test sets.
type harness struct {
	t     *testing.T
	srv   *provisionertest.Server
	clk   *clock
	gate  *gate
	log   *lines
	root  string
	pool  *pool.Pool
	state string

	mu       sync.Mutex
	waits    []time.Duration
	launches []launch
	// onLaunch, when set, is what the fake launcher does once it took the key: it
	// returns the run's Result and error. Unset, the run succeeds.
	onLaunch func(ctx context.Context, r explore.Run) (explore.Result, error)
	// onSleep, when set, is the pool's wait, after the clock moved on.
	onSleep func(ctx context.Context, d time.Duration) error

	cancel context.CancelFunc
	done   chan error
	runErr error
}

// newHarness is a pool of runs runs and prebuilds builds, not started.
func newHarness(t *testing.T, runs, prebuilds int) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, srv: provisionertest.New(t), clk: &clock{}, gate: newGate(), log: &lines{}, root: root}
	h.srv.Now = h.clk.Now
	h.state = filepath.Join(root, "qory", "pool")
	c := h.srv.Client()
	c.Now = h.clk.Now
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.clk.advance(d)
		return nil
	}
	c.HTTP = &http.Client{Transport: h.gate}
	log := slog.New(slog.NewJSONHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.Log = log
	h.pool = &pool.Pool{
		Settings: pool.Settings{
			Runs: runs, Prebuilds: prebuilds, State: h.state,
			Explore: explore.Settings{State: filepath.Join(root, "qory"), HarnessRef: testRef, RunConfig: filepath.Join(root, "runconfig"), Qory: os.Args[0], Git: os.Args[0], Grace: 2 * time.Second},
		},
		Client: c,
		Outcomes: &server.Client{
			Config: &server.Config{Version: 1, URL: h.srv.URL, AccessKeyID: provisionertest.KeyID, ApiaryPublicKey: h.srv.Pin()},
			Key:    h.srv.Pool, InstanceID: provisionertest.InstanceID, UserAgent: "qory-test", HTTP: &http.Client{Transport: h.gate},
		},
		Explore: h.explore,
		Prebuild: func(ctx context.Context, d pool.Prebuild) explore.Ending {
			d.Key.Clear()
			return explore.Ending{Post: true, State: explore.StateSucceeded}
		},
		Log: log,
		Now: h.clk.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.mu.Lock()
			h.waits = append(h.waits, d)
			sleep := h.onSleep
			h.mu.Unlock()
			h.clk.advance(d)
			if sleep != nil {
				return sleep(ctx, d)
			}
			return ctx.Err()
		},
	}
	if err := os.MkdirAll(h.pool.Settings.Explore.RunConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.noLeak() })
	return h
}

// explore is the fake launcher: it records the run, makes its run directory, takes the
// key and runs onLaunch.
func (h *harness) explore(ctx context.Context, s explore.Settings, r explore.Run) (explore.Result, error) {
	dir := explore.Dir(s.State, r.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Error(err)
	}
	l := launch{RunID: r.RunID, KeySum: sum(r.Key.Bytes()), Inputs: string(r.Inputs)}
	h.mu.Lock()
	on := h.onLaunch
	h.mu.Unlock()
	res := explore.Result{Dir: dir, Record: true, Ending: explore.EndingOf(true, child.Result{})}
	var err error
	if on != nil {
		res, err = on(ctx, r)
	}
	r.Key.Clear()
	l.Cleared = len(r.Key.Bytes()) == 0
	h.mu.Lock()
	h.launches = append(h.launches, l)
	h.mu.Unlock()
	return res, err
}

// setLaunch sets onLaunch.
func (h *harness) setLaunch(f func(ctx context.Context, r explore.Run) (explore.Result, error)) {
	h.mu.Lock()
	h.onLaunch = f
	h.mu.Unlock()
}

// setSleep sets onSleep.
func (h *harness) setSleep(f func(ctx context.Context, d time.Duration) error) {
	h.mu.Lock()
	h.onSleep = f
	h.mu.Unlock()
}

// ending is a fake launcher that ends the run with record and r.
func ending(record bool, r child.Result, err error) func(context.Context, explore.Run) (explore.Result, error) {
	return func(context.Context, explore.Run) (explore.Result, error) {
		return explore.Result{Record: record, Run: r, Ending: explore.EndingOf(record, r)}, err
	}
}

// start runs the pool until stop.
func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- h.pool.Run(ctx) }()
	h.t.Cleanup(func() {
		if h.cancel != nil {
			h.stop()
		}
	})
}

// stop stops the pool, as a signal does, and returns Run's error once it returned.
func (h *harness) stop() error {
	h.t.Helper()
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		h.runErr = err
		return err
	case <-time.After(30 * time.Second):
		h.t.Fatal("the pool did not stop")
	}
	return nil
}

// waitFor waits until cond holds.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s never happened; the fake was sent %v", what, h.srv.Routes())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settled waits until the fake made n dispatches, each is over, and the journal was
// written and is empty again. A dispatch is over once the fake ended it or recorded its
// run's outcome, or the pool logged it dropped, not released or its outcome not posted.
func (h *harness) settled(n int) {
	h.t.Helper()
	h.waitFor("the dispatches' end", func() bool {
		ds := h.srv.Dispatches()
		if len(ds) < n {
			return false
		}
		for _, d := range ds {
			if !h.over(d) {
				return false
			}
		}
		b, err := os.ReadFile(filepath.Join(h.state, pool.JournalFile))
		return err == nil && len(b) == 0
	})
}

// over reports whether the dispatch d is over, as [harness.settled] says.
func (h *harness) over(d provisionertest.Dispatch) bool {
	if d.State == provisionertest.StateEnded {
		return true
	}
	if _, ok := h.outcome(d.Run.RunID); ok {
		return true
	}
	for _, m := range h.log.all() {
		if m["dispatch_id"] != d.ID {
			continue
		}
		switch m["msg"] {
		case "the dispatch was over already, so nothing was released", "the handover was refused, so nothing was released", "Qory Apiary did not record the run's start",
			"the run could not be released", "the run's outcome could not be posted":
			return true
		}
	}
	return false
}

// only is the one dispatch the fake made.
func (h *harness) only() provisionertest.Dispatch {
	h.t.Helper()
	ds := h.srv.Dispatches()
	if len(ds) != 1 {
		h.t.Fatalf("the fake made %d dispatches, want 1", len(ds))
	}
	return ds[0]
}

// count is how many requests of the route the fake got.
func (h *harness) count(route provisionertest.Route) int {
	n := 0
	for _, r := range h.srv.Routes() {
		if r == route {
			n++
		}
	}
	return n
}

// waitsOf is the pool's waits.
func (h *harness) waitsOf() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.waits)
}

func (h *harness) launched() []launch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.launches)
}

// released checks that the last release sent for the dispatch was for reason with
// retry.
func (h *harness) released(d provisionertest.Dispatch, reason string, retry bool) {
	h.t.Helper()
	var rel *contracts.Release
	for _, c := range h.srv.Calls() {
		if c.Route == provisionertest.RouteRelease && c.DispatchID == d.ID {
			rel = &contracts.Release{}
			if err := json.Unmarshal(c.Body, rel); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	if rel == nil {
		h.t.Fatalf("%s was not released; the fake was sent %v", d, h.srv.Routes())
	}
	if rel.Reason != reason || rel.Retry != retry {
		h.t.Errorf("%s was released for %q, retry %v; want %q, retry %v", d, rel.Reason, rel.Retry, reason, retry)
	}
}

// notReleased checks that no release was sent.
func (h *harness) notReleased() {
	h.t.Helper()
	if n := h.count(provisionertest.RouteRelease); n != 0 {
		h.t.Errorf("%d releases were sent; the fake was sent %v", n, h.srv.Routes())
	}
}

// outcome is the outcome the fake recorded for the run, ok false with none.
func (h *harness) outcome(runID string) (provisionertest.Outcome, bool) {
	o, ok := h.srv.Outcomes()[runID]
	return o, ok
}

// noLeak fails the test when a run key the fake handed out, the pool's access key
// secret, or what starts either, is in a log line, Run's error, a file under the
// test's folders, or a fake qory's arguments or environment.
func (h *harness) noLeak() {
	t := h.t
	texts := []string{h.log.text()}
	if h.runErr != nil {
		texts = append(texts, h.runErr.Error())
	}
	_ = filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		texts = append(texts, path)
		if b, err := os.ReadFile(path); err == nil {
			texts = append(texts, string(b))
		}
		return nil
	})
	var keys []string
	for _, d := range h.srv.Dispatches() {
		keys = append(keys, d.Run.RunKey)
	}
	keys = append(keys, h.srv.Pool.Secret())
	for _, text := range texts {
		if accesskey.ContainsSecret(text) {
			t.Errorf("what starts a secret was written: %.300q", text)
		}
		for _, k := range keys {
			if strings.Contains(text, k) || strings.Contains(text, k[4:]) {
				t.Errorf("a secret was written: %.300q", text)
			}
		}
	}
}

// The test's ids and values.
const (
	testRef    = "708c43c0c0ffee0000000000000000000000beef"
	testKey    = "qrk_eyJ0ZXN0IjoidGhlIHBvb2wncyBsb29wIn0.c2lnbmF0dXJlLW9mLW5vdGhpbmc"
	runA       = "0192f0c1-7d4e-7a2b-8c3d-00000000000a"
	runB       = "0192f0c1-7d4e-7a2b-8c3d-00000000000b"
	runC       = "0192f0c1-7d4e-7a2b-8c3d-00000000000c"
	otherStep  = "review"
	retryAfter = "7"
)

// exploreRun and prebuildRun are a run of each step, of the id.
func exploreRun(id string) provisionertest.Run {
	return provisionertest.Run{RunID: id, Step: provisioner.StepExplore}
}

func prebuildRun(id string) provisionertest.Run {
	return provisionertest.Run{RunID: id, Step: provisioner.StepPrebuild}
}
