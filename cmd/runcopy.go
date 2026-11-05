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
// with a path of the copy tells the person where the run's work stays.
const (
	textCopyMount      = "make the mount %s read-only, with :ro on --mount or in wall.mounts: a run that may write works in a copy of the checkout, and a read-write mount lets it write your files directly"
	textCopyOutside    = "run qory inside the checkout %s: a run that may write works in a copy of the checkout, and the working folder %s is outside it"
	textCopyNoFolder   = "commit %[1]s, or run qory in a folder that the commit at HEAD has: a run that may write works in a copy of that commit, which has no folder %[1]s"
	textCopyOnBranch   = "the run's commits are on %s in %s."
	textCopyActing     = " They change these qory settings: %s. Check them before you check out the branch."
	textCopyPushActing = "qory run: the run's commits in %s change these qory settings: %s. Check them before you check out the branch."
	textCopyLimit      = "look at the run's copy in %s: the git session ran past its limit of %s, so nothing came back from the run"
	textCopyNoSession  = "look at the run's copy in %s: the git session did not start, so nothing came back from the run: %s"
	textCopyClosed     = "look at the run's copy in %s: the run ended before the git session started, so nothing came back from the run"
	textCopyStays      = "look at the run's copy in %s, then remove it: nothing more came back from the run"
	textCopyBack       = "copied %s back to %s from the run, and deleted %s there."
	textCopyLeftOut    = "left out of the copy back to your folder: %s."
	textCopyConflicts  = "you and the run both changed these files, so yours stay: %s."
	textCopyActingKept = "the run changed these qory settings, so those files stay as you have them: %s."
	textCopyNoRemove   = "remove %s by hand: %s"
)

// copyStepLimit is how long the check of a run's bundles, or a fetch from them, may take.
// A bundle has at most 1 GiB, and git checks every object in it.
const copyStepLimit = 10 * time.Minute

// runRef is the ref a run's commits land on in the person's repository. qory never checks
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
	// plain says the checkout is no git working tree: [runcopy.Dir.CopyBack] brings the
	// files back.
	plain bool
	// repos are the repositories of the copy, the person's first, nil for a plain folder.
	repos []runcopy.Repository
	// out is what the git session leaves out of its commit, by [runcopy.Repository.Path].
	out map[string]runcopy.LeftOut
	// keep says the copy stays when the run ends: nothing, or not everything, came back.
	keep bool
}

// startCopy makes the copy of the checkout at root for the run runID. branch is the branch
// the run works on, "" for [runBranch]. keepOut are the folders the copy must not overlap.
func startCopy(ctx context.Context, root, runID, branch string, keepOut ...string) (*runCopy, error) {
	if branch == "" {
		branch = runBranch(runID)
	}
	git, err := workingTree(root)
	if err != nil {
		return nil, err
	}
	dir, err := runcopy.New(runID, keepOut...)
	if err != nil {
		return nil, err
	}
	c := &runCopy{dir: dir, root: root, branch: branch, plain: !git, out: map[string]runcopy.LeftOut{}}
	if c.plain {
		err = dir.CopyFolder(root)
	} else {
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

// done removes the copy, unless a line of the run told the person that it stays. report
// prints a line of qory run.
func (c *runCopy) done(report func(string)) {
	if c == nil || c.keep {
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
func (c *runCopy) artifactsBack(report func(string)) bool {
	res, err := c.dir.CopyArtifactsBack(c.root)
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

// kept marks the copy to stay and prints where it is, once.
func (c *runCopy) kept(report func(string)) {
	if c.keep {
		return
	}
	c.keep = true
	report(fmt.Sprintf(textCopyStays, exports.Value(c.dir.Work(), false)))
}

// composedIn is at with the home in the copy, for a compose in a checkout whose home is
// inside it: the compose reads the person's files, writes only in the copy, and links the
// copy's files to the home there, so the person's checkout stays as it is.
func (c *runCopy) composedIn(at places) (places, error) {
	work := c.dir.Work()
	dir := checkout.QoryDir(work)
	if info, err := os.Lstat(dir); err == nil && (info.Mode()&fs.ModeSymlink != 0 || !info.IsDir()) {
		target, _ := os.Readlink(dir)
		return places{}, &render.ForeignPathError{Path: dir, Target: target}
	}
	at.dir = dir
	at.home = filepath.Join(work, config.DefaultHome)
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
		if m.Path == at.root && !m.ReadOnly {
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
		o := c.out[r.Path]
		o.Setup = states
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
		return fmt.Errorf(textCopyNoSession, exports.Value(c.dir.Work(), false), shownError(err))
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
		StopGrace:    spec.StopGrace,
		SocketGrace:  spec.SocketGrace,
		Timeout:      runcopy.SessionLimit,
	})
	switch {
	case err == nil && (res.TimedOut || limited.Err() != nil):
		c.keep = true
		return fmt.Errorf(textCopyLimit, exports.Value(c.dir.Work(), false), runcopy.SessionLimit)
	case err != nil && limited.Err() != nil:
		c.keep = true
		return fmt.Errorf(textCopyLimit, exports.Value(c.dir.Work(), false), runcopy.SessionLimit)
	case err != nil:
		c.keep = true
		return fmt.Errorf(textCopyNoSession, exports.Value(c.dir.Work(), false), shownError(err))
	}
	return nil
}

// quarantine checks the bundles of the git session, with [copyStepLimit], once the session's
// container is gone. It returns the quarantine per repository, nil for one with no commit
// past its base, and nil with no commit in any: [runcopy.ErrNoBundle] goes to report.
func (c *runCopy) quarantine(ctx context.Context, report func(string)) ([]*runcopy.Quarantine, error) {
	limited, cancel := context.WithTimeout(ctx, copyStepLimit)
	defer cancel()
	qs, err := c.dir.Quarantine(limited, c.branch, c.repos)
	if errors.Is(err, runcopy.ErrNoBundle) {
		if report != nil {
			report(err.Error())
		}
		return nil, nil
	}
	if err != nil {
		c.keep = true
		return nil, err
	}
	return qs, nil
}

// comeBack brings the run's work to the person: for a plain folder the files, and for a
// repository the commits, which land on [runBranch] in each repository, with one line per
// repository. Every git here runs after the run closed, so no container of the run is left.
// report prints a line of qory run. It reports whether everything came back.
func (c *runCopy) comeBack(ctx context.Context, runID string, report func(string)) bool {
	if c.plain {
		return c.copyBack(report)
	}
	qs, err := c.quarantine(ctx, report)
	if err != nil {
		report(shownError(err))
		c.kept(report)
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
		report(shownError(err))
		return false
	}
	at := filepath.Join(c.root, filepath.FromSlash(q.Repo.Path))
	branch := runBranch(runID)
	if err := q.FetchInto(limited, at, "refs/heads/"+branch); err != nil {
		report(shownError(err))
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
func (c *runCopy) copyBack(report func(string)) bool {
	res, err := c.dir.CopyBack(c.root)
	if err != nil {
		report(shownError(err))
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
		cancel()
		if err != nil {
			return nil, nil, err
		}
		if keys := actingKeys(changes); keys != "" {
			lines = append(lines, fmt.Sprintf(textCopyPushActing, exports.Value(dir, false), keys))
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
		k = preparation.ShownPath(k)
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
