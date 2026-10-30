// Package child starts a qory command for the pool program: qory run, or a step before
// it such as qory harness compose. The command gets an environment built from an
// allow-list, never a copy of the program's: PATH, cleaned as [CleanPath] has it so no
// entry lands in the pool's runs folder, HOME, the folder the caller names in its
// place when it names one, TMPDIR and LANG, XDG_CONFIG_HOME
// set to the runs' configuration directory, XDG_STATE_HOME set to the run's state
// directory when the caller names one, and DOCKER_HOST and DOCKER_CONTEXT when they are
// set. No QORY_* or ANTHROPIC_* variable, and no key, reaches it. Its standard input
// is /dev/null, and its standard output and error go, line by line and prefixed, to the
// program's log.
//
// A run key goes to the command through a pipe whose read end is its descriptor 3
// ([KeyFD]), which qory run reads with --run-key-fd 3. The program writes the key once
// the command started, closes the write end and clears its copy. A run code goes to it
// as a file the caller opened and unlinked, passed as descriptor 3, which qory run
// reads with --run-code-file /dev/fd/3. Neither is ever in the command's arguments or
// environment.
//
// The command is stopped when the context ends: SIGTERM, then, once the grace has
// passed, SIGKILL to the command's process group.
package child

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/secretmask"
)

// KeyFD is the descriptor the command reads the run key or the run code from: the
// first of ExtraFiles, the one after standard error.
const KeyFD = 3

// ContainsSecret reports whether v contains what starts an access key secret, a run
// key, a run code or an enrolment code: qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or
// qeci_, in any case, as [accesskey.ContainsSecret] checks.
func ContainsSecret(v string) bool {
	return accesskey.ContainsSecret(v) || strings.Contains(strings.ToLower(v), accesskey.CodePrefixRun)
}

// DefaultGrace is how long a command has after SIGTERM before it is killed.
const DefaultGrace = 30 * time.Second

// Secret is a run key as the program holds it: its bytes, and a way to clear them. It is
// the smallest view of a key's holder, defined here so this package needs nothing of
// the program that holds the key.
type Secret interface {
	// Bytes is the key, the holder's bytes themselves: no copy is kept of them.
	Bytes() []byte
	// Clear overwrites the bytes, and is safe to call more than once.
	Clear()
}

// allowed is the variables a command gets from the program's environment, when they are
// set and not empty. XDG_CONFIG_HOME and XDG_STATE_HOME are not among them: they are
// always the directories the caller names.
var allowed = []string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT"}

// Env is the environment of a command: the allowed variables of the program's
// environment that are set and not empty, its PATH cleaned as [CleanPath] has it with
// runs, XDG_CONFIG_HOME=configHome, and XDG_STATE_HOME=stateHome when stateHome is not
// empty.
func Env(configHome, stateHome, runs string) []string {
	env := make([]string, 0, len(allowed)+2)
	for _, name := range allowed {
		v := os.Getenv(name)
		if name == "PATH" {
			v = CleanPath(v, runs)
		}
		if v != "" {
			env = append(env, name+"="+v)
		}
	}
	env = append(env, "XDG_CONFIG_HOME="+configHome)
	if stateHome != "" {
		env = append(env, "XDG_STATE_HOME="+stateHome)
	}
	return env
}

// CleanPath is path, a PATH, without its empty and relative entries, and without each
// entry that, its links and ".." resolved, is runs or inside it: the pool's runs folder,
// where walled runs write, resolved the same way. An entry is resolved through the links
// of the longest part of it that exists, and cleaned past that. The entries kept stay
// as written, in their order. An empty runs drops no absolute entry.
func CleanPath(path, runs string) string {
	root := ""
	if runs != "" {
		root = resolved(runs)
	}
	var kept []string
	for _, e := range filepath.SplitList(path) {
		if e == "" || !filepath.IsAbs(e) {
			continue
		}
		if root != "" && within(resolved(e), root) {
			continue
		}
		kept = append(kept, e)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// LookPath is the program name on the PATH a command gets with runs, as [Env] gives it:
// in the first entry that holds an executable regular file of that name. When no entry
// holds one, the error is exec's, [exec.ErrNotFound].
func LookPath(name, runs string) (string, error) {
	for _, dir := range filepath.SplitList(CleanPath(os.Getenv("PATH"), runs)) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Program is name as a command starts it: name as it is when it holds a path
// separator, else the program of that name on the PATH a command gets with runs, as
// [LookPath] finds it.
func Program(name, runs string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return name, nil
	}
	return LookPath(name, runs)
}

// resolved is p, absolute, with its links and ".." resolved: all of it when it exists,
// else the longest part of it that exists, with the rest cleaned after it.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	p, rest := filepath.Clean(p), ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// within reports whether p is root or inside it; both are resolved.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// Command is a command to start.
type Command struct {
	// Path is the program; empty is the qory binary this process runs,
	// os.Executable. A name with no path separator is found on the command's PATH, as
	// [Program] has it.
	Path string
	// Args are the arguments after the program's name. None of them may contain a
	// secret, as [ContainsSecret] checks.
	Args []string
	// Dir is the folder it runs in.
	Dir string
	// ConfigHome is its XDG_CONFIG_HOME, an absolute directory.
	ConfigHome string
	// StateHome, when set, is its XDG_STATE_HOME, an absolute directory: where qory
	// keeps the run's record. Empty, the command gets no XDG_STATE_HOME.
	StateHome string
	// Home, when set, is its HOME, an absolute directory, in place of the program's
	// HOME, or of the HOME in Env: a program that looks in $HOME, such as docker or git,
	// reads this folder.
	Home string
	// Runs is the pool's runs folder, where walled runs write: no entry of the
	// command's PATH lands in it, as [CleanPath] has it.
	Runs string
	// Env, when not nil, is the command's whole environment, in place of the one [Env]
	// makes of ConfigHome, StateHome and Runs: for a command inside a wall, whose
	// environment is the session's.
	Env []string
	// Key, when set, is written to the command's descriptor [KeyFD] and cleared, on
	// every path. The caller passes --run-key-fd 3 among Args.
	Key Secret
	// File, when set and Key is not, is the command's descriptor [KeyFD]: a file the
	// caller opened for reading. Run closes it on every path.
	File *os.File
	// Output takes the command's standard output and error, both. It is a
	// [LineWriter], which [Run] flushes once the command ended.
	Output *LineWriter
	// Mask, when set with Output, masks the output before Output gets it, as
	// [secretmask.Set.Writer] masks a stream, a value split across two writes and across
	// a cut of a long line included.
	Mask *secretmask.Set
	// Grace is how long the command has after SIGTERM before it is killed; zero is
	// [DefaultGrace].
	Grace time.Duration
	// Started, when set, is called once the command started.
	Started func()
}

// Result is how a command ended.
type Result struct {
	// ExitCode is the command's exit status, -1 when a signal ended it.
	ExitCode int
	// Signal is the signal that ended it, 0 when it exited.
	Signal syscall.Signal
	// Stopped reports whether the program sent it SIGTERM, at the context's end,
	// before it ended.
	Stopped bool
}

// Run starts c, waits for it to end, and says how it ended. The error is for a command
// that could not be started; one that started and failed is its Result. When ctx ends,
// the command gets SIGTERM, then SIGKILL to its process group once the grace has passed.
func Run(ctx context.Context, c Command) (Result, error) {
	if c.Key != nil {
		defer c.Key.Clear()
	}
	if c.File != nil {
		defer c.File.Close()
	}
	for _, a := range c.Args {
		if ContainsSecret(a) {
			return Result{}, errors.New("pass a run key or a run code through a descriptor alone: an argument of the command contains a secret")
		}
	}
	if !filepath.IsAbs(c.ConfigHome) {
		return Result{}, fmt.Errorf("the runs' configuration directory %s is not an absolute path", exports.Value(c.ConfigHome, true))
	}
	if c.StateHome != "" && !filepath.IsAbs(c.StateHome) {
		return Result{}, fmt.Errorf("the run's state directory %s is not an absolute path", exports.Value(c.StateHome, true))
	}
	// The pool names Home under its folder, which it checks is absolute first: no
	// person reaches this check.
	if c.Home != "" && !filepath.IsAbs(c.Home) {
		return Result{}, fmt.Errorf("child.Command.Home %s is not an absolute path", exports.Value(c.Home, true))
	}
	path := c.Path
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return Result{}, err
		}
		path = exe
	}
	path, lookErr := Program(path, c.Runs)
	if lookErr != nil {
		return Result{}, lookErr
	}
	grace := c.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	cmd := exec.Command(path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = Env(c.ConfigHome, c.StateHome, c.Runs)
	if c.Env != nil {
		cmd.Env = c.Env
	}
	if c.Home != "" {
		cmd.Env = append(slices.DeleteFunc(slices.Clone(cmd.Env), func(kv string) bool { return strings.HasPrefix(kv, "HOME=") }), "HOME="+c.Home)
	}
	// A nil Stdin is the null device.
	cmd.Stdin = nil
	if c.Output != nil {
		// One writer for both: exec copies them through one pipe.
		cmd.Stdout, cmd.Stderr = c.Output, c.Output
		defer c.Output.Flush()
		if c.Mask != nil {
			masked := c.Mask.Writer(c.Output)
			cmd.Stdout, cmd.Stderr = masked, masked
			// Deferred after Output's Flush, so it runs first: the bytes it kept back
			// reach Output before Output writes its last line.
			defer masked.Flush()
		}
	}
	// After the command exits, what it started may hold its output open: exec waits
	// that long for it, then closes the pipe.
	cmd.WaitDelay = grace
	group(cmd)
	var r, w *os.File
	if c.Key != nil {
		var err error
		if r, w, err = os.Pipe(); err != nil {
			return Result{}, err
		}
		cmd.ExtraFiles = []*os.File{r}
	} else if c.File != nil {
		cmd.ExtraFiles = []*os.File{c.File}
	}
	if err := ctx.Err(); err != nil {
		closeAll(r, w)
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		closeAll(r, w)
		return Result{}, err
	}
	if c.File != nil {
		// The command has its copy of the descriptor.
		c.File.Close()
	}
	if c.Started != nil {
		c.Started()
	}
	if c.Key != nil {
		// The command has its copy of the read end. The key is shorter than a pipe's
		// buffer, so the write does not wait for the command to read; a command that
		// has exited already fails it, and its exit says the rest.
		r.Close()
		_, _ = w.Write(c.Key.Bytes())
		w.Close()
		c.Key.Clear()
	}
	var stopped atomic.Bool
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		stopped.Store(true)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-t.C:
			kill(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	res := Result{Stopped: stopped.Load()}
	var exit *exec.ExitError
	switch {
	case err == nil || errors.Is(err, exec.ErrWaitDelay):
		res.ExitCode = cmd.ProcessState.ExitCode()
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.ExitCode, res.Signal = -1, ws.Signal()
		}
	default:
		return res, err
	}
	return res, nil
}

// closeAll closes the files that are set.
func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			f.Close()
		}
	}
}
