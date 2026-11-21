package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/integrations/contracts"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/submission"
)

// The words of the submit program's run. Each states the cause only: line 1 has at most 100
// characters with its prefix, and the values follow on indented lines. textProgramLine is a line
// of the program's standard error, masked.
const (
	textProgramLine     = "qory run: submit program: %s"
	textProgramFailed   = "qory run: The submit program failed, so a repository it did not finish has no draft.\n  Exit code: %d.\n  Draft of the repository: program-failed."
	textProgramTimeout  = "qory run: The submit program ran past its limit, so a repository it did not finish has no draft.\n  Limit: %s.\n  Draft of the repository: program-timeout."
	textProgramNoStart  = "qory run: The submit program did not start, so a repository has no draft.\n  Cause: %s\n  Draft of the repository: program-failed."
	textProgramTooMany  = "qory run: The submit program did not run, so a repository has no draft.\n  Cause: the repository has %d commits, and the program takes at most %d.\n  Draft of the repository: program-failed."
	textProgramNoDraft  = "qory run: The submit program printed no whole draft, so a repository has no draft.\n  Exit code: %d.\n  Draft of the repository: program-failed."
	textProgramLeftOut  = "qory run: The submit program printed a line that qory cannot use, so qory left it out.\n  Line %d: %s."
	textProgramNoFolder = "qory run: The submit program did not run, so a repository has no draft.\n  Cause: %s\n  Draft of the repository: program-failed."
)

// maxProgramLines is how many lines of the program's standard error the step prints, and
// maxProgramLine the longest one; maxProgramOutput is how much of each of its outputs qory
// reads, and maxLeftOut how many lines that qory left out it lists.
const (
	maxProgramLines  = 20
	maxProgramLine   = 300
	maxProgramOutput = 1 << 20
	maxLeftOut       = 5
)

// programLimit is how long the submit program may run, from its start: the contract's
// SubmitTimeLimit, so a program that waits on a login prompt does not stop the run's end.
const programLimit = 120 * time.Second

// submitLimit, when not 0, is how long the submit program may run, in place of [programLimit]; a
// test sets it.
var submitLimit time.Duration

// submitProgram is the submit program of a run with no run code: the module's command, with the
// home's path in it, which runs on the person's machine, outside the wall, once for each
// repository of the submission. The program uses the login of the forge's command line that the
// person has, and gets no credential from qory. A run with a run code has none: the forge's
// adapter opens its draft from the stored submission.
type submitProgram struct {
	command []string
	limit   time.Duration
}

// programEnd is how one run of the program ended.
type programEnd struct {
	exit     int
	timedOut bool
	startErr string
	stdout   string
	stderr   string
}

// apply runs the program on document, once for each repository that the run pushed, and returns
// the document to store: each repository with the addresses of its commits and its draft, as
// the program's lines give them, or with the reason it has none. Each line that the program
// printed on its standard error goes to w. An error is a ctx that ended, which cuts the step.
func (p *submitProgram) apply(ctx context.Context, w io.Writer, document *submission.Submission) (*submission.Submission, error) {
	out := *document
	out.Repositories = slices.Clone(document.Repositories)
	for i, r := range document.Repositories {
		if r.Base == nil || len(r.Commits) == 0 {
			continue
		}
		result, err := p.applyTo(ctx, w, document, r)
		if err != nil {
			return nil, err
		}
		out.Repositories[i] = result
	}
	return &out, nil
}

// placeholder is the draft of a repository that the program may fill: the verdict is passed,
// the session wrote text, and no program has run yet.
var placeholder = submission.None(submission.NoneNoProgram, 0)

// applyTo runs the program for repository r of document and returns r with its results.
func (p *submitProgram) applyTo(ctx context.Context, w io.Writer, document *submission.Submission, r submission.Repository) (submission.Repository, error) {
	fail := func(reason string, exit int) submission.Repository {
		if bytes.Equal(r.Draft, placeholder) {
			r.Draft = submission.None(reason, exit)
		}
		return r
	}
	commits := make([]string, len(r.Commits))
	for i, c := range r.Commits {
		commits[i] = c.Commit
	}
	if len(commits) > maxSubmitCommits {
		fmt.Fprintf(w, textProgramTooMany+"\n", len(commits), maxSubmitCommits)
		// The program did not run, so its exit is 1, as the contract has it for a program that
		// did not start.
		return fail(submission.NoneProgramFailed, contracts.SubmitExitForge), nil
	}
	dir, err := os.MkdirTemp("", "qory-submit-")
	if err != nil {
		fmt.Fprintf(w, textProgramNoFolder+"\n", shownError(err))
		return fail(submission.NoneProgramFailed, contracts.SubmitExitForge), nil
	}
	defer os.RemoveAll(dir)
	input, err := p.input(document, r, commits, dir)
	if err != nil {
		fmt.Fprintf(w, textProgramNoFolder+"\n", shownError(err))
		return fail(submission.NoneProgramFailed, contracts.SubmitExitForge), nil
	}
	end := p.run(ctx, input, dir)
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	printProgramLines(w, end.stderr)
	stdout, unusable := withoutUnusableTitle(end.stdout, input.Draft)
	rec := contracts.ReadSubmitRun(contracts.SubmitEnd{Started: end.startErr == "", TimedOut: end.timedOut, Exit: end.exit},
		stdout, r.System, input.Draft, append([]string{r.Base.Commit}, commits...))
	// The reader ignores the output after a timeout, a program that did not start and an
	// exit status other than 0, 1 or 2, so those runs list no line.
	if end.startErr == "" && !end.timedOut && end.exit >= contracts.SubmitExitDone && end.exit < contracts.SubmitExitLogin {
		rec.Invalid = append(rec.Invalid, unusable...)
	}
	slices.SortFunc(rec.Invalid, func(a, b contracts.SubmitLineError) int { return a.Line - b.Line })
	for i, bad := range rec.Invalid {
		if i == maxLeftOut {
			break
		}
		fmt.Fprintf(w, textProgramLeftOut+"\n", bad.Line, bad.Reason)
	}
	r.CommitURL = rec.CommitURLs[r.Commit]
	r.Base = &submission.Base{Commit: r.Base.Commit, Branch: r.Base.Branch, CommitURL: rec.CommitURLs[r.Base.Commit]}
	r.Commits = slices.Clone(r.Commits)
	for i, c := range r.Commits {
		r.Commits[i].CommitURL = rec.CommitURLs[c.Commit]
	}
	if !bytes.Equal(r.Draft, placeholder) {
		return r, nil
	}
	if rec.Failed == nil {
		if rec.Draft != nil {
			draft, err := json.Marshal(rec.Draft)
			if err != nil {
				return r, err
			}
			r.Draft = draft
		}
		return r, nil
	}
	exit := 0
	if rec.Failed.Exit != nil {
		exit = *rec.Failed.Exit
	}
	switch {
	case rec.Failed.Reason == contracts.SubmitProgramTimeout:
		fmt.Fprintf(w, textProgramTimeout+"\n", p.limitOrDefault())
	case rec.Failed.Reason == contracts.SubmitLoginNeeded:
		// The program printed the login command as the first line of its standard error, which
		// the step has printed above. The branch stays pushed.
	case end.startErr != "":
		fmt.Fprintf(w, textProgramNoStart+"\n", shownText(end.startErr))
	case end.exit == contracts.SubmitExitDone:
		fmt.Fprintf(w, textProgramNoDraft+"\n", end.exit)
	default:
		fmt.Fprintf(w, textProgramFailed+"\n", end.exit)
	}
	r.Draft = submission.None(rec.Failed.Reason, exit)
	return r, nil
}

// input is the inputs for repository r of document, with its description in a file in dir.
// The draft is none for a repository whose draft is not the [placeholder], as after a session
// that did not pass. A run with no run code has no earlier submission, so a draft is never an
// update.
func (p *submitProgram) input(document *submission.Submission, r submission.Repository, commits []string, dir string) (submitInput, error) {
	in := submitInput{
		System: r.System, Path: r.Path, Branch: document.Ref, BaseBranch: r.Base.Branch, BaseCommit: r.Base.Commit,
		Commits: commits, Draft: contracts.SubmitDraftNone,
	}
	if !bytes.Equal(r.Draft, placeholder) || document.Text == nil {
		return in, nil
	}
	in.Draft, in.Title = contracts.DraftOpen, document.Text.Title
	file := filepath.Join(dir, "description.md")
	if err := os.WriteFile(file, []byte(document.Text.Description), 0o600); err != nil {
		return in, err
	}
	in.DescriptionFile = file
	return in, nil
}

// run starts the program in dir, outside the wall, with the person's environment and the
// inputs, and closed standard input, and returns how it ended.
func (p *submitProgram) run(ctx context.Context, in submitInput, dir string) programEnd {
	ctx, cancel := context.WithTimeout(ctx, p.limitOrDefault())
	defer cancel()
	cmd := exec.CommandContext(ctx, p.command[0], p.command[1:]...)
	cmd.Dir = dir
	cmd.Env = in.env(os.Environ())
	stdout, stderr := &capped{limit: maxProgramOutput}, &capped{limit: maxProgramOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	inOwnGroup(cmd)
	err := cmd.Run()
	killGroup(cmd)
	end := programEnd{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0:
		// The program ended with 0 and a helper kept its output open for [exec.Cmd.WaitDelay]: the
		// output that it wrote is whole, so the end is normal.
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		end.timedOut = true
	case errors.As(err, &exit):
		// A program that a signal ended has the exit status -1.
		end.exit = exit.ExitCode()
	default:
		end.startErr = err.Error()
	}
	return end
}

// limitOrDefault is how long the program may run.
func (p *submitProgram) limitOrDefault() time.Duration {
	switch {
	case p.limit != 0:
		return p.limit
	case submitLimit != 0:
		return submitLimit
	}
	return programLimit
}

// capped keeps at most limit bytes of what it is written and drops the rest, so a program that
// prints without end does not fill the memory; it never fails a write.
type capped struct {
	limit int
	buf   bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// printProgramLines writes the lines of the program's standard error, masked and cut. At most
// [maxProgramLines] lines go out.
func printProgramLines(w io.Writer, stderr string) {
	printed := 0
	for line := range strings.Lines(stderr) {
		line = shownText(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if printed == maxProgramLines {
			return
		}
		printed++
		fmt.Fprintf(w, textProgramLine+"\n", line)
	}
}

// shownText is a line of the program as a status line shows it: without control characters,
// masked, and at most [maxProgramLine] characters.
func shownText(line string) string {
	line = secretmask.Default().Replace(shownErrorText(strings.TrimRight(line, "\r\n")))
	if runes := []rune(line); len(runes) > maxProgramLine {
		line = string(runes[:maxProgramLine])
	}
	return line
}

// shownErrorText is s as [shownError] shows an error.
func shownErrorText(s string) string { return shownError(errors.New(s)) }

// The reasons for a draft_title line that qory leaves out, as [textProgramLeftOut] lists them.
const (
	reasonTitleChar  = "the title has the character %U, which a title does not have, at character %d"
	reasonTitleBlank = "the title has no character that is not a space, U+200C or U+200D"
)

// withoutUnusableTitle is stdout, the lines of a submit program, with each draft_title line
// blanked out whose title is not one that the submission format stores: the title of the
// contract is a line with a character that shows. It also returns each such line as a line
// that qory cannot use, with its number. A blank line keeps the numbers of the others, and the
// contract's reader then finds the draft incomplete and records program-failed, as for any
// draft line it leaves out. A draft of none has no draft lines to read, so nothing is left
// out. The checks that the reader makes of the title, such as its length, stay with the
// reader. Every line is tested, because the reader uses the first title that passes its
// checks, and that may come after a line that passes qory's test and fails the reader's.
// shortcut: qory tests the title beside the reader, until the repin to T-28 gives the reader
// the rule of the primitives; then delete this function.
func withoutUnusableTitle(stdout, draft string) (string, []contracts.SubmitLineError) {
	if draft == contracts.SubmitDraftNone {
		return stdout, nil
	}
	lines := strings.Split(stdout, "\n")
	var unusable []contracts.SubmitLineError
	for i, line := range lines[:len(lines)-1] {
		key, title, ok := strings.Cut(strings.TrimSuffix(line, "\r"), " ")
		if !ok || title == "" || key != contracts.SubmitKeyDraftTitle || strings.ContainsFunc(title, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			continue
		}
		var reason string
		if at := controlAt(title); at > 0 {
			reason = fmt.Sprintf(reasonTitleChar, []rune(title)[at-1], at)
		} else if !visibleTitle(title) {
			reason = reasonTitleBlank
		} else {
			continue
		}
		lines[i] = ""
		unusable = append(unusable, contracts.SubmitLineError{Line: i + 1, Reason: reason})
	}
	return strings.Join(lines, "\n"), unusable
}
