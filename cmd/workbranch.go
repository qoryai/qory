package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// branchType is the artifact type of a work item's branch, and maxBranchFile the largest
// file of one of its versions in bytes, the type's size limit in the software factory.
const (
	branchType    = "branch"
	maxBranchFile = 64 << 10
)

// readBranch reads the branch of the work item that r's run is for, on this machine and
// before the fetch: nil with no branch step, for a run that is not from a run code, has
// no work item, or has no target version on the system of its repositories, and for a
// server with the feature workflow_model off. Each error ends with [codeUsed].
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

	index, err := client.Artifacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	branch := &preparation.WorkBranch{Ref: branchformat.Name(r.workItem)}

	version, err := chosenBranch(snapshot, index)
	if err != nil {
		return nil, err
	}

	if version == nil {
		return branch, nil
	}

	document, err := readBranchVersion(ctx, client, version)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	if document.Ref != branch.Ref {
		return nil, fmt.Errorf("Add a new version of the artifact %s in Qory Apiary with the ref %s: version %d has another ref.%s",
			branchType, branch.Ref, version.Number, codeUsed)
	}

	branch.Version = document.Repositories

	return branch, nil
}

// featureOff reports whether err is a 404 with no error code, the error page of a server
// with the feature workflow_model off.
func featureOff(err error) bool {
	var apiError *runapi.Error

	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound && apiError.Code == ""
}

// chosenBranch is the version of the artifact type branch that a run starts at: the
// newest version of the artifact main when the work item's stage wrote it; else the
// version of any artifact name that a gate approved of a stage's input; else the newest
// version of the artifact main; nil with none of these.
func chosenBranch(snapshot *runapi.Snapshot, index *runapi.Index) (*runapi.Version, error) {
	var newest *runapi.Version

	for _, artifact := range index.Artifacts {
		if artifact.Type != branchType || artifact.Name != defaultName {
			continue
		}

		for i := range artifact.Versions {
			if newest == nil || artifact.Versions[i].Number > newest.Number {
				newest = &artifact.Versions[i]
			}
		}
	}

	stage := snapshot.Stage.ID
	if newest != nil && stage != nil && newest.StageID != nil && *stage == *newest.StageID {
		return newest, nil
	}

	for _, input := range snapshot.Inputs {
		if input.Type != branchType || input.Version == nil {
			continue
		}

		for _, artifact := range index.Artifacts {
			if artifact.Type != branchType {
				continue
			}

			for i := range artifact.Versions {
				if artifact.Versions[i].ID == input.Version.ID {
					return &artifact.Versions[i], nil
				}
			}
		}

		return nil, fmt.Errorf("Report this run to the instance admin of Qory Apiary. The approved version %d of the artifact %s is "+
			"not in the artifact's index.%s", input.Version.Number, branchType, codeUsed)
	}

	return newest, nil
}

// readBranchVersion downloads the one file of version and returns it, checked against the
// branch format.
func readBranchVersion(ctx context.Context, client *runapi.Client, version *runapi.Version) (*branchformat.Branch, error) {
	if len(version.Files) != 1 || version.Files[0].Size > maxBranchFile {
		return nil, fmt.Errorf("Add a new version of the artifact %s in Qory Apiary with one file of at most %d bytes: version %d has another size.",
			branchType, maxBranchFile, version.Number)
	}

	var content bytes.Buffer
	if err := client.Download(ctx, version.ID, version.Files[0], &content); err != nil {
		return nil, err
	}

	if err := branchformat.Check(content.Bytes()); err != nil {
		return nil, fmt.Errorf("Add a new version of the artifact %s in Qory Apiary in place of version %d: %w", branchType, version.Number, err)
	}

	var document branchformat.Branch
	if err := json.Unmarshal(content.Bytes(), &document); err != nil {
		return nil, fmt.Errorf("Add a new version of the artifact %s in Qory Apiary in place of version %d: %w", branchType, version.Number, err)
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
