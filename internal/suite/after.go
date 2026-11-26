package suite

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/hostgit"
)

// The files that the script of [Command] writes below the git folder of the workspace, and
// that [Judge] reads on this machine. The suite's code runs with the workspace mounted
// for writing, so a person must treat each of them as written by the agent.
const (
	// StateFolder is the folder of the files, relative to the workspace.
	StateFolder = ".git/qory-suite"
	statusFile  = StateFolder + "/status"
	resultCopy  = StateFolder + "/result"
	// maxStatus is the most bytes of the status file that qory reads.
	maxStatus = 256
)

// HeadMarker is the start of the first line that the script prints to standard output once
// it has cloned HEAD, before the suite's command runs and before anything else reaches
// standard output: HeadMarker and the commit that the suite runs on. The status file has the
// same commit, but the suite's code can write that file, so the marker line is the commit
// that qory trusts, and qory reads only the first line of standard output, see [ReadHead].
const HeadMarker = "qory-suite-head "

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
// its result file ("" for none), the commit that the session started at ("" for none), the
// commit that the run has a suite result for ("" for none) and the protocol name of
// [scriptGit]. The command and the result file are arguments, never part of the script's
// text.
//
// The script clears [StateFolder]. When git cannot read HEAD, or HEAD has no commit but the
// session started at one, it exits 72. When HEAD is the tested commit, or has no commit
// and the session started at none, it writes "skipped" to the status file and exits 0.
// Otherwise it clones HEAD into a new folder of the container with git, hooks off and no
// hard link, so no file that the agent left untracked or ignored reaches the suite. Each
// git of the script starts with [scriptGit]. The read of HEAD keeps its standard output,
// the commit, and sends its standard error to /dev/null. The clone and the checkout send
// both to /dev/null. So no filter, attribute or file name of the commit reaches the step's
// output before the marker. In the clone the script deletes the result file, prints the
// line "qory-suite-head <commit>" to its standard output, writes "ran <commit>" to the
// status file and runs the command with a home folder that the command can write. It
// copies the result file to the folder, and exits with the command's status.
var script = `cmd=$1; file=$2; start=$3; tested=$4; proto=$5
d=` + StateFolder + `
if ! rm -rf "$d" || ! mkdir -m 700 "$d"; then
  echo "qory: could not make the folder $d in the workspace." >&2
  exit 70
fi
head=$(` + scriptGit(`"$proto"`) + ` rev-parse --verify --quiet 'HEAD^{commit}' 2>/dev/null)
st=$?
if [ "$st" -gt 1 ] || { [ "$st" -eq 1 ] && [ -n "$start" ]; }; then
  echo "qory: git could not read HEAD of the workspace." >&2
  exit 72
fi
if [ -z "$head" ] || [ "$head" = "$tested" ]; then
  echo skipped >"$d/status"
  exit 0
fi
work=$(mktemp -d) || { echo "qory: could not make a temporary folder." >&2; exit 70; }
src=$work/src
if ! ` + scriptGit("file") + ` clone --upload-pack="GIT_ALLOW_PROTOCOL=$proto git-upload-pack" --quiet --no-local --no-hardlinks --no-checkout -- "$PWD" "$src" >/dev/null 2>&1 ||
  ! ` + scriptGit(`"$proto"`) + ` -C "$src" checkout --quiet --detach "$head" >/dev/null 2>&1; then
  echo "qory: could not clone the commit '$head' of the workspace." >&2
  exit 71
fi
if [ -n "$file" ]; then rm -f -- "$src/$file"; fi
echo "` + HeadMarker + `$head" || exit 70
echo "ran $head" >"$d/status" || exit 70
mkdir "$work/home" && HOME=$work/home && export HOME
unset GIT_CONFIG_COUNT
(cd "$src" && exec sh -c "$cmd")
st=$?
if [ -n "$file" ] && [ -f "$src/$file" ] && [ ! -L "$src/$file" ]; then
  cp -- "$src/$file" "$d/result" 2>/dev/null
fi
exit $st
`

// scriptGit is the start of each git command of [script], which reads the workspace or the
// clone: git with the variables and the -c options of every git of the script, before the
// options and the arguments of the one command, and with GIT_ALLOW_PROTOCOL set to allow,
// a shell word. The script's git reads no global or system configuration, since the agent
// can write the home folder, and has the -c options of [hostgit.Flags]: it runs no hook,
// and reads no replace ref or commit-graph, which could change the commit that the read of
// HEAD returns. A read of an object that a workspace with a promisor remote lacks starts
// git fetch, which runs programs of the workspace's configuration before the marker line,
// and such a program could print a forged one. [hostgit.NoLazyFetch] stops that fetch on a
// git that reads the variable: 2.45.0 and later, and the security releases of 2024-05-14
// of the lines before, 2.39.4, 2.40.2, 2.41.1, 2.42.2, 2.43.4 and 2.44.1. Another git,
// such as 2.39.3 or 2.44.0, does not read it: for that git, GIT_ALLOW_PROTOCOL wins over
// every protocol key of the workspace, and [hostgit.NoFetch] turns off what git fetch runs
// before it checks the protocol. The read of HEAD and the checkout allow only the name of
// [hostgit.NoProtocolName] that [Command] makes for each script. The clone needs the file
// protocol to clone the workspace, so it allows file, and its upload-pack, the git that
// reads the workspace's objects, gets the script's name. On a local transport git drops
// the -c options before it starts upload-pack. The upload-pack of the security releases of
// 2024-05-14, 2.45.1 among them, and of every later git turns the fetch of a missing
// object off, so on an earlier git only that GIT_ALLOW_PROTOCOL keeps upload-pack from a
// fetch. The variables are set for each git alone, so the suite's command has none.
func scriptGit(allow string) string {
	return "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 " + hostgit.NoLazyFetch + " GIT_ALLOW_PROTOCOL=" + allow +
		" git " + shellWords(slices.Concat(hostgit.Flags(), []string{"-c", "protocol.allow=never"}, hostgit.NoFetch()))
}

// shellWords is words, each in single quotes, separated by spaces.
func shellWords(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// commitID is the form of a commit id: 40 or 64 lower-case hexadecimal digits.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// The errors of [Command].
var (
	// ErrBadCommand is the error of a command that is longer than [MaxCommandBytes] or has a NUL.
	ErrBadCommand = errors.New("the full row's command is too long or has a NUL")
	// ErrBadResultFile is the error of a result file that is no path below the repository.
	ErrBadResultFile = errors.New("the full row's result file is no path below the repository")
	// ErrBadStart is the error of a start commit that is not a full commit id.
	ErrBadStart = errors.New("the start commit is not a full commit id")
)

// Command is the command of the session's after step, SessionSpec.After, that runs the full
// suite of full on the commit that HEAD has in the workspace when the session ends, unless
// HEAD is tested, the commit that the run has a suite result for, or "" for none. start is
// the commit the session started at, or "" for none. The script is described at [script];
// each call makes a new protocol name for it.
// shortcut: tested is not checked for its form, since a value that is no commit id never
// equals HEAD and the suite runs; replace it when a caller passes a value that is not
// from a marker line.
func Command(start, tested string, full Full) ([]string, error) {
	switch {
	case len(full.Command) > MaxCommandBytes || strings.ContainsRune(full.Command, 0):
		return nil, ErrBadCommand
	case start != "" && !commitID.MatchString(start):
		return nil, ErrBadStart
	case full.ResultFile != "" && !CleanResultPath(full.ResultFile):
		return nil, ErrBadResultFile
	}
	return []string{"sh", "-c", script, "sh", full.Command, full.ResultFile, start, tested, hostgit.NoProtocolName()}, nil
}

// State is how a suite ended. Failed, Timeout and Stopped are red; Skipped is no suite.
type State string

// The states of a suite.
const (
	Passed  State = "passed"
	Failed  State = "failed"
	Timeout State = "timeout"
	Stopped State = "stopped"
	// Skipped is a session whose HEAD is the commit that the run has a suite result for.
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
}

// NotStarted reports whether the suite ended before its command ran: the script failed, the step
// failed, or the cap cut the step before the script wrote "ran". No change to the repository's
// code mends it.
func (o Outcome) NotStarted() bool { return (o.State == Failed || o.State == Timeout) && !o.Started }

// Judge reads the files of the script below workspace, the folder the after step ran in,
// and judges the suite that end describes, with the full row's result file. A suite is
// Skipped when the status file is "skipped" and the script exited 0; Timeout at the cap, and
// Stopped when a stop from outside cut the step; Failed when the script did not get to the
// command, when the command exited other than 0, when a test failed, or when it ran 0
// tests. With exit status 0 and no readable result file the suite is Passed, with no counts.
func Judge(workspace string, full Full, end End) Outcome {
	kind, commit := readStatus(workspace)
	out := Outcome{Commit: commit, Started: kind == "ran", ExitCode: end.ExitCode}
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
