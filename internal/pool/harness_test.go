package pool_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The fake qory's script and reports are in the runs' configuration directory, the one
// folder of the test's that its allow-listed environment contains.
const scriptFile = "fake.json"

// script is what the fake qory run does.
type script struct {
	// Exit is the status it exits with.
	Exit int
	// Block makes it wait, once it wrote ready-<dispatch id>, for SIGTERM, then exit 1,
	// as qory run does, or for the file go-<dispatch id>, then exit Exit.
	Block bool
	// Quiet makes it print nothing. Otherwise it prints hello, the run code it read,
	// each line of Say with each ~ printed as _, so the script's file contains no
	// secret's start, then the lines of [bulkOutput] Bulk.
	Quiet bool
	Say   []string
	Bulk  int
	// Compose makes it write the file harness.md into a home under its --home, named
	// as qory run names a checkout's home, as a compose does.
	Compose bool
}

// bulkOutput is n lines of 100 characters and a line feed each, numbered from 0, then
// the line "last line".
func bulkOutput(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %06d %s\n", i, strings.Repeat("x", 88))
	}
	b.WriteString("last line\n")
	return b.String()
}

// report is what the fake qory saw, written to report-<dispatch id>.json. It contains
// the SHA-256 of the run code, never the code.
type report struct {
	Env  []string
	Args []string
	Dir  string
	// DirEntries is how many entries its folder had when it started.
	DirEntries int
	// HomeMode is the mode of the folder its HOME names, 0 when there is none.
	HomeMode os.FileMode
	// FD3File reports whether descriptor 3 was a regular file, mode 0600, that no name
	// links to, open for reading alone.
	FD3File bool
	CodeSum string
}

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "run" && os.Args[2] == "--rm" {
		os.Exit(fakeProbe())
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		os.Exit(fakeQory())
	}
	os.Exit(m.Run())
}

// The fake docker's script for the check of the cloud metadata service, and its report,
// in the runs' configuration directory.
const (
	probeFile   = "probe.json"
	probeReport = "report-probe.json"
)

// probeScript is what the fake docker run of the check does: it prints Say, line by
// line, and exits Exit. With no script, it prints nothing and exits 0.
type probeScript struct {
	Exit int
	Say  []string
}

// fakeProbe is docker run --rm of the check, as the script in $XDG_CONFIG_HOME sets it.
// It writes its arguments and environment to [probeReport], as [report] has them.
func fakeProbe() int {
	config := os.Getenv("XDG_CONFIG_HOME")
	rep := report{Env: os.Environ(), Args: os.Args[1:]}
	if info, err := os.Stat(os.Getenv("HOME")); err == nil && info.IsDir() {
		rep.HomeMode = info.Mode().Perm()
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(filepath.Join(config, probeReport), out, 0o600); err != nil {
		return 92
	}
	var s probeScript
	if b, err := os.ReadFile(filepath.Join(config, probeFile)); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return 91
		}
	}
	for _, line := range s.Say {
		fmt.Println(line)
	}
	return s.Exit
}

// probeImage is the agent's image the harness's check runs, pinned by its digest.
const probeImage = "registry.example.com/agent@sha256:2222222222222222222222222222222222222222222222222222222222222222"

// fakeQory is qory run, as the script in $XDG_CONFIG_HOME sets it. It prints the run
// code it read, which the pool's log must leave out.
func fakeQory() int {
	var st syscall.Stat_t
	fd3 := syscall.Fstat(child.KeyFD, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Nlink == 0 && st.Mode&0o777 == 0o600
	config := os.Getenv("XDG_CONFIG_HOME")
	b, err := os.ReadFile(filepath.Join(config, scriptFile))
	if err != nil {
		return 90
	}
	var s script
	if err := json.Unmarshal(b, &s); err != nil {
		return 91
	}
	cwd, _ := os.Getwd()
	entries, _ := os.ReadDir(cwd)
	id := filepath.Base(filepath.Dir(cwd))
	rep := report{Env: os.Environ(), Args: os.Args[1:], Dir: cwd, DirEntries: len(entries)}
	if info, err := os.Stat(os.Getenv("HOME")); err == nil && info.IsDir() {
		rep.HomeMode = info.Mode().Perm()
	}
	var code []byte
	if fd3 {
		f := os.NewFile(uintptr(child.KeyFD), "code")
		_, werr := f.Write([]byte("x"))
		code, _ = io.ReadAll(f)
		rep.FD3File = werr != nil
		rep.CodeSum = sum(code)
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(filepath.Join(config, "report-"+id+".json"), out, 0o600); err != nil {
		return 92
	}
	if s.Compose {
		if err := composeHome(os.Args[1:], cwd); err != nil {
			return 93
		}
	}
	if !s.Quiet {
		fmt.Println("hello")
		fmt.Println("redeeming " + string(code))
		for _, line := range s.Say {
			fmt.Println(strings.ReplaceAll(line, "~", "_"))
		}
		if s.Bulk > 0 {
			fmt.Print(bulkOutput(s.Bulk))
		}
	}
	if !s.Block {
		return s.Exit
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)
	_ = os.WriteFile(filepath.Join(config, "ready-"+id), nil, 0o600)
	for {
		select {
		case <-c:
			return 1
		case <-time.After(10 * time.Millisecond):
		}
		if _, err := os.Stat(filepath.Join(config, "go-"+id)); err == nil {
			return s.Exit
		}
	}
}

// composeHome writes harness.md into <--home>/<checkout>-x, the home of the checkout at
// root under the --home of args, as a compose of qory run does.
func composeHome(args []string, root string) error {
	i := slices.Index(args, "--home")
	if i < 0 || i+1 >= len(args) {
		return errors.New("no --home")
	}
	home := filepath.Join(args[i+1], filepath.Base(root)+"-x")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "harness.md"), []byte("composed\n"), 0o600)
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
	for line := range strings.SplitSeq(strings.TrimSpace(l.buf.String()), "\n") {
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
	var once sync.Once
	let := func() { once.Do(func() { close(ch) }) }
	g.mu.Lock()
	g.hold[suffix] = ch
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

// The pool's waits that the harness keeps waiting until the test lets one go: the
// heartbeat's, and the claim's max_lifetime_minutes of
// [provisionertest.MaxLifetimeMinutes].
const (
	heartbeat = 60 * time.Second
	lifetime  = provisionertest.MaxLifetimeMinutes * time.Minute
)

// harness is one pool and its world: the fake Qory Apiary, the clock, the gate, the
// log, and the fake qory run, the test binary, whose script the test sets.
type harness struct {
	t         *testing.T
	srv       *provisionertest.Server
	clk       *clock
	gate      *gate
	log       *lines
	root      string
	pool      *pool.Pool
	state     string
	runConfig string

	// beats lets one wait of a heartbeat go, and lifetimes one wait of the lifetime.
	// With freeBeats, a wait of a minute goes at once, as the claim's back-off is.
	beats, lifetimes chan struct{}
	freeBeats        bool

	mu    sync.Mutex
	waits []time.Duration
	// onSleep, when set, is the pool's wait of any other length, after the clock moved
	// on.
	onSleep func(ctx context.Context, d time.Duration) error

	cancel context.CancelFunc
	done   chan error
	runErr error
}

// newHarness is a pool of runs runs, not started, whose fake qory exits 0.
func newHarness(t *testing.T, runs int) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, srv: provisionertest.New(t), clk: &clock{}, gate: newGate(), log: &lines{}, root: root,
		beats: make(chan struct{}), lifetimes: make(chan struct{}),
	}
	h.srv.Now = h.clk.Now
	h.state = filepath.Join(root, "qory", "pool")
	h.runConfig = filepath.Join(root, "runconfig")
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
		Settings: pool.Settings{Runs: runs, State: h.state, RunConfig: h.runConfig, Qory: os.Args[0], Grace: 2 * time.Second,
			Probe: pool.Probe{Docker: os.Args[0], Image: probeImage, User: "1000:1000"}},
		Client: c,
		Log:    log,
		Now:    h.clk.Now,
		Sleep:  h.sleep,
	}
	if err := os.MkdirAll(h.runConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	h.script(script{})
	t.Cleanup(func() { h.noLeak() })
	return h
}

// sleep is the pool's wait: a heartbeat's or the lifetime's waits for the test, any
// other moves the clock on and returns.
func (h *harness) sleep(ctx context.Context, d time.Duration) error {
	h.mu.Lock()
	h.waits = append(h.waits, d)
	on, free := h.onSleep, h.freeBeats
	h.mu.Unlock()
	var let chan struct{}
	switch {
	case d == heartbeat && !free:
		let = h.beats
	case d == lifetime:
		let = h.lifetimes
	}
	if let != nil {
		select {
		case <-let:
			h.clk.advance(d)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.clk.advance(d)
	if on != nil {
		return on(ctx, d)
	}
	return ctx.Err()
}

// beat lets one heartbeat's wait go, once the pool waits on one.
func (h *harness) beat() {
	h.t.Helper()
	select {
	case h.beats <- struct{}{}:
	case <-time.After(20 * time.Second):
		h.t.Fatal("the pool sent no heartbeat")
	}
}

// setSleep sets onSleep.
func (h *harness) setSleep(f func(ctx context.Context, d time.Duration) error) {
	h.mu.Lock()
	h.onSleep = f
	h.mu.Unlock()
}

// probe sets what the fake docker run of the check does.
func (h *harness) probe(s probeScript) {
	h.t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(h.runConfig, probeFile), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// script sets what the fake qory run does.
func (h *harness) script(s script) {
	h.t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(h.runConfig, scriptFile), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// ready waits until the fake qory run of the dispatch id blocks.
func (h *harness) ready(id string) {
	h.t.Helper()
	h.waitFor("qory run of "+id, func() bool {
		_, err := os.Stat(filepath.Join(h.runConfig, "ready-"+id))
		return err == nil
	})
}

// letGo ends the blocked fake qory run of the dispatch id with the script's Exit.
func (h *harness) letGo(id string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.runConfig, "go-"+id), nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// reportOf is what the fake qory run of the dispatch id saw; ok is false when it did
// not run.
func (h *harness) reportOf(id string) (report, bool) {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.runConfig, "report-"+id+".json"))
	if err != nil {
		return report{}, false
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		h.t.Fatal(err)
	}
	return r, true
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
			h.t.Fatalf("%s never happened; the fake was sent %v; the log:\n%s", what, h.srv.Routes(), h.log.text())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// overLines are the lines after which the pool does nothing more for a dispatch but
// take it out of the journal.
var overLines = []string{
	"qory run for the dispatch ended", "the run could not be started", "released the run",
	"the dispatch was over already, so nothing was released", "the run could not be released",
	"Qory Apiary did not record the run's start",
}

// settled waits until the fake made n dispatches, the pool logged one of [overLines]
// for each, and the journal was written and is empty again.
func (h *harness) settled(n int) {
	h.t.Helper()
	h.waitFor("the dispatches' end", func() bool {
		ds := h.srv.Dispatches()
		if len(ds) < n {
			return false
		}
		for _, d := range ds {
			if !h.over(d.ID) {
				return false
			}
		}
		b, err := os.ReadFile(filepath.Join(h.state, pool.JournalFile))
		return err == nil && len(b) == 0
	})
}

// over reports whether the pool logged one of [overLines] for the dispatch id.
func (h *harness) over(id string) bool {
	return slices.ContainsFunc(h.log.all(), func(m map[string]any) bool {
		return m["dispatch_id"] == id && slices.Contains(overLines, m["msg"].(string))
	})
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

// released checks that the last release sent for the dispatch was for reason.
func (h *harness) released(d provisionertest.Dispatch, reason string) {
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
		h.t.Fatalf("%s was not released; the fake was sent %v", d.ID, h.srv.Routes())
	}
	if rel.Reason != reason {
		h.t.Errorf("%s was released for %q, want %q", d.ID, rel.Reason, reason)
	}
}

// notReleased checks that no release was sent.
func (h *harness) notReleased() {
	h.t.Helper()
	if n := h.count(provisionertest.RouteRelease); n != 0 {
		h.t.Errorf("%d releases were sent; the fake was sent %v", n, h.srv.Routes())
	}
}

// noLeak fails the test when a run code the fake handed out, the pool's access key
// secret, or what starts any secret, qecr_ among them, is in a log line, Run's error, a
// file under the test's folders, a fake qory's report: its arguments and environment,
// or the body of a request the fake got, a machine log among them.
func (h *harness) noLeak() {
	t := h.t
	texts := []string{h.log.text()}
	for _, c := range h.srv.Calls() {
		texts = append(texts, string(c.Body))
	}
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
	var secrets []string
	for _, d := range h.srv.Dispatches() {
		secrets = append(secrets, d.Run.RunCode, d.Run.RunCode[5:20])
	}
	secrets = append(secrets, h.srv.Pool.Secret(), h.srv.Pool.Secret()[4:])
	for _, text := range texts {
		if child.ContainsSecret(text) {
			t.Errorf("what starts a secret was written: %.300q", text)
		}
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("a secret was written: %.300q", text)
			}
		}
	}
}

// journalOf is the journal's lines in the pool's folder state.
func journalOf(t *testing.T, state string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, pool.JournalFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}
