package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/runcopy"
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
}

// reviewJoinPlan is what one review join reads and writes, made before the join: the fresh
// copy of the pushed commit that the session reads in place of the checkout,
// the empty working folder, the session's new reports folder, and the files of
// qory.review_session.
type reviewJoinPlan struct {
	// commit is the pushed head of the target repository, which the session reviews, and
	// base the commit that the diff starts at.
	commit, base string
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
	// stage is the id of the stage of the round, which a build report is pushed in.
	stage string
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
}

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
	p := &reviewJoinPlan{home: s.home, artifacts: filepath.Join(checkout.QoryDir(s.root), artifactsDir), project: guard.ProjectPaths()}
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
	if p.stage, p.round, err = reviewRound(ctx, p.client); err != nil {
		return nil, err
	}
	if err = checkWallUser(s.spec); err != nil {
		return nil, err
	}
	// The copy, the working folder and the reports folder lie beside the inputs folder in
	// its temporary folder, each named by the session's number: no mount reaches that
	// folder, and Forager stops a run that mounts a folder of the run's folder, which the
	// suite result file is in.
	under := filepath.Dir(s.in)
	number := strconv.Itoa(s.number)
	if p.copy, err = reviewcopy.New(filepath.Join(under, "review-copy-"+number)); err != nil {
		return nil, reviewCopyStop(err)
	}
	p.work = filepath.Join(under, "review-work-"+number)
	if err := os.Mkdir(p.work, 0o555); err != nil {
		p.remove()
		return nil, reviewStop("Making the review's working folder failed, so the review did not start", shownError(err))
	}
	if p.reports, p.reportsMount, err = newReviewReports(under, s.number); err != nil {
		p.remove()
		return nil, err
	}
	return p, nil
}

// removeCopy removes the folders that only the join reads: the copy's folder, which the
// copy session made read-only, and the empty working folder. The reports folder stays,
// so that the reports outlast the session, until [reviewJoinPlan.remove]; qory reads no
// report from it.
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

// reviewTarget is the pushed head, the base and the git folder of the target repository of
// the run: the repository of branch whose folder is the working folder cwd. The commit is
// the repository's [preparation.WorkRepository.Commit], the commit its last push moved the
// branch to, and the base its Start.
//
// A write run has one pushable repository, the target, so the review reads that one: this
// is the model, not a shortcut.
// shortcut: the reviewer's diff starts at the run's start, since qory keeps no base of the
// runs before; replace it when qory has one. The stop on project files starts at
// [targetProjectBase] instead.
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

// reviewRound reads the work item again and returns its stage's id and the stage's round.
// A work item with no stage, no round or no number of rounds, or a round outside 1 to
// rounds, is an error: no round is invented.
func reviewRound(ctx context.Context, client *runapi.Client) (stage string, round int, err error) {
	snapshot, err := client.WorkItem(ctx)
	if err != nil {
		return "", 0, withCause(reviewStop("Reading the stage's round failed, so the review did not start", shownError(err)), err)
	}
	s := snapshot.Stage
	if s.ID == nil || s.Round == nil || s.Rounds == nil || *s.Round < 1 || *s.Round > *s.Rounds {
		return "", 0, reviewStop("The work item's stage has no valid round, so the review did not start", "The work item needs stage.round and stage.rounds with 1 <= round <= rounds")
	}
	return *s.ID, *s.Round, nil
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
func reviewCopyStop(err error) error {
	var s *reviewcopy.Stop
	if !errors.As(err, &s) {
		return err
	}
	return reviewStop(s.Cause+", so the review did not start", shownErrorText(s.Detail))
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
	s, err := p.copy.Session(p.source, p.commit, p.base, p.projectBase, p.project)
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
	return nil
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
// shortcut: no previous_findings, since qory keeps no findings of the rounds before;
// replace it when qory has them.
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
	for _, c := range []struct{ from, to string }{{p.suiteFile, suite}, {p.copy.Diff(), diff}} {
		if err := copyReadOnly(c.from, c.to); err != nil {
			return reviewSessionInputs{}, reviewStop(fmt.Sprintf("Copying %s into the review's inputs failed, so the review did not start", strconv.Quote(c.from)), shownError(err))
		}
	}
	report, err := p.buildReport(ctx, filepath.Join(dir, "build-report"))
	if err != nil {
		return reviewSessionInputs{}, err
	}
	return newReviewSessionInputs(p.commit, p.copy.Commit(), diff, p.reports, suite, "", report)
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
	var found *runapi.Version
	for _, a := range index.Artifacts {
		if a.Type != "build-report" {
			continue
		}
		for i := range a.Versions {
			v := &a.Versions[i]
			if v.Cancelled || v.Withdrawn || v.StageID == nil || *v.StageID != p.stage {
				continue
			}
			if found == nil || v.At > found.At || v.At == found.At && v.Number > found.Number {
				found = v
			}
		}
	}
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
	if !join.WorkspaceReadOnly || join.Dir != p.work {
		return reviewStop("The review's working folder is not the empty folder qory made, so the review did not start",
			fmt.Sprintf("The working folder is %s", strconv.Quote(join.Dir)))
	}
	if err := checkReviewReports(p.reports, p.work, p.artifacts, p.home, join.Mounts, os.Geteuid()); err != nil {
		return err
	}
	for _, w := range join.Written {
		if session.Overlap(w, p.reports) == "contains" {
			return reviewStop("The review's reports folder lies in a folder that the session can write, so the review did not start",
				fmt.Sprintf("The session can write %s, which contains %s", strconv.Quote(w), strconv.Quote(p.reports)))
		}
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
