package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// State is where a suite is: running, or how it ended. Failed, Timeout and Stopped are
// red; a suite that ran no test is Failed.
type State string

// The states of a suite.
const (
	Running State = "running"
	Passed  State = "passed"
	Failed  State = "failed"
	Timeout State = "timeout"
	Stopped State = "stopped"
)

// Red reports whether s is an end that counts against the work: any but Running and Passed.
func (s State) Red() bool { return s == Failed || s == Timeout || s == Stopped }

// MaxOutput is the most bytes of a suite's output that a [Snapshot] has: the last ones.
const MaxOutput = 64 << 10

// Snapshot is a suite as a request reads it. Counts is nil when the suite's result file
// could not be read, and Minutes is the time the suite has run or ran, in whole minutes,
// at least 1.
type Snapshot struct {
	ID      string
	Commit  string
	State   State
	Counts  *Counts
	Minutes int
	Output  string
}

// Job is one suite for a [Runner]: the files of Commit are in Dir, and the command of
// Full runs in the top folder of Dir. The suite ends when the context of the call ends.
type Job struct {
	ID     string
	Commit string
	Dir    string
	Full   Full
}

// Runner runs job, writing what the suite prints to out, and returns the command's exit
// status. It returns only after every process of the suite is gone, so that nothing
// writes Dir once it returned. An error is a suite that did not start, or one that the
// call's context ended.
type Runner func(ctx context.Context, job Job, out io.Writer) (exit int, err error)

// Config is what a [Registry] needs.
type Config struct {
	// Source is the checkout that the commits are cloned from, and Root the folder that
	// contains the clones, one folder for each suite. Root exists, and only the container of
	// a suite mounts the folder of that suite.
	Source, Root string
	// Full is the command of the suite, and Limit the time it may take.
	Full  Full
	Limit time.Duration
	// Clone is [Clone] when nil.
	Clone func(ctx context.Context, src, commit, dest string) error
	Run   Runner
	// Mask replaces the secrets of the run in the output of a suite; nil leaves it as it is.
	Mask func(string) string
	// Now is time.Now when nil.
	Now func() time.Time
}

// The causes that end a suite before its command does.
var (
	errCap        = errors.New("the suite reached its time limit")
	errSuperseded = errors.New("a newer commit's suite replaced this one")
	errStopped    = errors.New("the suite was stopped")
)

// Registry starts the suites of one run and keeps their states. A suite outlives the
// request that started it, and ends with the run's context, with [Registry.Stop], or at
// its time limit.
type Registry struct {
	cfg Config
	ctx context.Context

	// starting serves one start at a time, so that two requests for one commit start one
	// suite.
	starting sync.Mutex
	mu       sync.Mutex
	byID     map[string]*entry
	byCommit map[string]*entry
	newest   *entry
	wg       sync.WaitGroup
}

// entry is one suite of a registry.
type entry struct {
	id, commit, dir string
	started         time.Time
	cancel          context.CancelCauseFunc
	done            chan struct{}
	out             *tail

	// state, counts and ended are set once, when the suite ends, under the registry's mu.
	state  State
	counts *Counts
	ended  time.Time
}

// New returns a registry whose suites end when ctx ends.
func New(ctx context.Context, cfg Config) *Registry {
	if cfg.Clone == nil {
		cfg.Clone = Clone
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Registry{cfg: cfg, ctx: ctx, byID: map[string]*entry{}, byCommit: map[string]*entry{}}
}

// Start starts the suite of commit and returns it at once, with created true. A suite of
// the same commit that exists returns as it is, with created false. A newer commit's suite
// stops every suite that still runs. The commit is cloned before Start returns, so no later
// write to the source changes the suite's files; an error is a commit that the clone
// could not take, and starts nothing.
func (r *Registry) Start(ctx context.Context, commit string) (snap Snapshot, created bool, err error) {
	r.starting.Lock()
	defer r.starting.Unlock()
	r.mu.Lock()
	if old, ok := r.byCommit[commit]; ok {
		snap = r.snapshot(old)
		r.mu.Unlock()
		return snap, false, nil
	}
	r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	id, err := newID()
	if err != nil {
		return Snapshot{}, false, err
	}
	dir := filepath.Join(r.cfg.Root, id)
	if err := r.cfg.Clone(ctx, r.cfg.Source, commit, dir); err != nil {
		return Snapshot{}, false, err
	}
	e := &entry{id: id, commit: commit, dir: dir, started: r.cfg.Now(), done: make(chan struct{}), out: &tail{max: MaxOutput}, state: Running}
	runCtx, cancel := context.WithCancelCause(r.ctx)
	e.cancel = cancel
	r.mu.Lock()
	for _, other := range r.byID {
		if other.state == Running {
			other.cancel(errSuperseded)
		}
	}
	r.byID[id], r.byCommit[commit], r.newest = e, e, e
	snap = r.snapshot(e)
	r.mu.Unlock()
	r.wg.Add(1)
	go r.run(runCtx, e)
	return snap, true, nil
}

// run runs e until its command ends or runCtx does, and sets its end.
func (r *Registry) run(ctx context.Context, e *entry) {
	defer r.wg.Done()
	timer := time.AfterFunc(r.cfg.Limit, func() { e.cancel(errCap) })
	before, _ := ReadResult(e.dir, r.cfg.Full.ResultFile)
	exit, err := r.cfg.Run(ctx, Job{ID: e.id, Commit: e.commit, Dir: e.dir, Full: r.cfg.Full}, e.out)
	timer.Stop()
	cause := context.Cause(ctx)
	state := Failed
	var counts *Counts
	switch {
	case cause != nil && errors.Is(cause, errCap):
		state = Timeout
	case cause != nil:
		state = Stopped
	case err != nil:
		fmt.Fprintf(e.out, "\nqory could not run the suite: %v\n", err)
	case exit != 0:
		fmt.Fprintf(e.out, "\nThe suite's command exited with status %d.\n", exit)
	default:
		state, counts = r.judge(e, before)
	}
	r.mu.Lock()
	e.state, e.counts, e.ended = state, counts, r.cfg.Now()
	r.mu.Unlock()
	e.cancel(nil)
	close(e.done)
}

// judge is the state and the counts of a suite whose command exited with 0. The counts
// come from the result file when the command wrote it anew; with no readable file they
// are nil, and the exit status decides. A file that counts no test, or a failed one, or
// whose totals do not match its testcases, fails the suite.
func (r *Registry) judge(e *entry, before Stamp) (State, *Counts) {
	if r.cfg.Full.ResultFile == "" {
		return Passed, nil
	}
	after, data := ReadResult(e.dir, r.cfg.Full.ResultFile)
	if !Fresh(before, after) {
		return Passed, nil
	}
	counts, err := ParseJUnit(data)
	switch {
	case errors.Is(err, ErrMismatch):
		fmt.Fprintf(e.out, "\nThe result file %s has totals that do not match its testcases.\n", r.cfg.Full.ResultFile)
		return Failed, nil
	case err != nil:
		return Passed, nil
	case counts.Tests == 0:
		fmt.Fprintf(e.out, "\nThe suite ran 0 tests, so it proves nothing: add tests.\n")
		return Failed, &counts
	case counts.Failed > 0:
		return Failed, &counts
	}
	return Passed, &counts
}

// snapshot is e as a request reads it. The caller holds r.mu.
func (r *Registry) snapshot(e *entry) Snapshot {
	end := e.ended
	if e.state == Running {
		end = r.cfg.Now()
	}
	minutes := max(1, int((end.Sub(e.started)+30*time.Second)/time.Minute))
	if e.state == Timeout {
		minutes = int(r.cfg.Limit / time.Minute)
	}
	output := e.out.String()
	if r.cfg.Mask != nil {
		output = r.cfg.Mask(output)
	}
	return Snapshot{ID: e.id, Commit: e.commit, State: e.state, Counts: e.counts, Minutes: minutes, Output: output}
}

// Get is the suite with id, false for an id that no suite of the run has.
func (r *Registry) Get(id string) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return Snapshot{}, false
	}
	return r.snapshot(e), true
}

// Newest is the suite that the last Start that created one returned, false when none
// started.
func (r *Registry) Newest() (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.newest == nil {
		return Snapshot{}, false
	}
	return r.snapshot(r.newest), true
}

// Wait returns the newest suite once it ended, or ctx's error when ctx ends first; false
// when no suite started.
func (r *Registry) Wait(ctx context.Context) (Snapshot, bool, error) {
	r.mu.Lock()
	e := r.newest
	r.mu.Unlock()
	if e == nil {
		return Snapshot{}, false, nil
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return Snapshot{}, true, ctx.Err()
	}
	snap, _ := r.Get(e.id)
	return snap, true, nil
}

// Stop ends every suite that runs, as a stop of the run does, and returns once each
// runner returned, so no process of a suite is left.
func (r *Registry) Stop() {
	r.mu.Lock()
	for _, e := range r.byID {
		if e.state == Running {
			e.cancel(errStopped)
		}
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// Close stops every suite and removes the clones.
func (r *Registry) Close() error {
	r.Stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, e := range r.byID {
		errs = append(errs, removeAll(e.dir))
	}
	return errors.Join(errs...)
}

// removeAll removes dir, also when a folder in it has no write permission.
func removeAll(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// newID is a new suite id: su_ and 8 hexadecimal digits.
func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "su_" + hex.EncodeToString(b[:]), nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
	cut bool
}

// Write keeps the last bytes of p and of what came before it.
func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
		t.cut = true
	}
	return len(p), nil
}

// String is the kept bytes from the first whole UTF-8 character on.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buf
	if t.cut {
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	return string(b)
}
