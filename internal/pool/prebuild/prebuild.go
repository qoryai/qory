// Package prebuild runs a run of the step prebuild on this machine, for the pool
// program: it builds the environment of the run's target into an image, pushes it to
// the pool's registry and reports it. Each run gets a run directory,
// <state>/pool/runs/<run_id>, mode 0700, as an explore run does, holding:
//
//	work/   an empty git working tree, into which the run's code is fetched
//	state/  every command's XDG_STATE_HOME, mode 0700: qory run keeps the fetch's record there
//
// The steps, each a command with the environment of [child.Env]:
//
//  1. The fetch: qory run --fetch-only with the run key on its descriptor 3, behind
//     the docker wall, in work. Inside the wall, qory run prepare fetches the head of
//     the default branch of the run's repository through the run's proxy, and runs
//     no start command and no agent. Whether the run started is whether qory run made
//     its record, as for an explore run ([explore.RecordDir]).
//  2. The commit: [Commit] reads it from work/.git, with no git run, since a walled run
//     wrote it.
//  3. The build: qory run prebuild of work, tagged <registry>/<name>:<run_id>, where
//     <name> is the first 16 hexadecimal digits of the SHA-256 of the target's
//     system/path, so no text of the target is in the name.
//  4. The push: docker push of the tag, with no login, by docker from the PATH every
//     command gets, run in the pool's folder, outside every run directory, as docker
//     image inspect and docker image rm are. The image is the tag's digest in
//     the registry, from docker image inspect's RepoDigests, and its size the sum of the
//     config's and the layers' sizes of its manifest, read from the registry.
//  5. The report: prebuilt, through the [Run]'s Report.
//
// The local tag and the run directory are removed at the end; the registry keeps the
// image. [Result]'s Ending says what the pool program does next: post the run's
// outcome, release the dispatch, or both.
package prebuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner"
)

// The release reasons of a prebuild that failed or was stopped after its handover.
const (
	// ReasonStopped is a build the pool program stopped, as it stopped.
	ReasonStopped = "the pool program stopped during the build"
	// ReasonNotFetched is a fetch that failed, or a commit that could not be read.
	ReasonNotFetched = "the code could not be fetched"
	// ReasonNoConfiguration is a repository with no development container
	// configuration: nothing was built.
	ReasonNoConfiguration = "the repository has no development container configuration"
	// ReasonNotBuilt is a build that failed.
	ReasonNotBuilt = "the environment could not be built"
	// ReasonNotStored is a push that failed, an image whose digest or size could not be
	// read, and a prebuilt Qory Apiary refused.
	ReasonNotStored = "the image could not be stored in the registry"
)

// RetryAfterHandover is the release's retry of every prebuild this package releases:
// false, since each is released after its handover, where retry true would queue a new
// run.
const RetryAfterHandover = false

// The exit statuses of qory run and qory run prebuild this package reads.
const (
	exitTimeout         = 124
	exitNoConfiguration = 7
)

// fetchTimeout is the fetch's time limit, qory run's --timeout.
const fetchTimeout = "15m"

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
	// pinnedShape is an image pinned by digest, as qory run prebuild takes the agent's.
	pinnedShape = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	// registryShape is pool.registry: a lower-case name with no tag and no digest.
	registryShape = regexp.MustCompile(`^[a-z0-9]+(?:[._:-][a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)+$`)
	// hostShape is a target's system: a host name.
	hostShape = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
)

// Settings are what every prebuild takes from the pool's settings.
type Settings struct {
	// State is the pool program's state directory, an absolute path. The run
	// directories are under <State>/pool/runs, as [explore.Dir] has them.
	State string
	// RunConfig is the XDG_CONFIG_HOME of every command, an absolute directory: the
	// runs' configuration directory, whose forager.yaml sets the wall.
	RunConfig string
	// Registry is pool.registry, the image name the builds are pushed under.
	Registry string
	// AgentImage, User and Helper are wall.image, pinned by digest, wall.user and
	// wall.helper of RunConfig's forager.yaml: qory run prebuild's --agent-image,
	// --user and --helper.
	AgentImage, User, Helper string
	// Qory is the qory binary; empty is the one this process runs. A name with no path
	// separator is found on the PATH every command gets, as [child.Program] has it.
	Qory string
	// Git is the git binary that makes work an empty working tree; empty is git. A name
	// with no path separator is found on the PATH every command gets, as
	// [child.Program] has it.
	Git string
	// Docker is the docker binary; empty is docker. A name with no path separator is
	// found on the PATH every command gets, as [child.Program] has it.
	Docker string
	// HTTP is the client the registry is read with; nil is one with no other setting.
	// It follows no redirect.
	HTTP *http.Client
	// Log takes each line the commands write, after the run id, and each failure.
	Log func(line string)
	// Grace is how long a command has after SIGTERM before it is killed; zero is
	// [child.DefaultGrace].
	Grace time.Duration
}

// Run is one run of the step prebuild, as its handover gave it.
type Run struct {
	// RunID is the run's id.
	RunID string
	// Target is the place whose environment is built, the handover's target.
	Target contracts.Target
	// Key is the run key. Launch clears it on every path.
	Key child.Secret
	// Inputs are the handover's inputs, the bytes as they came: exactly version, 1,
	// and environment_ref.
	Inputs []byte
	// Report sends prebuilt for the run's dispatch. Launch calls it once, after the
	// push.
	Report func(context.Context, provisioner.Image) error
}

// Result is how a prebuild ended.
type Result struct {
	// Dir is the run directory, "" when it was not made. Launch removes it.
	Dir string
	// Record reports whether qory run made the fetch's record: the run started.
	Record bool
	// Image is what was reported, or what was built so far.
	Image provisioner.Image
	// Ending is what the program does next.
	Ending explore.Ending
}

// Name is the name of a target's images under the registry: the first 16 hexadecimal
// digits of the SHA-256 of system/path.
func Name(t contracts.Target) string {
	sum := sha256.Sum256([]byte(t.System + "/" + t.Path))
	return hex.EncodeToString(sum[:])[:16]
}

// Launcher is [Launch] with s, as the pool program's loop starts a prebuild: its
// Ending, the error logged through s.Log.
func Launcher(s Settings) func(ctx context.Context, runID string, target contracts.Target, key child.Secret, inputs []byte, report func(context.Context, provisioner.Image) error) explore.Ending {
	return func(ctx context.Context, runID string, target contracts.Target, key child.Secret, inputs []byte, report func(context.Context, provisioner.Image) error) explore.Ending {
		res, err := Launch(ctx, s, Run{RunID: runID, Target: target, Key: key, Inputs: inputs, Report: report})
		if err != nil {
			s.logError(runID, "", err)
		}
		return res.Ending
	}
}

// Launch runs the prebuild r, until it ends or ctx ends; at ctx's end the command
// running is stopped as [child.Run] stops one. The error says why it did not succeed;
// the Result's Ending is set either way:
//
//	step                          released, retry false     posted
//	before qory run started       explore.ReasonNotStarted  nothing
//	the fetch or the commit       ReasonNotFetched          failed, when it made a record
//	the build: exit 7             ReasonNoConfiguration     failed
//	the build: any other failure  ReasonNotBuilt            failed
//	the push, digest, size        ReasonNotStored           failed
//	prebuilt refused              ReasonNotStored           failed
//	ctx ended                     ReasonStopped             failed, when it made a record
//	prebuilt took                 nothing                   succeeded
//
// A step stopped at its time limit, exit 124, posts failed with explore.ReasonTimeLimit,
// and a run stopped as ctx ended with explore.ReasonPoolStopped. The local tag, once a
// build was started, and the run directory are removed before Launch returns.
func Launch(ctx context.Context, s Settings, r Run) (res Result, err error) {
	if r.Key != nil {
		defer r.Key.Clear()
	}
	res.Ending = explore.EndingOf(false, child.Result{})
	// A prebuild that did not succeed once ctx ended was stopped by the pool program,
	// whatever step it was at.
	defer func() {
		if err != nil && ctx.Err() != nil {
			res.Ending = failed(res.Record, ReasonStopped, explore.ReasonPoolStopped)
		}
	}()
	envRef, err := checkRun(s, r)
	if err != nil {
		return res, err
	}
	res.Image.EnvironmentRef = envRef
	dir, err := makeDir(ctx, s, r.RunID)
	res.Dir = dir
	if dir != "" {
		defer func() {
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
	fetched, err := child.Run(ctx, child.Command{
		Path: s.Qory,
		Args: []string{"run", "--fetch-only", "--run-key-fd", strconv.Itoa(child.KeyFD), "--wall", "docker", "--timeout", fetchTimeout},
		Dir:  work, ConfigHome: s.RunConfig, StateHome: state, Runs: explore.RunsDir(s.State), Key: r.Key, Output: s.lines(r.RunID), Grace: s.Grace,
	})
	if err != nil {
		return res, fmt.Errorf("qory run could not be started: %w", err)
	}
	_, statErr := os.Stat(record)
	res.Record = statErr == nil
	switch {
	case ctx.Err() != nil:
		return res, errors.New("the pool program stopped during the fetch")
	case fetched != (child.Result{}):
		res.Ending = failed(res.Record, ReasonNotFetched, limitReason(fetched))
		return res, fmt.Errorf("qory run --fetch-only %s", ended(fetched))
	case !res.Record:
		res.Ending = failed(false, ReasonNotFetched, "")
		return res, errors.New("qory run --fetch-only made no record of the run")
	}
	// The commit, from this run's fetch alone.
	if res.Image.Commit, err = Commit(work); err != nil {
		res.Ending = failed(true, ReasonNotFetched, "")
		return res, err
	}
	// The build.
	name := s.Registry + "/" + Name(r.Target)
	tag := name + ":" + r.RunID
	defer s.removeTag(ctx, r.RunID, state, tag)
	built, err := child.Run(ctx, child.Command{
		Path: s.Qory,
		Args: []string{"run", "prebuild", "--repository", work, "--agent-image", s.AgentImage, "--user", s.User, "--tag", tag, "--helper", s.Helper},
		Dir:  dir, ConfigHome: s.RunConfig, StateHome: state, Runs: explore.RunsDir(s.State), Output: s.lines(r.RunID), Grace: s.Grace,
	})
	switch {
	case ctx.Err() != nil:
		return res, errors.New("the pool program stopped during the build")
	case err != nil:
		res.Ending = failed(true, ReasonNotBuilt, "")
		return res, fmt.Errorf("qory run prebuild could not be started: %w", err)
	case built.Signal == 0 && built.ExitCode == exitNoConfiguration:
		res.Ending = failed(true, ReasonNoConfiguration, "")
		return res, errors.New("qory run prebuild found no development container configuration")
	case built != (child.Result{}):
		res.Ending = failed(true, ReasonNotBuilt, limitReason(built))
		return res, fmt.Errorf("qory run prebuild %s", ended(built))
	}
	// The push, then what the registry holds, by docker from the cleaned PATH, in the
	// pool's folder.
	docker, err := s.docker()
	pushed := child.Result{}
	if err == nil {
		pushed, err = child.Run(ctx, child.Command{
			Path: docker, Args: []string{"push", tag},
			Dir: s.poolDir(), ConfigHome: s.RunConfig, StateHome: state, Runs: explore.RunsDir(s.State), Output: s.lines(r.RunID), Grace: s.Grace,
		})
	}
	switch {
	case ctx.Err() != nil:
		return res, errors.New("the pool program stopped during the push")
	case err != nil:
		res.Ending = failed(true, ReasonNotStored, "")
		return res, fmt.Errorf("docker push could not be started: %w", err)
	case pushed != (child.Result{}):
		res.Ending = failed(true, ReasonNotStored, "")
		return res, fmt.Errorf("docker push %s", ended(pushed))
	}
	if res.Image.Image, res.Image.Size, err = s.stored(ctx, r.RunID, state, tag, name); err != nil {
		res.Ending = failed(true, ReasonNotStored, "")
		return res, err
	}
	// The report.
	if r.Report == nil {
		res.Ending = failed(true, ReasonNotStored, "")
		return res, errors.New("the prebuild has no way to report its image")
	}
	if err := r.Report(ctx, res.Image); err != nil {
		res.Ending = failed(true, ReasonNotStored, "")
		return res, fmt.Errorf("prebuilt was not taken: %w", err)
	}
	res.Ending = explore.Ending{Post: true, State: explore.StateSucceeded}
	return res, nil
}

// failed is the ending of a prebuild that failed after its handover: released with
// retry false for reason, and posted failed, with the outcome's reason code, when the
// run made a record.
func failed(record bool, reason, code string) explore.Ending {
	e := explore.Ending{Release: true, Retry: RetryAfterHandover, ReleaseReason: reason}
	if record {
		e.Post, e.State, e.Reason = true, explore.StateFailed, code
	}
	return e
}

// limitReason is the outcome's reason code of a command that ended as r: the time limit
// for exit 124, else none.
func limitReason(r child.Result) string {
	if r.Signal == 0 && r.ExitCode == exitTimeout {
		return explore.ReasonTimeLimit
	}
	return ""
}

// checkRun checks the settings and the run before anything is made, and returns the
// inputs' environment_ref.
func checkRun(s Settings, r Run) (string, error) {
	if !runIDShape.MatchString(r.RunID) {
		return "", errors.New("the run id is not a UUID in the canonical lower-case form")
	}
	if !hostShape.MatchString(r.Target.System) || r.Target.Path == "" || utf8.RuneCountInString(r.Target.Path) > 200 || !utf8.ValidString(r.Target.Path) {
		return "", errors.New("the run's target is not a system and a path")
	}
	for _, d := range []struct{ name, path string }{{"the state directory", s.State}, {"the runs' configuration directory", s.RunConfig}} {
		if !filepath.IsAbs(d.path) {
			return "", fmt.Errorf("%s %s is not an absolute path", d.name, exports.Value(d.path, true))
		}
	}
	if !registryShape.MatchString(s.Registry) {
		return "", errors.New("the registry is not an image name with no tag and no digest")
	}
	if _, _, err := registryOf(s.Registry); err != nil {
		return "", err
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

// stored is the image the push stored, name@sha256:<digest> from docker image
// inspect's RepoDigests, and its size, read from the registry.
func (s Settings) stored(ctx context.Context, runID, state, tag, name string) (string, int64, error) {
	docker, err := s.docker()
	if err != nil {
		return "", 0, fmt.Errorf("docker image inspect: %w", err)
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
		return "", 0, fmt.Errorf("docker image inspect: %w", err)
	}
	var got inspected
	if err := jsonv2.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		return "", 0, errors.New("docker image inspect printed no image")
	}
	digest := ""
	for _, d := range got.RepoDigests {
		if rest, ok := strings.CutPrefix(d, name+"@"); ok {
			if digest != "" && digest != rest {
				return "", 0, errors.New("docker image inspect names the image in the registry twice")
			}
			digest = rest
		}
	}
	if digest == "" {
		return "", 0, errors.New("docker image inspect names no digest of the image in the registry")
	}
	host, path, err := registryOf(name)
	if err != nil {
		return "", 0, err
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{}
	}
	c := *client
	c.CheckRedirect = noRedirect
	size, err := imageSize(ctx, &c, host, path, digest, platform{OS: got.OS, Architecture: got.Architecture, Variant: got.Variant})
	if err != nil {
		return "", 0, err
	}
	return name + "@" + digest, size, nil
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
// every command gets, which no entry in the pool's runs folder is on.
func (s Settings) docker() (string, error) {
	name := s.Docker
	if name == "" {
		name = "docker"
	}
	return child.Program(name, explore.RunsDir(s.State))
}

// poolDir is the pool's folder, <State>/pool: where the docker commands run, outside
// every run directory.
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
