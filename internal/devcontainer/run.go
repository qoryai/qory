package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/qoryai/qory/internal/integration"
)

// The words of [Run]. The first %s is where the failure is: a lifecycle command, or a
// named command of one as [textNamed] has it.
const (
	textExit        = "%s exited with status %d"
	textSignal      = "%s was ended by a signal"
	textNotFound    = "%s could not start: its program is not on the PATH of its environment"
	textNotStarted  = "%s could not start: %s"
	textStopped     = "%s was stopped before it finished"
	textNotExpanded = "%s is run before its variables are replaced"
)

// Grace is how long a command stopped by its context has between SIGTERM and SIGKILL,
// and how long a process left holding a command's output, once the command exited,
// keeps it open before it is closed.
const Grace = 5 * time.Second

// grace is [Grace], which a test shortens.
var grace = Grace

// Run runs cmd, which [Command.Expand] returned, in dir, and returns how long it took.
// A command with no steps runs nothing and returns at once.
//
// Each step runs as a separate process: Line as /bin/sh -c Line, and Args directly,
// the program found on the PATH env sets, or as written when it holds a slash. env is
// the whole environment of each, nil an empty one; standard input is /dev/null, and
// stdout and stderr receive the output. Each step runs in a new process group. The
// steps of the object form run in parallel and Run waits for all of them; the command
// succeeds when each exits 0. A step that fails is named by its member and its name,
// never its text, the failures in the order written, joined by "; ".
//
// An [*os.File] is handed to each step as it is: the steps write to the file itself,
// the steps of the object form at once when they do, and a process a step leaves
// running keeps writing there after Run returns. Any other writer, nil aside, is
// written through a pipe, one step's write at a time, and the pipe is closed [Grace]
// after its step exits: a process the step left running that writes to it later gets
// an error, or SIGPIPE.
//
// When ctx ends, each step's whole group receives SIGTERM, and what is left of it
// [Grace] later receives SIGKILL; a process holding a step's output open is waited
// for no longer than that. A step that exited 0 leaves its group as it is: a command
// may start a service that is meant to keep running, as postStartCommand often does.
func Run(ctx context.Context, cmd Command, dir string, env []string, stdout, stderr io.Writer) (time.Duration, error) {
	start := time.Now()
	if cmd.Empty() {
		return 0, nil
	}
	if !cmd.expanded {
		return 0, fmt.Errorf(textNotExpanded, cmd.Member)
	}
	if len(cmd.Steps) > 1 {
		var mu sync.Mutex
		stdout, stderr = locked(&mu, stdout), locked(&mu, stderr)
	}
	errs := make([]error, len(cmd.Steps))
	var wg sync.WaitGroup
	for i, s := range cmd.Steps {
		wg.Go(func() {
			errs[i] = runStep(ctx, cmd.label(s), s, dir, env, stdout, stderr)
		})
	}
	wg.Wait()
	var failed []string
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return time.Since(start), errors.New(strings.Join(failed, "; "))
	}
	return time.Since(start), nil
}

// runStep runs one step as [Run] has it; where names it for an error.
func runStep(ctx context.Context, where string, s Step, dir string, env []string, stdout, stderr io.Writer) error {
	name, args := "/bin/sh", []string{"-c", s.Line}
	if s.Args != nil {
		program, ok := lookPath(s.Args[0], env)
		if !ok {
			return fmt.Errorf(textNotFound, where)
		}
		name, args = program, s.Args[1:]
	}
	c := exec.CommandContext(ctx, name, args...)
	if s.Args != nil {
		c.Args[0] = s.Args[0]
	}
	c.Dir = dir
	c.Env = append([]string{}, env...)
	// A nil Stdin is the null device.
	c.Stdin = nil
	c.Stdout, c.Stderr = stdout, stderr
	c.WaitDelay = grace
	group(c)
	var stoppedAt atomic.Int64
	terminate := c.Cancel
	c.Cancel = func() error {
		stoppedAt.Store(time.Now().UnixNano())
		return terminate()
	}
	err := c.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		deadline := time.Now()
		if at := stoppedAt.Load(); at != 0 {
			deadline = time.Unix(0, at).Add(grace)
		}
		endGroup(c, deadline)
		return fmt.Errorf(textStopped, where)
	}
	var ee *exec.ExitError
	switch {
	case errors.Is(err, exec.ErrWaitDelay):
		// The step exited 0, and a process it left kept its output open past the grace.
		return nil
	case errors.As(err, &ee):
		if code := ee.ExitCode(); code >= 0 {
			return fmt.Errorf(textExit, where, code)
		}
		return fmt.Errorf(textSignal, where)
	}
	// What kept the step from starting, without the path or the program it names.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Errorf(textNotStarted, where, integration.Printable(errno.Error()))
	}
	return fmt.Errorf(textNotStarted, where, "the system refused it")
}

// lookPath is the program name runs: name itself when it holds a slash, or the first
// executable regular file of that name in a folder of the PATH env sets, and false
// when there is none. A folder of PATH that is not absolute is passed over, so a
// program is never found in the folder the step runs in by chance.
func lookPath(name string, env []string) (string, bool) {
	if strings.Contains(name, "/") {
		return name, true
	}
	if name == "" {
		return "", false
	}
	path, _ := lookup(env, "PATH")
	for _, folder := range filepath.SplitList(path) {
		if !filepath.IsAbs(folder) {
			continue
		}
		p := filepath.Join(folder, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// locked is w with each write under mu, so steps that run in parallel write one at a
// time; nil stays nil, the null device, and an [*os.File] stays as it is, so each step
// gets the file and not a pipe.
func locked(mu *sync.Mutex, w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	if f, ok := w.(*os.File); ok {
		return f
	}
	return &lockedWriter{mu, w}
}

// lockedWriter is a writer that writes under a lock it shares.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

// Write writes p under the lock.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
