package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// workItem is the id of the work item in the documents of [codeDocuments].
const workItem = "wi_8Kx2mQ4vT9"

// branchAPI serves a run API whose work item has one branch version, with file as its
// file, or no version for an empty file, and counts the file's downloads.
func branchAPI(t *testing.T, file string) string {
	t.Helper()
	versions := "[]"
	if file != "" {
		sum := sha256.Sum256([]byte(file))
		versions = fmt.Sprintf(`[{"id":"av_1","number":1,"writer":{"kind":"run","run_id":"r"},"stage_id":"st_a","at":"2026-11-02T09:00:00Z",`+
			`"files":[{"filename":"branch.json","content_type":"application/json","size":%d,"sha256":%q}],"approvals":[]}]`,
			len(file), hex.EncodeToString(sum[:]))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runs/r/work-item":
			fmt.Fprintf(w, `{"version":1,"work_item":{"id":%q,"number":1},"run":{"id":"r","number":57},"stage":{"id":"st_a"},"inputs":[],"outputs":[]}`, workItem)
		case "/v1/runs/r/artifacts":
			fmt.Fprintf(w, `{"version":1,"artifacts":[{"type":"branch","name":"main","newest_version":1,"versions":%s}]}`, versions)
		case "/v1/runs/r/artifacts/av_1/files/branch.json":
			fmt.Fprint(w, file)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1/runs/r"
}

// runOnBranch is [runAtVersion] with the run API at api, and the steps of the run.
func runOnBranch(t *testing.T, h *hostFetch, version, api string, flags ...string) (string, error, keyRun, []cmd.StepEvent) {
	t.Helper()
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		target := map[string]any{"system": proxygittest.Host, "path": "acme/site"}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = version
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
	})
	t.Cleanup(cmd.SetJoinSession(func(session.SessionSpec) {}))
	t.Setenv("QORY_RUN_KEY", "")
	var steps []cmd.StepEvent
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: "acme/site", RunAPI: api,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		OnStep: func(e cmd.StepEvent) { steps = append(steps, e) },
	})
	defer restore()
	out, err := run(t, append([]string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}, flags...)...)
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, steps
}

// branchDocument is a version's file for the branch of [workItem] at commit in acme/site.
func branchDocument(t *testing.T, commit string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"ref":          "qory/" + workItem,
		"repositories": []map[string]string{{"system": proxygittest.Host, "path": "acme/site", "commit": commit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// stepNamed reports whether steps has a started step of kind with the name want.
func stepNamed(steps []cmd.StepEvent, kind, want string) bool {
	return slices.ContainsFunc(steps, func(e cmd.StepEvent) bool {
		return e.Type == "step_started" && e.Kind == kind && e.Name == want
	})
}

// TestARunWorksOnTheBranchAndComposesTheTargetVersion is a run of a work item with a branch
// version whose commit, like the head, has no command hello: the run composes the
// target version, which has it, and the agent's working folder is on the branch.
func TestARunWorksOnTheBranchAndComposesTheTargetVersion(t *testing.T) {
	h, first, head, _ := versionRepository(t)
	out, err, n, steps := runOnBranch(t, h, first, branchAPI(t, branchDocument(t, head)))
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	ref := "qory/" + workItem
	wants(t, out, "qory run: fetched acme/site at "+first[:12]+" in ", "qory run: composed ", "qory run: on branch "+ref+" at "+head[:12])
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("the working folder is at %s, want %s", got, head)
	}
	if got := gitOut(t, h.dir, "symbolic-ref", "--short", "HEAD"); got != ref {
		t.Errorf("the branch is %s, want %s", got, ref)
	}
	if want := "fetch " + proxygittest.Host + "/acme/site at " + ref + " " + head[:7]; !stepNamed(steps, "fetch", want) {
		t.Errorf("no fetch step %q in %+v", want, steps)
	}
}

// TestAWalledRunRecordsTheCheckoutBeforeTheBranch is a walled run whose branch adds a key
// that qory acts on to qory.yaml: the record of the run's places has the target version's
// file, so qory does not act on the key after the run.
func TestAWalledRunRecordsTheCheckoutBeforeTheBranch(t *testing.T) {
	h, first, _, files := versionRepository(t)
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	work := tempDir(t)
	proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(h.server.Root, "acme", "site.git"), ".")
	proxygittest.Git(t, work, "checkout", "--quiet", "-b", "work", first)
	writeFile(t, filepath.Join(work, "qory.yaml"), files["qory.yaml"]+"worktree:\n  run:\n    add: [make setup]\n")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-am", "branch")
	proxygittest.Git(t, work, "push", "--quiet", "origin", "work")
	commit := proxygittest.Git(t, work, "rev-parse", "HEAD")

	out, err, n, _ := runOnBranch(t, h, first, branchAPI(t, branchDocument(t, commit)), "--home", filepath.Join(tempDir(t), "home"))
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != commit {
		t.Errorf("the working folder is at %s, want %s", got, commit)
	}
	_, err = run(t, "config")
	if err == nil || !strings.Contains(err.Error(), "changes worktree.run.add since a walled run began here") {
		t.Errorf("config: %v", err)
	}
}

// TestARunWithNoBranchVersionCreatesTheBranch is the first run of a work item: the branch
// is new, at the target version.
func TestARunWithNoBranchVersionCreatesTheBranch(t *testing.T) {
	h, first, _, _ := versionRepository(t)
	out, err, n, steps := runOnBranch(t, h, first, branchAPI(t, ""))
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	ref := "qory/" + workItem
	if got := gitOut(t, h.dir, "symbolic-ref", "--short", "HEAD"); got != ref {
		t.Errorf("the branch is %s, want %s", got, ref)
	}
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("the working folder is at %s, want %s", got, first)
	}
	if want := "fetch " + proxygittest.Host + "/acme/site at " + ref + " " + first[:7]; !stepNamed(steps, "fetch", want) {
		t.Errorf("no fetch step %q in %+v", want, steps)
	}
}

// TestARunWithTheFeatureOffStaysOnTheDefaultBranch is a server with no run API: the run
// works as it does with no work item.
func TestARunWithTheFeatureOffStaysOnTheDefaultBranch(t *testing.T) {
	h, first, _, _ := versionRepository(t)
	out, err, n, _ := runOnBranch(t, h, first, "")
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	if got := gitOut(t, h.dir, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("the branch is %s, want main", got)
	}
	lacks(t, out, "on branch")
}

// TestARunStopsOnABrokenBranchVersion is each version that no fetch may start from: the
// run stops before the fetch, and closes failed.
func TestARunStopsOnABrokenBranchVersion(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for name, c := range map[string]struct{ file, want string }{
		"another work item": {
			`{"ref":"qory/wi_other","repositories":[{"system":"git.test","path":"acme/site","commit":"` + commit + `"}]}`,
			"Add a new version of the artifact branch in Qory Apiary with the ref qory/" + workItem + ": version 1 has another ref.",
		},
		"not the format": {`{"ref":"main"}`, "Add a new version of the artifact branch in Qory Apiary in place of version 1: fix the branch document"},
		"not JSON":       {"text", "Add a new version of the artifact branch in Qory Apiary in place of version 1: write the branch document"},
	} {
		t.Run(name, func(t *testing.T) {
			h, first, _, _ := versionRepository(t)
			out, err, n, steps := runOnBranch(t, h, first, branchAPI(t, c.file))
			if cmd.ExitCode(err) != 1 || n.joined {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out+err.Error(), c.want, "the code is used")
			if len(steps) != 0 {
				t.Errorf("steps = %+v", steps)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "qory.yaml")); !os.IsNotExist(err) {
				t.Errorf("qory.yaml is fetched: %v", err)
			}
		})
	}
}
