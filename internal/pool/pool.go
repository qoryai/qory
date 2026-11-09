// Package pool is the pool program's loop, what qory run pool runs: it claims a node
// pool's dispatches from Qory Apiary with the pool's access key, and starts a qory run
// for each on this machine, the same command a machine that a provisioner creates runs.
//
// For each dispatch claimed it checks the claim: one whose machine member is set, or
// whose lease passed, is released. It writes the run code to a file, mode 0600, in the
// pool's folder, opens the file for reading and unlinks it, so no file name leads to the
// code. It sends dispatched, then starts qory run --run-code-file /dev/fd/3 --gateway
// <gateway_url> --home <dispatch's folder>/home, with the open file as descriptor 3, in
// an empty folder work/ of the dispatch's folder <pool's folder>/runs/<dispatch_id>,
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
// succeeds. The ended entry is written before the first gone: once qory run ended or
// could not be started, or after the release or the dispatched that the gone follows. A
// release of an entry left at claimed after a dispatched that succeeded returns 409
// machine_exists, and the pool sends gone for it. A release that fails with no response,
// an unsigned response, a fourth signed 429, a server error or another refusal than a
// signed 400, 403 or 404 leaves the entry at claimed, and is sent again beside the claim
// loop after a wait, as gone is, until it is settled; no qory run starts for that
// dispatch, and it takes none of the pool's Runs. The dispatch's folder is removed
// before its first gone: once qory run ended, after the upload of its machine log, or
// at once when no qory run started. The entry is removed once the dispatch ends; an
// ended entry once its gone got a signed 204, 400, 403 or 404. When the program starts,
// before its first claim, it runs the check of [Probe]: when a container on the
// engine's default bridge network connects to the cloud metadata service, or the check
// cannot run, Run returns its error and claims nothing. Then each claimed entry it
// finds is released and each dispatched entry is dropped, with the folders of both.
// Each ended entry, and each claimed one whose release returns 409 machine_exists, gets
// gone, sent as above in a separate goroutine beside the claim loop, so the first claim
// does not wait for it; a release that fails is sent again there too.
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
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
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
// gone: a claimed one is released, a dispatched one, which Qory Apiary does not
// release, is dropped, and an ended one gets gone. A dispatch released, over already or
// dropped leaves the journal, and its folder is removed. A claimed one whose release
// returned 409 machine_exists is written at ended, and gets gone too; one whose release
// failed stays at claimed, and its release is sent again. Each gone and each release sent
// again goes out beside the claim loop, as [Pool.giveBack] has it. Once ctx ends, the
// rest stay.
func (p *Pool) replay(ctx context.Context) {
	for _, e := range p.journal.list() {
		if ctx.Err() != nil {
			return
		}
		switch e.Phase {
		case PhaseEnded:
			p.endBeside(ctx, e.DispatchID)
		case PhaseClaimed:
			p.giveBack(ctx, e.DispatchID, ReasonRestarted)
		default:
			p.forget(e.DispatchID)
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
	dir, code, err := p.prepare(c)
	if err != nil {
		p.log.Warn(lineNotStarted, slog.String("dispatch_id", c.DispatchID), errAttr(err))
		p.giveBack(stop, c.DispatchID, ReasonNotStarted)
		return false
	}
	defer code.Close()
	if stop.Err() != nil {
		p.giveBack(stop, c.DispatchID, ReasonStoppedBeforeStart)
		return false
	}
	if started, gone := p.dispatched(stop, c); !started {
		return gone
	}
	p.run(stop, c, dir, code)
	return true
}

// dispatchDir is the folder of the dispatch id: <pool's folder>/runs/<id>.
func (p *Pool) dispatchDir(id string) string {
	return filepath.Join(p.Settings.State, runsFolder, id)
}

// prepare makes the dispatch's folder of c, mode 0700, with work/, empty, state/, qory
// run's XDG_STATE_HOME, home/, its --home, and home-user/, its HOME, each mode 0700, and
// returns it with c's run code open for reading. The code is written to a new file, mode
// 0600, in the pool's folder, which is opened for reading and unlinked before prepare
// returns, on every path; c's run code is cleared.
func (p *Pool) prepare(c provisioner.Claimed) (string, *os.File, error) {
	defer c.RunCode.Clear()
	dir := p.dispatchDir(c.DispatchID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", nil, err
	}
	for _, d := range []string{dir, filepath.Join(dir, "work"), filepath.Join(dir, "state"), filepath.Join(dir, homeFolder), filepath.Join(dir, userHomeFolder)} {
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
// dispatch journaled at its new phase, and, when it was not, whether the dispatch gets
// gone, with its journal entry at ended. A signed 429 is waited out up to
// [maxRateLimited] times while the lease lasts. On 409 lease_expired or 410 gone the
// dispatch is not released, and gets gone: an earlier try of dispatched may have been
// taken with its answer lost. Any other end releases the dispatch, as [Pool.giveBack]
// has it: for the pool's stop during a wait, the lease, or a failure.
func (p *Pool) dispatched(stop context.Context, c provisioner.Claimed) (started, gone bool) {
	id := c.DispatchID
	work := context.WithoutCancel(stop)
	for limited := 0; ; limited++ {
		err := p.Client.Dispatched(work, id)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			p.setPhase(id, PhaseDispatched)
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

// run runs qory run for the dispatched claim c, in dir, with code as its descriptor 3,
// until it ends, and logs how it ended. qory run is stopped at stop's end, once the
// claim's max_lifetime_minutes passed, or once the heartbeat finds the dispatch ended.
// Each line of its output goes to the pool's log, after the dispatch id, and is kept.
// Once qory run ended, or could not be started, the dispatch's journal entry is written
// at ended, and the lines kept are uploaded as the dispatch's machine log, on a context
// that does not end with stop; [Pool.end] removes dir next. The output is masked first
// with the process's mask list, [secretmask.Default], which replaces the value of a
// URL's signature or token.
func (p *Pool) run(stop context.Context, c provisioner.Claimed, dir string, code *os.File) {
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
	res, err := child.Run(ctx, child.Command{
		Path:       p.Settings.Qory,
		Args:       []string{"run", "--run-code-file", fmt.Sprintf("/dev/fd/%d", child.KeyFD), "--gateway", c.GatewayURL, "--home", filepath.Join(dir, homeFolder)},
		Dir:        filepath.Join(dir, "work"),
		ConfigHome: p.Settings.RunConfig,
		StateHome:  filepath.Join(dir, "state"),
		Home:       filepath.Join(dir, userHomeFolder),
		Runs:       filepath.Join(p.Settings.State, runsFolder),
		File:       code,
		Output:     output,
		Mask:       secretmask.Default(),
		Grace:      p.Settings.Grace,
		Started:    func() { p.log.Info(lineStarted, id) },
	})
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
		if !machine && !provisioner.EndsAttempts(err) {
			return releaseKept, err
		}
		p.log.Warn(lineNotReleased, ids, slog.String("reason", reason), errAttr(err))
		if machine {
			return releaseMachine, nil
		}
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
