package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/runapi"
)

// maxStartFile is the largest file of the version that a run starts from, in bytes: the size
// limit of a delivered type.
const maxStartFile = 786432

// The words of a run whose start, the head of the version that it starts from, is unknown.
// Line 1 is the fix, and the cause is on line 2.
const (
	textNoStart          = "Report the run to the instance admin of Qory Apiary: no start found" + codeUsed + "\n  %s"
	textNoStartEntry     = "Version %d of the artifact %s, from run %s, has no pushed commit on %s."
	textNoStartReference = "Version %d of the artifact %s has %d top-level references to commit %s of %s, not 1."
	textNoStartFile      = "Version %d of the artifact %s has no one JSON file of at most %d bytes."
	textNoApproved       = "The approved version of the artifact %s is not in the artifact's index."
	textOutputTypeName   = "Report the run to the instance admin of Qory Apiary: an output type is no name.\n  The stage lists the output type %s, not a name of a-z, 0-9 and single hyphens that starts with a letter and has at most 64 characters" + codeUsed
)

// readWorkItem reads the work item that r's run is for, at the start of the run, with or
// without a repository: it sets r.snapshot and r.runNumber from the work item snapshot of
// GET /work-item, and stops on an output type that is not a name, as such a type becomes a
// path of the outbox. It reads nothing for a run that is not from a run code or has no work
// item, for a run with no run API, which has no control plane to store an output in, and
// for a server with the feature workflow_model off. Each error ends with
// [codeUsed]. The read ends at deadline, the end of the run's setup, and has no such end
// when deadline is zero.
func (r *codeRun) readWorkItem(ctx context.Context, run openedRun, deadline time.Time) error {
	if r == nil || r.workItem == "" || !hasRunAPI(run) {
		return nil
	}
	client, ctx, cancel, err := setupClient(ctx, run, deadline)
	if err != nil {
		return err
	}
	defer cancel()
	snapshot, err := client.WorkItem(ctx)
	if featureOff(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w%s", err, codeUsed)
	}
	for _, o := range snapshot.Outputs {
		if !runapi.ArtifactName(o.Type) {
			return fmt.Errorf(textOutputTypeName, shownText(provisioner.ShownType(o.Type)))
		}
	}
	r.snapshot, r.runNumber = snapshot, snapshot.Run.Number
	return nil
}

// setupClient is the run API client of run, with ctx bounded by [runAPITimeout] and by
// deadline when it is not zero, and what ends that ctx. Each error ends with [codeUsed].
func setupClient(ctx context.Context, run openedRun, deadline time.Time) (*runapi.Client, context.Context, context.CancelFunc, error) {
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, ctx, func() {}, fmt.Errorf("%w%s", err, codeUsed)
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, ctx, func() {}, fmt.Errorf("%w%s", err, codeUsed)
	}
	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	if deadline.IsZero() {
		return client, ctx, cancel, nil
	}
	ctx, stop := context.WithDeadline(ctx, deadline)
	return client, ctx, func() { stop(); cancel() }, nil
}

// readBranch reads the branch of the work item that r's run is for, on this machine and
// before the fetch: where the run starts, per repository, and which pushed commits are a
// cancelled run's. It is nil with no branch step, for a run that [codeRun.readWorkItem]
// read no work item for, or that has no target version on the system of its repositories.
// Each error ends with [codeUsed]. The reads end at deadline, the end of the run's setup,
// and have no such end when deadline is zero.
func (r *codeRun) readBranch(ctx context.Context, run openedRun, deadline time.Time) (*preparation.WorkBranch, error) {
	if r == nil || r.snapshot == nil || r.target.Version == "" || r.target.System != run.System() {
		return nil, nil
	}
	client, ctx, cancel, err := setupClient(ctx, run, deadline)
	if err != nil {
		return nil, err
	}
	defer cancel()
	snapshot := r.snapshot

	index, err := client.Artifacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}

	branch := &preparation.WorkBranch{Ref: preparation.WorkItemRef(r.workItem)}
	pushed := snapshot.PushedCommits

	version, artifact, approved := startVersion(snapshot, index, r.targetRef)
	if version == nil && artifact != "" {
		return nil, fmt.Errorf(textNoStart, fmt.Sprintf(textNoApproved, shownText(artifact)))
	}
	if version != nil {
		commit, skipped, err := startCommit(ctx, client, version, artifact, sameRunVersions(index, r.targetRef, version), pushed, r.targetRef)
		if err != nil {
			return nil, err
		}
		branch.Version = []preparation.Repository{{System: r.targetRef.Address.System, Path: r.targetRef.Address.Path, Commit: commit}}
		branch.Approved = approved
		// A commit that the start's run pushed after the start, which no version of the run
		// confirms, counts as a cancelled run's: the run does not move there, and its push
		// replaces it.
		pushed = slices.Clone(pushed)
		for _, i := range skipped {
			pushed[i].Cancelled = true
		}
	}

	if err := pushedStarts(branch, pushed); err != nil {
		return nil, err
	}

	return branch, nil
}

// pushedStarts sets what the work item's record of pushed commits gives branch: Pushed, every
// commit of a run that a person did not cancel, and Cancelled, every commit of a cancelled run.
// The entries come ordered by run number and then by push order, and Pushed keeps that order, so
// the last entry of a repository is its newest. A commit that is not a full commit id ends the
// read before git gets it: the run API returns these values, and none of them may be a ref name
// or an option.
func pushedStarts(branch *preparation.WorkBranch, pushed []runapi.PushedEntry) error {
	for _, entry := range pushed {
		if !lowerCommitID(entry.Commit) {
			return fmt.Errorf("Report this run to the instance admin of Qory Apiary.\n"+
				"  The list of pushed commits has %s, which is not a full commit id, so the run did not start%s", shownText(entry.Commit), codeUsed)
		}

		landed := preparation.Landed{System: entry.System, Path: entry.Path, Commit: entry.Commit}
		if entry.Cancelled {
			branch.Cancelled = append(branch.Cancelled, landed)
		} else {
			branch.Pushed = append(branch.Pushed, landed)
		}
	}

	return nil
}

// featureOff reports whether err is a 404 with no error code, the error page of a server
// with the feature workflow_model off.
func featureOff(err error) bool {
	var apiError *runapi.Error

	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound && apiError.Code == ""
}

// startVersion is the version that a run starts from, of the artifacts of target's
// collection, the name [outputName] gives a delivered output: the newest version that is not
// cancelled and that the work item's stage wrote, as for Build, even when another stage wrote
// a newer one; else the version that a gate approved, as for Verify; nil with none of these,
// where the run starts at what the run documents check out. A version counts only when no
// version of its artifact that is not cancelled, with a higher number, is a withdrawal: a
// withdrawal as the newest version leaves no version of its artifact. artifact is the type of
// the version's artifact; with a nil version it is set only when the approved version is not
// in the index. approved is true when the version is the one that a gate approved, not one that
// the stage wrote: the run stays at its commit. A run has the approved version when no
// artifact has a counting version of its stage (the stage wrote none, every version of its
// stage is cancelled, or a withdrawal follows each), and a work item in no stage has it too.
// When several types have such versions, the one with the latest time of all wins.
// shortcut: two delivered types of one stage written by different runs give the newest one;
// replace it when the run API names the version a run starts from. [startCommit] confirms the
// start with another version of the same run when the newest one has no reference to it.
func startVersion(snapshot *runapi.Snapshot, index *runapi.Index, target *contracts.Target) (version *runapi.Version, artifact string, approved bool) {
	if target == nil || target.Address == nil {
		return nil, "", false
	}
	name := outputName(exports.ProcessType{Send: "delivered"}, target)
	stage := snapshot.Stage.ID
	var (
		written, agreed         *runapi.Version
		writtenType, agreedType string
		missing                 string
	)
	for _, a := range index.Artifacts {
		if a.Name != name {
			continue
		}
		after := lastWithdrawal(a)
		if pick := newestOfStage(a, stage, after); pick != nil && (written == nil || compareAt(pick.At, written.At) > 0) {
			written, writtenType = pick, a.Type
		}
		if a.ApprovedVersion == nil {
			continue
		}
		var pick *runapi.Version
		found := false
		for i := range a.Versions {
			if a.Versions[i].ID == *a.ApprovedVersion {
				found = true
				if a.Versions[i].Number > after {
					pick = &a.Versions[i]
				}
			}
		}
		switch {
		case !found && missing == "" && !withdrawn(a):
			missing = a.Type
		case pick != nil && (agreed == nil || compareAt(pick.At, agreed.At) > 0):
			agreed, agreedType = pick, a.Type
		}
	}
	switch {
	case written != nil:
		return written, writtenType, false
	case missing != "":
		return nil, missing, false
	}
	return agreed, agreedType, agreed != nil
}

// lastWithdrawal is the highest number of a version of artifact that is a withdrawal and that
// the index does not mark cancelled, 0 for none: no version with that number or a lower one
// counts as a start.
func lastWithdrawal(artifact runapi.Artifact) int {
	last := 0
	for _, v := range artifact.Versions {
		if v.Withdrawn && !v.Cancelled {
			last = max(last, v.Number)
		}
	}
	return last
}

// newestOfStage is the version of artifact with the highest number above after that the index
// does not mark cancelled and that the stage wrote: nil for none, and for a work item in no
// stage.
func newestOfStage(artifact runapi.Artifact, stage *string, after int) *runapi.Version {
	var newest *runapi.Version
	for i := range artifact.Versions {
		v := &artifact.Versions[i]
		if stage != nil && v.StageID != nil && *v.StageID == *stage && !v.Cancelled && v.Number > after && (newest == nil || v.Number > newest.Number) {
			newest = v
		}
	}
	return newest
}

// startVersionOf is a version that a run may start from, and its artifact type.
type startVersionOf struct {
	version *runapi.Version
	typ     string
}

// sameRunVersions are the versions in index, newest first, that the run which wrote chosen
// stored in the artifacts of target other than chosen, cancelled ones left out, and those
// with a later withdrawal of their artifact that is not cancelled, which confirm no commit: a
// stage can store two delivered types for one target, and only one of them may confirm the start.
func sameRunVersions(index *runapi.Index, target *contracts.Target, chosen *runapi.Version) []startVersionOf {
	name := outputName(exports.ProcessType{Send: "delivered"}, target)
	var out []startVersionOf
	for _, a := range index.Artifacts {
		if a.Name != name {
			continue
		}
		after := lastWithdrawal(a)
		for i := range a.Versions {
			v := &a.Versions[i]
			if v.ID != chosen.ID && !v.Cancelled && v.Number > after && v.Writer.Kind == "run" && v.Writer.RunID == chosen.Writer.RunID {
				out = append(out, startVersionOf{v, a.Type})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b startVersionOf) int { return compareAt(b.version.At, a.version.At) })
	return out
}

// noStart is the error of [startCommit] for a version that confirms no commit. A failed
// read of a version's file is another error, see [references].
type noStart struct{ error }

// startCommit is the commit a run starts at in the target's repository: of the commits of
// pushed, the work item's pushed commits, that the run which wrote version pushed to the
// target's address, the newest that a version of that run confirms. A version confirms a
// commit when its one file has exactly one top-level reference to that commit in the
// target's collection: version first, then others, the other versions of that run, in their
// order. skipped is the index in pushed of each entry of that run for the target's address
// that is newer than the start: a session of the run pushed it and stored no version that
// confirms it. typ is the version's artifact type. With no confirmed commit the error
// is a [noStart] about the newest one.
func startCommit(ctx context.Context, client *runapi.Client, version *runapi.Version, typ string, others []startVersionOf, pushed []runapi.PushedEntry, target *contracts.Target) (commit string, skipped []int, err error) {
	shown := shownText(typ)
	repository := shownText(target.Address.System + "/" + target.Address.Path)
	var entries []int
	for i, e := range pushed {
		if e.RunID == version.Writer.RunID && e.System == target.Address.System && preparation.SameRepository(e.System, e.Path, target.Address.Path) {
			entries = append(entries, i)
		}
	}
	if len(entries) == 0 || version.Writer.Kind != "run" {
		return "", nil, noStart{fmt.Errorf(textNoStart, fmt.Sprintf(textNoStartEntry, version.Number, shown, shownText(version.Writer.RunID), repository))}
	}
	counts, err := references(ctx, client, version, target)
	if err != nil {
		return "", nil, err
	}
	// The files of others are read once each, and only when version does not confirm a commit;
	// a version whose download fails confirms nothing.
	var read []referenceCount
	confirmed := func(commit string) bool {
		if counts.count(commit) == 1 {
			return true
		}
		if read == nil {
			read = make([]referenceCount, 0, len(others))
			for _, o := range others {
				refs, err := references(ctx, client, o.version, target)
				if err != nil {
					refs = nil
				}
				read = append(read, refs)
			}
		}
		return slices.ContainsFunc(read, func(refs referenceCount) bool { return refs.count(commit) == 1 })
	}
	for k := len(entries) - 1; k >= 0; k-- {
		commit = pushed[entries[k]].Commit
		if !confirmed(commit) {
			continue
		}
		return commit, entries[k+1:], nil
	}
	newest := pushed[entries[len(entries)-1]].Commit
	if counts == nil {
		return "", nil, noStart{fmt.Errorf(textNoStart, fmt.Sprintf(textNoStartFile, version.Number, shown, maxStartFile))}
	}
	return "", nil, noStart{fmt.Errorf(textNoStart, fmt.Sprintf(textNoStartReference, version.Number, shown, counts.count(newest), shownCommitID(newest), repository))}
}

// referenceCount is the number of top-level references in a version's file to each address
// path in the target's collection; nil for a version that has no one JSON file of at most
// [maxStartFile] bytes.
type referenceCount map[string]int

// count is the number of top-level references to commit.
func (c referenceCount) count(commit string) int { return c[commit] }

// references is the count of top-level references in the one file of version, per address
// path, in target's collection; nil for a version that has no one JSON file of at most
// [maxStartFile] bytes. An error is that of the download, and ends with [codeUsed].
func references(ctx context.Context, client *runapi.Client, version *runapi.Version, target *contracts.Target) (referenceCount, error) {
	if len(version.Files) != 1 || version.Files[0].Size > maxStartFile {
		return nil, nil
	}
	var content bytes.Buffer
	if err := client.Download(ctx, version.ID, version.Files[0], &content); err != nil {
		return nil, fmt.Errorf("%w%s", err, codeUsed)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(content.Bytes(), &members); err != nil {
		return nil, nil
	}
	found := referenceCount{}
	for _, raw := range members {
		var ref struct {
			Collection string `json:"collection,case:strict"`
			Address    *struct {
				System string `json:"system,case:strict"`
				Path   string `json:"path,case:strict"`
			} `json:"address,case:strict"`
		}
		if json.Unmarshal(raw, &ref) == nil && ref.Collection == target.Collection && ref.Address != nil {
			found[ref.Address.Path]++
		}
	}
	return found, nil
}

// compareAt compares the times a and b of the run API, RFC 3339 with or without fractional
// seconds: negative when a is earlier. A time that does not parse is the earliest.
func compareAt(a, b string) int {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	switch {
	case errA != nil && errB != nil:
		return strings.Compare(a, b)
	case errA != nil:
		return -1
	case errB != nil:
		return 1
	}
	return ta.Compare(tb)
}

// lowerCommitID reports whether v is a full commit id in lower case, the form that the run
// API and the pull-request output use: 40 or 64 lower-case hexadecimal digits.
func lowerCommitID(v string) bool { return preparation.CommitID(v) && strings.ToLower(v) == v }

// shownCommitID is the first 12 characters of commit, a full commit id.
func shownCommitID(commit string) string { return commit[:min(12, len(commit))] }

// fetchStepNameOn is [fetchStepName] with the branch and the commit a run starts at, for
// a run with branch set: the version's commit of the target, or the target version.
func fetchStepNameOn(branch *preparation.WorkBranch, r *codeRun, l runList) string {
	name := fetchStepName(l)
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
