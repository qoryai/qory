package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/stack"
)

// openFindingsMode is the mode of open-findings.json: the next Build session reads it
// through a read-only mount, and the file has no secret.
const openFindingsMode = 0o644

// recordedVerdict is the verdict that a session recorded before it ended, with how many
// points the verdict has.
type recordedVerdict struct {
	Verdict string
	Points  int
}

// reviewJoin is one reviewer's join as [reviewRun.Join] starts it.
type reviewJoin struct {
	Spec reviewSessionSpec
	// Number is the session number that Forager gives the join: the number of the session
	// before it, plus 1. qory predicts it, and a join that gets another one ends the run.
	Number int
	// Round is the round's number, from 1.
	Round int
	// Previous is the open findings of the round before, nil in round 1. The reviewer
	// reads them as qory.review.previous_findings.
	Previous *reviewround.OpenFindings
}

// reviewJoined is how a reviewer's join ended: the number Forager gave the session, and
// how the session ended. A session that failed or reached its limit is not an error of
// the join: its [reviewround.Failed] or [reviewround.TimedOut] makes a finding.
type reviewJoined struct {
	Session int
	Ended   reviewround.Ending
}

// reviewRun is one review round: the reviewers and the facts the round decides on, and
// the steps that the caller runs against Forager and the run API. A test sets fakes.
type reviewRun struct {
	// Reviewers are the members of the stage's reviewers group role, in the order the
	// group lists them: the order of the round.
	Reviewers []reviewerAgent
	// Number is the round's number, from 1. NextID is the number of the first new
	// finding's id in the run, 0 to continue after Previous.
	Number int
	NextID int
	// BuildSession is the number of the session that made the change.
	BuildSession int
	// Recorded is what that session recorded, nil for no verdict.
	Recorded *recordedVerdict
	// Suite is the result of the full suite on the reviewed commit, nil for none.
	Suite *reviewround.Suite
	// Previous is the open findings of the round before, nil in round 1.
	Previous *reviewround.OpenFindings

	// Provider, Tiers and TimeLimit are the inputs of [newReviewSession] that are the
	// same for each reviewer.
	Provider  string
	Tiers     map[string]stack.Tier
	TimeLimit time.Duration
	// OpenFindingsPath is where the round writes open-findings.json, in a folder that
	// exists.
	OpenFindingsPath string

	// Copy makes the copy of the reviewed commit that the reviewer name reads.
	Copy func(ctx context.Context, name string) (reviewcopy.Folder, error)
	// ReportRoot makes the folder that only the reviewer name mounts writable.
	ReportRoot func(ctx context.Context, name string) (string, error)
	// Join starts the reviewer's session and returns when it ended.
	Join func(ctx context.Context, j reviewJoin) (reviewJoined, error)
	// SendVerdict sends the round's verdict and points to the run.
	SendVerdict func(ctx context.Context, verdict string, points []runapi.Point) error
	// SendNext sends the one next-session request, with session, the number of the last
	// session of the run so far.
	SendNext func(ctx context.Context, session int) error
}

// reviewOutcome is what a round did.
type reviewOutcome struct {
	// Ran is false for a round that [shouldReview] skipped.
	Ran bool
	// Decision is the round's decision, the zero value when Ran is false.
	Decision reviewround.Decision
	// Session is the number that the next-session request carried: the last join's, or
	// BuildSession when no reviewer joined.
	Session int
}

// shouldReview reports whether the session that recorded v is followed by a review round:
// not when it recorded changes-requested with no points, as the loop then goes back to
// implement-changes. The verdict of a reviewer's session is never recorded this way.
func shouldReview(v *recordedVerdict) bool {
	return v == nil || v.Verdict != runapi.VerdictChangesRequested || v.Points > 0
}

// runReviewRound runs the round r: each reviewer's session in turn, the decision of
// [reviewround.Decide], open-findings.json, the verdict, and one next-session request
// with the last session's number. A round that [shouldReview] skips starts no reviewer
// and sends the request with BuildSession and no verdict. A reviewer whose session
// crashed or reached its limit is a finding. An error from a step stops the round, and
// the steps after it do not run: a round that cannot place a reviewer's join, or whose
// join got another session number than predicted, sends no verdict and no request.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73). The reviewers' source waits for
// user decision 905, so Reviewers is an input.
func runReviewRound(ctx context.Context, r reviewRun) (reviewOutcome, error) {
	if !shouldReview(r.Recorded) {
		return reviewOutcome{Session: r.BuildSession}, r.SendNext(ctx, r.BuildSession)
	}
	if len(r.Reviewers) == 0 {
		return reviewOutcome{}, errors.New("Give the round at least one reviewer.\nThe round has none.")
	}
	for i, agent := range r.Reviewers {
		switch {
		case !reviewerNameForm.MatchString(agent.Name) || agent.Name == reviewround.SuiteEntry:
			return reviewOutcome{}, fmt.Errorf("Name each reviewer with lower-case letters, digits and hyphens, not %s.\nThe reviewer is %s.", reviewround.SuiteEntry, strconv.Quote(agent.Name))
		case slices.ContainsFunc(r.Reviewers[:i], func(o reviewerAgent) bool { return o.Name == agent.Name }):
			return reviewOutcome{}, fmt.Errorf("List each reviewer once.\nThe reviewer %s is in the round twice.", strconv.Quote(agent.Name))
		}
	}
	last := r.BuildSession
	reviewers := make([]reviewround.Reviewer, 0, len(r.Reviewers))
	for _, agent := range r.Reviewers {
		if err := ctx.Err(); err != nil {
			return reviewOutcome{}, err
		}
		spec, err := r.sessionOf(ctx, agent)
		if err != nil {
			return reviewOutcome{}, err
		}
		number := last + 1
		joined, err := r.Join(ctx, reviewJoin{Spec: spec, Number: number, Round: r.Number, Previous: r.Previous})
		if err != nil {
			return reviewOutcome{}, fmt.Errorf("Check the join of the reviewer: it stopped the round.\nThe reviewer is %s: %w", strconv.Quote(agent.Name), err)
		}
		if joined.Session != number {
			return reviewOutcome{}, fmt.Errorf("Stop the run: a reviewer's join got another session number than qory predicted.\nFor the reviewer %s, qory predicted %d and the session has %d.", strconv.Quote(agent.Name), number, joined.Session)
		}
		last = number
		reviewers = append(reviewers, reviewround.Reviewer{Name: agent.Name, Ended: joined.Ended, Root: spec.ReportRoot, Report: reviewReportName})
	}
	decision, err := reviewround.Decide(reviewround.Round{Number: r.Number, NextID: r.NextID, Reviewers: reviewers, Suite: r.Suite, Previous: r.Previous})
	if err != nil {
		return reviewOutcome{}, err
	}
	if err := writeOpenFindings(r.OpenFindingsPath, decision.Open); err != nil {
		return reviewOutcome{}, err
	}
	if err := r.SendVerdict(ctx, decision.Verdict, decision.Points); err != nil {
		return reviewOutcome{}, err
	}
	return reviewOutcome{Ran: true, Decision: decision, Session: last}, r.SendNext(ctx, last)
}

// sessionOf makes the copy and the report folder of the reviewer agent, and its session.
func (r reviewRun) sessionOf(ctx context.Context, agent reviewerAgent) (reviewSessionSpec, error) {
	copyDir, err := r.Copy(ctx, agent.Name)
	if err != nil {
		return reviewSessionSpec{}, fmt.Errorf("Check the reviewer's copy: it stopped the round.\nThe reviewer is %s: %w", strconv.Quote(agent.Name), err)
	}
	root, err := r.ReportRoot(ctx, agent.Name)
	if err != nil {
		return reviewSessionSpec{}, fmt.Errorf("Check the reviewer's report folder: it stopped the round.\nThe reviewer is %s: %w", strconv.Quote(agent.Name), err)
	}
	return newReviewSession(reviewSessionInput{Reviewer: agent, Copy: copyDir, ReportRoot: root, Provider: r.Provider, Tiers: r.Tiers, TimeLimit: r.TimeLimit})
}

// writeOpenFindings writes open as open-findings.json at path: a new file, then a rename
// over the old one, so a session that reads the old file never reads half of the new.
func writeOpenFindings(path string, open reviewround.OpenFindings) error {
	data, err := open.JSON()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".open-findings-*")
	if err != nil {
		return fmt.Errorf("Give the round a folder that qory can write.\n%w", err)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(openFindingsMode)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("Free space for open-findings.json.\n%s: %w", strconv.Quote(path), err)
	}
	return nil
}
