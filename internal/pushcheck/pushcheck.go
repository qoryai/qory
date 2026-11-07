// Package pushcheck runs the push check that a composed harness declares for an artifact
// type, before a version of that type reaches the run API: in a new container from the
// run's image, with no network, as the run's user, with the home read-only at its path,
// the newest versions of the work item's artifacts as the working folder and the pushed
// files beside them. A check that exits 0 lets the version go on; any other end is a
// [session.RunAPIRefusal] with the fix first, which a walled run's forwarder sends to the
// program that pushed, and which the host's push step prints.
package pushcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
)

// The limits of one check.
const (
	// Limit is how long the check's program may run, from the start of its container.
	Limit = 30 * time.Second
	// Budget is how long one check has for the fetch and the engine's commands. With
	// the removal of its container, at most engineWait more, a check returns within 80
	// seconds, within the 90-second limit of a walled run's forwarder.
	Budget = 75 * time.Second
	// fetchWait is how long the fetch of the artifacts may take, and engineWait one
	// engine command other than the start.
	fetchWait  = 30 * time.Second
	engineWait = 5 * time.Second
	// maxOutput is the most read of the program's standard output, and of its standard
	// error.
	maxOutput = 64 << 10
	// maxFindings is the most findings of one check, and maxFinding the most characters
	// of one.
	maxFindings = 200
	maxFinding  = 1000
)

// The paths inside the check's container: the working folder, with the artifacts, and
// the folder of the pushed files.
const (
	WorkDir  = "/qory-push-check/artifacts"
	FilesDir = "/qory-push-check/files"
)

// The texts of a refusal. Each starts with its fix.
const (
	textFailed    = "Fix each finding below and push again. The check for %s failed:"
	textNoNode    = "Install node, or run in an image built FROM qory-agent, and push again. The check for %s could not start: %s."
	textNoProgram = "Install the program %s, and push again. The check for %s could not start: %s."
	textTimeLimit = "Make the check for %s finish within 30 s, and push again. It ran longer and was stopped."
)

const (
	// containerPrefix starts the name of a check's container and of its temporary
	// folders.
	containerPrefix = "qory-push-check-"
	// artifactsPath is the path of a push, after the run's id.
	artifactsPath = "/artifacts"
	// stateFormat is the format of docker inspect that prints a container's end.
	stateFormat = "{{json .State}}"
	// The exits of a program that the container's init could not start: not
	// executable, or not found, its interpreter included.
	exitCannotInvoke = 126
	exitNotFound     = 127
)

// File is one file of a version, with its name in the version.
type File struct {
	Name    string
	Content []byte
}

// Fetch writes into dir, an empty folder, the work item's artifact index as index.json,
// byte for byte as the run API returned it, and the newest version of each artifact of
// the types, under <type>/<name>/v<N>/, as qory artifact pull lays them out. A type with
// no version has no folder.
type Fetch func(ctx context.Context, dir string, types []string) error

// Engine is the container engine the check's container runs in: the one the run's wall
// uses, through the same command.
type Engine struct {
	// Command is the program, docker or the value of wall.command; empty means docker.
	Command string
	// Run runs argv with its standard output and standard error going to the writers,
	// and returns its error, an *exec.ExitError for an exit otherwise than 0. Nil runs it
	// on this machine with this process's environment without the access key's
	// variables and the run key, as the wall runs the engine's command.
	Run func(ctx context.Context, argv []string, stdout, stderr io.Writer) error
}

// Checker runs the push checks of one run.
type Checker struct {
	// Home is the composed home, mounted read-only at this path in the check's container,
	// where the wall mounts it for the agent.
	Home string
	// Checks are the home's push checks, as [Load] reads them.
	Checks []compose.PushCheck
	// Image is the reference of the run's image, and User the uid:gid the run's
	// containers run as.
	Image, User string
	Engine      Engine
	Fetch       Fetch
	// Mask replaces each secret value of the run in a text; nil leaves the text as it is.
	Mask func(string) string
	// Limit, when set, replaces [Limit]. Tests set it.
	Limit time.Duration
}

// Load reads the push checks of the home at home, from [render.PushChecksFile]; nil when
// the home has no such file, as a harness whose modules declare no check.
func Load(home string) ([]compose.PushCheck, error) {
	data, err := os.ReadFile(filepath.Join(home, render.PushChecksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Compose the harness again. Reading its push checks failed: %w", exports.HideError(err))
	}
	var file render.PushChecks
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("Compose the harness again. Its %s does not read: %w", render.PushChecksFile, err)
	}
	for _, c := range file.PushChecks {
		if len(c.Command) == 0 || !filepath.IsAbs(c.Command[0]) {
			return nil, fmt.Errorf("Compose the harness again. Its %s has a push check for %s with no program's path", render.PushChecksFile, exports.Value(c.Type, true))
		}
	}
	return file.PushChecks, nil
}

// For is the check of artifactType, nil for none.
func (c *Checker) For(artifactType string) *compose.PushCheck {
	for i := range c.Checks {
		if c.Checks[i].Type == artifactType {
			return &c.Checks[i]
		}
	}
	return nil
}

// RunAPICheck is the check of a POST to the run API, for [session.OpenSpec.RunAPICheck].
// Only a POST of /artifacts is checked, and of it only a push of a type with a check.
// A body that is not a push in the run API's form passes as it is: the run API returns
// its error. A body whose members [readPush] cannot read one way only, such as one with a
// member twice, is an error, so the forwarder returns 503 and nothing is stored that
// was not checked. The check never changes the body.
func (c *Checker) RunAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	if req.Path != artifactsPath {
		return nil, nil
	}
	artifactType, files, err := readPush(req.Body)
	if errors.Is(err, errAmbiguous) {
		return nil, err
	}
	if err != nil || c.For(artifactType) == nil {
		return nil, nil
	}
	for i, f := range files {
		if !localFilename(f.Name) || slices.ContainsFunc(files[:i], func(o File) bool { return o.Name == f.Name }) {
			return nil, fmt.Errorf("the push of %s has a file name the check cannot write: %s", artifactType, exports.Value(f.Name, true))
		}
	}
	return c.Check(ctx, artifactType, files)
}

// Check runs the check of artifactType on files, the files of one version, within
// [Budget]: nil, nil when artifactType has no check or the check exits 0. A check that
// exits otherwise, cannot start or runs past [Limit] is a refusal with status 422 and
// [runapi.CodePushCheckFailed]. A failure of the fetch, of a temporary folder or of the
// engine is an error. Both temporary folders and the container are removed in every
// case.
func (c *Checker) Check(ctx context.Context, artifactType string, files []File) (*session.RunAPIRefusal, error) {
	check := c.For(artifactType)
	if check == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	pulled, err := tempFolder()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pulled)
	pushed, err := tempFolder()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pushed)
	args := slices.Clone(check.Command[1:])
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(pushed, f.Name), f.Content, 0o644); err != nil {
			return nil, fmt.Errorf("writing the pushed file %s for the check failed: %w", exports.Value(f.Name, true), exports.HideError(err))
		}
		args = append(args, path.Join(FilesDir, f.Name))
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, fetchWait)
	err = c.Fetch(fetchCtx, pulled, append([]string{artifactType}, check.Reads...))
	cancelFetch()
	if err != nil {
		return nil, fmt.Errorf("the fetch of the artifacts for the check of %s failed: %w", artifactType, err)
	}
	if err := c.openTo(pulled, pushed); err != nil {
		return nil, err
	}
	end, err := c.run(ctx, check.Command[0], args, pulled, pushed)
	if err != nil {
		return nil, err
	}
	return c.refusal(artifactType, check.Command[0], end), nil
}

// tempFolder makes a new temporary folder that only this user can open.
func tempFolder() (string, error) {
	dir, err := os.MkdirTemp("", containerPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("making a temporary folder for the check failed: %w", exports.HideError(err))
	}
	return dir, nil
}

// openTo lets the run's user read both folders when that user is not this one: the
// engine shows them to the container with their owner and mode.
func (c *Checker) openTo(dirs ...string) error {
	uid, _, _ := strings.Cut(c.User, ":")
	if c.User == "" || uid == strconv.Itoa(os.Getuid()) {
		return nil
	}
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o755); err != nil {
			return fmt.Errorf("opening the check's folder to the run's user failed: %w", exports.HideError(err))
		}
	}
	return nil
}

// ended is how the check's container ended.
type ended struct {
	// timedOut is a program that ran past the limit, and startErr the engine's error
	// for a program that did not start, "" for none.
	timedOut bool
	startErr string
	exitCode int
	stdout   []byte
	stderr   []byte
}

// state is what the engine reports of a container's end.
type state struct {
	Status   string
	ExitCode int
	Error    string
}

// run runs program with args in a new container and returns how it ended. The engine's
// failures, such as an engine that is not reachable, are errors; an image the engine
// does not hold, and a program that does not start, are an end with startErr set.
func (c *Checker) run(ctx context.Context, program string, args []string, pulled, pushed string) (ended, error) {
	name, err := containerName()
	if err != nil {
		return ended{}, err
	}
	mounts := make([]string, 0, 3)
	for _, m := range [][2]string{{c.Home, c.Home}, {pulled, WorkDir}, {pushed, FilesDir}} {
		mount, err := bindMount(m[0], m[1])
		if err != nil {
			return ended{}, err
		}
		mounts = append(mounts, "--mount", mount)
	}
	create := []string{"create", "--name", name, "--network", "none", "--user", c.User,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--pull", "never",
		"--no-healthcheck", "--workdir", WorkDir}
	create = append(create, mounts...)
	create = append(create, "--entrypoint", program, c.Image)
	create = append(create, args...)
	// The container is removed whole in every case, also once ctx has ended.
	defer c.engine(context.WithoutCancel(ctx), engineWait, nil, nil, "rm", "--force", "--volumes", name)
	var errOut bytes.Buffer
	if err := c.engine(ctx, engineWait, nil, &errOut, create...); err != nil {
		if noSuchImage(errOut.String()) {
			return ended{startErr: lastLine(errOut.Bytes())}, nil
		}
		return ended{}, fmt.Errorf("the engine did not create the check's container: %s", engineError(errOut.Bytes(), err))
	}
	stdout, stderr := &capped{limit: maxOutput}, &capped{limit: maxOutput}
	limit := c.Limit
	if limit == 0 {
		limit = Limit
	}
	startCtx, cancel := context.WithTimeout(ctx, limit)
	startErr := c.engine(startCtx, 0, stdout, stderr, "start", "--attach", name)
	timedOut := startCtx.Err() != nil
	cancel()
	switch {
	case timedOut && ctx.Err() != nil:
		return ended{}, fmt.Errorf("the check's time ran out: %w", ctx.Err())
	case timedOut:
		return ended{timedOut: true}, nil
	}
	var out, inspectErr bytes.Buffer
	if err := c.engine(ctx, engineWait, &out, &inspectErr, "inspect", "--format", stateFormat, name); err != nil {
		return ended{}, fmt.Errorf("the engine did not report how the check's container ended: %s (the start: %v)", engineError(inspectErr.Bytes(), err), startErr)
	}
	var s state
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &s); err != nil {
		return ended{}, fmt.Errorf("the engine's report of the check's container does not read: %w", err)
	}
	switch {
	case s.Error != "":
		// The container's init, not the program, failed to start: the init starts the
		// program, and writes a program that does not start as exit 126 or 127.
		return ended{}, fmt.Errorf("the engine did not start the check's container: %s", cleanLine(s.Error))
	case s.Status != "exited":
		return ended{}, fmt.Errorf("the check's container is %s after its start, not exited (the start: %v)", exports.Value(s.Status, true), startErr)
	case s.ExitCode == exitCannotInvoke || s.ExitCode == exitNotFound:
		// --init's tini starts the program, and writes its failure as well.
		line := tiniFatal.ReplaceAllString(lastLine(stderr.Bytes()), "")
		if line == "" {
			line = fmt.Sprintf("it exited with %d", s.ExitCode)
		}
		return ended{startErr: line}, nil
	}
	return ended{exitCode: s.ExitCode, stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
}

// engine runs one command of the engine with args, within wait when it is not 0.
func (c *Checker) engine(ctx context.Context, wait time.Duration, stdout, stderr io.Writer, args ...string) error {
	if wait > 0 {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	command := c.Engine.Command
	if command == "" {
		command = "docker"
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	argv := append([]string{command}, args...)
	if c.Engine.Run != nil {
		return c.Engine.Run(ctx, argv, stdout, stderr)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The access key's variables and the run key are Forager's alone: the engine's
	// command never receives them, as the wall's commands do not.
	cmd.Env = accesskey.WithoutVariables(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// refusal is the refusal of a check of artifactType with program that ended as end, nil
// for an exit 0.
func (c *Checker) refusal(artifactType, program string, end ended) *session.RunAPIRefusal {
	r := &session.RunAPIRefusal{Status: http.StatusUnprocessableEntity, Code: runapi.CodePushCheckFailed}
	switch {
	case end.timedOut:
		r.Message = fmt.Sprintf(textTimeLimit, artifactType)
	case end.startErr != "":
		cause := strings.TrimSuffix(c.mask(cleanLine(end.startErr)), ".")
		if namesNode(program) {
			r.Message = fmt.Sprintf(textNoNode, artifactType, cause)
		} else {
			r.Message = fmt.Sprintf(textNoProgram, exports.Value(program, false), artifactType, cause)
		}
	case end.exitCode == 0:
		return nil
	default:
		r.Message = fmt.Sprintf(textFailed, artifactType)
		r.Findings = findings(end.stdout, end.stderr, c.mask)
	}
	return r
}

// mask is [Checker.Mask] applied to text.
func (c *Checker) mask(text string) string {
	if c.Mask == nil {
		return text
	}
	return c.Mask(text)
}

// findings are the lines of stdout, or of stderr when stdout has none, each without
// control characters and masked, cut to [maxFinding] characters, at most [maxFindings]
// of them. An empty line is no finding.
func findings(stdout, stderr []byte, mask func(string) string) []string {
	lines := cleanLines(stdout)
	if len(lines) == 0 {
		lines = cleanLines(stderr)
	}
	if len(lines) > maxFindings {
		lines = lines[:maxFindings]
	}
	for i, line := range lines {
		if mask != nil {
			line = mask(line)
		}
		lines[i] = cut(line)
	}
	return lines
}

// cleanLines are the lines of b without control characters, and without the lines that
// are then empty or blank.
func cleanLines(b []byte) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = cleanLine(line); strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// cleanLine is line as valid UTF-8 without control characters.
func cleanLine(line string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(line, string(utf8.RuneError)))
}

// cut is line cut to [maxFinding] characters.
func cut(line string) string {
	if utf8.RuneCountInString(line) <= maxFinding {
		return line
	}
	return string([]rune(line)[:maxFinding])
}

// namesNode reports whether the first line of program is a #! line with node as its
// interpreter or one of its words, such as #!/usr/bin/env node; false for a program that
// cannot be read.
func namesNode(program string) bool {
	f, err := os.Open(program)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	line, _, _ := bytes.Cut(head[:n], []byte("\n"))
	rest, ok := bytes.CutPrefix(line, []byte("#!"))
	if !ok {
		return false
	}
	for _, word := range strings.Fields(string(rest)) {
		if path.Base(word) == "node" {
			return true
		}
	}
	return false
}

// tiniFatal is the start of the line that the container's init writes for a program it
// could not start.
var tiniFatal = regexp.MustCompile(`^\[FATAL tini \(\d+\)\] `)

// noSuchImage reports whether the engine's error is for an image it does not hold.
func noSuchImage(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "no such image") || strings.Contains(text, "unable to find image")
}

// engineError is the last line the engine's command printed, or err with none.
func engineError(errOut []byte, err error) string {
	if line := lastLine(errOut); line != "" {
		return line
	}
	return err.Error()
}

// lastLine is the last line of b that is not blank, without control characters.
func lastLine(b []byte) string {
	lines := cleanLines(b)
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// containerName is a new name for a check's container.
func containerName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return containerPrefix + hex.EncodeToString(b), nil
}

// bindMount is one --mount value of a read-only bind. The value is comma-separated
// fields, so a path that contains a comma or a quote is an error, as the wall has it.
func bindMount(src, dst string) (string, error) {
	for _, p := range []string{src, dst} {
		if strings.ContainsAny(p, ",\"\n") {
			return "", fmt.Errorf("the path %s contains a comma or a quote, and the check's container cannot mount it", exports.Value(p, true))
		}
	}
	return "type=bind,src=" + src + ",dst=" + dst + ",readonly", nil
}

// localFilename reports whether name is safe as one file name in a folder.
func localFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) &&
		strings.IndexFunc(name, unicode.IsControl) < 0
}

// capped keeps what is written to it up to limit bytes, and takes the rest without
// keeping it, so the program never waits on a full pipe.
type capped struct {
	limit int
	buf   bytes.Buffer
}

// Write keeps p up to the limit.
func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Bytes is what was kept.
func (c *capped) Bytes() []byte { return c.buf.Bytes() }
