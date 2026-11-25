package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hookrun"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/stack"
)

// The lines of qory run about the workflow hooks of a session. Hooks run only in a run from a run
// code: each line has the cause alone, with no fix, and at most 100 characters in its first line;
// a hook's name in a line has at most [maxShownHookName] characters.
const (
	textHookStep      = "hook %s at %s"
	textHookPassed    = "the hook %s passed"
	textHookExited    = "the hook %s is red: it exited with status %d"
	textHookTimedOut  = "the hook %s is red: it ran past its limit of %d seconds"
	textHookNoStart   = "the hook %s is red.\n  %s"
	textHookStopped   = "the hook %s is red: qory run was stopped while it ran"
	textHookAgent     = "Agent hook %s did not run: agent hooks are not built yet."
	textHookReview    = "Review hook %s did not run: review hooks are not built yet."
	textHookNoWall    = "Hook %s is red: this stage runs with no wall."
	textHookProgram   = "Hook %s is red: its program is not a file in the commit.\n  %s"
	textHookTooLarge  = "Hook %s is red: its program is over 64 MiB.\n  %s"
	textHookNoRun     = "Hook %s did not run, so it is red.\n  %s"
	textHookOptional  = "the hook %s is red and not required, so the verdict stays"
	textHooksPosted   = "posted the verdict %s for the red hooks"
	textHooksNoRoom   = "qory posted no verdict for the red hooks: with the agent's points, it would pass 20 points"
	textHooksNoPost   = "qory could not post the hooks' verdict.\n  %s"
	textHooksCut      = "qory run was stopped while the hooks ran, so the run closes cancelled"
	textHookUndoFail  = "qory could not undo what hook %s changed; no push follows.\n  %s"
	textHookKept      = "Hook %s's container was not removed, so no push follows.\n  docker rm --force %s failed: %s"
	textHooksUnread   = "qory could not read the run's hooks.\n  %s"
	textHooksNoJoin   = "The session did not start: qory could not undo what a hook at its start changed."
	textHooksNoRecord = "qory could not write the hooks' records.\n  %s"
	textHooksNoFolder = "qory could not make the hooks' folder.\n  %s"
	textHookLater     = "A later session's start hooks are red: its setup container is already running.\n  Forager has no pause for it."
)

// The causes of a red hook that did not run, as its record and its finding have them.
const (
	causeAgent   = "agent hooks are not built yet"
	causeReview  = "review hooks are not built yet"
	causeNoWall  = "a hook runs only inside a wall, and this run has none"
	causeStopped = "qory run was stopped"
	// causeSetupLimit is the cause of a hook at session 1's start that the setup's limit ended,
	// or kept from running.
	causeSetupLimit = "it ran past the setup's limit"
	causeUnrestored = "it did not run: qory could not undo the change of a hook before it"
	// causeNoStart is the cause of a hook whose container ran no program to its end. docker run
	// exits 125 also when the program does, so no text of its output goes into the cause.
	causeNoStart = "its container did not start, or its program exited with status 125"
	// causeNoQory is the cause of the hooks of a checkout whose .qory is no folder: a hook could
	// write to the outbox there.
	causeNoQory = ".qory in the checkout is not a folder"
	// causeNoHead is the cause of the program hooks of a run with no work branch whose checkout's
	// HEAD qory could not read before session 1, see [hookCommits].
	causeNoHead = "qory could not read the checkout's HEAD before session 1"
	// causeLater is the cause of a hook at the start of a later session of a shared run.
	causeLater = "a later session's setup container is already running, and Forager has no pause for it"
)

// The points and the findings of a red hook. A point is one line for the agent of the next
// session; a proof has the form of the review report's.
const (
	textHookPointExited = "The hook %s failed: it exited with status %d."
	textHookPointLimit  = "The hook %s failed: it ran past its limit of %d seconds."
	textHookPointNoRun  = "The hook %s did not run: %s."
	textHookProof       = "failing check: hook %s exited %d"
	textHookProofLimit  = "failing check: hook %s ran past its limit of %d seconds"
	textHookFixRed      = "Change the work so that the hook %s passes."
	textHookFixNoRun    = "Run the hook %s again in a run where it can start."
	textHookFixAgent    = "Remove the agent hook %s from the stack's hooks."
	textHookFixReview   = "Remove the review hook %s from the stack's hooks."
)

// maxShownHookName is the most characters of a hook's name in a line of qory run.
const maxShownHookName = 32

// hookRestoreLimit is the longest the restore after one hook may take, all its rounds: the
// default limit of a hook, [stack.DefaultHookTimeout], reused. A test sets a shorter one. A
// second stop of qory run ends it sooner.
var hookRestoreLimit = stack.DefaultHookTimeout

// The causes of a restore that ended before it was done: no push follows.
const (
	causeRestoreLimit   = "the restore ran past its limit of %s"
	causeRestoreStopped = "a second stop of qory run ended the restore"
)

// hookRemoveWait is how long the removal of the setup container before a session's hooks may
// take.
const hookRemoveWait = removeSetupLimit

// runHookContainer runs the container of a program hook, [dockerGitContainer]; a test replaces it.
var runHookContainer = dockerGitContainer

// readHooks reads the hooks of a session's home, [hookrun.Read]; a test replaces it.
var readHooks = hookrun.Read

// hookSetup is what the workflow hooks of one session need from the run.
type hookSetup struct {
	run    openedRun
	spec   *runSpec
	runID  string
	walled bool
	// home is the session's home, which has hooks.json, and checkout the checkout's root.
	home, checkout string
	// workflow is the session's workflow, and number the session's number.
	workflow string
	number   int
	// commit is the commit that the programs of the hooks at the end of the session are read
	// at, the session's start commit, and start the one for the hooks at session 1's start, the
	// run's start commit.
	commit, start string
	stderr        io.Writer
	said          func(string)
	// again ends at a second stop of qory run, which ends a restore; nil for none.
	again context.Context
	// starting, when not nil, is called before the hooks at session 1's start run, when it has
	// any.
	starting func()
}

// sessionHooks is the workflow hooks of one session: the records of the hooks at its start,
// which ran before its setup commands or are red, and the hooks at its end, with their programs
// read from git before the join. See [openHooks].
type sessionHooks struct {
	s hookSetup
	// folder is the session's folder of the hooks' files in the run directory.
	folder string
	// end is the hooks at the session's end, with their programs once they are read: in session
	// 1 before its setup commands, and in a later session once the setup container is removed,
	// see [sessionHooks.runAll]. unread is whether they are not read yet.
	end     []hookProgram
	unread  bool
	records []hookrun.Record
	// cut is whether a stop of qory run, or the setup's limit, ended a hook; unrestored whether a
	// restore failed, or a hook's container may still run.
	cut, unrestored bool
	// kept is the line of a hook's container whose removal failed, "" for none.
	kept string
	// findings is how many findings the session's records have.
	findings int
}

// hookProgram is one hook with its program: the file qory read from git, or why it has none.
type hookProgram struct {
	hook    compose.Hook
	program string
	// refused is why the program was not read, "" when it was or the hook runs an agent, and
	// line the line of the step, which has the hook's name and refused.
	refused, line string
}

// hookPin is HEAD of the checkout of a run with no work branch, as [hookCommits] read it once.
type hookPin struct {
	read   bool
	commit string
}

// hookCommits is the commit of the programs of a session's hooks at its end and the commit of
// those at its start: the target repository's commit of the work branch, the session's start,
// and the run's start. A run with no work branch takes HEAD of the checkout, read once before
// its first join, before any agent ran, into *pinned. When that read failed, both are "" in
// every session, and the hooks are red: a later read would see what an agent committed.
func hookCommits(branch *preparation.WorkBranch, checkout string, pinned *hookPin) (commit, start string) {
	if branch != nil && len(branch.Repositories) > 0 {
		r := branch.Repositories[0]
		return r.Commit, r.Start
	}
	if !pinned.read {
		pinned.read = true
		pinned.commit, _ = hookrun.Head(checkout) // "" after an error
	}
	return pinned.commit, pinned.commit
}

// openHooks reads the hooks of the session's workflow from hooks.json in its home: it reads the
// program of each program hook from git, at the session's start commit for a hook at the end and
// at the run's start commit for one at the start, into a file in the run directory that no agent
// writes. In session 1 it runs the hooks at the start, before the setup commands start their
// container; in a later session each of them is red. It returns nil for a session with no hook,
// and an error for a hooks.json it cannot read and for a restore after a hook at the start that
// failed: then the session does not join.
func openHooks(ctx context.Context, s hookSetup) (*sessionHooks, error) {
	all, err := readHooks(s.home)
	if err != nil {
		text := fmt.Sprintf(textHooksUnread, shownError(err))
		return nil, &runLineError{&reasonError{code: string(contracts.EndCodeHarnessInvalid), text: text}}
	}
	start := hookrun.At(all, s.workflow, hookrun.PointStart)
	end := hookrun.At(all, s.workflow, hookrun.PointEnd)
	if len(start) == 0 && len(end) == 0 {
		return nil, nil
	}
	h := &sessionHooks{s: s, folder: filepath.Join(s.run.Dir(), hookrun.Folder, strconv.Itoa(s.number))}
	if err := os.MkdirAll(h.folder, 0o700); err != nil {
		return nil, &runLineError{fmt.Errorf(textHooksNoFolder, shownError(err))}
	}
	if s.number > 1 {
		// A later session's setup container may still write .git: the programs of the hooks at
		// its end are read once it is removed.
		for _, hook := range end {
			h.end = append(h.end, hookProgram{hook: hook})
		}
		h.unread = true
		// shortcut: a later session's setup container runs from session 1 on, and a process of
		// it could write the checkout, .git and .qory during a hook's snapshot, mounts and
		// restore, so each hook at a later session's start is red, never skipped, and its program
		// does not run. Replace it when Forager can pause the setup container around the hooks.
		for _, hook := range start {
			h.records = append(h.records, h.red(hook, -1, causeLater, textHookLater))
		}
		return h, nil
	}
	h.end = h.programs(ctx, end, s.commit)
	if len(start) > 0 && s.starting != nil {
		s.starting()
	}
	h.records = h.runAll(ctx, h.programs(ctx, start, s.start), false)
	switch {
	case h.kept != "":
		// The hook's container may still write the checkout, so the session does not join.
		h.write()
		return nil, &runLineError{errors.New(h.kept)}
	case h.unrestored:
		// The agent would start on the hook's change, so the session does not join.
		h.write()
		return nil, &runLineError{errors.New(textHooksNoJoin)}
	case h.cut:
		// A stop ended a hook at the start: the run ends as stopped before the join.
		h.write()
		return nil, ctx.Err()
	}
	return h, nil
}

// write writes the records of the session's hooks into the run directory; a failure is a line.
func (h *sessionHooks) write() {
	path := filepath.Join(h.s.run.Dir(), hookrun.Folder, fmt.Sprintf("session-%d.json", h.s.number))
	if err := hookrun.Write(path, h.s.number, h.records); err != nil {
		h.s.said(fmt.Sprintf(textHooksNoRecord, shownError(err)))
	}
}

// programs reads the program of each program hook of hooks at commit, see [hookrun.Program].
// A run with no wall reads none: no hook runs there.
func (h *sessionHooks) programs(ctx context.Context, hooks []compose.Hook, commit string) []hookProgram {
	var out []hookProgram
	for _, hook := range hooks {
		p := hookProgram{hook: hook}
		if h.s.walled && !notBuilt(hook) {
			p.program = filepath.Join(h.folder, strconv.Itoa(hook.Index))
			var err error
			switch {
			case len(hook.Run) == 0:
				err = errors.New("the hook has no program")
			case commit == "":
				err = errors.New(causeNoHead)
			default:
				err = hookrun.Program(ctx, h.s.checkout, commit, hook.Run[0], p.program)
			}
			// A path that is no file, or a program too large, is the repository's to fix; any
			// other cause is not.
			var refused *hookrun.Refused
			var large *hookrun.TooLarge
			switch {
			case errors.As(err, &refused):
				p.line = textHookProgram
			case errors.As(err, &large):
				p.line = textHookTooLarge
			default:
				p.line = textHookNoRun
			}
			if err != nil {
				p.refused = causeLine(err)
			}
		}
		out = append(out, p)
	}
	return out
}

// after runs the hooks at the end of a session that succeeded, or whose work the push step
// pushes, pushes, once its agent exited and its suite's result is posted, before the push step;
// then it writes the records of the session's hooks, the ones at its start too, and when a
// required hook is red it posts changes-requested with one point per red hook, over the
// agent's verdict as [suiteRun.post] does for a suite that the agent can fix: an escalate
// stays escalate. A restore that failed, or a hook's container that may still run, returns a
// failed outcome and an error, and [sessionHooks.restored] is then false, so no push follows. A
// stop while a hook ran posts nothing, and it closes the run cancelled, as a stop during the
// suite does.
func (h *sessionHooks) after(ctx, again context.Context, limit time.Time, out session.Outcome, pushes bool) (session.Outcome, error) {
	if h == nil {
		return out, nil
	}
	counts := out.State == event.StateSucceeded || pushes
	// After a stop of qory run no hook runs and nothing is posted: each hook is red, stopped.
	stopped := counts && ctx.Err() != nil
	switch {
	case stopped:
		for _, p := range h.end {
			h.records = append(h.records, h.red(p.hook, -1, causeStopped, ""))
		}
	case counts:
		h.records = append(h.records, h.runAll(ctx, h.end, true)...)
	}
	h.write()
	if h.unrestored {
		return session.Outcome{State: event.StateFailed, ExitCode: 1}, &exitError{code: 1}
	}
	if !counts || stopped {
		return out, nil
	}
	if h.cut {
		// A stop while a hook ran posts nothing, as a stop before the hooks does: a hook that the
		// stop ended has no result.
		h.s.said(textHooksCut)
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}, &exitError{code: 1}
	}
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	if err := h.post(postCtx, maxSubmissionTries); err != nil {
		h.s.said(fmt.Sprintf(textHooksNoPost, shownError(err)))
		failed := session.Outcome{State: event.StateFailed, ExitCode: 1}
		// A stop or a limit that ended postCtx cut the post: no call failed, so there is no
		// end code, as for [suiteRun.post].
		if postCtx.Err() == nil {
			failed.Reason = string(endCodeOf(err))
		}
		return failed, &exitError{code: 1}
	}
	return out, nil
}

// restored is whether the checkout is as the snapshot before the session's hooks has it, or no
// hook ran: false after a restore that failed, or a removal of a hook's container that failed.
func (h *sessionHooks) restored() bool { return h == nil || !h.unrestored }

// post posts changes-requested for the red required hooks, with tries requests: one point per
// finding, at most [maxVerdictPoints]. With no red required hook it posts nothing.
func (h *sessionHooks) post(ctx context.Context, tries int) error {
	var points []runapi.Point
	for _, r := range h.records {
		if r.Verdict != hookrun.Red || !r.Required {
			continue
		}
		for _, f := range r.Findings {
			points = append(points, runapi.Point{Text: f.Point, Action: runapi.ActionRunAgain})
		}
	}
	if len(points) == 0 {
		return nil
	}
	// A red hook turns a passed verdict into changes-requested also when it has more findings
	// than a verdict has points: the points past the 20th are in the records.
	points = points[:min(len(points), maxVerdictPoints)]
	suites := &suiteRun{run: h.s.run, runID: h.s.runID, said: h.s.said}
	line, err := suites.post(ctx, points[0], kindFixable, tries, points[1:]...)
	switch {
	case err != nil:
		return err
	case line == textSuiteNoRoom:
		h.s.said(textHooksNoRoom)
	case line != "":
		var verdict string
		_, _ = fmt.Sscanf(line, textSuitePosted, &verdict)
		h.s.said(fmt.Sprintf(textHooksPosted, verdict))
	}
	return nil
}

// stopCause is the cause of a hook that the end of ctx ended or kept from running: at session
// 1's start, ctx ends also at the setup's limit.
func stopCause(ctx context.Context, atEnd bool) string {
	if !atEnd && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return causeSetupLimit
	}
	return causeStopped
}

// runAll runs the program hooks of programs one after another, in their order, each in a build
// step, and returns their records. Before the first, it takes the snapshot of the checkout, and
// after each it restores the checkout from it. Hooks at the end of a session, atEnd, run once
// the setup container is removed, which can write the checkout; hooks at session 1's start run
// before it starts. A run with no wall runs none:
// each hook is red. A restore that failed sets [sessionHooks.unrestored], and no hook runs after
// it.
func (h *sessionHooks) runAll(ctx context.Context, programs []hookProgram, atEnd bool) []hookrun.Record {
	if len(programs) == 0 {
		return nil
	}
	notRun := func(cause, line string) []hookrun.Record {
		var records []hookrun.Record
		for _, p := range programs {
			records = append(records, h.red(p.hook, -1, cause, fmt.Sprintf(line, shownHookName(p.hook.Name), cause)))
		}
		return records
	}
	// A stop, or the setup's limit, during the setup's removal, the read of the programs or the
	// snapshot ends the hooks as a stop while one ran does: each is red with the stop's cause,
	// and cut is set, so no verdict is posted.
	cutAll := func() []hookrun.Record {
		h.cut = true
		var records []hookrun.Record
		for _, p := range programs {
			records = append(records, h.red(p.hook, -1, stopCause(ctx, atEnd), ""))
		}
		return records
	}
	if !h.s.walled {
		var records []hookrun.Record
		for _, p := range programs {
			records = append(records, h.red(p.hook, -1, causeNoWall, fmt.Sprintf(textHookNoWall, shownHookName(p.hook.Name))))
		}
		return records
	}
	if atEnd {
		removeCtx, cancel := context.WithTimeout(ctx, hookRemoveWait)
		err := h.s.run.RemoveSetup(removeCtx)
		cancel()
		if err == nil {
			err = agentStopProof(true, h.s.run.ContainerKept(), "no hook ran")
		}
		switch {
		case ctx.Err() != nil:
			return cutAll()
		case err != nil:
			return notRun(causeLine(err), textHookNoRun)
		}
		if h.unread {
			hooks := make([]compose.Hook, 0, len(programs))
			for _, p := range programs {
				hooks = append(hooks, p.hook)
			}
			programs, h.unread = h.programs(ctx, hooks, h.s.commit), false
		}
	}
	snap, err := hookrun.Take(ctx, h.s.checkout, h.folder)
	switch {
	case ctx.Err() != nil:
		return cutAll()
	case err != nil:
		return notRun(causeLine(err), textHookNoRun)
	}
	var records []hookrun.Record
	for _, p := range programs {
		switch {
		case h.unrestored:
			records = append(records, h.red(p.hook, -1, causeUnrestored, ""))
			continue
		case h.cut || ctx.Err() != nil:
			h.cut = true
			records = append(records, h.red(p.hook, -1, stopCause(ctx, atEnd), ""))
			continue
		}
		if !notBuilt(p.hook) && p.refused == "" {
			// .qory is mounted read-only into the hook's container, so the hook writes nothing to
			// the outbox that the push step reads. A restore removes the folder when git does not
			// ignore it, so it is made before each hook.
			if err := qoryFolder(h.s.checkout); err != nil {
				p.refused, p.line = causeLine(err), textHookNoRun
			}
		}
		records = append(records, h.runOne(ctx, p, atEnd))
		if notBuilt(p.hook) || p.refused != "" {
			continue
		}
		if err := h.restore(ctx, snap); err != nil {
			h.unrestored = true
			h.s.said(fmt.Sprintf(textHookUndoFail, shownHookName(p.hook.Name), err))
		}
	}
	return records
}

// restore restores the checkout from snap after a hook, see [hookrun.Snapshot.Restore], and
// returns the cause of a restore that failed as one line. The restore runs also after a stop,
// so a hook's change never reaches the push, for at most [hookRestoreLimit], and a second stop
// of qory run ends it; a restore that ended before it was done failed.
func (h *sessionHooks) restore(ctx context.Context, snap *hookrun.Snapshot) error {
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hookRestoreLimit)
	defer cancel()
	if h.s.again != nil {
		stop := context.AfterFunc(h.s.again, cancel)
		defer stop()
	}
	err := snap.Restore(restoreCtx)
	switch {
	case err == nil:
		return nil
	case h.s.again != nil && h.s.again.Err() != nil:
		return errors.New(causeRestoreStopped)
	case restoreCtx.Err() != nil:
		return fmt.Errorf(causeRestoreLimit, limitText(hookRestoreLimit))
	}
	return errors.New(causeLine(err))
}

// runOne runs the program hook p in a container of the run's image, in a build step, and
// returns its record. An agent hook and a hook whose program was not read do not run: each is
// red, with its line in the step.
func (h *sessionHooks) runOne(ctx context.Context, p hookProgram, atEnd bool) hookrun.Record {
	name := shownHookName(p.hook.Name)
	step, stepErr := h.s.run.StartStep(event.StepBuild, fmt.Sprintf(textHookStep, name, p.hook.At))
	// The program's output goes to out, which writes no line to the status descriptor, and qory's
	// lines to w.
	var w, out io.Writer = h.s.stderr, h.s.stderr
	var lines, output *stepLines
	if stepErr == nil {
		lines = &stepLines{stderr: h.s.stderr, step: step}
		output = &stepLines{stderr: h.s.stderr, step: step, program: true}
		w, out = lines, output
	}
	exit := func(state string, code int) {
		if lines != nil {
			output.flush()
			lines.flush()
			_ = step.Exit(state, code)
		}
	}
	say := func(line string) { fmt.Fprintln(w, "qory run: "+line) }
	switch {
	case notBuilt(p.hook):
		// shortcut: an agent hook is red until the agent-hook design sets how it runs; replace then.
		// A review hook is red until the same design sets how qory reads its report.
		cause, line := causeAgent, textHookAgent
		if p.hook.Kind == stack.HookReview {
			cause, line = causeReview, textHookReview
		}
		r := h.red(p.hook, -1, cause, "")
		say(fmt.Sprintf(line, name))
		exit(event.StateFailed, -1)
		return r
	case p.refused != "":
		r := h.red(p.hook, -1, p.refused, "")
		say(fmt.Sprintf(p.line, name, p.refused))
		exit(event.StateFailed, -1)
		return r
	}
	limit := time.Duration(p.hook.TimeoutSeconds) * time.Second
	if limit <= 0 {
		limit = stack.DefaultHookTimeout
	}
	limited, cancel := context.WithTimeout(ctx, limit)
	code, err := runHookContainer(limited, h.container(p, out))
	timedOut := limited.Err() != nil || errors.Is(err, context.DeadlineExceeded)
	cancel()
	if output != nil {
		output.flush()
	}
	if removeErr := removalError(err); removeErr != nil && (timedOut || ctx.Err() != nil) {
		// The container may still write the checkout, so no restore holds and no push follows.
		h.unrestored = true
		h.kept = h.keptLine(name, removeErr)
		say(h.kept)
	}
	switch {
	case ctx.Err() != nil:
		h.cut = true
		cause := stopCause(ctx, atEnd)
		if cause == causeStopped {
			say(fmt.Sprintf(textHookStopped, name))
		} else {
			say(fmt.Sprintf(textHookNoStart, name, cause))
		}
		exit(event.StateCancelled, -1)
		return h.red(p.hook, -1, cause, "")
	case timedOut:
		cause := fmt.Sprintf("it ran past its limit of %d seconds", int(limit.Seconds()))
		say(fmt.Sprintf(textHookTimedOut, name, int(limit.Seconds())))
		exit(event.StateFailed, -1)
		r := h.red(p.hook, -1, cause, "")
		r.Findings[0].Point = hookPoint(p.hook, fmt.Sprintf(textHookPointLimit, name, int(limit.Seconds())))
		r.Findings[0].Fix = fmt.Sprintf(textHookFixRed, p.hook.Name)
		r.Findings[0].Proof = cutRunes(fmt.Sprintf(textHookProofLimit, p.hook.Name, int(limit.Seconds())), maxProofLength)
		return r
	case err != nil:
		// The error can be the last line of the program's output: it goes into the step's log
		// alone, and the record has a fixed cause.
		fmt.Fprintln(out, causeLine(err))
		say(fmt.Sprintf(textHookNoStart, name, causeNoStart))
		exit(event.StateFailed, -1)
		return h.red(p.hook, -1, causeNoStart, "")
	case code != 0:
		say(fmt.Sprintf(textHookExited, name, code))
		exit(event.StateFailed, code)
		r := h.red(p.hook, code, fmt.Sprintf("it exited with status %d", code), "")
		r.Findings[0].Point = hookPoint(p.hook, fmt.Sprintf(textHookPointExited, name, code))
		return r
	}
	say(fmt.Sprintf(textHookPassed, name))
	exit(event.StateSucceeded, 0)
	return hookrun.Record{
		Name: p.hook.Name, Index: p.hook.Index, At: p.hook.At, Kind: p.hook.Kind, Required: p.hook.Required,
		Verdict: hookrun.Green, Reviewer: hookrun.Reviewer(p.hook.Index), Findings: []reviewround.Finding{},
	}
}

// notBuilt is whether hook is one that qory does not run yet, an agent hook or a review hook,
// which is red with the line [textHookAgent] or [textHookReview] and never skipped. A review hook reads a review
// report that qory does not parse yet, also when it runs a program.
func notBuilt(hook compose.Hook) bool {
	return hook.Agent != "" || hook.Kind == stack.HookReview
}

// container is the container of the program hook p: the run's image, the wall's user and
// limits, no network, no capability and no join of the run's gateway, so the hook reaches no run
// variable, no run API and no forge. The checkout is bound at its path for writing, with its
// .git and its .qory bound read-only over it, and the program read-only at its path. The run's
// read targets are read-only repositories below .qory, in [preparation.Repositories], so the hook
// writes none, and the snapshot and the restore cover the target alone. Nothing below .qory is
// bound by its path. The program runs in the checkout's root with the hook's arguments, and HOME
// at /tmp.
//
// shortcut: the hook runs as the wall's user, and qory restores the checkout as this process's
// user; with another wall user, git clean on this machine may not empty a folder that the hook
// made, the restore fails and the run ends with no push. Replace it when the restore runs in a
// container as the wall's user.
func (h *sessionHooks) container(p hookProgram, out io.Writer) gitContainer {
	mounts := []wall.Mount{
		{Path: h.s.checkout}, {Path: filepath.Join(h.s.checkout, ".git"), ReadOnly: true},
		{Path: filepath.Join(h.s.checkout, ".qory"), ReadOnly: true}, {Path: p.program, ReadOnly: true},
	}
	gs := runcopy.GitSession{Command: p.program, Args: p.hook.Run[1:], Env: []string{"HOME=/tmp"}, Dir: h.s.checkout}
	c := gitContainerFor(h.s.run, h.s.spec, h.s.runID, gs, mounts, out)
	c.Name = "qory-hook-" + h.s.runID
	return c
}

// qoryFolder makes the folder .qory in the checkout at dir when it has none, mode 0755, and
// returns an error when .qory is not a folder, such as a link.
func qoryFolder(dir string) error {
	path := filepath.Join(dir, ".qory")
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		return errors.New(causeNoQory)
	}
	return nil
}

// keptLine is the line of the hook name whose container's removal failed with removeErr, with
// docker rm's output when there is one.
func (h *sessionHooks) keptLine(name string, removeErr error) string {
	container := "qory-hook-" + h.s.runID
	cause := causeLine(removeErr)
	var rm *removeError
	switch {
	case errors.As(removeErr, &rm) && rm.out != "":
		cause = causeLine(errors.New(rm.out))
	case rm != nil:
		cause = causeLine(rm.err)
	}
	return fmt.Sprintf(textHookKept, name, container, cause)
}

// removalError is the error of the removal of a hook's container in err, the error of
// [runHookContainer] after a stop or a time-out: nil when the removal passed.
func removalError(err error) error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return nil
	}
	for _, e := range joined.Unwrap() {
		if !errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded) {
			return e
		}
	}
	return nil
}

// red is the record of the hook red for cause, with exit the program's exit status, -1 for
// none, and its one finding, which qory writes: blocking for a required hook. A line that is
// not "" goes into a build step of the hook, for a hook that did not reach [sessionHooks.runOne].
func (h *sessionHooks) red(hook compose.Hook, exit int, cause, line string) hookrun.Record {
	name := shownHookName(hook.Name)
	if line != "" {
		if step, err := h.s.run.StartStep(event.StepBuild, fmt.Sprintf(textHookStep, name, hook.At)); err == nil {
			lines := &stepLines{stderr: h.s.stderr, step: step}
			fmt.Fprintln(lines, "qory run: "+line)
			lines.flush()
			_ = step.Exit(event.StateFailed, -1)
		} else {
			fmt.Fprintln(h.s.stderr, "qory run: "+line)
		}
	}
	if !hook.Required {
		h.s.said(fmt.Sprintf(textHookOptional, name))
	}
	severity, fix := reviewround.Blocking, fmt.Sprintf(textHookFixRed, hook.Name)
	if !hook.Required {
		severity = reviewround.NonBlocking
	}
	switch {
	case hook.Kind == stack.HookReview:
		fix = fmt.Sprintf(textHookFixReview, hook.Name)
	case hook.Agent != "":
		fix = fmt.Sprintf(textHookFixAgent, hook.Name)
	case exit < 0:
		fix = fmt.Sprintf(textHookFixNoRun, hook.Name)
	}
	proof := fmt.Sprintf(textHookProof, hook.Name, exit)
	if exit < 0 {
		proof += ": " + cause
	}
	quote := strings.Join(hook.Run, " ")
	if hook.Agent != "" {
		quote = "agent: " + hook.Agent
	}
	// shortcut: the finding's id is the hook's place among the session's records, f<n>; the
	// round's open findings number every finding across the run. Replace it when one writer joins
	// the hooks' entries to the round's, with an id for each that the run has not used.
	h.findings++
	id := "f" + strconv.Itoa(h.findings)
	return hookrun.Record{
		Name: hook.Name, Index: hook.Index, At: hook.At, Kind: hook.Kind, Required: hook.Required,
		Verdict: hookrun.Red, Cause: cause, ExitCode: exit, Reviewer: hookrun.Reviewer(hook.Index),
		Findings: []reviewround.Finding{{
			ID: id, Severity: severity, Quote: quote, Problem: cause, Fix: fix,
			Proof: cutRunes(proof, maxProofLength), Point: hookPoint(hook, fmt.Sprintf(textHookPointNoRun, name, cause)),
		}},
	}
}

// maxProofLength is the most characters of a finding's proof, as the review report allows.
const maxProofLength = 1000

// hookPoint is text as the point of the hook, when a verdict with it as its one point passes
// the verdict format; else the point with the hook's reviewer name in place of text.
func hookPoint(hook compose.Hook, text string) string {
	if validPoint(runapi.Point{Text: text, Action: runapi.ActionRunAgain}) {
		return text
	}
	return fmt.Sprintf("The hook %s is red.", hookrun.Reviewer(hook.Index))
}

// shownHookName is a hook's name as a line shows it: at most [maxShownHookName] characters, the
// first 15 and the last 16 with … between them for a longer name.
func shownHookName(name string) string {
	r := []rune(name)
	if len(r) <= maxShownHookName {
		return name
	}
	return string(r[:15]) + "…" + string(r[len(r)-16:])
}

// maxCauseLength is the most characters of a red hook's cause.
const maxCauseLength = 300

// causeLine is err as one printable line: each of its lines, trimmed, joined with spaces, with a
// URL's password hidden, and at most [maxCauseLength] characters before the ... of a cut.
func causeLine(err error) string {
	lines := strings.Split(exports.HideURLError(err).Error(), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return integration.PrintableWithin(strings.Join(slices.DeleteFunc(lines, func(l string) bool { return l == "" }), " "), maxCauseLength)
}

// cutRunes is text in at most n characters.
func cutRunes(text string, n int) string {
	if r := []rune(text); len(r) > n {
		return string(r[:n])
	}
	return text
}
