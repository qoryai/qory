package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
)

// reviewCheck is what [checkReviewRound] needs after a review-changes session ends.
type reviewCheck struct {
	// Expected are the names of the reviewers whose report the round needs: the members
	// of the composed group role agents/reviewers (compose.Result.Groups) whose paths
	// match the change, as the harness's panel verb picks them. No Go code matches a
	// member's paths yet, so the caller passes the names.
	Expected []string
	// Reports is the folder where each reviewer writes <agent>.json, the value of
	// qory.review_session.reports.
	Reports string
	// Suite is the result of the full suite on the reviewed commit, nil for none.
	Suite *reviewround.Suite
	// Previous is the open findings of the round before, nil in round 1.
	Previous *reviewround.OpenFindings
	// Recorded is the verdict that the session recorded.
	Recorded string
	// OpenFindings is the path of open-findings.json for the next implement-changes
	// session.
	OpenFindings string
}

// reviewCheckResult is the outcome of [checkReviewRound].
type reviewCheckResult struct {
	// Stands is whether the recorded verdict passed and the round is clean.
	Stands bool
	// Refusal is the text for a session that recorded passed on a round that is not
	// clean, "" otherwise. Its first line is at most 100 characters and has the fix.
	Refusal string
	// Decision is the round's decision, with its verdict, points and open findings.
	Decision reviewround.Decision
}

// checkReviewRound checks the reports of the reviewers in c.Expected, each at
// <c.Reports>/<agent>.json, with [reviewround.Decide], and writes the round's open
// findings to c.OpenFindings. A report that is missing, too large or invalid is a
// blocking finding, not an error. A recorded passed stands only when every expected
// report is there and nothing blocks. The error starts with the fix, and is for a round
// that the caller made wrong, such as no expected reviewer, and for a file that cannot
// be written.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
// shortcut: each reviewer counts as finished, so a session that failed or timed out
// reads as a missing report, with the point for that.
func checkReviewRound(c reviewCheck) (reviewCheckResult, error) {
	round := reviewround.Round{Number: 1, Suite: c.Suite, Previous: c.Previous}
	if c.Previous != nil {
		round.Number = c.Previous.Round + 1
	}
	for _, name := range c.Expected {
		round.Reviewers = append(round.Reviewers, reviewround.Reviewer{
			Name: name, Ended: reviewround.Finished, Root: c.Reports, Report: name + ".json",
		})
	}
	d, err := reviewround.Decide(round)
	if err != nil {
		return reviewCheckResult{}, err
	}
	if err := writeOpenFindings(c.OpenFindings, d.Open); err != nil {
		return reviewCheckResult{}, err
	}
	res := reviewCheckResult{Decision: d}
	switch {
	case c.Recorded != runapi.VerdictPassed:
	case d.Verdict == reviewround.Passed:
		res.Stands = true
	default:
		res.Refusal = reviewRefusal(d)
	}
	return res, nil
}

// reviewRefusal is the text for a recorded passed on a round whose decision is d, which
// has a verdict other than passed.
func reviewRefusal(d reviewround.Decision) string {
	var counts []string
	total := 0
	for _, entry := range d.Open.Reviewers {
		n := 0
		for _, f := range entry.Findings {
			if f.Severity == reviewround.Blocking {
				n++
			}
		}
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%s (%d)", entry.Reviewer, n))
			total += n
		}
	}
	sort.Strings(counts)
	return fmt.Sprintf("Record %s, not passed: the review round is not clean.\n"+
		"  Round %d has %d blocking findings, from %s.\n"+
		"  A missing report is a blocking finding. open-findings.json has each finding.",
		d.Verdict, d.Open.Round, total, strings.Join(counts, ", "))
}
