// Package pool is the pool program's loop, what qory run pool runs: it claims a node
// pool's runs from Qory Apiary with the pool's access key, starts each on this machine,
// and says how each ended.
//
// For each run claimed it checks the step and the lease, says the run's job started
// with dispatched, asks for the run key with the handover, starts the run, an explore
// run with [explore.Launch] or a build with the [PrebuildLauncher], then posts its
// outcome or releases it, as the run's [explore.Ending] says. A run is released with
// retry true before its handover alone: after it, a release ends the run, and retry
// true would queue a new one. A run that started and failed posts its outcome alone.
//
// A journal, <pool's folder>/dispatches, holds each live dispatch's ids, step and
// phase, never a run key or inputs. It is written and synced before each request that
// changes a dispatch's phase, and the dispatch's entry is removed once it ends. When
// the program starts, before its first claim, each entry it finds is released: retry
// true for a run whose handover was not sent, retry false for one whose handover may
// have been sent, or that was handed over or started, whose run key is gone with the
// program that held it.
//
// When ctx ends, the pool stops: it claims no more, a claim being held is given up, a
// dispatch whose run is not handed over yet is released with retry true, one handed
// over and not started with retry false, and each running run is stopped through the
// context its launcher got. Run returns once every dispatch ended.
package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/qoryai/forager/server"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner"
)

// The waits of the loop: a claim that failed is sent again after a wait that doubles
// from firstBackoff up to maxBackoff, and a handover up to maxHandoverWait.
const (
	firstBackoff    = time.Second
	maxBackoff      = time.Minute
	maxHandoverWait = 30 * time.Second
	// maxRateLimited is how many signed 429s dispatched, a handover or a release
	// waits out before it gives up.
	maxRateLimited = 3
	// outcomeTries is how many times an outcome is posted, the same bytes each time,
	// before it is logged as not posted.
	outcomeTries = 3
)

// Settings are what the pool runs by.
type Settings struct {
	// Runs is how many dispatches may be live at once, at least 1; Prebuilds is how
	// many of them may be builds, from 1 to Runs.
	Runs, Prebuilds int
	// State is the pool's folder, an absolute path, mode 0700, which holds the
	// journal.
	State string
	// Explore is what every explore run is launched with; its State is the folder the
	// run directories are under.
	Explore explore.Settings
}

// Prebuild is one run of the step prebuild, as its handover gave it.
type Prebuild struct {
	RunID string
	// Target is the signed, checked handover's target, whose code the build is of; the
	// handover of a prebuild always has one.
	Target contracts.Target
	// Key is the run key; the launcher clears it on every path.
	Key    child.Secret
	Inputs []byte
	// Report sends prebuilt for this dispatch; the launcher calls it once, after its
	// push.
	Report func(context.Context, provisioner.Image) error
}

// PrebuildLauncher builds the environment of a prebuild, until it ends or ctx ends,
// and says what the pool does next. At ctx's end the build is stopped.
type PrebuildLauncher func(ctx context.Context, d Prebuild) explore.Ending

// Outcomes is where a run's outcome is posted: Forager's client of the server, under
// the pool's access key. Discover reads the signed discovery, whose run.url the
// outcome goes to.
type Outcomes interface {
	Discover(ctx context.Context) (*server.Configuration, string, error)
	PostOutcome(ctx context.Context, runURL, runID string, o server.Outcome) error
}

// Pool is the pool program's loop and what it reaches the world through.
type Pool struct {
	Settings Settings
	// Client is the provisioner contract's client, signed with the pool's access key.
	Client *provisioner.Client
	// Outcomes posts a run's outcome.
	Outcomes Outcomes
	// Explore launches an explore run; nil is [explore.Launch].
	Explore func(context.Context, explore.Settings, explore.Run) (explore.Result, error)
	// Prebuild launches a build.
	Prebuild PrebuildLauncher
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

	mu        sync.Mutex
	prebuilds int

	// discoverMu guards discovered, the second of the clock the last discovery was
	// signed in, or the pool started in.
	discoverMu sync.Mutex
	discovered time.Time
}

// Run runs the pool until ctx ends, then stops it as the package says, and returns nil
// once every dispatch ended. Its error is for a pool that could not start: settings it
// cannot run by, or a journal it cannot read, in which case no run is claimed.
func (p *Pool) Run(ctx context.Context) error {
	s := p.Settings
	switch {
	case s.Runs < 1 || s.Prebuilds < 1 || s.Prebuilds > s.Runs:
		return errors.New("the pool runs at least 1 run at once, and from 1 build to as many as its runs")
	case !filepath.IsAbs(s.State):
		return fmt.Errorf("the pool's folder %s is not an absolute path", exports.Value(s.State, true))
	case p.Client == nil || p.Outcomes == nil:
		return errors.New("the pool has no client of Qory Apiary")
	case p.Prebuild == nil:
		return errors.New("the pool has no launcher of builds")
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
	j, err := openJournal(s.State)
	if err != nil {
		return err
	}
	p.journal = j
	p.discovered = time.Now().Truncate(time.Second)
	p.replay(ctx)
	p.slots = make(chan struct{}, s.Runs)
	p.claimLoop(ctx)
	p.wg.Wait()
	return nil
}

// replay releases each dispatch the journal holds, as the program that held it is
// gone: retry true for one claimed or dispatched, whose handover was not sent, and retry
// false for one at its handover, handed over or started, whose run key may have been
// given and is lost. A dispatch released, or over already, leaves the
// journal and its run directory is removed; one Qory Apiary could not be reached for
// stays, for the next start. Once ctx ends, the rest stay.
func (p *Pool) replay(ctx context.Context) {
	work := context.WithoutCancel(ctx)
	for _, e := range p.journal.list() {
		if ctx.Err() != nil {
			return
		}
		retry := e.Phase == PhaseClaimed || e.Phase == PhaseDispatched
		if !p.release(work, e.DispatchID, e.RunID, ReasonRestarted, retry) {
			continue
		}
		p.forget(e.DispatchID, e.RunID)
	}
}

// claimLoop claims runs while fewer than Runs dispatches are live, and starts a
// dispatch for each, until ctx ends. A claim that failed is sent again after a wait: a
// signed 429's Retry-After, else one that doubles from [firstBackoff] up to
// [maxBackoff], after no answer, an answer not signed under the pin, a 401 among them,
// a server error, a 409 no_gateway or any other refusal.
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
		p.log.Info(lineClaimed, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), slog.String("step", c.Step))
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
// whether it starts. A step this machine does not run is released with retry false; a
// lease that passed and a prebuild over the share of builds are released with retry
// true. A claim that could not be journaled is released with retry true.
func (p *Pool) admit(ctx context.Context, c provisioner.Claimed) bool {
	work := context.WithoutCancel(ctx)
	if !stepShape.MatchString(c.Step) {
		p.release(work, c.DispatchID, c.RunID, ReasonStep, false)
		return false
	}
	if err := p.journal.set(Entry{c.DispatchID, c.RunID, c.Step, PhaseClaimed}); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
		p.release(work, c.DispatchID, c.RunID, ReasonNotStarted, true)
		return false
	}
	reason, retry := "", false
	switch {
	case c.Step != provisioner.StepExplore && c.Step != provisioner.StepPrebuild:
		reason = ReasonStep
	case !p.now().Before(c.LeaseUntil):
		reason, retry = ReasonLeasePassed, true
	case c.Step == provisioner.StepPrebuild:
		p.mu.Lock()
		over := p.prebuilds >= p.Settings.Prebuilds
		if !over {
			p.prebuilds++
		}
		p.mu.Unlock()
		if over {
			reason, retry = ReasonPrebuildCap, true
		}
	}
	if reason == "" {
		return true
	}
	p.release(work, c.DispatchID, c.RunID, reason, retry)
	p.forget(c.DispatchID, c.RunID)
	return false
}

// dispatch takes the claim c from dispatched to its run's end, and then out of the
// journal. stop is the pool's context: once it ends, no request is sent again after a
// wait, and the run is stopped; the requests already decided go out on a context that
// does not end with it.
func (p *Pool) dispatch(stop context.Context, c provisioner.Claimed) {
	work := context.WithoutCancel(stop)
	if c.Step == provisioner.StepPrebuild {
		defer func() {
			p.mu.Lock()
			p.prebuilds--
			p.mu.Unlock()
		}()
	}
	defer p.forget(c.DispatchID, c.RunID)
	if !p.dispatched(stop, work, c) {
		return
	}
	// The handover is journaled before it is sent: once sent, the run may be handed
	// over, and a replay must never queue it again.
	if err := p.journal.set(Entry{c.DispatchID, c.RunID, c.Step, PhaseHandover}); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
		p.release(work, c.DispatchID, c.RunID, ReasonNotStarted, true)
		return
	}
	h, ok := p.handover(stop, work, c)
	if !ok {
		return
	}
	key := &runKey{held: h.RunKey}
	defer key.Clear()
	if err := p.journal.set(Entry{c.DispatchID, c.RunID, c.Step, PhaseHandedOver}); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
	}
	if stop.Err() != nil {
		p.release(work, c.DispatchID, c.RunID, ReasonStoppedBeforeStart, false)
		return
	}
	if err := p.journal.set(Entry{c.DispatchID, c.RunID, c.Step, PhaseStarted}); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
		p.release(work, c.DispatchID, c.RunID, ReasonNotStarted, false)
		return
	}
	p.log.Info(lineStarted, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), slog.String("step", c.Step))
	var end explore.Ending
	if c.Step == provisioner.StepPrebuild {
		end = p.Prebuild(stop, Prebuild{
			RunID: c.RunID, Target: *h.Target, Key: key, Inputs: h.Inputs,
			Report: func(ctx context.Context, img provisioner.Image) error {
				return p.Client.Prebuilt(ctx, c.DispatchID, c.RunID, img)
			},
		})
	} else {
		launch := p.Explore
		if launch == nil {
			launch = explore.Launch
		}
		res, err := launch(stop, p.Settings.Explore, explore.Run{
			RunID: c.RunID, Key: key, Domain: h.Domain, RunSettings: h.RunSettings, Inputs: h.Inputs,
		})
		if err != nil {
			p.log.Warn(lineNotStarted, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
		}
		end = res.Ending
	}
	p.ended(stop, work, c, end)
}

// ended acts on the ending of the run of c, which was handed over: it releases the
// run, with retry false, for the launcher's reason, and posts its outcome, as end says.
// A run that could not be started gives the pool's stop as its reason,
// [ReasonStoppedBeforeStart], once the pool stopped; a build stopped is the prebuild
// launcher's to say.
func (p *Pool) ended(stop, work context.Context, c provisioner.Claimed, end explore.Ending) {
	if end.Release {
		reason := end.ReleaseReason
		if reason == "" {
			reason = ReasonNotStarted
		}
		if reason == ReasonNotStarted && stop.Err() != nil {
			reason = ReasonStoppedBeforeStart
		}
		p.release(work, c.DispatchID, c.RunID, reason, false)
	}
	if end.Post {
		p.post(work, c, end.State, end.Reason)
	}
}

// dispatched sends dispatched for c, and reports whether it was answered, with the
// dispatch journaled at its new phase. A signed 429 is waited out up to
// [maxRateLimited] times while the lease lasts. On 409 lease_expired or 410 gone the
// dispatch is dropped, with no release. Any other end releases the run with retry
// true: for the pool's stop during a wait, the lease, or a failure. A stop once it was
// answered is the handover's to act on.
func (p *Pool) dispatched(stop, work context.Context, c provisioner.Claimed) bool {
	for limited := 0; ; limited++ {
		err := p.Client.Dispatched(work, c.DispatchID)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			if err := p.journal.set(Entry{c.DispatchID, c.RunID, c.Step, PhaseDispatched}); err != nil {
				p.log.Error(lineNotJournaled, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
				p.release(work, c.DispatchID, c.RunID, ReasonNotStarted, true)
				return false
			}
			return true
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if !p.now().Add(r.RetryAfter).Before(c.LeaseUntil) {
				p.release(work, c.DispatchID, c.RunID, ReasonLeasePassed, true)
				return false
			}
			if p.sleep(stop, r.RetryAfter) != nil {
				p.release(work, c.DispatchID, c.RunID, ReasonStoppedBeforeStart, true)
				return false
			}
			continue
		}
		p.log.Warn(lineNotRecorded, slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID), errAttr(err))
		if !errors.Is(err, provisioner.ErrLeaseExpired) && !errors.Is(err, provisioner.ErrGone) {
			p.release(work, c.DispatchID, c.RunID, ReasonNotStarted, true)
		}
		return false
	}
}

// handover sends c's handover until it is answered with the signed 200 for the claim's
// run and step, which it returns, while the lease lasts. A 409 no_gateway, a signed 503
// unavailable, no answer, an answer not signed under the pin or over its bound, and any
// other server error send it again after a wait that doubles from [firstBackoff] up to
// [maxHandoverWait]; a signed 429 after its Retry-After, up to [maxRateLimited] times.
// On 409 lease_expired, 410 gone or 403 forbidden the dispatch is dropped, with no
// release. On 409 handed_over the run is released with retry true, and its run key is
// never recovered. A 200 that is not the claim's run or step, or that breaks the
// contract, is a handover that took effect, so the run is released with retry false and
// its run key cleared: for the pool's other step, explore for a prebuild claimed or the
// reverse, as handed over for another step; for a step that is neither, as a step the
// pool program does not run.
// Any other end releases the run with retry true: for the pool's stop, the lease, or a
// failure.
func (p *Pool) handover(stop, work context.Context, c provisioner.Claimed) (provisioner.Handed, bool) {
	wait, limited := firstBackoff, 0
	ids := []any{slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID)}
	for {
		if stop.Err() != nil {
			p.release(work, c.DispatchID, c.RunID, ReasonStoppedBeforeStart, true)
			return provisioner.Handed{}, false
		}
		if !p.now().Before(c.LeaseUntil) {
			p.release(work, c.DispatchID, c.RunID, ReasonLeasePassed, true)
			return provisioner.Handed{}, false
		}
		ctx, cancel := context.WithDeadline(work, c.LeaseUntil)
		h, err := p.Client.Handover(ctx, c.DispatchID, c.RunID)
		// The lease is over when its context ended, not when err says a deadline passed:
		// a client's timeout says so too, while the lease lasts.
		leaseOver := ctx.Err() != nil
		cancel()
		var r *provisioner.Refusal
		var pause time.Duration
		switch {
		case err == nil && h.RunID != c.RunID:
			h.RunKey.Clear()
			p.release(work, c.DispatchID, c.RunID, ReasonNotHanded, false)
			return provisioner.Handed{}, false
		case err == nil && h.Step != c.Step:
			h.RunKey.Clear()
			reason := ReasonStep
			if h.Step == provisioner.StepExplore || h.Step == provisioner.StepPrebuild {
				reason = ReasonOtherStep
			}
			p.release(work, c.DispatchID, c.RunID, reason, false)
			return provisioner.Handed{}, false
		case err == nil:
			return h, true
		case errors.Is(err, provisioner.ErrHandedOver):
			p.release(work, c.DispatchID, c.RunID, ReasonHandedOver, true)
			return provisioner.Handed{}, false
		case errors.Is(err, provisioner.ErrLeaseExpired), errors.Is(err, provisioner.ErrGone):
			p.log.Info(lineOverAlready, append(ids, errAttr(err))...)
			return provisioner.Handed{}, false
		case errors.Is(err, provisioner.ErrForbidden):
			p.log.Warn(lineHandoverRefused, append(ids, errAttr(err))...)
			return provisioner.Handed{}, false
		case errors.Is(err, provisioner.ErrAnswerUnusable):
			p.release(work, c.DispatchID, c.RunID, ReasonNotHanded, false)
			return provisioner.Handed{}, false
		case leaseOver:
			p.release(work, c.DispatchID, c.RunID, ReasonLeasePassed, true)
			return provisioner.Handed{}, false
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r):
			if limited++; limited > maxRateLimited {
				p.release(work, c.DispatchID, c.RunID, ReasonNotHanded, true)
				return provisioner.Handed{}, false
			}
			pause = r.RetryAfter
		case transient(err):
			pause, wait = wait, min(wait*2, maxHandoverWait)
		default:
			p.release(work, c.DispatchID, c.RunID, ReasonNotHanded, true)
			return provisioner.Handed{}, false
		}
		pause = max(min(pause, c.LeaseUntil.Sub(p.now())), 0)
		p.log.Warn(lineHandoverAgain, append(ids, slog.Duration("wait", pause), errAttr(err))...)
		if pause > 0 && p.sleep(stop, pause) != nil {
			continue
		}
	}
}

// transient is whether a handover's err leaves the dispatch as it was, so the handover
// may be sent again while the lease lasts: a 409 no_gateway, a signed 503 unavailable,
// no answer, an answer not signed under the pin or over its bound, or any other server
// error.
func transient(err error) bool {
	var r *provisioner.Refusal
	switch {
	case errors.Is(err, provisioner.ErrNoGateway), errors.Is(err, provisioner.ErrUnavailable), errors.Is(err, provisioner.ErrUnsigned),
		errors.Is(err, provisioner.ErrUnreachable), errors.Is(err, provisioner.ErrTooLarge):
		return true
	case errors.As(err, &r):
		return r.Status >= 500
	}
	return false
}

// release gives the run of the dispatch id back, for reason, with retry, and reports
// whether the dispatch is over: released, or over already. A signed 429 is waited out up
// to [maxRateLimited] times. A 409 lease_expired or a 410 gone is the dispatch over
// already; a signed 403 or 404 is one Qory Apiary will not release, which is not sent
// again either.
func (p *Pool) release(work context.Context, id, runID, reason string, retry bool) bool {
	ids := []any{slog.String("dispatch_id", id), slog.String("run_id", runID)}
	for limited := 0; ; limited++ {
		err := p.Client.Release(work, id, reason, retry)
		var r *provisioner.Refusal
		switch {
		case err == nil:
			p.log.Info(lineReleased, append(ids, slog.String("reason", reason), slog.Bool("retry", retry))...)
			return true
		case errors.Is(err, provisioner.ErrLeaseExpired), errors.Is(err, provisioner.ErrGone):
			p.log.Info(lineOverAlready, append(ids, errAttr(err))...)
			return true
		case errors.Is(err, provisioner.ErrRateLimited) && errors.As(err, &r) && limited < maxRateLimited:
			if p.sleep(work, r.RetryAfter) != nil {
				return false
			}
			continue
		}
		p.log.Warn(lineNotReleased, append(ids, slog.String("reason", reason), slog.Bool("retry", retry), errAttr(err))...)
		return errors.Is(err, provisioner.ErrForbidden) || errors.Is(err, provisioner.ErrNotFound)
	}
}

// post posts the outcome of c's run, state and reason, to the run endpoint the signed
// discovery names, up to [outcomeTries] times with the same time, so each try sends
// the same bytes.
func (p *Pool) post(work context.Context, c provisioner.Claimed, state, reason string) {
	o := server.Outcome{State: state, Reason: reason, Time: server.RegistrationTime(p.now())}
	ids := []any{slog.String("dispatch_id", c.DispatchID), slog.String("run_id", c.RunID)}
	wait := firstBackoff
	var err error
	for try := 1; ; try++ {
		var conf *server.Configuration
		if conf, err = p.discover(work); err == nil {
			if err = p.Outcomes.PostOutcome(work, conf.Run.URL, c.RunID, o); err == nil {
				p.log.Info(linePosted, append(ids, slog.String("state", state))...)
				return
			}
		}
		if try == outcomeTries || p.sleep(work, wait) != nil {
			break
		}
		wait *= 2
	}
	p.log.Warn(lineNotPosted, append(ids, errAttr(err))...)
}

// discover reads the signed discovery, one at a time and each in a later second of the
// clock than the one before, since the signature of a GET covers its time in whole
// seconds and Qory Apiary refuses one it has seen. The first is in a later second than
// the pool's start, after the discovery qory run pool reads before it starts the loop.
func (p *Pool) discover(ctx context.Context) (*server.Configuration, error) {
	p.discoverMu.Lock()
	defer p.discoverMu.Unlock()
	if wait := p.discovered.Add(time.Second).Sub(time.Now()); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
	p.discovered = time.Now().Truncate(time.Second)
	conf, _, err := p.Outcomes.Discover(ctx)
	return conf, err
}

// forget takes the dispatch id out of the journal, and removes its run's directory.
func (p *Pool) forget(id, runID string) {
	if err := p.journal.remove(id); err != nil {
		p.log.Error(lineNotJournaled, slog.String("dispatch_id", id), slog.String("run_id", runID), errAttr(err))
	}
	if p.Settings.Explore.State != "" {
		_ = explore.Remove(p.Settings.Explore.State, runID)
	}
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
