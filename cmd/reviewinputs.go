package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxReviewPath is the most characters of a path that the harness's tool guard accepts as
// qory.review.report.
const maxReviewPath = 4096

// The members of the member qory of inputs.json that a stage with reviewers adds. They
// are named as the members of qoryInputs are: lower-case words joined by underscores.
// Q-36 adds qory.start_commits, qory.start_artifacts for a Build session, and the members
// commit, diff, previous_findings and build_report to qory.review; this file does not
// define them.

// stageInputs is qory.stage, written into the inputs.json of every session of a stage
// whose workflow entry has reviewers.
type stageInputs struct {
	// Reviewers is the name of the group role that lists the reviewers, such as
	// reviewers. The harness reads a name of lower-case letters, digits and hyphens.
	Reviewers string `json:"reviewers"`
}

// reviewInputs is qory.review, written into the inputs.json of a reviewer's session. Each
// value is the absolute path of a file or folder that the reviewer reads, except Report,
// the path where the reviewer writes its report.
type reviewInputs struct {
	// Report is the path of the report file that the reviewer writes.
	Report string `json:"report"`
	// SuiteResult is the path of the suite result file, see suite.WriteResult.
	SuiteResult string `json:"suite_result"`
}

// reviewStartArtifactTypes are the types of the work item's artifacts that qory pulls
// into the start_artifacts folder of a reviewer's session: the acceptance criteria, the
// spec and the build plan, which acceptance-reviewer reads below <start>/<type>/.
var reviewStartArtifactTypes = []string{"acceptance-criteria", "spec", "build-plan"}

// reviewerInputs is the members of qory that a reviewer's inputs.json has beside the ones
// every session has.
type reviewerInputs struct {
	Stage          stageInputs  `json:"stage"`
	Review         reviewInputs `json:"review"`
	StartArtifacts string       `json:"start_artifacts"`
}

// harnessNoRole are the words that the harness does not take as the name of a group role
// in qory.stage.reviewers: it reads each as no.
var harnessNoRole = []string{"false", "no", "null", "off", "none", "0"}

// roleNameForm is the form of a group role's name that the harness takes: lower-case
// letters, digits and hyphens.
var roleNameForm = regexp.MustCompile(`^[a-z0-9-]+$`)

// newStageInputs is qory.stage for the group role role. It returns an error that starts
// with the fix for a name that the harness does not take as a role.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newStageInputs(role string) (stageInputs, error) {
	if !roleNameForm.MatchString(role) || slices.Contains(harnessNoRole, role) {
		return stageInputs{}, fmt.Errorf("Give the stage a group role of lower-case letters, digits and hyphens.\nThe role is %s, and the harness reads %s as no role.", strconv.Quote(role), "false, no, null, off, none and 0")
	}
	return stageInputs{Reviewers: role}, nil
}

// newReviewerInputs are the members of qory for a reviewer session, in a stage
// whose reviewers are the group role role, with suiteResult, the path of the suite result
// file, and startArtifacts, the path of the folder of [reviewStartArtifactTypes]. It
// returns an error that starts with the fix for a role, a report path that the harness's
// tool guard does not accept, and a path of a file or folder to read that is not absolute
// and clean.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newReviewerInputs(role, report, suiteResult, startArtifacts string) (reviewerInputs, error) {
	stage, err := newStageInputs(role)
	if err != nil {
		return reviewerInputs{}, err
	}
	if why := reviewPathProblem(report); why != "" {
		return reviewerInputs{}, fmt.Errorf("Give the reviewer an absolute path in qory.review.report that the harness accepts.\nThe path %s %s.", strconv.Quote(report), why)
	}
	for _, p := range []struct{ what, path string }{{"review.suite_result", suiteResult}, {"start_artifacts", startArtifacts}} {
		if !cleanAbs(p.path) {
			return reviewerInputs{}, fmt.Errorf("Give the reviewer an absolute, clean path in qory.%s.\nThe path is %s.", p.what, strconv.Quote(p.path))
		}
	}
	return reviewerInputs{Stage: stage, Review: reviewInputs{Report: report, SuiteResult: suiteResult}, StartArtifacts: startArtifacts}, nil
}

// cleanAbs reports whether path is absolute and clean, and has no line break or NUL.
func cleanAbs(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\n\r\x00")
}

// reviewPathProblem is why the harness's tool guard does not accept path as
// qory.review.report or as another path of qory.review, as the end of a sentence about the
// path ("is not absolute"), and "" for a path it accepts: an absolute path with no . or ..
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
