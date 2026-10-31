// Package prebuild runs a run of the step prebuild, for qory run once it redeemed a run
// code whose documents have step prebuild: it builds the environment of the run's target
// into images behind the wall of the run, pushes them to the places the documents set,
// and writes the run's dev.qory.run.built. Each run gets a run directory,
// <state>/pool/runs/<run_id>, mode 0700, holding:
//
//	work/   an empty git working tree, into which the run's code is fetched
//	state/  XDG_STATE_HOME of every command, mode 0700, and the folder of the run's record
//	out/    where the step behind the wall writes its result, [ResultFile]
//
// The steps:
//
//  1. The open: the run opens at the gateway of its run key, with the wall of
//     [Settings.Wall] and the image [Settings.Image] is set to, and its record in the folder
//     that [explore.RecordDir] returns for work. A run whose policy is not the one of the
//     server's run configuration, as the record's dev.qory.run.policy_applied has it,
//     stops here. The run stays open until Launch returns. Whether the run started is
//     whether the open made its record.
//  2. The fetch, in qory run's process: through the run's host proxy, [preparation.Run]
//     fetches the run's repositories into work, as qory run does once a run is open. The
//     fetch has [fetchTimeout], counted from the open. Every git of the fetch starts from
//     the environment of [child.Env], so git is found on its PATH.
//  3. The commit: [Commit] reads it from work/.git, with no git run.
//  4. The step, behind the wall: one session joins the run, with the qory helper's
//     [StepArgs] and the [Step] as its command, work as its working folder and out
//     mounted writable. [RunStep] builds work on the wall's nested Docker and pushes the
//     images through the run's proxy, with no login: the target image as
//     <image_destination>:<run_id>, and the environment image the build keeps apart as
//     <environment_destination>:<run_id>. A run with environment_image builds the target
//     image from it, and builds no environment image. The session gets no variable of
//     this machine's and no model key. Its lines go to the log.
//  5. The report: Launch reads out/result.json, [readResult], and checks each image's
//     name against the run's destinations, then [session.Run.Built] writes
//     dev.qory.run.built with the target image, its size, the commit and the inputs'
//     environment_ref; the environment image, by its digest, when the build kept one
//     apart and it was pushed, or the build started from environment_image; and the base
//     image, by its digest, when the build resolved the image its target step starts
//     from. Then the run closes succeeded.
//
// The nested Docker's images and build cache go with the wall when the run closes; this
// machine's Docker has none of them, and the registry keeps the images pushed. The run
// directory is removed at the end, unless events of the run did not reach its gateway.
package prebuild

import (
	"bufio"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/mask"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/secretmask"
)

// exitTimeout is the exit code of a run closed at its time limit.
const exitTimeout = 124

// fetchTimeout is the fetch's time limit, counted from the run's open: 15 minutes. A
// test shortens it.
var fetchTimeout = 15 * time.Minute

// fetchPrefix starts each line and error of [preparation.Run].
const fetchPrefix = "qory run: "

// errFetchLimit is the cause of a fetch stopped at [fetchTimeout].
var errFetchLimit = errors.New("the fetch ran past its time limit")

// The words of a run stopped by the end of Launch's context: the fix, the step, then the
// cause.
const (
	textStoppedBuild = "start the run again with a new run code: the run was cancelled during the build: %v"
	textStoppedPush  = "start the run again with a new run code: the run was cancelled during the push: %v"
)

// The words of the fetch and of the close. Each is one format string.
const (
	textNoRepositories = "list the run's repositories in %s in the run configuration: a prebuild fetches them, and the variable is empty"
	textNoKey          = "give the prebuild its run key: it has none"
	textNotOpened      = "check that this machine reaches the run's gateway: the run did not open there: %s"
	textNoHostProxy    = "check that qory run can listen on a port of 127.0.0.1: the run's host proxy, through which qory run fetches the run's code, did not open: %s"
	textNotClosed      = "update qory: the run could not be closed: %s"
	textClosedBefore   = "check the run's record in Qory Apiary: the gateway closed the run as %s before qory run closed it"
	textNoRecord       = "check that this machine can write qory run's state directory: the run opened and made no record"
	textNotReported    = "update qory: the build's result could not be written to the run: %v"
	textPlaceSecret    = "ask Qory Apiary's instance admin: the run's %s contains qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_, which start a secret, and qory writes no image with them to dev.qory.run.built"
	textPlaceCase      = "ask Qory Apiary's instance admin: the run's %s has upper-case letters, and qory writes images to dev.qory.run.built by lower-case references alone"
	textUndelivered    = "check that this machine reaches the run's gateway: %s of the run did not reach it; %s/undelivered contains them"
	textNotServer      = "set a run configuration for the run's workspace in Qory Apiary: a prebuild runs under its policy alone, and the run's policy is %s"
	textNotRegistry    = "set pool.registry to the registry of Qory Apiary's QORY_REGISTRY, or ask its instance admin: the run's %s is not under %s"
	textNotJoined      = "check the wall and wall.prebuild_image in the run configuration's forager.yaml: the prebuild's session behind the wall did not start: %s"
	textNoResult       = "check the prebuild's lines behind the wall: its result, out/" + ResultFile + ", could not be read: %s"
	textStepExited     = "check the prebuild's lines behind the wall: the step %s and wrote no error"
	textResultPlace    = "check the prebuild's lines behind the wall: its result has the image %s for the run's %s, which is %s"
)

// undeliveredError is a close that left n events of the run that its gateway did not
// accept, in the run's record at record: the record stays.
type undeliveredError struct {
	n      int
	record string
}

func (e *undeliveredError) Error() string {
	events := strconv.Itoa(e.n) + " events"
	if e.n == 1 {
		events = "1 event"
	}
	return fmt.Sprintf(textUndelivered, events, exports.Value(e.record, false))
}

// maxInspect is the most bytes read of docker image inspect's answer.
const maxInspect = 1 << 20

// inspectFormat is what docker image inspect prints of the image: one JSON object.
const inspectFormat = `{"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json .Variant}}}`

var (
	// runIDShape is a run id: a UUID in the canonical lower-case form.
	runIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// pinnedShape is an image pinned by digest, as the build takes the agent's.
	pinnedShape = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	// destinationShape is a place at a registry, as image_destination and
	// environment_destination are: image_place of the run contract's
	// documents.schema.json, in lower case. The host is DNS labels joined by dots, with
	// hyphens inside a label, and an optional port; or one label and a port; or
	// localhost. The path has no tag and no digest.
	destinationShape = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+(?::[0-9]{1,5})?|[a-z0-9](?:[a-z0-9-]*[a-z0-9])?:[0-9]{1,5}|localhost)(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)+$`)
)

// Settings are what every prebuild takes from qory run's settings.
type Settings struct {
	// State is qory run's state directory, an absolute path. The run directories are
	// under <State>/pool/runs, as [explore.Dir] has them.
	State string
	// RunConfig is the XDG_CONFIG_HOME of every command, an absolute directory: the
	// configuration directory whose qory/forager.yaml sets the wall.
	RunConfig string
	// AgentImage and User are wall.image, pinned by digest, and wall.user of RunConfig's
	// forager.yaml: the build's agent image, and the wall user the image runs as.
	AgentImage, User string
	// Wall is the wall the run opens with, its helper wall.helper; Image is the name of
	// the image it starts, wall.prebuild_image, and Images that image's definition alone,
	// so a policy that selects another image is refused. CAEnv is wall.ca_env, Exec the
	// helper's exec verb behind the wall, and Limits the wall's limits, wall.cpus,
	// wall.memory, wall.pids_limit and wall.shm_size.
	Wall   wall.Wall
	Image  string
	Images []session.Image
	CAEnv  []string
	Exec   []string
	Limits wall.Limits
	// Registry is pool.registry, the image name the prebuild's images must be pushed
	// under; empty checks no destination against it.
	Registry string
	// Version is the version the run's events report, qory's.
	Version string
	// Machine is machine of forager.yaml, the adapter that made the machine, which
	// run.started reports; empty leaves it out.
	Machine string
	// Open opens the run at its gateway; nil is [session.Open]. A test replaces it.
	Open func(ctx context.Context, spec session.OpenSpec) (Opened, error)
	// Fetch fetches the run's repositories, as [preparation.Run]; nil is
	// preparation.Run. A test replaces it.
	Fetch func(ctx context.Context, dir string, base []string, getenv func(string) string, p proxygit.Proxy, report io.Writer) (bool, error)
	// Git is the git binary that makes work an empty working tree; empty is git. A name
	// with no path separator is found on the PATH every command gets, as
	// [child.Program] has it.
	Git string
	// Log takes each line the commands write, after the run id, and each failure.
	Log func(line string)
	// Masker is the run's mask list, OpenSpec.Masker of the run's session, which masks
	// the run's record, events and lines with it. It is required: qory does not mask the
	// step's output again, so with nil, Forager's session makes a masker without qory's
	// values, and those values stay unmasked.
	Masker *mask.Masker
}

// Run is one run of the step prebuild, as the documents of its run code set it.
type Run struct {
	// RunID is the run's id.
	RunID string
	// Key is the run key. Launch clears it on every path.
	Key child.Secret
	// Inputs are the documents' inputs, the bytes as they came: exactly version, 1,
	// and environment_ref.
	Inputs []byte
	// ImageDestination is the documents' image_destination: where the target image is
	// pushed, as <ImageDestination>:<RunID>.
	ImageDestination string
	// EnvironmentDestination is the documents' environment_destination: where the
	// environment image the build keeps apart is pushed, as
	// <EnvironmentDestination>:<RunID>. It is "" when EnvironmentImage is set.
	EnvironmentDestination string
	// EnvironmentImage is the documents' environment_image: the image the build's target
	// step starts from, by digest, as [devimage.Options.EnvironmentImage]; "" builds the
	// environment step. A run whose environment image is not in its registry closes
	// failed with image_missing and -1.
	EnvironmentImage string
}

// Opened is a run open at its gateway, as the prebuild uses it: a [*session.Run].
type Opened interface {
	HostProxy() (session.HostProxy, error)
	// Join runs the step's session behind the wall, as [session.Run.Join].
	Join(ctx context.Context, spec session.SessionSpec) (*session.Result, error)
	// System and Repositories are the server's values of [preparation.EnvSystem] and
	// [preparation.EnvRepositories], "" when it sets none.
	System() string
	Repositories() string
	// Built writes the run's dev.qory.run.built, as [session.Run.Built].
	Built(b session.Built) error
	Close(ctx context.Context, out session.Outcome) (*session.Closed, error)
}

// Result is how a prebuild ended.
type Result struct {
	// Dir is the run directory, "" when it was not made. Launch removes it.
	Dir string
	// Record reports whether the open made the run's record: the run started.
	Record bool
	// Image is what dev.qory.run.built was written with, or what was built so far.
	Image session.Built
}

// Launch runs the prebuild r, until it ends or ctx ends; at ctx's end the step running
// is stopped, behind the wall too. The error is why it did not succeed. Once the run is
// open, it closes once, as Launch returns:
//
//	step                                     the run closes
//	the fetch at its time limit              cancelled, timeout, 124
//	ctx ended                                cancelled, interrupted, -1
//	the build: environment image not in      failed, image_missing, -1
//	  its registry
//	any other failure                        failed, -1
//	dev.qory.run.built written               succeeded, -1
//
// A failed close is the error of a prebuild that succeeded; after a failure, it is
// logged, and the error is the failure's. A close that left events the gateway did not
// accept, on a run the gateway did not close, is a failed close, and the run directory
// stays, with the record's delivered.log and undelivered/, as qory run leaves the
// checkout and the record of an explore run in that case. Otherwise the run directory is
// removed before Launch returns.
func Launch(ctx context.Context, s Settings, r Run) (res Result, err error) {
	if r.Key != nil {
		defer r.Key.Clear()
	}
	// qory run sets both once it checked wall.prebuild_image, so no person reaches this
	// check.
	if s.Wall == nil || s.Image == "" {
		return res, errors.New("the prebuild needs the wall and the image it runs in behind it")
	}
	envRef, err := checkRun(s, r)
	if err != nil {
		return res, err
	}
	res.Image.EnvironmentRef = envRef
	dir, err := makeDir(ctx, s, r.RunID)
	res.Dir = dir
	// keep is set once the close left events in the run's record that its gateway did
	// not accept: then the run directory stays.
	keep := false
	if dir != "" {
		defer func() {
			if keep {
				return
			}
			if rmErr := explore.Remove(s.State, r.RunID); rmErr != nil {
				s.logError(r.RunID, "the run directory could not be removed: ", rmErr)
			}
		}()
	}
	if err != nil {
		return res, err
	}
	work, state := filepath.Join(dir, "work"), filepath.Join(dir, "state")
	record, err := explore.RecordDir(state, work, r.RunID)
	if err != nil {
		return res, err
	}
	// The fetch.
	run, out, err := s.fetch(ctx, r, work, state, record)
	_, statErr := os.Stat(record)
	res.Record = statErr == nil
	// The run key opens nothing more.
	if r.Key != nil {
		r.Key.Clear()
	}
	if run == nil {
		return res, err
	}
	// The run is open: it closes with out once the rest is done, cancelled when ctx
	// ended during a step after the fetch.
	fetched := err == nil
	defer func() {
		if fetched && err != nil && ctx.Err() != nil {
			out = session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
		}
		closeErr := s.closeRun(ctx, run, out, record)
		var undelivered *undeliveredError
		keep = errors.As(closeErr, &undelivered)
		switch {
		case closeErr == nil:
		case err == nil:
			err = closeErr
		default:
			s.logError(r.RunID, "", closeErr)
		}
	}()
	if err != nil {
		return res, err
	}
	out = session.Outcome{State: event.StateFailed, ExitCode: -1}
	if !res.Record {
		return res, errors.New(textNoRecord)
	}
	// The commit, from this run's fetch alone.
	if res.Image.Commit, err = Commit(work); err != nil {
		return res, err
	}
	// The step, behind the wall.
	got, err := s.step(ctx, run, r, dir)
	if got.ImageMissing {
		out = session.Outcome{State: event.StateFailed, Reason: refusal.ImageMissing, ExitCode: -1}
	}
	if err != nil {
		return res, err
	}
	res.Image.Image = r.ImageDestination + "@" + got.Target.Digest
	res.Image.Size = got.Target.Size
	res.Image.EnvironmentImage = r.EnvironmentImage
	if got.Environment != nil {
		res.Image.EnvironmentImage = r.EnvironmentDestination + "@" + got.Environment.Digest
	}
	res.Image.Base = got.Base
	// The report.
	if err = run.Built(res.Image); err != nil {
		return res, fmt.Errorf(textNotReported, err)
	}
	out = session.Outcome{State: event.StateSucceeded, ExitCode: -1}
	return res, nil
}

// step runs the prebuild's step of r behind the wall of run, in one session: the qory
// helper's [StepArgs] and [Step.Args], in dir/work, with dir/out, empty, mounted
// writable, as places a walled run writes. The session gets no variable of this
// machine's, none of the run's and no model key: the wall gives it the run's proxy and
// bundle, and nothing else. Its output goes to the log. It returns the result the step
// wrote, checked against r, and the error of a step that did not push.
func (s Settings) step(ctx context.Context, run Opened, r Run, dir string) (stepResult, error) {
	work, out := filepath.Join(dir, "work"), filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		return stepResult{}, err
	}
	st := Step{
		Out: out, RunID: r.RunID, ImageDestination: r.ImageDestination, EnvironmentDestination: r.EnvironmentDestination,
		EnvironmentImage: r.EnvironmentImage, AgentImage: s.AgentImage, User: s.User,
	}
	// The session masks its output with s.Masker, so lines gets it as it is: a second
	// pass would mask inside a placeholder, such as qory-proxy-password.
	lines := s.lines(r.RunID)
	joined, err := run.Join(ctx, session.SessionSpec{
		Command: wall.HelperPath, Args: append(slices.Clone(StepArgs), st.Args()...), Dir: work,
		Mounts: []wall.Mount{{Path: out}}, Written: []string{work, out},
		ForagerFiles: []string{filepath.Join(s.RunConfig, "qory")},
		Exec:         s.Exec, Limits: s.Limits,
		Stdin: strings.NewReader(""), Stdout: lines, Stderr: lines,
	})
	lines.Flush()
	if err != nil {
		if ctx.Err() != nil {
			return stepResult{}, fmt.Errorf(textStoppedBuild, context.Cause(ctx))
		}
		return stepResult{}, fmt.Errorf(textNotJoined, integration.Printable(exports.HideError(err).Error()))
	}
	got, readErr := readResult(out)
	switch {
	case ctx.Err() != nil && got.Stopped == stoppedPush:
		return stepResult{}, fmt.Errorf(textStoppedPush, context.Cause(ctx))
	case ctx.Err() != nil:
		return stepResult{}, fmt.Errorf(textStoppedBuild, context.Cause(ctx))
	case readErr != nil:
		return stepResult{}, readErr
	case got.Error != "":
		return stepResult{ImageMissing: got.ImageMissing}, errors.New(shownError(got.Error))
	case joined != nil && joined.ExitCode != 0:
		return stepResult{}, fmt.Errorf(textStepExited, ended(child.Result{ExitCode: joined.ExitCode}))
	}
	return got, checkResult(got, r)
}

// checkResult returns an error for a result of the step that is not one of r: a target image that is
// not under image_destination, an environment image that is not under
// environment_destination, or one for a run that has environment_image, and a digest,
// size or base image that dev.qory.run.built does not take.
func checkResult(got stepResult, r Run) error {
	if got.Target == nil {
		return fmt.Errorf(textNoResult, "it has no target image")
	}
	images := []struct {
		member, want string
		image        *stepImage
	}{{"image_destination", r.ImageDestination, got.Target}}
	if got.Environment != nil {
		images = append(images, struct {
			member, want string
			image        *stepImage
		}{"environment_destination", r.EnvironmentDestination, got.Environment})
	}
	for _, i := range images {
		switch {
		case i.want == "":
			return fmt.Errorf(textResultPlace, "an environment image", i.member, "not set")
		case i.image.Name != i.want:
			return fmt.Errorf(textResultPlace, integration.Printable(exports.Value(i.image.Name, true)), i.member, exports.Value(i.want, true))
		case !digestShape.MatchString(i.image.Digest):
			return fmt.Errorf(textNoResult, "a digest is not sha256: and 64 lower-case hexadecimal digits")
		}
	}
	if got.Target.Size < 1 || got.Target.Size > maxSize {
		return fmt.Errorf(textNoResult, "the target image's size is out of range")
	}
	if got.Environment != nil && got.Environment.Size != 0 {
		return fmt.Errorf(textNoResult, "it has a size for the environment image")
	}
	if b := got.Base; b != "" && (!pinnedShape.MatchString(b) || !policy.ImageReference(b) || strings.ToLower(b) != b || accesskey.ContainsSecret(b)) {
		return fmt.Errorf(textNoResult, "the base image is not pinned by digest")
	}
	return nil
}

// fetch opens the run r at the gateway of its run key, with the wall and its record at
// record, checks that the run's policy is the server's run configuration's, and fetches the run's repositories into work through the run's host proxy.
// It returns the run, open, for the caller to close, nil when it did not open, and the
// outcome a fetch that failed closes it with: cancelled with timeout and 124 at
// [fetchTimeout], counted from the open; cancelled with interrupted when ctx ended;
// failed otherwise. The error is why the fetch did not succeed: the open, a policy that
// is not the server's, the host proxy, the fetch, or a run configuration that lists no
// repository. No line and no
// error contains the host proxy's password. Every git of the fetch starts from the
// environment of [child.Env] with XDG_STATE_HOME at state, as the build's programs do.
func (s Settings) fetch(ctx context.Context, r Run, work, state, record string) (Opened, session.Outcome, error) {
	out := session.Outcome{State: event.StateFailed, ExitCode: -1}
	if r.Key == nil || len(r.Key.Bytes()) == 0 {
		return nil, out, errors.New(textNoKey)
	}
	runs := filepath.Dir(record)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return nil, out, err
	}
	fetchCtx, cancel := context.WithTimeoutCause(ctx, fetchTimeout, errFetchLimit)
	defer cancel()
	open := s.Open
	if open == nil {
		open = func(ctx context.Context, spec session.OpenSpec) (Opened, error) {
			run, err := session.Open(ctx, spec)
			if err != nil {
				return nil, err
			}
			return run, nil
		}
	}
	run, err := open(fetchCtx, session.OpenSpec{
		Gateway: session.RemoteGateway{RunKey: string(r.Key.Bytes())}, RunsDir: runs, ForagerVersion: s.Version, RunID: r.RunID,
		Machine: s.Machine, Masker: s.Masker, Report: func(line string) { s.log(r.RunID, line) },
		Wall: s.Wall, Image: s.Image, Images: s.Images, CAEnv: s.CAEnv,
	})
	if err != nil {
		return nil, out, fmt.Errorf(textNotOpened, integration.Printable(exports.HideURLError(err).Error()))
	}
	// The run's policy is the server's run configuration's, or nothing is fetched: the
	// machine's policy has none of its rules for the registry.
	if source := policySourceOf(record); source != policyFetched {
		return run, out, fmt.Errorf(textNotServer, policyShown(source))
	}
	fetchErr := s.fetchThrough(fetchCtx, run, r.RunID, work, state)
	switch {
	case fetchErr == nil:
	case errors.Is(context.Cause(fetchCtx), errFetchLimit):
		out = session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}
	case ctx.Err() != nil:
		out = session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	}
	return run, out, fetchErr
}

// closeRun closes run, whose record is at record, with out: the error is a close
// Forager did not take, a run the gateway closed before, as other than succeeded, or
// events the gateway did not accept, on a run it did not close, an [undeliveredError].
// Run.Close sends nothing it waits on longer than its flush, so no error of it is a
// gateway this machine does not reach: Closed.Undelivered counts what did not reach it.
func (s Settings) closeRun(ctx context.Context, run Opened, out session.Outcome, record string) error {
	closed, err := run.Close(context.WithoutCancel(ctx), out)
	switch {
	case err != nil:
		return fmt.Errorf(textNotClosed, integration.Printable(exports.HideError(err).Error()))
	case closed == nil:
	case closed.RunClosed && closed.State != event.StateSucceeded:
		return fmt.Errorf(textClosedBefore, closed.State)
	case closed.Undelivered > 0 && !closed.RunClosed:
		return &undeliveredError{n: closed.Undelivered, record: record}
	}
	return nil
}

// fetchThrough fetches the repositories of the open run into work through the run's
// host proxy, which it closes before it returns, with a line for what it fetched, and
// git started from the environment of [child.Env] with XDG_STATE_HOME at state. A run
// configuration that lists no repository is an error: a prebuild builds a repository.
func (s Settings) fetchThrough(ctx context.Context, run Opened, runID, work, state string) error {
	h, err := run.HostProxy()
	if err != nil {
		return fmt.Errorf(textNoHostProxy, integration.Printable(exports.HideURLError(err).Error()))
	}
	defer h.Close()
	fetch := s.Fetch
	if fetch == nil {
		fetch = preparation.Run
	}
	values := func(name string) string {
		switch name {
		case preparation.EnvSystem:
			return run.System()
		case preparation.EnvRepositories:
			return run.Repositories()
		}
		return ""
	}
	// The fetch's lines and errors start with qory run's prefix, which the prebuild's
	// lines leave out: the run id comes first there.
	log := s.logf()
	lines, flush := maskedLines(child.NewLineWriter("", func(line string) { log(runID + ": " + strings.TrimPrefix(line, fetchPrefix)) }))
	p := proxygit.Proxy{URL: h.ProxyURL, CABundle: h.CABundle}
	fetched, err := fetch(ctx, work, child.Env(s.RunConfig, state, explore.RunsDir(s.State)), values, p, lines)
	flush()
	switch {
	case err != nil:
		return errors.New(proxygit.Hide(strings.TrimPrefix(err.Error(), fetchPrefix), &p))
	case !fetched:
		return fmt.Errorf(textNoRepositories, preparation.EnvRepositories)
	}
	return nil
}

// checkRun checks the settings and the run before anything is made, and returns the
// inputs' environment_ref.
func checkRun(s Settings, r Run) (string, error) {
	if !runIDShape.MatchString(r.RunID) {
		return "", errors.New("the run id is not a UUID in the canonical lower-case form")
	}
	for _, d := range []struct{ name, path string }{{"the state directory", s.State}, {"the runs' configuration directory", s.RunConfig}} {
		if !filepath.IsAbs(d.path) {
			return "", fmt.Errorf("%s %s is not an absolute path", d.name, exports.Value(d.path, true))
		}
	}
	if (r.EnvironmentDestination == "") == (r.EnvironmentImage == "") {
		return "", errors.New("ask Qory Apiary's instance admin: the run has both or neither of environment_destination and environment_image, and a prebuild takes one")
	}
	// Each image the run reports goes into dev.qory.run.built, which takes a lower-case
	// reference pinned by digest, of at most 255 characters before the digest, with no
	// qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in it: a value that the event
	// cannot take stops the run here, before anything is built or pushed.
	places := []struct{ member, value string }{{"image_destination", r.ImageDestination}}
	if r.EnvironmentDestination != "" {
		places = append(places, struct{ member, value string }{"environment_destination", r.EnvironmentDestination})
	}
	if r.EnvironmentImage != "" {
		places = append(places, struct{ member, value string }{"environment_image", r.EnvironmentImage})
	}
	for _, d := range places {
		image, place := d.value, d.member != "environment_image"
		if place {
			image += "@sha256:" + strings.Repeat("0", 64)
		}
		_, noRegistry := registryOf(d.value)
		switch {
		case accesskey.ContainsSecret(d.value):
			return "", fmt.Errorf(textPlaceSecret, d.member)
		case strings.ToLower(d.value) != d.value:
			return "", fmt.Errorf(textPlaceCase, d.member)
		case !place && !policy.ImageReference(image):
			return "", errors.New("ask Qory Apiary's instance admin: the run's environment_image is not an image pinned by digest, NAME@sha256: and 64 hexadecimal digits with no tag, NAME at most 255 characters")
		case place && (noRegistry != nil || !destinationShape.MatchString(d.value) || !policy.ImageReference(image)):
			return "", fmt.Errorf("ask Qory Apiary's instance admin: the run's %s is not a place at a registry, a host and a path of at most 255 characters with no tag and no digest", d.member)
		}
	}
	if !pinnedShape.MatchString(s.AgentImage) {
		return "", errors.New("the agent image is not pinned by digest")
	}
	if s.User == "" {
		return "", errors.New("the wall's user is needed to build")
	}
	if s.Registry != "" {
		for _, d := range places {
			if d.member != "environment_image" && d.value != s.Registry && !strings.HasPrefix(d.value, s.Registry+"/") {
				return "", fmt.Errorf(textNotRegistry, d.member, exports.Value(s.Registry, true))
			}
		}
	}
	return EnvironmentRef(r.Inputs)
}

// policyFetched is the source of dev.qory.run.policy_applied of a run whose policy is
// the server's run configuration.
const policyFetched = "fetched"

// maxRecordLine is the most bytes of a line of the run's record [policySource] reads.
const maxRecordLine = 4 << 20

// policySource is the source of the last dev.qory.run.policy_applied in the run's
// record, session.jsonl in record, which the open wrote: "fetched" for the server's run
// configuration, "config" for a policy the gateway's machine sets, "none" for none, and
// "" when the record has none that reads.
func policySource(record string) string {
	f, err := os.Open(filepath.Join(record, sink.SessionFile))
	if err != nil {
		return ""
	}
	defer f.Close()
	source := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxRecordLine)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			Data struct {
				Source string `json:"source"`
			} `json:"data"`
		}
		if jsonv2.Unmarshal(sc.Bytes(), &e) == nil && e.Type == event.PolicyApplied {
			source = e.Data.Source
		}
	}
	return source
}

// policySourceOf is [policySource]; a test replaces it.
var policySourceOf = policySource

// policyShown is a policy's source as [textNotServer] says it.
func policyShown(source string) string {
	switch source {
	case "config":
		return "the gateway's machine's"
	case "none":
		return "none"
	}
	return "not recorded"
}

// prebuildInputs is a prebuild's inputs: exactly version and environment_ref.
type prebuildInputs struct {
	Version        *int    `json:"version"`
	EnvironmentRef *string `json:"environment_ref"`
}

// EnvironmentRef is the environment_ref of a prebuild's inputs, which must be exactly
// {"version":1,"environment_ref":…}: one JSON object, no member twice and no other
// member, version 1, and environment_ref a string of 1 to 200 characters.
func EnvironmentRef(inputs []byte) (string, error) {
	var in prebuildInputs
	if err := jsonv2.Unmarshal(inputs, &in, jsonv2.RejectUnknownMembers(true)); err != nil {
		return "", errors.New("the inputs are not one JSON object of version and environment_ref alone")
	}
	switch {
	case in.Version == nil || *in.Version != 1:
		return "", errors.New("the inputs' version is not 1")
	case in.EnvironmentRef == nil:
		return "", errors.New("the inputs have no environment_ref")
	case *in.EnvironmentRef == "" || utf8.RuneCountInString(*in.EnvironmentRef) > 200:
		return "", errors.New("the inputs' environment_ref is not 1 to 200 characters")
	}
	return *in.EnvironmentRef, nil
}

// makeDir makes the run directory and its state/ and work/, an empty git working tree,
// and returns the directory, links resolved, or "" when it was not made.
func makeDir(ctx context.Context, s Settings, runID string) (string, error) {
	runs := explore.RunsDir(s.State)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Join(s.State, "pool"), runs} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	made := explore.Dir(s.State, runID)
	if err := os.Mkdir(made, 0o700); err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(made)
	if err != nil {
		return made, err
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		return dir, err
	}
	git := s.Git
	if git == "" {
		git = "git"
	}
	if git, err = child.Program(git, explore.RunsDir(s.State)); err != nil {
		return dir, fmt.Errorf("git init: %w", err)
	}
	cmd := exec.CommandContext(ctx, git, "init", "--quiet", filepath.Join(dir, "work"))
	cmd.Env = append(child.Env(s.RunConfig, state, explore.RunsDir(s.State)), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, flush := maskedLines(s.lines(runID))
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	flush()
	if err != nil {
		return dir, fmt.Errorf("git init: %w", err)
	}
	return dir, nil
}

// maskedLines returns a writer that masks what it gets with the process's mask list,
// [secretmask.Default], as a stream, and then writes it to w, and what flushes both. The
// masking comes before w cuts a long line, so a value across the cut is masked whole.
func maskedLines(w *child.LineWriter) (io.Writer, func()) {
	m := secretmask.Default().Writer(w)
	return m, func() {
		m.Flush()
		w.Flush()
	}
}

// lines is the writer of a command's output: each line after the run id.
func (s Settings) lines(runID string) *child.LineWriter {
	return child.NewLineWriter(runID+": ", s.logf())
}

// log writes one line after the run id, as a command's line is written: a line that
// holds what starts a secret is left out.
func (s Settings) log(runID, line string) {
	w := s.lines(runID)
	_, _ = io.WriteString(w, line)
	w.Flush()
}

// logError writes what, then err as [exports.HideError] prints it, as one line after the
// run id, as [Settings.log] writes one.
func (s Settings) logError(runID, what string, err error) {
	s.log(runID, what+exports.HideError(err).Error())
}

// logf is the Log, or one that drops every line.
func (s Settings) logf() func(string) {
	if s.Log == nil {
		return func(string) {}
	}
	return s.Log
}

// ended says how a command ended, for an error.
func ended(r child.Result) string {
	if r.Signal != 0 {
		return "ended on the signal " + r.Signal.String()
	}
	return "exited " + strconv.Itoa(r.ExitCode)
}

// limited writes to w up to n bytes, and drops the rest.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	k := len(p)
	if k > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	if _, err := l.w.Write(p); err != nil {
		return 0, err
	}
	return k, nil
}
