package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
)

// textNotChecked is the cause in [textNotStored] of a version of the push step that did
// not pass its push check.
const textNotChecked = "the version did not pass the check for %s"

// The texts of qory run for its push checks. The load's errors start with their fix, alone
// in the first line, and the cause follows on an indented line;
// textCheckNotFinished is the status line of a push whose check returned an error,
// textRoundNotPosted that of a verdict whose round the check could not post, and
// textHeadMoved that of a verdict whose check found the checkout's HEAD at another commit
// than the one the review read. The last two have no cause: the end of a session that
// succeeds shows it only when the failure is the same then, see [findingsRun.postVerify].
const (
	textChecksNoWall     = "Set wall.adapter to docker, and run again.\n  The push checks of %s run in containers of the docker wall alone."
	textChecksNoRunAPI   = "Open the run through a gateway that sends the run API address, and run again.\n  The push checks read the artifacts through the run API, which failed: %s."
	textChecksBadRunAPI  = "Update the run's gateway, and run again.\n  The run API address that the gateway sent is not one the push checks can use: %s."
	textChecksEngine     = "Switch the engine back to the one the run started on, and run again.\n  The push checks would run on the engine %s, and the run's containers are on the engine %s."
	textChecksNoFolder   = "Check the folder of the push checks' files, and run again.\n  The folder is %s, and making it failed: %s."
	textChecksBadImage   = "Set wall.image or --image to an image reference, and run again.\n  Push checks cannot start from %s. A reference starts with a letter or a digit and has only letters, digits, and . _ / : @ -."
	textChecksNoImage    = "Pull the wall's default image onto this machine, and run again.\n  Push checks run in it, and the engine does not have the image %s."
	textChecksNoRuntime  = "Install the container runtime on the engine, and run again.\n  The push checks' container runs under the runtime %s, and the engine does not have it."
	textChecksBadRuntime = "Fix the runtime name in wall.images, and run again.\n  The push checks' container runs under the runtime %s, and Docker accepts no runtime name with a \"/\"."
	textChecksNotSet     = "Run again.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: %s."
	textCheckNotFinished = "the check before a push did not finish, and the push was not stored (503 " + runapi.CodePushCheckUnavailable + "): %s"
	textRoundNotPosted   = "the round was not posted, so the verdict was not stored (503 " + runapi.CodePushCheckUnavailable + ")"
	textHeadMoved        = "the checkout's HEAD moved, so the verdict was not stored (503 " + runapi.CodePushCheckUnavailable + ")"
)

// The errors of [runPushChecks.load] and [runPushChecks.set] for a run from a run code.
// The code is used by then, so the run starts again only from the new run code that taking
// the work item in again makes. That run opens on the engine selected at its start, so
// switching the engine back does not help this one.
const (
	textChecksNoWallFromCode     = "Set wall.adapter to docker, then take the work item in again in Qory Apiary.\n  The push checks of %s run in containers of the docker wall alone" + codeUsed
	textChecksNoRunAPIFromCode   = "Take the work item in again in Qory Apiary, through a gateway that sends the run API address.\n  The push checks read the artifacts through the run API, which failed: %s" + codeUsed
	textChecksBadRunAPIFromCode  = "Update the run's gateway, then take the work item in again in Qory Apiary.\n  The run API address that the gateway sent is not one the push checks can use: %s" + codeUsed
	textChecksEngineFromCode     = "Take the work item in again in Qory Apiary, and do not switch the engine while that run starts.\n  The push checks would run on the engine %s, and the run's containers are on the engine %s" + codeUsed
	textChecksNoFolderFromCode   = "Check the folder of the push checks' files, then take the work item in again in Qory Apiary.\n  The folder is %s, and making it failed: %s" + codeUsed
	textChecksBadImageFromCode   = "Set wall.image or --image to an image reference, then take the work item in again in Qory Apiary.\n  Push checks cannot start from %s" + codeUsed
	textChecksNoDefaultFromCode  = "Set wall.image in forager.yaml, then take the work item in again in Qory Apiary.\n  Push checks run in the wall's default image" + codeUsed
	textChecksNoImageFromCode    = "Pull the wall's default image, then take the work item in again in Qory Apiary.\n  Push checks run in it, and the engine does not have the image %s" + codeUsed
	textChecksNoRuntimeFromCode  = "Install the container runtime, then take the work item in again in Qory Apiary.\n  The push checks' container runs under the runtime %s, and the engine does not have it" + codeUsed
	textChecksBadRuntimeFromCode = "Fix the runtime name in wall.images, then take the work item in again in Qory Apiary.\n  The push checks' container runs under the runtime %s, and Docker accepts no runtime name with a \"/\"" + codeUsed
	textChecksNotSetFromCode     = "Take the work item in again in Qory Apiary.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: %s" + codeUsed
	textChecksUnreadFromCode     = "Take the work item in again in Qory Apiary, so that the new run composes the harness again.\n  The push checks in %s could not be read: %s" + codeUsed
)

// checksStop is the error of a line that stops a run with push checks, with args in it:
// text, or textFromCode for a run from a run code, fromCode.
func checksStop(fromCode bool, text, textFromCode string, args ...any) error {
	if fromCode {
		text = textFromCode
	}
	return fmt.Errorf(text, args...)
}

// The folder in the run directory that holds the temporary folders of the run's push
// checks, and the label, NAME=value, that the wall removes the run's containers by,
// followed by the run's id, as Forager's wall has it.
const (
	pushChecksDir = "push-checks"
	runLabel      = "dev.qory.run="
)

// engineSelectWait is how long the selection of the push checks' engine may take.
const engineSelectWait = 30 * time.Second

// loadPushChecks reads the push checks of a home; tests replace it.
var loadPushChecks = pushcheck.Load

// runPushChecks is the push checks of a walled run, from the home qory run composed for
// it: [runPushChecks.runAPICheck] checks each POST of the run API from behind the wall,
// once [runPushChecks.set] turned it on, and [runPushChecks.check] the push step's
// version. In a run whose home has no push check, nothing is checked.
type runPushChecks struct {
	mu      sync.Mutex
	checker *pushcheck.Checker
	// report writes one status line of qory run, masked; nil writes none.
	report func(string)
	// fromCode is whether the run comes from a run code: its load and set errors are the
	// lines of [checksStop] for such a run.
	fromCode bool
	// inputs is the path of the run's inputs.json, which the check gets, see
	// [pushcheck.Checker.Inputs]; "" for a run with none. inputsData is the bytes qory
	// wrote there.
	inputs     string
	inputsData []byte
	// scope is the push step's scope check, see [runPushChecks.scopeCheck].
	scope runScope
	// verdicts is whether the check limits the verdicts of each session, see
	// [runPushChecks.limitVerdicts]. running is whether a session is joining the run, and
	// mode is how the verdicts of that session are limited, see [runPushChecks.joining].
	// They are separate from scope, which follows the session that pushes. mu guards all
	// three.
	verdicts, running bool
	mode              joinMode
	// round posts the findings of the round of a Verify session, see
	// [findingsRun.postVerify]; nil for any other session. Its second argument is whether
	// the POST records the verdict passed. mu guards it.
	round func(ctx context.Context, passed bool) error
	// types are the artifact types of the latest session's composed process.yaml, which
	// [pushcheck.FilledMember] reads the members that qory fills from. mu guards it.
	types []exports.ProcessType
}

// setTypes sets the artifact types of the session that the next POST follows, see
// [runPushChecks.runAPICheck].
func (p *runPushChecks) setTypes(types []exports.ProcessType) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.types = types
}

// marksFill reports whether a type of the latest session marks a member that qory fills,
// so the check of each POST has a [pushcheck.FilledMember] rule to apply.
func (p *runPushChecks) marksFill() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(p.types, func(t exports.ProcessType) bool { return len(t.Marks) > 0 })
}

// setRound sets the post of a Verify session's round, which [runPushChecks.runAPICheck]
// runs in the check of the session's POST of /verdict, with whether the POST records
// passed, see [pushcheck.RecordsPassed]; nil clears it.
func (p *runPushChecks) setRound(post func(ctx context.Context, passed bool) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.round = post
}

// limitVerdicts makes [runPushChecks.runAPICheck] apply [pushcheck.SessionVerdict] or
// [pushcheck.ReviewVerdict] to each POST, by the mode of the session that is running, and
// [pushcheck.NoSessionVerdict] while no session runs. Call it before [runPushChecks.set],
// and set the check with always, since the verdict rule needs no checker.
func (p *runPushChecks) limitVerdicts() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verdicts = true
}

// joinMode is how a session that joins the run may record its verdict.
type joinMode int

const (
	// joinNone is a session whose workflow does not judge: it records escalate or no
	// verdict, since a later workflow in the loop judges the work. It is the zero value.
	joinNone joinMode = iota
	// joinJudges is a session that judges the work: it records any verdict.
	joinJudges
	// joinReviews is a review session: it records changes-requested, escalate or no
	// verdict, and qory sends passed after the round.
	joinReviews
)

// joinModeOf is the mode of a session whose workflow judges the work when judges is true,
// see [workflowJudges], and that joins as a review join when review is true. A workflow
// with judges false is [joinNone], whether or not it joins as a review join.
func joinModeOf(judges, review bool) joinMode {
	switch {
	case !judges:
		return joinNone
	case review:
		return joinReviews
	}
	return joinJudges
}

// joining records that a session joins the run, with the mode, see [joinModeOf]; call it
// right before the join, and [runPushChecks.joined] as soon as the join returns, with any
// result.
func (p *runPushChecks) joining(mode joinMode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, p.mode = true, mode
}

// joined records that no session runs.
func (p *runPushChecks) joined() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, p.mode = false, joinNone
}

// verdictRefusal is the refusal of a POST by the verdict rule, nil for a POST that passes
// or a run that has no such rule.
func (p *runPushChecks) verdictRefusal(req session.RunAPIRequest) *session.RunAPIRefusal {
	p.mu.Lock()
	verdicts, running, mode := p.verdicts, p.running, p.mode
	p.mu.Unlock()
	switch {
	case !verdicts:
		return nil
	case !running:
		return pushcheck.NoSessionVerdict(req)
	case mode == joinReviews:
		return pushcheck.ReviewVerdict(req)
	}
	// shortcut: a session that is no review join, such as Verify, records its verdict itself,
	// and qory compares it with the reviewers' reports only for a passed with no report,
	// see [findingsRun.postVerify]; Qory Apiary returns 409 for any passed while a blocking
	// finding is open. Replace when qory sends the verdict of such a session from its round.
	return pushcheck.SessionVerdict(mode == joinJudges, req)
}

// current is the checker, nil before the load or for a home with no push check.
func (p *runPushChecks) current() *pushcheck.Checker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checker
}

// set turns on the check of each POST of the run API from behind the wall of run, with
// [runPushChecks.runAPICheck], when the load found push checks or always is true; with
// neither, it sets no check. Forager sets no check once a join passed its checks, so a run
// of several sessions with a run API, see [hasRunAPI], sets it with always, and a later
// session's checks turn on in [runPushChecks.next]; until then the check passes every
// POST. Forager's SetRunAPICheck fails for a run with no run API, so such a run sets no
// check without push checks. Call it after the load and before the run's setup and join.
func (p *runPushChecks) set(run openedRun, always bool) error {
	if p.current() == nil && !always {
		return nil
	}
	if err := run.SetRunAPICheck(p.runAPICheck); err != nil {
		return checksStop(p.fromCode, textChecksNotSet, textChecksNotSetFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	return nil
}

// runAPICheck is the check of each POST for [session.Run.SetRunAPICheck]: the verdict
// rule, see [runPushChecks.limitVerdicts], the rule of the members that qory fills, see
// [pushcheck.FilledMember], which applies in every run, then
// [pushcheck.Checker.RunAPICheck] of the run's checker, which a run with no checker
// lacks. Forager writes no line for the checker's error, so qory run writes it as a
// status line.
func (p *runPushChecks) runAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	if refusal := p.verdictRefusal(req); refusal != nil {
		return refusal, nil
	}
	p.mu.Lock()
	types := p.types
	p.mu.Unlock()
	if refusal := pushcheck.FilledMember(types, req); refusal != nil {
		return refusal, nil
	}
	// A Verify session's round is posted before its verdict reaches Qory Apiary, so a
	// finding that the round closes no longer blocks the verdict passed.
	p.mu.Lock()
	round := p.round
	p.mu.Unlock()
	if round != nil {
		// Qory Apiary reads the last of a member that a body sends twice, so a POST that qory
		// cannot read in one way may be a verdict: it stops before the round.
		if refusal := pushcheck.UnreadablePost(req); refusal != nil {
			return refusal, nil
		}
	}
	if round != nil && pushcheck.IsVerdict(req) {
		if err := round(ctx, pushcheck.RecordsPassed(req)); err != nil {
			if p.report != nil {
				line := textRoundNotPosted
				if errors.As(err, new(headMoved)) {
					line = textHeadMoved
				}
				p.report(line)
			}
			return nil, err
		}
	}
	c := p.current()
	if c == nil {
		return nil, nil
	}
	refusal, err := c.RunAPICheck(ctx, req)
	// report is qory run's status line, which masks the run's secrets.
	if err != nil && p.report != nil {
		p.report(fmt.Sprintf(textCheckNotFinished, shownError(err)))
	}
	return refusal, err
}

// check is [pushcheck.Checker.Check] of the run's checker.
func (p *runPushChecks) check(ctx context.Context, artifactType string, files []pushcheck.File) (*pushcheck.Failure, error) {
	c := p.current()
	if c == nil {
		return nil, nil
	}
	return c.Check(ctx, artifactType, files)
}

// load reads the push checks of home, the home qory run composed for the run runID,
// which the wall of spec mounts read-only at that path. The checks run in a container
// from the wall's default image, see [checkImage], on the engine of the wall, as the
// run's user, with the wall's limits and the run's label, and read the artifacts through
// the run's run API on this machine. A run whose engine does not have the wall's default
// image, or the container runtime that the checks run under, see [checkRuntime], stops
// here, before the setup and the agent. Their temporary folders are in a
// folder of the run directory, which done removes. A home with no push check, and a run
// with no control plane, controlPlane false, load nothing. An error starts with its fix.
func (p *runPushChecks) load(ctx context.Context, run openedRun, home, runID string, spec *runSpec, controlPlane bool) (done func(), err error) {
	checker, err := pushChecker(ctx, run, home, runID, spec, controlPlane, p.fromCode)
	if err != nil || checker == nil {
		return func() {}, err
	}
	checker.Inputs, checker.InputsData = p.inputs, p.inputsData
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checker = checker
	return func() { os.RemoveAll(checker.Dir) }, nil
}

// next makes the checks follow a later session of the run, whose home qory run composed
// again at home, after [runPushChecks.load] and [runPushChecks.set] ran for session 1: the
// checker reads the push checks of that home and the session's inputs.json, which
// [runPushChecks.inputs] and [runPushChecks.inputsData] have by now. No session runs in
// between, so no check reads the checker while it changes. A run whose session 1 loaded no
// checker loads one here: set turned on the check before session 1, see
// [runPushChecks.set], and the check reads the checker of the latest session.
func (p *runPushChecks) next(ctx context.Context, run openedRun, home, runID string, spec *runSpec, controlPlane bool) (done func(), err error) {
	c := p.current()
	if c == nil {
		return p.load(ctx, run, home, runID, spec, controlPlane)
	}
	checks, err := loadPushChecks(home)
	var unread *pushcheck.UnreadError
	if p.fromCode && errors.As(err, &unread) {
		return func() {}, fmt.Errorf(textChecksUnreadFromCode, unread.File, unread.Cause)
	}
	if err != nil {
		return func() {}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	c.Checks, c.Inputs, c.InputsData = checks, p.inputs, p.inputsData
	return func() {}, nil
}

// pushChecker is the checker of [runPushChecks.load], nil for a home with no push check
// and for a run with no control plane, which has no run API and takes no push. fromCode
// is a run from a run code, see [checksStop].
func pushChecker(ctx context.Context, run openedRun, home, runID string, spec *runSpec, controlPlane, fromCode bool) (*pushcheck.Checker, error) {
	checks, err := loadPushChecks(home)
	var unread *pushcheck.UnreadError
	if fromCode && errors.As(err, &unread) {
		return nil, fmt.Errorf(textChecksUnreadFromCode, unread.File, unread.Cause)
	}
	if err != nil || len(checks) == 0 || !controlPlane {
		return nil, err
	}
	return newChecker(ctx, run, home, runID, spec, fromCode, checks,
		filepath.Join(run.Dir(), pushChecksDir))
}

// newChecker is a checker of the push checks checks of a walled run, as [pushChecker]
// describes it, whose temporary folders go in dir, a new folder that it makes. The scope
// check of the push step runs with it too, with no checks, see [runPushChecks.scopeCheck].
func newChecker(ctx context.Context, run openedRun, home, runID string, spec *runSpec,
	fromCode bool, checks []compose.PushCheck, dir string) (*pushcheck.Checker, error) {
	d, ok := spec.Wall.(*wall.Docker)
	if !ok {
		return nil, checksStop(fromCode, textChecksNoWall, textChecksNoWallFromCode, exports.Value(home, false))
	}
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, checksStop(fromCode, textChecksNoRunAPI, textChecksNoRunAPIFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, checksStop(fromCode, textChecksBadRunAPI, textChecksBadRunAPIFromCode, strings.TrimSuffix(shownError(err), "."))
	}
	engine, err := checkEngine(ctx, d, fromCode)
	if err != nil {
		return nil, err
	}
	user := d.User
	if user == "" {
		// The wall's default: the uid:gid of this process.
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	// A run from a run code runs its checks in the wall's default image alone: its
	// policy, from the server, may select an image built from the repository.
	if fromCode && spec.Image == "" {
		return nil, errors.New(textChecksNoDefaultFromCode)
	}
	// The image goes on the engine's command line: a value of another shape than a
	// reference, such as one that starts with -, stops the run before any command gets it.
	image := checkImage(run, spec)
	if !config.IsImageRef(image) {
		return nil, checksStop(fromCode, textChecksBadImage, textChecksBadImageFromCode, exports.Value(image, true))
	}
	checker := &pushcheck.Checker{
		Home: home, Checks: checks, Image: image, Runtime: checkRuntime(run, spec), User: user,
		Engine: engine,
		Fetch:  artifactsForCheck(client),
		Dir:    dir,
		Limits: wall.Limits{CPUs: spec.Limits.CPUs, Memory: spec.Limits.Memory, PIDs: spec.Limits.PIDs},
		Label:  runLabel + runID,
		Mask:   secretmask.Default().Replace,
	}
	// The run's image, for a run with no default image, comes in the run's image step,
	// after this load. An inspect that fails for another cause than a missing image lets
	// the run go on: a check that cannot start stops its push with 503, and the push
	// stores nothing.
	if spec.Image != "" {
		if has, err := checker.HasImage(ctx); err == nil && !has {
			return nil, checksStop(fromCode, textChecksNoImage, textChecksNoImageFromCode, exports.Value(checker.Image, false))
		}
	}
	// The runtime that the checks' container needs, the same way: an info that fails or
	// does not list runtimes as Docker does lets the run go on.
	if has, err := checker.HasRuntime(ctx); err == nil && !has {
		// Docker itself refuses an unlisted name with a "/" ("unknown or invalid runtime
		// name"): moby v28.0.0 daemon/runtime_unix.go:218-220 and :266-268. The fix is
		// the name, not an install.
		if strings.Contains(checker.Runtime, "/") {
			return nil, checksStop(fromCode, textChecksBadRuntime, textChecksBadRuntimeFromCode, exports.Value(checker.Runtime, false))
		}
		return nil, checksStop(fromCode, textChecksNoRuntime, textChecksNoRuntimeFromCode, exports.Value(checker.Runtime, false))
	}
	// The folder is qory's user's alone, who is the run's user too, see [oneUser].
	noFolder := func(err error) error {
		return checksStop(fromCode, textChecksNoFolder, textChecksNoFolderFromCode, exports.Value(checker.Dir, false), strings.TrimSuffix(shownError(exports.HideError(err)), "."))
	}
	if err := os.Mkdir(checker.Dir, 0o700); err != nil {
		return nil, noFolder(err)
	}
	return checker, nil
}

// checkImage is the reference of the image that the push checks of a run of spec start
// from: the wall's default image, --image or wall.image, which Forager reads as the name
// of an image of wall.images first and as a reference otherwise. A change to the
// repository, such as one to its .devcontainer/ that a prebuilt image is built from,
// changes the run's image and not this one, so it cannot turn the checks off. A run on a
// person's machine, with no run code and no default image, which its policy from
// --policy allows, runs them in the run's image; [pushChecker] stops a run from a run
// code with no default image before it calls this.
func checkImage(run openedRun, spec *runSpec) string {
	if spec.Image == "" {
		return run.Image().Ref
	}
	for _, i := range spec.Images {
		if i.Name == spec.Image {
			return i.Ref
		}
	}
	return spec.Image
}

// checkRuntime is the --runtime of the checks' container: the runtime of the image that
// the run's policy selected, run.Image(), when it has one, so a check never runs under a
// weaker runtime than the run's container. Otherwise it is the one Forager gives the
// default image: the entry's runtime, and "" for a default that is a bare reference or for
// no default, whose run's image has no runtime.
func checkRuntime(run openedRun, spec *runSpec) string {
	if runtime := run.Image().Runtime; runtime != "" {
		return runtime
	}
	for _, i := range spec.Images {
		if i.Name == spec.Image {
			return i.Runtime
		}
	}
	return ""
}

// checkEngine is the engine of the push checks of a run behind d: d's command, with the
// selection of the engine that the wall recorded at the run's open, as a new
// [wall.Docker.Engine] reads it. A new selection that reaches an engine with another id
// than the recorded one is an error, which starts with its fix: the checks would run
// on another engine than the run's containers. With no recorded selection, or an engine
// that gives no id, it is d's command with this process's environment. fromCode is a run
// from a run code, see [checksStop].
func checkEngine(ctx context.Context, d *wall.Docker, fromCode bool) (pushcheck.Engine, error) {
	ctx, cancel := context.WithTimeout(ctx, engineSelectWait)
	defer cancel()
	e := pushcheck.Engine{Command: d.Command}
	id, err := d.EngineID(ctx)
	if err != nil || id == "" {
		return e, nil
	}
	selected := (&wall.Docker{Command: d.Command}).Engine(ctx)
	if selected.ID != id {
		return pushcheck.Engine{}, checksStop(fromCode, textChecksEngine, textChecksEngineFromCode, engineShown(selected.ID), engineShown(id))
	}
	return pushcheck.Engine{Command: selected.Command, Env: append([]string{}, selected.Env...)}, nil
}

// engineShown is an engine's id as a text shows it, "with no id" for none.
func engineShown(id string) string {
	if id == "" {
		return "with no id"
	}
	return exports.Value(id, true)
}

// notesFile is the file of a push check's working folder with the work item's notes.
const notesFile = "notes.json"

// textNotesNotList is the error of a work item whose notes in the run API's response are
// not a JSON array.
const textNotesNotList = "The notes in the run API's response are not a JSON array."

// artifactsForCheck is the fetch of a push check through client: the work item's notes
// as notes.json, the artifact index, and the newest version of each artifact of the
// types that is neither cancelled nor a withdrawal, laid out as qory artifact pull writes
// them into its store, with raw bytes and names where the pull escapes hidden
// characters. For a withdrawn name that is the version before the withdrawal, so
// a later push of the name is checked against what it had before; qory artifact pull
// leaves the name out.
func artifactsForCheck(client *runapi.Client) pushcheck.Fetch {
	return func(ctx context.Context, dir string, types []string) error {
		snapshot, err := client.WorkItem(ctx)
		if err != nil {
			return err
		}
		notes, err := checkNotes(snapshot.Notes)
		if err != nil {
			return err
		}
		index, err := client.Artifacts(ctx)
		if err != nil {
			return err
		}
		store := &artifactStore{root: filepath.Dir(dir), dir: dir, fresh: true}
		for _, artifact := range index.Artifacts {
			if !slices.Contains(types, artifact.Type) {
				continue
			}
			chosen := newestBeforeWithdrawal(artifact)
			if chosen == nil {
				continue
			}
			if err := store.fetch(ctx, client, artifact, *chosen); err != nil {
				return err
			}
		}
		if err := store.write(dir, notesFile, func(w io.Writer) error {
			_, err := w.Write(notes)
			return err
		}); err != nil {
			return err
		}
		return store.writeIndex(index.Raw)
	}
}

// checkNotes is the content of notes.json for raw, the notes of the work item's
// snapshot: raw as the run API returned it, or [] when the response has none or null.
func checkNotes(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []byte("[]"), nil
	}
	if trimmed[0] != '[' {
		return nil, errors.New(textNotesNotList)
	}
	return trimmed, nil
}

// checkOutputVersion runs the push check of push's artifact type on push's files, before
// the step stores the version. A check that does not pass is the step's error: the step's
// fix line, then the check's line and its findings, each a line of qory run. So is a check
// that could not run. branch is the branch that the step pushed, nil for none.
func checkOutputVersion(ctx context.Context, checks *runPushChecks, branch *preparation.WorkBranch, push runapi.Push) error {
	files := make([]pushcheck.File, len(push.Files))
	for i, f := range push.Files {
		files[i] = pushcheck.File{Name: f.Filename, Content: f.Content}
	}
	failed, err := checks.check(ctx, push.Type, files)
	if err != nil {
		return withCause(notStored(branch, push.Type, shownError(err)), err)
	}
	if failed == nil {
		return nil
	}
	lines := []string{notStored(branch, push.Type, fmt.Sprintf(textNotChecked, provisioner.ShownType(push.Type))).Error(), failed.Step}
	lines = append(lines, failed.Findings...)
	return errors.New(strings.Join(lines, "\nqory run: "))
}
