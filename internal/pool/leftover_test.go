package pool_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The lines of a qory run left from before the pool started.
const (
	lineLeftoverStopped = "a qory run of the dispatch is left from before the pool started, so the pool stops it"
	lineLeftoverHeld    = "stop the programs that hold run.lock in the dispatch's folder, then restart the pool: a qory run left from before the pool started still holds that lock, so the pool keeps the dispatch for its next start and reports no gone"
	// lockNotTriedEnd is the end of the line of a lock that could not be tried, after
	// the error.
	lockNotTriedEnd = "), so it keeps the dispatch for its next start and reports no gone"
	lineReapFailed  = "remove the containers and networks with the label dev.qory.run=<run_id> from the container engine: the removal of the wall of a qory run left from before the pool started failed; the pool reports the machine as gone all the same"
)

// leftoverPoll is the pool's wait between two tries of a left run's lock.
const leftoverPoll = 100 * time.Millisecond

// leftRunID is the run id of the record a left qory run made.
const leftRunID = "0b5c1f6e-3d2a-4c8b-9e7f-1a2b3c4d5e6f"

// leftFolder makes the folder of the dispatch id as a pool that crashed while its qory
// run ran leaves it: work/, the run's record under state/, and run.lock. It returns the
// lock file's path.
func leftFolder(t *testing.T, h *harness, id string) string {
	t.Helper()
	dir := filepath.Join(h.state, "runs", id)
	for _, d := range []string{filepath.Join(dir, "work"), filepath.Join(dir, "state", "qory", "runs", "work-0123456789ab", leftRunID)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lock := filepath.Join(dir, "run.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return lock
}

// writeJournal writes the journal of the entries into the harness's pool's folder.
func writeJournal(t *testing.T, h *harness, entries ...pool.Entry) {
	t.Helper()
	var b []byte
	for _, e := range entries {
		b = append(b, e.DispatchID+" "+e.Phase+"\n"...)
	}
	if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// holder is a program that holds a left run's lock.
type holder struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// holdLock starts a program in a new process group that holds the lock of the file
// lock, which it inherits as a descriptor, as a qory run does once the pool that started
// it crashed: the test's copy is closed. With ignoreTerm, the program and the sleep it
// starts ignore SIGTERM. The program is killed at the test's end.
func holdLock(t *testing.T, lock string, ignoreTerm bool) *holder {
	t.Helper()
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	script := "exec sleep 60"
	if ignoreTerm {
		script = `trap "" TERM; sleep 60`
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.ExtraFiles = []*os.File{f}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &holder{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-h.done
	})
	return h
}

// pid is the holder's process id, which is its process group's.
func (h *holder) pid() int { return h.cmd.Process.Pid }

// running reports whether the holder has not ended.
func (h *holder) running() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// endedBy waits up to 20 s for the holder to end, and is the signal that ended it, 0
// when it exited or did not end.
func (h *holder) endedBy() syscall.Signal {
	select {
	case <-h.done:
	case <-time.After(20 * time.Second):
		return 0
	}
	if ws, ok := h.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal()
	}
	return 0
}

// writeGroupFile writes the process group id to run.pgid beside lock.
func writeGroupFile(t *testing.T, lock string, id int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(lock), "run.pgid"), []byte(strconv.Itoa(id)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lockFree reports whether the lock of the file lock is free, false when the file
// cannot be opened; it drops the lock again. It is safe to call from any goroutine.
func lockFree(lock string) bool {
	f, err := os.OpenFile(lock, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// reaps records the run ids the pool's Reap was called with, and whether the lock of
// the left run was free and no gone sent at each call; err is Reap's error.
type reaps struct {
	mu    sync.Mutex
	ids   []string
	order []bool
}

func (r *reaps) seam(h *harness, lock, id string, err error) func(context.Context, string) error {
	return func(_ context.Context, runID string) error {
		ends, _ := h.machineEnds(id)
		ok := lockFree(lock) && len(ends) == 0
		r.mu.Lock()
		r.ids = append(r.ids, runID)
		r.order = append(r.order, ok)
		r.mu.Unlock()
		return err
	}
}

func (r *reaps) calls() ([]string, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ids), slices.Clone(r.order)
}

// realPolls makes each of the pool's waits between two tries of a left run's lock take
// 10 ms, while the pool's clock moves on by the whole wait, and records whether the
// fake got more than before claims by then.
func realPolls(h *harness, before int, claimed *bool, mu *sync.Mutex) {
	h.setSleep(func(ctx context.Context, d time.Duration) error {
		if d == leftoverPoll {
			if h.count(provisionertest.RouteClaim) > before {
				mu.Lock()
				*claimed = true
				mu.Unlock()
			}
			time.Sleep(10 * time.Millisecond)
		}
		return ctx.Err()
	})
}

// TestALeftoverRunIsStopped starts a pool of one run whose journal contains a
// dispatched entry whose qory run is left from a crash of the pool: a program in a new
// process group, apart from the test's, holds run.lock, and run.pgid contains that
// group. The pool logs that it stops the run, and sends SIGTERM to the group; a program
// that ignores SIGTERM gets SIGKILL once the grace passed. Once the lock is free, Reap
// is called with the run id of the record in the dispatch's folder, then gone is sent,
// and the entry and the folder go. While the run stops, it takes the pool's one Run, so
// no claim is sent.
func TestALeftoverRunIsStopped(t *testing.T) {
	for _, ignoreTerm := range []bool{false, true} {
		t.Run("ignoreTerm="+strconv.FormatBool(ignoreTerm), func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Offer(provisionertest.Run{}, provisionertest.Run{})
			e := claimTo(t, h.srv, pool.PhaseDispatched)
			lock := leftFolder(t, h, e.DispatchID)
			holder := holdLock(t, lock, ignoreTerm)
			writeGroupFile(t, lock, holder.pid())
			writeJournal(t, h, e)
			var r reaps
			h.pool.Reap = r.seam(h, lock, e.DispatchID, nil)
			var mu sync.Mutex
			var claimedEarly bool
			realPolls(h, h.count(provisionertest.RouteClaim), &claimedEarly, &mu)
			h.start()
			h.waitFor("the gone's line", func() bool { return len(h.log.with(lineGone)) == 1 })
			h.stop()
			if m := h.log.with(lineLeftoverStopped); len(m) != 1 || m[0]["dispatch_id"] != e.DispatchID || m[0]["process_group"] != float64(holder.pid()) {
				t.Errorf("the stop's lines are %v", m)
			}
			if ids, order := r.calls(); !slices.Equal(ids, []string{leftRunID}) || !order[0] {
				t.Errorf("Reap was called with %q, with the lock free and no gone sent: %v", ids, order)
			}
			if states, _ := h.machineEnds(e.DispatchID); !slices.Equal(states, []string{"gone"}) {
				t.Errorf("the machine ends are %q, want one gone", states)
			}
			if slices.ContainsFunc(h.srv.Calls(), func(c provisionertest.Call) bool {
				return c.Route == provisionertest.RouteRelease && c.DispatchID == e.DispatchID
			}) {
				t.Error("the left dispatch was released")
			}
			mu.Lock()
			if claimedEarly {
				t.Error("a claim was sent while the left run stopped")
			}
			mu.Unlock()
			if lines := journalOf(t, h.state); len(lines) != 0 {
				t.Errorf("the journal is %q", lines)
			}
			h.noFolder(e.DispatchID)
			want := syscall.SIGTERM
			if ignoreTerm {
				want = syscall.SIGKILL
			}
			if got := holder.endedBy(); got != want {
				t.Errorf("the holder ended by %v, want %v", got, want)
			}
			if len(h.log.with(lineLeftoverHeld)) != 0 || len(h.log.with(lineReapFailed)) != 0 {
				t.Errorf("the log:\n%s", h.log.text())
			}
		})
	}
}

// TestAFreeLockSendsGoneAtOnce starts a pool with an entry at each phase whose folder
// has a free run.lock and a run's record: no wait for a lock, no stop, Reap with the
// run id, then the entry's end: gone for an ended and a dispatched entry, the release
// for a claimed one, which returns 409 machine_exists and gets gone. A failed Reap is
// logged and gone is sent all the same.
func TestAFreeLockSendsGoneAtOnce(t *testing.T) {
	for _, phase := range []string{pool.PhaseClaimed, pool.PhaseDispatched, pool.PhaseEnded} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Offer(provisionertest.Run{})
			e := claimTo(t, h.srv, pool.PhaseDispatched)
			e.Phase = phase
			lock := leftFolder(t, h, e.DispatchID)
			writeJournal(t, h, e)
			var r reaps
			h.pool.Reap = r.seam(h, lock, e.DispatchID, errors.New("the engine is not running"))
			h.start()
			h.waitFor("the gone's line", func() bool { return len(h.log.with(lineGone)) == 1 })
			h.stop()
			if slices.Contains(h.waitsOf(), leftoverPoll) {
				t.Error("the pool waited for a free lock")
			}
			if len(h.log.with(lineLeftoverStopped)) != 0 {
				t.Errorf("the pool stopped a run with a free lock:\n%s", h.log.text())
			}
			if ids, _ := r.calls(); !slices.Equal(ids, []string{leftRunID}) {
				t.Errorf("Reap was called with %q", ids)
			}
			if m := h.log.with(lineReapFailed); len(m) != 1 || m[0]["dispatch_id"] != e.DispatchID || m[0]["run_id"] != leftRunID || m[0]["error"] != "the engine is not running" {
				t.Errorf("the failed reap's lines are %v", m)
			}
			if states, _ := h.machineEnds(e.DispatchID); !slices.Equal(states, []string{"gone"}) {
				t.Errorf("the machine ends are %q, want one gone", states)
			}
			if released := h.count(provisionertest.RouteRelease); released != map[string]int{pool.PhaseClaimed: 1}[phase] {
				t.Errorf("%d releases were sent", released)
			}
			if lines := journalOf(t, h.state); len(lines) != 0 {
				t.Errorf("the journal is %q", lines)
			}
			h.noFolder(e.DispatchID)
		})
	}
}

// TestALockStillHeldKeepsTheEntry starts a pool whose dispatched entry's run.lock a
// program holds, while run.pgid does not lead to it: another group, which gets SIGTERM;
// no run.pgid; a run.pgid written before the holder started, as when its id passed to a
// new group; and the id of a process that has ended. Only the first gets a signal and
// the line of the stop. After the waits, the pool logs the line that starts with its
// fix, with the lock's path and the cause, sends no gone, calls no Reap, and keeps the
// entry at dispatched with its folder; the holder still runs, and the left run keeps
// the pool's one Run, so no claim is sent.
func TestALockStillHeldKeepsTheEntry(t *testing.T) {
	for _, c := range []struct {
		name    string
		group   func(t *testing.T, lock string, holder *holder)
		stopped int
		cause   string
	}{
		// The other group's leader ends at SIGTERM: the check before SIGKILL then fails
		// on macOS, and passes for its zombie on Linux, so the cause is either.
		{name: "another group", stopped: 1, cause: "", group: func(t *testing.T, lock string, _ *holder) {
			other := exec.Command("sleep", "60")
			other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := other.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = syscall.Kill(-other.Process.Pid, syscall.SIGKILL)
				_ = other.Wait()
			})
			writeGroupFile(t, lock, other.Process.Pid)
		}},
		{name: "no run.pgid", cause: "run.pgid", group: func(*testing.T, string, *holder) {}},
		{name: "a run.pgid older than its process", cause: "started at", group: func(t *testing.T, lock string, holder *holder) {
			writeGroupFile(t, lock, holder.pid())
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(filepath.Join(filepath.Dir(lock), "run.pgid"), old, old); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a process that ended", cause: "does not run", group: func(t *testing.T, lock string, _ *holder) {
			ended := exec.Command("true")
			if err := ended.Run(); err != nil {
				t.Fatal(err)
			}
			writeGroupFile(t, lock, ended.Process.Pid)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Offer(provisionertest.Run{}, provisionertest.Run{})
			e := claimTo(t, h.srv, pool.PhaseDispatched)
			lock := leftFolder(t, h, e.DispatchID)
			holder := holdLock(t, lock, false)
			c.group(t, lock, holder)
			writeJournal(t, h, e)
			var r reaps
			h.pool.Reap = r.seam(h, lock, e.DispatchID, nil)
			var mu sync.Mutex
			var claimed bool
			before := h.count(provisionertest.RouteClaim)
			realPolls(h, before, &claimed, &mu)
			h.start()
			h.waitFor("the held lock's line", func() bool { return len(h.log.with(lineLeftoverHeld)) == 1 })
			time.Sleep(200 * time.Millisecond)
			h.stop()
			if m := h.log.with(lineLeftoverHeld); m[0]["dispatch_id"] != e.DispatchID || m[0]["path"] != lock || !strings.Contains(m[0]["error"].(string), c.cause) {
				t.Errorf("the held lock's line is %v, want the cause %q", m[0], c.cause)
			}
			if n := len(h.log.with(lineLeftoverStopped)); n != c.stopped {
				t.Errorf("%d lines of a stop, want %d", n, c.stopped)
			}
			if !holder.running() || lockFree(lock) {
				t.Error("the program that held the lock was stopped")
			}
			if n := h.count(provisionertest.RouteMachineEnd); n != 0 {
				t.Errorf("%d machine ends were sent", n)
			}
			if n := h.count(provisionertest.RouteClaim) - before; n != 0 || claimed {
				t.Errorf("%d claims were sent while the left run kept the pool's one run", n)
			}
			if ids, _ := r.calls(); len(ids) != 0 {
				t.Errorf("Reap was called with %q", ids)
			}
			if lines, want := journalOf(t, h.state), e.DispatchID+" "+pool.PhaseDispatched; len(lines) != 1 || lines[0] != want {
				t.Errorf("the journal is %q, want %q", lines, want)
			}
			if _, err := os.Stat(lock); err != nil {
				t.Errorf("the dispatch's folder: %v", err)
			}
		})
	}
}

// TestALockThatCannotBeTried starts a pool whose dispatched entry's run.lock is a link,
// which the lock's open does not follow: the pool logs the line that starts with its
// fix, with the lock's path and the error in its text and the dispatch id as its one
// attribute besides the level, and not the line of a held lock. It stops
// nothing, sends no gone, calls no Reap, and keeps the entry at dispatched.
func TestALockThatCannotBeTried(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{})
	e := claimTo(t, h.srv, pool.PhaseDispatched)
	lock := leftFolder(t, h, e.DispatchID)
	target := filepath.Join(h.root, "elsewhere.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, h, e)
	var r reaps
	h.pool.Reap = r.seam(h, lock, e.DispatchID, nil)
	before := h.count(provisionertest.RouteClaim)
	h.start()
	start := "check `" + lock + "`: the pool could not lock it ("
	h.waitFor("the line of a lock not tried", func() bool { return len(h.log.withPrefix(start)) == 1 })
	h.waitFor("a claim", func() bool { return h.count(provisionertest.RouteClaim) > before })
	h.stop()
	m := h.log.withPrefix(start)[0]
	msg := m["msg"].(string)
	cause := strings.TrimSuffix(strings.TrimPrefix(msg, start), lockNotTriedEnd)
	if !strings.HasSuffix(msg, lockNotTriedEnd) || cause == "" || m["dispatch_id"] != e.DispatchID || m["path"] != nil || m["error"] != nil {
		t.Errorf("the line is %v", m)
	}
	if len(h.log.with(lineLeftoverHeld)) != 0 || len(h.log.with(lineLeftoverStopped)) != 0 {
		t.Errorf("the log:\n%s", h.log.text())
	}
	if states, _ := h.machineEnds(e.DispatchID); len(states) != 0 {
		t.Errorf("the machine ends are %q", states)
	}
	if ids, _ := r.calls(); len(ids) != 0 {
		t.Errorf("Reap was called with %q", ids)
	}
	if lines, want := journalOf(t, h.state), e.DispatchID+" "+pool.PhaseDispatched; len(lines) != 1 || lines[0] != want {
		t.Errorf("the journal is %q, want %q", lines, want)
	}
}

// TestAStopDuringALeftoversStop stops the pool while it waits for a left run that
// ignores SIGTERM to free its lock: the wait ends at the stop, no SIGKILL is sent, no
// line of a held lock is logged, no gone is sent, and the entry stays at dispatched for
// the next start.
func TestAStopDuringALeftoversStop(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{})
	e := claimTo(t, h.srv, pool.PhaseDispatched)
	lock := leftFolder(t, h, e.DispatchID)
	holder := holdLock(t, lock, true)
	writeGroupFile(t, lock, holder.pid())
	writeJournal(t, h, e)
	var r reaps
	h.pool.Reap = r.seam(h, lock, e.DispatchID, nil)
	waiting := make(chan struct{}, 1)
	h.setSleep(func(ctx context.Context, d time.Duration) error {
		if d == leftoverPoll {
			select {
			case waiting <- struct{}{}:
			default:
			}
			<-ctx.Done()
		}
		return ctx.Err()
	})
	h.start()
	select {
	case <-waiting:
	case <-time.After(20 * time.Second):
		t.Fatal("the pool did not wait for the left run's lock")
	}
	h.stop()
	if len(h.log.with(lineLeftoverStopped)) != 1 || len(h.log.with(lineLeftoverHeld)) != 0 || len(h.log.withPrefix("check `")) != 0 {
		t.Errorf("the log:\n%s", h.log.text())
	}
	if !holder.running() {
		t.Error("the left run got SIGKILL after the stop")
	}
	if states, _ := h.machineEnds(e.DispatchID); len(states) != 0 {
		t.Errorf("the machine ends are %q", states)
	}
	if ids, _ := r.calls(); len(ids) != 0 {
		t.Errorf("Reap was called with %q", ids)
	}
	if lines, want := journalOf(t, h.state), e.DispatchID+" "+pool.PhaseDispatched; len(lines) != 1 || lines[0] != want {
		t.Errorf("the journal is %q, want %q", lines, want)
	}
}

// TestADispatchedNotJournaledStartsNoRun fails every write of the journal once
// dispatched is sent: no qory run starts, the pool logs that the run could not be
// started, sends no release, and sends gone.
func TestADispatchedNotJournaledStartsNoRun(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{})
	let := h.gate.holdNext("/dispatched")
	h.start()
	<-h.gate.reached
	// A folder with a file in it where the journal's next version goes fails the
	// write's removal of a version left from before.
	block := filepath.Join(h.state, pool.JournalFile+".new")
	if err := os.MkdirAll(filepath.Join(block, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	let()
	d := h.only()
	h.waitFor("the gone's line", func() bool { return len(h.log.with(lineGone)) == 1 })
	h.stop()
	if err := os.RemoveAll(block); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.reportOf(d.ID); ok || len(h.log.with(lineStarted)) != 0 {
		t.Error("qory run started")
	}
	if m := h.log.with(lineNotStarted); len(m) != 1 || m[0]["dispatch_id"] != d.ID {
		t.Errorf("the not-started lines are %v", m)
	}
	if states, _ := h.machineEnds(d.ID); !slices.Equal(states, []string{"gone"}) {
		t.Errorf("the machine ends are %q, want one gone", states)
	}
	h.notReleased()
}

// TestARunHoldsItsLock runs one dispatch whose fake qory blocks: its descriptor 4 is
// run.lock of its folder, run.pgid contains its process group, which is its process
// id, and the lock is held. Once it ended, the lock is free while the machine log's
// upload is held, before the folder goes.
func TestARunHoldsItsLock(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Block: true})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
	d := h.only()
	h.ready(d.ID)
	dir := filepath.Join(h.state, "runs", d.ID)
	lock := filepath.Join(dir, "run.lock")
	if lockFree(lock) {
		t.Error("the lock is free while qory run runs")
	}
	b, err := os.ReadFile(filepath.Join(dir, "run.pgid"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := strconv.Atoi(string(b[:len(b)-1]))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := syscall.Getpgid(group); err != nil || g != group {
		t.Errorf("run.pgid contains %d, whose process group is %d: %v", group, g, err)
	}
	if info, err := os.Stat(lock); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("run.lock: %v, %v", info, err)
	}
	let := h.gate.holdNext("/machine-log")
	h.letGo(d.ID)
	<-h.gate.reached
	if !lockFree(lock) {
		t.Error("the lock is held once qory run ended")
	}
	let()
	h.settled(1)
	h.stop()
	if r, ok := h.reportOf(d.ID); !ok || !r.FD4Lock {
		t.Errorf("qory run's descriptor 4 was not run.lock: %+v", r)
	}
}
