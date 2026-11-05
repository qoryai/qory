package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runcopy"
)

// The lines of a walled write run, which works in a copy of the person's checkout. A line
// with a path of the copy shows the person where the run's work stays. The texts of a cause go
// after [textCopyRead] or [textCopyAgain], which have the fix. [textCopyBehind] also stands alone
// beside the line of [runCopy.kept], which has the fix.
const (
	textCopyMount      = "make the mount %s read-only, with :ro on --mount or in wall.mounts: a walled run reads the checkout or works in a copy of it, and a read-write mount lets it write your files directly"
	textCopyOutside    = "run qory inside the checkout %s: a run that may write works in a copy of the checkout, and the working folder %s is outside it"
	textCopyNoFolder   = "commit %[1]s, or run qory in a folder that the commit at HEAD has: a run that may write works in a copy of that commit, which has no folder %[1]s"
	textCopyReserved   = "start the run from a folder that does not contain your home folder or qory's state directory: the folder %s %s, and the run's copy of it would have every file there"
	textCopyBehind     = "part of the run's work did not come back: the copy has changes not committed, commits on another branch or in a submodule, a detached commit, a commit a reset took off the branch, a stash, a tag, a sparse checkout, a nested repository, or files that git add could not read."
	textCopyStepLimit  = "the check of the run's commits ran past its limit of %s, so those commits did not come back"
	textCopyFetchLimit = "the fetch of the run's commits ran past its limit of %s, so those commits did not come back"
	textCopyBackLimit  = "the copy back of the run's files ran past its limit of %s, so part of them did not come back"
	textCopyWaiting    = "got %s: bringing the run's work back, for at most %s. Send a second signal to stop that."
	textCopyOnBranch   = "the run's commits are on %s in %s."
	textCopyActing     = " They change these qory settings, links or repository folders: %s. Check them before you check out the branch."
	textCopyPushActing = "qory run: the run's commits in %s change these qory settings, links or repository folders: %s. Check them before you check out the branch."
	textCopyQoryFile   = "remove .qory from the commit at HEAD in %s, or make it a folder: a compose in a run's copy writes in a folder .qory, and that commit has a file or a link there"
	textCopyRead       = "read the run's files in %s, and run no git there: %s%s"
	textCopyAgain      = "take the work item in again in Qory Apiary: %s%s"
	textCopyLimit      = "the git session ran past its limit of %s, so nothing came back from the run"
	textCopyNoSession  = "the git session did not start, so nothing came back from the run: %s"
	textCopyExited     = "the git session exited with %d, so nothing came back from the run"
	textCopyCut        = "%s during the git session, so nothing came back from the run"
	textCopyCheckCut   = "%s during the check or the fetch of the run's commits, so those commits did not come back"
	textCopyWallKept   = "removing a part of the run's wall failed during the run, so a process may have written the copy after its session ended, and nothing came back from the run"
	textCopyClosed     = "the run ended before the git session started, so nothing came back from the run"
	textCopyStays      = "read the run's files in %s, run no git there, then remove the folder: nothing more came back from the run"
	textCopyBack       = "copied %s back to %s from the run, and deleted %s there."
	textCopyLeftOut    = "left out of the copy back to your folder: %s."
	textCopyConflicts  = "you and the run both changed these files, so yours stay: %s."
	textCopyActingKept = "the run changed these qory settings, so those files stay as you have them: %s."
	textCopyNotIn      = "left out of the run's copy: %s."
	textCopyNoRemove   = "remove %s by hand: %s"
	textCopyNoDisarm   = "remove %s, and run no git there: qory renames each .git in a copy it keeps, and that failed: %s"
)

// copyStepLimit is how long the check of a run's bundles, a fetch from them, or a copy of a run's
// files back may take. A bundle has at most 1 GiB, and git checks every object in it. A test
// sets a shorter one.
var copyStepLimit = 10 * time.Minute

// sessionGraceFromCode is how long the git session of a run from a run code has after its stop
// signal, see [pushAfterSignal]. git needs no time to save anything: a cut session brings nothing
// back.
const sessionGraceFromCode = 3 * time.Second

// removeSetupLimit is how long the removal of a run's setup container may take before its work
// comes back.
const removeSetupLimit = 2 * time.Minute

// stepErr is err of a step under limited, the context of ctx with [copyStepLimit]: the line of
// that limit when the limit ended the step. A stop that ended ctx stays err, for [runCopy.checkFailed].
func stepErr(ctx, limited context.Context, err error) error {
	return limitErr(ctx, limited, textCopyStepLimit, err)
}

// limitErr is [stepErr] with text, the line of the limit.
func limitErr(ctx, limited context.Context, text string, err error) error {
	if err != nil && ctx.Err() == nil && limited.Err() != nil {
		return fmt.Errorf(text, copyStepLimit)
	}
	return err
}

// disarmLimit is how long the renames in a copy that stays may take.
const disarmLimit = time.Minute

// runBranch is the branch a run's commits land on in the person's repository. qory never checks
// it out.
func runBranch(runID string) string { return "qory/run-" + runID }

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
	// plain is whether the checkout is not a git working tree: [runcopy.Dir.CopyBack] brings the
	// files back.
	plain bool
	// repos are the repositories of the copy, the person's first, nil for a plain folder.
	repos []runcopy.Repository
	// out is what the git session leaves out of its commit, by [runcopy.Repository.Path].
	out map[string]runcopy.LeftOut
	// keep is whether the copy stays when the run ends: nothing, or not everything, came back.
	keep bool
	// readOnly is whether the run only reads: its setup works in the copy, the agent has the copy
	// read-only, and nothing comes back.
	readOnly bool
	// pushes is whether the run gets the push step. The person may have no access to this machine,
	// so no line has a path of the copy, and the copy does not stay.
	pushes bool
	// fromCode is whether the run comes from a run code: a line that ends it ends with [codeUsed].
	fromCode bool
	// cutBy is what ended a step whose context ended, as a line has it.
	cutBy func() string
}

// stopped is the line of a return of the run's work that stopped with cause. Past it, the copy
// stays, unless the run gets the push step.
func (c *runCopy) stopped(cause string) error {
	if c.pushes {
		return fmt.Errorf(textCopyAgain, strings.ReplaceAll(cause, c.dir.Root(), "the run's folder"), c.used())
	}
	c.keep = true
	return fmt.Errorf(textCopyRead, exports.Value(c.dir.Work(), false), cause, c.used())
}

// used is what ends a line that ends the run: [codeUsed] for a run from a run code.
func (c *runCopy) used() string {
	if c.fromCode {
		return codeUsed
	}
	return ""
}

// checkFailed is the line of a check of the run's commits that failed with err, under ctx: the
// cut, when ctx ended, and never a failed check for it.
func (c *runCopy) checkFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil && c.cutBy != nil {
		return c.stopped(fmt.Sprintf(textCopyCheckCut, c.cutBy()))
	}
	return c.stopped(shownError(err))
}

// startCopy makes the copy of the checkout at root for the run runID. branch is the branch
// the run works on, "" for [runBranch]. keepOut are the folders the copy must not overlap.
// noRepository is whether the run comes from a run code or a run key and lists no repositories: in
// a folder where git init ran and that has no commit, it gets the copy of a plain folder with
// .git left out. A local run in such a folder stops, since a repository needs a commit.
func startCopy(ctx context.Context, root, runID, branch string, noRepository bool, keepOut ...string) (*runCopy, error) {
	if branch == "" {
		branch = runBranch(runID)
	}
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
	c := &runCopy{dir: dir, root: root, branch: branch, plain: !git || empty, out: map[string]runcopy.LeftOut{}}
	switch {
	case empty:
		err = dir.CopyFolderWithoutGit(ctx, root)
	case c.plain:
		err = dir.CopyFolder(ctx, root)
	default:
		c.repos, err = dir.Clone(ctx, root, branch)
	}
	if err != nil {
		if rerr := dir.Remove(); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, err
	}
	return c, nil
}

// artifactsIn copies the checkout's artifacts and work item into the copy of a repository. A
// plain folder's copy has them already. report prints a line of qory run.
func (c *runCopy) artifactsIn(ctx context.Context, report func(string)) error {
	if c.plain {
		return nil
	}
	res, err := c.dir.CopyArtifactsIn(ctx, c.root)
	if out := slices.Concat(res.Skipped, res.Conflicts); len(out) > 0 {
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

// artifactsBack copies .qory/artifacts of the copy back to the person's, once the run's
// containers are gone, with or without a commit: the git session leaves .qory out. It prints
// what it left out. A file you both have keeps your version, so the copy stays with the
// run's. It returns false when the copy back stopped.
func (c *runCopy) artifactsBack(ctx context.Context, report func(string)) bool {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	res, err := c.dir.CopyArtifactsBack(limited, c.root)
	if len(res.Skipped) > 0 {
		report(fmt.Sprintf(textCopyLeftOut, shownPaths(res.Skipped)))
	}
	if len(res.Conflicts) > 0 {
		report(fmt.Sprintf(textCopyConflicts, shownPaths(res.Conflicts)))
		c.kept(report)
	}
	if err != nil {
		report(shownError(err))
		c.kept(report)
		return false
	}
	return true
}

// kept marks the copy to stay and prints where it is, once. A run with the push step has no
// such line, see [runCopy.pushes].
func (c *runCopy) kept(report func(string)) {
	if c.keep || c.pushes {
		return
	}
	c.keep = true
	report(fmt.Sprintf(textCopyStays, exports.Value(c.dir.Work(), false)) + c.used())
}

// composedIn is at with the home in the copy, for a compose in a checkout whose home is
// inside it: the compose reads the person's files, writes only in the copy, and links the
// copy's files to the home there, so the person's checkout stays as it is.
func (c *runCopy) composedIn(at places) (places, error) {
	work := c.dir.Work()
	dir := checkout.QoryDir(work)
	if info, err := os.Lstat(dir); err == nil && (info.Mode()&fs.ModeSymlink != 0 || !info.IsDir()) {
		return places{}, input(fmt.Errorf(textCopyQoryFile, exports.Value(c.root, false)))
	}
	at.dir = dir
	at.home = filepath.Join(work, config.DefaultHome)
	at.previous = at.report
	at.report = reportFor(at.home)
	at.linksIn = work
	return at, nil
}

// bind makes spec work in the copy: the workspace is the copy with the working folder's
// path in the checkout at at.root, and the checkout's read-write mount is the copy's. A home
// outside the checkout that the checkout's mount held gets its read-only mount.
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
	if at.dir == "" && reallyWithin(at.root, at.home) {
		spec.Mounts = append(spec.Mounts, wall.Mount{Path: at.home, ReadOnly: true})
	}
	spec.Written = writtenPlaces(work, spec.Mounts)
	return nil
}

// recordLinks lists the links the compose wrote into the copy, for the git session to leave
// out: the links into its .qory folder. Call it before any run writes the copy.
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
// git session leaves it out while it stays as the setup left it.
func (c *runCopy) recordSetup(ctx context.Context) error {
	for _, r := range c.repos {
		paths, err := c.dir.SetupChanges(ctx, r)
		if err != nil {
			return err
		}
		states, err := c.dir.SetupStates(ctx, r, paths)
		if err != nil {
			return err
		}
		submodules, err := c.dir.SubmoduleSetup(ctx, r)
		if err != nil {
			return err
		}
		o := c.out[r.Path]
		o.Setup, o.Submodules = states, submodules
		c.out[r.Path] = o
	}
	return nil
}

// gitSession is the bare session after the agent: it commits what the agent left in the copy
// and writes the run's bundles, with no agent, no model key and no bind but the copy and the
// bundle folder. It ends at [runcopy.SessionLimit] at the latest. message is the message of
// its commit, "" for the default. The caller has removed the setup container: a process in
// it could write the copy during the session.
func (c *runCopy) gitSession(ctx context.Context, run openedRun, spec *runSpec, message string) error {
	gs, err := c.dir.GitSession(ctx, c.branch, message, c.repos, c.out)
	if err != nil {
		return c.noSession(ctx, err)
	}
	grace := spec.StopGrace
	if c.fromCode {
		grace = sessionGraceFromCode
	}
	limited, cancel := context.WithTimeout(ctx, runcopy.SessionLimit)
	defer cancel()
	res, err := joinSession(run, limited, session.SessionSpec{
		Command:      gs.Command,
		Args:         gs.Args,
		Env:          gs.Env,
		Dir:          gs.Dir,
		Mounts:       c.dir.SessionMounts(),
		Exec:         spec.Exec,
		ForagerFiles: spec.ForagerFiles,
		Written:      spec.Written,
		Limits:       spec.Limits,
		Stdin:        strings.NewReader(""),
		Stdout:       spec.Stderr,
		Stderr:       spec.Stderr,
		StopSignal:   spec.StopSignal,
		StopGrace:    grace,
		SocketGrace:  spec.SocketGrace,
		Timeout:      runcopy.SessionLimit,
	})
	timedOut := err == nil && res.TimedOut || limited.Err() != nil
	switch {
	case ctx.Err() != nil && (err != nil || timedOut) && c.cutBy != nil:
		return c.stopped(fmt.Sprintf(textCopyCut, c.cutBy()))
	case timedOut:
		return c.stopped(fmt.Sprintf(textCopyLimit, runcopy.SessionLimit))
	case err != nil:
		return c.noSession(ctx, err)
	case res.ExitCode != 0:
		// A session that failed can leave a bundle of part of the work: no bundle is used.
		return c.stopped(fmt.Sprintf(textCopyExited, res.ExitCode))
	}
	return nil
}

// noSession is the stop of a git session that did not start with err. When ctx ended, what ended
// it is the cause, and err only follows from that.
func (c *runCopy) noSession(ctx context.Context, err error) error {
	if ctx.Err() != nil && c.cutBy != nil {
		return c.stopped(fmt.Sprintf(textCopyCut, c.cutBy()))
	}
	return c.stopped(fmt.Sprintf(textCopyNoSession, shownError(err)))
}

// quarantine checks the bundles of the git session, with [copyStepLimit], once the session's
// container is gone. It returns the quarantine per repository, nil for one with no commit
// past its base, and nil with no commit in any: [runcopy.ErrNoBundle] goes to report.
func (c *runCopy) quarantine(ctx context.Context, report func(string)) ([]*runcopy.Quarantine, error) {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	qs, err := c.dir.Quarantine(limited, c.branch, c.repos)
	if errors.Is(err, runcopy.ErrNoBundle) {
		if report != nil && !c.dir.LeftBehind(c.repos) {
			report(err.Error())
		}
		return nil, nil
	}
	return qs, stepErr(ctx, limited, err)
}

// comeBack brings the run's work to the person: for a plain folder the files, and for a
// repository the commits, which land on [runBranch] in each repository, with one line per
// repository. Every git here runs after the run closed, so no container of the run is left.
// report prints a line of qory run. It reports whether everything came back.
func (c *runCopy) comeBack(ctx context.Context, runID string, report func(string)) bool {
	if c.plain {
		return c.copyBack(ctx, report)
	}
	qs, err := c.quarantine(ctx, report)
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
	if c.dir.LeftBehind(c.repos) {
		report(textCopyBehind)
		ok = false
	}
	if !ok {
		c.kept(report)
	}
	return ok
}

// fetchOne fetches the run's commits from the quarantine q into the person's repository, and
// prints the line that lists the branch and any changed acting key.
func (c *runCopy) fetchOne(ctx context.Context, q *runcopy.Quarantine, runID string, report func(string)) bool {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	changes, err := q.ActingChanges(limited)
	if err != nil {
		report(c.checkFailed(ctx, stepErr(ctx, limited, err)).Error())
		return false
	}
	at := filepath.Join(c.root, filepath.FromSlash(q.Repo.Path))
	branch := runBranch(runID)
	if err := q.FetchInto(limited, at, "refs/heads/"+branch); err != nil {
		if limited.Err() != nil {
			report(c.checkFailed(ctx, limitErr(ctx, limited, textCopyFetchLimit, err)).Error())
			return false
		}
		report(shownError(err) + c.used())
		return false
	}
	line := fmt.Sprintf(textCopyOnBranch, exports.Value(branch, false), exports.Value(at, false))
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
			report(fmt.Sprintf(textCopyBackLimit, copyStepLimit) + c.used())
		} else {
			report(shownError(err) + c.used())
		}
		c.kept(report)
		return false
	}
	if len(res.Copied)+len(res.Deleted) > 0 {
		report(fmt.Sprintf(textCopyBack, count(len(res.Copied), "file", "files"), exports.Value(c.root, false), count(len(res.Deleted), "file", "files")))
	}
	if len(res.Skipped) > 0 {
		report(fmt.Sprintf(textCopyLeftOut, shownPaths(res.Skipped)))
	}
	if len(res.Conflicts) > 0 {
		report(fmt.Sprintf(textCopyConflicts, shownPaths(res.Conflicts)))
	}
	if keys := actingKeys(res.Acting); keys != "" {
		report(fmt.Sprintf(textCopyActingKept, keys))
	}
	// The person's version of each such file stays, so the run's is only in the copy.
	if len(res.Conflicts)+len(res.Acting) > 0 {
		c.kept(report)
	}
	return true
}

// pushable is what the push step takes from the quarantines qs: per repository its folder,
// as [preparation.WorkRepository.Dir] has it, the commit, and the lines of acting keys the
// commits change, in order.
func pushable(ctx context.Context, qs []*runcopy.Quarantine) ([]preparation.Quarantined, []string, error) {
	var from []preparation.Quarantined
	var lines []string
	for _, q := range qs {
		if q == nil {
			continue
		}
		dir := q.Repo.Path
		if dir == "" {
			dir = "."
		}
		from = append(from, preparation.Quarantined{Dir: dir, Path: q.Path, Head: q.Head})
		limited, cancel := context.WithTimeout(ctx, copyStepLimit)
		changes, err := q.ActingChanges(limited)
		err = stepErr(ctx, limited, err)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		if keys := actingKeys(changes); keys != "" {
			shown := exports.Value(dir, false)
			if dir == "." {
				shown = "the run's repository"
			}
			lines = append(lines, fmt.Sprintf(textCopyPushActing, shown, keys))
		}
	}
	return from, lines, nil
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

// shownPaths is paths of a run's copy as a line lists them.
func shownPaths(paths []string) string {
	shown := make([]string, len(paths))
	for i, p := range paths {
		shown[i] = preparation.ShownPath(p)
	}
	return strings.Join(shown, ", ")
}
