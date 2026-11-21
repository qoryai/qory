package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/stack"
)

// reviewReportName is the name of a reviewer's report file in its report folder.
const reviewReportName = "report.json"

// maxReviewPath is the most characters of a path that the harness's tool guard accepts as
// qory.review.report.
const maxReviewPath = 4096

// reviewerNameForm is the form of a reviewer's name, as [reviewround.Decide] takes it:
// lower-case letters, digits and hyphens, from a letter or a digit, at most 64 characters.
var reviewerNameForm = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// reviewerAgent is a member of the stage's reviewers group role: the agent's name and its
// tier, "" for an agent with a model id or none.
type reviewerAgent struct {
	Name string
	Tier string
}

// reviewSessionInput is what [newReviewSession] builds a reviewer's session from.
type reviewSessionInput struct {
	Reviewer reviewerAgent
	// Copy is the folder that [reviewcopy.Folder.Session] filled for this reviewer.
	Copy reviewcopy.Folder
	// ReportRoot is the absolute, clean folder that qory made for this reviewer's report,
	// outside Copy, which no other session mounts.
	ReportRoot string
	// Provider is the name of the model key the run uses, the run's
	// SessionSpec.ModelProviderCredentialName; "" is the run's default.
	Provider string
	// Tiers maps a tier to its model, as the compose result has it.
	Tiers map[string]stack.Tier
	// TimeLimit is the time limit of the workflow entry with the reviewers, 0 for none.
	TimeLimit time.Duration
}

// reviewSessionSpec is a reviewer's session as qory starts it.
type reviewSessionSpec struct {
	Reviewer string
	Provider string
	// Model and Effort are the entry that the reviewer's tier maps to; "" for a reviewer
	// with no tier or a tier with no map, which runs on the run's main model.
	Model     string
	Effort    string
	TimeLimit time.Duration
	// Mounts are the commit folder and the diff file of the copy, read-only, and the
	// report folder, writable. The copy's state folder is not mounted.
	Mounts     []wall.Mount
	ReportRoot string
	// Report is the absolute path of the report file, the value of qory.review.report.
	Report string
}

// newReviewSession builds the session of one reviewer. It returns an error that starts
// with the fix for a name or tier of another form, a time limit below 0, a folder that is
// not absolute and clean, a report path that the tool guard of the harness does not
// accept, and a report folder that is the copy's folder, below it, or above it.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func newReviewSession(in reviewSessionInput) (reviewSessionSpec, error) {
	name := in.Reviewer.Name
	if !reviewerNameForm.MatchString(name) {
		return reviewSessionSpec{}, fmt.Errorf("Name each reviewer with lower-case letters, digits and hyphens.\nThe reviewer is %s.", strconv.Quote(name))
	}
	if tier := in.Reviewer.Tier; tier != "" && !module.IsTier(tier) {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer a tier of %s.\nThe tier of %s is %s.", strings.Join(module.Tiers, ", "), name, strconv.Quote(tier))
	}
	if in.TimeLimit < 0 {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer a time limit of 0 or more.\nThe limit of %s is %s.", name, in.TimeLimit)
	}
	copyRoot := in.Copy.Root()
	if !cleanAbs(copyRoot) {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer the copy that reviewcopy made.\nThe copy of %s is at %s.", name, strconv.Quote(copyRoot))
	}
	if !cleanAbs(in.ReportRoot) {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer an absolute, clean report folder.\nThe folder of %s is %s.", name, strconv.Quote(in.ReportRoot))
	}
	if within(copyRoot, in.ReportRoot) || within(in.ReportRoot, copyRoot) {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer a report folder apart from its copy.\nFor %s, the report folder %s and the copy %s overlap.", name, strconv.Quote(in.ReportRoot), strconv.Quote(copyRoot))
	}
	report := filepath.Join(in.ReportRoot, reviewReportName)
	if why := reviewPathProblem(report); why != "" {
		return reviewSessionSpec{}, fmt.Errorf("Give a reviewer a report folder that the harness accepts.\nFor %s, the report path %s %s.", name, strconv.Quote(report), why)
	}
	spec := reviewSessionSpec{
		Reviewer:  name,
		Provider:  in.Provider,
		TimeLimit: in.TimeLimit,
		Mounts: []wall.Mount{
			{Path: in.Copy.Commit(), ReadOnly: true},
			{Path: in.Copy.Diff(), ReadOnly: true},
			{Path: in.ReportRoot},
		},
		ReportRoot: in.ReportRoot,
		Report:     report,
	}
	if t, ok := in.Tiers[in.Reviewer.Tier]; ok && in.Reviewer.Tier != "" {
		spec.Model, spec.Effort = t.Model, t.Effort
	}
	return spec, nil
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
