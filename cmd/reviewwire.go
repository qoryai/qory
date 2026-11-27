package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/secretmask"
)

// reviewInputsDir is the folder of the inputs folder, in/, that has the files of the
// review session's qory.review_session: the diff, the suite result and the build report.
// It is made new for a review join and removed before the next session.
const reviewInputsDir = "review-session"

// reviewStop is the error of a stop of a review join, in the form of a dispatched run,
// whose end line has the cause alone: line 1 is the cause, with no fix and no full stop,
// and line 2, when detail is not "", has the detail, indented by two spaces. The text
// ends with [codeUsed]. A line 1 that is printed after "qory run: " with more than
// 100 characters, and with codeUsed when line 1 is the last line, goes on after its last
// ", so " on an indented line.
func reviewStop(cause, detail string) error {
	detail = strings.TrimSuffix(detail, ".")
	tail := codeUsed
	if detail != "" {
		tail = ""
	}
	if i := strings.LastIndex(cause, ", so "); i >= 0 && len("qory run: ")+len(cause)+len(tail) > 100 {
		cause = cause[:i+1] + "\n  " + cause[i+2:]
	}
	if detail != "" {
		cause += "\n  " + detail
	}
	return errors.New(cause + codeUsed)
}

// reviewJoinSetup is what [planReviewJoin] needs of the run.
type reviewJoinSetup struct {
	run      openedRun
	spec     *runSpec
	workflow string
	// root is the checkout, cwd the working folder and home the harness's home.
	root, cwd, home string
	branch          *preparation.WorkBranch
	// in is the run's inputs folder, in/. Its folder above it, which no mount reaches,
	// has the copy and the reports folder.
	in string
	// suite is the last suite of the run, see the loop's lastSuite.
	suite *suiteRecord
	// copied is whether the run works in a copy of the checkout, see [runCopy].
	copied bool
	// number is the number of the review session, from 1, which is in the names of the
	// folders of the join. runtime is the runtime's name and img the run's image.
	number  int
	runtime string
	img     session.Image
	// findings is the run's findings, nil for a run that reads none: the diff then starts at
	// the run's start and the session has no previous_findings. res is the composed harness
	// of the session, which lists the reviewers of its group.
	findings *findingsRun
	res      *compose.Result
	// For the first session of a Verify run, see [verifiesAsReview], which [planVerify]
	// plans: the session keeps its checkout, so no suite, no working folder and no
	// read-only join. access is the access that the composed stack declares for the
	// workflow, target the run's target, snapshot the work item as the run read it at its
	// start, and types the artifact types of the composed process.
	access   string
	target   *contracts.Target
	snapshot *runapi.Snapshot
	types    []exports.ProcessType
	// targetVersion is the target version of the run's documents, a commit of the target.
	targetVersion string
}

// reviewJoinPlan is what one review join reads and writes, made before the join: the fresh
// copy of the pushed commit that the session reads in place of the checkout,
// the empty working folder, the session's new reports folder, and the files of
// qory.review_session.
type reviewJoinPlan struct {
	// commit is the pushed head of the target repository, which the session reviews, and
	// base the commit that the diff starts at: the base that Qory Apiary stores for the
	// target when fromLedger is true, else the run's start.
	commit, base string
	fromLedger   bool
	// source is the git folder that has the commit.
	source string
	// project are the paths of the runtime's project files, see
	// [render.CommandGuard.ProjectPaths]. projectBase is the commit that the list of
	// changed project files starts at, [preparation.WorkRepository.ProjectBase]: "" for
	// none, and the list then has each project file of the commit.
	project     []string
	projectBase string
	// round is the stage's round of the review, from the run API's work item.
	round int
	// stage is the id of the stage of the round, which a build report is pushed in, and
	// stageSlug its name in a finding, see [stageName].
	stage, stageSlug string
	// suiteFile is the suite result file of the commit, in the run's folder.
	suiteFile string
	client    *runapi.Client
	// copy is the fresh copy, whose commit folder the join mounts read-only. work is the
	// empty folder that is the join's working folder, mounted read-only.
	copy reviewcopy.Folder
	work string
	// reports is the session's new reports folder, and reportsMount its writable mount.
	reports      string
	reportsMount wall.Mount
	// artifacts and home are the places the reports folder stays outside of, see
	// [checkReviewReports].
	artifacts, home string
	// findings is the run's findings, nil for none, found the findings that the work item
	// had when the plan was made, and roles the reviewers of the session's group.
	findings *findingsRun
	found    contracts.Findings
	roles    []string
	// cwd is the checkout's folder, and system and path the repository that it holds, which
	// the fetch of a base reads.
	cwd, system, path string
	// res and workflow are the composed harness of the session and its workflow, which
	// list the reviewers of the group.
	res      *compose.Result
	workflow string
	// changed is the paths that the diff of the review changes, read once the copy is made,
	// and changedErr why the list is not read, nil when it is.
	changed    []string
	changedErr error
	// verdictBefore is the id of the newest verdict of the run before the session joins.
	verdictBefore string
	// verify is true for the plan of a Verify session, see [planVerify]. root is its
	// checkout, the one writable mount beside the reports folder, access the access that
	// the composed stack declares for its workflow, pushedBase whether its base is the
	// base of a pull-request version, occasion the number that its round is posted under,
	// see [findingsPoster.round], and missingOccasion the number of the round that has
	// the finding of each missing report alone, see [findingsRun.postVerify].
	verify          bool
	root            string
	access          string
	pushedBase      bool
	occasion        int
	missingOccasion int
	// copyRun and copySpec are the run and its spec, which the copy session used, kept for
	// [reviewJoinPlan.checkHead] of a Verify session. headLock lets one read of HEAD run at
	// a time, since the container has a fixed name and the read replaces state/head.
	copyRun  openedRun
	copySpec *runSpec
	headLock headLock
}

// headLock lets one holder at a time through, and lets a caller that waits stop when its
// context ends. Its zero value is ready to use.
type headLock struct {
	once  sync.Once
	slots chan struct{}
}

// acquire waits for the lock, or for the end of ctx, and returns the error of ctx then.
func (l *headLock) acquire(ctx context.Context) error {
	l.once.Do(func() { l.slots = make(chan struct{}, 1) })
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *headLock) release() { <-l.slots }

// workspace is the folders of the join, for [reviewJoinSpec].
func (p *reviewJoinPlan) workspace() reviewWorkspace {
	return reviewWorkspace{work: p.work, commit: p.copy.Commit(), reports: p.reportsMount}
}

// planReviewJoin makes the plan of a review join and its folders, and returns the first
// error. The cheap stops come before any folder: a run that cannot join a review session
// (no wall, a runtime other than claude, nested Docker, a setup inside the agent's
// container), no run API or no round of the stage, no suite result file of the reviewed
// commit, and a wall user whose uid is not qory's. The review session never starts without
// its suite result file or its round.
func planReviewJoin(ctx context.Context, s reviewJoinSetup) (*reviewJoinPlan, error) {
	if _, err := readOnlyJoin(*s.spec, s.root, s.workflow, s.runtime, s.img); err != nil {
		return nil, err
	}
	rt, _ := render.Lookup(s.runtime)
	guard, ok := render.ReviewGuard(rt)
	if !ok {
		return nil, noReviewGuard(s.runtime)
	}
	if s.copied {
		return nil, reviewStop("The run works in a copy of the checkout, so the review did not start", "The review reads the pushed commit from the checkout's git folder")
	}
	p := &reviewJoinPlan{home: s.home, artifacts: filepath.Join(checkout.QoryDir(s.root), artifactsDir), res: s.res, workflow: s.workflow, project: guard.ProjectPaths()}
	var err error
	if p.commit, p.base, p.source, err = reviewTarget(s.branch, s.cwd); err != nil {
		return nil, err
	}
	p.projectBase = targetProjectBase(s.branch)
	if p.suiteFile, err = reviewSuiteFile(ctx, s.suite, p.commit, p.source); err != nil {
		return nil, err
	}
	if p.client, err = reviewClient(s.run); err != nil {
		return nil, err
	}
	stage, err := readStage(ctx, p.client, "the review did not start")
	if err != nil {
		return nil, err
	}
	p.stage, p.stageSlug, p.round = stage.id, stage.slug(), stage.round
	if err := p.readLedger(ctx, s, s.run.System()); err != nil {
		return nil, err
	}
	if err := p.folders(s, true); err != nil {
		return nil, err
	}
	return p, nil
}

// folders makes the folders of the plan, once [checkWallUser] passed: the fresh copy, the
// empty working folder when work is true, and the reports folder. On an error it removes
// what it made.
func (p *reviewJoinPlan) folders(s reviewJoinSetup, work bool) error {
	if err := checkWallUser(s.spec); err != nil {
		return err
	}
	var err error
	// The copy, the working folder and the reports folder lie beside the inputs folder in
	// its temporary folder, each named by the session's number: no mount reaches that
	// folder, and Forager stops a run that mounts a folder of the run's folder, which the
	// suite result file is in.
	under := filepath.Dir(s.in)
	number := strconv.Itoa(s.number)
	if p.copy, err = reviewcopy.New(filepath.Join(under, "review-copy-"+number)); err != nil {
		return reviewCopyStop(err)
	}
	if work {
		p.work = filepath.Join(under, "review-work-"+number)
		if err := os.Mkdir(p.work, 0o555); err != nil {
			p.remove()
			return reviewStop("Making the review's working folder failed, so the review did not start", shownError(err))
		}
	}
	if p.reports, p.reportsMount, err = newReviewReports(under, s.number); err != nil {
		p.remove()
		return err
	}
	return nil
}

// removeCopy removes the folders that only the join reads: the copy's folder, which the
// copy session made read-only, and the empty working folder. The reports folder stays,
// until [reviewJoinPlan.remove]. qory reads the reports when the round ends, before this
// call.
func (p *reviewJoinPlan) removeCopy() {
	if p.copy.Root() != "" {
		_ = filepath.WalkDir(p.copy.Root(), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				err = os.Chmod(path, 0o700)
			}
			return err
		})
		_ = os.RemoveAll(p.copy.Root())
	}
	if p.work != "" {
		_ = os.RemoveAll(p.work)
	}
}

// remove removes everything that the plan made, at the end of the run: [removeCopy] and
// the reports folder.
func (p *reviewJoinPlan) remove() {
	p.removeCopy()
	if p.reports != "" {
		_ = os.RemoveAll(p.reports)
	}
}

// readLedger reads the findings of the work item, for a run with findings, and sets the
// base of the diff to the base that Qory Apiary stores for the run's target, the reviewers
// of the group, and the repository that [reviewJoinPlan.fetchBase] reads, which is on
// system. A run with none keeps the run's start as the base.
func (p *reviewJoinPlan) readLedger(ctx context.Context, s reviewJoinSetup, system string) error {
	if !s.findings.active() {
		return nil
	}
	p.findings, p.cwd, p.system = s.findings, s.cwd, system
	for _, r := range s.branch.Repositories {
		if r.Dir == "." {
			p.path = r.Path
		}
	}
	p.roles = reviewGroupRoles(s.res, s.workflow)
	found, err := s.findings.poster.read(ctx, "the review did not start")
	if err != nil {
		return err
	}
	if err := fullWindow(found, "the review did not start"); err != nil {
		return err
	}
	p.found = found
	if base := ledgerBase(found, s.findings.target); base != "" {
		p.base, p.fromLedger = base, true
	}
	return nil
}

// fetchBase makes the base of the diff an object of the checkout's git folder when the
// base comes from Qory Apiary, or from a pull-request version, and the checkout lacks it,
// through the run's host proxy and the git that fetched the checkout. The run has no
// session or setup container when it is called.
func (p *reviewJoinPlan) fetchBase(ctx context.Context, run openedRun) error {
	if !p.fromLedger && !p.pushedBase {
		return nil
	}
	h, err := run.HostProxy()
	if err != nil {
		return withCause(reviewStop("The host proxy did not start, so the review did not start", shownError(err)), err)
	}
	secretmask.Default().AddURLPassword(h.proxy.URL)
	defer h.close()
	if err := preparation.FetchCommit(ctx, p.cwd, os.Environ(), p.system, p.path, p.base, h.proxy); err != nil {
		return withCause(reviewStop("The base commit of the diff was not fetched, so the review did not start", shownError(err)), err)
	}
	return nil
}

// reviewTarget is the pushed head, the base and the git folder of the target repository of
// the run: the repository of branch whose folder is the working folder cwd. The commit is
// the repository's [preparation.WorkRepository.Commit], the commit its last push moved the
// branch to, and the base its Start.
//
// A write run has one pushable repository, the target, so the review reads that one: this
// is the model, not a shortcut. The base is the commit the run started at;
// [planReviewJoin] replaces it with the base that Qory Apiary stores for the target. The
// stop on project files starts at [targetProjectBase] instead.
func reviewTarget(branch *preparation.WorkBranch, cwd string) (commit, base, source string, err error) {
	if branch == nil {
		return "", "", "", reviewStop("The run has no branch, so it has no pushed commit and the review did not start", "")
	}
	for _, r := range branch.Repositories {
		if r.Dir != "." {
			continue
		}
		source = filepath.Join(cwd, ".git")
		if !cleanAbs(source) {
			return "", "", "", reviewStop("The working folder is not an absolute, clean path, so the review did not start",
				fmt.Sprintf("The git folder would be %s", strconv.Quote(source)))
		}
		info, err := os.Lstat(source)
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", "", reviewStop("The checkout has no .git folder, so the review did not start",
				fmt.Sprintf("The review reads the commit from %s", strconv.Quote(source)))
		}
		if err != nil || !info.IsDir() {
			return "", "", "", reviewStop("The checkout's .git is a link or a file, so the review did not start",
				fmt.Sprintf("The review reads the commit from %s", strconv.Quote(source)))
		}
		return r.Commit, r.Start, source, nil
	}
	return "", "", "", reviewStop(`The branch has no repository at ".", so the review did not start`, "")
}

// reviewSuiteFile is the suite result file of last, the last suite of the run, for the
// review of commit. It returns an error for no suite, for a suite that did not start,
// for a suite whose after step left no marker of its commit, and for a suite of another
// commit: the review never starts without the suite of the commit it reads. The commit
// of the suite is last.head, the marker that the script printed, never the status file's
// commit, which the suite's code can write. source is the git folder that has commit.
func reviewSuiteFile(ctx context.Context, last *suiteRecord, commit, source string) (string, error) {
	switch {
	case last == nil || last.path == "" || last.outcome.Started && last.head == "":
		return "", reviewStop("The run has no suite result for the pushed commit, so the review did not start",
			fmt.Sprintf("The review reads %s", commit))
	case last.head == "":
		return "", reviewStop("The full suite of the pushed commit did not start, so the review did not start", "")
	case last.head != commit && committedAtPush(ctx, source, commit, last.head):
		return "", reviewStop("The agent left changes uncommitted, so the suite did not run on the pushed commit and the review did not start",
			fmt.Sprintf("The push committed them as %s on top of %s, the commit the suite ran on", commit, last.head))
	case last.head != commit:
		return "", reviewStop("The last suite ran on another commit than the pushed one, so the review did not start",
			fmt.Sprintf("The review reads %s, and the last suite ran on %s", commit, strconv.Quote(last.head)))
	}
	return last.path, nil
}

// committedAtPushLimit is how long committedAtPush lets git run, and committedAtPushMax
// the most bytes of its output that it reads. The header and the first line of a commit's
// message that it needs are far shorter.
const (
	committedAtPushLimit = 30 * time.Second
	committedAtPushMax   = 64 << 10
)

// committedAtPush reports whether head is the commit that the push step made of what the
// agent left uncommitted, [textChangesLeft], and its first parent is parent. It reads head
// with git as qory runs it on this machine, from the git folder source, for at most
// [committedAtPushLimit], and reads at most [committedAtPushMax] bytes of its output. It
// is false when git or the read fails: the stop then has the text of a suite of another
// commit.
func committedAtPush(ctx context.Context, source, head, parent string) bool {
	ctx, cancel := context.WithTimeout(ctx, committedAtPushLimit)
	defer cancel()
	cmd, err := hostgit.Command(ctx, filepath.Dir(source), "--git-dir="+source, "cat-file", "commit", head)
	if err != nil {
		return false
	}
	hostgit.Group(cmd)
	cmd.WaitDelay = time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return false
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, committedAtPushMax))
	truncated := len(out) >= committedAtPushMax
	if truncated {
		// git may still write to the pipe, which nothing reads now.
		cancel()
	}
	waitErr := cmd.Wait()
	hostgit.StopGroup(cmd)
	if readErr != nil || waitErr != nil && !truncated {
		return false
	}
	header, message, _ := strings.Cut(string(out), "\n\n")
	for line := range strings.Lines(header) {
		if id, ok := strings.CutPrefix(line, "parent "); ok {
			return strings.TrimSpace(id) == parent && strings.HasPrefix(message, strings.TrimSuffix(textChangesLeft, "%d"))
		}
	}
	return false
}

// reviewClient is the client of run's run API.
func reviewClient(run openedRun) (*runapi.Client, error) {
	if !hasRunAPI(run) {
		return nil, reviewStop("The run has no run API, so the review did not start", "The round of the review and the build report come from the run API")
	}
	httpClient, base, err := run.RunAPI()
	var client *runapi.Client
	if err == nil {
		client, err = runapi.New(base, httpClient)
	}
	if err != nil {
		return nil, withCause(reviewStop("The run API client of the review was not made, so the review did not start", shownError(err)), err)
	}
	return client, nil
}

// checkWallUser stops a review join whose wall user is not qory's user. The folders of the
// join belong to qory's user, and the wall's user reads and writes them by its uid, so the
// uid of wall.user must be qory's; its gid is not compared. [oneUser] stops such a run
// before its first session, and this check stops a run that reaches the join otherwise.
func checkWallUser(spec *runSpec) error {
	d, ok := spec.Wall.(*wall.Docker)
	if !ok || d.User == "" {
		return nil
	}
	if uid, ok := wallUserUID(d.User); ok && uid == qoryUID() {
		return nil
	}
	return reviewStop("The wall's user is not qory's user, so the review did not start",
		fmt.Sprintf("The review's folders belong to qory's uid %d, and wall.user is %s", qoryUID(), strconv.Quote(d.User)))
}

// reviewCopyStop is err as a stop of the review join: a [*reviewcopy.Stop] with its cause,
// the words "so the review did not start" and its detail, as [reviewStop] makes it. Any
// other error stays as it is.
func reviewCopyStop(err error) error { return reviewCopyStopAt(err, "the review did not start") }

// reviewCopyStopAt is [reviewCopyStop] with what, the words after "so", in place of "the
// review did not start": what the caller did not do.
func reviewCopyStopAt(err error, what string) error {
	var s *reviewcopy.Stop
	if !errors.As(err, &s) {
		return err
	}
	return reviewStop(s.Cause+", so "+what, shownErrorText(s.Detail))
}

// targetProjectBase is the [preparation.WorkRepository.ProjectBase] of the repository of
// branch at ".", the target: the merge base of the pushed head with the default branch, or
// "" for none. A rerun does not move it, so a project file that an earlier run of the work
// item added still stops the review.
func targetProjectBase(branch *preparation.WorkBranch) string {
	if branch == nil {
		return ""
	}
	for _, r := range branch.Repositories {
		if r.Dir == "." {
			return r.ProjectBase
		}
	}
	return ""
}

// makeCopy runs the copy session in a container of the run's image, as the git session of
// [runCopy.inContainer] runs, with the checkout's git folder read-only and the copy's
// folder writable, and no other bind. Its lines go to out. A change from the project base
// that adds, changes or removes a project file of the runtime stops the review, see
// [projectFilesStop], before the session starts.
func (p *reviewJoinPlan) makeCopy(ctx context.Context, run openedRun, spec *runSpec, out io.Writer) error {
	start := p.copy.Session
	if p.verify {
		start = p.copy.SessionWithHead
	}
	s, err := start(p.source, p.commit, p.base, p.projectBase, p.project)
	if err != nil {
		return reviewCopyStop(err)
	}
	c := gitContainerFor(run, spec, spec.RunID, runcopy.GitSession{Command: s.Command, Args: s.Args, Env: s.Env, Dir: s.Dir},
		[]wall.Mount{{Path: p.source, ReadOnly: true}, {Path: p.copy.Root()}}, out)
	c.Name = "qory-review-copy-" + spec.RunID
	limited, cancel := context.WithTimeout(ctx, runcopy.SessionMax)
	defer cancel()
	code, err := runGitContainer(limited, c)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case limited.Err() != nil:
		return reviewStop(fmt.Sprintf("The session that copies the commit passed its limit of %s, so the review did not start", runcopy.SessionMax), "")
	case err != nil:
		return reviewStop("The container that copies the commit did not start, so the review did not start", shownError(err))
	}
	left, rerr := p.copy.Result()
	switch {
	case code != 0 && rerr != nil:
		return reviewStop(fmt.Sprintf("The session that copies the commit exited with %d, so the review did not start", code), shownError(rerr))
	case code != 0:
		return reviewStop(fmt.Sprintf("The session that copies the commit exited with %d, so the review did not start", code), "")
	case rerr != nil:
		return reviewCopyStop(rerr)
	}
	changed, err := p.copy.Changed()
	if err != nil {
		return reviewCopyStop(err)
	}
	if len(changed) > 0 {
		return projectFilesStop(changed, p.projectBase != "")
	}
	if len(left) > 0 {
		fmt.Fprintf(out, "the copy for the reviewers leaves out %d links and submodules of the commit\n", len(left))
	}
	if p.verify {
		// The session writes the checkout, so the commit that its HEAD points to, as the copy
		// session read it, must be the commit of the copy and the diff.
		head, err := p.copy.Head()
		if err != nil {
			return reviewCopyStop(err)
		}
		if head != p.commit {
			return reviewStop("The checkout's HEAD is not the commit the review reads, so the review did not start",
				fmt.Sprintf("The checkout's HEAD is %s, and the review reads %s", strconv.Quote(head), p.commit))
		}
		p.copyRun, p.copySpec = run, spec
	}
	p.changed, p.changedErr = p.copy.DiffPaths()
	return nil
}

// headCheckLimit is how long [reviewJoinPlan.checkHead] lets its container run. The check
// of a verdict has 90 seconds in all, see [runPushChecks.runAPICheck].
var headCheckLimit = 30 * time.Second

// headMoved is the error of [reviewJoinPlan.checkHead] for a checkout whose HEAD is not
// the commit that the review read. Its text is the form of [reviewStop].
type headMoved struct{ error }

func (e headMoved) Unwrap() error { return e.error }

// checkHead reads the HEAD of the Verify session's checkout again, in a container of the
// run's image from the files of the git folder with no git, as the copy session read it,
// and stops with a [headMoved] when it is not the commit of the review. Any other failure
// to read it is a stop too. what is the end of the first line: what the caller did not do.
//
// shortcut: after the last read, HEAD stays unchanged only if no process writes the
// checkout, and the git folder may change between the check of its type and the mount;
// replace it when no process writes the checkout after the session. A verdict recorded at
// a check binds the HEAD that was read then, and a later move does not change that
// verdict; the end of the session reads HEAD again only when it posts the round.
func (p *reviewJoinPlan) checkHead(ctx context.Context, what string) error {
	if err := p.headLock.acquire(ctx); err != nil {
		return err
	}
	defer p.headLock.release()
	// The session writes the checkout: a git folder that it replaced with a link is never
	// mounted.
	if info, err := os.Lstat(p.source); err != nil || !info.IsDir() {
		return reviewStop("The checkout's .git is not a folder any more, so "+what,
			fmt.Sprintf("The review reads the HEAD from %s", strconv.Quote(p.source)))
	}
	s, err := p.copy.HeadSession(p.source)
	if err != nil {
		return reviewCopyStopAt(err, what)
	}
	// The read replaces state/head, so a read that writes none leaves no earlier commit.
	if err := p.copy.RemoveHead(); err != nil {
		var stopped *reviewcopy.Stop
		if errors.As(err, &stopped) {
			return reviewStop(stopped.Cause+", so "+what, shownErrorText(stopped.Detail))
		}
		return err
	}
	var out lastLine
	c := gitContainerFor(p.copyRun, p.copySpec, p.copySpec.RunID, runcopy.GitSession{Command: s.Command, Args: s.Args, Env: s.Env, Dir: s.Dir},
		[]wall.Mount{{Path: p.source, ReadOnly: true}, {Path: p.copy.Root()}}, &out)
	c.Name = "qory-review-head-" + p.copySpec.RunID
	limited, cancel := context.WithTimeout(ctx, headCheckLimit)
	defer cancel()
	code, err := runGitContainer(limited, c)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case limited.Err() != nil:
		return reviewStop(fmt.Sprintf("Reading the checkout's HEAD passed its limit of %s, so %s", headCheckLimit, what), "")
	case err != nil:
		return reviewStop("The container that reads the checkout's HEAD did not start, so "+what, shownError(err))
	case code != 0:
		return headReadStop(code, out.text(), what)
	}
	head, err := p.copy.Head()
	if err != nil {
		return reviewCopyStopAt(err, what)
	}
	if head != p.commit {
		return headMoved{reviewStop("The checkout's HEAD is not the commit the review read, so "+what,
			fmt.Sprintf("The checkout's HEAD is %s, and the review read %s", strconv.Quote(head), p.commit))}
	}
	return nil
}

// headReadStop is the stop for a read of the checkout's HEAD that exited with code and
// whose last line was text. A line of the script, "qory review copy: <reason>", is the
// cause on line 1, and the exit code is line 2; any other last line follows the exit code.
func headReadStop(code int, text, what string) error {
	exited := fmt.Sprintf("Reading the checkout's HEAD exited with %d", code)
	reason, ok := strings.CutPrefix(text, "qory review copy: ")
	if ok {
		reason = shownErrorText(reason)
	}
	if !ok || reason == "" {
		return reviewStop(exited+", so "+what, shownErrorText(text))
	}
	first, size := utf8.DecodeRuneInString(reason)
	return reviewStop(string(unicode.ToUpper(first))+reason[size:]+", so "+what, exited)
}

// maxShownProjectFiles is the most paths that [projectFilesStop] shows, and
// maxShownProjectPath the most characters of each.
const (
	maxShownProjectFiles = 5
	maxShownProjectPath  = 200
)

// projectFilesStop is the stop of a review whose change from the project base adds,
// changes or removes changed, paths of the runtime's project files, such as
// .claude/skills/x/SKILL.md. The session could load such a file, with the tools and
// settings that it sets. Line 1 is the fix. Line 2 says why the review did not start,
// with the paths, each cut at [maxShownProjectPath] characters with "...", its
// characters that are not printable as ?, and quoted, at most [maxShownProjectFiles] of
// them; and that the stage starts again from the work item, since the run code is used.
// A file that is the same at the project base is not in changed. With baseRead false, the
// run has no project base, changed has each project file of the commit, and line 2 says
// that qory treats them as changed, and that the stage starts again.
func projectFilesStop(changed []string, baseRead bool) error {
	shown := make([]string, 0, maxShownProjectFiles)
	for _, p := range changed[:min(len(changed), maxShownProjectFiles)] {
		shown = append(shown, strconv.Quote(integration.PrintableWithin(p, maxShownProjectPath)))
	}
	list := strings.Join(shown, ", ")
	if more := len(changed) - len(shown); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	if !baseRead {
		return errors.New("Move the change to project files out of the reviewed change in the Build session\n" +
			"  The review did not start: qory could not read the change's base, so it treats these files in the commit as changed: " + list +
			". The run code is used: start the stage again from the work item in Qory Apiary.")
	}
	return errors.New("Move the change to project files out of the reviewed change in the Build session\n" +
		"  The review did not start, since the session could load the settings and tools of " + list +
		", which the change adds, changes or removes. The run code is used: start the stage again from the work item in Qory Apiary.")
}

// inputs writes the files of qory.review_session into in/review-session, read-only, and
// returns the member. in is the run's inputs folder, which the join mounts read-only.
// The suite result and the diff are copied from the run's folder and the copy. The
// build report is the newest version of the artifact type build-report that the stage
// wrote, fetched through the run API; the member is left out when there is none.
//
// previous_findings is the file of the findings of the earlier rounds that the group's
// reviewers raised, with the replies to them; the member is left out when the work item
// has none.
func (p *reviewJoinPlan) inputs(ctx context.Context, in string) (reviewSessionInputs, error) {
	dir := filepath.Join(in, reviewInputsDir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return reviewSessionInputs{}, reviewStop(fmt.Sprintf("Making %s in the inputs folder failed, so the review did not start", strconv.Quote(dir)), shownError(err))
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return reviewSessionInputs{}, reviewModesStop(err)
	}
	suite := filepath.Join(dir, "suite-result.json")
	diff := filepath.Join(dir, "commit.diff")
	files := []struct{ from, to string }{{p.suiteFile, suite}, {p.copy.Diff(), diff}}
	if p.verify {
		// A Verify session runs no full suite and writes no build report.
		suite, files = "", files[1:]
	}
	for _, c := range files {
		if err := copyReadOnly(c.from, c.to); err != nil {
			return reviewSessionInputs{}, reviewStop(fmt.Sprintf("Copying %s into the review's inputs failed, so the review did not start", strconv.Quote(c.from)), shownError(err))
		}
	}
	previous, err := p.previousFile(dir)
	if err != nil {
		return reviewSessionInputs{}, err
	}
	if p.verify {
		return newReviewSessionInputs(p.commit, p.copy.Commit(), diff, p.reports, "", previous, "")
	}
	report, err := p.buildReport(ctx, filepath.Join(dir, "build-report"))
	if err != nil {
		return reviewSessionInputs{}, err
	}
	return newReviewSessionInputs(p.commit, p.copy.Commit(), diff, p.reports, suite, previous, report)
}

// previousFile writes previous_findings into dir, read-only, and returns its path; "" when
// the run has no findings or the group has none from the rounds before.
func (p *reviewJoinPlan) previousFile(dir string) (string, error) {
	if p.findings == nil {
		return "", nil
	}
	data, err := previousFindings(p.round, p.stageSlug, p.roles, p.found)
	if err != nil {
		return "", reviewStop("The findings of the work item do not pass qory's checks, so the review did not start", shownError(err))
	}
	var file findingsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return "", err
	}
	if len(file.Findings) == 0 {
		return "", nil
	}
	path := filepath.Join(dir, openFindingsFile)
	if err := os.WriteFile(path, data, 0o444); err != nil {
		return "", reviewStop(fmt.Sprintf("Writing %s in the review's inputs failed, so the review did not start", strconv.Quote(path)), shownError(err))
	}
	return path, nil
}

// reviewModesStop is err, the error of a change of a mode of a folder or file of the
// review, as a stop of the review join.
func reviewModesStop(err error) error {
	return withCause(reviewStop("qory could not set the modes of the review's folders, so the review did not start", shownError(err)), err)
}

// copyReadOnly copies the regular file src to dst, a new file of mode 0444. A link is an
// error.
func copyReadOnly(src, dst string) error {
	if info, err := os.Lstat(src); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", strconv.Quote(src))
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0o444)
}

// buildReport fetches the newest version of the artifact type build-report that the stage
// wrote, a version that is neither cancelled nor a withdrawal, into the new folder dir, as
// one read-only file, and returns its path; "" when no version qualifies. The newest is
// the one with the latest time, then the highest number. A version with another number of
// files than one is an error: the member is one file.
func (p *reviewJoinPlan) buildReport(ctx context.Context, dir string) (string, error) {
	index, err := p.client.Artifacts(ctx)
	if featureOff(err) {
		return "", nil
	}
	if err != nil {
		return "", withCause(reviewStop("Reading the build report failed, so the review did not start", shownError(err)), err)
	}
	found := newestOfType(index, "build-report", p.stage)
	if found == nil {
		return "", nil
	}
	if len(found.Files) != 1 || !runapi.LocalFilename(found.Files[0].Filename) {
		return "", reviewStop(fmt.Sprintf("The newest build report has %d files, so the review did not start", len(found.Files)),
			fmt.Sprintf("A build report is one file with a plain name, and the version is %s", found.ID))
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", reviewStop(fmt.Sprintf("Making %s in the inputs folder failed, so the review did not start", strconv.Quote(dir)), shownError(err))
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", reviewModesStop(err)
	}
	path := filepath.Join(dir, found.Files[0].Filename)
	var fetchErr error
	if err := writeFresh(path, func(w io.Writer) error {
		fetchErr = p.client.Download(ctx, found.ID, found.Files[0], w)
		return fetchErr
	}); err != nil {
		// The detail is the error of the download or of the file, without the fix that
		// writeFresh puts in front of it.
		detail := err
		if fetchErr == nil || err != fetchErr {
			if inner := errors.Unwrap(err); inner != nil {
				detail = inner
			}
		}
		return "", withCause(reviewStop("Fetching the build report failed, so the review did not start", shownError(detail)), err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		return "", reviewModesStop(err)
	}
	return path, nil
}

// check checks the join that the plan's folders are in, right before it starts: join is
// the spec of [reviewJoinSpec], whose mounts are the final bind list and whose Written set
// is the places the container may write. The workspace is the empty working folder,
// read-only. The copy and the working folder are the only mounts of the join that the run
// did not bind, both read-only, and no mount but the reports folder's is writable. The
// reports folder keeps the rules of [checkReviewReports] against the mounts and against
// Written.
func (p *reviewJoinPlan) check(join session.SessionSpec) error {
	if p.verify {
		return p.checkVerify(join)
	}
	if !join.WorkspaceReadOnly || join.Dir != p.work {
		return reviewStop("The review's working folder is not the empty folder qory made, so the review did not start",
			fmt.Sprintf("The working folder is %s", strconv.Quote(join.Dir)))
	}
	if err := checkReviewReports(p.reports, p.work, p.artifacts, p.home, join.Mounts, os.Geteuid(), p.access); err != nil {
		return err
	}
	if err := p.checkWritten(join.Written); err != nil {
		return err
	}
	readOnly := map[string]int{}
	for _, m := range join.Mounts {
		switch {
		case m.Path == p.reports && m.Target == "":
		case !m.ReadOnly:
			return reviewStop("A mount other than the reports folder is writable, so the review did not start",
				fmt.Sprintf("The mount %s is writable", strconv.Quote(m.Path)))
		case m.Target == "":
			readOnly[m.Path]++
		}
	}
	if readOnly[p.work] != 1 {
		return reviewStop("The review's working folder is not the empty folder qory made, so the review did not start",
			fmt.Sprintf("The working folder is %s", strconv.Quote(p.work)))
	}
	if readOnly[p.copy.Commit()] != 1 {
		return reviewStop("The review has no copy of the commit or no writable reports folder, so it did not start",
			fmt.Sprintf("The copy is %s and the reports folder is %s", strconv.Quote(p.copy.Commit()), strconv.Quote(p.reports)))
	}
	return nil
}

// checkWritten stops a join whose places that it may write, written, contain the reports
// folder.
func (p *reviewJoinPlan) checkWritten(written []string) error {
	for _, w := range written {
		if session.Overlap(w, p.reports) == "contains" {
			return reviewStop("The review's reports folder lies in a folder that the session can write, so the review did not start",
				fmt.Sprintf("The session can write %s, which contains %s", strconv.Quote(w), strconv.Quote(p.reports)))
		}
	}
	return nil
}

// newestOfType is the newest version of the artifacts of the type typ that the stage
// wrote, leaving out a cancelled version and a withdrawal: the one with the latest time,
// then the highest number. It is nil when no version qualifies.
func newestOfType(index *runapi.Index, typ, stage string) *runapi.Version {
	var found *runapi.Version
	for _, a := range index.Artifacts {
		if a.Type != typ {
			continue
		}
		for i := range a.Versions {
			v := &a.Versions[i]
			if v.Cancelled || v.Withdrawn || v.StageID == nil || *v.StageID != stage {
				continue
			}
			if found == nil || v.At > found.At || v.At == found.At && v.Number > found.Number {
				found = v
			}
		}
	}
	return found
}
