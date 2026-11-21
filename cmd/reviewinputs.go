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
// named as the members of qoryInputs are: lower-case words joined by underscores. Q-36
// adds qory.start_commits and qory.start_artifacts for a Build session, and Q-71 adds
// qory.review.outputs; this file defines neither. The review-changes session has
// qory.review_session and no qory.review, because the harness's tool guard treats a
// session with qory.review as one reviewer, and this session starts several.

// reviewSession is qory.review_session, written into the inputs.json of the
// review-changes session. Its reviewer agents read the commit and the diff here, not in
// the session's prompt, so the session cannot point them at another commit.
type reviewSession struct {
	// Commit is the full id of the pushed head of the fresh copy that the reviewers read.
	Commit string `json:"commit"`
	// Diff is the absolute path of the diff of the change, from the base to Commit.
	Diff string `json:"diff"`
	// Reports is the absolute path of the folder where each reviewer agent of the session
	// writes its report, as <agent>.json.
	Reports string `json:"reports"`
	// SuiteResult is the absolute path of the suite result file, see suite.WriteResult.
	SuiteResult string `json:"suite_result"`
}

// reviewStartArtifactTypes are the types of the work item's artifacts that qory pulls
// into the start_artifacts folder of the review-changes session: the acceptance criteria,
// the spec and the build plan, which acceptance-reviewer reads below <start>/<type>/.
var reviewStartArtifactTypes = []string{"acceptance-criteria", "spec", "build-plan"}

// reviewSessionInputs is the members of qory that the inputs.json of the review-changes
// session has beside the ones every session has.
type reviewSessionInputs struct {
	ReviewSession  reviewSession `json:"review_session"`
	StartArtifacts string        `json:"start_artifacts"`
}

// commitForm is a full commit id: 40 hexadecimal digits for SHA-1, 64 for SHA-256.
var commitForm = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// newReviewSessionInputs are the members of qory for the review-changes session, with
// commit, the full id of the fresh copy's pushed head, diff, the path of its diff,
// reports, the folder where the reviewer agents write their reports, suiteResult, the
// path of the suite result file, and startArtifacts, the path of the folder of
// [reviewStartArtifactTypes]. It returns an error that starts with the fix for a reports
// folder that the harness's tool guard does not accept as a place to write, and for a
// path of a file or folder to read that is not absolute and clean, and for a commit that
// is not a full commit id.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newReviewSessionInputs(commit, diff, reports, suiteResult, startArtifacts string) (reviewSessionInputs, error) {
	if !commitForm.MatchString(commit) {
		return reviewSessionInputs{}, fmt.Errorf("Give the session the full id of the fresh copy's head in qory.review_session.commit.\nThe value is %s.", strconv.Quote(commit))
	}
	if why := reviewPathProblem(reports); why != "" {
		return reviewSessionInputs{}, fmt.Errorf("Give the session an absolute path in qory.review_session.reports that the harness accepts.\nThe path %s %s.", strconv.Quote(reports), why)
	}
	for _, p := range []struct{ what, path string }{{"review_session.diff", diff}, {"review_session.suite_result", suiteResult}, {"start_artifacts", startArtifacts}} {
		if !cleanAbs(p.path) {
			return reviewSessionInputs{}, fmt.Errorf("Give the session an absolute, clean path in qory.%s.\nThe path is %s.", p.what, strconv.Quote(p.path))
		}
	}
	return reviewSessionInputs{ReviewSession: reviewSession{Commit: commit, Diff: diff, Reports: reports, SuiteResult: suiteResult}, StartArtifacts: startArtifacts}, nil
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
