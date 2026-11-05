package cmd_test

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/secretmask"
)

// versionWays is the fix of a branch pushed with no version stored, and noType the line of
// a workspace with no artifact type branch.
const (
	versionWays = "add a new version of the artifact branch in Qory Apiary with the head of qory/" + workItem + " on acme/site, or delete that branch on " + proxygittest.Host
	noType      = "Define the artifact type branch in the workspace's Artifact types, then " + versionWays + ", then run the round again"
)

// pushRunNumber is the run's number in the work item of [pushAPI], and changesLeft the
// message of the commit of what the agent of that run left.
const (
	pushRunNumber = 57
	changesLeft   = "Changes left by run 57"
)

// pushAPI is a run API that stores the versions of the artifact branch that a run posts.
// post, when set, gets the number of each POST, from 1, and writes its response when it
// returns true; store then says whether the POST stored its version. index does the same
// for each GET of the artifacts. The counts are of the requests that arrived. number is
// the run's number in the work item, left out of the response when it is 0.
type pushAPI struct {
	server *httptest.Server
	mu     sync.Mutex
	runID  string
	number int
	files  [][]byte
	notes  []string
	// writer, when set, is the run id the index shows, and shownFile the file whose sum it
	// shows, in place of the ones stored.
	writer    string
	shownFile []byte
	bodies    []string
	posts     int
	reads     int
	post      func(n int, w http.ResponseWriter) (handled, store bool)
	index     func(n int, w http.ResponseWriter) bool
}

// newPushAPI serves a run API whose work item has the id [workItem] and the stage st_a.
func newPushAPI(t *testing.T) *pushAPI {
	t.Helper()
	a := &pushAPI{number: pushRunNumber}
	a.server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.server.Close)
	return a
}

// base is the run API's base URL.
func (a *pushAPI) base() string { return a.server.URL + "/v1/runs/r" }

func (a *pushAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/r/work-item":
		run := fmt.Sprintf(`{"id":%q}`, a.runID)
		if a.number != 0 {
			run = fmt.Sprintf(`{"id":%q,"number":%d}`, a.runID, a.number)
		}
		fmt.Fprintf(w, `{"version":1,"work_item":{"id":%q,"number":1},"run":%s,"stage":{"id":"st_a"},"inputs":[],"outputs":[]}`, workItem, run)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/r/artifacts":
		a.reads++
		if a.index == nil || !a.index(a.reads, w) {
			a.writeIndex(w)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/r/artifacts/av_"):
		var n int
		fmt.Sscanf(r.URL.Path, "/v1/runs/r/artifacts/av_%d/files/branch.json", &n)
		w.Write(a.files[n-1])
	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/r/artifacts":
		a.posted(w, r)
	default:
		http.NotFound(w, r)
	}
}

// writeIndex writes the index of the versions stored so far.
func (a *pushAPI) writeIndex(w http.ResponseWriter) {
	versions := make([]map[string]any, len(a.files))
	writer := cmp.Or(a.writer, a.runID)
	for i, file := range a.files {
		if a.shownFile != nil {
			file = a.shownFile
		}
		sum := sha256.Sum256(file)
		versions[i] = map[string]any{
			"id": fmt.Sprintf("av_%d", i+1), "number": i + 1, "stage_id": "st_a", "at": "2026-11-02T09:00:00Z",
			"writer": map[string]any{"kind": "run", "run_id": writer},
			"files": []map[string]any{{
				"filename": "branch.json", "content_type": "application/json", "size": len(file), "sha256": hex.EncodeToString(sum[:]),
			}},
			"approvals": []any{},
		}
	}
	artifacts := []map[string]any{}
	if len(versions) > 0 {
		artifacts = append(artifacts, map[string]any{"type": "branch", "name": "main", "newest_version": len(versions), "versions": versions})
	}
	json.NewEncoder(w).Encode(map[string]any{"version": 1, "artifacts": artifacts})
}

// posted stores the file of a POST of the artifact branch, as the run API does.
func (a *pushAPI) posted(w http.ResponseWriter, r *http.Request) {
	a.posts++
	var body struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Note  string `json:"note"`
		Files []struct {
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
			Content     string `json:"content_base64"`
		} `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Files) != 1 {
		http.Error(w, "{}", http.StatusBadRequest)
		return
	}
	content, err := base64.StdEncoding.DecodeString(body.Files[0].Content)
	if err != nil {
		http.Error(w, "{}", http.StatusBadRequest)
		return
	}
	a.bodies = append(a.bodies, body.Type+" "+body.Name+" "+body.Files[0].Filename+" "+body.Files[0].ContentType)
	a.notes = append(a.notes, body.Note)
	store := true
	if a.post != nil {
		var handled bool
		if handled, store = a.post(a.posts, w); handled && !store {
			return
		}
		if handled {
			a.files = append(a.files, content)
			return
		}
	}
	a.files = append(a.files, content)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"version":1,"artifact":{"type":"branch","name":"main","version":{"id":"av_%d","number":%d}}}`, len(a.files), len(a.files))
}

// counts is how many POST and GET artifacts arrived, and how many versions are stored.
func (a *pushAPI) counts() (posts, reads, stored int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.posts, a.reads, len(a.files)
}

// document is the stored version number n, from 1, checked against the branch format.
func (a *pushAPI) document(t *testing.T, n int) branchformat.Branch {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.files) < n {
		t.Fatalf("%d versions are stored, want %d", len(a.files), n)
	}
	if err := branchformat.Check(a.files[n-1]); err != nil {
		t.Fatalf("version %d: %v", n, err)
	}
	var doc branchformat.Branch
	if err := json.Unmarshal(a.files[n-1], &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// failing is a response of the run API with status and the error code, Retry-After set
// when it is not empty.
func failing(w http.ResponseWriter, status int, code, retry string) {
	if retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

// pushRepo is a run's repository on a stand-in forge that takes a push, with the fixture
// of a write run. h is the run's working folder and run directory, and repositories is
// the list of the run configuration.
type pushRepo struct {
	h            *hostFetch
	api          *pushAPI
	first        string
	bare         string
	repositories string
	change       func(map[string]any)
	flags        []string
	waits        []time.Duration
	setup        func(dir string)
	mask         string
	// pushStarted, when set, runs as the push step starts. removeSetup, when set, is the
	// run's RemoveSetup, and proxyErr the error of the run's HostProxy.
	pushStarted func()
	removeSetup func(context.Context) error
	proxyErr    func() error
	// mountsOf, when set, gets the spec of the agent's session.
	mountsOf func(session.SessionSpec)
	// real has the run's setup, removal and join use a real wall at a gateway in this
	// process. runID and runsDir are those of the last open.
	real           bool
	runID, runsDir string
}

// record is the path of the session's record of the last run.
func (p *pushRepo) record() string { return filepath.Join(p.runsDir, p.runID, "session.jsonl") }

// newPushRepo serves acme/site at one commit, whose stack declares the workflow hello
// with access, and takes a push.
func newPushRepo(t *testing.T, access string) *pushRepo {
	t.Helper()
	return newPushRepoWith(t, access, nil)
}

// newPushRepoWith is [newPushRepo] with the files extra in the repository.
func newPushRepoWith(t *testing.T, access string, extra map[string]string) *pushRepo {
	t.Helper()
	hello := helloFiles(t)
	h := newHostFetch(t, "", "main")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n"
	files := map[string]string{
		"qory.yaml":                       ownStack(stack + "workflows:\n  hello:\n    access: " + access + "\n"),
		"README.md":                       "acme/site\n",
		"modules/hello/qory-module.yaml":  hello["examples/hello/modules/hello/qory-module.yaml"],
		"modules/hello/commands/hello.md": hello["examples/hello/modules/hello/commands/hello.md"],
	}
	maps.Copy(files, extra)
	first := h.server.Repository(t, "acme/site", "main", files)
	p := &pushRepo{h: h, api: newPushAPI(t), first: first, bare: filepath.Join(h.server.Root, "acme", "site.git"), repositories: "acme/site"}
	proxygittest.Git(t, p.bare, "config", "http.receivepack", "true")
	t.Cleanup(cmd.SetBranchWait(func(d time.Duration) { p.waits = append(p.waits, d) }))
	return p
}

// pushed is what a run of a [pushRepo] came to.
type pushed struct {
	out    string
	err    error
	closes []session.Outcome
	steps  []cmd.StepEvent
	calls  []string
	runID  string
}

// succeeded is the result of a session that ended with exit 0.
func succeeded() (*session.Result, error) {
	return &session.Result{State: event.StateSucceeded}, nil
}

// run runs qory run from a run code of the work item [workItem] at the commit p.first,
// with the session that agent stands in for: it writes to the working folder, and its
// result is what session returns, [succeeded] for nil.
func (p *pushRepo) run(t *testing.T, agent func(dir string), result func() (*session.Result, error)) pushed {
	t.Helper()
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		target := map[string]any{"system": proxygittest.Host, "path": "acme/site"}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = p.first
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
		if p.change != nil {
			p.change(d)
		}
	})
	if result == nil {
		result = succeeded
	}
	if !p.real {
		t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
			// The session has registered the run, as a join does before the runtime starts.
			if spec.OnVariables != nil {
				spec.OnVariables(session.Applied{})
			}
			if p.mountsOf != nil {
				p.mountsOf(spec)
			}
			if agent != nil {
				agent(spec.Dir)
			}
			return result()
		}))
	}
	t.Setenv("QORY_RUN_KEY", "")
	var got pushed
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: p.repositories, RunAPI: p.api.base(),
		Proxy: p.h.server.Proxy.URL, CABundle: p.h.server.Proxy.CABundle, Dir: p.h.runDir,
		RemoveSetup: p.removeSetup, HostProxyErr: p.proxyErr, AtGateway: p.real, Real: p.real,
		Setup: func(spec session.SetupSpec) error {
			if p.setup != nil {
				p.setup(spec.Dir)
			}
			return nil
		},
		OnStep: func(e cmd.StepEvent) {
			got.steps = append(got.steps, e)
			if e.Type == "step_started" && e.Kind == event.StepPush && p.pushStarted != nil {
				p.pushStarted()
			}
		},
		OnOpen: func(spec session.OpenSpec) {
			if p.mask != "" {
				secretmask.Default().Add(p.mask)
			}
			got.runID, p.runID, p.runsDir = spec.RunID, spec.RunID, spec.RunsDir
			p.api.mu.Lock()
			defer p.api.mu.Unlock()
			p.api.runID = spec.RunID
		},
	})
	defer restore()
	got.out, got.err = run(t, append([]string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}, p.flags...)...)
	got.closes, got.calls = closes(), calls()
	return got
}

// again gives the next run a new, empty working folder and run directory.
func (p *pushRepo) again(t *testing.T) {
	t.Helper()
	dir := tempDir(t)
	t.Chdir(dir)
	runGit(t, dir, "init", "--quiet", "-b", "main")
	p.h.dir, p.h.runDir = dir, tempDir(t)
}

// branchHead is the commit of the work item's branch on the forge, "" with no branch.
func (p *pushRepo) branchHead(t *testing.T) string {
	t.Helper()
	return proxygittest.Git(t, p.bare, "for-each-ref", "--format=%(objectname)", "refs/heads/qory/"+workItem)
}

// step is the push step's name, and the state it exited with, "" with no push step.
func (r pushed) step() (name, state string) {
	number := 0
	for _, e := range r.steps {
		switch {
		case e.Type == "step_started" && e.Kind == event.StepPush:
			name, number = e.Name, e.Step
		case e.Type == "step_exited" && e.Step == number:
			state = e.State
		}
	}
	return name, state
}

// log is the push step's lines.
func (r pushed) log() string {
	number := 0
	var lines strings.Builder
	for _, e := range r.steps {
		switch {
		case e.Type == "step_started" && e.Kind == event.StepPush:
			number = e.Step
		case e.Type == "log" && e.Step == number:
			lines.WriteString(e.Line)
		}
	}
	return lines.String()
}

// wantsClose fails the test unless the run closed once with state and exited code.
func (r pushed) wantsClose(t *testing.T, state string, code int) {
	t.Helper()
	if len(r.closes) != 1 || r.closes[0].State != state {
		t.Errorf("the run closed with %+v, want %s", r.closes, state)
	}
	if got := cmd.ExitCode(r.err); got != code {
		t.Errorf("exit %d (%v), want %d\n%s", got, r.err, code, r.out)
	}
}

// commitAll commits every file in dir, as an agent does.
func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	proxygittest.Git(t, dir, "add", "-A")
	proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", message)
}

// writesAndCommits is an agent that commits one file and leaves a second uncommitted.
func writesAndCommits(t *testing.T) func(string) {
	return func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, "README.md"), "changed\n")
	}
}

// TestAWriteRunPushesWhatTheAgentLeft is a write run whose agent made a commit and left a
// new file and a change uncommitted, from a depth-1 fetch: the branch on the forge has
// both, one more commit than the agent made with the run's id in its message, on the
// target version, and the run posts one version in the branch format at that commit.
func TestAWriteRunPushesWhatTheAgentLeft(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := gitOut(t, p.h.dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the working folder is shallow: %s, want true", got)
	}
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	if got := gitOut(t, p.h.dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("the forge's branch is at %s, the working folder at %s", head, got)
	}
	for file, want := range map[string]string{"committed.txt": "committed\n", "left.txt": "left\n", "README.md": "changed\n"} {
		if got := proxygittest.Git(t, p.bare, "show", head+":"+file); got != strings.TrimSpace(want) {
			t.Errorf("%s on the branch is %q, want %q", file, got, want)
		}
	}
	if got := proxygittest.Git(t, p.bare, "log", "--format=%s", p.first+".."+head); got != changesLeft+"\nAdd a file" {
		t.Errorf("the branch's new commits are:\n%s", got)
	}
	if got := proxygittest.Git(t, p.bare, "log", "-1", "--format=%an <%ae> %cn <%ce>", head); got != "qory <qory@localhost> qory <qory@localhost>" {
		t.Errorf("the author and the committer of the commit are %s", got)
	}
	if got := proxygittest.Git(t, p.bare, "merge-base", p.first, head); got != p.first {
		t.Errorf("the merge base with the target version is %s", got)
	}
	doc := p.api.document(t, 1)
	want := branchformat.Branch{Ref: "qory/" + workItem, Repositories: []branchformat.Repository{{System: proxygittest.Host, Path: "acme/site", Commit: head}}}
	if doc.Ref != want.Ref || !slices.Equal(doc.Repositories, want.Repositories) {
		t.Errorf("the version is %+v, want %+v", doc, want)
	}
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Errorf("%d POST, want 1", posts)
	}
	if got := p.api.bodies[0]; got != "branch main branch.json application/json" {
		t.Errorf("the POST is %q", got)
	}
	name, state := r.step()
	if want := "push qory/" + workItem + " to " + proxygittest.Host + "/acme/site"; name != want || state != event.StateSucceeded {
		t.Errorf("the push step is %q %s, want %q succeeded\n%+v", name, state, want, r.steps)
	}
	wants(t, r.log(), "qory run: pushed qory/"+workItem+" to acme/site at "+head[:12])
	p.h.noPassword(t, r.out)
}

// TestARunWithNoNewCommitsPushesNothing is a write run whose agent changed nothing: the
// step runs, and writes no branch and no version.
func TestARunWithNoNewCommitsPushesNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
	if posts, reads, _ := p.api.counts(); posts != 0 || reads != 1 {
		t.Errorf("%d POST and %d reads of the index, want 0 and the read of the fetch", posts, reads)
	}
	wants(t, r.log(), "qory run: no new commits to push")
}

// TestAReadRunPushesNothing is a walled run that only reads, with a change in the working
// folder: by the documents' access read, and by a workflow declared read in documents
// with access write. Neither has a push step, a branch on the forge or a version.
func TestAReadRunPushesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		declared string
		change   func(map[string]any)
	}{
		"access read in the documents":   {"write", lowered},
		"a workflow declared read":       {"read", nil},
		"a workflow declared read twice": {"read", lowered},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, c.declared)
			docker, _ := fakeDocker(t)
			walledBy(t, docker)
			p.change, p.flags = c.change, []string{"--home", filepath.Join(tempDir(t), "home")}
			r := p.run(t, func(dir string) { writeFile(t, filepath.Join(dir, "left.txt"), "left\n") }, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if name, _ := r.step(); name != "" {
				t.Errorf("a push step %q", name)
			}
			if head := p.branchHead(t); head != "" {
				t.Errorf("the forge has the branch at %s", head)
			}
			if posts, _, _ := p.api.counts(); posts != 0 {
				t.Errorf("%d POST, want 0", posts)
			}
		})
	}
}

// TestASessionThatDidNotSucceedPushes is a write run whose agent left a change and whose
// session ended another way: the step pushes it, and the commit and the version show that
// end. The run closes as the session ended.
func TestASessionThatDidNotSucceedPushes(t *testing.T) {
	for name, c := range map[string]struct {
		result *session.Result
		state  string
		code   int
		end    string
	}{
		"failed":    {&session.Result{State: event.StateFailed, ExitCode: 3}, event.StateFailed, 3, "The run ended failed: exit 3."},
		"timed out": {&session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, event.StateCancelled, 124, "The run ended cancelled: timeout."},
		"cancelled": {&session.Result{State: event.StateCancelled, Cancelled: true, ExitCode: 130}, event.StateCancelled, 1, "The run ended cancelled: interrupted."},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			r := p.run(t, writesAndCommits(t), func() (*session.Result, error) { return c.result, nil })
			r.wantsClose(t, c.state, c.code)
			if got := r.closes[0]; got.ExitCode != c.result.ExitCode {
				t.Errorf("the run closed with exit %d, want %d", got.ExitCode, c.result.ExitCode)
			}
			if _, state := r.step(); state != event.StateSucceeded {
				t.Errorf("the step ended %q, want succeeded\n%s", state, r.out)
			}
			head := p.branchHead(t)
			if head == "" {
				t.Fatalf("the forge has no branch\n%s", r.out)
			}
			got := proxygittest.Git(t, p.bare, "log", "-1", "--format=%B", head)
			if want := changesLeft + "\n\n" + c.end; got != want {
				t.Errorf("the commit's message is %q, want %q", got, want)
			}
			if len(p.api.notes) != 1 || p.api.notes[0] != c.end {
				t.Errorf("the notes are %q, want %q", p.api.notes, c.end)
			}
		})
	}
}

// TestTheCommitHasTheRunNumberAndNotTheRunID is the subject of the commit of what the agent
// left: the number from the work item, in place of the run's id.
func TestTheCommitHasTheRunNumberAndNotTheRunID(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.number = 41
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	got := proxygittest.Git(t, p.bare, "log", "-1", "--format=%s", p.branchHead(t))
	if got != "Changes left by run 41" {
		t.Errorf("the commit's subject is %q", got)
	}
	if r.runID == "" || strings.Contains(got, r.runID) {
		t.Errorf("the commit's subject %q has the run id %q", got, r.runID)
	}
}

// TestAResponseWithNoRunNumberStopsTheRunBeforeTheJoin is a write run of a work item with
// no run number: the run ends before the agent, so no commit reads "run 0".
func TestAResponseWithNoRunNumberStopsTheRunBeforeTheJoin(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.number = 0
	joined := false
	r := p.run(t, func(string) { joined = true }, nil)
	if cmd.ExitCode(r.err) != 1 || joined {
		t.Fatalf("%v (exit %d, joined %v), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), joined, r.out)
	}
	wants(t, r.out+r.err.Error(), "Check the run's gateway. The response to GET /work-item has no run number; the code is used")
	if name, _ := r.step(); name != "" {
		t.Errorf("the run has the push step %q", name)
	}
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
}

// TestAReadRunNeedsNoRunNumber is a read run of a work item with no run number, by the
// documents' access and by a workflow declared read: it commits nothing, so it runs and
// closes as usual.
func TestAReadRunNeedsNoRunNumber(t *testing.T) {
	for name, c := range map[string]struct {
		declared string
		change   func(map[string]any)
	}{
		"access read in the documents": {"write", lowered},
		"a workflow declared read":     {"read", nil},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, c.declared)
			docker, _ := fakeDocker(t)
			walledBy(t, docker)
			p.change, p.flags = c.change, []string{"--home", filepath.Join(tempDir(t), "home")}
			p.api.number = 0
			joined := false
			r := p.run(t, func(string) { joined = true }, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if !joined {
				t.Errorf("the agent did not run\n%s", r.out)
			}
			if name, _ := r.step(); name != "" {
				t.Errorf("a push step %q", name)
			}
		})
	}
}

// TestASucceededRunShowsNoEnd is the commit and the version of a session that succeeded.
func TestASucceededRunShowsNoEnd(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	got := proxygittest.Git(t, p.bare, "log", "-1", "--format=%B", p.branchHead(t))
	if want := changesLeft; got != want {
		t.Errorf("the commit's message is %q, want %q", got, want)
	}
	if len(p.api.notes) != 1 || p.api.notes[0] != "" {
		t.Errorf("the notes are %q, want one empty note", p.api.notes)
	}
}

// TestAFailedPushKeepsTheEndOfTheSession is a push that fails after a session that did not
// succeed: the step fails and prints its line, and the run closes as the session ended.
func TestAFailedPushKeepsTheEndOfTheSession(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusConflict, "run_ended", "")
		return true, false
	}
	r := p.run(t, writesAndCommits(t), func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
	})
	r.wantsClose(t, event.StateCancelled, 124)
	if got := r.closes[0]; got.Reason != event.ReasonTimeout {
		t.Errorf("the run closed with %+v, want the reason timeout", got)
	}
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the step ended %q, want failed", state)
	}
	wants(t, r.out, "the version of the artifact branch was not stored")
}

// TestARunTheGatewayClosedPushesNothing is a session that ended because Qory Apiary closed
// the run, which takes no version.
func TestARunTheGatewayClosedPushesNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, Reason: "stopped", RunClosed: true}, nil
	})
	if name, _ := r.step(); name != "" {
		t.Errorf("a push step %q", name)
	}
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestASessionThatDidNotStartPushesNothing is a join that returned an error.
func TestASessionThatDidNotStartPushesNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), func() (*session.Result, error) { return nil, errors.New("the join failed") })
	if len(r.closes) != 1 || r.closes[0].State != event.StateFailed {
		t.Errorf("the run closed with %+v, want failed", r.closes)
	}
	if name, _ := r.step(); name != "" {
		t.Errorf("a push step %q", name)
	}
}

// TestQoryAndHarnessLinksStayOutOfTheCommit is an agent that left a file, a harness link
// at the root and one in a folder, files under .qory, and an exclude file with none of
// their lines, and staged a link and a .qory file: the commit has the file alone.
func TestQoryAndHarnessLinksStayOutOfTheCommit(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "harness", "CLAUDE.md"), "the harness\n")
		writeFile(t, filepath.Join(dir, ".qory", "harness", ".claude", "settings.json"), "{}\n")
		writeFile(t, filepath.Join(dir, ".claude", "local.txt"), "the checkout's\n")
		if err := os.Symlink(".qory/harness/CLAUDE.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../.qory/harness/.claude/settings.json", filepath.Join(dir, ".claude", "settings.json")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), "")
		proxygittest.Git(t, dir, "add", "-f", "CLAUDE.md", ".qory/harness/CLAUDE.md")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	got := strings.Fields(proxygittest.Git(t, p.bare, "diff", "--name-only", p.first, head))
	if want := []string{".claude/local.txt", "left.txt"}; !slices.Equal(got, want) {
		t.Errorf("the commit has %q, want %q", got, want)
	}
}

// TestALinkOfTheAgentIsCommitted is a link that does not lead into .qory: it is the
// agent's work.
func TestALinkOfTheAgentIsCommitted(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		if err := os.Symlink("README.md", filepath.Join(dir, "LINK.md")); err != nil {
			t.Fatal(err)
		}
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := proxygittest.Git(t, p.bare, "diff", "--name-only", p.first, p.branchHead(t)); got != "LINK.md" {
		t.Errorf("the commit has %q", got)
	}
}

// TestIgnoredFilesFollowGit is a file that the repository's .gitignore ignores: it stays
// out of the commit, and the .gitignore goes in.
func TestIgnoredFilesFollowGit(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".gitignore"), "*.log\n")
		writeFile(t, filepath.Join(dir, "build.log"), "ignored\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := proxygittest.Git(t, p.bare, "diff", "--name-only", p.first, p.branchHead(t)); got != ".gitignore" {
		t.Errorf("the commit has %q", got)
	}
}

// TestAHookInGitDoesNotRun is an agent that wrote each hook a commit and a push run: none
// runs, and the push goes through.
func TestAHookInGitDoesNotRun(t *testing.T) {
	p := newPushRepo(t, "write")
	mark := filepath.Join(tempDir(t), "ran")
	r := p.run(t, func(dir string) {
		for _, hook := range []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "pre-push"} {
			file := filepath.Join(dir, ".git", "hooks", hook)
			writeFile(t, file, "#!/bin/sh\necho "+hook+" >> "+mark+"\nexit 1\n")
			if err := os.Chmod(file, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got, err := os.ReadFile(mark); err == nil {
		t.Errorf("hooks ran: %s", got)
	}
	if p.branchHead(t) == "" {
		t.Errorf("the forge has no branch\n%s", r.out)
	}
}

// TestAFailedSafetyCheckFailsTheStep is each thing an agent can write in .git that the
// check stops on: the step fails, the run closes failed, and no git writes anything: no
// commit in the working folder, no branch, no version.
func TestAFailedSafetyCheckFailsTheStep(t *testing.T) {
	for name, c := range map[string]struct {
		write func(t *testing.T, dir string)
		want  string
	}{
		"a key that runs a program": {
			func(t *testing.T, dir string) {
				proxygittest.Git(t, dir, "config", "core.sshCommand", "/bin/false")
			},
			"it has core.sshcommand in .git/config, and the check stops on each key that neither git init nor the fetch writes",
		},
		"a hook in the configuration": {
			func(t *testing.T, dir string) {
				proxygittest.Git(t, dir, "config", "hook.mark.command", "/bin/false")
			},
			"it has hook.mark.command in .git/config",
		},
		"a proxy for origin": {
			func(t *testing.T, dir string) {
				proxygittest.Git(t, dir, "config", "remote.origin.proxy", "http://127.0.0.1:9")
			},
			"it has remote.origin.proxy in .git/config",
		},
		"another push URL": {
			func(t *testing.T, dir string) {
				proxygittest.Git(t, dir, "config", "remote.origin.pushurl", "https://elsewhere.example/acme/site.git")
			},
			"it has remote.origin.pushurl in .git/config",
		},
		"a link under .git": {
			func(t *testing.T, dir string) {
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".git", "refs", "elsewhere")); err != nil {
					t.Fatal(err)
				}
			},
			"it has the link .git/refs/elsewhere",
		},
		"alternates": {
			func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, ".git", "objects", "info", "alternates"), "/elsewhere\n")
			},
			"its .git has .git/objects/info/alternates",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			var head string
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
				commitAll(t, dir, "Add a file")
				head = gitOut(t, dir, "rev-parse", "HEAD")
				writeFile(t, filepath.Join(dir, "more.txt"), "more\n")
				c.write(t, dir)
			}, nil)
			r.wantsClose(t, event.StateFailed, 1)
			if _, state := r.step(); state != event.StateFailed {
				t.Errorf("the step ended %q, want failed", state)
			}
			wants(t, r.out, "qory run: take the work item in again in Qory Apiary: after the agent exited, the check of repository acme/site failed, so qory committed and pushed nothing: "+c.want, "the code is used")
			wants(t, r.log(), c.want)
			if got := gitOut(t, p.h.dir, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD moved to %s", got)
			}
			if got := p.branchHead(t); got != "" {
				t.Errorf("the forge has the branch at %s", got)
			}
			if posts, _, _ := p.api.counts(); posts != 0 {
				t.Errorf("%d POST, want 0", posts)
			}
		})
	}
}

// TestARejectedPushFailsTheStep is a branch on the forge that moved since the run fetched
// it: git rejects the push, which is a fast-forward with no force, the branch stays
// where it is, no version is stored, and the run closes failed with the fix first.
func TestARejectedPushFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	writeFile(t, filepath.Join(other, "moved.txt"), "moved\n")
	commitAll(t, other, "Move the branch")
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	moved := p.branchHead(t)

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the step ended %q, want failed", state)
	}
	wants(t, r.out, "qory run: add a new version of the artifact branch in Qory Apiary with the head of qory/"+workItem+" on acme/site, or delete that branch on "+proxygittest.Host+
		", then take the work item in again: the branch moved since the run fetched it, and the push failed: ! [rejected]")
	lacks(t, r.out, proxygittest.Password)
	if got := p.branchHead(t); got != moved {
		t.Errorf("the branch moved to %s", got)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestAHookOfTheForgeFailsTheStep is a forge that stops the update, as a protected branch
// does: the line points at the forge's rule.
func TestAHookOfTheForgeFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	const masked = "value-of-the-run-1234"
	p.mask = masked
	hook := "#!/bin/sh\necho the branch is protected >&2\necho see https://bot:token-456@git.test/rules >&2\necho " + masked + " >&2\nexit 1\n"
	writeFile(t, filepath.Join(p.bare, "hooks", "pre-receive"), hook)
	if err := os.Chmod(filepath.Join(p.bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	head := gitOut(t, p.h.dir, "rev-parse", "HEAD")
	wants(t, r.log(), "qory run: remote: the branch is protected\n", "qory run: remote: see https://(hidden)@git.test/rules\n",
		"qory run: not pushed from acme/site: "+head[:12]+" "+changesLeft+"\n",
		"qory run: not pushed from acme/site: ")
	for _, secret := range []string{"token-456", "bot:", masked} {
		if strings.Contains(r.log(), secret) {
			t.Errorf("the step's log shows %q:\n%s", secret, r.log())
		}
	}
	for line := range strings.Lines(r.out) {
		if strings.Contains(line, "take the work item") || strings.Contains(line, "change the rule on") {
			if strings.Contains(line, "remote:") {
				t.Errorf("the error line has the forge's lines: %q", line)
			}
		}
	}
	wants(t, r.out, "qory run: change the rule on "+proxygittest.Host+" that stops a push to qory/"+workItem+" of acme/site, then take the work item in again: the push failed: ! [remote rejected]")
}

// TestAPushThatARuleRefusesSaysWhatToAllow is a run's proxy that returns 403 to the POST
// of the push, as a policy with path rules does when no rule covers git-receive-pack.
func TestAPushThatARuleRefusesSaysWhatToAllow(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Refuse("/acme/site.git/git-receive-pack")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: add /acme/site.git/git-receive-pack to the path rules of the run's policy for "+proxygittest.Host+
		", and give the run's credential for "+proxygittest.Host+" write access to acme/site, then take the work item in again: the push through the run's gateway returned 403: ")
	if got := p.branchHead(t); got != "" {
		t.Errorf("the forge has the branch at %s", got)
	}
}

// TestARateLimitedPostWaitsAndSendsAgain is a 429 with Retry-After: 2 twice: the step
// waits that long each time and the third POST stores one version.
func TestARateLimitedPostWaitsAndSendsAgain(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(n int, w http.ResponseWriter) (bool, bool) {
		if n <= 2 {
			failing(w, http.StatusTooManyRequests, "rate_limited", "2")
			return true, false
		}
		return false, true
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if posts, reads, stored := p.api.counts(); posts != 3 || stored != 1 || reads != 1 {
		t.Errorf("%d POST, %d reads, %d stored, want 3, the read of the fetch, 1", posts, reads, stored)
	}
	if want := []time.Duration{2 * time.Second, 2 * time.Second}; !slices.Equal(p.waits, want) {
		t.Errorf("the waits are %v, want %v", p.waits, want)
	}
}

// TestARateLimitIsBounded is a 429 to every POST: five go out, the step fails, and the
// run closes failed. A Retry-After past a minute waits a minute.
func TestARateLimitIsBounded(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusTooManyRequests, "rate_limited", "3600")
		return true, false
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if posts, _, stored := p.api.counts(); posts != 5 || stored != 0 {
		t.Errorf("%d POST, %d stored, want 5 and 0", posts, stored)
	}
	if len(p.waits) != 4 || p.waits[0] != time.Minute {
		t.Errorf("the waits are %v, want 4 of a minute", p.waits)
	}
	wants(t, r.out, "qory run: "+versionWays+", then take the work item in again: qory pushed qory/"+workItem+", and the version of the artifact branch was not stored: ")
}

// TestNoResponseToAStoredVersionSendsNothingAgain is a POST that stored its version and
// whose response is a 502: the step reads the index, finds the version and sends no
// second POST. A second case loses the connection.
func TestNoResponseToAStoredVersionSendsNothingAgain(t *testing.T) {
	for name, lose := range map[string]func(http.ResponseWriter){
		"answer_unverified": func(w http.ResponseWriter) { failing(w, http.StatusBadGateway, "answer_unverified", "") },
		"no response": func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
				lose(w)
				return true, true
			}
			r := p.run(t, writesAndCommits(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if posts, reads, stored := p.api.counts(); posts != 1 || stored != 1 || reads != 2 {
				t.Errorf("%d POST, %d reads, %d stored, want 1, 2 and 1", posts, reads, stored)
			}
		})
	}
}

// TestNoResponseWithNoVersionSendsAgain is a POST that stored nothing and came back 503:
// the index has no version, so a second POST stores one.
func TestNoResponseWithNoVersionSendsAgain(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(n int, w http.ResponseWriter) (bool, bool) {
		if n == 1 {
			failing(w, http.StatusServiceUnavailable, "unavailable", "")
			return true, false
		}
		return false, true
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if posts, reads, stored := p.api.counts(); posts != 2 || stored != 1 || reads != 2 {
		t.Errorf("%d POST, %d reads, %d stored, want 2, 2 and 1", posts, reads, stored)
	}
}

// TestNoResponseThatTheIndexDoesNotSettleFails is a request with no response and an index that stays
// unreadable: no second POST goes out, and the step fails with what is unknown.
func TestNoResponseThatTheIndexDoesNotSettleFails(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusServiceUnavailable, "unavailable", "")
		return true, false
	}
	p.api.index = func(n int, w http.ResponseWriter) bool {
		if n == 1 {
			return false
		}
		failing(w, http.StatusServiceUnavailable, "unavailable", "")
		return true
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if posts, reads, _ := p.api.counts(); posts != 1 || reads != 6 {
		t.Errorf("%d POST, %d reads, want 1, and the read of the fetch and 5", posts, reads)
	}
	wants(t, r.out, "qory run: check the artifact branch of the work item in Qory Apiary; with no version of this run, "+versionWays+"; then take the work item in again: qory pushed qory/"+workItem+
		", and the request that stores its version ended with no response, so the version may or may not be stored: ")
}

// TestAWorkspaceWithoutTheTypeFailsTheStep is unknown_type: the step fails after the push,
// with the line of the workflow model and the two ways of a pushed branch.
func TestAWorkspaceWithoutTheTypeFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusUnprocessableEntity, "unknown_type", "")
		return true, false
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the step ended %q, want failed", state)
	}
	wants(t, r.out, "qory run: "+noType+"\n")
	wants(t, r.log(), "qory run: "+noType+"\n")
	if posts, reads, _ := p.api.counts(); posts != 1 || reads != 1 {
		t.Errorf("%d POST, %d reads, want 1 and the read of the fetch", posts, reads)
	}
	if p.branchHead(t) == "" {
		t.Error("the forge has no branch: the push comes before the POST")
	}
}

// TestARunEndedPostFailsTheStep is 409 run_ended: no second POST.
func TestARunEndedPostFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusConflict, "run_ended", "")
		return true, false
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Errorf("%d POST, want 1", posts)
	}
}

// TestASecondRunStartsAtThePushedCommit is two runs of one work item: the second fetches
// the commit of the version the first posted, starts on the branch there, adds a commit,
// and its push is a fast-forward of the first. Its version replaces the commit.
func TestASecondRunStartsAtThePushedCommit(t *testing.T) {
	p := newPushRepo(t, "write")
	first := p.run(t, writesAndCommits(t), nil)
	first.wantsClose(t, event.StateSucceeded, 0)
	pushedHead := p.branchHead(t)

	p.again(t)
	p.api.number = 58
	var startedAt, onBranch string
	second := p.run(t, func(dir string) {
		startedAt = gitOut(t, dir, "rev-parse", "HEAD")
		onBranch = gitOut(t, dir, "symbolic-ref", "--short", "HEAD")
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
	}, nil)
	second.wantsClose(t, event.StateSucceeded, 0)
	if startedAt != pushedHead || onBranch != "qory/"+workItem {
		t.Errorf("the second run started at %s on %s, want %s on the branch", startedAt, onBranch, pushedHead)
	}
	wants(t, second.out, "qory run: on branch qory/"+workItem+" at "+pushedHead[:12])
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "merge-base", pushedHead, head); got != pushedHead || head == pushedHead {
		t.Errorf("the branch went from %s to %s, not as a fast-forward", pushedHead, head)
	}
	if got := proxygittest.Git(t, p.bare, "log", "-1", "--format=%s", head); got != "Changes left by run 58" {
		t.Errorf("the new commit's message is %q", got)
	}
	doc := p.api.document(t, 2)
	if len(doc.Repositories) != 1 || doc.Repositories[0].Commit != head {
		t.Errorf("the second version is %+v, want %s", doc, head)
	}
}

// TestAVersionKeepsAnUnchangedRepository is a run of two repositories that reads a
// version with both, whose agent changed the further one alone: the new version has the
// target's commit as it was and the further one's new commit, and the target has no push.
func TestAVersionKeepsAnUnchangedRepository(t *testing.T) {
	p := newPushRepo(t, "write")
	lib := p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	libBare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, libBare, "config", "http.receivepack", "true")
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "site.txt"), "site\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "lib.txt"), "changed\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	doc := p.api.document(t, 1)
	if len(doc.Repositories) != 2 {
		t.Fatalf("the first version is %+v, want both repositories\n%s", doc, r.out)
	}
	if name, _ := r.step(); name != "push qory/"+workItem+" to "+proxygittest.Host+"/acme/site and 1 further repository" {
		t.Errorf("the push step is %q", name)
	}
	libHead := proxygittest.Git(t, libBare, "rev-parse", "refs/heads/qory/"+workItem)
	if got := proxygittest.Git(t, libBare, "merge-base", lib, libHead); got != lib {
		t.Errorf("the further repository's branch does not descend from %s", lib)
	}
	siteHead := p.branchHead(t)

	p.again(t)
	second := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "second.txt"), "second\n")
	}, nil)
	second.wantsClose(t, event.StateSucceeded, 0)
	next := p.api.document(t, 2)
	want := []branchformat.Repository{
		{System: proxygittest.Host, Path: "acme/site", Commit: siteHead},
		{System: proxygittest.Host, Path: "acme/lib", Commit: proxygittest.Git(t, libBare, "rev-parse", "refs/heads/qory/"+workItem)},
	}
	if !slices.Equal(next.Repositories, want) {
		t.Errorf("the second version has %+v, want %+v", next.Repositories, want)
	}
	if want[1].Commit == libHead {
		t.Error("the further repository did not move")
	}
	if got := p.branchHead(t); got != siteHead {
		t.Errorf("the target moved to %s", got)
	}
}

// TestAWalledRunPushes is a walled run: the check of the settings of a place the run used
// passes after the session, and a further repository too.
func TestAWalledRunPushes(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	proxygittest.Git(t, filepath.Join(p.h.server.Root, "acme", "lib.git"), "config", "http.receivepack", "true")
	p.repositories = "acme/site,acme/lib"
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if doc := p.api.document(t, 1); len(doc.Repositories) != 2 {
		t.Errorf("the version is %+v, want both repositories\n%s", doc, r.out)
	}
}

// TestAHistoryThatLeavesTheStartFailsTheStep is an agent that replaced the commit the run
// started at: on a branch the forge does not have yet any push would pass, so the step
// stops before the push.
func TestAHistoryThatLeavesTheStartFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "--amend", "-m", "Replace the first commit")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: take the work item in again in Qory Apiary, and keep the run's commits on top of commit "+p.first[:12])
	if got := p.branchHead(t); got != "" {
		t.Errorf("the forge has the branch at %s", got)
	}
}

// TestAPushOverOneMebibyteThroughTheProxy is a commit larger than http.postBuffer, which
// git sends chunked. The stand-in proxy is a tunnel, so this covers the git side alone.
func TestAPushOverOneMebibyteThroughTheProxy(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		large := make([]byte, 2<<20)
		for i := range large {
			large[i] = byte(i * 2654435761 >> 13)
		}
		sum := sha256.Sum256(large)
		for i := 0; i < len(large)-32; i += 32 {
			copy(large[i:], sum[:])
			sum = sha256.Sum256(sum[:])
		}
		if err := os.WriteFile(filepath.Join(dir, "large.bin"), large, 0o644); err != nil {
			t.Fatal(err)
		}
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if p.branchHead(t) == "" {
		t.Errorf("the forge has no branch\n%s", r.out)
	}
}

// recordGit puts a git on the PATH that adds one line for each call to the file it
// returns, and then runs the git it replaced: GIT_CONFIG_NOSYSTEM, GIT_CONFIG_GLOBAL, the
// first value of git's configuration from the environment, which is the proxy's URL,
// GIT_CEILING_DIRECTORIES, and the arguments, separated by |. A call with no system or no global setting off reads
// the file config in place of the file of this machine, so a step that loses those
// settings fails the test and never reaches a credential helper of this machine, such
// as one that opens a window on the desktop.
func recordGit(t *testing.T, config string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	calls := filepath.Join(tempDir(t), "calls")
	writeFile(t, calls, "")
	bin := tempDir(t)
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s\\n' \"$GIT_CONFIG_NOSYSTEM\" \"$GIT_CONFIG_GLOBAL\" \"$GIT_CONFIG_VALUE_0\" \"$GIT_CEILING_DIRECTORIES\" \"$*\" >> " + calls + "\n" +
		"if [ \"$GIT_CONFIG_NOSYSTEM\" != 1 ]; then GIT_CONFIG_SYSTEM=" + config + "; export GIT_CONFIG_SYSTEM; fi\n" +
		"GIT_CONFIG_GLOBAL=${GIT_CONFIG_GLOBAL:-" + config + "}; export GIT_CONFIG_GLOBAL\n" +
		"exec " + real + " \"$@\"\n"
	writeFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// subcommand is the first word of args that no -c option comes with.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

// TestEveryGitOfThePushStepIsHardened records each git of the record after the setup and
// of the push step: each reads no system or global configuration file, and each has
// [proxygit.Hardened].
func TestEveryGitOfThePushStepIsHardened(t *testing.T) {
	for name, walled := range map[string]bool{"unwalled": false, "walled": true} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			if walled {
				docker, _ := fakeDocker(t)
				walledBy(t, docker)
				p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
			}
			// The values of the test differ from the ones the step must set.
			empty := filepath.Join(tempDir(t), "gitconfig")
			writeFile(t, empty, "")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
			t.Setenv("GIT_CONFIG_SYSTEM", empty)
			t.Setenv("GIT_CONFIG_GLOBAL", empty)
			calls := recordGit(t, empty)
			// A walled run records what its setup left, so the calls start there.
			const mark = "the calls start here"
			startCalls := func() {
				before, err := os.ReadFile(calls)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, calls, string(before)+mark+"\n")
			}
			if walled {
				p.setup = func(string) { startCalls() }
			}
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
				if !walled {
					startCalls()
				}
			}, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			_, step, ok := strings.Cut(string(log), mark+"\n")
			if !ok {
				t.Fatalf("the file of calls has no line %q:\n%s", mark, log)
			}
			seen := map[string]bool{}
			for line := range strings.Lines(step) {
				parts := strings.SplitN(strings.TrimSuffix(line, "\n"), "|", 5)
				if len(parts) != 5 {
					t.Fatalf("the line %q has no five parts", line)
				}
				nosystem, global, proxy, ceiling, all := parts[0], parts[1], parts[2], parts[3], parts[4]
				args := strings.Fields(all)
				if nosystem != "1" || global != os.DevNull {
					t.Errorf("git %s has GIT_CONFIG_NOSYSTEM=%q and GIT_CONFIG_GLOBAL=%q", all, nosystem, global)
				}
				sub := subcommand(args)
				seen[sub] = true
				if strings.Contains(proxy, "@") && sub != "push" {
					t.Errorf("git %s reaches no remote and has the password of the proxy %q", all, proxy)
				}
				// The checks of a bundle reach no remote: they have their hardening, with
				// protocol.allow=never, in the quarantine they name.
				if walled && strings.Contains(all, "-c core.hooksPath=/dev/null -c core.fsmonitor=false") && strings.Contains(all, "-c protocol.allow=never") {
					continue
				}
				keyList := sub == "config" && strings.Contains(all, "--name-only")
				if !strings.Contains(strings.Join(args, " "), strings.Join(proxygit.Hardened, " ")) {
					t.Errorf("git %s has no options %v", all, proxygit.Hardened)
				}
				if keyList {
					continue
				}
				if ceiling == "" {
					t.Errorf("git %s has no GIT_CEILING_DIRECTORIES", all)
				}
				if !strings.Contains(all, "-c gc.auto=0 -c maintenance.auto=false") {
					t.Errorf("git %s has no maintenance options", all)
				}
			}
			wanted := []string{"config", "status", "reset", "add", "diff", "commit", "rev-parse", "merge-base", "push"}
			if walled {
				wanted = []string{"cat-file", "update-ref", "read-tree", "rev-parse", "push"}
			}
			for _, want := range wanted {
				if !seen[want] {
					t.Errorf("the step ran no git %s:\n%s", want, step)
				}
			}
		})
	}
}

// TestACredentialHelperOfAConfigFileIsNotRead has a system or global configuration file
// with a helper that writes a file. The helper does not run in a run that fetches and
// pushes through a proxy with a password. A plain git with the same file and the same
// proxy runs it, so the test fails if the helper stops being reachable.
func TestACredentialHelperOfAConfigFileIsNotRead(t *testing.T) {
	for name, variable := range map[string]string{"system": "GIT_CONFIG_SYSTEM", "global": "GIT_CONFIG_GLOBAL"} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			marker := filepath.Join(tempDir(t), "helper-ran")
			helper := filepath.Join(tempDir(t), "helper")
			writeFile(t, helper, "#!/bin/sh\nprintf '%s\\n' \"$1\" >> "+marker+"\n")
			if err := os.Chmod(helper, 0o755); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(tempDir(t), "gitconfig")
			writeFile(t, config, "[credential]\n\thelper = "+helper+"\n")
			t.Setenv(variable, config)
			if variable == "GIT_CONFIG_SYSTEM" {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
			}
			recordGit(t, config)
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
			}, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if p.branchHead(t) == "" {
				t.Fatalf("the run pushed nothing:\n%s", r.out)
			}
			if got, err := os.ReadFile(marker); err == nil {
				t.Errorf("the run ran the helper of the %s configuration: %q", name, got)
			}
			proxy := p.h.server.Proxy
			plain := exec.Command("git", "-c", "http.proxy="+proxy.URL, "-c", "http.proxyAuthMethod=basic",
				"-c", "http.sslCAInfo="+proxy.CABundle, "ls-remote", "--", p.h.server.URL("acme/site"))
			plain.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "NO_PROXY=", "no_proxy=")
			if out, err := plain.CombinedOutput(); err != nil {
				t.Fatalf("the plain git failed: %v\n%s", err, out)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("the plain git did not run the helper, so the test shows nothing: %v", err)
			}
		})
	}
}

// walledPush is a walled write run, whose setup writes lock.txt and changes README.md.
func walledPush(t *testing.T) *pushRepo {
	t.Helper()
	p := newPushRepo(t, "write")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	p.setup = func(dir string) {
		writeFile(t, filepath.Join(dir, "lock.txt"), "the setup\n")
		writeFile(t, filepath.Join(dir, "README.md"), "the setup\n")
	}
	return p
}

// TestTheIdentityOfTheAgentDoesNotLoseTheWork is an agent that set user.name and user.email
// to commit, with and without a wall, whose two gates each take those keys. Another key
// stays stopped.
func TestTheIdentityOfTheAgentDoesNotLoseTheWork(t *testing.T) {
	for name, walled := range map[string]bool{"unwalled": false, "walled": true} {
		t.Run(name, func(t *testing.T) {
			var p *pushRepo
			if walled {
				p = walledPush(t)
			} else {
				p = newPushRepo(t, "write")
			}
			r := p.run(t, func(dir string) {
				proxygittest.Git(t, dir, "config", "user.name", "Agent")
				proxygittest.Git(t, dir, "config", "user.email", "agent@example.com")
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
			}, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if p.branchHead(t) == "" {
				t.Errorf("the forge has no branch\n%s", r.out)
			}
		})
	}
}

// TestAnAgentsCommitOfQoryAndALinkKeepsTheNextRun is an agent that committed a file in .qory
// or a link of the compose: the run's commit puts each back, so the next run of the work
// item starts on the branch, and the agent's other work is on the branch.
func TestAnAgentsCommitOfQoryAndALinkKeepsTheNextRun(t *testing.T) {
	for name, what := range map[string]string{"a file in .qory": ".qory", "a link": "link"} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			first := p.run(t, func(dir string) {
				path := filepath.Join(".qory", "mine.txt")
				writeFile(t, filepath.Join(dir, path), "mine\n")
				if what == "link" {
					path = "CLAUDE.md"
					if err := os.Symlink(".qory/mine.txt", filepath.Join(dir, path)); err != nil {
						t.Fatal(err)
					}
				}
				writeFile(t, filepath.Join(dir, "kept.txt"), "kept\n")
				proxygittest.Git(t, dir, "add", "-f", path, "kept.txt")
				proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "the agent")
			}, nil)
			first.wantsClose(t, event.StateSucceeded, 0)
			head := p.branchHead(t)
			if got := proxygittest.Git(t, p.bare, "diff", "--name-only", p.first, head); got != "kept.txt" {
				t.Errorf("the branch differs from the start in %q", got)
			}
			p.again(t)
			second := p.run(t, func(dir string) {
				if got := gitOut(t, dir, "show", "HEAD:kept.txt"); got != "kept" {
					t.Errorf("kept.txt is %q", got)
				}
				writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
			}, nil)
			second.wantsClose(t, event.StateSucceeded, 0)
		})
	}
}

// TestAStaleIndexLockOfAWalledRunStopsNothing is a killed walled agent that left
// .git/index.lock, in the target and in a further repository of its copy: the git session
// uses another git directory, so the push goes on.
func TestAStaleIndexLockOfAWalledRunStopsNothing(t *testing.T) {
	for name, folder := range map[string]string{"target": "", "further": filepath.Join(".qory", "repositories", "acme", "lib")} {
		t.Run(name, func(t *testing.T) {
			p := walledPush(t)
			p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
			proxygittest.Git(t, filepath.Join(p.h.server.Root, "acme", "lib.git"), "config", "http.receivepack", "true")
			p.repositories = "acme/site,acme/lib"
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, folder, "left.txt"), "left\n")
				writeFile(t, filepath.Join(dir, folder, ".git", "index.lock"), "")
			}, func() (*session.Result, error) {
				return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
			})
			r.wantsClose(t, event.StateCancelled, 124)
			if _, state := r.step(); state != event.StateSucceeded {
				t.Errorf("the step ended %q, want succeeded\n%s", state, r.out)
			}
			if posts, _, _ := p.api.counts(); posts != 1 {
				t.Errorf("%d POST, want 1", posts)
			}
		})
	}
}

// TestAnUnwalledRunWithAnIndexLockFailsTheStep is an agent on the host, whose stop leaves
// no proof, in the target and in a further repository: the lock stays, nothing is pushed.
func TestAnUnwalledRunWithAnIndexLockFailsTheStep(t *testing.T) {
	for name, folder := range map[string]string{"target": "", "further": filepath.Join(".qory", "repositories", "acme", "lib")} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
			proxygittest.Git(t, filepath.Join(p.h.server.Root, "acme", "lib.git"), "config", "http.receivepack", "true")
			p.repositories = "acme/site,acme/lib"
			var lock string
			r := p.run(t, func(dir string) {
				lock = filepath.Join(dir, folder, ".git", "index.lock")
				writeFile(t, filepath.Join(dir, folder, "left.txt"), "left\n")
				writeFile(t, lock, "")
			}, nil)
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, "has .git/index.lock and the stop of the agent is not proven")
			if _, err := os.Lstat(lock); err != nil {
				t.Errorf("the lock is gone: %v", err)
			}
			if posts, _, _ := p.api.counts(); posts != 0 {
				t.Errorf("%d POST, want 0", posts)
			}
		})
	}
}

// TestARunThatChangedOnlyWhatTheSetupChangedPushesNothing is a walled run whose agent
// left the working folder as the setup left it.
func TestARunThatChangedOnlyWhatTheSetupChangedPushesNothing(t *testing.T) {
	p := walledPush(t)
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.log(), "qory run: no new commits to push")
	if got := p.branchHead(t); got != "" {
		t.Errorf("the forge has the branch at %s", got)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST went out", posts)
	}
}

// TestTheAgentsChangeBesideTheSetupsIsPushed is a walled run whose agent changed one path
// the setup changed: the commit has that path and not the other.
func TestTheAgentsChangeBesideTheSetupsIsPushed(t *testing.T) {
	p := walledPush(t)
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "README.md"), "the agent\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if p.branchHead(t) == "" {
		t.Fatalf("the run pushed nothing:\n%s", r.out)
	}
	got := proxygittest.Git(t, p.bare, "diff-tree", "-r", "--name-only", "--no-commit-id", p.branchHead(t))
	if got != "README.md" {
		t.Errorf("the commit has %q, want README.md", got)
	}
}

// TestASetupThatAddsAKeyRunsNoProgram is a setup that wrote a key git would use to run a
// program in the copy's .git: the record of what the setup changed does not read it, so the
// agent starts and the program does not run on this machine.
func TestASetupThatAddsAKeyRunsNoProgram(t *testing.T) {
	p := walledPush(t)
	canary := filepath.Join(tempDir(t), "canary")
	p.setup = func(dir string) { proxygittest.Git(t, dir, "config", "core.fsmonitor", "touch "+canary) }
	started := false
	r := p.run(t, func(string) { started = true }, nil)
	if !started {
		t.Errorf("the agent did not start:\n%s", r.out)
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("the program of the setup's key ran on this machine")
	}
}

// TestAReadRunRecordsNothingAfterTheSetup is a read run whose setup wrote a key: the
// record belongs to a run that can push, so the line of the record does not appear.
func TestAReadRunRecordsNothingAfterTheSetup(t *testing.T) {
	p := walledPush(t)
	p.change = lowered
	p.setup = func(dir string) { proxygittest.Git(t, dir, "config", "core.fsmonitor", "/bin/false") }
	r := p.run(t, nil, nil)
	if strings.Contains(r.out, "after the setup, the check of repository") {
		t.Errorf("a read run ran the record:\n%s", r.out)
	}
}

// TestTheMessageOfAnUnverifiedAnswerIsPrintedAsItIs is a 502 answer_unverified to each POST,
// with the message of the gateway's route: the step's line has that message, word for word.
func TestTheMessageOfAnUnverifiedAnswerIsPrintedAsItIs(t *testing.T) {
	const message = "Check the gateway's clock: this line is the route's."
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"answer_unverified","message":%q}`, message)
		return true, false
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "the version of the artifact branch was not stored: "+message+"; the code is used")
}

// TestASecondRepositoryThatFailsGivesTheWays is a run of two repositories whose first push
// landed and whose second failed: no version is stored, and the line has the two ways for
// the first and the fix of the second.
func TestASecondRepositoryThatFailsGivesTheWays(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	bare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, bare, "config", "http.receivepack", "true")
	writeFile(t, filepath.Join(bare, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: "+versionWays+": qory pushed acme/site and stored no version, because the push of the next repository failed: change the rule on "+proxygittest.Host)
	if p.branchHead(t) == "" {
		t.Error("the first repository has no branch")
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST went out", posts)
	}
}

// hangingPost makes each POST of a version stay open with no response, as a run API that
// stopped does, and calls then, which sends the signals of the test.
func hangingPost(t *testing.T, p *pushRepo, then func()) {
	t.Helper()
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true, false
		}
		t.Cleanup(func() { conn.Close() })
		if then != nil {
			then()
		}
		return true, false
	}
}

// term sends this process SIGTERM, which qory run under test handles, and waits so the next
// one counts as a second signal.
func term(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Error(err)
	}
	time.Sleep(100 * time.Millisecond)
}

// TestAStopAfterThePushShowsBothWaysOut is a step that a limit or a signal stopped during
// the request that stores the version, after the push: the close, the exit status, and a
// line that starts with the check of the version and has the cause.
func TestAStopAfterThePushShowsBothWaysOut(t *testing.T) {
	timedOut := func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
	}
	for name, c := range map[string]struct {
		result     func() (*session.Result, error)
		limit      string
		signals    int
		state, why string
		code       int
		cause      string
	}{
		"the run's time limit": {
			limit: "\n    time_limit: 3s", state: event.StateCancelled, why: event.ReasonTimeout, code: 124,
			cause: "the run reached its time limit of 3s",
		},
		"past a session that timed out": {
			result: timedOut, limit: "\n    time_limit: 3s", state: event.StateCancelled, why: event.ReasonTimeout, code: 124,
			cause: "the step reached its limit of 200ms",
		},
		"a signal": {
			signals: 1, state: event.StateCancelled, why: event.ReasonInterrupted, code: 1,
			cause: "qory run got SIGTERM, and the step reached its limit of 400ms",
		},
		"a second signal": {
			signals: 2, state: event.StateCancelled, why: event.ReasonInterrupted, code: 1,
			cause: "qory run got a second signal",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write"+c.limit)
			defer cmd.SetPushTimes(200*time.Millisecond, 400*time.Millisecond)()
			hangingPost(t, p, func() {
				for range c.signals {
					term(t)
				}
			})
			r := p.run(t, writesAndCommits(t), c.result)
			r.wantsClose(t, c.state, c.code)
			if got := r.closes[0].Reason; got != c.why {
				t.Errorf("the reason of the close is %q, want %q", got, c.why)
			}
			if _, state := r.step(); state != event.StateCancelled {
				t.Errorf("the step ended %q, want cancelled\n%s", state, r.out)
			}
			if p.branchHead(t) == "" {
				t.Errorf("the forge has no branch\n%s", r.out)
			}
			wants(t, r.log(), "qory run: pushed qory/"+workItem+" to acme/site at ")
			wants(t, r.out, "check the artifact branch of the work item in Qory Apiary; with no version of this run, "+versionWays+
				"; then take the work item in again: qory pushed acme/site, and "+c.cause+", so the version may or may not be stored; the code is used")
			lacks(t, r.out, "may not have reached the branch")
		})
	}
}

// TestAStopBeforeThePushLands is a second signal during the push, which a hook of the forge
// holds open: the line has the fix, and no version is stored.
func TestAStopBeforeThePushLands(t *testing.T) {
	p := newPushRepo(t, "write")
	writeFile(t, filepath.Join(p.bare, "hooks", "pre-receive"), "#!/bin/sh\nsleep 2\nexit 1\n")
	if err := os.Chmod(filepath.Join(p.bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.pushStarted = func() {
		go func() {
			time.Sleep(500 * time.Millisecond)
			term(t)
			term(t)
		}()
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateCancelled, 1)
	wants(t, r.out, "take the work item in again in Qory Apiary: qory run got a second signal during the push step, and the run's commits may not have reached the branch; the code is used")
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestAVersionOfAnotherRunOrContentIsNotStored is a POST with no response and an index with
// a version that is not this one: the step sends the POST again.
func TestAVersionOfAnotherRunOrContentIsNotStored(t *testing.T) {
	for name, change := range map[string]func(a *pushAPI){
		"another run":   func(a *pushAPI) { a.writer = "run_other" },
		"other content": func(a *pushAPI) { a.shownFile = []byte("other content") },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			change(p.api)
			p.api.post = func(n int, w http.ResponseWriter) (bool, bool) {
				if n == 1 {
					failing(w, http.StatusServiceUnavailable, "unavailable", "")
					return true, true
				}
				return false, true
			}
			r := p.run(t, writesAndCommits(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if posts, _, _ := p.api.counts(); posts != 2 {
				t.Errorf("%d POST, want 2", posts)
			}
		})
	}
}

// TestTheStepClosesItsProxyBeforeTheRunCloses is the host proxy of the step, which ends
// before the close, and every run API and host proxy call before it, which fail after.
func TestTheStepClosesItsProxyBeforeTheRunCloses(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	opened := 0
	for _, call := range r.calls {
		switch call {
		case "host proxy":
			opened++
		case "host proxy closed":
			opened--
		}
	}
	if opened != 0 {
		t.Errorf("%d host proxies stayed open: %q", opened, r.calls)
	}
}

// TestTheStepMasksThePasswordOfItsProxy swaps the set of secret values as the step starts,
// so the password in it after the run is one the step added.
func TestTheStepMasksThePasswordOfItsProxy(t *testing.T) {
	p := newPushRepo(t, "write")
	p.pushStarted = func() {
		t.Cleanup(func(before *secretmask.Set) func() { return func() { secretmask.SetDefault(before) } }(secretmask.SetDefault(secretmask.New())))
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := secretmask.Default().Replace(proxygittest.Password); strings.Contains(got, proxygittest.Password) {
		t.Error("the step did not add the password of its proxy to the secret values")
	}
}

// allSessionEnds are the ends of a session that get the push step.
var allSessionEnds = map[string]func() (*session.Result, error){
	"succeeded": succeeded,
	"failed":    func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 3}, nil },
	"timed out": func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
	},
	"cancelled": func() (*session.Result, error) {
		return &session.Result{State: event.StateCancelled, Cancelled: true, ExitCode: 130}, nil
	},
}

// TestTheSetupContainerGoesBeforeTheStep is a run with a setup container, for each end of
// its session: the removal comes after the session and before the step.
func TestTheSetupContainerGoesBeforeTheStep(t *testing.T) {
	for name, result := range allSessionEnds {
		t.Run(name, func(t *testing.T) {
			p := walledPush(t)
			var joined, removed bool
			p.removeSetup = func(context.Context) error {
				if !joined {
					t.Error("the removal came before the session")
				}
				removed = true
				return nil
			}
			p.pushStarted = func() {
				if !removed {
					t.Error("the step started with the setup container there")
				}
			}
			r := p.run(t, func(dir string) {
				joined = true
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
			}, result)
			if !removed {
				t.Error("no removal")
			}
			if _, state := r.step(); state != event.StateSucceeded {
				t.Errorf("the step ended %q\n%s", state, r.out)
			}
		})
	}
}

// TestAProcessOfTheSetupCannotWriteAfterTheCheck is a process in the setup container that
// outlives the session and writes a filter key into .git/config as soon as git takes the
// lock of the index, after the check. The removal ends it before the step.
func TestAProcessOfTheSetupCannotWriteAfterTheCheck(t *testing.T) {
	p := walledPush(t)
	canary := filepath.Join(tempDir(t), "canary")
	stop, done := make(chan struct{}), make(chan struct{})
	armed := make(chan string, 1)
	go func() {
		defer close(done)
		var dir string
		select {
		case dir = <-armed:
		case <-stop:
			return
		}
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); err == nil {
				config, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
				os.WriteFile(filepath.Join(dir, ".git", "config"), append(config, []byte("[filter \"x\"]\n\tclean = .qory/program.sh\n")...), 0o644)
				return
			}
		}
	}()
	p.removeSetup = func(context.Context) error {
		close(stop)
		<-done
		return nil
	}
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, ".qory", "program.sh"), "#!/bin/sh\nenv > "+canary+"\ncat\n")
		if err := os.Chmod(filepath.Join(dir, ".qory", "program.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, ".gitattributes"), "* filter=x\n")
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		armed <- dir
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if _, err := os.Stat(canary); err == nil {
		t.Error("a program of the process ran on this machine")
	}
	config, err := os.ReadFile(filepath.Join(p.h.dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(config), "filter")
}

// TestAFailedRemovalOfTheSetupContainerRunsNoPush is a removal that failed, also cut by a
// second signal: the run has no step, commits nothing and pushes nothing, though the host
// proxy and the steps still work. The close follows the rules of a failed step.
func TestAFailedRemovalOfTheSetupContainerRunsNoPush(t *testing.T) {
	const line = "take the work item in again in Qory Apiary: the setup container of this run was not removed, so a process in it could write the working folder, and qory pushed nothing: "
	timedOut := allSessionEnds["timed out"]
	for name, c := range map[string]struct {
		result      func() (*session.Result, error)
		remove      func(ctx context.Context) error
		state, want string
		code        int
	}{
		"after a session that succeeded": {
			remove: func(context.Context) error { return errors.New("the engine failed") },
			state:  event.StateFailed, want: line + "the engine failed", code: 1,
		},
		"after a session that timed out": {
			result: timedOut, remove: func(context.Context) error { return errors.New("the engine failed") },
			state: event.StateCancelled, want: line + "the engine failed", code: 124,
		},
		"cut by a second signal": {
			remove: func(ctx context.Context) error {
				term(t)
				term(t)
				<-ctx.Done()
				return ctx.Err()
			},
			state: event.StateCancelled, want: line, code: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := walledPush(t)
			p.removeSetup = c.remove
			r := p.run(t, writesAndCommits(t), c.result)
			r.wantsClose(t, c.state, c.code)
			wants(t, r.out, c.want)
			if name, _ := r.step(); name != "" {
				t.Errorf("a push step %q", name)
			}
			if head := p.branchHead(t); head != "" {
				t.Errorf("the forge has the branch at %s", head)
			}
			if posts, _, _ := p.api.counts(); posts != 0 {
				t.Errorf("%d POST, want 0", posts)
			}
			lacksCopyPath(t, r.out)
		})
	}
}

// TestAWallThatStaysRunsNoGit is a HostProxy that fails because the wall of a session could
// not be removed, for each end of the session: the agent may still run, so the step runs
// no git.
func TestAWallThatStaysRunsNoGit(t *testing.T) {
	const refused = "Open a new run. The wall of session 1 could not be removed and may still run, so this run takes no more sessions."
	for name, result := range allSessionEnds {
		t.Run(name, func(t *testing.T) {
			p := walledPush(t)
			t.Cleanup(cmd.SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
				return &session.Result{}, nil
			}))
			empty := filepath.Join(tempDir(t), "gitconfig")
			writeFile(t, empty, "")
			calls := recordGit(t, empty)
			var after int64 = -1
			p.proxyErr = func() error {
				if after < 0 {
					return nil
				}
				return errors.New(refused)
			}
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
				info, err := os.Stat(calls)
				if err != nil {
					t.Fatal(err)
				}
				after = info.Size()
			}, result)
			if _, state := r.step(); state != event.StateFailed {
				t.Errorf("the step ended %q, want failed\n%s", state, r.out)
			}
			wants(t, r.out, refused)
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			// The check of the run's branch name opens no repository.
			for line := range strings.Lines(string(log[after:])) {
				if !strings.Contains(line, " check-ref-format ") {
					t.Errorf("git ran after the session:\n%s", line)
				}
			}
			if head := p.branchHead(t); head != "" {
				t.Errorf("the forge has the branch at %s", head)
			}
		})
	}
}
