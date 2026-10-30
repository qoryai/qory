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
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/pool/child"
	devimage "github.com/qoryai/qory/internal/prebuild"
	"github.com/qoryai/qory/internal/secretmask"
)

// StepArgs are the arguments of the qory helper that select the prebuild's step behind
// the wall, the hidden verb qory run prebuild-step, which [RunStep] is; [Step.Args] follow
// them.
var StepArgs = []string{"run", "prebuild-step"}

// ResultFile is the file in the out folder that [RunStep] writes and [Launch] reads:
// what the step built and pushed, or why it did not, as [stepResult] has it.
const ResultFile = "result.json"

// maxResult is the most bytes [Launch] reads of [ResultFile]: 1 MiB.
const maxResult = 1 << 20

// maxResultError is the most characters of a step's error [Launch] takes from
// [ResultFile].
const maxResultError = 2000

// The values of stepResult.Stopped, the step a stop ended: the build, or the push and the
// reads of what the registry stores.
const (
	stoppedBuild = "build"
	stoppedPush  = "push"
)

// Step is one prebuild's step behind the wall, as the arguments of qory run prebuild-step
// give it: the build of the working folder it runs in, and the pushes.
type Step struct {
	// Out is the folder the step writes [ResultFile] in, an absolute path.
	Out string
	// RunID, ImageDestination, EnvironmentDestination and EnvironmentImage are the
	// run's, as [Run] has them: the target image is pushed as
	// <ImageDestination>:<RunID>, the environment image the build keeps apart as
	// <EnvironmentDestination>:<RunID>.
	RunID, ImageDestination, EnvironmentDestination, EnvironmentImage string
	// AgentImage and User are the build's, as [Settings] has them.
	AgentImage, User string
}

// stepFlags are the flags of qory run prebuild-step, in the order [Step.Args] writes them,
// each with its member of the step.
func (s *Step) stepFlags() []struct {
	name  string
	value *string
} {
	return []struct {
		name  string
		value *string
	}{
		{"--out", &s.Out}, {"--run-id", &s.RunID}, {"--image-destination", &s.ImageDestination},
		{"--environment-destination", &s.EnvironmentDestination}, {"--environment-image", &s.EnvironmentImage},
		{"--agent-image", &s.AgentImage}, {"--user", &s.User},
	}
}

// Args are the arguments of qory run prebuild-step after [StepArgs]: each flag that is set,
// then its value.
func (s Step) Args() []string {
	var out []string
	for _, f := range s.stepFlags() {
		if *f.value != "" {
			out = append(out, f.name, *f.value)
		}
	}
	return out
}

// ParseStep reads the arguments of qory run prebuild-step after [StepArgs], as [Step.Args]
// writes them, and checks them as [Launch] checks the run: a flag it does not know, one
// given twice or without a value, and a step for which [checkStep] returns an error are
// errors.
func ParseStep(args []string) (Step, error) {
	var s Step
	flags := s.stepFlags()
	seen := map[string]bool{}
	for i := 0; i < len(args); i += 2 {
		k := slices.IndexFunc(flags, func(f struct {
			name  string
			value *string
		}) bool {
			return f.name == args[i]
		})
		switch {
		case k < 0:
			return Step{}, fmt.Errorf("give qory run prebuild-step only %s: it does not take %s", stepFlagNames(flags), integration.Printable(exports.Value(args[i], true)))
		case seen[args[i]]:
			return Step{}, fmt.Errorf("give %s once: qory run prebuild-step got it twice", args[i])
		case i+1 >= len(args):
			return Step{}, fmt.Errorf("give a value after %s: qory run prebuild-step got none", args[i])
		}
		seen[args[i]] = true
		*flags[k].value = args[i+1]
	}
	return s, checkStep(s)
}

// stepFlagNames are the flags' names, as a sentence lists them.
func stepFlagNames(flags []struct {
	name  string
	value *string
}) string {
	names := make([]string, len(flags))
	for i, f := range flags {
		names[i] = f.name
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// checkStep returns an error for a step for which [checkRun] returns one as a run, and for
// an out folder that is not an absolute path.
func checkStep(s Step) error {
	if !filepath.IsAbs(s.Out) {
		return errors.New("give qory run prebuild-step --out with an absolute path: it writes its result there")
	}
	_, err := checkRun(Settings{State: "/", RunConfig: "/", AgentImage: s.AgentImage, User: s.User},
		Run{RunID: s.RunID, Inputs: []byte(`{"version":1,"environment_ref":"step"}`), ImageDestination: s.ImageDestination,
			EnvironmentDestination: s.EnvironmentDestination, EnvironmentImage: s.EnvironmentImage})
	return err
}

// StepSettings are what the step takes from the wall it runs behind.
type StepSettings struct {
	// Dir is the working folder, the run's fetched repository, which the step builds.
	Dir string
	// Env is the step's environment, the session's: the build's programs get
	// [devimage.AllowedEnv] of it, and docker's push the same with a docker
	// configuration that has no login in place of DOCKER_CONFIG.
	Env []string
	// Helper is the qory helper's path behind the wall, which the build's seed step runs
	// from; empty is [wall.HelperPath].
	Helper string
	// CABundle is the run's bundle of authorities behind the wall, which the seed step's
	// commands get; empty is [wall.BundlePath] when it is a regular file.
	CABundle string
	// Docker is the docker binary; empty is docker, found on the PATH of Env.
	Docker string
	// Build builds the image, as [devimage.Build]; nil is devimage.Build. A test replaces
	// it.
	Build func(ctx context.Context, o devimage.Options) (devimage.Result, error)
	// Out takes the step's lines and Log what its programs print; nil drops them.
	Out, Log io.Writer
	// Grace is how long docker push has after SIGTERM before it is killed; zero is
	// [child.DefaultGrace].
	Grace time.Duration
}

// stepImage is an image the step pushed, as [ResultFile] has it: its name, the
// destination it was pushed under, its digest in the registry and, for the target image,
// its size there.
type stepImage struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size,omitempty"`
}

// stepResult is [ResultFile]: what the step pushed, or why it did not.
type stepResult struct {
	// Target and Environment are the target image and the environment image the step
	// pushed; Environment is nil when the build kept none apart. Base is the base image
	// the build reports, "" for none.
	Target      *stepImage `json:"target,omitempty"`
	Environment *stepImage `json:"environment,omitempty"`
	Base        string     `json:"base,omitempty"`
	// Error is why the step did not push, "" when it did; ImageMissing is true
	// when the environment image is not in its registry; Stopped is the step a stop ended,
	// [stoppedBuild] or [stoppedPush], "" for none.
	Error        string `json:"error,omitempty"`
	ImageMissing bool   `json:"image_missing,omitempty"`
	Stopped      string `json:"stopped,omitempty"`
}

// RunStep is the prebuild's step behind the wall, in the working folder s.Dir: the
// build of internal/prebuild, on the wall's nested Docker, as [devimage.Options.Walled]
// has it; then the push of the environment image the build kept apart, unless the run
// has environment_image, then of the target image, by docker from the PATH of s.Env,
// with a docker configuration that has no login, through the run's proxy. Each image is
// its tag's digest in the registry, from docker image inspect's RepoDigests, and the
// target image's size is the sum of the config's and the layers' sizes of its manifest,
// which docker buildx imagetools inspect --raw reads from the registry. It writes
// [ResultFile] in st.Out, a new file, with what it pushed, or with its error, and returns
// the error. The nested Docker's images and cache go with the wall.
func RunStep(ctx context.Context, st Step, s StepSettings) error {
	if err := checkStep(st); err != nil {
		return err
	}
	var res stepResult
	stopped, err := s.run(ctx, st, &res)
	if err != nil {
		res = stepResult{Error: err.Error(), ImageMissing: errors.Is(err, devimage.ErrImageMissing)}
		if ctx.Err() != nil {
			res.Stopped = stopped
		}
	}
	if werr := writeResult(st.Out, res); werr != nil && err == nil {
		err = werr
	}
	return err
}

// run is [RunStep] without the result's file. It returns, with an error, the step it
// was in: [stoppedBuild] or [stoppedPush].
func (s StepSettings) run(ctx context.Context, st Step, res *stepResult) (string, error) {
	build := s.Build
	if build == nil {
		build = devimage.Build
	}
	if s.Out == nil {
		s.Out = io.Discard
	}
	if s.Log == nil {
		s.Log = io.Discard
	}
	helper := s.Helper
	if helper == "" {
		helper = wall.HelperPath
	}
	bundle := s.CABundle
	if bundle == "" {
		if info, err := os.Stat(wall.BundlePath); err == nil && info.Mode().IsRegular() {
			bundle = wall.BundlePath
		}
	}
	tag := st.ImageDestination + ":" + st.RunID
	envTag := ""
	if st.EnvironmentDestination != "" {
		envTag = st.EnvironmentDestination + ":" + st.RunID
	}
	built, err := build(ctx, devimage.Options{
		Repository: s.Dir, AgentImage: st.AgentImage, User: st.User, Tag: tag, Helper: helper, SeedArgs: devimage.SeedArgs,
		EnvironmentImage: st.EnvironmentImage, EnvironmentTag: envTag,
		Env: s.Env, Out: s.Out, Log: s.Log, Walled: true, CABundle: bundle,
	})
	switch {
	case ctx.Err() != nil:
		return stoppedBuild, fmt.Errorf(textStoppedBuild, context.Cause(ctx))
	case errors.Is(err, devimage.ErrNoConfiguration), errors.Is(err, devimage.ErrLimit):
		return stoppedBuild, err
	case err != nil:
		return stoppedBuild, fmt.Errorf(devimage.Prefix+"%w", err)
	}
	res.Base = built.Base
	p, done, err := s.pusher()
	if err != nil {
		return stoppedPush, err
	}
	defer done()
	if env := built.Environment; env != "" && st.EnvironmentImage == "" {
		if err := p.push(ctx, env); err != nil {
			return stoppedPush, err
		}
		digest, _, err := p.digest(ctx, env, st.EnvironmentDestination)
		if err != nil {
			return stoppedPush, err
		}
		res.Environment = &stepImage{Name: st.EnvironmentDestination, Digest: digest}
	}
	if err := p.push(ctx, tag); err != nil {
		return stoppedPush, err
	}
	digest, size, err := p.stored(ctx, tag, st.ImageDestination)
	if err != nil {
		return stoppedPush, err
	}
	res.Target = &stepImage{Name: st.ImageDestination, Digest: digest, Size: size}
	return "", nil
}

// pusher runs the docker commands of the push: docker, in dir, with env, its lines to
// lines.
type pusher struct {
	docker string
	env    []string
	dir    string
	out    io.Writer
	grace  time.Duration
}

// lines is the writer of a docker command's lines.
func (p pusher) lines() *child.LineWriter {
	out := p.out
	if out == nil {
		out = io.Discard
	}
	return child.NewLineWriter("", func(line string) { fmt.Fprintln(out, line) })
}

// pusher is the push's docker commands, with a docker configuration that has no login,
// an empty config.json in a temporary folder, in place of DOCKER_CONFIG, and done, which
// removes that folder. The commands run in the folder, and get the variables of
// [devimage.AllowedEnv] of the step's environment.
func (s StepSettings) pusher() (pusher, func(), error) {
	dir, err := os.MkdirTemp("", "qory-push-")
	if err != nil {
		return pusher{}, nil, err
	}
	done := func() { _ = os.RemoveAll(dir) }
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		done()
		return pusher{}, nil, err
	}
	var env []string
	for _, kv := range s.Env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(devimage.AllowedEnv, name) && name != "DOCKER_CONFIG" && name != "DOCKER_CERT_PATH" {
			env = append(env, kv)
		}
	}
	docker := s.Docker
	if docker == "" {
		docker = "docker"
	}
	if !strings.ContainsRune(docker, '/') {
		docker = lookIn(docker, env)
	}
	return pusher{docker: docker, env: append(env, "DOCKER_CONFIG="+dir), dir: dir, out: s.Out, grace: s.Grace}, done, nil
}

// lookIn is name's path on the PATH env sets, name itself when no entry has it.
func lookIn(name string, env []string) string {
	for _, kv := range env {
		if path, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, dir := range filepath.SplitList(path) {
				p := filepath.Join(dir, name)
				if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
					return p
				}
			}
		}
	}
	return name
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
func (p pusher) digest(ctx context.Context, tag, name string) (string, platform, error) {
	docker, err := exec.LookPath(p.docker)
	if err != nil {
		return "", platform{}, fmt.Errorf("check that the prebuild image has docker on its PATH: docker image inspect could not be started: %w", err)
	}
	cmd := exec.CommandContext(ctx, docker, "image", "inspect", "--format", inspectFormat, tag)
	cmd.Dir, cmd.Env = p.dir, p.env
	var stdout bytes.Buffer
	cmd.Stdout = &limited{w: &stdout, n: maxInspect}
	errOut, flush := maskedLines(p.lines())
	cmd.Stderr = errOut
	err = cmd.Run()
	flush()
	if err != nil {
		return "", platform{}, fmt.Errorf("check docker's lines behind the wall: docker image inspect of the pushed image failed: %w", err)
	}
	var got inspected
	if err := jsonv2.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
		return "", platform{}, errors.New("check the prebuild image's docker: docker image inspect printed no image that qory reads")
	}
	digest := ""
	for _, d := range got.RepoDigests {
		if rest, ok := strings.CutPrefix(d, name+"@"); ok {
			if digest != "" && digest != rest {
				return "", platform{}, errors.New("check the prebuild image's docker: docker image inspect lists two digests of the image in the registry")
			}
			digest = rest
		}
	}
	if digest == "" {
		return "", platform{}, errors.New("check docker push's lines behind the wall: docker image inspect lists no digest of the pushed image in the registry")
	}
	return digest, platform{OS: got.OS, Architecture: got.Architecture, Variant: got.Variant}, nil
}

// stored is the digest of the image the push of tag stored, as [pusher.digest] reads
// it, and its size, read from its manifest in the registry.
func (p pusher) stored(ctx context.Context, tag, name string) (string, int64, error) {
	digest, plat, err := p.digest(ctx, tag, name)
	if err != nil {
		return "", 0, err
	}
	size, err := p.imageSize(ctx, name, digest, plat)
	if err != nil {
		return "", 0, err
	}
	return digest, size, nil
}

// push runs docker push of tag, with its lines on the step's Out. When ctx ends during
// it, the error is [textStoppedPush].
func (p pusher) push(ctx context.Context, tag string) error {
	pushed, err := child.Run(ctx, child.Command{
		Path: p.docker, Args: []string{"push", tag}, Dir: p.dir, ConfigHome: p.dir, Env: p.env,
		Output: p.lines(), Mask: secretmask.Default(), Grace: p.grace,
	})
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf(textStoppedPush, context.Cause(ctx))
	case err != nil:
		return fmt.Errorf("check that the prebuild image has docker on its PATH: docker push could not be started: %w", err)
	case pushed != (child.Result{}):
		return fmt.Errorf("check docker push's lines behind the wall, and that the run's egress reaches the registry: docker push %s", ended(pushed))
	}
	return nil
}

// writeResult writes res as [ResultFile] in out, a new file of mode 0600 that is no
// link.
func writeResult(out string, res stepResult) error {
	data, err := jsonv2.Marshal(res)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(out, ResultFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readResult reads [ResultFile] in out as [Launch] takes it from behind the wall, which
// may have written anything there: a path whose last part is a link, a file that is not
// regular or is larger than [maxResult], and one that is not exactly a [stepResult] are
// refused. It opens the file without following a link, and checks what it opened.
func readResult(out string) (stepResult, error) {
	var res stepResult
	path := filepath.Join(out, ResultFile)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return res, fmt.Errorf(textNoResult, integration.Printable(exports.HideError(err).Error()))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxResult {
		return res, fmt.Errorf(textNoResult, "it is not a regular file of at most 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxResult+1))
	if err != nil || len(data) > maxResult {
		return res, fmt.Errorf(textNoResult, "it is not a regular file of at most 1 MiB")
	}
	if err := jsonv2.Unmarshal(data, &res, jsonv2.RejectUnknownMembers(true)); err != nil {
		return stepResult{}, fmt.Errorf(textNoResult, "it does not read as the step's result")
	}
	return res, nil
}

// shownError is a step's error as [Launch] returns it: printable, with what looks like
// a secret or a URL's password hidden, and at most [maxResultError] characters.
func shownError(text string) string {
	text = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, exports.HideValues(exports.HideURLs(text)))
	if r := []rune(text); len(r) > maxResultError {
		text = string(r[:maxResultError]) + "…"
	}
	return text
}
