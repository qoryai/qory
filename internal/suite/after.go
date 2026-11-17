package suite

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The files that the script of [Command] writes below the git folder of the workspace, and
// that [Judge] reads on this machine. The suite's code runs with the workspace mounted
// for writing, so a person must treat each of them as written by the agent.
const (
	// StateFolder is the folder of the files, relative to the workspace.
	StateFolder = ".git/qory-suite"
	statusFile  = StateFolder + "/status"
	resultCopy  = StateFolder + "/result"
	outputFile  = StateFolder + "/output"
	secondsFile = StateFolder + "/seconds"
	// maxStatus is the most bytes of the status file that qory reads.
	maxStatus = 256
	// MaxOutput is the most bytes of the command's output that [Outcome] has: the last ones.
	MaxOutput = 64 << 10
)

// MaxCommandBytes is the longest command that [Command] takes: Linux limits one argument
// to 128 KiB, and the command is one.
const MaxCommandBytes = 64 << 10

// The exit statuses of the script before the suite's command ran.
const (
	// ExitNoState is the exit status of a script that could not make [StateFolder] or a
	// temporary folder.
	ExitNoState = 70
	// ExitNoClone is the exit status of a script whose clone of the commit failed.
	ExitNoClone = 71
	// ExitNoHead is the exit status of a script whose git could not read HEAD of the workspace.
	ExitNoHead = 72
)

// script is the shell script of the after step. Its arguments are the full row's command,
// its result file ("" for none) and the commit that the session started at ("" for none).
// The command and the result file are arguments, never part of the script's text.
//
// The script clears [StateFolder]. When git cannot read HEAD, or HEAD has no commit but the
// session started at one, it exits 72. When HEAD is still the start commit, or has no commit
// and the session started at none, it writes "skipped" to the status file and exits 0.
// Otherwise it clones HEAD into a new folder of the container with git, hooks off and no hard
// link, so no file that the agent left untracked or ignored reaches the suite. In the clone it
// deletes the result file, writes "ran <commit>" to the status file and runs the command with
// a home folder that the command can write, its output also kept. It copies the result file,
// the last 64 KiB of the output and the seconds the command took to the folder, and exits
// with the command's status.
const script = `cmd=$1; file=$2; start=$3
d=` + StateFolder + `
if ! rm -rf "$d" || ! mkdir -m 700 "$d"; then
  echo "qory: could not make the folder $d in the workspace." >&2
  exit 70
fi
head=$(git rev-parse --verify --quiet 'HEAD^{commit}')
st=$?
if [ "$st" -gt 1 ] || { [ "$st" -eq 1 ] && [ -n "$start" ]; }; then
  echo "qory: git could not read HEAD of the workspace." >&2
  exit 72
fi
if [ "$head" = "$start" ]; then
  echo skipped >"$d/status"
  exit 0
fi
work=$(mktemp -d) || { echo "qory: could not make a temporary folder." >&2; exit 70; }
src=$work/src
if ! git -c core.hooksPath=/dev/null -c protocol.file.allow=always clone --quiet --no-local --no-hardlinks --no-checkout -- "$PWD" "$src" ||
  ! git -C "$src" -c core.hooksPath=/dev/null checkout --quiet --detach "$head"; then
  echo "qory: could not clone the commit '$head' of the workspace." >&2
  exit 71
fi
if [ -n "$file" ]; then rm -f -- "$src/$file"; fi
echo "ran $head" >"$d/status" || exit 70
mkdir "$work/home" && HOME=$work/home && export HOME
unset GIT_CONFIG_COUNT
t0=$(date +%s)
{ (cd "$src" && exec sh -c "$cmd"); echo $? >"$work/st"; } 2>&1 | tee "$work/out"
st=$(cat "$work/st" 2>/dev/null || echo 1)
echo $(( $(date +%s) - t0 )) >"$d/seconds"
tail -c 65536 "$work/out" >"$d/output" 2>/dev/null
if [ -n "$file" ] && [ -f "$src/$file" ] && [ ! -L "$src/$file" ]; then
  cp -- "$src/$file" "$d/result" 2>/dev/null
fi
exit $st
`

// commitID is the form of a commit id: 40 or 64 lower-case hexadecimal digits.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// The errors of [Command].
var (
	// ErrBadCommand is the error of a command that is longer than [MaxCommandBytes] or has a NUL.
	ErrBadCommand = errors.New("the full row's command is too long or has a NUL")
	// ErrBadStart is the error of a start commit that is not a full commit id.
	ErrBadStart = errors.New("the start commit is not a full commit id")
)

// Command is the command of the session's after step, SessionSpec.After, that runs the full
// suite of full on the commit that HEAD has in the workspace when the session ends, unless
// HEAD is start, the commit the session started at, or "" for none. The script is described at [script].
func Command(start string, full Full) ([]string, error) {
	switch {
	case len(full.Command) > MaxCommandBytes || strings.ContainsRune(full.Command, 0):
		return nil, ErrBadCommand
	case start != "" && !commitID.MatchString(start):
		return nil, ErrBadStart
	case full.ResultFile != "" && !CleanResultPath(full.ResultFile):
		return nil, fmt.Errorf("%w: the result file %q is no path below the repository", ErrBadCommand, full.ResultFile)
	}
	return []string{"sh", "-c", script, "sh", full.Command, full.ResultFile, start}, nil
}

// State is how a suite ended. Failed, Timeout and Stopped are red; Skipped is no suite.
type State string

// The states of a suite.
const (
	Passed  State = "passed"
	Failed  State = "failed"
	Timeout State = "timeout"
	Stopped State = "stopped"
	// Skipped is a session whose HEAD did not move.
	Skipped State = "skipped"
)

// Red reports whether s counts against the work: Failed, Timeout and Stopped.
func (s State) Red() bool { return s == Failed || s == Timeout || s == Stopped }

// End is how the after step ended, as Forager's session reports it.
type End struct {
	// Succeeded is the step state succeeded: the script exited 0.
	Succeeded bool
	// Cancelled is the step state cancelled, and TimedOut its reason timeout, the cap.
	Cancelled, TimedOut bool
	// ExitCode is the script's exit status, or -1 for a step that ended with none.
	ExitCode int
}

// Outcome is the judgement of one suite.
type Outcome struct {
	State State
	// Commit is the commit the suite ran on, "" when the script did not get to it.
	Commit string
	// Started is whether the script got as far as the command.
	Started bool
	// ExitCode is the script's exit status: the command's, for a command that started.
	ExitCode int
	// Counts are the numbers of the result file, nil when qory cannot read them.
	Counts *Counts
	// Mismatch is whether the result file's totals do not match its testcases.
	Mismatch bool
	// Seconds is the time the command took, -1 when the script did not write it.
	Seconds int
	// Output is the last [MaxOutput] bytes of what the command printed, "" for none.
	Output string
}

// NotStarted reports whether the suite failed before its command ran: the script or the step
// ended first, which no change to the repository's code mends.
func (o Outcome) NotStarted() bool { return o.State == Failed && !o.Started }

// Judge reads the files of the script below workspace, the folder the after step ran in,
// and judges the suite that end describes, with the full row's result file. A suite is
// Skipped when the status file is "skipped" and the script exited 0; Timeout at the cap, and
// Stopped when a stop from outside cut the step; Failed when the script did not get to the
// command, when the command exited other than 0, when a test failed, or when it ran 0
// tests. With exit status 0 and no readable result file the suite is Passed, with no counts.
func Judge(workspace string, full Full, end End) Outcome {
	kind, commit := readStatus(workspace)
	out := Outcome{Commit: commit, Started: kind == "ran", ExitCode: end.ExitCode, Seconds: -1}
	if kind == "skipped" && end.Succeeded {
		out.State = Skipped
		return out
	}
	switch {
	case end.TimedOut:
		out.State = Timeout
		return out
	case end.Cancelled:
		out.State = Stopped
		return out
	case !out.Started:
		out.State = Failed
		return out
	}
	out.State = Failed
	out.Output, out.Seconds = readOutput(workspace), readSeconds(workspace)
	if full.ResultFile != "" {
		counts, err := ParseJUnit(ReadResult(workspace, resultCopy))
		switch {
		case errors.Is(err, ErrMismatch):
			out.Mismatch = true
		case err == nil:
			out.Counts = &counts
		}
	}
	if end.ExitCode == 0 && !out.Mismatch && (out.Counts == nil || out.Counts.Tests > 0 && out.Counts.Failed == 0) {
		out.State = Passed
	}
	return out
}

// readOutput is the output file, cut to its last [MaxOutput] bytes at a whole UTF-8 character.
func readOutput(workspace string) string {
	b := ReadBelow(workspace, outputFile, MaxOutput)
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return string(b)
}

// readSeconds is the whole number of seconds in the seconds file, -1 for a file with none.
func readSeconds(workspace string) int {
	n, err := strconv.Atoi(strings.TrimSpace(string(ReadBelow(workspace, secondsFile, 32))))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// readStatus is the first word of the status file, "ran" or "skipped", and the commit after
// "ran"; "" for a file that is missing or has another form.
func readStatus(workspace string) (kind, commit string) {
	line, _, _ := bytes.Cut(ReadBelow(workspace, statusFile, maxStatus), []byte("\n"))
	word, rest, _ := strings.Cut(string(line), " ")
	switch {
	case word == "skipped" && rest == "":
		return "skipped", ""
	case word == "ran" && commitID.MatchString(rest):
		return "ran", rest
	}
	return "", ""
}

// Clear removes [StateFolder] below workspace, without following a link out of it.
func Clear(workspace string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	return root.RemoveAll(StateFolder)
}
