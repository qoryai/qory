package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/stack"
)

// The folders of a session's launch snapshot: startArtifactsDir in the run directory,
// which no mount reaches and which the push step's scope check reads, and the copy of the
// same name in the run's inputs folder, which the agent reads read-only.
const startArtifactsDir = "start-artifacts"

// textStartArtifacts is the error of a launch snapshot whose fetch failed, and
// textStartArtifactsCopy of one whose folders on this machine could not be removed, made
// or copied, each with the cause.
const (
	textStartArtifacts     = "The fetch of the session's start artifacts failed, so the agent did not start.\n  Cause: %s"
	textStartArtifactsCopy = "copying the session's start artifacts on this machine failed, so the agent did not start.\n  Cause: %s"
)

// startArtifacts makes the launch snapshot of a session whose plan types are plans, nil
// for none, see [snapshotPlanTypes]: the run folder's copy at runDir/start-artifacts,
// which it returns, and the copy at in/start-artifacts, each made new, so a later
// session gets a new one. The snapshot is the newest version of each plan type that is
// neither cancelled nor a withdrawal, and of each other artifact the version a gate
// approved, laid out as qory artifact pull writes them, with an index.json that lists
// only those versions. A
// fetch through the run API that fails is [textStartArtifacts], see
// [startArtifactsFailed], and a folder that fails [textStartArtifactsCopy], see
// [startArtifactsCopyFailed]. A run with no run API, client nil, and a server with the
// feature workflow_model off get a snapshot with no artifact.
// shortcut: a run with no run API gets an empty snapshot; replace it when a run with no
// control plane checks its scope.
func startArtifacts(ctx context.Context, client *runapi.Client, runDir, in string, plans []string) (string, error) {
	snapshot := filepath.Join(runDir, startArtifactsDir)
	copied := filepath.Join(in, startArtifactsDir)
	for _, dir := range []string{snapshot, copied} {
		if err := os.RemoveAll(dir); err != nil {
			return "", startArtifactsCopyFailed("removing the start artifacts of the session before", err)
		}
	}
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		return "", startArtifactsCopyFailed("making the folder of the start artifacts", err)
	}
	if err := fetchStartArtifacts(ctx, client, snapshot, plans); err != nil {
		return "", startArtifactsFailed(err)
	}
	if err := copyTree(snapshot, copied); err != nil {
		return "", startArtifactsCopyFailed("copying the start artifacts to the inputs folder", err)
	}
	return snapshot, nil
}

// startArtifactsFailed is [textStartArtifacts] for err, the fetch's error, with err kept for
// [endCodeOf]: a failed call to the run API, a [*runapi.CallError], ends run_api_failed.
func startArtifactsFailed(err error) error {
	return &shownCause{line: fmt.Errorf(textStartArtifacts, strings.TrimSuffix(shownError(err), ".")), cause: err}
}

// startArtifactsCopyFailed is [textStartArtifactsCopy] for step, a step on this machine
// that failed with err; the run closes machine_setup_invalid.
func startArtifactsCopyFailed(step string, err error) error {
	cause := strings.TrimSuffix(step+" failed: "+shownError(exports.HideError(err)), ".")
	return &reasonError{code: string(contracts.EndCodeMachineSetupInvalid), text: fmt.Sprintf(textStartArtifactsCopy, cause)}
}

// launchSnapshot is [startArtifacts] for a session of run, through its run API; with no
// run API, the snapshot has no artifact. A run API whose client cannot be made is the
// fetch's error. The error is a line of qory run.
func launchSnapshot(ctx context.Context, run openedRun, in string, plans []string) (string, error) {
	var client *runapi.Client
	if hasRunAPI(run) {
		httpClient, base, err := run.RunAPI()
		if err == nil {
			client, err = runapi.New(base, httpClient)
		}
		if err != nil {
			return "", &runLineError{startArtifactsFailed(err)}
		}
	}
	snapshot, err := startArtifacts(ctx, client, run.Dir(), in, plans)
	if err != nil {
		return "", &runLineError{err}
	}
	return snapshot, nil
}

// fetchStartArtifacts writes the launch snapshot of [startArtifacts] into dir, an empty
// folder, through client; with client nil, an index.json with no artifact alone.
func fetchStartArtifacts(ctx context.Context, client *runapi.Client, dir string, plans []string) error {
	store := &artifactStore{root: filepath.Dir(dir), dir: dir, fresh: true}
	empty := []byte(`{"artifacts":[]}` + "\n")
	if client == nil {
		return store.writeIndex(empty)
	}
	index, err := client.Artifacts(ctx)
	if featureOff(err) {
		return store.writeIndex(empty)
	}
	if err != nil {
		return err
	}
	chosen := make([]*runapi.Version, len(index.Artifacts))
	for i, artifact := range index.Artifacts {
		chosen[i] = snapshotVersion(artifact, plans)
		if chosen[i] == nil {
			continue
		}
		if err := store.fetch(ctx, client, artifact, *chosen[i]); err != nil {
			return err
		}
	}
	raw, err := snapshotIndex(index, chosen)
	if err != nil {
		return err
	}
	return store.writeIndex(raw)
}

// snapshotPlanTypes are the plan types of the launch snapshot of a session of the workflow
// name, whose entry sets plan as its plan type, with workflows the composed stack's table.
// A session whose entry has a review_group, see [startsReviewers], gets every plan type
// that any workflow of the table declares in plan, sorted and with no duplicate: it
// reviews what the other sessions pushed, not only what a gate approved. Any other
// session gets plan alone, nil for "".
func snapshotPlanTypes(workflows map[string]stack.Workflow, name, plan string) []string {
	if !startsReviewers(workflows, name) {
		if plan == "" {
			return nil
		}
		return []string{plan}
	}
	var types []string
	for _, w := range workflows {
		if w.Plan != "" {
			types = append(types, w.Plan)
		}
	}
	slices.Sort(types)
	return slices.Compact(types)
}

// snapshotVersion is the version of artifact in a launch snapshot whose plan types are
// plans: for a type in plans, the newest version that is neither cancelled nor a
// withdrawal; for any other type, the version that a gate approved, by its id, when it
// is not a withdrawal; nil for none.
func snapshotVersion(artifact runapi.Artifact, plans []string) *runapi.Version {
	if slices.Contains(plans, artifact.Type) {
		return newestBeforeWithdrawal(artifact)
	}
	if artifact.ApprovedVersion == nil {
		return nil
	}
	for i, v := range artifact.Versions {
		if v.ID == *artifact.ApprovedVersion && !v.Withdrawn {
			return &artifact.Versions[i]
		}
	}
	return nil
}

// snapshotIndex is index.Raw with only the artifacts that have a version in chosen, by the
// order of index.Artifacts, each with that version alone in versions, as newest_version,
// and as approved_version when a gate approved it, null otherwise. Every other member
// stays as the run API returned it.
func snapshotIndex(index *runapi.Index, chosen []*runapi.Version) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(index.Raw, &top); err != nil {
		return nil, fmt.Errorf("the artifact index does not read: %w", err)
	}
	var artifacts []map[string]json.RawMessage
	if err := json.Unmarshal(top["artifacts"], &artifacts); err != nil || len(artifacts) != len(chosen) {
		return nil, errors.New("the artifacts of the artifact index do not read")
	}
	kept := []map[string]json.RawMessage{}
	for i, entry := range artifacts {
		v := chosen[i]
		if v == nil {
			continue
		}
		var versions []json.RawMessage
		if err := json.Unmarshal(entry["versions"], &versions); err != nil || len(versions) != len(index.Artifacts[i].Versions) {
			return nil, errors.New("the versions of the artifact index do not read")
		}
		for j := range index.Artifacts[i].Versions {
			if index.Artifacts[i].Versions[j].ID == v.ID {
				entry["versions"], _ = json.Marshal([]json.RawMessage{versions[j]})
			}
		}
		entry["newest_version"], _ = json.Marshal(v.Number)
		approved := index.Artifacts[i].ApprovedVersion
		if approved == nil || *approved != v.ID {
			entry["approved_version"] = json.RawMessage("null")
		}
		kept = append(kept, entry)
	}
	top["artifacts"], _ = json.Marshal(kept)
	out, err := json.Marshal(top)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// copyTree copies the folder src, with its folders and regular files, to dst, a path that
// does not exist yet: each folder mode 0755 and each file 0644, so the run's user reads
// them. Anything else in src, such as a link, is an error.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
			return os.Chmod(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(path, target)
		}
		return fmt.Errorf("%s is not a file or a folder", exports.Value(path, false))
	})
}

// copyFile copies the regular file src to dst, a new file of mode 0644.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closed := out.Close(); err == nil {
		err = closed
	}
	if err == nil {
		err = os.Chmod(dst, 0o644)
	}
	return err
}
