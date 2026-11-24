package cmd_test

import (
	"cmp"
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

// startAPI is the work item that [branchAPI] serves: the file of its one version of the
// artifact pull-request of the target's collection, "" for no version, and the work item's
// pushed commits, each of acme/site by the run r unless the entry has a path with a slash.
type startAPI struct {
	file string
	// pushed is the pushed commits, each "commit" or "path commit", oldest first.
	pushed []string
	// later is the commits that a later run, r2, pushed to acme/site, oldest first, after the
	// commits of pushed.
	later []string
	// stage is the work item's stage, st_a when "", and approved marks the version approved.
	stage    string
	approved bool
	// newer is the stage of a newer version, av_2, that the later run r2 wrote: "" for none.
	newer string
}

// branchAPI serves a run API whose work item has one version of pull-request, with file as
// its file, or no version for an empty file, and the commits pushed as its list.
func branchAPI(t *testing.T, file string, pushed ...string) string {
	t.Helper()
	return startAPI{file: file, pushed: pushed}.serve(t)
}

// serve serves the run API of a.
func (a startAPI) serve(t *testing.T) string {
	t.Helper()
	artifacts := "[]"
	if a.file != "" {
		sum := sha256.Sum256([]byte(a.file))
		approved := ""
		if a.approved {
			approved = `,"approved_version":"av_1"`
		}
		newer, number := "", 1
		if a.newer != "" {
			number = 2
			newer = fmt.Sprintf(`,{"id":"av_2","number":2,"writer":{"kind":"run","run_id":"r2"},"stage_id":%q,"at":"2026-11-03T09:00:00Z","files":[],"approvals":[]}`, a.newer)
		}
		artifacts = fmt.Sprintf(`[{"type":"pull-request","name":%q,"newest_version":%d%s,"versions":[{"id":"av_1","number":1,"writer":{"kind":"run","run_id":"r"},"stage_id":"st_a","at":"2026-11-02T09:00:00Z",`+
			`"files":[{"filename":"pull-request.json","content_type":"application/json","size":%d,"sha256":%q}],"approvals":[]}%s]}]`,
			pullRequestName, number, approved, len(a.file), hex.EncodeToString(sum[:]), newer)
	}
	entries := []map[string]any{}
	for _, p := range a.pushed {
		path, commit, ok := strings.Cut(p, " ")
		if !ok {
			path, commit = "acme/site", p
		}
		entries = append(entries, map[string]any{"system": proxygittest.Host, "path": path, "commit": commit, "run_id": "r", "cancelled": false, "at": "2026-11-02T09:00:00Z"})
	}
	for _, commit := range a.later {
		entries = append(entries, map[string]any{"system": proxygittest.Host, "path": "acme/site", "commit": commit, "run_id": "r2", "cancelled": false, "at": "2026-11-03T09:00:00Z"})
	}
	pushed, _ := json.Marshal(entries)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runs/r/work-item":
			fmt.Fprintf(w, `{"version":1,"work_item":{"id":%q,"number":1},"run":{"id":"r","number":57},"stage":{"id":%q},"inputs":[],"outputs":[],"pushed_commits":%s}`,
				workItem, cmp.Or(a.stage, "st_a"), pushed)
		case "/v1/runs/r/artifacts":
			fmt.Fprintf(w, `{"version":1,"artifacts":%s}`, artifacts)
		case "/v1/runs/r/artifacts/av_1/files/pull-request.json":
			fmt.Fprint(w, a.file)
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
		target := map[string]any{"collection": "R_1001", "address": map[string]any{"system": proxygittest.Host, "path": "acme/site"}}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = version
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
	})
	t.Cleanup(cmd.SetJoinSession(func(session.SessionSpec) {}))
	t.Setenv("QORY_RUN_KEY", "")
	var steps []cmd.StepEvent
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: decoyRepositories, RunAPI: api,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		OnStep: func(e cmd.StepEvent) { steps = append(steps, e) },
	})
	defer restore()
	out, err := run(t, append([]string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}, flags...)...)
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, steps
}

// branchDocument is a version's file of pull-request for the branch of [workItem] at commit
// in acme/site: the target, a title, and the head at commit.
func branchDocument(t *testing.T, commit string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"target": reference("acme/site"),
		"title":  "Add a page",
		"head":   reference(commit),
		"branch": "qory/" + workItem,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// reference is a reference in the collection R_1001 to path on the forge.
func reference(path string) map[string]any {
	return map[string]any{"collection": "R_1001", "address": map[string]any{"system": proxygittest.Host, "path": path}}
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
	out, err, n, steps := runOnBranch(t, h, first, branchAPI(t, branchDocument(t, head), head))
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

// TestARunStartsAtTheTargetsEntryOfTheVersionsRun is a version from a run that pushed to two
// repositories, the target's first: the run starts at the target's commit, not at the other
// repository's later one. A version that the work item's stage did not write, and that is
// approved, is a start too.
func TestARunStartsAtTheTargetsEntryOfTheVersionsRun(t *testing.T) {
	for name, a := range map[string]func(file, head string) startAPI{
		"the stage's newest": func(file, head string) startAPI {
			return startAPI{file: file, pushed: []string{head, "acme/lib " + strings.Repeat("c", 40)}}
		},
		"the approved one": func(file, head string) startAPI {
			return startAPI{file: file, pushed: []string{head}, stage: "st_b", approved: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, first, head, _ := versionRepository(t)
			out, err, n, _ := runOnBranch(t, h, first, a(branchDocument(t, head), head).serve(t))
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != head {
				t.Errorf("the working folder is at %s, want %s", got, head)
			}
		})
	}
}

// TestARunThatStartsAtAnApprovedVersionMovesToNoLaterPush is a version from run r, with its
// commit head, and a later run's commit that descends from head, on the default branch: a run
// of a stage that wrote no version, whose start is the approved version, as Verify, starts
// at head. A run of the stage that wrote the newest version, as Build, starts at the later
// commit.
func TestARunThatStartsAtAnApprovedVersionMovesToNoLaterPush(t *testing.T) {
	for name, c := range map[string]struct {
		api  func(file, head, later string) startAPI
		want func(head, later string) string
	}{
		"a stage that wrote no version stays at the approved commit": {
			func(file, head, later string) startAPI {
				return startAPI{file: file, pushed: []string{head}, later: []string{later}, stage: "st_b", approved: true}
			},
			func(head, _ string) string { return head },
		},
		"the stage's version, under a newer version of another stage, moves to the later commit": {
			func(file, head, later string) startAPI {
				return startAPI{file: file, pushed: []string{head}, later: []string{later}, newer: "st_b", approved: true}
			},
			func(_, later string) string { return later },
		},
		"the stage's newest version moves to the later commit": {
			func(file, head, later string) startAPI {
				return startAPI{file: file, pushed: []string{head}, later: []string{later}}
			},
			func(_, later string) string { return later },
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, first, head, _ := versionRepository(t)
			work := tempDir(t)
			proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(h.server.Root, "acme", "site.git"), ".")
			writeFile(t, filepath.Join(work, "later.txt"), "later\n")
			commitAll(t, work, "A later run's commit")
			proxygittest.Git(t, work, "push", "--quiet", "origin", "main")
			later := proxygittest.Git(t, work, "rev-parse", "HEAD")

			out, err, n, _ := runOnBranch(t, h, first, c.api(branchDocument(t, head), head, later).serve(t))
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			if want, got := c.want(head, later), gitOut(t, h.dir, "rev-parse", "HEAD"); got != want {
				t.Errorf("the working folder is at %s, want %s", got, want)
			}
		})
	}
}

// TestARunStopsOnAVersionWithNoStart is each version that no fetch may start from: the run
// stops before the fetch with the fix line and the cause, and closes failed.
func TestARunStopsOnAVersionWithNoStart(t *testing.T) {
	commit := strings.Repeat("a", 40)
	twice := func(t *testing.T) string {
		raw, _ := json.Marshal(map[string]any{"target": reference("acme/site"), "title": "T", "head": reference(commit), "base": reference(commit)})
		return string(raw)
	}
	for name, c := range map[string]struct {
		file   func(t *testing.T) string
		pushed []string
		want   string
	}{
		"no pushed entry": {
			func(t *testing.T) string { return branchDocument(t, commit) }, nil,
			"Version 1 of the artifact pull-request, from run r, has no pushed commit on " + proxygittest.Host + "/acme/site.",
		},
		"an entry of another repository": {
			func(t *testing.T) string { return branchDocument(t, commit) }, []string{"acme/lib " + commit},
			"Version 1 of the artifact pull-request, from run r, has no pushed commit on " + proxygittest.Host + "/acme/site.",
		},
		"no reference to the commit": {
			func(t *testing.T) string { return branchDocument(t, strings.Repeat("b", 40)) }, []string{commit},
			"Version 1 of the artifact pull-request has 0 top-level references to commit aaaaaaaaaaaa of " + proxygittest.Host + "/acme/site, not 1.",
		},
		"two references to the commit": {
			twice, []string{commit},
			"Version 1 of the artifact pull-request has 2 top-level references to commit aaaaaaaaaaaa of " + proxygittest.Host + "/acme/site, not 1.",
		},
		// The reference's members are read in their case alone: a head whose members are in
		// another case is no reference.
		"a reference in another case": {
			func(t *testing.T) string {
				raw, _ := json.Marshal(map[string]any{"target": reference("acme/site"), "title": "T", "head": map[string]any{"Collection": "R_1001", "Address": map[string]any{"System": proxygittest.Host, "Path": commit}}})
				return string(raw)
			}, []string{commit},
			"Version 1 of the artifact pull-request has 0 top-level references to commit aaaaaaaaaaaa of " + proxygittest.Host + "/acme/site, not 1.",
		},
		"too big": {
			func(*testing.T) string { return strings.Repeat("a", 786432+1) }, []string{commit},
			"Version 1 of the artifact pull-request has no one JSON file of at most 786432 bytes.",
		},
		"not JSON": {
			func(*testing.T) string { return "text" }, []string{commit},
			"Version 1 of the artifact pull-request has no one JSON file of at most 786432 bytes.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, first, _, _ := versionRepository(t)
			out, err, n, steps := runOnBranch(t, h, first, branchAPI(t, c.file(t), c.pushed...))
			if cmd.ExitCode(err) != 1 || n.joined {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out+err.Error(), "Report the run to the instance admin of Qory Apiary: no start found; the code is used\n  "+c.want)
			if len(steps) != 0 {
				t.Errorf("steps = %+v", steps)
			}
			if _, err := os.Stat(filepath.Join(h.dir, "qory.yaml")); !os.IsNotExist(err) {
				t.Errorf("qory.yaml is fetched: %v", err)
			}
		})
	}
}
