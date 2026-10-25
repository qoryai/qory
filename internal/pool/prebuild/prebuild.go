// Package prebuild runs a run of the step prebuild, for qory run once it redeemed a run
// code whose documents have step prebuild: it builds the environment of the run's target
// into images, pushes them to the places the documents set, and writes the run's
// dev.qory.run.built. Each run gets a run directory, <state>/pool/runs/<run_id>, mode
// 0700, holding:
//
//	work/   an empty git working tree, into which the run's code is fetched
//	state/  XDG_STATE_HOME of every command, mode 0700, and the folder of the run's record
//
// The steps, in qory run's process:
//
//  1. The fetch: the run opens at the gateway of its run key, with no wall, and its
//     record in the folder that [explore.RecordDir] returns for work. Through the run's
//     host proxy, [preparation.Run] fetches the run's repositories into work, as qory
//     run does once a run is open. The run stays open until Launch returns. Whether the
//     run started is whether the open made its record. The fetch has [fetchTimeout],
//     counted from the open. Every git of the fetch starts from the environment of
//     [child.Env], so git is found on its PATH.
//  2. The commit: [Commit] reads it from work/.git, with no git run.
//  3. The build: the build of internal/prebuild, of work, in its two steps. The target
//     image is tagged <image_destination>:<run_id>, and the environment image the build
//     keeps apart <environment_destination>:<run_id>. A run with environment_image
//     builds the target image from it, and builds no environment image. Its programs get
//     the environment of [child.Env], and are found on its PATH.
//  4. The push: docker push of the environment image, when the build kept one apart,
//     then of the target image, by docker from the PATH every command gets, run in
//     <state>/pool, outside every run directory, as docker image inspect, docker buildx
//     imagetools inspect and docker image rm are. qory sets no login and reads none:
//     docker reaches the registry with the settings in $HOME/.docker. Each image is its
//     tag's digest in the registry, from docker image inspect's RepoDigests. The target
//     image's size is the sum of the config's and the layers' sizes of its manifest,
//     which docker buildx imagetools inspect --raw reads from the registry.
//  5. The report: [session.Run.Built] writes dev.qory.run.built with the target image,
//     its size, the commit and the inputs' environment_ref; the environment image, by
//     its digest, when the build kept one apart and it was pushed, or the build started
//     from environment_image; and the base image, by its digest, when the build
//     resolved the image its target step starts from. Then the run closes succeeded.
//
// The local tags, the target image's and the environment image's, are removed at the
// end, and so is the run directory, unless events of the run did not reach its
// gateway; the registry keeps the images.
package prebuild

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	devimage "github.com/qoryai/qory/internal/prebuild"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
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

// cleanupTimeout bounds the removal of the local tag, which runs after ctx may have
// ended.
const cleanupTimeout = 30 * time.Second

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
	// AgentImage, User and Helper are wall.image, pinned by digest, wall.user and
	// wall.helper of RunConfig's forager.yaml: the build's agent image, the wall user
	// the image runs as, and qory's Linux build that runs the seed step.
	AgentImage, User, Helper string
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
	// Build builds the image, as [devimage.Build]; nil is devimage.Build. A test
	// replaces it.
	Build func(ctx context.Context, o devimage.Options) (devimage.Result, error)
	// Git is the git binary that makes work an empty working tree; empty is git. A name
	// with no path separator is found on the PATH every command gets, as
	// [child.Program] has it.
	Git string
	// Docker is the docker binary; empty is docker. A name with no path separator is
	// found on the PATH every command gets, as [child.Program] has it.
	Docker string
	// Log takes each line the commands write, after the run id, and each failure.
	Log func(line string)
	// Grace is how long a command has after SIGTERM before it is killed; zero is
	// [child.DefaultGrace].
	Grace time.Duration
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
	// Built is what the build made: the local references of the environment image and
	// of the target image, and the base image by digest.
	Built devimage.Result
}

// Launch runs the prebuild r, until it ends or ctx ends; at ctx's end the step running
// is stopped. The error is why it did not succeed. Once the run is open, it closes once,
// as Launch returns:
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
// removed before Launch returns. The local tags, once a build was started, are removed
// in every case.
func Launch(ctx context.Context, s Settings, r Run) (res Result, err error) {
	if r.Key != nil {
		defer r.Key.Clear()
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
	// The build.
	tag := r.ImageDestination + ":" + r.RunID
	envTag := ""
	if r.EnvironmentDestination != "" {
		envTag = r.EnvironmentDestination + ":" + r.RunID
	}
	defer s.removeTag(ctx, r.RunID, state, tag)
	build := s.Build
	if build == nil {
		build = devimage.Build
	}
	lines, log := s.lines(r.RunID), s.lines(r.RunID)
	res.Built, err = build(ctx, devimage.Options{
		Repository: work, AgentImage: s.AgentImage, User: s.User, Tag: tag, Helper: s.Helper, SeedArgs: devimage.SeedArgs,
		EnvironmentImage: r.EnvironmentImage, EnvironmentTag: envTag,
		Env: child.Env(s.RunConfig, state, explore.RunsDir(s.State)), Out: lines, Log: log,
	})
	lines.Flush()
	log.Flush()
	switch {
	case ctx.Err() != nil:
		return res, fmt.Errorf(textStoppedBuild, context.Cause(ctx))
	case errors.Is(err, devimage.ErrImageMissing):
		out = session.Outcome{State: event.StateFailed, Reason: refusal.ImageMissing, ExitCode: -1}
		return res, fmt.Errorf(devimage.Prefix+"%w", err)
	case errors.Is(err, devimage.ErrNoConfiguration), errors.Is(err, devimage.ErrLimit):
		return res, err
	case err != nil:
		return res, fmt.Errorf(devimage.Prefix+"%w", err)
	}
	// The pushes, then what the registry holds, by docker from the cleaned PATH, in
	// <State>/pool: the environment image the build kept apart, then the target image.
	res.Image.EnvironmentImage = r.EnvironmentImage
	if env := res.Built.Environment; env != "" {
		defer s.removeTag(ctx, r.RunID, state, env)
		if r.EnvironmentImage == "" {
			if err = s.push(ctx, r.RunID, state, env); err != nil {
				return res, err
			}
			digest, _, err := s.digest(ctx, r.RunID, state, env, r.EnvironmentDestination)
			if err != nil {
				return res, err
			}
			res.Image.EnvironmentImage = r.EnvironmentDestination + "@" + digest
		}
	}
	if err = s.push(ctx, r.RunID, state, tag); err != nil {
		return res, err
	}
	if res.Image.Image, res.Image.Size, err = s.stored(ctx, r.RunID, state, tag, r.ImageDestination); err != nil {
		return res, err
	}
	// The report.
	res.Image.Base = res.Built.Base
	if err = run.Built(res.Image); err != nil {
		return res, fmt.Errorf(textNotReported, err)
	}
	out = session.Outcome{State: event.StateSucceeded, ExitCode: -1}
	return res, nil
}

// fetch opens the run r at the gateway of its run key, with no wall and its record at
// record, and fetches the run's repositories into work through the run's host proxy.
// It returns the run, open, for the caller to close, nil when it did not open, and the
// outcome a fetch that failed closes it with: cancelled with timeout and 124 at
// [fetchTimeout], counted from the open; cancelled with interrupted when ctx ended;
// failed otherwise. The error is why the fetch did not succeed: the open, the host
// proxy, the fetch, or a run configuration that lists no repository. No line and no
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
		Machine: s.Machine, Report: func(line string) { s.log(r.RunID, line) },
	})
	if err != nil {
		return nil, out, fmt.Errorf(textNotOpened, integration.Printable(exports.HideURLError(err).Error()))
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
	lines := child.NewLineWriter("", func(line string) { log(runID + ": " + strings.TrimPrefix(line, fetchPrefix)) })
	p := proxygit.Proxy{URL: h.ProxyURL, CABundle: h.CABundle}
	fetched, err := fetch(ctx, work, child.Env(s.RunConfig, state, explore.RunsDir(s.State)), values, p, lines)
	lines.Flush()
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
	if s.User == "" || s.Helper == "" {
		return "", errors.New("the wall's user and helper are both needed to build")
	}
	return EnvironmentRef(r.Inputs)
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
	out := s.lines(runID)
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	out.Flush()
	if err != nil {
		return dir, fmt.Errorf("git init: %w", err)
	}
	return dir, nil
}

// inspected is what docker image inspect prints of the image, as [inspectFormat] has it.
type inspected struct {
	RepoDigests  []string `json:"repo_digests"`
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	Variant      string   `json:"variant"`
}

// digest is the digest of the image the push of tag stored under name, sha256:<hex>,
// from docker image inspect's RepoDigests, and the image's platform.
func (s Settings) digest(ctx context.Context, runID, state, tag, name string) (string, platform, error) {
	docker, err := s.docker()
	if err != nil {
		return "", platform{}, fmt.Errorf("docker image inspect: %w", err)
	}
	cmd := exec.CommandContext(ctx, docker, "image", "inspect", "--format", inspectFormat, tag)
	cmd.Dir = s.poolDir()
	cmd.Env = child.Env(s.RunConfig, state, explore.RunsDir(s.State))
	var stdout bytes.Buffer
	cmd.Stdout = &limited{w: &stdout, n: maxInspect}
	errOut := s.lines(runID)
	cmd.Stderr = errOut
	err = cmd.Run()
	errOut.Flush()
	if err != nil {
		return "", platform{}, fmt.Errorf("docker image inspect: %w", err)
	}
	var got inspected
	if err := jsonv2.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		return "", platform{}, errors.New("docker image inspect printed no image")
	}
	digest := ""
	for _, d := range got.RepoDigests {
		if rest, ok := strings.CutPrefix(d, name+"@"); ok {
			if digest != "" && digest != rest {
				return "", platform{}, errors.New("docker image inspect names the image in the registry twice")
			}
			digest = rest
		}
	}
	if digest == "" {
		return "", platform{}, errors.New("docker image inspect names no digest of the image in the registry")
	}
	return digest, platform{OS: got.OS, Architecture: got.Architecture, Variant: got.Variant}, nil
}

// stored is the image the push of tag stored, name@sha256:<digest> as [Settings.digest]
// reads it, and its size, read from its manifest in the registry.
func (s Settings) stored(ctx context.Context, runID, state, tag, name string) (string, int64, error) {
	digest, p, err := s.digest(ctx, runID, state, tag, name)
	if err != nil {
		return "", 0, err
	}
	size, err := s.imageSize(ctx, runID, state, name, digest, p)
	if err != nil {
		return "", 0, err
	}
	return name + "@" + digest, size, nil
}

// push runs docker push of tag, by docker from the cleaned PATH, in <State>/pool, with
// its lines after the run id. When ctx ends during it, the error is [textStoppedPush].
func (s Settings) push(ctx context.Context, runID, state, tag string) error {
	docker, err := s.docker()
	pushed := child.Result{}
	if err == nil {
		pushed, err = child.Run(ctx, child.Command{
			Path: docker, Args: []string{"push", tag},
			Dir: s.poolDir(), ConfigHome: s.RunConfig, StateHome: state, Runs: explore.RunsDir(s.State), Output: s.lines(runID), Grace: s.Grace,
		})
	}
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf(textStoppedPush, context.Cause(ctx))
	case err != nil:
		return fmt.Errorf("docker push could not be started: %w", err)
	case pushed != (child.Result{}):
		return fmt.Errorf("docker push %s", ended(pushed))
	}
	return nil
}

// removeTag removes the local tag once the build was started, whatever came of it,
// within [cleanupTimeout] even after ctx ended. The registry keeps the image.
func (s Settings) removeTag(ctx context.Context, runID, state, tag string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	docker, err := s.docker()
	if err != nil {
		s.logError(runID, "the local tag of the image could not be removed: ", err)
		return
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, docker, "image", "rm", tag)
	cmd.Dir = s.poolDir()
	cmd.Env = child.Env(s.RunConfig, state, explore.RunsDir(s.State))
	cmd.Stdout, cmd.Stderr = io.Discard, &limited{w: &out, n: maxInspect}
	if err := cmd.Run(); err != nil && !strings.Contains(out.String(), "No such image") {
		s.logError(runID, "the local tag of the image could not be removed: ", err)
	}
}

// docker is the docker binary: Docker, or docker, as [child.Program] has it on the PATH
// every command gets, which no entry in <State>/pool/runs is on.
func (s Settings) docker() (string, error) {
	name := s.Docker
	if name == "" {
		name = "docker"
	}
	return child.Program(name, explore.RunsDir(s.State))
}

// poolDir is <State>/pool, the folder of the run directories' folder: where the docker
// commands run, outside every run directory.
func (s Settings) poolDir() string {
	return filepath.Join(s.State, "pool")
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
