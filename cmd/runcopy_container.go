package cmd

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/runcopy"
)

// gitContainer is the git session of a walled write run as a container that qory starts on the
// run's engine, with no network and no join of the run's gateway: see [runGitContainer].
type gitContainer struct {
	// Docker is the engine's command, the wall's, and Env its environment: this process's,
	// without the access key's variables and the run key.
	Docker string
	Env    []string
	// Name is the container's name, qory-copy-<run id>, and Label the run's label, which the
	// wall's removal of what a run left finds.
	Name, Label string
	// User is the wall's user, Runtime the wall's container runtime, "" for the engine's
	// default, and Image the run's image.
	User, Runtime, Image string
	// Mounts are the binds of the git session, [runcopy.Dir.SessionMounts], each at its path.
	Mounts []wall.Mount
	// Session is the program the container runs.
	Session runcopy.GitSession
	// Output gets what the container and the docker command print.
	Output io.Writer
}

// gitContainerPIDs is the most processes the git container may have. The container gets no
// memory or CPU limit, and none of the agent's limits, which are set for the agent and can stop
// git on a large copy.
const gitContainerPIDs = 4096

// containerRemoveWait is how long the removal of a git container that a cut or its limit
// stopped may take.
const containerRemoveWait = time.Minute

// exitNotStarted is the exit status of docker run when docker itself failed, so the container
// did not run.
const exitNotStarted = 125

// runGitContainer runs the git container c, [dockerGitContainer]; a test replaces it.
var runGitContainer = dockerGitContainer

// dockerGitContainer runs c with docker run and returns the container's exit status. It returns
// an error when the container did not start, with docker's last line as its text, and when ctx
// ended first: then it removes the container, under ctx without its end, so no process of it
// writes the copy after the return.
func dockerGitContainer(ctx context.Context, c gitContainer) (int, error) {
	args, err := c.args()
	if err != nil {
		return -1, err
	}
	last := &lastLine{}
	cmd := exec.CommandContext(ctx, c.Docker, args...)
	cmd.Env = c.Env
	cmd.Stdout = c.Output
	cmd.Stderr = io.MultiWriter(c.Output, last)
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return -1, errors.Join(ctx.Err(), c.remove(ctx))
	}
	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit) && exit.ExitCode() != exitNotStarted:
		return exit.ExitCode(), nil
	case runErr == nil:
		return 0, nil
	case last.text() != "":
		return -1, errors.New(last.text())
	}
	return -1, runErr
}

// args are the arguments of docker run for c.
func (c gitContainer) args() ([]string, error) {
	args := []string{"run", "--rm", "--pull", "never", "--network", "none",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--no-healthcheck",
		"--user", c.User, "--label", c.Label, "--name", c.Name}
	if c.Runtime != "" {
		args = append(args, "--runtime", c.Runtime)
	}
	args = append(args, "--pids-limit", strconv.Itoa(gitContainerPIDs))
	for _, m := range c.Mounts {
		mount, err := bindAt(m)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", mount)
	}
	for _, e := range c.Session.Env {
		args = append(args, "--env", e)
	}
	args = append(args, "--workdir", c.Session.Dir, "--entrypoint", c.Session.Command, c.Image)
	return append(args, c.Session.Args...), nil
}

// remove removes the container c, under ctx without its end and within
// [containerRemoveWait]. A container that is gone already is no error.
func (c gitContainer) remove(ctx context.Context) error {
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerRemoveWait)
	defer cancel()
	cmd := exec.CommandContext(rmCtx, c.Docker, "rm", "--force", "--volumes", c.Name)
	cmd.Env = c.Env
	out, err := cmd.CombinedOutput()
	if err != nil && !bytes.Contains(bytes.ToLower(out), []byte("no such container")) {
		return fmt.Errorf("docker rm --force %s: %w: %s", c.Name, err, bytes.TrimSpace(out))
	}
	return nil
}

// bindAt is the --mount value of m at its path, read-write unless m is read-only. The fields
// are quoted as docker reads them, as CSV, so a path with a comma or a quote binds as it is.
func bindAt(m wall.Mount) (string, error) {
	fields := []string{"type=bind", "src=" + m.Path, "dst=" + m.Path}
	if m.ReadOnly {
		fields = append(fields, "readonly")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write(fields); err != nil {
		return "", err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// lastLine keeps the last line that is not empty of what it gets.
type lastLine struct {
	mu   sync.Mutex
	buf  []byte
	last string
}

// Write keeps the last whole line of p that is not empty, and the part after it.
func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			l.last = line
		}
		l.buf = l.buf[i+1:]
	}
	// A line longer than 4096 bytes keeps its end.
	if len(l.buf) > 4096 {
		l.buf = l.buf[len(l.buf)-4096:]
	}
	return len(p), nil
}

// text is the last line, the part with no line break when it is not empty.
func (l *lastLine) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rest := strings.TrimSpace(string(l.buf)); rest != "" {
		return rest
	}
	return l.last
}

// gitContainerFor is the container of the git session gs of the run runID, behind the wall of
// spec, with the run's image, writing to out. The engine is the wall's command, with this
// process's environment without the access key's variables and the run key, as the wall's
// commands have it.
func gitContainerFor(run openedRun, spec *runSpec, runID string, gs runcopy.GitSession, mounts []wall.Mount, out io.Writer) gitContainer {
	c := gitContainer{
		Docker: "docker", Env: accesskey.WithoutVariables(os.Environ()),
		Name: "qory-copy-" + runID, Label: runLabel + runID,
		// The wall's default user is the uid:gid of this process.
		User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Mounts: mounts, Session: gs, Output: out,
	}
	img := run.Image()
	c.Image, c.Runtime = img.Ref, img.Runtime
	if d, ok := spec.Wall.(*wall.Docker); ok {
		if d.Command != "" {
			c.Docker = d.Command
		}
		if d.User != "" {
			c.User = d.User
		}
	}
	return c
}
