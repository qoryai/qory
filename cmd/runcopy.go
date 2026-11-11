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
	"strings"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runcopy"
)

// The lines of a walled write run, which works in a copy of the person's checkout. A line
// with a path of the copy shows the person where the run's work stays. The texts of a cause go
// after [textCopyRead], which has the fix. [textCopyBehind], [textCopyNotAdded] and [textCopyLarge]
// stand alone beside the line of [runCopy.kept], which has the fix.
const (
	textCopyMount      = "make the mount %s read-only, with :ro on --mount or in wall.mounts: a walled run reads the checkout or works in a copy of it, and a read-write mount lets it write your files directly"
	textCopyOutside    = "run qory inside the checkout %s: a run that may write works in a copy of the checkout, and the working folder %s is outside it"
	textCopyNoFolder   = "commit %[1]s, or run qory in a folder that the commit at HEAD has: a run that may write works in a copy of that commit, which has no folder %[1]s"
	textCopyReserved   = "start the run from a folder that does not contain your home folder or qory's state directory: the folder %s %s, and the run's copy of it would have every file there"
	textCopyBehind     = "part of the run's work did not come back: the copy has a nested repository the run made."
	textCopyNotAdded   = "part of the run's work did not come back: the copy has changes that git could not add to the run's commit, such as a file another user owns or a change in a submodule."
	textCopyLarge      = "left out of the run's commit, as each has more than %s: %s. They stay in the run's copy."
	textCopyStepLimit  = "the check of the run's commits ran past its limit of %s, so those commits did not come back"
	textCopyFetchLimit = "the fetch of the run's commits ran past its limit of %s, so those commits did not come back"
	textCopyBackLimit  = "the copy back of the run's files ran past its limit of %s, so part of them did not come back"
	textCopyWaiting    = "got %s: bringing the run's work back, for at most %s. Send a second signal to stop that."
	textCopyOnBranch   = "the run's commits are on %s in %s."
	textCopyOtherRefs  = " The run's other refs are under %s: list them with git for-each-ref %s."
	textCopyRefs       = "the run's refs are under %s in %s: list them with git for-each-ref %s."
	textCopyActing     = " They change these qory settings, links or repository folders: %s. Check them before you check out the branch."
	textCopyRead       = "read the run's files in %s, and run no git there: %s"
	textCopyLimit      = "the git session ran past its limit of %s, so the run's commits did not come back"
	textCopyNoSession  = "the git session did not start, so the run's commits did not come back: %s"
	textCopyExited     = "the git session exited with %d, so the run's commits did not come back"
	textCopyCut        = "%s during the git session, so the run's commits did not come back"
	textCopyCheckCut   = "%s during the check or the fetch of the run's commits, so those commits did not come back"
	textCopyWallKept   = "a container of the run could not be removed and may still run and write the copy, so nothing came back from the run"
	textCopyStays      = "read the run's files in %s, run no git there, then remove the folder: it has the work that did not come back"
	textCopyBack       = "copied %s back to %s from the run, and deleted %s there."
	textCopyLeftOut    = "left out of the copy back to your folder: %s."
	textCopyFailed     = "left out of the copy back, as qory could not read or write them: %s."
	textCopyGenerated  = "left out of the copy back, as the folders that builds and package managers write have more than %d files or %s in all: %s. They stay in the run's copy."
	textCopyConflicts  = "you and the run both changed these files: yours stay, and the run's version of each is beside it with -from-run: %s."
	textCopyNotDeleted = "you and the run both changed these files, so yours stay: %s."
	textCopyActingKept = "the run changed these qory settings, so those files stay as you have them: %s."
	textCopyNotIn      = "left out of the run's copy: %s."
	textCopyNoRemove   = "remove %s by hand: %s"
	textCopyNoDisarm   = "remove %s, and run no git there: qory renames each .git in a copy it keeps, and that failed: %s"
	textCopyFolder     = "the run's copy"
)

// copyStepLimit is how long a copy of a run's files back may take, and the base of how long the
// check of a run's bundles or a fetch from them may take, see [stepLimitFor]. A test sets a
// shorter one.
var copyStepLimit = 10 * time.Minute

// maxStepLimit is the longest the check of a run's bundles or a fetch from them may take.
const maxStepLimit = 60 * time.Minute

// stepLimitFor is how long the check of bundles of size bytes, or a fetch from them, may take:
// [copyStepLimit] and a minute for each full GiB, as git checks every object, at most
// [maxStepLimit]. A bundle has at most [runcopy.MaxBundle].
func stepLimitFor(size int64) time.Duration {
	limit := copyStepLimit + time.Duration(size>>30)*time.Minute
	return min(limit, max(maxStepLimit, copyStepLimit))
}

// removeSetupLimit is how long the removal of a run's setup container may take before its work
// comes back.
const removeSetupLimit = 2 * time.Minute

// stepErr is err of a step under limited, the context of ctx with limit: the line of that limit
// when the limit ended the step. A stop that ended ctx stays err, for [runCopy.checkFailed].
func stepErr(ctx, limited context.Context, limit time.Duration, err error) error {
	return limitErr(ctx, limited, textCopyStepLimit, limit, err)
}

// limitErr is [stepErr] with text, the line of the limit.
func limitErr(ctx, limited context.Context, text string, limit time.Duration, err error) error {
	if err != nil && ctx.Err() == nil && limited.Err() != nil {
		return fmt.Errorf(text, limit)
	}
	return err
}

// disarmLimit is how long the renames in a copy that stays may take.
const disarmLimit = time.Minute

// runCopiesDir is the folder of the copies of walled runs, <user cache>/qory/runs, "" when
// the lookup of the cache folder fails. No program qory runs, and no file it reads for a
// run, may lie there, since a run could have written it.
func runCopiesDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "qory", "runs")
}

// writableMount stops a run that may write when mounts has a read-write mount other than
// the checkout at root: it works in a copy, which the mount would go around.
func writableMount(root string, mounts []wall.Mount, suffix string) error {
	for _, m := range mounts {
		if !m.ReadOnly && m.Path != root {
			return input(fmt.Errorf(textCopyMount+"%s", exports.Value(m.Path, false), suffix))
		}
	}
	return nil
}

// runCopy is the copy a walled write run works in, and what brings its work back.
type runCopy struct {
	dir runcopy.Dir
	// root is the person's checkout, or the person's folder for a plain one.
	root string
	// branch is the branch of the copy that the run works on.
	branch string
	// back is the branch the run's commits land on in the person's repository, from
	// [runcopy.FreeBranch]: the same as branch for a run with no branch of a work item. qory
	// never checks it out. It is "" for a plain folder.
	back string
	// plain is whether the checkout is not a git working tree: [runcopy.Dir.CopyBack] brings the
	// files back.
	plain bool
	// qoryLeftOut is whether [runCopy.leftOut] printed .qory.
	qoryLeftOut bool
	// repos are the repositories of the copy, the person's first, nil for a plain folder.
	repos []runcopy.Repository
	// out is what the git session leaves out of its commit, by [runcopy.Repository.Path].
	out map[string]runcopy.LeftOut
	// keep is whether the copy stays when the run ends: nothing, or not everything, came back.
	keep bool
	// limit is how long the check of the run's bundles and each fetch from them may take, from
	// [stepLimitFor].
	limit time.Duration
	// readOnly is whether the run only reads: its setup works in the copy, the agent has the copy
	// read-only, and nothing comes back.
	readOnly bool
	// cutBy is what ended a step whose context ended, as a line has it.
	cutBy func() string
	// stepCode is the exit code of a copy step that ended failed, -1 when it did not end or
	// has none. cut is whether a stop or the run's time limit ended the git session. The run's
	// close reads both, see [pushEnding.copyOutcome].
	stepCode int
	cut      bool
}

// copyStop is the stop of a return of the run's work. Its error is the line on the terminal, and
// log is the line in the step's log, which has no path of the copy: the record reaches Qory
// Apiary, and its reader may have no access to this machine.
type copyStop struct{ line, log string }

// Error is the line on the terminal.
func (s *copyStop) Error() string { return s.line }

// stopped is the stop of a return with cause, a text of qory that has no path of the copy.
func (c *runCopy) stopped(cause string) *copyStop {
	return c.stop(cause, cause)
}

// stoppedBy is the stop of a return that err ended, with err's text in place of the %s of text.
// [integration.Printable] cuts that text, and a part of a path that the cut leaves no longer
// matches, so [runCopy.withoutPaths] runs before the cut.
func (c *runCopy) stoppedBy(text string, err error) *copyStop {
	cause := exports.HideURLError(err).Error()
	return c.stop(fmt.Sprintf(text, integration.Printable(cause)), fmt.Sprintf(text, integration.Printable(c.withoutPaths(cause))))
}

// stop is the stop with cause on the terminal and logged in the step's log. Past it, the copy
// stays.
func (c *runCopy) stop(cause, logged string) *copyStop {
	c.keep = true
	return &copyStop{
		line: fmt.Sprintf(textCopyRead, exports.Value(c.dir.Work(), false), cause),
		log:  fmt.Sprintf(textCopyRead, textCopyFolder, logged),
	}
}

// withoutPaths is text with [textCopyFolder] in place of each path of the copy, as it is and as
// [preparation.ShownPath] shows it: an error of [runcopy] has the second. The folder inside the
// copy goes first, so a path under it does not keep the folder's name.
func (c *runCopy) withoutPaths(text string) string {
	work, root := c.dir.Work(), c.dir.Root()
	for _, path := range []string{work, preparation.ShownPath(work), root, preparation.ShownPath(root)} {
		text = strings.ReplaceAll(text, path, textCopyFolder)
	}
	return text
}

// checkFailed is the line of a check of the run's commits that failed with err, under ctx: the
// cut, when ctx ended, and never a failed check for it.
func (c *runCopy) checkFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil && c.cutBy != nil {
		return c.stopped(fmt.Sprintf(textCopyCheckCut, c.cutBy()))
	}
	return c.stoppedBy("%s", err)
}

// startCopy makes the copy of the checkout at root for the run runID. branch is the branch
// the run works on, "" for the free name of [runcopy.FreeBranch]. keepOut are the folders the
// copy must not overlap.
// noRepository is whether the run comes from a run code or a run key and lists no repositories: in
// a folder where git init ran and that has no commit, it gets the copy of a plain folder with
// .git left out. A local run in such a folder stops, since a repository needs a commit.
func startCopy(ctx context.Context, root, runID, branch string, noRepository bool, keepOut ...string) (*runCopy, error) {
	git, err := workingTree(root)
	if err != nil {
		return nil, err
	}
	empty := git && noRepository && runcopy.NoCommits(ctx, root)
	if relation := reserved(root); relation != "" && (!git || empty) {
		return nil, input(fmt.Errorf(textCopyReserved, exports.Value(root, false), relation))
	}
	dir, err := runcopy.New(runID, keepOut...)
	if err != nil {
		return nil, err
	}
	c := &runCopy{dir: dir, root: root, branch: branch, plain: !git || empty, out: map[string]runcopy.LeftOut{}, stepCode: -1}
	switch {
	case empty:
		err = dir.CopyFolderWithoutGit(ctx, root)
	case c.plain:
		err = dir.CopyFolder(ctx, root)
	default:
		if c.back, err = runcopy.FreeBranch(ctx, root, runID); err != nil {
			break
		}
		if c.branch == "" {
			c.branch = c.back
		}
		c.repos, err = dir.Clone(ctx, root, c.branch)
	}
	if err != nil {
		// No [integration.Printable] runs on the whole line, so a line of git in err is cut here.
		err = runcopy.ShownError(err)
		if rerr := dir.Remove(); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, err
	}
	return c, nil
}

// artifactsIn copies the checkout's artifacts and work item into the copy of a repository, and
// keeps .qory out of an agent's git add -A there. A plain folder's copy has them already. report
// prints a line of qory run.
func (c *runCopy) artifactsIn(ctx context.Context, report func(string)) error {
	if c.plain {
		return nil
	}
	if err := c.dir.Exclude("/" + checkout.Dir); err != nil {
		return err
	}
	res, err := c.dir.CopyArtifactsIn(ctx, c.root)
	if out := slices.Concat(reportedSkips(res.Skipped, c.root), res.Conflicts); len(out) > 0 {
		report(fmt.Sprintf(textCopyNotIn, shownPaths(out)))
	}
	return err
}

// done removes the copy, unless a line of the run told the person that it stays. A copy that
// stays gets no .git, so a git there runs nothing the run planted. After a failed close a
// container of the run may be left, and it could write the copy again. report prints a line of
// qory run.
func (c *runCopy) done(report func(string)) {
	if c == nil {
		return
	}
	if c.keep {
		ctx, cancel := context.WithTimeout(context.Background(), disarmLimit)
		defer cancel()
		if err := c.dir.Disarm(ctx); err != nil {
			report(fmt.Sprintf(textCopyNoDisarm, exports.Value(c.dir.Root(), false), strings.ReplaceAll(exports.HideError(err).Error(), "\n", "; ")))
		}
		return
	}
	if err := c.dir.Remove(); err != nil {
		report(fmt.Sprintf(textCopyNoRemove, exports.Value(c.dir.Root(), false), exports.HideError(err)))
	}
}

// reportedSkips is what a copy of artifacts from the folder from left out, as a person reads it:
// .qory at the top is not listed when from has a regular file there. A commit with a file named
// .qory has no artifacts to leave out, on any run.
func reportedSkips(skipped []string, from string) []string {
	if info, err := os.Lstat(filepath.Join(from, checkout.Dir)); err == nil && info.Mode().IsRegular() {
		return withoutQoryDir(skipped)
	}
	return skipped
}

// copyBackSkips is what the copy back from the copy at work left out, as a person reads it: .qory
// at the top is not listed when it is a folder with nothing but the artifacts, which
// [runCopy.artifactsBack] copies back and reports, and the work item, which qory wrote. Anything
// else there stays in the copy, which qory removes, so the line is the last trace of it.
func copyBackSkips(skipped []string, work string) []string {
	at := filepath.Join(work, checkout.Dir)
	if info, err := os.Lstat(at); err != nil || !info.IsDir() {
		return skipped
	}
	entries, err := os.ReadDir(at)
	if err != nil {
		return skipped
	}
	for _, e := range entries {
		if e.Name() != artifactsDir && e.Name() != workItemFile {
			return skipped
		}
	}
	return withoutQoryDir(skipped)
}

// withoutQoryDir is skipped without .qory at the top.
func withoutQoryDir(skipped []string) []string {
	return slices.DeleteFunc(slices.Clone(skipped), func(p string) bool { return p == checkout.Dir })
}

// artifactsBack copies .qory/artifacts of the copy back to the person's, once the run's
// containers are gone, with or without a commit: the git session leaves .qory out. It prints
// what it left out. A file you both have keeps your version, with the run's beside it. The
// copy stays with a file that qory could not read or write. It returns false when the copy
// back stopped.
func (c *runCopy) artifactsBack(ctx context.Context, report func(string)) bool {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	res, err := c.dir.CopyArtifactsBack(limited, c.root)
	c.leftOut(reportedSkips(res.Skipped, c.dir.Work()), report)
	c.failed(res.Failed, report)
	if len(res.Conflicts) > 0 {
		report(fmt.Sprintf(textCopyConflicts, shownPaths(res.Conflicts)))
	}
	if err != nil {
		report(shownError(err))
		c.kept(report)
		return false
	}
	return true
}

// leftOut prints what a copy back left out, with .qory at the top once: both copies back of a plain
// folder can leave it out.
func (c *runCopy) leftOut(skipped []string, report func(string)) {
	if c.qoryLeftOut {
		skipped = withoutQoryDir(skipped)
	}
	if len(skipped) > 0 {
		report(fmt.Sprintf(textCopyLeftOut, shownPaths(skipped)))
		c.qoryLeftOut = slices.Contains(skipped, checkout.Dir)
	}
}

// failed prints the entries that a copy back left out because a read or a write of them failed,
// and keeps the copy, which still has them.
func (c *runCopy) failed(paths []string, report func(string)) {
	if len(paths) > 0 {
		report(fmt.Sprintf(textCopyFailed, shownPaths(paths)))
		c.kept(report)
	}
}

// kept marks the copy to stay and prints where it is, once.
func (c *runCopy) kept(report func(string)) {
	if c.keep {
		return
	}
	c.keep = true
	report(fmt.Sprintf(textCopyStays, exports.Value(c.dir.Work(), false)))
}

// bind makes spec work in the copy: the workspace is the copy with the working folder's
// path in the checkout at at.root, and the checkout's read-write mount is the copy's.
func (c *runCopy) bind(spec *runSpec, at places, cwd string) error {
	work := c.dir.Work()
	rel, err := filepath.Rel(realPath(at.root), realPath(cwd))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return input(fmt.Errorf(textCopyOutside, exports.Value(at.root, false), exports.Value(cwd, false)))
	}
	dir := work
	if rel != "." {
		for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
			dir = filepath.Join(dir, part)
			if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
				return input(fmt.Errorf(textCopyNoFolder, exports.Value(rel, false)))
			}
		}
	}
	spec.Dir = dir
	for i, m := range spec.Mounts {
		if m.Path == at.root && (!m.ReadOnly || c.readOnly) {
			spec.Mounts[i].Path = work
		}
	}
	spec.Written = writtenPlaces(work, spec.Mounts)
	return nil
}

// recordLinks lists the links into the copy's .qory folder that its commit has, for the git
// session to leave out. Call it before any run writes the copy.
func (c *runCopy) recordLinks() error {
	if c.plain {
		return nil
	}
	work := c.dir.Work()
	var links []string
	err := filepath.WalkDir(work, func(p string, e fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case e.IsDir() && (e.Name() == ".git" || e.Name() == checkout.Dir):
			return fs.SkipDir
		case e.Type()&fs.ModeSymlink == 0:
			return nil
		}
		own, err := render.OwnLink(work, p)
		if err != nil || !own {
			return err
		}
		rel, err := filepath.Rel(work, p)
		if err != nil {
			return err
		}
		links = append(links, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	if len(links) > 0 {
		o := c.out[""]
		o.Links = links
		c.out[""] = o
	}
	return nil
}

// recordSetup records what the setup left changed in each repository of the copy, so the
// git session leaves it out while it stays as the setup left it. No [integration.Printable]
// runs on the whole line of its error, so a line of git in it is cut, see [runcopy.ShownError].
func (c *runCopy) recordSetup(ctx context.Context) error {
	for _, r := range c.repos {
		paths, err := c.dir.SetupChanges(ctx, r)
		if err != nil {
			return runcopy.ShownError(err)
		}
		states, err := c.dir.SetupStates(ctx, r, paths)
		if err != nil {
			return runcopy.ShownError(err)
		}
		submodules, err := c.dir.SubmoduleSetup(ctx, r)
		if err != nil {
			return runcopy.ShownError(err)
		}
		o := c.out[r.Path]
		o.Setup, o.Submodules = states, submodules
		c.out[r.Path] = o
	}
	return nil
}

// gitSession is the git session after the agent, in the run's step of kind copy, on a run that
// is still open: it commits what the agent left in the copy and writes the run's bundles, in the
// container of [runCopy.inContainer]. The caller has removed the setup container and found no
// container of the run kept, so no process of the run writes the copy during the session.
//
// The step ends before this returns, so the run's close finds it ended, with the state of
// [runCopy.stepEnd]. The container's lines go into the step's log. Unless the session succeeds,
// the step's last line is [copyStop.log]. stderr is qory run's stderr, masked with the run's
// mask list, which the container's lines go to as well.
//
// The first error is that of a copy step that did not start, for example when the link to the
// gateway dropped after the join: no session ran, and the caller runs it after the close, see
// [runCopy.gitSessionAfterClose]. The second is the stop of the return, which goes through
// [runCopy.stop] and keeps the copy: a session that failed, or a copy step that did not end.
func (c *runCopy) gitSession(ctx context.Context, run openedRun, spec *runSpec, stderr io.Writer) (error, error) {
	step, err := run.StartStep(event.StepCopy, textCopyStep)
	if err != nil {
		return err, nil
	}
	lines := &stepLines{stderr: stderr, step: step}
	code, stop := c.inContainer(ctx, run, spec, lines)
	lines.flush()
	if stop == nil {
		if err := step.Exit(event.StateSucceeded, 0); err != nil {
			return nil, c.stoppedBy("%s", err)
		}
		return nil, nil
	}
	state, code := c.stepEnd(code)
	c.stepCode = code
	lines.log(stepFailure(errors.New(stop.log)))
	// The line of the stop stays when the exit fails too.
	_ = step.Exit(state, code)
	return nil, stop
}

// gitSessionAfterClose is the git session of [runCopy.gitSession] after the run's close, with no
// step: the gateway closed the run, the agent's join returned an error, or the copy step did not
// start. The caller runs it only once the close showed that no container of the run is left.
// Its lines go to stderr alone, as for [runCopy.gitSession].
func (c *runCopy) gitSessionAfterClose(ctx context.Context, run openedRun, spec *runSpec, stderr io.Writer) error {
	if _, stop := c.inContainer(ctx, run, spec, stderr); stop != nil {
		return stop
	}
	return nil
}

// inContainer runs the git session in a container of the run's image that qory starts, with no
// network, no gateway and no bind but the copy and the bundle folder, see [runGitContainer]. It
// ends at the time limit of [runcopy.SessionLimitFor] at the latest, from a count of the copy
// with lstat, [runcopy.Dir.WorkSize], which follows no link. It returns the session's exit code, -1 for a
// session with no exit code to show, and the stop of the return of the run's work, nil for a
// session that succeeded. A cut, which is a stop or the run's time limit that ended ctx, sets
// [runCopy.cut].
func (c *runCopy) inContainer(ctx context.Context, run openedRun, spec *runSpec, out io.Writer) (int, *copyStop) {
	gs, err := c.dir.GitSession(ctx, c.branch, "", c.repos, c.out)
	if err != nil {
		return -1, c.noSession(ctx, err)
	}
	// A count that a cut ended gives a limit too: the cut ends the container at once.
	entries, size, _ := c.dir.WorkSize(ctx)
	limit := runcopy.SessionLimitFor(entries, size)
	limited, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	code, err := runGitContainer(limited, gitContainerFor(run, spec, spec.RunID, gs, c.dir.SessionMounts(), out))
	timedOut := limited.Err() != nil || errors.Is(err, context.DeadlineExceeded)
	switch {
	case ctx.Err() != nil && (err != nil || timedOut) && c.cutBy != nil:
		return -1, c.cutStop()
	case timedOut:
		return -1, c.stopped(fmt.Sprintf(textCopyLimit, limit))
	case err != nil:
		return -1, c.noSession(ctx, err)
	case code != 0:
		// A session that failed can leave a bundle of part of the work: no bundle is used.
		return code, c.stopped(fmt.Sprintf(textCopyExited, code))
	}
	return 0, nil
}

// stepEnd is the state and the exit code of a copy step whose session stopped with code: cancelled
// with -1 after a cut, failed with code otherwise.
func (c *runCopy) stepEnd(code int) (string, int) {
	if c.cut {
		return event.StateCancelled, -1
	}
	return event.StateFailed, code
}

// cutStop is the stop of a git session that a stop or the run's time limit cut.
func (c *runCopy) cutStop() *copyStop {
	c.cut = true
	return c.stopped(fmt.Sprintf(textCopyCut, c.cutBy()))
}

// noSession is the stop of a git session that did not start with err. When ctx ended, what ended
// it is the cause, and err only follows from that.
func (c *runCopy) noSession(ctx context.Context, err error) *copyStop {
	if ctx.Err() != nil && c.cutBy != nil {
		return c.cutStop()
	}
	return c.stoppedBy(textCopyNoSession, err)
}

// quarantine checks the bundles of the git session, with [runCopy.limit] from their size, once the
// session's container is gone. It returns the quarantine per repository, nil for one with no commit
// past its base, and nil with no commit in any: [runcopy.ErrNoBundle] goes to report, when left
// has nothing.
func (c *runCopy) quarantine(ctx context.Context, left runcopy.Left, report func(string)) ([]*runcopy.Quarantine, error) {
	c.limit = stepLimitFor(c.dir.BundleBytes(c.repos))
	limited, cancel := context.WithTimeout(ctx, c.limit)
	defer cancel()
	qs, err := c.dir.Quarantine(limited, c.branch, c.repos)
	if errors.Is(err, runcopy.ErrNoBundle) {
		if report != nil && !left.Any() {
			report(err.Error())
		}
		return nil, nil
	}
	return qs, stepErr(ctx, limited, c.limit, err)
}

// comeBack brings the run's work to the person: for a plain folder the files, and for a
// repository the commits, which land on [runCopy.back] in each repository, with one line per
// repository. Every git here runs after the run closed, so no container of the run is left.
// report prints a line of qory run. It reports whether everything came back.
func (c *runCopy) comeBack(ctx context.Context, runID string, report func(string)) bool {
	if c.plain {
		return c.copyBack(ctx, report)
	}
	left := c.dir.Left(c.repos)
	qs, err := c.quarantine(ctx, left, report)
	if err != nil {
		report(c.checkFailed(ctx, err).Error())
		return false
	}
	ok := true
	for _, q := range qs {
		if q == nil {
			continue
		}
		if !c.fetchOne(ctx, q, runID, report) {
			ok = false
		}
	}
	if len(left.Large) > 0 {
		report(fmt.Sprintf(textCopyLarge, maxFileShown, largeFiles(left.Large)))
		ok = false
	}
	if left.Nested {
		report(textCopyBehind)
		ok = false
	}
	if left.Behind {
		report(textCopyNotAdded)
		ok = false
	}
	if !ok {
		c.kept(report)
	}
	return ok
}

// fetchOne fetches the run's commits from the quarantine q into the person's repository, and
// prints the line that lists the branch, the prefix of the run's other refs and any changed
// acting key. The branch is [runCopy.back], or the next free name when a branch there has or
// blocks it by now.
func (c *runCopy) fetchOne(ctx context.Context, q *runcopy.Quarantine, runID string, report func(string)) bool {
	limited, cancel := context.WithTimeout(ctx, c.limit)
	defer cancel()
	changes, err := q.ActingChanges(limited)
	if err != nil {
		report(c.checkFailed(ctx, stepErr(ctx, limited, c.limit, err)).Error())
		return false
	}
	at := filepath.Join(c.root, filepath.FromSlash(q.Repo.Path))
	branch := ""
	if q.Head != "" {
		if branch, err = runcopy.FreeBranchAgain(limited, at, c.back, runID); err != nil {
			report(c.checkFailed(ctx, stepErr(ctx, limited, c.limit, err)).Error())
			return false
		}
	}
	prefix, err := q.FetchInto(limited, at, "refs/heads/"+branch)
	if err != nil {
		if limited.Err() != nil {
			report(c.checkFailed(ctx, limitErr(ctx, limited, textCopyFetchLimit, c.limit, err)).Error())
			return false
		}
		report(shownError(err))
		return false
	}
	var line string
	switch {
	case branch == "":
		line = fmt.Sprintf(textCopyRefs, prefix, exports.Value(at, false), prefix)
	case prefix != "":
		line = fmt.Sprintf(textCopyOnBranch, exports.Value(branch, false), exports.Value(at, false)) + fmt.Sprintf(textCopyOtherRefs, prefix, prefix)
	default:
		line = fmt.Sprintf(textCopyOnBranch, exports.Value(branch, false), exports.Value(at, false))
	}
	if keys := actingKeys(changes); keys != "" {
		line += fmt.Sprintf(textCopyActing, keys)
	}
	report(line)
	return true
}

// copyBack brings the run's files back to the person's plain folder.
func (c *runCopy) copyBack(ctx context.Context, report func(string)) bool {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	res, err := c.dir.CopyBack(limited, c.root)
	if err != nil {
		if ctx.Err() == nil && limited.Err() != nil {
			report(fmt.Sprintf(textCopyBackLimit, copyStepLimit))
		} else {
			report(shownError(err))
		}
		c.failed(res.Failed, report)
		c.generated(res.Generated, report)
		c.kept(report)
		return false
	}
	if len(res.Copied)+len(res.Deleted) > 0 {
		report(fmt.Sprintf(textCopyBack, count(len(res.Copied), "file", "files"), exports.Value(c.root, false), count(len(res.Deleted), "file", "files")))
	}
	c.leftOut(copyBackSkips(res.Skipped, c.dir.Work()), report)
	c.failed(res.Failed, report)
	back := !c.generated(res.Generated, report)
	if len(res.Conflicts) > 0 {
		report(fmt.Sprintf(textCopyConflicts, shownPaths(res.Conflicts)))
	}
	if len(res.NotDeleted) > 0 {
		report(fmt.Sprintf(textCopyNotDeleted, shownPaths(res.NotDeleted)))
	}
	if keys := actingKeys(res.Acting); keys != "" {
		report(fmt.Sprintf(textCopyActingKept, keys))
	}
	// The person's version of each such file stays, so the run's is only in the copy.
	if len(res.Acting) > 0 {
		c.kept(report)
	}
	return back
}

// generated prints the folders of builds and package managers that a copy back left out past
// [runcopy.MaxGeneratedFiles] or [runcopy.MaxGeneratedBytes], and keeps the copy, which still
// has them. It reports whether it printed any.
func (c *runCopy) generated(folders []string, report func(string)) bool {
	if len(folders) == 0 {
		return false
	}
	report(fmt.Sprintf(textCopyGenerated, runcopy.MaxGeneratedFiles, shownBytes(runcopy.MaxGeneratedBytes), shownPaths(folders)))
	c.kept(report)
	return true
}

// shownBytes is n bytes as a line shows it: in GiB or MiB when n is a whole number of them.
func shownBytes(n int64) string {
	switch {
	case n > 0 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n > 0 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}

// actingKeys is the keys of changes as a line lists them, each once in order: a key, or the
// file for a change that no key covers. "" for none.
func actingKeys(changes []runcopy.ActingChange) string {
	var keys []string
	for _, c := range changes {
		k := c.Key
		if k == "" {
			k = c.File
		}
		if k = preparation.ShownPath(k); k == "." {
			k = "the repository's top folder"
		}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, ", ")
}

// maxFileShown is [runcopy.MaxFile] as a line shows it.
var maxFileShown = fmt.Sprintf("%d MiB", runcopy.MaxFile>>20)

// largeFiles is the files of [runcopy.Left.Large] as a line lists them: text that a program in the
// git session can write, each with no control character.
func largeFiles(files []string) string {
	shown := make([]string, len(files))
	for i, f := range files {
		shown[i] = integration.Printable(f)
	}
	return strings.Join(shown, ", ")
}

// shownPaths is paths of a run's copy as a line lists them.
func shownPaths(paths []string) string {
	shown := make([]string, len(paths))
	for i, p := range paths {
		shown[i] = preparation.ShownPath(p)
	}
	return strings.Join(shown, ", ")
}
