package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/submission"
)

// handoffEnv is the variable that gives a session the path of its hand-off file, and
// handoffMax the largest hand-off file qory reads, in bytes.
const (
	handoffEnv = "QORY_SUBMISSION_HANDOFF"
	handoffMax = 256 << 10
)

// The limits that the submission format sets on what a hand-off gives: the title's
// characters, the description's, and the step's. A hand-off part over a limit is dropped,
// since the stored version would fail the check of the format after the push.
const (
	maxTitle       = 256
	maxDescription = 20000
	maxStep        = 128
)

// stepUnreviewed is the ledger step of a commit that the hand-off gives no step: the commit
// of the edits that the time limit left, and any commit the session did not list.
const stepUnreviewed = "unreviewed"

// handoff is what a session hands to the submit step in the file at [handoffEnv]: the title
// and the description of the draft pull request, and the ledger step that made each commit,
// keyed by the full commit id. qory is the only writer of the submission: it takes nothing
// else from the file.
type handoff struct {
	Text  *submission.Text  `json:"text"`
	Steps map[string]string `json:"steps"`
	// dropped is one line for each part that [readHandoff] left out because the format does
	// not allow it, with its values; nil for none.
	dropped []string
}

// handoffRel is the hand-off file of a run, in the checkout.
var handoffRel = filepath.Join(".qory", "handoff", "submission.json")

// handoffExclude is the line of the checkout's .git/info/exclude that keeps the hand-off
// folder out of a commit: the file is in the checkout, and only qory reads it.
const handoffExclude = "/.qory/handoff/"

// handoffPath is the hand-off file of a run in the checkout at root: .qory/handoff/submission.json.
func handoffPath(root string) string { return filepath.Join(root, handoffRel) }

// readHandoff reads the hand-off file of the checkout at root. A root that is "" or a file
// that is not there is no hand-off, nil with no error; a file that is not JSON in the form of
// [handoff], or is over [handoffMax], is an error that the caller prints before it goes on
// without it. The agent wrote the path, so the read stays inside root, whatever links the
// agent made: a link that leads out of the checkout, at any part of the path, is an error, and
// so is a link or another kind of file as the file. A title, a description or a step over a
// limit of the format is dropped and listed in [handoff.dropped].
func readHandoff(root string) (*handoff, error) {
	if root == "" {
		return nil, nil
	}
	checkout, err := os.OpenRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer checkout.Close()
	info, err := checkout.Lstat(handoffRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the path is not a regular file")
	}
	// The path may be swapped for a pipe after the check: the open does not wait for a writer,
	// and the opened file is checked again.
	f, err := checkout.OpenFile(handoffRel, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the path is not a regular file")
	}
	// The file may be swapped for a larger one after the check: the read stops at the limit.
	data, err := io.ReadAll(io.LimitReader(f, handoffMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > handoffMax {
		return nil, fmt.Errorf("the file has more than %d bytes, and qory reads at most %d", handoffMax, handoffMax)
	}
	var h handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, errors.New(jsonCause(err))
	}
	h.limit()
	return &h, nil
}

// The lines that [handoff.limit] lists for a part it dropped, each with its values; the
// caller indents them under [textHandoffDropped]. maxDroppedSteps is how many dropped steps
// get a line each.
const (
	textDroppedTitle       = "Title: %d characters, over the limit of %d. qory dropped the text."
	textDroppedDescription = "Description: %d characters, over the limit of %d. qory dropped the text."
	textDroppedBlank       = "Title: has no character that is not a space, U+200C or U+200D. qory dropped the text."
	textDroppedControl     = "Title: has a control character at character %d. qory dropped the text."
	textDroppedNUL         = "Description: has a NUL character at character %d. qory dropped the text."
	textDroppedMarker      = "Description: line %d is a marker line of the draft's block. qory dropped the text."
	textDroppedStepControl = "Step of commit %s: has a control, invisible, direction or separator character. qory dropped the step."
	textDroppedFurtherCtl  = "%d further steps have a control, invisible, direction or separator character. qory dropped them."
	textDroppedKeys        = "%d step keys are not a full commit id of 40 or 64 lower-case hexadecimal digits. qory dropped them."
	textDroppedStep        = "Step of commit %s: %d characters, over the limit of %d. qory dropped the step."
	textDroppedFurther     = "%d further steps are over the limit of %d characters. qory dropped them."
	maxDroppedSteps        = 5
)

// controlAt is the 1-based position of the first character of title that [submission.TitleRune]
// does not pass (a control, a format character other than U+200C and U+200D, or U+2028 or
// U+2029); 0 for none.
func controlAt(title string) int {
	i := 0
	for _, r := range title {
		i++
		if !submission.TitleRune(r) {
			return i
		}
	}
	return 0
}

// readHandoffPrinting is [readHandoff] of root, with the lines that tell a person what was not
// used: the cause when the file is not usable, and each part of it that the format does not
// allow. It returns nil for no hand-off.
func readHandoffPrinting(w io.Writer, root string) *handoff {
	given, err := readHandoff(root)
	if err != nil {
		fmt.Fprintf(w, textHandoffIgnored+"\n", filepath.ToSlash(handoffRel), shownError(err))
	}
	if given != nil && len(given.dropped) > 0 {
		fmt.Fprintln(w, textHandoffDropped)
		for _, line := range given.dropped {
			fmt.Fprintln(w, "  "+line)
		}
	}
	return given
}

// jsonCause is the cause of an error of the JSON reader for the file of the agent, with no
// content of the file: the reader's text can have the first character that it did not
// expect, or a member's name.
func jsonCause(err error) string {
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("the file is not JSON: syntax error at byte %d", syntax.Offset)
	case errors.As(err, &typed) && typed.Field != "":
		member, _, _ := strings.Cut(typed.Field, ".")
		if member == "text" || member == "steps" {
			return fmt.Sprintf("the member %s of the file has the wrong type", member)
		}
	}
	return "the file is not JSON of the form of the hand-off"
}

// limit drops the parts of h that are over a limit of the submission format.
func (h *handoff) limit() {
	if h.Text != nil {
		switch title, description := utf8.RuneCountInString(h.Text.Title), utf8.RuneCountInString(h.Text.Description); {
		case controlAt(h.Text.Title) > 0:
			at := controlAt(h.Text.Title)
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedControl, at))
		case title > 0 && !visibleTitle(h.Text.Title):
			h.Text = nil
			h.dropped = append(h.dropped, textDroppedBlank)
		case title > maxTitle:
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedTitle, title, maxTitle))
		case description > maxDescription:
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedDescription, description, maxDescription))
		case nulAt(h.Text.Description) > 0:
			at := nulAt(h.Text.Description)
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedNUL, at))
		case markerLineAt(h.Text.Description) > 0:
			at := markerLineAt(h.Text.Description)
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedMarker, at))
		}
	}
	badKeys := 0
	for commit := range h.Steps {
		if !submission.CommitID(commit) {
			delete(h.Steps, commit)
			badKeys++
		}
	}
	if badKeys > 0 {
		h.dropped = append(h.dropped, fmt.Sprintf(textDroppedKeys, badKeys))
	}
	var controlled []string
	for commit, step := range h.Steps {
		if strings.ContainsFunc(step, notTitleRune) {
			controlled = append(controlled, commit)
		}
	}
	slices.Sort(controlled)
	for i, commit := range controlled {
		delete(h.Steps, commit)
		if i < maxDroppedSteps {
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedStepControl, shownCommit(commit)))
		}
	}
	if len(controlled) > maxDroppedSteps {
		h.dropped = append(h.dropped, fmt.Sprintf(textDroppedFurtherCtl, len(controlled)-maxDroppedSteps))
	}
	var over []string
	for commit, step := range h.Steps {
		if utf8.RuneCountInString(step) > maxStep {
			over = append(over, commit)
		}
	}
	slices.Sort(over)
	for i, commit := range over {
		step := h.Steps[commit]
		delete(h.Steps, commit)
		if i < maxDroppedSteps {
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedStep, shownCommit(commit), utf8.RuneCountInString(step), maxStep))
		}
	}
	if len(over) > maxDroppedSteps {
		h.dropped = append(h.dropped, fmt.Sprintf(textDroppedFurther, len(over)-maxDroppedSteps, maxStep))
	}
}

// notTitleRune reports whether r is a character that [submission.TitleRune] does not pass.
func notTitleRune(r rune) bool { return !submission.TitleRune(r) }

// visibleTitle reports whether title has a character that shows: not a space, U+200C or U+200D.
// The spaces are the ones that the contract's draft_title rule lists (U+0020, U+00A0, U+1680,
// U+2000 to U+200A, U+202F, U+205F and U+3000), which are the characters that [unicode.IsSpace]
// passes once controls and U+2028 and U+2029 are out. U+200C and U+200D pass [submission.TitleRune]
// but show nothing alone. The pinned contract still accepts a title of only these, see
// TestTheHandoffTitleRuleAgreesWithTheContractOnTheseTitles.
func visibleTitle(title string) bool {
	return strings.ContainsFunc(title, func(r rune) bool { return !unicode.IsSpace(r) && r != '\u200c' && r != '\u200d' })
}

// shownCommit is commit as a line shows it: printable, masked and at most 64 characters.
func shownCommit(commit string) string {
	shown := []rune(shownText(commit))
	if len(shown) > 64 {
		shown = shown[:64]
	}
	return string(shown)
}

// nulAt is the 1-based position of the first NUL character of text, 0 for none: a description
// with one is not text that a forge takes.
func nulAt(text string) int {
	i := 0
	for _, r := range text {
		i++
		if r == 0 {
			return i
		}
	}
	return 0
}

// markerLineAt is the 1-based number of the first line of description that is a marker line of the
// block that the submit program writes, 0 for none: `<!-- qory:start -->` or `<!-- qory:end -->`
// followed by nothing but spaces, tabs or a carriage return. A description with one would end or
// start the block early on the draft.
func markerLineAt(description string) int {
	n := 0
	for line := range strings.Lines(description) {
		n++
		switch strings.TrimRight(strings.TrimSuffix(line, "\n"), " \t\r") {
		case "<!-- qory:start -->", "<!-- qory:end -->":
			return n
		}
	}
	return 0
}

// stepOf is the ledger step of commit: the hand-off's, else [stepUnreviewed].
func (h *handoff) stepOf(commit string) string {
	if h != nil {
		if step := h.Steps[commit]; step != "" {
			return step
		}
	}
	return stepUnreviewed
}

// text is the hand-off's text for the draft, nil when there is none or it is not whole.
func (h *handoff) text() *submission.Text {
	if h == nil || h.Text == nil || h.Text.Title == "" {
		return nil
	}
	return h.Text
}

// newestVerdict is the verdict of the work item as the submission names it, from the run
// API: the newest verdict, when this run wrote it, and none otherwise. A verdict that the
// submission format does not name is none.
func newestVerdict(ctx context.Context, client *runapi.Client, runID string) (string, error) {
	snapshot, err := client.WorkItem(ctx)
	if err != nil {
		return "", err
	}
	if snapshot.Verdict == nil || snapshot.Verdict.Version == nil {
		return submission.VerdictNone, nil
	}
	index, err := client.Artifacts(ctx)
	if err != nil {
		return "", err
	}
	for _, artifact := range index.Artifacts {
		for _, v := range artifact.Versions {
			if v.ID == snapshot.Verdict.Version.ID && v.Writer.Kind == "run" && v.Writer.RunID == runID {
				if slices.Contains([]string{submission.VerdictPassed, submission.VerdictChangesRequested, submission.VerdictEscalate}, snapshot.Verdict.Verdict) {
					return snapshot.Verdict.Verdict, nil
				}
			}
		}
	}
	return submission.VerdictNone, nil
}

// noDraftReason is why a repository has no draft before the submit program runs: the
// verdict is not passed, the session wrote no text, or the harness has no submit program.
func noDraftReason(verdict string, text *submission.Text) string {
	switch {
	case verdict == submission.VerdictEscalate:
		return submission.NoneEscalated
	case verdict != submission.VerdictPassed:
		return submission.NoneNotPassed
	case text == nil:
		return submission.NoneNoText
	}
	// The draft is the placeholder that the submit program fills, or that stays when the
	// harness has no program or the program handles no such repository.
	return submission.NoneNoProgram
}

// assembleSubmission is the document that the submit step stores for the repositories of a
// push: the branch, the verdict, the hand-off's text, and for each repository its commit,
// the commit it started at and each commit of the run with its step from the hand-off, and
// its draft as [noDraftReason] has it. repositories are the entries of
// [preparation.WorkBranch.Version]: a repository that this run did not push has its commit
// alone.
func assembleSubmission(branch *preparation.WorkBranch, repositories []submission.Repository, verdict string, h *handoff) *submission.Submission {
	text := h.text()
	doc := &submission.Submission{Ref: branch.Ref, Verdict: verdict, Text: text}
	reason := noDraftReason(verdict, text)
	for _, r := range repositories {
		entry := submission.Repository{System: r.System, Path: r.Path, Commit: r.Commit, Base: r.Base, Draft: submission.None(reason, 0)}
		for _, c := range r.Commits {
			entry.Commits = append(entry.Commits, submission.Commit{Commit: c.Commit, Step: h.stepOf(c.Commit)})
		}
		doc.Repositories = append(doc.Repositories, entry)
	}
	return doc
}
