package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// maxReviewPath is the most characters of a path that the harness's tool guard accepts as
// a path to write, such as qory.review_session.reports.
const maxReviewPath = 4096

// The members of the member qory of inputs.json for the review-changes session. They are
// named as the members of qoryInputs are: lower-case words joined by underscores. Every
// dispatched session has qory.start_commits and qory.start_artifacts, which this file does
// not define. The review-changes session has qory.review_session and no qory.review,
// because the harness's tool guard treats a session with qory.review as one reviewer, and
// this session starts several.

// reviewSession is qory.review_session, written into the inputs.json of the
// review-changes session. Its reviewer agents read the commit and the diff here, not in
// the session's prompt, so the session cannot point them at another commit.
type reviewSession struct {
	// Commit is the full id of the pushed head of the fresh copy that the reviewers read.
	Commit string `json:"commit"`
	// CommitFolder is the absolute path of the read-only copy of the commit, as the session
	// sees it: the folder is mounted at its path on the machine. The session's working
	// folder is empty, so the reviewers read the commit's files here and nowhere else.
	CommitFolder string `json:"commit_folder"`
	// Diff is the absolute path of the diff of the change, from the base to Commit.
	Diff string `json:"diff"`
	// Reports is the absolute path of the folder where each reviewer agent of the session
	// writes its report, as <agent>.json.
	Reports string `json:"reports"`
	// SuiteResult is the absolute path of the suite result file, see suite.WriteResult,
	// left out for a session at write-no-push access, such as verify-a-ticket, which runs
	// no full suite.
	SuiteResult string `json:"suite_result,omitempty"`
	// PreviousFindings is the absolute path of open-findings.json of the round before,
	// left out in every round, since qory keeps no findings of the rounds before.
	PreviousFindings string `json:"previous_findings,omitempty"`
	// BuildReport is the absolute path of the report of the implement-changes session,
	// left out when there is none.
	BuildReport string `json:"build_report,omitempty"`
}

// reviewSessionInputs is the members of qory that the inputs.json of the review-changes
// session has beside the ones every session has. The session reads the work item's
// artifacts in qory.start_artifacts, which every dispatched session has (see
// [startArtifacts]).
type reviewSessionInputs struct {
	ReviewSession reviewSession `json:"review_session"`
}

// commitForm is a full commit id: 40 hexadecimal digits for SHA-1, 64 for SHA-256.
var commitForm = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// newReviewSessionInputs are the members of qory for the review-changes session, with
// commit, the full id of the fresh copy's pushed head, commitFolder, the path of the
// read-only copy of that commit, diff, the path of its diff, and reports, the folder
// where the reviewer agents write their reports. suiteResult, previous and buildReport are
// the paths of the suite result file, of the open findings of the round before and of the
// implement-changes report, "" for none. It returns a [reviewStop] for a reports folder
// that the harness's tool guard does not accept as a place to write, for a path of a file
// or folder to read that is not absolute and clean, and for a commit that is not a full
// commit id.
func newReviewSessionInputs(commit, commitFolder, diff, reports, suiteResult, previous, buildReport string) (reviewSessionInputs, error) {
	if !commitForm.MatchString(commit) {
		return reviewSessionInputs{}, reviewStop("qory.review_session.commit is not a full commit id, so the review did not start",
			fmt.Sprintf("The value is %s", strconv.Quote(commit)))
	}
	if why := reviewPathProblem(reports); why != "" {
		return reviewSessionInputs{}, reviewStop("qory.review_session.reports is a path the harness does not accept, so the review did not start",
			fmt.Sprintf("The path %s %s", strconv.Quote(reports), why))
	}
	for _, p := range []struct {
		what, path string
		optional   bool
	}{
		{"review_session.commit_folder", commitFolder, false}, {"review_session.diff", diff, false},
		{"review_session.suite_result", suiteResult, true}, {"review_session.previous_findings", previous, true},
		{"review_session.build_report", buildReport, true},
	} {
		if !(p.optional && p.path == "") && !cleanAbs(p.path) {
			return reviewSessionInputs{}, reviewStop(fmt.Sprintf("qory.%s is not an absolute, clean path, so the review did not start", p.what),
				fmt.Sprintf("The path is %s", strconv.Quote(p.path)))
		}
	}
	return reviewSessionInputs{ReviewSession: reviewSession{Commit: commit, CommitFolder: commitFolder, Diff: diff, Reports: reports, SuiteResult: suiteResult, PreviousFindings: previous, BuildReport: buildReport}}, nil
}

// cleanAbs reports whether path is absolute and clean, and has no line break or NUL.
func cleanAbs(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\n\r\x00")
}

// reviewPathProblem is why the harness's tool guard does not accept path as a place to
// write, such as qory.review_session.reports, as the end of a sentence about the path
// ("is not absolute"), and "" for a path it accepts: an absolute path with no . or ..
// part, no double or trailing slash, no NUL, of at most 4096 characters, and no .git or
// .claude part. Whether links lie on the path is for the folder's maker.
func reviewPathProblem(path string) string {
	switch {
	case !strings.HasPrefix(path, "/"):
		return "is not absolute"
	case strings.ContainsRune(path, 0):
		return "has a NUL"
	case len(path) > maxReviewPath:
		return "has more than " + strconv.Itoa(maxReviewPath) + " characters"
	case strings.Contains(path, "//") || strings.HasSuffix(path, "/"):
		return "has a double or trailing slash"
	}
	for _, part := range strings.Split(path[1:], "/") {
		switch part {
		case ".", "..":
			return "has a " + part + " part"
		case ".git", ".claude":
			return "has a " + part + " part, where the harness's tool guard denies a write"
		}
	}
	return ""
}
