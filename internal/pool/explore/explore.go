// Package explore starts a run of the explore step on this machine, for the pool
// program, as the GitHub job of the explore runner does. Each run gets a run directory,
// <state>/pool/runs/<run_id>, mode 0700, holding:
//
//	stack/qory-stack.yaml  the stack of one module, explore-an-item
//	work/                  an empty git working tree, where the harness is composed and the run runs
//	homes/                 the composed harness's homes
//	in/inputs.json         the handover's inputs, as they came, mode 0600
//	state/                 every command's XDG_STATE_HOME, mode 0700: qory run keeps the run's record there
//
// The run is qory run claude with the run key on its descriptor 3, behind the docker
// wall, with in/ mounted read-only, at most 12 minutes and 40 turns. Every command gets
// the environment of [child.Env], with state/ as its XDG_STATE_HOME, so what qory keeps
// of the run stays in the run directory, and goes with it.
//
// Whether the run started is whether qory run made its record: the folder qory run
// keeps it in, which qory run's documentation names (see [RecordDir]). [EndingOf] says
// what the program does once it ended: post an outcome, or release the dispatch.
package explore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/qoryai/integrations/contracts"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/stack"
)

// The explore-an-item module: its name, the public repository it is in, and its folder
// there.
const (
	moduleName = "explore-an-item"
	moduleGit  = "https://github.com/qoryai/qory-github"
	modulePath = "harness/explore-an-item"
)

// The run's limits, as the GitHub job sets them.
const (
	runTimeout  = "12m"
	runMaxTurns = "40"
)

// promptText is the run's prompt, with the folder of inputs.json in place of %s.
const promptText = "Explore the ticket described in %s/inputs.json, following the explore-an-item skill. End with the explore v1 JSON document alone as your final response."

// inputsFile is the file the handover's inputs are written to, in in/.
const inputsFile = "inputs.json"

// runIDShape is a run id: a UUID in the canonical lower-case form.
var runIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// harnessRefShape is a harness ref: a commit id, or a release tag vX.Y.Z.
var harnessRefShape = regexp.MustCompile(`^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$`)

// Settings are what every run of the explore step takes from the pool's settings.
type Settings struct {
	// State is the pool program's state directory, an absolute path. The run
	// directories are under <State>/pool/runs.
	State string
	// HarnessRef is the commit or release tag of qory-github the explore-an-item
	// module is read from; HarnessPath, in its place, is the absolute path of a local
	// checkout of qory-github. Exactly one is set.
	HarnessRef  string
	HarnessPath string
	// RunConfig is the XDG_CONFIG_HOME of every command, an absolute directory: the
	// runs' configuration directory, whose forager.yaml sets the wall.
	RunConfig string
	// Qory is the qory binary; empty is the one this process runs. A name with no path
	// separator is found on the PATH every command gets, as [child.Program] has it.
	Qory string
	// Git is the git binary; empty is git. A name with no path separator is found on
	// the PATH every command gets, as [child.Program] has it.
	Git string
	// Log takes each line the commands write, after the run id.
	Log func(line string)
	// Grace is how long a command has after SIGTERM before it is killed; zero is
	// [child.DefaultGrace].
	Grace time.Duration
}

// Run is one run of the explore step, as its handover gave it.
type Run struct {
	// RunID is the run's id.
	RunID string
	// Key is the run key. Launch clears it on every path.
	Key child.Secret
	// Domain is the domain of the workspace the run is for, such as software, as the
	// handover gave it.
	Domain string
	// RunSettings is the runtime and model settings the handover gave, checked, or nil.
	// Launch composes the harness without them.
	RunSettings *contracts.RunSettings
	// Inputs are the handover's inputs, the bytes as they came.
	Inputs []byte
}

// Result is how a run ended.
type Result struct {
	// Dir is the run directory, "" when it was not made.
	Dir string
	// Record reports whether qory run made the run's record: the run started.
	Record bool
	// Run is how qory run ended; the zero Result when it was not started.
	Run child.Result
	// Ending is what the program does next, [EndingOf] Record and Run.
	Ending Ending
}

// Dir is the run directory of the run runID: <state>/pool/runs/<runID>.
func Dir(state, runID string) string {
	return filepath.Join(RunsDir(state), runID)
}

// RunsDir is the pool's runs folder under the pool program's state directory state,
// <state>/pool/runs, which holds every run directory: where walled runs write.
func RunsDir(state string) string {
	return filepath.Join(state, "pool", "runs")
}

// Launch makes the run's directory, composes the harness, writes the inputs and runs
// qory run, until it ends or ctx ends; at ctx's end qory run is stopped as
// [child.Run] stops a command. The error says why the run did not start, when a step
// before qory run failed or qory run could not be started; the Result's Ending is set
// either way. The run directory is left for [Remove].
func Launch(ctx context.Context, s Settings, r Run) (Result, error) {
	if r.Key != nil {
		defer r.Key.Clear()
	}
	res := Result{Ending: EndingOf(false, child.Result{})}
	dir, err := prepare(ctx, s, r)
	res.Dir = dir
	if err != nil {
		return res, err
	}
	work, in := filepath.Join(dir, "work"), filepath.Join(dir, "in")
	record, err := RecordDir(filepath.Join(dir, "state"), work, r.RunID)
	if err != nil {
		return res, err
	}
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("qory run has a record of the run %s already", r.RunID)
	}
	args := []string{
		"run", "claude", "--home", filepath.Join(dir, "homes"),
		"--run-key-fd", strconv.Itoa(child.KeyFD),
		"--wall", "docker", "--mount", in + ":ro", "--timeout", runTimeout,
		"--", "--max-turns", runMaxTurns, "--add-dir", in, "-p", fmt.Sprintf(promptText, in),
	}
	ran, err := child.Run(ctx, child.Command{
		Path: s.Qory, Args: args, Dir: work, ConfigHome: s.RunConfig, StateHome: filepath.Join(dir, "state"),
		Runs: RunsDir(s.State), Key: r.Key, Output: s.lines(r.RunID), Grace: s.Grace,
	})
	if err != nil {
		return res, fmt.Errorf("qory run could not be started: %w", err)
	}
	res.Run = ran
	_, statErr := os.Stat(record)
	res.Record = statErr == nil
	res.Ending = EndingOf(res.Record, ran)
	return res, nil
}

// prepare makes the run directory and everything in it before qory run, and returns
// the directory, links resolved, or "" when it was not made.
func prepare(ctx context.Context, s Settings, r Run) (string, error) {
	if !runIDShape.MatchString(r.RunID) {
		return "", errors.New("the run id is not a UUID in the canonical lower-case form")
	}
	source, err := s.source()
	if err != nil {
		return "", err
	}
	for _, d := range []struct{ name, path string }{{"the state directory", s.State}, {"the runs' configuration directory", s.RunConfig}} {
		if !filepath.IsAbs(d.path) {
			return "", fmt.Errorf("%s %s is not an absolute path", d.name, exports.Value(d.path, true))
		}
	}
	runs := RunsDir(s.State)
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Join(s.State, "pool"), runs} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	made := Dir(s.State, r.RunID)
	if err := os.Mkdir(made, 0o700); err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(made)
	if err != nil {
		return made, err
	}
	if err := writeStack(filepath.Join(dir, "stack"), source); err != nil {
		return dir, err
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		return dir, err
	}
	work := filepath.Join(dir, "work")
	if err := s.gitInit(ctx, r.RunID, work, state); err != nil {
		return dir, err
	}
	composed, err := child.Run(ctx, child.Command{
		Path: s.Qory,
		Args: []string{"harness", "compose", "-f", filepath.Join(dir, "stack", stack.FileName), "--runtime", "claude", "--home", filepath.Join(dir, "homes")},
		Dir:  work, ConfigHome: s.RunConfig, StateHome: state, Runs: RunsDir(s.State), Output: s.lines(r.RunID), Grace: s.Grace,
	})
	if err != nil {
		return dir, fmt.Errorf("qory harness compose could not be started: %w", err)
	}
	if composed != (child.Result{}) {
		return dir, fmt.Errorf("qory harness compose %s", ended(composed))
	}
	in := filepath.Join(dir, "in")
	if err := os.Mkdir(in, 0o700); err != nil {
		return dir, err
	}
	if err := writeFile(filepath.Join(in, inputsFile), r.Inputs); err != nil {
		return dir, err
	}
	return dir, nil
}

// source is the module's source the settings name: the public repository at the ref,
// or the module's folder in the local checkout.
func (s Settings) source() (stack.Source, error) {
	switch {
	case s.HarnessRef != "" && s.HarnessPath != "":
		return stack.Source{}, errors.New("the harness is given both as a ref and as a path; give one")
	case s.HarnessRef != "":
		if !harnessRefShape.MatchString(s.HarnessRef) {
			return stack.Source{}, errors.New("the harness ref is neither a commit id nor a release tag")
		}
		return stack.Source{Git: moduleGit, Ref: s.HarnessRef, Path: modulePath}, nil
	case s.HarnessPath != "":
		if !filepath.IsAbs(s.HarnessPath) {
			return stack.Source{}, fmt.Errorf("the harness path %s is not an absolute path", exports.Value(s.HarnessPath, true))
		}
		return stack.Source{Path: filepath.Join(s.HarnessPath, filepath.FromSlash(modulePath))}, nil
	}
	return stack.Source{}, errors.New("the harness is given neither as a ref nor as a path")
}

// stackFile is the stack the run composes: one module, explore-an-item.
type stackFile struct {
	APIVersion string        `yaml:"apiVersion"`
	Name       string        `yaml:"name"`
	Modules    []stackModule `yaml:"modules"`
}

// stackModule is the stack's one module.
type stackModule struct {
	Name   string       `yaml:"name"`
	Source stack.Source `yaml:"source"`
}

// writeStack writes the stack of the module from source to qory-stack.yaml in dir,
// which it makes, and reads it back as qory reads a stack, so a stack qory would
// refuse is no run.
func writeStack(dir string, source stack.Source) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(stackFile{APIVersion: stack.APIVersion, Name: "explore", Modules: []stackModule{{Name: moduleName, Source: source}}})
	if err != nil {
		return err
	}
	file := filepath.Join(dir, stack.FileName)
	if err := writeFile(file, b); err != nil {
		return err
	}
	_, err = stack.Load(file)
	return err
}

// gitInit makes work an empty git working tree, as git init --quiet does, with no
// configuration of the user's or the system's.
func (s Settings) gitInit(ctx context.Context, runID, work, state string) error {
	git := s.Git
	if git == "" {
		git = "git"
	}
	git, err := child.Program(git, RunsDir(s.State))
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	cmd := exec.CommandContext(ctx, git, "init", "--quiet", work)
	cmd.Env = append(child.Env(s.RunConfig, state, RunsDir(s.State)), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out := s.lines(runID)
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	out.Flush()
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	return nil
}

// lines is the writer of a command's output: each line after the run id.
func (s Settings) lines(runID string) *child.LineWriter {
	log := s.Log
	if log == nil {
		log = func(string) {}
	}
	return child.NewLineWriter(runID+": ", log)
}

// ended says how a command ended, for an error.
func ended(r child.Result) string {
	if r.Signal != 0 {
		return "ended on the signal " + r.Signal.String()
	}
	return "exited " + strconv.Itoa(r.ExitCode)
}

// writeFile writes b to a new file, mode 0600.
func writeFile(name string, b []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// RecordDir is the folder qory run keeps the record of the run runID in, when it runs
// in work with XDG_STATE_HOME set to state, an absolute directory:
// <state>/qory/runs/<checkout>-<hash>/<runID>, as qory run's documentation says it.
// <checkout> is work's name and <hash> the first 12 hex digits of the SHA-256 of its
// path, both with links resolved.
func RecordDir(state, work, runID string) (string, error) {
	if !filepath.IsAbs(state) {
		return "", fmt.Errorf("the run's state directory %s is not an absolute path", exports.Value(state, true))
	}
	real, err := filepath.EvalSymlinks(work)
	if err != nil {
		return "", err
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", err
	}
	return filepath.Join(state, "qory", "runs", checkoutFolder(real), runID), nil
}

// Remove removes the run directory of the run runID, and drops its place record.
func Remove(state, runID string) error {
	if !runIDShape.MatchString(runID) {
		return errors.New("the run id is not a UUID in the canonical lower-case form")
	}
	dir := Dir(state, runID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	dropPlace(dir)
	return nil
}

// dropPlace drops the place record of the run directory dir, once it is removed. It
// is a variable so a test sees it called.
//
// TODO: drop it through internal/places when the host-git work carries that package
// onto integrate. Until then there is no place record, and nothing to drop.
var dropPlace = func(dir string) { _ = dir }

// checkoutFolder is the name of the folder of a checkout's records, for the checkout
// at real, an absolute path with links resolved: its name, "-", and the first 12 hex
// digits of the SHA-256 of the path.
func checkoutFolder(real string) string {
	sum := sha256.Sum256([]byte(real))
	return filepath.Base(real) + "-" + hex.EncodeToString(sum[:])[:12]
}
