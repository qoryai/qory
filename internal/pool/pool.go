// Package pool is the pool program's loop, what qory run pool runs: it claims a node
// pool's dispatches from Qory Apiary with the pool's access key, and starts a qory run
// for each on this machine, the same command a machine that a provisioner creates runs.
//
// For each dispatch claimed it checks the claim: one whose machine member is set, or
// whose lease passed, is released. It writes the run code to a file, mode 0600, in the
// pool's folder, opens the file for reading and unlinks it, so no file name leads to the
// code. It sends dispatched, then starts qory run --run-code-file /dev/fd/3 --gateway
// <gateway_url>, with the open file as descriptor 3, in an empty folder work/ of the
// dispatch's folder <pool's folder>/runs/<dispatch_id>, with the environment of
// [child.Env]: the pool's access key never reaches it. qory run redeems the code and
// reports the run's outcome itself; the pool posts none. While qory run runs, the pool
// sends dispatched again every [heartbeatEvery], and stops qory run once Qory Apiary
// returns 410 gone or 409 lease_expired to it, or once the claim's max_lifetime_minutes
// passed. Once qory run ends, the pool uploads its output as the dispatch's machine
// log, once: its lines as the pool's log has them, at most the last 768 KiB, with each
// line that contains what looks like a secret replaced. A dispatched dispatch is never
// released: Qory Apiary returns 409 machine_exists to a release after dispatched.
//
// A journal, <pool's folder>/dispatches, contains each live dispatch's id and phase,
// claimed or dispatched, never a run code. The claimed entry is written and synced
// before the request dispatched, and the dispatched entry after that request succeeds.
// A release of an entry left at claimed after a dispatched that succeeded returns 409
// machine_exists, and the pool drops the entry. The entry is removed once it ends.
// When the program starts, before its first claim, each claimed entry it finds is
// released and each dispatched entry is dropped, with the folders of both.
//
// When ctx ends, the pool stops: it claims no more, a claim being sent is given up, a
// dispatch not yet dispatched is released, and each qory run is stopped as [child.Run]
// stops a command. Run returns once every dispatch ended.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/provisioner"
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
)

// The pool's folder's entries besides the journal: the folder of the dispatches'
// folders, and the prefix of a run code's file, which is unlinked once it is open.
const (
	runsFolder     = "runs"
	codeFilePrefix = "runcode-"
)

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

	log     *slog.Logger
	journal *journal
	wg      sync.WaitGroup
	slots   chan struct{}
}

// Run runs the pool until ctx ends, then stops it as the package describes, and returns
// nil once every dispatch ended. Its error is for a pool that could not start: settings
// it cannot run by, or a journal it cannot read, in which case no run is claimed.
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
	p.replay(ctx)
	p.slots = make(chan struct{}, s.Runs)
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
// release, is dropped. A dispatch released, over already or dropped leaves the journal,
// and its folder is removed; one Qory Apiary could not be reached for stays, for the
// next start. Once ctx ends, the rest stay.
func (p *Pool) replay(ctx context.Context) {
	work := context.WithoutCancel(ctx)
	for _, e := range p.journal.list() {
		if ctx.Err() != nil {
			return
		}
		if e.Phase == PhaseClaimed && !p.release(work, e.DispatchID, ReasonRestarted) {
			continue
		}
		p.forget(e.DispatchID)
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
			defer func() { <-p.slots }()
			p.dispatch(ctx, c)
		}()
	}
}

// admit journals the claim c and checks it before its dispatch starts, and reports
// whether it starts. A claim whose machine member is set, whose lease passed, or that
// could not be journaled is released, and its run code cleared.
func (p *Pool) admit(ctx context.Context, c provisioner.Claimed) bool {
	work := context.WithoutCancel(ctx)
	if err := p.journal.set(Entry{c.DispatchID, PhaseClaimed}); err != nil {
		c.RunCode.Clear()
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		p.release(work, c.DispatchID, ReasonNotStarted)
		return false
	}
	reason := ""
	switch {
	case c.Machine != "":
		reason = ReasonMachine
	case !p.now().Before(c.LeaseUntil):
		reason = ReasonLeasePassed
	}
	if reason == "" {
		return true
	}
	c.RunCode.Clear()
	p.release(work, c.DispatchID, reason)
	p.forget(c.DispatchID)
	return false
}

// dispatch takes the claim c from its folder and run code's file through dispatched to
// its qory run's end, and then out of the journal. stop is the pool's context: once it
// ends, no request is sent again after a wait, and qory run is stopped; the requests
// already decided go out on a context that does not end with it.
func (p *Pool) dispatch(stop context.Context, c provisioner.Claimed) {
	work := context.WithoutCancel(stop)
	defer p.forget(c.DispatchID)
	dir, code, err := p.prepare(c)
	if err != nil {
		p.log.Warn(lineNotStarted, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		p.release(work, c.DispatchID, ReasonNotStarted)
		return
	}
	defer code.Close()
	if stop.Err() != nil {
		p.release(work, c.DispatchID, ReasonStoppedBeforeStart)
		return
	}
	if !p.dispatched(stop, work, c) {
		return
	}
	p.run(stop, c, dir, code)
}

// dispatchDir is the folder of the dispatch id: <pool's folder>/runs/<id>.
func (p *Pool) dispatchDir(id string) string {
	return filepath.Join(p.Settings.State, runsFolder, id)
}

// prepare makes the dispatch's folder of c, mode 0700, with work/, empty, and state/,
// qory run's XDG_STATE_HOME, and returns it with c's run code open for reading. The code
// is written to a new file, mode 0600, in the pool's folder, which is opened for reading
// and unlinked before prepare returns, on every path; c's run code is cleared.
func (p *Pool) prepare(c provisioner.Claimed) (string, *os.File, error) {
	defer c.RunCode.Clear()
	dir := p.dispatchDir(c.DispatchID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, err
	}
	for _, d := range []string{dir, filepath.Join(dir, "work"), filepath.Join(dir, "state")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return "", nil, err
		}
	}
	name := filepath.Join(p.Settings.State, codeFilePrefix+c.DispatchID)
	defer os.Remove(name)
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	_, err = c.RunCode.WriteTo(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", nil, err
	}
	code, err := os.Open(name)
	if err != nil {
		return "", nil, err
	}
	if err := os.Remove(name); err != nil {
		code.Close()
		return "", nil, err
	}
	return dir, code, nil
}

// dispatched sends dispatched for c, and reports whether it was accepted, with the
// dispatch journaled at its new phase. A signed 429 is waited out up to
// [maxRateLimited] times while the lease lasts. On 409 lease_expired or 410 gone the
// dispatch is dropped, with no release. Any other end releases the dispatch: for the
// pool's stop during a wait, the lease, or a failure.
func (p *Pool) dispatched(stop, work context.Context, c provisioner.Claimed) bool {
	for limited := 0; ; limited++ {
		err := p.Client.Dispatched(work, c.DispatchID)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			if err := p.journal.set(Entry{c.DispatchID, PhaseDispatched}); err != nil {
				p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), errAttr(err))
			}
			return true
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if !p.now().Add(r.RetryAfter).Before(c.LeaseUntil) {
				p.release(work, c.DispatchID, ReasonLeasePassed)
				return false
			}
			if p.sleep(stop, r.RetryAfter) != nil {
				p.release(work, c.DispatchID, ReasonStoppedBeforeStart)
				return false
			}
			continue
		}
		p.log.Warn(lineNotRecorded, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		if !errors.Is(err, provisioner.ErrLeaseExpired) && !errors.Is(err, provisioner.ErrGone) {
			p.release(work, c.DispatchID, ReasonNotStarted)
		}
		return false
	}
}

// run runs qory run for the dispatched claim c, in dir, with code as its descriptor 3,
// until it ends, and logs how it ended. qory run is stopped at stop's end, once the
// claim's max_lifetime_minutes passed, or once the heartbeat finds the dispatch ended.
// Each line of its output goes to the pool's log, after the dispatch id, and is kept;
// once qory run ended, or could not be started, the lines kept are uploaded as the
// dispatch's machine log, on a context that does not end with stop.
func (p *Pool) run(stop context.Context, c provisioner.Claimed, dir string, code *os.File) {
	id := slog.String("dispatch_id", c.DispatchID)
	var out outputTail
	defer func() {
		log, cut := out.text()
		p.uploadLog(context.WithoutCancel(stop), c.DispatchID, log, cut)
	}()
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
	res, err := child.Run(ctx, child.Command{
		Path:       p.Settings.Qory,
		Args:       []string{"run", "--run-code-file", fmt.Sprintf("/dev/fd/%d", child.KeyFD), "--gateway", c.GatewayURL},
		Dir:        filepath.Join(dir, "work"),
		ConfigHome: p.Settings.RunConfig,
		StateHome:  filepath.Join(dir, "state"),
		Runs:       filepath.Join(p.Settings.State, runsFolder),
		File:       code,
		Output:     output,
		Grace:      p.Settings.Grace,
		Started:    func() { p.log.Info(lineStarted, id) },
	})
	unwatch()
	wg.Wait()
	if err != nil {
		p.log.Warn(lineNotStarted, id, errAttr(err))
		return
	}
	attrs := []any{id, slog.Int("exit_code", res.ExitCode)}
	if res.Signal != 0 {
		attrs = append(attrs, slog.String("signal", res.Signal.String()))
	}
	p.log.Info(lineEnded, attrs...)
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

// release gives the dispatch id back, for reason, and reports whether the dispatch is
// over: released, or over already. A signed 429 is waited out up to [maxRateLimited]
// times. A 409 lease_expired or a 410 gone is the dispatch over already; a signed 403,
// 404 or 409 machine_exists is one Qory Apiary does not release, which is not sent
// again either.
func (p *Pool) release(work context.Context, id, reason string) bool {
	ids := slog.String("dispatch_id", id)
	for limited := 0; ; limited++ {
		err := p.Client.Release(work, id, reason)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			p.log.Info(lineReleased, ids, slog.String("reason", reason))
			return true
		case errors.Is(err, provisioner.ErrLeaseExpired), errors.Is(err, provisioner.ErrGone):
			p.log.Info(lineOverAlready, ids, errAttr(err))
			return true
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if p.sleep(work, r.RetryAfter) != nil {
				return false
			}
			continue
		}
		p.log.Warn(lineNotReleased, ids, slog.String("reason", reason), errAttr(err))
		return errors.Is(err, provisioner.ErrForbidden) || errors.Is(err, provisioner.ErrNotFound) || errors.Is(err, provisioner.ErrMachineExists)
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
