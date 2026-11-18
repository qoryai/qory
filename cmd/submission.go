package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/submission"
)

// handoffEnv is the variable that gives a session the path of its hand-off file, and
// handoffMax the largest hand-off file qory reads, in bytes.
const (
	handoffEnv = "QORY_SUBMISSION_HANDOFF"
	handoffMax = 256 << 10
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
}

// handoffPath is the hand-off file of a run in the checkout at root: .qory/handoff/submission.json.
func handoffPath(root string) string {
	return filepath.Join(root, ".qory", "handoff", "submission.json")
}

// readHandoff reads the hand-off file at path. A path that is "" or a file that is not there
// is no hand-off, nil with no error; a file that is not JSON in the form of [handoff], or
// is over [handoffMax], is an error that the caller prints before it goes on without it.
func readHandoff(path string) (*handoff, error) {
	if path == "" {
		return nil, nil
	}
	// The agent wrote the path: a link or another kind of file is not read, since a link
	// could lead to a file of the host.
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the path is not a regular file")
	}
	if info.Size() > handoffMax {
		return nil, fmt.Errorf("the file has %d bytes, and qory reads at most %d", info.Size(), handoffMax)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var h handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
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
func noDraftReason(verdict string, text *submission.Text, program bool) string {
	switch {
	case verdict == submission.VerdictEscalate:
		return submission.NoneEscalated
	case verdict != submission.VerdictPassed:
		return submission.NoneNotPassed
	case text == nil:
		return submission.NoneNoText
	}
	// The program that opens the draft is not run yet: the draft stays none.
	_ = program
	return submission.NoneNoProgram
}

// assembleSubmission is the document that the submit step stores for the repositories of a
// push: the branch, the verdict, the hand-off's text, and for each repository its commit,
// the commit it started at and each commit of the run with its step from the hand-off, and
// its draft as [noDraftReason] has it. repositories are the entries of
// [preparation.WorkBranch.Version]: a repository that this run did not push has its commit
// alone.
func assembleSubmission(branch *preparation.WorkBranch, repositories []submission.Repository, verdict string, h *handoff, program bool) *submission.Submission {
	text := h.text()
	doc := &submission.Submission{Ref: branch.Ref, Verdict: verdict, Text: text}
	reason := noDraftReason(verdict, text, program)
	for _, r := range repositories {
		entry := submission.Repository{System: r.System, Path: r.Path, Commit: r.Commit, Base: r.Base, Draft: submission.None(reason, 0)}
		for _, c := range r.Commits {
			entry.Commits = append(entry.Commits, submission.Commit{Commit: c.Commit, Step: h.stepOf(c.Commit)})
		}
		doc.Repositories = append(doc.Repositories, entry)
	}
	return doc
}
