package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/submission"
)

// The words of the submit program's run. Each starts with the fix. textProgramLine is a
// line of the program's output, masked.
const (
	textProgramLine     = "qory run: submit program: %s"
	textProgramFailed   = "qory run: Read the lines of the submit program above, then run the round again: it exited with %d, so a repository it did not finish has no draft (program-failed)."
	textProgramTimeout  = "qory run: Run the round again: the submit program ran past %s, so a repository it did not finish has no draft (program-timeout)."
	textProgramNoStart  = "qory run: Check the submit program: it did not start (%s), so a repository has no draft (program-failed)."
	textProgramNotReady = "qory run: Check the wall for the submit program: %s, so a repository has no draft (program-failed)."
	textProgramDiscards = "qory run: Check the submit program: %s, so qory kept the submission it gave the program and a repository has no draft (program-failed)."
	textProgramNoWall   = "set wall.adapter to docker, because the submit program runs in a container of the docker wall alone"
	textProgramNoImage  = "set wall.image in forager.yaml, because the submit program runs in the wall's default image"
	textProgramBadImage = "set wall.image or --image to an image reference, because the submit program cannot start from %s"
	textProgramEngine   = "switch the engine back to the one the run started on, because the submit program would run on the engine %s and the run's containers are on the engine %s"
)

// submitLimit, when not 0, is how long the submit program may run, in place of
// [pushcheck.ProgramLimit]; a test sets it.
var submitLimit time.Duration

// maxProgramLines is how many lines of the program's output the step prints, and
// maxProgramLine the longest one.
const (
	maxProgramLines = 20
	maxProgramLine  = 300
)

// submitProgram is the submit program of a run: the module's command, with the home's path in
// it, and what starts its container. The container is built when the program runs, since the
// image of a run with no default image comes in the run's image step, after the compose.
type submitProgram struct {
	command  []string
	home     string
	run      openedRun
	spec     *runSpec
	fromCode bool
	// limit, when set, replaces [pushcheck.ProgramLimit]; a test sets it.
	limit time.Duration
}

// checker is the container's checker, as the run's push checks have one: the wall's default
// image, as the run's user, with the wall's limits and the run's label. An error starts with
// its fix and has no final period.
func (p *submitProgram) checker(ctx context.Context) (*pushcheck.Checker, error) {
	d, ok := p.spec.Wall.(*wall.Docker)
	if !ok {
		return nil, errors.New(textProgramNoWall)
	}
	engine, err := programEngine(ctx, d)
	if err != nil {
		return nil, err
	}
	user := d.User
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	// A run from a run code runs the program in the wall's default image alone: its policy,
	// from the server, may select an image built from the repository.
	if p.fromCode && p.spec.Image == "" {
		return nil, errors.New(textProgramNoImage)
	}
	image := checkImage(p.run, p.spec)
	if !config.IsImageRef(image) {
		return nil, fmt.Errorf(textProgramBadImage, exports.Value(image, true))
	}
	return &pushcheck.Checker{
		Home: p.home, Image: image, Runtime: checkRuntime(p.run, p.spec), User: user, Engine: engine,
		Limits: wall.Limits{CPUs: p.spec.Limits.CPUs, Memory: p.spec.Limits.Memory, PIDs: p.spec.Limits.PIDs},
		Label:  runLabel + p.spec.RunID, Mask: secretmask.Default().Replace, ProgramLimit: p.limit,
	}, nil
}

// programEngine is the engine of the program's container: d's command with the selection
// that the wall recorded at the run's open. A new selection that reaches another engine than
// the recorded one is an error: the program would run on another engine than the run's
// containers.
func programEngine(ctx context.Context, d *wall.Docker) (pushcheck.Engine, error) {
	ctx, cancel := context.WithTimeout(ctx, engineSelectWait)
	defer cancel()
	e := pushcheck.Engine{Command: d.Command}
	id, err := d.EngineID(ctx)
	if err != nil || id == "" {
		return e, nil
	}
	selected := (&wall.Docker{Command: d.Command}).Engine(ctx)
	if selected.ID != id {
		return pushcheck.Engine{}, fmt.Errorf(textProgramEngine, engineShown(selected.ID), engineShown(id))
	}
	return pushcheck.Engine{Command: selected.Command, Env: append([]string{}, selected.Env...)}, nil
}

// apply runs the program on document through proxy, the run's push proxy, which stays open
// until it returns, and returns the document to store: what the program left, when it
// changed only what it may, and otherwise document. A repository whose draft the program did
// not fill gets the reason that the program's end gives. Each line it prints goes to w. An error is a
// ctx that ended, which cuts the step.
func (p *submitProgram) apply(ctx context.Context, w io.Writer, proxy proxygit.Proxy, document *submission.Submission) (*submission.Submission, error) {
	content, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	// The program sends the password as the user qory in a Basic header: both forms are secret.
	if u, err := url.Parse(proxy.URL); err == nil && u.User != nil {
		if password, ok := u.User.Password(); ok {
			secretmask.Default().Add(base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + password)))
		}
	}
	var end pushcheck.ProgramEnd
	checker, err := p.checker(ctx)
	if err == nil {
		end, err = checker.RunProgram(ctx, pushcheck.Program{Command: p.command, Submission: content, ProxyURL: proxy.URL, CABundle: proxy.CABundle})
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		fmt.Fprintf(w, textProgramNotReady+"\n", shownError(err))
		return withDraftNone(document, submission.NoneProgramFailed, 1), nil
	}
	printProgramLines(w, end)
	result, why := afterProgram(document, end)
	switch {
	case end.TimedOut:
		fmt.Fprintf(w, textProgramTimeout+"\n", p.programLimit())
	case end.StartErr != "":
		fmt.Fprintf(w, textProgramNoStart+"\n", shownText(end.StartErr))
	case end.Exit != 0:
		fmt.Fprintf(w, textProgramFailed+"\n", end.Exit)
	}
	if why != "" {
		fmt.Fprintf(w, textProgramDiscards+"\n", why)
	}
	return result, nil
}

// programLimit is how long the program may run.
func (p *submitProgram) programLimit() time.Duration {
	if p.limit != 0 {
		return p.limit
	}
	return pushcheck.ProgramLimit
}

// printProgramLines writes the lines of the program's output, masked and cut: its standard
// output first, which has the pull requests it opened, then its standard error, which has the
// fixes. At most [maxProgramLines] lines go out.
func printProgramLines(w io.Writer, end pushcheck.ProgramEnd) {
	printed := 0
	for _, out := range [][]byte{end.Stdout, end.Stderr} {
		for line := range strings.Lines(string(out)) {
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

// placeholder is the draft of a repository that the program may fill: the verdict is passed,
// the session wrote text, and no program has run yet.
var placeholder = submission.None(submission.NoneNoProgram, 0)

// afterProgram is the document that the step stores after the program ended as end: the file
// the program left when it changed only what it may, otherwise document; with, for a
// repository whose draft is still the [placeholder], the reason that end gives. A program that
// exited 0 leaves a placeholder as it is: no program handles that repository. why is the cause
// when the program's file is not used, "" for none.
func afterProgram(document *submission.Submission, end pushcheck.ProgramEnd) (result *submission.Submission, why string) {
	reason, exit := "", 0
	switch {
	case end.TimedOut:
		reason = submission.NoneProgramTimeout
	case end.StartErr != "" || end.Exit != 0:
		reason, exit = submission.NoneProgramFailed, end.Exit
		if exit != 1 && exit != 2 {
			exit = 1
		}
	}
	left, err := programChanges(document, end.Submission, end.FileErr)
	if err != nil {
		left, why = document, err.Error()
		if reason == "" {
			reason, exit = submission.NoneProgramFailed, 1
		}
	}
	if reason == "" {
		return left, why
	}
	return withDraftNone(left, reason, exit), why
}

// withDraftNone is document with the draft {"none": reason} in each repository whose draft is
// the [placeholder].
func withDraftNone(document *submission.Submission, reason string, exit int) *submission.Submission {
	out := *document
	out.Repositories = slices.Clone(document.Repositories)
	for i, r := range out.Repositories {
		if bytes.Equal(r.Draft, placeholder) {
			out.Repositories[i].Draft = submission.None(reason, exit)
		}
	}
	return &out
}

// programChanges reads the file that the program left and checks that it differs from
// document only in the commit links of each repository (commit_url, base.commit_url and each
// commits[].commit_url), and in the draft of a repository whose draft is the [placeholder]:
// there it may be a pull request, never another reason. fileErr is the cause when the file
// could not be read.
func programChanges(document *submission.Submission, left []byte, fileErr string) (*submission.Submission, error) {
	if left == nil {
		return nil, fmt.Errorf("it left no file the step can read: %s", fileErr)
	}
	var got submission.Submission
	dec := json.NewDecoder(bytes.NewReader(left))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		return nil, fmt.Errorf("it left a file that is not a submission: %s", shownError(err))
	}
	if err := submission.Check(left); err != nil {
		return nil, fmt.Errorf("it left a file that is not a submission: %s", shownError(err))
	}
	if got.Ref != document.Ref || got.Verdict != document.Verdict || !reflect.DeepEqual(got.Text, document.Text) ||
		len(got.Repositories) != len(document.Repositories) {
		return nil, errors.New("it changed the ref, the verdict, the text or the list of repositories")
	}
	for i, was := range document.Repositories {
		now := &got.Repositories[i]
		if now.System != was.System || now.Path != was.Path || now.Commit != was.Commit {
			return nil, fmt.Errorf("it changed the system, the path or the commit of repository %d", i+1)
		}
		if (now.Base == nil) != (was.Base == nil) || was.Base != nil && now.Base.Commit != was.Base.Commit {
			return nil, fmt.Errorf("it changed the base of repository %d", i+1)
		}
		if len(now.Commits) != len(was.Commits) {
			return nil, fmt.Errorf("it changed the commits of repository %d", i+1)
		}
		for j, c := range was.Commits {
			if now.Commits[j].Commit != c.Commit || now.Commits[j].Step != c.Step {
				return nil, fmt.Errorf("it changed the commits of repository %d", i+1)
			}
		}
		draft, err := draftAfterProgram(was.Draft, now.Draft)
		if err != nil {
			return nil, fmt.Errorf("%w of repository %d", err, i+1)
		}
		now.Draft = draft
	}
	return &got, nil
}

// draftAfterProgram is the draft the program left, compact, when it may stand: the draft that
// was there, or, in place of the [placeholder], a pull request. Any other change is an error.
func draftAfterProgram(was, now json.RawMessage) (json.RawMessage, error) {
	var a, b any
	if json.Unmarshal(was, &a) != nil || json.Unmarshal(now, &b) != nil {
		return nil, errors.New("it left a draft that is not JSON")
	}
	if reflect.DeepEqual(a, b) {
		return was, nil
	}
	fields, ok := b.(map[string]any)
	if !bytes.Equal(was, placeholder) || !ok || fields["none"] != nil {
		return nil, errors.New("it changed the draft")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, now); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}
