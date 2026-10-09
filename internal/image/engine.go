package image

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/wall"
)

// ProbeUser is the user the probe's container runs as: a uid and a gid no image names,
// since the wall runs the agent as the machine's user, whom the image does not know.
const ProbeUser = "65532:65532"

// ProbeArgs are the arguments that make qory's Linux build run the probe.
var ProbeArgs = []string{"image", "probe"}

// ProbeWait is how long the probe's container may run, the start of every program in it
// included, and how long the image's files may take to read.
const ProbeWait = 5 * time.Minute

// engineWait is how long one question to the engine may take.
const engineWait = 30 * time.Second

// refShape is what an image reference may look like on a command line: never a word
// that starts with a dash, which the docker command would read as a flag. The wall
// refuses the same shapes.
var refShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

// CheckRef refuses what cannot be an image reference.
func CheckRef(ref string) error {
	if !refShape.MatchString(ref) {
		return fmt.Errorf("%q is not an image reference", ref)
	}
	return nil
}

// Pinned reports whether a reference names its image by digest.
func Pinned(ref string) bool { return strings.Contains(ref, "@sha256:") }

// Engine is the container engine a check reaches through its command, as the wall does.
type Engine struct {
	// Command is the program: docker, or what wall.command names. Empty means docker.
	Command string
	// Run runs the command with its arguments, writes its standard output to stdout as
	// it comes, and returns its standard error. Nil runs it on this machine.
	Run func(ctx context.Context, argv []string, stdout io.Writer) (stderr []byte, err error)
}

// Target is one image to check, and what the check needs to probe it.
type Target struct {
	// Ref is the image's reference, as a run names it.
	Ref string
	// Helper is the path on this machine of qory's static Linux build for the engine's
	// architecture, the probe.
	Helper string
	// Claude is the version claude --version must report; empty takes any.
	Claude string
}

// stream runs one docker command within its limit, its standard output to stdout.
func (e Engine) stream(ctx context.Context, wait time.Duration, stdout io.Writer, args ...string) ([]byte, error) {
	command := e.Command
	if command == "" {
		command = "docker"
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	argv := append([]string{command}, args...)
	if e.Run != nil {
		return e.Run(ctx, argv, stdout)
	}
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdout, c.Stderr = stdout, &stderr
	err := c.Run()
	return stderr.Bytes(), err
}

// run runs one docker command within its limit and returns both its streams.
func (e Engine) run(ctx context.Context, wait time.Duration, args ...string) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	stderr, err := e.stream(ctx, wait, &stdout, args...)
	return stdout.Bytes(), stderr, err
}

// Platform is what the engine runs, os/arch in Go's words: linux/arm64. An engine that
// does not answer is an error that carries what the command printed.
func (e Engine) Platform(ctx context.Context) (string, error) {
	out, errOut, err := e.run(ctx, engineWait, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	platform := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(platform, "/") {
		return "", fmt.Errorf("the docker command reaches no engine: %s", lastLine(errOut, err))
	}
	return platform, nil
}

// inspected is what a check reads of docker image inspect.
type inspected struct {
	Os           string
	Architecture string
	RepoDigests  []string
	Config       struct {
		Env []string
	}
}

// Check checks one image on an engine that runs platform, from outside, then its files,
// then with the probe, and returns every line in the order they print. An image the
// engine does not hold is one failed line. An image of another platform than the
// engine's fails its first line and is checked all the same: the probe runs natively,
// since it is built for the engine, and the image's programs as the engine runs them.
func (e Engine) Check(ctx context.Context, platform string, t Target) []Check {
	out, errOut, err := e.run(ctx, engineWait, "image", "inspect", "--format", "{{json .}}", t.Ref)
	if err != nil {
		if bytes.Contains(bytes.ToLower(errOut), []byte("no such image")) {
			return []Check{{"image", Fail, "the engine does not hold " + t.Ref + "; docker pull " + t.Ref}}
		}
		return []Check{{"image", Fail, "docker image inspect " + t.Ref + ": " + lastLine(errOut, err)}}
	}
	var img inspected
	if err := json.Unmarshal(bytes.TrimSpace(out), &img); err != nil {
		return []Check{{"image", Fail, "docker image inspect " + t.Ref + " printed what does not read: " + err.Error()}}
	}
	own := img.Os + "/" + img.Architecture
	checks := []Check{{"image", Pass, "the engine holds it, for " + own + ", the platform the engine runs"}}
	if own != platform {
		checks[0] = Check{"image", Fail, "the image is for " + own + ", and the engine runs " + platform + "; pull or build it for " + platform}
	}
	checks = append(checks, homeSet(img.Config.Env), reference(t.Ref, img.RepoDigests), e.files(ctx, t.Ref))
	return append(checks, e.probe(ctx, t)...)
}

// homeSet checks that the image's environment sets HOME: without it the engine gives /,
// which a user with no name in the image cannot write.
func homeSet(env []string) Check {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			return Check{"home", Pass, "the image sets HOME=" + v}
		}
	}
	return Check{"home", Fail, "the image sets no HOME, so the engine gives /; set one any user may write, such as ENV HOME=/home/agent with mode 1777"}
}

// reference reports whether the reference is pinned by digest. When it is not, it names
// the digest the engine holds the image under, which is a registry's only when the image
// came from one or went to one: an engine that keeps images in containerd gives a local
// build a digest too.
func reference(ref string, digests []string) Check {
	if Pinned(ref) {
		return Check{"reference", Info, "pinned by digest"}
	}
	for _, d := range digests {
		if _, digest, ok := strings.Cut(d, "@"); ok {
			return Check{"reference", Info, "not pinned by digest; the engine holds the image as " + digest + ", which a registry serves only if the image was pulled from it or pushed to it"}
		}
	}
	return Check{"reference", Info, "not pinned by digest, and the engine holds no digest of it: a local build"}
}

// files reads the image's whole file system, as docker export writes it from a container
// created and never started, for setuid and setgid files and files with capabilities of
// their own. Reading it from outside sees every file, whatever a user inside may read.
func (e Engine) files(ctx context.Context, ref string) Check {
	out, errOut, err := e.run(ctx, engineWait, "create", "--pull", "never", "--network", "none", "--entrypoint", wall.HelperPath, ref)
	id := strings.TrimSpace(string(out))
	if err != nil || id == "" {
		return Check{"files", Fail, "the image's files were not read: docker create: " + lastLine(errOut, err)}
	}
	defer e.run(context.WithoutCancel(ctx), engineWait, "rm", "--force", "--volumes", id)
	r, w := io.Pipe()
	scanned := make(chan struct {
		s   Special
		err error
	}, 1)
	go func() {
		s, err := Scan(r)
		r.CloseWithError(err)
		scanned <- struct {
			s   Special
			err error
		}{s, err}
	}()
	errOut, err = e.stream(ctx, ProbeWait, w, "export", id)
	w.CloseWithError(err)
	got := <-scanned
	switch {
	case err != nil:
		return Check{"files", Fail, "the image's files were not read to the end: docker export: " + lastLine(errOut, err)}
	case got.err != nil:
		return Check{"files", Fail, "the image's files were not read to the end: " + got.err.Error()}
	}
	return got.s.check()
}

// probe runs the probe in a container of the image, started as the wall starts an
// agent, and returns its lines, or the one line that says why it did not run. A report
// that lacks one of [ProbeChecks], or has a line of no known result, fails.
func (e Engine) probe(ctx context.Context, t Target) []Check {
	if strings.ContainsAny(t.Helper, ",\"\n") {
		return []Check{{"probe", Fail, "the probe's path " + t.Helper + " holds a comma or a quote and cannot be mounted"}}
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none",
		"--user", ProbeUser, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init",
		"--mount", "type=bind,src=" + t.Helper + ",dst=" + wall.HelperPath + ",readonly",
		"--entrypoint", wall.HelperPath, t.Ref}
	args = append(args, ProbeArgs...)
	if t.Claude != "" {
		args = append(args, "--claude-version", t.Claude)
	}
	out, errOut, err := e.run(ctx, ProbeWait, args...)
	var rep Report
	if jerr := json.Unmarshal(bytes.TrimSpace(out), &rep); jerr != nil || rep.Version != ReportVersion {
		why := lastLine(errOut, err)
		if strings.Contains(string(errOut), "unknown command") {
			why = t.Helper + " has no image probe; set wall.helper to the Linux build of this qory"
		} else if err == nil && jerr != nil {
			why = "it printed what does not read: " + jerr.Error()
		} else if err == nil {
			why = fmt.Sprintf("it printed a report of version %d, and this qory reads %d; set wall.helper to the Linux build of this qory", rep.Version, ReportVersion)
		}
		return []Check{{"probe", Fail, "the probe did not run in the image: " + why}}
	}
	checks := rep.Checks
	var lacks []string
	for _, name := range ProbeChecks {
		if !slices.ContainsFunc(checks, func(c Check) bool { return c.Name == name }) {
			lacks = append(lacks, name)
		}
	}
	for i, c := range checks {
		if c.Result != Pass && c.Result != Fail && c.Result != Info {
			checks[i] = Check{c.Name, Fail, fmt.Sprintf("the probe's %s line has the result %q, which this qory does not read: %s", c.Name, c.Result, c.Detail)}
		}
	}
	if len(lacks) > 0 {
		checks = append(checks, Check{"probe", Fail, "the probe's report has no line for " + strings.Join(lacks, ", ") + ", so the image was not checked for it; set wall.helper to the Linux build of this qory"})
	}
	return checks
}

// lastLine is the last line a command printed on its standard error, or its error when
// it printed none.
func lastLine(stderr []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	if err != nil {
		return err.Error()
	}
	return "no answer"
}

// Arch is the architecture of a platform, os/arch: arm64 of linux/arm64.
func Arch(platform string) string {
	_, arch, _ := strings.Cut(platform, "/")
	return arch
}

// HelperArch reads the architecture of qory's Linux build at path, in Go's words: amd64
// or arm64, and the ELF machine's name in lower case for another. It refuses a file that
// is not a Linux executable and one that is dynamically linked, which the wall refuses
// too, since an image may hold no loader.
func HelperArch(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("not a Linux executable: %w", err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "", errors.New("dynamically linked; build it with CGO_ENABLED=0")
		}
	}
	switch f.Machine {
	case elf.EM_X86_64:
		return "amd64", nil
	case elf.EM_AARCH64:
		return "arm64", nil
	}
	return strings.ToLower(strings.TrimPrefix(f.Machine.String(), "EM_")), nil
}
