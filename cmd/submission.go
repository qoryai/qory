package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/render"
)

// handoffMax is the largest pull-request output that the local submit step reads, in bytes.
const handoffMax = 256 << 10

// The limits on what the pull-request output gives the submit program: the title's
// characters, the description's, and the step's. A part over a limit is dropped.
const (
	maxTitle       = 256
	maxDescription = 20000
	maxStep        = 128
)

// stepUnreviewed is the ledger step of a commit that the hand-off gives no step: the commit
// of the edits that the time limit left, and any commit the session did not list.
const stepUnreviewed = "unreviewed"

// handoff is what the local submit step takes from the session's pull-request output: the
// title and the description of the draft pull request, and the ledger step that made each
// commit, keyed by the full commit id. It takes nothing else from the file.
type handoff struct {
	Text  *draftText
	Steps map[string]string
	// dropped is one line for each part that [readHandoff] left out because a draft does
	// not allow it, with its values; nil for none.
	dropped []string
}

// pullRequestType is the one output type that the local submit step reads.
// shortcut: reads one kind by name until the later card.
const pullRequestType = "pull-request"

// handoffRel is the pull-request output of a run, in the copy: [outputPath] of the type.
var handoffRel = filepath.FromSlash(outputPath(pullRequestType, true))

// pullRequestFile is the part of a pull-request output that the local submit step reads:
// title, text and steps. Other members are read and not used.
type pullRequestFile struct {
	Title *string `json:"title,case:strict"`
	Text  *string `json:"text,case:strict"`
	Steps []struct {
		Commit string `json:"commit,case:strict"`
		Step   string `json:"step,case:strict"`
	} `json:"steps,case:strict"`
}

// The words of the local submit step on the pull-request output: line 1 is the fix, then the
// file and the cause, or the parts that it dropped.
const (
	textHandoffIgnored = "qory run: Fix the pull-request output: qory cannot read it, so the draft has no text.\n  File: %s.\n  Cause: %s"
	textHandoffDropped = "qory run: Fix the pull-request output: qory dropped a part that a draft does not allow."
)

// readHandoff reads the pull-request output of the copy at root. A root that is "" or a file
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
	var pr pullRequestFile
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, errors.New(jsonCause(err))
	}
	h := handoff{Steps: map[string]string{}}
	if pr.Title != nil {
		h.Text = &draftText{Title: *pr.Title}
		if pr.Text != nil {
			h.Text.Description = *pr.Text
		}
	}
	for _, s := range pr.Steps {
		h.Steps[s.Commit] = s.Step
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
	textDroppedChar        = "Remove the character %U at character %d of the description. qory dropped the text."
	textDroppedStepControl = "Step of commit %s: has a control, invisible, direction or separator character. qory dropped the step."
	textDroppedFurtherCtl  = "%d further steps have a control, invisible, direction or separator character. qory dropped them."
	textDroppedKeys        = "%d step keys are not a full commit id of 40 or 64 lower-case hexadecimal digits. qory dropped them."
	textDroppedStep        = "Step of commit %s: %d characters, over the limit of %d. qory dropped the step."
	textDroppedFurther     = "%d further steps are over the limit of %d characters. qory dropped them."
	maxDroppedSteps        = 5
)

// controlAt is the 1-based position of the first character of title that [primitives.StopsLine]
// stops (a control, a format character other than U+200C and U+200D, or U+2028 or
// U+2029); 0 for none.
func controlAt(title string) int {
	i := 0
	for _, r := range title {
		i++
		if primitives.StopsLine(r) {
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
	return "the file is not JSON of the form of the pull-request output"
}

// limit drops the parts of h that are over a limit of a draft.
func (h *handoff) limit() {
	if h.Text != nil {
		switch title, description := utf8.RuneCountInString(h.Text.Title), utf8.RuneCountInString(h.Text.Description); {
		case controlAt(h.Text.Title) > 0:
			at := controlAt(h.Text.Title)
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedControl, at))
		case title > 0 && !strings.ContainsFunc(h.Text.Title, primitives.ShowsInTitle):
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
		case stoppedAt(h.Text.Description) > 0:
			at := stoppedAt(h.Text.Description)
			r := []rune(h.Text.Description)[at-1]
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedChar, r, at))
		case markerLineAt(h.Text.Description) > 0:
			at := markerLineAt(h.Text.Description)
			h.Text = nil
			h.dropped = append(h.dropped, fmt.Sprintf(textDroppedMarker, at))
		}
	}
	badKeys := 0
	for commit := range h.Steps {
		if !lowerCommitID(commit) {
			delete(h.Steps, commit)
			badKeys++
		}
	}
	if badKeys > 0 {
		h.dropped = append(h.dropped, fmt.Sprintf(textDroppedKeys, badKeys))
	}
	var controlled []string
	for commit, step := range h.Steps {
		if strings.ContainsFunc(step, primitives.StopsLine) {
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

// stoppedAt is the 1-based position of the first character of description that the text
// rule of the contract stops, 0 for none: a control character other than tab, line feed
// and carriage return, U+2028, U+2029, or a Cf character other than U+200C and U+200D.
// The description is Markdown, so the rule is [primitives.StopsText], not the rule of a
// title.
func stoppedAt(description string) int {
	i := 0
	for _, r := range description {
		i++
		if primitives.StopsText(r) {
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
func (h *handoff) text() *draftText {
	if h == nil || h.Text == nil || h.Text.Title == "" {
		return nil
	}
	return h.Text
}

// noDraftReason is why a repository has no draft before the submit program runs: the
// verdict is not passed, the session wrote no text, or the harness has no submit program.
func noDraftReason(verdict string, text *draftText) string {
	switch {
	case verdict != verdictPassed:
		return noneNotPassed
	case text == nil:
		return noneNoText
	}
	// The draft is the placeholder that the submit program fills, or that stays when the
	// harness has no program or the program handles no such repository.
	return noneNoProgram
}
