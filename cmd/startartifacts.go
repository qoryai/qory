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
	"strings"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
)

// The folders of a session's launch snapshot: startArtifactsDir in the run directory,
// which no mount reaches and which the push step's scope check reads, and the copy of the
// same name in the run's inputs folder, which the agent reads read-only.
const startArtifactsDir = "start-artifacts"

// textStartArtifacts is the error of a launch snapshot whose fetch failed, with the cause.
const textStartArtifacts = "The fetch of the session's start artifacts failed, so the agent did not start.\n  Cause: %s"

// startArtifacts makes the launch snapshot of a session whose workflow's plan type is
// plan, "" for none: the run folder's copy at runDir/start-artifacts, which it returns,
// and the copy at in/start-artifacts, each made new, so a later session gets its own. The
// snapshot is the newest version of plan that is neither cancelled nor a withdrawal, and
// of each other artifact the version a gate approved, laid out as qory artifact pull
// writes them, with an index.json that lists only those versions. A fetch through the run
// API that fails is a [*reasonError] of run_api_failed. A run with no run API, client
// nil, and a server with the feature workflow_model off get a snapshot with no artifact.
// shortcut: a run with no run API gets an empty snapshot; replace it when a run with no
// control plane checks its scope.
func startArtifacts(ctx context.Context, client *runapi.Client, runDir, in, plan string) (string, error) {
	snapshot := filepath.Join(runDir, startArtifactsDir)
	copied := filepath.Join(in, startArtifactsDir)
	for _, dir := range []string{snapshot, copied} {
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("removing the start artifacts of the session before failed: %w", exports.HideError(err))
		}
	}
	if err := os.Mkdir(snapshot, 0o700); err != nil {
		return "", fmt.Errorf("making the folder of the start artifacts failed: %w", exports.HideError(err))
	}
	if err := fetchStartArtifacts(ctx, client, snapshot, plan); err != nil {
		return "", &reasonError{code: string(contracts.EndCodeRunAPIFailed), text: fmt.Sprintf(textStartArtifacts, strings.TrimSuffix(shownError(err), "."))}
	}
	if err := copyTree(snapshot, copied); err != nil {
		return "", fmt.Errorf("copying the start artifacts to the inputs folder failed: %w", exports.HideError(err))
	}
	return snapshot, nil
}

// launchSnapshot is [startArtifacts] for a session of run, through its run API; with no
// run API, the snapshot has no artifact. A run API whose client cannot be made is the
// fetch's error.
func launchSnapshot(ctx context.Context, run openedRun, in, plan string) (string, error) {
	var client *runapi.Client
	if hasRunAPI(run) {
		httpClient, base, err := run.RunAPI()
		if err == nil {
			client, err = runapi.New(base, httpClient)
		}
		if err != nil {
			return "", &runLineError{&reasonError{code: string(contracts.EndCodeRunAPIFailed), text: fmt.Sprintf(textStartArtifacts, strings.TrimSuffix(shownError(err), "."))}}
		}
	}
	snapshot, err := startArtifacts(ctx, client, run.Dir(), in, plan)
	var reason *reasonError
	if errors.As(err, &reason) {
		return "", &runLineError{err}
	}
	return snapshot, err
}

// fetchStartArtifacts writes the launch snapshot of [startArtifacts] into dir, an empty
// folder, through client; with client nil, an index.json with no artifact alone.
func fetchStartArtifacts(ctx context.Context, client *runapi.Client, dir, plan string) error {
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
		chosen[i] = snapshotVersion(artifact, plan)
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

// snapshotVersion is the version of artifact in a launch snapshot whose plan type is plan:
// for plan, the newest version that is neither cancelled nor a withdrawal; for any other
// type, the version that a gate approved, by its id, when it is not a withdrawal; nil for
// none.
func snapshotVersion(artifact runapi.Artifact, plan string) *runapi.Version {
	if artifact.Type == plan {
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
