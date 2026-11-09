// Package pool is the pool program's loop, what qory run pool runs: it claims a node
// pool's dispatches from Qory Apiary with the pool's access key, and starts a qory run
// for each on this machine, the same command a machine that a provisioner creates runs.
//
// For each dispatch claimed it checks the claim: one whose machine member is set, or
// whose lease passed, is released. It writes the run code to a file, mode 0600, in the
// pool's folder, opens the file for reading and unlinks it, so no file name leads to the
// code. It sends dispatched, then starts qory run --run-code-file /dev/fd/3 --gateway
// <gateway_url> --home <dispatch's folder>/home, with the open file as descriptor 3, and
// the file run.lock of the dispatch's folder, whose lock the pool took, as descriptor 4,
// in an empty folder work/ of the dispatch's folder <pool's folder>/runs/<dispatch_id>,
// with the environment of [child.Env] and HOME set to home-user/ of the dispatch's
// folder: the pool's access key never reaches it, and neither do the files under the
// pool's HOME. The run's harness home is under home/, outside work/, so no later run
// reads it: the pool removes both with the dispatch's folder. qory run redeems the code
// and reports the run's outcome itself; the pool posts none. While qory run runs, the
// pool sends dispatched again every [heartbeatEvery], and stops qory run once Qory
// Apiary returns 410 gone or 409 lease_expired to it, or once the claim's
// max_lifetime_minutes passed. Once qory run ends, the pool uploads its output as the
// dispatch's machine log, once: its lines as the pool's log has them, at most the last
// 768 KiB, with each line that contains what looks like a secret replaced. A dispatched
// dispatch is never released: Qory Apiary returns 409 machine_exists to a release after
// dispatched.
//
// After the machine log, the pool reports the dispatch's machine gone: the provisioner
// contract's machine end with the state gone, with which Qory Apiary frees the slot it
// keeps for the dispatch once the dispatch ended. The pool has no machine command: qory
// run is the dispatch's machine, and the end of qory run is the end of the machine. A
// gone that fails, with no response, an unsigned response, a signed 429 or 503
// unavailable, or another server error, is sent again, built anew, after a wait: a
// signed 429's Retry-After, else one that doubles from [firstBackoff] up to
// [maxBackoff]. It is sent until Qory Apiary returns a signed 204, 400, 403 or 404,
// after which the same call cannot succeed. The pool also sends gone, with no qory run
// started, for a dispatch whose release returned 409 machine_exists, and for one whose
// first dispatched returned 410 gone or 409 lease_expired, since an earlier try of
// dispatched may have been taken with its response lost. A gone takes none of the
// pool's Runs: the next claim may go out while it is sent.
//
// A journal, <pool's folder>/dispatches, contains each live dispatch's id and phase,
// claimed, dispatched or ended, never a run code. The claimed entry is written and
// synced before the request dispatched, and the dispatched entry after that request
// succeeds; when the dispatched entry cannot be written, no qory run starts, and the
// dispatch gets gone. The ended entry is written before the first gone: once qory run
// ended or could not be started, or after the release or the dispatched that the gone
// follows. A release of an entry left at claimed after a dispatched that succeeded
// returns 409 machine_exists, and the pool sends gone for it. A release that fails with
// no response, an unsigned response, a fourth signed 429, a server error or another
// refusal than a signed 400, 403 or 404 leaves the entry at claimed, and is sent again
// beside the claim loop after a wait, as gone is, until it is settled; no qory run
// starts for that dispatch, and it takes none of the pool's Runs. The dispatch's folder
// is removed before its first gone: once qory run ended, after the upload of its
// machine log, or at once when no qory run started. The entry is removed once the
// dispatch ends; an ended entry once its gone got a signed 204, 400, 403 or 404. When
// the program starts, before its first claim, it runs the check of [Probe]: when a
// container on the engine's default bridge network connects to the cloud metadata
// service, or the check cannot run, Run returns its error and claims nothing. Then each
// claimed entry it finds is released, with its folder, and each dispatched entry is
// written at ended. Each ended entry, each dispatched one, and each claimed one whose
// release returns 409 machine_exists, gets gone, sent as above in a separate goroutine
// beside the claim loop, so the first claim does not wait for it; a release that fails
// is sent again there too.
//
// qory run, and each program it starts that inherits descriptor 4, hold the lock of
// run.lock until they end, so the lock outlasts a crash of the pool, and the pool
// closes its copy once qory run ended. qory run starts in a new session, whose id is
// its process id. Once qory run started, the pool writes that id and qory run's start
// stamp to run.session of the dispatch's folder. When the program starts, an entry
// whose run.lock is held is a qory run left from before: beside the claim loop, and in
// one of the pool's Runs while one is free, the pool sends SIGTERM to each process of
// that session, then, once the grace passed, SIGKILL to each process of it then, and
// waits up to the grace again for the lock. The pool signals the session only while it
// is shown to be the run's, which it checks before each signal: the process of its id
// has the stamp in run.session, or, on Linux, no process has that id and a process of
// the session has run.lock open. Otherwise the pool sends nothing and waits the grace
// for the lock, and the line of the held lock gives the cause. When the lock is still
// held then, or
// cannot be tried, the entry stays, with its folder, for the next start, and no gone is
// sent; so does it when ctx ends during the waits. Once the lock is free, the pool
// removes the wall of each run whose record is in the dispatch's folder, with
// [Pool.Reap], as qory run resend does, and acts on the entry as above.
//
// When ctx ends, the pool stops: it claims no more, a claim being sent is given up, a
// dispatch not yet dispatched is released, and each qory run is stopped as [child.Run]
// stops a command. A gone that has not got a signed 204, 400, 403 or 404 is sent once
// more, for at most [lastGoneTimeout]; when that fails, the ended entry stays, for the
// next start. A release that has failed is not sent again, and its claimed entry stays,
// for the next start. Run returns once every dispatch ended.
package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/secretmask"
)

// The waits of the loop: a claim that failed is sent again after a wait that doubles
// from firstBackoff up to maxBackoff.
const (
	firstBackoff = time.Second
	maxBackoff   = time.Minute
	// maxRateLimited is how many signed 429s dispatched or a release waits out before
	// it gives up.
	maxRateLimited = 3
	// heartbeatEvery is how often dispatched is sent again while a dispatch's qory run
	// runs.
	heartbeatEvery = 60 * time.Second
	// lastGoneTimeout is how long the last gone of a dispatch may take once the pool
	// stops, so a stop is not held up by Qory Apiary.
	lastGoneTimeout = 10 * time.Second
)

// The pool's folder's entries besides the journal: the folder of the dispatches'
// folders, and the prefix of a run code's file, which is unlinked once it is open.
const (
	runsFolder     = "runs"
	codeFilePrefix = "runcode-"
)

// The files of a dispatch's folder that outlast the pool program: the lock that the
// dispatch's qory run holds, and the id of its session with qory run's start stamp.
const (
	runLockFile    = "run.lock"
	runSessionFile = "run.session"
)

// leftoverPoll is how often the pool tries the lock of a qory run left from before it
// started, while it waits for that run to stop.
const leftoverPoll = 100 * time.Millisecond

// homeFolder is the folder of a dispatch's folder that its qory run composes the harness
// under, as --home: one home per checkout under it, as qory run puts it.
const homeFolder = "home"

// userHomeFolder is the folder of a dispatch's folder that is its qory run's HOME, in
// place of the pool program's: a run's docker and git read their registry logins and
// configuration from it, not from the pool's HOME or another run's.
const userHomeFolder = "home-user"

// Settings are what the pool runs by.
type Settings struct {
	// Runs is how many dispatches may be live at once, at least 1.
	Runs int
	// State is the pool's folder, an absolute path, mode 0700, which contains the
	// journal and the dispatches' folders.
	State string
	// RunConfig is each qory run's XDG_CONFIG_HOME, an absolute directory.
	RunConfig string
	// Qory is the qory binary; empty is the one this process runs. A name with no path
	// separator is found on the PATH every command gets, as [child.Program] has it.
	Qory string
	// Grace is how long a qory run has after SIGTERM before it is killed; zero is
	// [child.DefaultGrace].
	Grace time.Duration
	// Probe is the check of the cloud metadata service that runs before the first
	// claim.
	Probe Probe
}

// Pool is the pool program's loop and what it reaches the world through.
type Pool struct {
	Settings Settings
	// Client is the provisioner contract's client, signed with the pool's access key.
	Client *provisioner.Client
	// Log takes the pool's lines, each through [SafeHandler]; nil logs nothing.
	Log *slog.Logger
	// Now is the clock a lease is read against; nil is time.Now.
	Now func() time.Time
	// Sleep waits d or until ctx is done; nil waits on a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Reap removes the containers and networks of the wall of the run id, as qory run
	// resend does; nil removes nothing. The pool calls it for a qory run left from
	// before it started, once that run stopped.
	Reap func(ctx context.Context, runID string) error

	log     *slog.Logger
	journal *journal
	wg      sync.WaitGroup
	slots   chan struct{}
}

// Run runs the pool until ctx ends, then stops it as the package describes, and returns
// nil once every dispatch ended. Its error is for a pool that could not start: settings
// it cannot run by, a journal it cannot read, or a check of [Probe] that failed or
// connected to the cloud metadata service, in which case no run is claimed.
func (p *Pool) Run(ctx context.Context) error {
	s := p.Settings
	switch {
	case s.Runs < 1:
		return errors.New("set pool.runs in forager.yaml to 1 or more: the pool runs at least 1 run at once")
	case !filepath.IsAbs(s.State):
		return fmt.Errorf("the pool's folder %s is not an absolute path", exports.Value(s.State, true))
	case !filepath.IsAbs(s.RunConfig):
		return fmt.Errorf("set pool.run_config in forager.yaml to an absolute path: %s is not one", exports.Value(s.RunConfig, true))
	case p.Client == nil:
		return errors.New("the pool has no client of Qory Apiary")
	}
	p.log = slog.New(SafeHandler(slog.DiscardHandler))
	if p.Log != nil {
		p.log = slog.New(SafeHandler(p.Log.Handler()))
	}
	if err := os.MkdirAll(s.State, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.State, 0o700); err != nil {
		return err
	}
	if err := removeCodeFiles(s.State); err != nil {
		return err
	}
	j, err := openJournal(s.State)
	if err != nil {
		return err
	}
	p.journal = j
	// No claim is sent, and no claim the journal contains is released, before the check
	// passed.
	stopped, err := probe(ctx, s)
	if err != nil || stopped {
		return err
	}
	p.slots = make(chan struct{}, s.Runs)
	p.replay(ctx)
	p.claimLoop(ctx)
	p.wg.Wait()
	return nil
}

// removeCodeFiles removes each run code's file in the pool's folder state: one is left
// there only when a program stopped between its write and its unlink. The folder is
// listed, not matched as a pattern, so a path with [ or * in it is read as written.
func removeCodeFiles(state string) error {
	entries, err := os.ReadDir(state)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), codeFilePrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(state, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// replay acts on each dispatch the journal contains, as the program that held it is
// gone: a claimed one is released, and a dispatched one, which Qory Apiary does not
// release, is written at ended; an ended one, and a dispatched one, get gone. A dispatch
// released or over already leaves the journal, and its folder is removed. A claimed one
// whose release returned 409 machine_exists is written at ended, and gets gone too; one
// whose release failed stays at claimed, and its release is sent again. Each gone and
// each release sent again goes out beside the claim loop, as [Pool.giveBack] has it.
//
// A dispatch whose qory run, or a program it started, still holds the lock of its
// folder is stopped first, as [Pool.stopLeftover] has it, beside the claim loop: while
// it stops, it takes one of the pool's Runs when one is free. A dispatch whose folder
// contains a run's record then has that run's wall removed, as [Pool.Reap] has it,
// beside the claim loop too, before the dispatch is acted on as above. A dispatch whose
// lock is still held after the stop stays in the journal at its phase, with its folder,
// for the next start, and keeps its Run; so does one whose lock cannot be tried, which
// takes no Run at the first try and keeps the one it took when a later try fails. Once
// ctx ends, the rest stay.
func (p *Pool) replay(ctx context.Context) {
	for _, e := range p.journal.list() {
		if ctx.Err() != nil {
			return
		}
		id := e.DispatchID
		lock, err := p.runLock(id)
		switch {
		case errors.Is(err, foragerdir.ErrLocked):
			slot := p.takeSlot()
			p.wg.Go(func() {
				if !p.stopLeftover(ctx, id) {
					return
				}
				if slot {
					<-p.slots
				}
				if ctx.Err() == nil {
					p.reap(ctx, id)
					p.resume(ctx, e, false)
				}
			})
			continue
		case err != nil:
			p.lockNotTried(id, err)
			continue
		}
		_ = lock.Release()
		if len(p.leftRuns(id)) > 0 {
			p.wg.Go(func() {
				p.reap(ctx, id)
				p.resume(ctx, e, false)
			})
			continue
		}
		p.resume(ctx, e, true)
	}
}

// resume acts on the journal entry e at the replay, once no qory run of its dispatch
// is going, as [Pool.replay] has it. beside sends its gone in a goroutine beside the
// claim loop; otherwise it is sent before resume returns.
func (p *Pool) resume(ctx context.Context, e Entry, beside bool) {
	id := e.DispatchID
	switch e.Phase {
	case PhaseClaimed:
		p.giveBack(ctx, id, ReasonRestarted)
		return
	case PhaseDispatched:
		p.setPhase(id, PhaseEnded)
	}
	if beside {
		p.endBeside(ctx, id)
	} else {
		p.end(ctx, id)
	}
}

// takeSlot takes one of the pool's Runs when one is free, and reports whether it took
// one: a qory run left from before the pool started takes one while it stops, and
// never waits for one.
func (p *Pool) takeSlot() bool {
	select {
	case p.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// lockPath is the lock file of the dispatch id's folder, run.lock.
func (p *Pool) lockPath(id string) string {
	return filepath.Join(p.dispatchDir(id), runLockFile)
}

// runLock takes the lock of the dispatch id's folder, without waiting: the lock that
// the dispatch's qory run holds while it runs. A lock another process holds is
// [foragerdir.ErrLocked]. A folder with no lock file returns no lock and no error.
func (p *Pool) runLock(id string) (*foragerdir.Lock, error) {
	path := p.lockPath(id)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return foragerdir.LockFile(path)
}

// stopLeftover stops the qory run of the dispatch id that is left from before the pool
// started, and reports whether its lock is free. When the session that run.session of
// its folder contains is the run's, as [sessionMembers] checks, the pool sends SIGTERM
// to each of its processes. While the lock is still held once the grace of [Settings]
// has passed, it checks the session again and sends SIGKILL to each process, in up to
// [killRounds] rounds, and waits up to the grace again for the lock. When the session
// is not shown to be the run's, the pool sends no signal and waits up to the grace for
// the lock. A lock still held after the waits is logged with the cause, as is a lock
// that cannot be tried. Once stop ends, the waits end, and nothing more is sent or
// logged; the waits are the pool's Sleep.
func (p *Pool) stopLeftover(stop context.Context, id string) bool {
	ids := slog.String("dispatch_id", id)
	grace := p.Settings.Grace
	if grace <= 0 {
		grace = child.DefaultGrace
	}
	lock := p.lockPath(id)
	session, start, why := readSession(p.dispatchDir(id))
	var members []int
	if why == nil {
		members, why = sessionMembers(session, start, lock)
	}
	if why == nil {
		p.log.Warn(lineLeftoverStopped, ids, slog.Int("session", session), slog.Int("processes", len(members)))
		child.SignalEach(members, session, false)
		free, err := p.waitLock(stop, id, grace)
		if free {
			return true
		}
		if stop.Err() == nil && errors.Is(err, foragerdir.ErrLocked) {
			why = killSession(session, start, lock)
		}
	}
	free, err := p.waitLock(stop, id, grace)
	switch {
	case free:
		return true
	case stop.Err() != nil:
		return false
	case !errors.Is(err, foragerdir.ErrLocked):
		p.lockNotTried(id, err)
		return false
	}
	if why != nil {
		err = why
	}
	p.log.Error(lineLeftoverHeld, ids, slog.String("path", exports.HidePath(lock)), errAttr(err))
	return false
}

// killRounds is how many times, at most, the pool lists a left run's session and sends
// SIGKILL to the processes that were not in an earlier list: a process may start
// another one before SIGKILL reaches it.
const killRounds = 3

// killSession sends SIGKILL to each process of the session id that [sessionMembers]
// lists, and lists the session again, until a list has no process that an earlier one
// did not have, for at most [killRounds] lists. The error is that of the first list,
// when it lists no process to signal; a later list that fails ends the rounds.
func killSession(id int, start, lock string) error {
	seen := map[int]bool{}
	for range killRounds {
		members, err := sessionMembers(id, start, lock)
		if err != nil {
			if len(seen) == 0 {
				return err
			}
			return nil
		}
		var added []int
		for _, m := range members {
			if !seen[m] {
				seen[m] = true
				added = append(added, m)
			}
		}
		if len(added) == 0 {
			return nil
		}
		child.SignalEach(added, id, true)
	}
	return nil
}

// sessionMembers is the id of each process of the session id, as
// [child.SessionMembers] lists them, when the session is shown to be the one that qory
// run started with the start stamp start, and whose lock is the file lock. The session
// is the run's when the process of the id has that stamp, as [child.CheckLeader]
// checks. When no process has the id, the session is the run's only when one of its
// processes has lock open, matched by device and inode, as [child.HasOpen] reads it on
// Linux; on another system, such a session is not shown to be the run's. Any other
// case is an error that gives the cause, as is a session with no process: a program
// outside the session holds the lock.
func sessionMembers(id int, start, lock string) ([]int, error) {
	err := child.CheckLeader(id, start)
	leaderless := errors.Is(err, child.ErrNoProcess)
	if err != nil && !leaderless {
		return nil, err
	}
	members, err := child.SessionMembers(id)
	if err != nil {
		return nil, fmt.Errorf("the processes of session %d could not be listed: %w", id, err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no process is in session %d, so a program outside it holds the lock", id)
	}
	if !leaderless {
		return members, nil
	}
	if !child.OpenFilesRead {
		return nil, fmt.Errorf("process %d, the leader of session %d, does not run, and the files that the session's processes have open cannot be read on this system", id, id)
	}
	info, err := os.Lstat(lock)
	if err != nil {
		return nil, err
	}
	if len(child.HasOpen(members, info)) == 0 {
		return nil, fmt.Errorf("process %d, the leader of session %d, does not run, and no process of the session has run.lock open", id, id)
	}
	return members, nil
}

// lockNotTried logs that the lock of the dispatch id's folder could not be tried, for
// err, with the lock's path and the error in the line's text.
func (p *Pool) lockNotTried(id string, err error) {
	line := fmt.Sprintf(lineLockNotTried, exports.HidePath(p.lockPath(id)), exports.HideError(err).Error())
	p.log.Error(line, slog.String("dispatch_id", id))
}

// waitLock tries the lock of the dispatch id's folder every [leftoverPoll], for up to
// d by the pool's clock, and reports whether it was free. It returns at once when a try
// fails with another error than [foragerdir.ErrLocked], and once ctx ends; when the
// lock was not free, the error is that of the last try.
func (p *Pool) waitLock(ctx context.Context, id string, d time.Duration) (bool, error) {
	until := p.now().Add(d)
	for {
		lock, err := p.runLock(id)
		if err == nil {
			_ = lock.Release()
			return true, nil
		}
		if !errors.Is(err, foragerdir.ErrLocked) || ctx.Err() != nil || !p.now().Before(until) {
			return false, err
		}
		_ = p.sleep(ctx, leftoverPoll)
	}
}

// maxSessionFile is the most of run.session that is read: far more than its one line.
const maxSessionFile = 128

// readSession is what run.session of the dispatch's folder dir contains, read from one
// open of the file: the session id and qory run's start stamp, as [writeSession] writes
// them. A file of another shape is an error, as is an id of 1 or less, the process id
// of the pool program, or the id of its session.
func readSession(dir string) (int, string, error) {
	f, err := os.Open(filepath.Join(dir, runSessionFile))
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSessionFile))
	if err != nil {
		return 0, "", err
	}
	bad := fmt.Errorf("%s does not contain the id of a qory run's session and the run's start", runSessionFile)
	fields := strings.Fields(string(b))
	if len(fields) != 2 || strings.Trim(fields[1], "0123456789.") != "" {
		return 0, "", bad
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 1 || n == os.Getpid() || n == child.Session() {
		return 0, "", bad
	}
	return n, fields[1], nil
}

// writeSession writes the session id and the start stamp of its leader, qory run, as
// [child.StartStamp] reads it, to run.session of the dispatch's folder dir, mode 0600,
// as one line: "<id> <stamp>". It writes a file beside run.session, syncs it, and
// renames it to run.session, so run.session is either whole or missing. It writes no
// file when the stamp cannot be read.
func writeSession(dir string, id int) error {
	stamp, err := child.StartStamp(id)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, runSessionFile+".*")
	if err != nil {
		return err
	}
	_, err = f.WriteString(strconv.Itoa(id) + " " + stamp + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, runSessionFile))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// leftRuns is the run ids of the records under the dispatch id's folder, which qory
// run made with XDG_STATE_HOME set to its state/, as [explore.RecordedRuns] lists them.
func (p *Pool) leftRuns(id string) []string {
	return explore.RecordedRuns(filepath.Join(p.dispatchDir(id), "state"))
}

// reap removes the wall of each run whose record is under the dispatch id's folder, as
// [Pool.Reap] has it, on a context that does not end with stop, and logs each failure;
// a failure changes nothing else.
func (p *Pool) reap(stop context.Context, id string) {
	if p.Reap == nil {
		return
	}
	for _, run := range p.leftRuns(id) {
		if err := p.Reap(context.WithoutCancel(stop), run); err != nil {
			p.log.Warn(lineReapFailed, slog.String("dispatch_id", id), slog.String("run_id", run), errAttr(err))
		}
	}
}

// claimLoop claims dispatches while fewer than Runs are live, and starts each, until
// ctx ends. A claim that failed is sent again after a wait: a signed 429's
// Retry-After, else one that doubles from [firstBackoff] up to [maxBackoff], after no
// response, a response not signed under the pin, a 401 among them, a server error, a
// 409 no_gateway or any other refusal.
func (p *Pool) claimLoop(ctx context.Context) {
	wait := firstBackoff
	for {
		select {
		case p.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		c, ok, err := p.Client.Claim(ctx)
		if ctx.Err() != nil {
			c.RunCode.Clear()
			<-p.slots
			return
		}
		if err != nil {
			<-p.slots
			pause := wait
			var r *provisioner.Refusal
			if errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) {
				pause = r.RetryAfter
			} else {
				wait = min(wait*2, maxBackoff)
			}
			p.log.Warn(lineClaimFailed, slog.Duration("wait", pause), errAttr(err))
			if p.sleep(ctx, pause) != nil {
				return
			}
			continue
		}
		wait = firstBackoff
		if !ok {
			<-p.slots
			continue
		}
		p.log.Info(lineClaimed, slog.String("dispatch_id", c.DispatchID))
		if !p.admit(ctx, c) {
			<-p.slots
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			gone := p.dispatch(ctx, c)
			// The slot is free once qory run ended or could not start: gone, and a
			// release sent again, run nothing on this machine.
			<-p.slots
			if gone {
				p.end(ctx, c.DispatchID)
			}
		}()
	}
}

// admit journals the claim c and checks it before its dispatch starts, and reports
// whether it starts. A claim whose machine member is set, whose lease passed, or that
// could not be journaled is released, and its run code cleared, as [Pool.giveBack] has
// it. The release of a claim that could not be journaled is not sent again.
func (p *Pool) admit(ctx context.Context, c provisioner.Claimed) bool {
	if err := p.journal.set(Entry{c.DispatchID, PhaseClaimed}); err != nil {
		c.RunCode.Clear()
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		end, err := p.release(context.WithoutCancel(ctx), c.DispatchID, ReasonNotStarted)
		if !p.settle(ctx, c.DispatchID, end) {
			p.log.Warn(lineNotReleased, slog.String("dispatch_id", c.DispatchID), slog.String("reason", ReasonNotStarted), errAttr(err))
		}
		return false
	}
	reason := ""
	switch {
	case c.Machine != "":
		// The contract rules out machine and machine_spec in the answer to a claim without
		// offers, and the pool sends none; this check is all that stops a run on such an
		// answer.
		reason = ReasonMachine
	case !p.now().Before(c.LeaseUntil):
		reason = ReasonLeasePassed
	}
	if reason == "" {
		return true
	}
	c.RunCode.Clear()
	p.giveBack(ctx, c.DispatchID, reason)
	return false
}

// dispatch takes the claim c from its folder and run code's file through dispatched to
// its qory run's end, and reports whether the caller sends gone for the dispatch, whose
// journal entry is then at ended: after qory run, or after a dispatched that returned
// 410 gone or 409 lease_expired. A dispatch released is handled by [Pool.giveBack]. stop
// is the pool's context: once it ends, no request is sent again after a wait, and qory
// run is stopped; the requests already decided go out on a context that does not end
// with it.
func (p *Pool) dispatch(stop context.Context, c provisioner.Claimed) bool {
	dir, code, lock, err := p.prepare(c)
	if err != nil {
		p.log.Warn(lineNotStarted, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		p.giveBack(stop, c.DispatchID, ReasonNotStarted)
		return false
	}
	defer code.Close()
	// [Pool.run] closes the lock once qory run ended; this closes it on the other paths.
	defer lock.Close()
	if stop.Err() != nil {
		p.giveBack(stop, c.DispatchID, ReasonStoppedBeforeStart)
		return false
	}
	if started, gone := p.dispatched(stop, c); !started {
		return gone
	}
	p.run(stop, c, dir, code, lock)
	return true
}

// dispatchDir is the folder of the dispatch id: <pool's folder>/runs/<id>.
func (p *Pool) dispatchDir(id string) string {
	return filepath.Join(p.Settings.State, runsFolder, id)
}

// prepare makes the dispatch's folder of c, mode 0700, with work/, empty, state/, qory
// run's XDG_STATE_HOME, home/, its --home, and home-user/, its HOME, each mode 0700, and
// the lock file run.lock, mode 0600, whose lock it takes. It returns the folder with c's
// run code open for reading, and the lock. The code is written to a new file, mode
// 0600, in the pool's folder, which is opened for reading and unlinked before prepare
// returns, on every path; c's run code is cleared.
func (p *Pool) prepare(c provisioner.Claimed) (string, *os.File, *foragerdir.Lock, error) {
	defer c.RunCode.Clear()
	dir := p.dispatchDir(c.DispatchID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", nil, nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, nil, err
	}
	for _, d := range []string{dir, filepath.Join(dir, "work"), filepath.Join(dir, "state"), filepath.Join(dir, homeFolder), filepath.Join(dir, userHomeFolder)} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return "", nil, nil, err
		}
	}
	lock, err := foragerdir.LockFile(filepath.Join(dir, runLockFile))
	if err != nil {
		return "", nil, nil, err
	}
	name := filepath.Join(p.Settings.State, codeFilePrefix+c.DispatchID)
	defer os.Remove(name)
	code, err := writeCode(name, c)
	if err != nil {
		lock.Release()
		return "", nil, nil, err
	}
	return dir, code, lock, nil
}

// writeCode writes c's run code to a new file, mode 0600, at name, and returns the file
// open for reading, with name unlinked.
func writeCode(name string, c provisioner.Claimed) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = c.RunCode.WriteTo(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	code, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(name); err != nil {
		code.Close()
		return nil, err
	}
	return code, nil
}

// dispatched sends dispatched for c, and reports whether it was accepted, with the
// dispatch journaled at its new phase, and, when it was not, whether the dispatch gets
// gone, with its journal entry at ended. A signed 429 is waited out up to
// [maxRateLimited] times while the lease lasts. On 409 lease_expired or 410 gone the
// dispatch is not released, and gets gone: an earlier try of dispatched may have been
// taken with its response lost. Any other end releases the dispatch, as [Pool.giveBack]
// has it: for the pool's stop during a wait, the lease, or a failure. A dispatched that
// succeeded but could not be journaled starts no qory run: Qory Apiary does not release
// the dispatch then, so it gets gone, as one whose qory run could not start does.
func (p *Pool) dispatched(stop context.Context, c provisioner.Claimed) (started, gone bool) {
	id := c.DispatchID
	work := context.WithoutCancel(stop)
	for limited := 0; ; limited++ {
		err := p.Client.Dispatched(work, id)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			if jerr := p.journal.set(Entry{id, PhaseDispatched}); jerr != nil {
				p.log.Error(lineNotJournaled, slog.String("dispatch_id", id), errAttr(jerr))
				p.log.Warn(lineNotStarted, slog.String("dispatch_id", id), errAttr(jerr))
				p.setPhase(id, PhaseEnded)
				return false, true
			}
			return true, false
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if !p.now().Add(r.RetryAfter).Before(c.LeaseUntil) {
				p.giveBack(stop, id, ReasonLeasePassed)
				return false, false
			}
			if p.sleep(stop, r.RetryAfter) != nil {
				p.giveBack(stop, id, ReasonStoppedBeforeStart)
				return false, false
			}
			continue
		}
		p.log.Warn(lineNotRecorded, slog.String("dispatch_id", id), errAttr(err))
		if errors.Is(err, provisioner.ErrLeaseExpired) || errors.Is(err, provisioner.ErrGone) {
			p.setPhase(id, PhaseEnded)
			return false, true
		}
		p.giveBack(stop, id, ReasonNotStarted)
		return false, false
	}
}

// run runs qory run for the dispatched claim c, in dir, with code as its descriptor 3
// and lock's file as its descriptor 4, until it ends, and logs how it ended. Once qory
// run started, the id of its session and its start stamp are written to run.session
// in dir; once it ended, lock is closed. qory run is stopped at stop's end, once the
// claim's max_lifetime_minutes passed, or once the heartbeat finds the dispatch ended.
// Each line of its output goes to the pool's log, after the dispatch id, and is kept.
// Once qory run ended, or could not be started, the dispatch's journal entry is written
// at ended, and the lines kept are uploaded as the dispatch's machine log, on a context
// that does not end with stop; [Pool.end] removes dir next. The output is masked first
// with the process's mask list, [secretmask.Default], which replaces the value of a
// URL's signature or token.
func (p *Pool) run(stop context.Context, c provisioner.Claimed, dir string, code *os.File, lock *foragerdir.Lock) {
	id := slog.String("dispatch_id", c.DispatchID)
	var out outputTail
	ctx, cancel := context.WithCancel(stop)
	defer cancel()
	watch, unwatch := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		p.heartbeat(watch, c.DispatchID, cancel)
	})
	wg.Go(func() {
		if p.sleep(watch, time.Duration(c.MaxLifetimeMinutes)*time.Minute) == nil {
			p.log.Warn(lineLifetime, id, slog.Int("max_lifetime_minutes", c.MaxLifetimeMinutes))
			cancel()
		}
	})
	output := child.NewLineWriter("", func(line string) {
		p.log.Info(c.DispatchID + ": " + line)
		out.add(line)
	})
	// A run.session that could not be written matters only after a crash of the pool:
	// the next start then sends no signal, and logs lineLeftoverHeld after the grace.
	recordSession := func(session int) { _ = writeSession(dir, session) }
	res, err := child.Run(ctx, child.Command{
		Path:       p.Settings.Qory,
		Args:       []string{"run", "--run-code-file", fmt.Sprintf("/dev/fd/%d", child.KeyFD), "--gateway", c.GatewayURL, "--home", filepath.Join(dir, homeFolder)},
		Dir:        filepath.Join(dir, "work"),
		ConfigHome: p.Settings.RunConfig,
		StateHome:  filepath.Join(dir, "state"),
		Home:       filepath.Join(dir, userHomeFolder),
		Runs:       filepath.Join(p.Settings.State, runsFolder),
		File:       code,
		Hold:       lock.File(),
		Session:    recordSession,
		Output:     output,
		Mask:       secretmask.Default(),
		Grace:      p.Settings.Grace,
		Started:    func() { p.log.Info(lineStarted, id) },
	})
	// Close, not Release: a program that qory run started and that outlived it keeps the
	// lock held, so a restart finds it.
	_ = lock.Close()
	unwatch()
	wg.Wait()
	p.setPhase(c.DispatchID, PhaseEnded)
	if err != nil {
		p.log.Warn(lineNotStarted, id, errAttr(err))
	} else {
		attrs := []any{id, slog.Int("exit_code", res.ExitCode)}
		if res.Signal != 0 {
			attrs = append(attrs, slog.String("signal", res.Signal.String()))
		}
		p.log.Info(lineEnded, attrs...)
	}
	log, cut := out.text()
	p.uploadLog(context.WithoutCancel(stop), c.DispatchID, log, cut)
}

// heartbeat sends dispatched for the dispatch id every [heartbeatEvery] until ctx ends.
// On 410 gone or 409 lease_expired it logs that the dispatch ended, calls end and
// returns; any other error is logged, and dispatched is sent again at the next beat.
func (p *Pool) heartbeat(ctx context.Context, id string, end func()) {
	for {
		if p.sleep(ctx, heartbeatEvery) != nil {
			return
		}
		err := p.Client.Dispatched(ctx, id)
		switch {
		case err == nil:
		case ctx.Err() != nil:
			return
		case errors.Is(err, provisioner.ErrGone), errors.Is(err, provisioner.ErrLeaseExpired):
			p.log.Warn(lineDispatchEnded, slog.String("dispatch_id", id), errAttr(err))
			end()
			return
		default:
			p.log.Warn(lineHeartbeatFailed, slog.String("dispatch_id", id), slog.Duration("wait", heartbeatEvery), errAttr(err))
		}
	}
}

// releaseEnd is how a release ended.
type releaseEnd int

const (
	// releaseKept is a release that failed and may succeed later: no response, an
	// unsigned response, a fourth signed 429, a server error, or another refusal. The
	// journal entry stays at its phase, and [Pool.releaseAgain] sends the release again
	// while the pool runs; the next start releases what is left.
	releaseKept releaseEnd = iota
	// releaseOver is a dispatch released, over already, or one Qory Apiary does not
	// release for a signed 400, 403 or 404: the entry goes.
	releaseOver
	// releaseMachine is a release that returned 409 machine_exists: Qory Apiary took
	// dispatched for the dispatch, or the machine redeemed its run code, so the
	// dispatch gets gone.
	releaseMachine
)

// release gives the dispatch id back, for reason, and reports how that ended, with the
// error of a release kept. A signed 429 is waited out up to [maxRateLimited] times. A
// 409 lease_expired or a 410 gone is the dispatch over already; a signed 400, 403 or 404
// is one Qory Apiary does not release, and a 409 machine_exists one that gets gone;
// neither is sent again, and each is logged. A release kept is logged by its caller.
func (p *Pool) release(work context.Context, id, reason string) (releaseEnd, error) {
	ids := slog.String("dispatch_id", id)
	for limited := 0; ; limited++ {
		err := p.Client.Release(work, id, reason)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			p.log.Info(lineReleased, ids, slog.String("reason", reason))
			return releaseOver, nil
		case errors.Is(err, provisioner.ErrLeaseExpired), errors.Is(err, provisioner.ErrGone):
			p.log.Info(lineOverAlready, ids, errAttr(err))
			return releaseOver, nil
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if p.sleep(work, r.RetryAfter) != nil {
				return releaseKept, err
			}
			continue
		}
		machine := errors.Is(err, provisioner.ErrMachineExists)
		if machine {
			p.log.Warn(lineNotReleased, ids, slog.String("reason", reason), errAttr(err))
			return releaseMachine, nil
		}
		if !errors.As(err, &r) {
			return releaseKept, err
		}
		line, ok := releaseEndedLine[r.Status]
		if !ok || !provisioner.EndsAttempts(err) {
			return releaseKept, err
		}
		p.log.Warn(line, ids, slog.String("reason", reason), errAttr(err))
		return releaseOver, nil
	}
}

// giveBack releases the dispatch id for reason, on a context that does not end with
// stop, and acts on how that ended, as [Pool.settle] does. When the release is kept, the
// entry stays at its phase, and [Pool.releaseAgain] sends the release again in a
// goroutine beside the claim loop, which Run waits for. No qory run starts for the
// dispatch.
func (p *Pool) giveBack(stop context.Context, id, reason string) {
	end, err := p.release(context.WithoutCancel(stop), id, reason)
	if !p.settle(stop, id, end) {
		p.wg.Go(func() { p.releaseAgain(stop, id, reason, err) })
	}
}

// settle acts on the end of a release of the dispatch id, and reports whether it was
// settled: a dispatch released or over already leaves the journal, with its folder; one
// whose release returned 409 machine_exists is written at ended, and gets gone beside
// the claim loop. A release kept changes nothing.
func (p *Pool) settle(stop context.Context, id string, end releaseEnd) bool {
	switch end {
	case releaseOver:
		p.forget(id)
	case releaseMachine:
		p.setPhase(id, PhaseEnded)
		p.endBeside(stop, id)
	default:
		return false
	}
	return true
}

// releaseAgain sends the release of the dispatch id, for reason, again after a wait,
// until it is settled, as [Pool.settle] has it, or stop ends. err is the error of the
// release before. The wait is a signed 429's Retry-After, else one that doubles from
// [firstBackoff] up to [maxBackoff], as gone's is. Once stop ends, the entry stays at its
// phase, and the next start releases it.
func (p *Pool) releaseAgain(stop context.Context, id, reason string, err error) {
	ids, why := slog.String("dispatch_id", id), slog.String("reason", reason)
	work := context.WithoutCancel(stop)
	wait := firstBackoff
	for {
		if stop.Err() != nil {
			p.log.Warn(lineReleaseKept, ids, why, errAttr(err))
			return
		}
		pause := wait
		var r *provisioner.Refusal
		if errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) {
			pause = r.RetryAfter
		} else {
			wait = min(wait*2, maxBackoff)
		}
		p.log.Warn(fmt.Sprintf(lineReleaseFailed, pause), ids, why, slog.Duration("wait", pause), errAttr(err))
		if p.sleep(stop, pause) != nil {
			continue
		}
		var end releaseEnd
		if end, err = p.release(work, id, reason); p.settle(stop, id, end) {
			return
		}
	}
}

// endBeside runs [Pool.end] for the dispatch id in a goroutine, beside the claim loop,
// which Run waits for.
func (p *Pool) endBeside(stop context.Context, id string) {
	p.wg.Go(func() { p.end(stop, id) })
}

// end removes the folder of the dispatch id, whose journal entry is at ended, if it has
// one, sends gone for it, and takes the dispatch out of the journal once gone is
// settled; otherwise the entry stays, for the next start.
func (p *Pool) end(stop context.Context, id string) {
	_ = os.RemoveAll(p.dispatchDir(id))
	if p.gone(stop, id) {
		p.forget(id)
	}
}

// gone sends the machine end gone for the dispatch id, one call at a time, and reports
// whether it is settled: it got a signed 204, 400, 403 or 404, after which the same call
// cannot succeed. Any other failure is sent again, built anew,
// after a wait: a signed 429's Retry-After, else one that doubles from [firstBackoff] up
// to [maxBackoff]. Once stop ends, gone is sent once more, for at most
// [lastGoneTimeout], and a failure then leaves it unsettled.
func (p *Pool) gone(stop context.Context, id string) bool {
	ids := slog.String("dispatch_id", id)
	wait := firstBackoff
	for {
		last := stop.Err() != nil
		var ctx context.Context
		var cancel context.CancelFunc
		if last {
			ctx, cancel = context.WithTimeout(context.WithoutCancel(stop), lastGoneTimeout)
		} else {
			ctx, cancel = context.WithCancel(stop)
		}
		err := p.Client.MachineEnd(ctx, id, contracts.MachineEndGone)
		cancel()
		var r *provisioner.Refusal
		switch {
		case err == nil:
			p.log.Info(lineGone, ids)
			return true
		case provisioner.EndsAttempts(err) && errors.As(err, &r):
			p.log.Warn(goneEndedLine[r.Status], ids, errAttr(err))
			return true
		case last:
			p.log.Warn(lineGoneKept, ids, errAttr(err))
			return false
		case stop.Err() != nil:
			// The stop cut this send short; the last one follows.
			continue
		}
		pause := wait
		if errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) {
			pause = r.RetryAfter
		} else {
			wait = min(wait*2, maxBackoff)
		}
		p.log.Warn(fmt.Sprintf(lineGoneFailed, pause), ids, slog.Duration("wait", pause), errAttr(err))
		_ = p.sleep(stop, pause)
	}
}

// goneEndedLine is the line of a gone that got each signed status after which
// [provisioner.EndsAttempts] ends its attempts.
var goneEndedLine = map[int]string{
	http.StatusBadRequest: lineGone400,
	http.StatusForbidden:  lineGone403,
	http.StatusNotFound:   lineGone404,
}

// releaseEndedLine is the line of a release that got each signed status after which
// [provisioner.EndsAttempts] ends its attempts.
var releaseEndedLine = map[int]string{
	http.StatusBadRequest: lineRelease400,
	http.StatusForbidden:  lineRelease403,
	http.StatusNotFound:   lineRelease404,
}

// setPhase writes the dispatch id's journal entry at phase, and logs a write that
// failed.
func (p *Pool) setPhase(id, phase string) {
	if err := p.journal.set(Entry{id, phase}); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", id), errAttr(err))
	}
}

// forget takes the dispatch id out of the journal, and removes its folder.
func (p *Pool) forget(id string) {
	if err := p.journal.remove(id); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", id), errAttr(err))
	}
	_ = os.RemoveAll(p.dispatchDir(id))
}

func (p *Pool) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pool) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
