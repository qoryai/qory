package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/submission"
)

// submissionType is the artifact type of a work item's submission, and maxSubmissionFile
// the largest file of one of its versions in bytes, the type's size limit in the software
// factory.
const (
	submissionType    = "submission"
	maxSubmissionFile = 256 << 10
)

// readBranch reads the branch of the work item that r's run is for, on this machine and
// before the fetch: where the run starts, per repository, and which pushed commits are a
// cancelled run's. It is nil with no branch step, for a run that is not from a run code, has
// no work item, or has no target version on the system of its repositories, and for a
// server with the feature workflow_model off. Each error ends with [codeUsed]. It sets
// r.runNumber from the work item snapshot.
func (r *codeRun) readBranch(ctx context.Context, run openedRun, timeout time.Duration, opened time.Time) (*preparation.WorkBranch, error) {
	if r == nil || r.workItem == "" || r.target.Version == "" || r.target.System != run.System() {
		return nil, nil
	}

	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	defer cancel()

	if timeout > 0 {
		var stop context.CancelFunc

		ctx, stop = context.WithDeadline(ctx, opened.Add(timeout))
		defer stop()
	}

	snapshot, err := client.WorkItem(ctx)
	if featureOff(err) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	r.runNumber = snapshot.Run.Number

	index, err := client.Artifacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	branch := &preparation.WorkBranch{Ref: submission.Name(r.workItem)}

	version, err := chosenSubmission(snapshot, index)
	if err != nil {
		return nil, err
	}

	if version == nil {
		pushedStarts(branch, snapshot.PushedCommits)

		return branch, nil
	}

	document, err := readSubmissionVersion(ctx, client, version)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	if document.Ref != branch.Ref {
		return nil, fmt.Errorf("Version %d of the artifact %s has a ref other than %s, so the run did not start%s",
			version.Number, submissionType, branch.Ref, codeUsed)
	}

	// A repository starts at the commit of the version; its commits, links and draft
	// belong to the run that wrote it.
	for _, r := range document.Repositories {
		branch.Version = append(branch.Version, submission.Repository{System: r.System, Path: r.Path, Commit: r.Commit})
	}

	pushedStarts(branch, snapshot.PushedCommits)

	return branch, nil
}

// pushedStarts sets what the work item's record of pushed commits gives branch: Pushed,
// per repository the newest commit of a run that a person did not cancel, and Cancelled,
// every commit of a cancelled run. The entries come ordered by run number and then by push
// order, so the last one of a repository is its newest.
func pushedStarts(branch *preparation.WorkBranch, pushed []runapi.PushedEntry) {
	newest := map[[2]string]preparation.Landed{}
	order := [][2]string{}

	for _, entry := range pushed {
		landed := preparation.Landed{System: entry.System, Path: entry.Path, Commit: entry.Commit}
		if entry.Cancelled {
			branch.Cancelled = append(branch.Cancelled, landed)

			continue
		}

		key := [2]string{entry.System, entry.Path}
		if _, seen := newest[key]; !seen {
			order = append(order, key)
		}

		newest[key] = landed
	}

	for _, key := range order {
		branch.Pushed = append(branch.Pushed, newest[key])
	}
}

// featureOff reports whether err is a 404 with no error code, the error page of a server
// with the feature workflow_model off.
func featureOff(err error) bool {
	var apiError *runapi.Error

	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound && apiError.Code == ""
}

// chosenSubmission is the version of the artifact type submission that a run starts at: the
// newest version of the artifact main that the work item's stage wrote and that is not
// cancelled; else the version that a gate approved; nil with none of these, where the run
// starts at what the run documents check out. A newest version that is a withdrawal counts
// as none.
func chosenSubmission(snapshot *runapi.Snapshot, index *runapi.Index) (*runapi.Version, error) {
	for _, artifact := range index.Artifacts {
		if artifact.Type != submissionType || artifact.Name != defaultName {
			continue
		}

		stage := snapshot.Stage.ID
		if newest := newestNotCancelled(artifact); newest != nil && stage != nil && newest.StageID != nil && *stage == *newest.StageID {
			return newest, nil
		}

		if artifact.ApprovedVersion == nil || withdrawn(artifact) {
			return nil, nil
		}

		for i := range artifact.Versions {
			if artifact.Versions[i].ID == *artifact.ApprovedVersion {
				return &artifact.Versions[i], nil
			}
		}

		return nil, fmt.Errorf("Report this run to the instance admin of Qory Apiary. The approved version of the artifact %s is "+
			"not in the artifact's index.%s", submissionType, codeUsed)
	}

	return nil, nil
}

// readSubmissionVersion downloads the one file of version and returns it, checked against the
// submission format.
func readSubmissionVersion(ctx context.Context, client *runapi.Client, version *runapi.Version) (*submission.Submission, error) {
	if len(version.Files) != 1 || version.Files[0].Size > maxSubmissionFile {
		return nil, fmt.Errorf("Version %d of the artifact %s does not have exactly one file of at most %d bytes, so the run did not start",
			version.Number, submissionType, maxSubmissionFile)
	}

	var content bytes.Buffer
	if err := client.Download(ctx, version.ID, version.Files[0], &content); err != nil {
		return nil, err
	}

	if err := submission.Check(content.Bytes()); err != nil {
		return nil, fmt.Errorf("Version %d of the artifact %s does not match the submission format, so the run did not start: %w", version.Number, submissionType, err)
	}

	var document submission.Submission
	if err := json.Unmarshal(content.Bytes(), &document); err != nil {
		return nil, fmt.Errorf("Version %d of the artifact %s cannot be read as a submission document, so the run did not start: %w", version.Number, submissionType, err)
	}

	return &document, nil
}

// fetchStepNameOn is [fetchStepName] with the branch and the commit a run starts at, for
// a run with branch set: the version's commit of the target, or the target version.
func fetchStepNameOn(branch *preparation.WorkBranch, r *codeRun, getenv func(string) string) string {
	name := fetchStepName(getenv)
	if branch == nil {
		return name
	}

	commit := branch.CommitOf(r.target.System, r.target.Path)
	if commit == "" {
		commit = r.target.Version
	}

	if len(commit) > 7 {
		commit = commit[:7]
	}

	return fmt.Sprintf(textFetchStepBranch, name, branch.Ref, commit)
}
