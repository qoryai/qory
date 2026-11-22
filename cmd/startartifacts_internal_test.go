package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/runapi"
)

// snapshotAPI is a run API whose index has build-plan with an approved v1, a newer v2
// and a cancelled v3, a proposal with an approved v1 and a newer v2, and a spec with no
// approved version. Each version has one file, whose content is its id.
func snapshotAPI(t *testing.T) *runapi.Client {
	t.Helper()
	file := func(id string) string {
		sum := sha256.Sum256([]byte(id))
		return fmt.Sprintf(`{"filename":"f.md","content_type":"text/markdown","size":%d,"sha256":"%s"}`, len(id), hex.EncodeToString(sum[:]))
	}
	version := func(id string, n int, cancelled bool) string {
		return fmt.Sprintf(`{"id":"%s","number":%d,"writer":{"kind":"run","run_id":"r"},"cancelled":%v,"withdrawn":false,"stage_id":null,"note":null,"at":"2026-11-22T10:00:0%dZ","files":[%s],"approvals":[]}`, id, n, cancelled, n, file(id))
	}
	index := `{"version":1,"artifacts":[` +
		`{"type":"build-plan","name":"main","stage_id":null,"approved_version":"bp1","newest_version":3,"changed_after_approval":true,"versions":[` + version("bp1", 1, false) + `,` + version("bp2", 2, false) + `,` + version("bp3", 3, true) + `]},` +
		`{"type":"proposal","name":"main","stage_id":null,"approved_version":"pr1","newest_version":2,"changed_after_approval":true,"versions":[` + version("pr1", 1, false) + `,` + version("pr2", 2, false) + `]},` +
		`{"type":"spec","name":"main","stage_id":null,"approved_version":null,"newest_version":1,"changed_after_approval":false,"versions":[` + version("sp1", 1, false) + `]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/runs/r")
		if path == "/artifacts" {
			io.WriteString(w, index)
			return
		}
		id, ok := strings.CutPrefix(path, "/artifacts/")
		id, _, _ = strings.Cut(id, "/")
		if !ok || id == "" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, id)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestALaunchSnapshotHoldsThePlanAndTheApprovedVersions is the launch snapshot of a session
// whose plan type is build-plan: the newest build plan that is not cancelled, the approved
// proposal, and no spec, which no gate approved. index.json lists those versions alone, so
// newest_version is each one's number, and approved_version is null for a build plan that
// is not the approved one. A later session's snapshot replaces both copies whole; with no
// plan type, it has the approved build plan.
func TestALaunchSnapshotHoldsThePlanAndTheApprovedVersions(t *testing.T) {
	client := snapshotAPI(t)
	runDir, in := t.TempDir(), t.TempDir()
	snapshot, err := startArtifacts(t.Context(), client, runDir, in, "build-plan")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != filepath.Join(runDir, startArtifactsDir) {
		t.Errorf("snapshot at %s", snapshot)
	}
	for _, dir := range []string{snapshot, filepath.Join(in, startArtifactsDir)} {
		for path, want := range map[string]string{"build-plan/main/v2/f.md": "bp2", "proposal/main/v1/f.md": "pr1"} {
			if got, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(got) != want {
				t.Errorf("%s in %s: %q, %v", path, dir, got, err)
			}
		}
		for _, absent := range []string{"build-plan/main/v1", "build-plan/main/v3", "proposal/main/v2", "spec"} {
			if _, err := os.Stat(filepath.Join(dir, absent)); !os.IsNotExist(err) {
				t.Errorf("%s has %s: %v", dir, absent, err)
			}
		}
		var index struct {
			Artifacts []struct {
				Type            string            `json:"type"`
				Newest          int               `json:"newest_version"`
				Approved        *string           `json:"approved_version"`
				Changed         bool              `json:"changed_after_approval"`
				Versions        []json.RawMessage `json:"versions"`
				VersionsNumbers []struct {
					ID string `json:"id"`
				} `json:"-"`
			} `json:"artifacts"`
		}
		data, err := os.ReadFile(filepath.Join(dir, "index.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &index); err != nil {
			t.Fatal(err)
		}
		if len(index.Artifacts) != 2 || index.Artifacts[0].Type != "build-plan" || index.Artifacts[0].Newest != 2 ||
			index.Artifacts[0].Approved != nil || len(index.Artifacts[0].Versions) != 1 || !strings.Contains(string(index.Artifacts[0].Versions[0]), `"bp2"`) ||
			index.Artifacts[1].Type != "proposal" || index.Artifacts[1].Newest != 1 || index.Artifacts[1].Approved == nil ||
			*index.Artifacts[1].Approved != "pr1" || len(index.Artifacts[1].Versions) != 1 || !index.Artifacts[1].Changed {
			t.Errorf("index.json in %s:\n%s", dir, data)
		}
	}

	stale := filepath.Join(in, startArtifactsDir, "left.txt")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := startArtifacts(t.Context(), client, runDir, in, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a later session keeps the earlier copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, startArtifactsDir, "build-plan", "main", "v1")); err != nil {
		t.Errorf("a session with no plan type lacks the approved build plan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, startArtifactsDir, "build-plan", "main", "v2")); !os.IsNotExist(err) {
		t.Errorf("a session with no plan type has the newest build plan: %v", err)
	}
}

// TestALaunchSnapshotThatFailsEndsRunAPIFailed is a run API that is not reachable: the
// session does not start, and the run closes run_api_failed, with the cause on line 2.
func TestALaunchSnapshotThatFailsEndsRunAPIFailed(t *testing.T) {
	client, err := runapi.New("http://127.0.0.1:1/v1/runs/r", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = startArtifacts(t.Context(), client, t.TempDir(), t.TempDir(), "build-plan")
	var reason *reasonError
	if !errors.As(err, &reason) || reason.code != string(contracts.EndCodeRunAPIFailed) ||
		!strings.HasPrefix(err.Error(), "The fetch of the session's start artifacts failed, so the agent did not start.\n  Cause: ") {
		t.Errorf("got %v", err)
	}
}

// TestALaunchSnapshotWithNoRunAPIIsEmpty is a run with no run API: the snapshot has an
// index.json with no artifact.
func TestALaunchSnapshotWithNoRunAPIIsEmpty(t *testing.T) {
	in := t.TempDir()
	if _, err := startArtifacts(t.Context(), nil, t.TempDir(), in, "build-plan"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(in, startArtifactsDir, "index.json")); err != nil || string(data) != "{\"artifacts\":[]}\n" {
		t.Errorf("index.json: %q, %v", data, err)
	}
}

// TestALaterSessionGetsANewLaunchSnapshot is two launch snapshots of one run: the second
// has none of the files that were in the folders before it, in the run folder and in the
// inputs folder.
func TestALaterSessionGetsANewLaunchSnapshot(t *testing.T) {
	client := snapshotAPI(t)
	runDir, in := t.TempDir(), t.TempDir()
	for range 2 {
		snapshot, err := startArtifacts(t.Context(), client, runDir, in, "build-plan")
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{snapshot, filepath.Join(in, startArtifactsDir)} {
			if _, err := os.Stat(filepath.Join(dir, "stray.md")); err == nil {
				t.Errorf("%s keeps a file of the session before", dir)
			}
			if err := os.WriteFile(filepath.Join(dir, "stray.md"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
