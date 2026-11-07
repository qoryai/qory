// Package pushcheck runs the push check that a composed harness declares for an artifact
// type, before a version of that type reaches the run API: in a new container from the
// run's image, with no network, as the run's user, with the home read-only at its path,
// the newest versions of the work item's artifacts as the working folder and the pushed
// files beside them. A check that exits 0 lets the version go on; any other end is a
// [Failure] with the fix first, which a walled run's forwarder sends to the program that
// pushed as a [session.RunAPIRefusal], and which the host's push step prints.
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
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
)

// The limits of one check.
const (
	// PIDs is the most processes and threads of a check's container when the wall sets
	// no limit for the run's containers.
	PIDs = 512
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

// The texts of a check that did not pass. The program that pushed gets the fix, ", and
// push again.", then the cause; the push step's line has the fix, then the cause.
const (
	textFailed      = "The check for %s failed:"
	textFailedFix   = "Fix each finding below and push again. "
	textNoNode      = "Install node, or run in an image built FROM qory-agent"
	textNoProgram   = "Install the program %s"
	textNotStarted  = "The check for %s could not start: %s."
	textTimeLimit   = "Make the check for %s finish within 30 s"
	textTimeCause   = "It ran longer and was stopped."
	textNoOutput    = "The check exited with %d and printed nothing."
	textStderrLabel = "The check exited with %d and printed nothing on stdout. Its stderr:"
)

// textUnread is the error of a home's push checks that cannot be read.
const textUnread = "Compose the harness again, and run again. The push checks in %s could not be read: %s."

// The texts of a push that the check cannot read, each with its fix.
const (
	textNoPush        = "Send the push as qory artifact push sends it"
	textNoPushCause   = "The body is not a push: %s."
	textMembers       = "Send each member of the push once, in lower case"
	textFilename      = "Give each file a different name of at most 255 bytes, other than . and .., with no slash, backslash or control character"
	textFilenameBad   = "%s is not one."
	textFilenameTwice = "%s is the name of two files."
	textOneFile       = "Give each file a different name, also when you ignore case and accents"
	textOneFileCause  = "%s and %s are one file on this machine."
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
	// Env, when not nil, is the selection of the engine, NAME=value, as the wall
	// recorded it at the run's open: the command runs with it in place of the variables
	// of [EngineVariables] that this process has.
	Env []string
	// Run runs argv with its standard output and standard error going to the writers,
	// and returns its error, an *exec.ExitError for an exit otherwise than 0. Nil runs it
	// on this machine with this process's environment without the access key's
	// variables and the run key, as the wall runs the engine's command.
	Run func(ctx context.Context, argv []string, stdout, stderr io.Writer) error
}

// EngineVariables are the variables that select the engine that the docker command,
// podman or nerdctl reaches, as Forager's wall has them.
var EngineVariables = []string{
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH",
	"DOCKER_TLS_VERIFY", "CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERD_ADDRESS",
	"CONTAINERD_NAMESPACE",
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
	// Dir is the folder the check's temporary folders are made in; "" for the system's
	// temporary folder.
	Dir string
	// Limits are the wall's limits of the run's containers. A check's container gets
	// their CPUs and Memory, and their PIDs, or [PIDs] when it is 0.
	Limits wall.Limits
	// Label is the label, NAME=value, that the wall removes the run's containers by; ""
	// for none.
	Label string
	// Mask replaces each secret value of the run in a text; nil leaves the text as it is.
	Mask func(string) string
	// Limit, when set, replaces [Limit]. Tests set it.
	Limit time.Duration
}

// Failure is a check that did not pass, or a push that the check cannot read.
type Failure struct {
	// Message is the line for the program that pushed: the fix, then "push again", then
	// the cause.
	Message string
	// Step is the line for the push step: the fix without "push again", then the cause;
	// for a check that failed, the cause alone.
	Step string
	// Findings are the lines of the check; nil for none.
	Findings []string
}

// Refusal is the run API's 422 [runapi.CodePushCheckFailed] for f, nil for a nil f.
func (f *Failure) Refusal() *session.RunAPIRefusal {
	if f == nil {
		return nil
	}
	return &session.RunAPIRefusal{Status: http.StatusUnprocessableEntity, Code: runapi.CodePushCheckFailed, Message: f.Message, Findings: f.Findings}
}

// failure is the failure with fix and cause, which may be "".
func failure(fix, cause string) *Failure {
	f := &Failure{Message: fix + ", and push again.", Step: fix + "."}
	if cause != "" {
		f.Message += " " + cause
		f.Step += " " + cause
	}
	return f
}

// Load reads the push checks of the home at home, from [render.PushChecksFile]; nil when
// the home has no such file, as a harness whose modules declare no check. An error
// starts with its fix.
func Load(home string) ([]compose.PushCheck, error) {
	file := filepath.Join(home, render.PushChecksFile)
	unread := func(cause string) error {
		return fmt.Errorf(textUnread, exports.Value(file, false), strings.TrimSuffix(cause, "."))
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unread(exports.HideError(err).Error())
	}
	var checks render.PushChecks
	if err := json.Unmarshal(data, &checks); err != nil {
		return nil, unread("it is not JSON in the form the compose writes: " + err.Error())
	}
	for _, c := range checks.PushChecks {
		if len(c.Command) == 0 || !filepath.IsAbs(c.Command[0]) {
			return nil, unread(fmt.Sprintf("the check for %s has no absolute path of a program", exports.Value(c.Type, true)))
		}
	}
	return checks.PushChecks, nil
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
// Only a POST of /artifacts of a checker with checks is checked. A body that is not a
// push in the run API's form, or whose members [readPush] cannot read one way only, such
// as one with a member twice, is a refusal: nothing is stored that was not checked. Of a
// push, only one of a type with a check runs the check, and a file's name that is not
// [runapi.LocalFilename] or is there twice is a refusal. The check never changes the
// body.
func (c *Checker) RunAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	if req.Path != artifactsPath || len(c.Checks) == 0 {
		return nil, nil
	}
	artifactType, files, err := readPush(req.Body)
	switch {
	case errors.Is(err, errAmbiguous):
		return failure(textMembers, "").Refusal(), nil
	case err != nil:
		return failure(textNoPush, fmt.Sprintf(textNoPushCause, cut(cleanLine(strings.TrimSuffix(err.Error(), "."))))).Refusal(), nil
	case c.For(artifactType) == nil:
		return nil, nil
	}
	for i, f := range files {
		switch {
		case !runapi.LocalFilename(f.Name):
			return failure(textFilename, fmt.Sprintf(textFilenameBad, exports.Value(f.Name, true))).Refusal(), nil
		case slices.ContainsFunc(files[:i], func(o File) bool { return o.Name == f.Name }):
			return failure(textFilename, fmt.Sprintf(textFilenameTwice, exports.Value(f.Name, true))).Refusal(), nil
		}
	}
	f, err := c.Check(ctx, artifactType, files)
	return f.Refusal(), err
}

// Check runs the check of artifactType on files, the files of one version, each with a
// name that is [runapi.LocalFilename], within [Budget]: nil, nil when artifactType has no
// check or the check exits 0. A check that exits otherwise, cannot start or runs past
// [Limit] is a [Failure], and so are two files that are one file on this machine's file
// system, which ignores case or the form of an accent. A failure of the fetch, of a
// temporary folder or of the engine is an error. Both temporary folders and the
// container are removed in every case.
func (c *Checker) Check(ctx context.Context, artifactType string, files []File) (*Failure, error) {
	check := c.For(artifactType)
	if check == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	pulled, err := c.tempFolder()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pulled)
	pushed, err := c.tempFolder()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pushed)
	args := slices.Clone(check.Command[1:])
	for i, f := range files {
		err := writeNew(filepath.Join(pushed, f.Name), f.Content)
		if errors.Is(err, fs.ErrExist) {
			return oneFile(pushed, files[:i], f.Name), nil
		}
		if err != nil {
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
	return c.failure(artifactType, check.Command[0], end), nil
}

// writeNew writes content to a new file at path, and fails with an error that is
// [fs.ErrExist] when a file is there: on a file system that ignores case or the form of
// an accent, also one whose name differs in those alone.
func writeNew(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if closed := f.Close(); err == nil {
		err = closed
	}
	return err
}

// oneFile is the failure of name, a file that could not be written into dir because a
// file is there, after the files written: with the written file that is the same file
// on this machine, when one is.
func oneFile(dir string, written []File, name string) *Failure {
	at, err := os.Stat(filepath.Join(dir, name))
	if err == nil {
		for _, w := range written {
			if other, err := os.Stat(filepath.Join(dir, w.Name)); err == nil && os.SameFile(at, other) {
				return failure(textOneFile, fmt.Sprintf(textOneFileCause, exports.Value(w.Name, true), exports.Value(name, true)))
			}
		}
	}
	return failure(textOneFile, "")
}

// tempFolder makes a new temporary folder in [Checker.Dir] that only this user can
// open.
func (c *Checker) tempFolder() (string, error) {
	dir, err := os.MkdirTemp(c.Dir, containerPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("making a temporary folder for the check failed: %w", exports.HideError(err))
	}
	return dir, nil
}

// otherUser reports whether the run's user is not this process's user.
func (c *Checker) otherUser() bool {
	uid, _, _ := strings.Cut(c.User, ":")
	return c.User != "" && uid != strconv.Itoa(os.Getuid())
}

// openTo lets the run's user read both folders and everything in them when that user
// is not this one: the engine shows them to the container with their owner and mode,
// and a umask such as 077 leaves a new file or folder to its owner alone. Each folder
// gets mode 755 and each file 644.
func (c *Checker) openTo(dirs ...string) error {
	if !c.otherUser() {
		return nil
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir():
				return os.Chmod(path, 0o755)
			case d.Type().IsRegular():
				return os.Chmod(path, 0o644)
			}
			return nil
		})
		if err != nil {
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
	if c.Label != "" {
		create = append(create, "--label", c.Label)
	}
	create = append(create, c.limits()...)
	create = append(create, mounts...)
	create = append(create, "--entrypoint", program, c.Image)
	create = append(create, args...)
	// The container is removed whole in every case, also once ctx has ended.
	defer c.engine(context.WithoutCancel(ctx), engineWait, nil, nil, "rm", "--force", "--volumes", name)
	var errOut bytes.Buffer
	if err := c.engine(ctx, engineWait, nil, &errOut, create...); err != nil {
		if noSuchImage(errOut.String()) {
			return ended{}, fmt.Errorf("the engine has no image %s for the check's container: %s", exports.Value(c.Image, false), engineError(errOut.Bytes(), err))
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
		if line, ok := notStarted(program, lastLine(stderr.Bytes())); ok {
			return ended{startErr: line}, nil
		}
	}
	return ended{exitCode: s.ExitCode, stdout: stdout.Bytes(), stderr: stderr.Bytes()}, nil
}

// limits are the flags of the check container's limits.
func (c *Checker) limits() []string {
	var out []string
	if c.Limits.CPUs != "" {
		out = append(out, "--cpus", c.Limits.CPUs)
	}
	if c.Limits.Memory != "" {
		out = append(out, "--memory", c.Limits.Memory)
	}
	pids := c.Limits.PIDs
	if pids <= 0 {
		pids = PIDs
	}
	return append(out, "--pids-limit", strconv.Itoa(pids))
}

// notStarted is the cause of a program that exited with 126 or 127 and whose standard
// error ends with line, and true when line is about the start of the program itself:
// the line of the container's init, which starts the program, without its prefix, or a
// line that ends with "not found", "No such file or directory" or "Permission denied"
// for the program or a word of its #! line, such as the line of /usr/bin/env for a
// node it does not find. Any other line, such as one about a program that the check
// runs, is false: an exit of the check.
func notStarted(program, line string) (string, bool) {
	if tiniFatal.MatchString(line) {
		return tiniFatal.ReplaceAllString(line, ""), true
	}
	var rest string
	found := false
	for _, end := range []string{": not found", ": No such file or directory", ": Permission denied"} {
		if rest, found = strings.CutSuffix(line, end); found {
			break
		}
	}
	if !found {
		return "", false
	}
	// The name is the field before the cause, such as 'node' in
	// "/usr/bin/env: 'node': No such file or directory", or in
	// "env: can't execute 'node': No such file or directory".
	name := rest
	if i := strings.LastIndex(rest, ": "); i >= 0 {
		name = rest[i+2:]
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), "can't execute ")
	name = strings.Trim(name, "'\"`‘’")
	if name == "" {
		return "", false
	}
	for _, word := range append([]string{program}, shebang(program)...) {
		if name == word || path.Base(name) == path.Base(word) && !strings.Contains(name, "/") {
			return line, true
		}
	}
	return "", false
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
	if c.Engine.Env != nil {
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")
			return slices.Contains(EngineVariables, name)
		})
		cmd.Env = append(cmd.Env, c.Engine.Env...)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// failure is the failure of a check of artifactType with program that ended as end, nil
// for an exit 0.
func (c *Checker) failure(artifactType, program string, end ended) *Failure {
	switch {
	case end.timedOut:
		return failure(fmt.Sprintf(textTimeLimit, artifactType), textTimeCause)
	case end.startErr != "":
		cause := fmt.Sprintf(textNotStarted, artifactType, strings.TrimSuffix(c.mask(cleanLine(end.startErr)), "."))
		if namesNode(program) {
			return failure(textNoNode, cause)
		}
		return failure(fmt.Sprintf(textNoProgram, exports.Value(program, false)), cause)
	case end.exitCode == 0:
		return nil
	}
	cause := fmt.Sprintf(textFailed, artifactType)
	return &Failure{Message: textFailedFix + cause, Step: cause, Findings: findings(end.exitCode, end.stdout, end.stderr, c.mask)}
}

// mask is [Checker.Mask] applied to text.
func (c *Checker) mask(text string) string {
	if c.Mask == nil {
		return text
	}
	return c.Mask(text)
}

// findings are the findings of a check that exited with code: the lines of stdout; when
// stdout has none, [textStderrLabel] and then the lines of stderr; when neither has one,
// [textNoOutput] alone. Each line is without control characters and masked, cut to
// [maxFinding] characters, at most [maxFindings] of them, the label included. An empty
// line is no finding.
func findings(code int, stdout, stderr []byte, mask func(string) string) []string {
	var label []string
	lines := cleanLines(stdout)
	if len(lines) == 0 {
		lines = cleanLines(stderr)
		if len(lines) == 0 {
			return []string{fmt.Sprintf(textNoOutput, code)}
		}
		label = []string{fmt.Sprintf(textStderrLabel, code)}
	}
	if len(lines) > maxFindings-len(label) {
		lines = lines[:maxFindings-len(label)]
	}
	for i, line := range lines {
		if mask != nil {
			line = mask(line)
		}
		lines[i] = cut(line)
	}
	return append(label, lines...)
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
	return slices.ContainsFunc(shebang(program), func(word string) bool { return path.Base(word) == "node" })
}

// shebang is the words of the #! line of program, nil for a program with none or that
// cannot be read.
func shebang(program string) []string {
	f, err := os.Open(program)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	line, _, _ := bytes.Cut(head[:n], []byte("\n"))
	rest, ok := bytes.CutPrefix(line, []byte("#!"))
	if !ok {
		return nil
	}
	return strings.Fields(string(rest))
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
