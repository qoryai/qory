package cmd_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/integrations/contracts"
	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/secretmask"
)

// deleteBranch is the fix of a branch pushed with no version stored, and noType the line of
// a workspace with no artifact type pull-request.
const (
	deleteBranch = "delete branch qory/" + workItem + " of acme/site on " + proxygittest.Host
	noType       = "Define pull-request in the workspace's Artifact types, then run the round again.\n  Before the round, " + deleteBranch + "."
)

// pullRequestOutputs is the stage's outputs of [pushAPI] by default: pull-request, not
// required, and pullRequestName the artifact of the documents' target R_1001.
const (
	pullRequestOutputs = `[{"type":"pull-request","required":false,"version":null,"check":"missing"}]`
	pullRequestName    = "t-d56309ab9e43e655d2d51d832b8c25f7"
)

// pullRequestProcess is a process.yaml whose type pull-request is delivered after the gate,
// with a target, a title, and each member that qory fills from the push.
var pullRequestProcess = `artifact_types:
  - name: pull-request
    base_view: structured
    size_limit: 65536
    max_files: 1
    deliver: {send: after_gate}
    json_schema:
      type: object
      required: [target, title]
      properties:
        target: {$ref: "` + primitives.URL + `#/$defs/reference"}
        title: {$ref: "` + primitives.URL + `#/$defs/title"}
        text: {type: string}
        commits: {type: array, x-qory-fill: pushed-commits}
        head: {$ref: "` + primitives.URL + `#/$defs/reference", x-qory-fill: pushed-head}
        base: {$ref: "` + primitives.URL + `#/$defs/reference", x-qory-fill: pushed-base}
        branch: {type: string, x-qory-fill: pushed-branch}
        base_branch: {type: string, x-qory-fill: pushed-base-branch}
`

// storedKind is the artifact type, the name, the file name and the writing run of a stored
// version.
type storedKind struct{ typ, name, filename, writer string }

// pushRunNumber is the run's number in the work item of [pushAPI], and changesLeft the
// message of the commit of what the agent of that run left.
const (
	pushRunNumber = 57
	changesLeft   = "Changes left by run 57"
)

// pushAPI is a run API that stores the versions that a run posts.
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
	kinds  []storedKind
	notes  []string
	// outputs is the stage's outputs, as JSON, [pullRequestOutputs] when "". approved, when
	// set, is the id of the approved version of each artifact.
	outputs  string
	approved string
	// rowRuns is the run id of each row of rows, the run that reported it.
	rowRuns []string
	// writer, when set, is the run id the index shows, and shownFile the file whose sum it
	// shows, in place of the ones stored.
	writer    string
	shownFile []byte
	bodies    []string
	posts     int
	reads     int
	post      func(n int, w http.ResponseWriter) (handled, store bool)
	// recorded is the report of pushed commits: each call's body, as it arrived, and the
	// rows that its calls added, in order. record, when set, responds to call n in place of 204,
	// and returns whether it did.
	recorded []string
	rows     []recordedRow
	// pushedCommits is the work item's list of pushed commits, as JSON, "[]" with none, and
	// verdict the newest verdict the work item has, "" for none: a verdict that this run
	// wrote, as a version of the artifact verdict.
	pushedCommits string
	verdict       string
	record        func(n int, w http.ResponseWriter) bool
	index         func(n int, w http.ResponseWriter) bool
	// workItemFails has each GET of the work item end with a 500.
	workItemFails bool
	// other, when set, responds to a request of no route above, and returns whether it did;
	// a request it leaves gets 404.
	other func(w http.ResponseWriter, r *http.Request) bool
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
	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/r/work-item" && a.workItemFails:
		failing(w, http.StatusInternalServerError, "internal", "")
	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/r/work-item":
		run := fmt.Sprintf(`{"id":%q}`, a.runID)
		if a.number != 0 {
			run = fmt.Sprintf(`{"id":%q,"number":%d}`, a.runID, a.number)
		}
		verdict := "null"
		if a.verdict != "" {
			verdict = fmt.Sprintf(`{"verdict":%q,"version":{"id":"vd_1","number":1}}`, a.verdict)
		}
		pushed := cmp.Or(a.pushedCommits, a.pushedRows())
		outputs := cmp.Or(a.outputs, pullRequestOutputs)
		fmt.Fprintf(w, `{"version":1,"work_item":{"id":%q,"number":1},"run":%s,"stage":{"id":"st_a"},"inputs":[],"outputs":%s,"verdict":%s,"pushed_commits":%s}`, workItem, run, outputs, verdict, pushed)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/r/artifacts":
		a.reads++
		if a.index == nil || !a.index(a.reads, w) {
			a.writeIndex(w)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/r/artifacts/av_"):
		var n int
		fmt.Sscanf(r.URL.Path, "/v1/runs/r/artifacts/av_%d/files/", &n)
		if n < 1 || n > len(a.files) || !strings.HasSuffix(r.URL.Path, "/files/"+a.kinds[n-1].filename) {
			http.NotFound(w, r)
			return
		}
		w.Write(a.files[n-1])
	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/r/artifacts":
		a.posted(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/r/pushed-commits":
		a.recordedPush(w, r)
	case a.other != nil && a.other(w, r):
	default:
		http.NotFound(w, r)
	}
}

// recordedRow is a row of the run's list of pushed commits.
type recordedRow struct{ System, Path, Commit string }

// recordedPush is POST pushed-commits as Qory Apiary handles it: 400 for a body that is not
// the contract's, else a row for each commit not in the list yet, and 204.
func (a *pushAPI) recordedPush(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	a.recorded = append(a.recorded, string(raw))
	if a.record != nil && a.record(len(a.recorded), w) {
		return
	}
	var body struct {
		Repositories []recordedRow `json:"repositories"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	full := regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	if err := dec.Decode(&body); err != nil || len(body.Repositories) == 0 || len(body.Repositories) > 64 {
		failing(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	for _, row := range body.Repositories {
		if !full.MatchString(row.Commit) || row.System == "" || row.Path == "" {
			failing(w, http.StatusBadRequest, "invalid_request", "")
			return
		}
	}
	for _, row := range body.Repositories {
		if !slices.Contains(a.rows, row) {
			a.rows = append(a.rows, row)
			a.rowRuns = append(a.rowRuns, a.runID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// reports is how many reports of pushed commits arrived, and the rows that the list has.
func (a *pushAPI) reports() (calls int, rows []recordedRow) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recorded), slices.Clone(a.rows)
}

// pushedRows is the work item's list of pushed commits as Qory Apiary gives it: each row that
// a run reported, with that run's id.
func (a *pushAPI) pushedRows() string {
	entries := []map[string]any{}
	for i, row := range a.rows {
		entries = append(entries, map[string]any{"system": row.System, "path": row.Path, "commit": row.Commit,
			"run_id": a.rowRuns[i], "cancelled": false, "at": "2026-11-02T09:00:00Z"})
	}
	raw, _ := json.Marshal(entries)
	return string(raw)
}

// writeIndex writes the index of the versions stored so far, grouped by type and name.
func (a *pushAPI) writeIndex(w http.ResponseWriter) {
	type group struct {
		kind     storedKind
		versions []map[string]any
	}
	var groups []*group
	for i, file := range a.files {
		if a.shownFile != nil {
			file = a.shownFile
		}
		sum := sha256.Sum256(file)
		kind := a.kinds[i]
		var g *group
		for _, each := range groups {
			if each.kind.typ == kind.typ && each.kind.name == kind.name {
				g = each
			}
		}
		if g == nil {
			g = &group{kind: kind}
			groups = append(groups, g)
		}
		g.versions = append(g.versions, map[string]any{
			"id": fmt.Sprintf("av_%d", i+1), "number": len(g.versions) + 1, "stage_id": "st_a",
			"at": fmt.Sprintf("2026-11-02T09:%02d:00Z", i), "writer": map[string]any{"kind": "run", "run_id": cmp.Or(a.writer, kind.writer)},
			"files": []map[string]any{{
				"filename": kind.filename, "content_type": "application/json", "size": len(file), "sha256": hex.EncodeToString(sum[:]),
			}},
			"approvals": []any{},
		})
	}
	artifacts := []map[string]any{}
	if a.verdict != "" {
		artifacts = append(artifacts, map[string]any{"type": "verdict", "name": "main", "newest_version": 1, "versions": []map[string]any{{
			"id": "vd_1", "number": 1, "stage_id": "st_a", "at": "2026-11-02T09:00:00Z",
			"writer": map[string]any{"kind": "run", "run_id": cmp.Or(a.writer, a.runID)}, "files": []any{}, "approvals": []any{},
		}}})
	}
	for _, g := range groups {
		artifact := map[string]any{"type": g.kind.typ, "name": g.kind.name, "newest_version": len(g.versions), "versions": g.versions}
		if a.approved != "" {
			artifact["approved_version"] = a.approved
		}
		artifacts = append(artifacts, artifact)
	}
	json.NewEncoder(w).Encode(map[string]any{"version": 1, "artifacts": artifacts})
}

// posted stores the file of a POST of a version, as the run API does.
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
	kind := storedKind{body.Type, body.Name, body.Files[0].Filename, a.runID}
	a.notes = append(a.notes, body.Note)
	store := true
	if a.post != nil {
		var handled bool
		if handled, store = a.post(a.posts, w); handled && !store {
			return
		}
		if handled {
			a.files, a.kinds = append(a.files, content), append(a.kinds, kind)
			return
		}
	}
	a.files, a.kinds = append(a.files, content), append(a.kinds, kind)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"version":1,"artifact":{"type":%q,"name":%q,"version":{"id":"av_%d","number":%d}}}`, body.Type, body.Name, len(a.files), len(a.files))
}

// counts is how many POST and GET artifacts arrived, and how many versions are stored.
func (a *pushAPI) counts() (posts, reads, stored int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.posts, a.reads, len(a.files)
}

// document is the stored version number n, from 1, as JSON members.
func (a *pushAPI) document(t *testing.T, n int) map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.files) < n {
		t.Fatalf("%d versions are stored, want %d", len(a.files), n)
	}
	var doc map[string]any
	if err := json.Unmarshal(a.files[n-1], &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// headOf is the commit of the member head of doc, a stored pull-request.
func headOf(doc map[string]any) string { return pathOf(doc["head"]) }

// pathOf is the address path of ref, a reference.
func pathOf(ref any) string {
	m, _ := ref.(map[string]any)
	address, _ := m["address"].(map[string]any)
	path, _ := address["path"].(string)
	return path
}

// commitsOf is the commits of the member commits of doc, oldest first.
func commitsOf(doc map[string]any) []string {
	list, _ := doc["commits"].([]any)
	var out []string
	for _, c := range list {
		out = append(out, pathOf(c))
	}
	return out
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
	onSetup      func(session.SetupSpec)
	mask         string
	// pushStarted, when set, runs as the push step starts. removeSetup, when set, is the
	// run's RemoveSetup, and proxyErr the error of the run's HostProxy and PushProxy.
	pushStarted func()
	removeSetup func(context.Context) error
	proxyErr    func() error
	// pushVia, when set, is the run's push proxy, in place of its host proxy. noPush has
	// a run with no push credential, whose PushProxy returns Forager's error for one.
	pushVia proxygit.Proxy
	noPush  bool
	// onCall, when set, gets each call the run makes, as it is made.
	onCall func(string)
	// mountsOf, when set, gets the spec of the agent's session.
	mountsOf func(session.SessionSpec)
	// onOpen, when set, gets the spec of each open. onSetCheck, when set, gets the check
	// of each SetRunAPICheck, and its error is the call's.
	onOpen     func(session.OpenSpec)
	runAPIErr  func() error
	onSetCheck func(func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error
	// policyImage, when set, is the image the run's policy selects, the run's image in
	// place of the wall's default.
	policyImage string
	// real has the run's setup, removal and join use a real wall at a gateway in this
	// process. runID and runsDir are those of the last open.
	real           bool
	runID, runsDir string
	// gateway, when set, is called by each report over the link with its number, from 1, before
	// the call reaches Qory Apiary: a non-nil error is the gateway's response to the call, and the
	// call goes no further. linkMu guards the numbers and the limits of the calls.
	gateway    func(n int) error
	linkMu     sync.Mutex
	linkCalls  int
	linkLimits []time.Duration
	// nextSession, when set, is the run's next-session request, see [cmd.Nowhere], and
	// stackProxy gives the run a stack proxy at the forge, for a harness document whose
	// modules the compose fetches.
	nextSession func(ctx context.Context, body []byte) ([]byte, error)
	stackProxy  bool
	// noOutput has the session write no pull-request output to its outbox.
	noOutput bool
}

// outboxOf is the value of QORY_OUTBOX in the session's variables, "" with none.
func outboxOf(spec session.SessionSpec) string {
	for _, v := range spec.Variables.Run {
		if value, ok := strings.CutPrefix(v, "QORY_OUTBOX="); ok {
			return value
		}
	}
	return ""
}

// viaLink is each report of the run over the link of its gateway: [pushRepo.gateway] first,
// then the run API of [pushAPI] as Qory Apiary. It notes how long the call has, the time to the
// context's deadline.
func (p *pushRepo) viaLink(ctx context.Context, body []byte) ([]byte, error) {
	p.linkMu.Lock()
	p.linkCalls++
	n := p.linkCalls
	if deadline, ok := ctx.Deadline(); ok {
		p.linkLimits = append(p.linkLimits, time.Until(deadline))
	}
	p.linkMu.Unlock()
	if p.gateway != nil {
		if err := p.gateway(n); err != nil {
			return nil, err
		}
	}
	return cmd.PushedThroughLink(ctx, p.api.base(), body)
}

// record is the path of the session's record of the last run.
func (p *pushRepo) record() string { return filepath.Join(p.runsDir, p.runID, "session.jsonl") }

// newPushRepo serves acme/site at one commit, whose stack declares the workflow hello
// with access, and takes a push. The run is behind the wall of the fake docker, with a
// home outside the checkout, since a run from a run code that may write needs a wall.
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
		"modules/hello/process.yaml":      pullRequestProcess,
	}
	maps.Copy(files, extra)
	first := h.server.Repository(t, "acme/site", "main", files)
	p := &pushRepo{h: h, api: newPushAPI(t), first: first, bare: filepath.Join(h.server.Root, "acme", "site.git"), repositories: "acme/site"}
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
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
		target := map[string]any{"collection": "R_1001", "address": map[string]any{"system": proxygittest.Host, "path": "acme/site"}}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = p.first
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
		d["deliver"] = []any{map[string]any{"type": "pull-request", "send": "after_gate"}}
		if p.change != nil {
			p.change(d)
		}
		// Documents have deliver only at access write, as documents.schema.json requires.
		if d["access"] != "write" {
			delete(d, "deliver")
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
			if outbox := outboxOf(spec); outbox != "" && !p.noOutput {
				writeFile(t, filepath.Join(outbox, "pull-request", "pull-request.json"), `{"title":"Add a page"}`)
			}
			if agent != nil {
				agent(spec.Dir)
			}
			return result()
		}))
	}
	t.Setenv("QORY_RUN_KEY", "")
	var got pushed
	nowhere := cmd.Nowhere{
		System: proxygittest.Host, Repositories: p.repositories, RunAPI: p.api.base(),
		Proxy: p.h.server.Proxy.URL, CABundle: p.h.server.Proxy.CABundle, Dir: p.h.runDir,
		StackProxy: p.h.server.Stack.URL, StackCABundle: p.h.server.Stack.CABundle,
		RemoveSetup: p.removeSetup, HostProxyErr: p.proxyErr, PushProxyErr: p.proxyErr, NoPushProxy: p.noPush, OnCall: p.onCall,
		PushProxy: p.pushVia.URL, PushCABundle: p.pushVia.CABundle, AtGateway: p.real, Real: p.real,
		SetRunAPICheck: p.onSetCheck, RunAPIErr: p.runAPIErr, PolicyImage: p.policyImage, PushedCommits: p.viaLink,
		Setup: func(spec session.SetupSpec) error {
			if p.setup != nil {
				p.setup(spec.Dir)
			}
			if p.onSetup != nil {
				p.onSetup(spec)
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
			if p.onOpen != nil {
				p.onOpen(spec)
			}
			if p.mask != "" {
				secretmask.Default().Add(p.mask)
			}
			got.runID, p.runID, p.runsDir = spec.RunID, spec.RunID, spec.RunsDir
			p.api.mu.Lock()
			defer p.api.mu.Unlock()
			p.api.runID = spec.RunID
		},
		NextSession: p.nextSession,
	}
	if p.stackProxy {
		nowhere.StackProxy, nowhere.StackCABundle = p.h.server.Proxy.URL, p.h.server.Proxy.CABundle
	}
	closes, calls, restore := cmd.OpenNowhereWith(nowhere)
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
// target version, and the run posts one version of pull-request at that commit.
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
	if doc["branch"] != "qory/"+workItem || doc["base_branch"] != "main" || headOf(doc) != head || pathOf(doc["base"]) != p.first || doc["title"] != "Add a page" {
		t.Errorf("the version is %+v, want the branch qory/%s on main, the head %s and the base %s", doc, workItem, head, p.first)
	}
	if target, _ := doc["target"].(map[string]any); target["collection"] != "R_1001" {
		t.Errorf("the version's target is %+v, want the collection R_1001", doc["target"])
	}
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Errorf("%d POST, want 1", posts)
	}
	if got := p.api.bodies[0]; got != "pull-request "+pullRequestName+" pull-request.json application/json" {
		t.Errorf("the POST is %q", got)
	}
	name, state := r.step()
	if want := "push qory/" + workItem + " to " + proxygittest.Host + "/acme/site"; name != want || state != event.StateSucceeded {
		t.Errorf("the push step is %q %s, want %q succeeded\n%+v", name, state, want, r.steps)
	}
	wants(t, r.log(), "qory run: pushed qory/"+workItem+" to acme/site at "+head[:12])
	p.h.noPassword(t, r.out)
}

// TestThePushStepPushesRefsHeadsQoryAndTheWorkItemId is a write run of the work item
// wi_01HZX: the only ref under refs/heads/qory/ on the forge is refs/heads/qory/wi_01HZX,
// and the version's ref is qory/wi_01HZX. Qory Apiary's tests pin the same prefix, so a
// change of the prefix on either side fails a test on both.
func TestThePushStepPushesRefsHeadsQoryAndTheWorkItemId(t *testing.T) {
	p := newPushRepo(t, "write")
	p.change = func(d map[string]any) {
		d["subject"].(map[string]any)["id"] = "wi_01HZX"
		if in, ok := d["inputs"].(map[string]any); ok && in["work_item"] != nil {
			in["work_item"].(map[string]any)["id"] = "wi_01HZX"
		}
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := proxygittest.Git(t, p.bare, "for-each-ref", "--format=%(refname)", "refs/heads/qory/"); got != "refs/heads/qory/wi_01HZX" {
		t.Errorf("the refs under refs/heads/qory/ on the forge are %q, want refs/heads/qory/wi_01HZX\n%s", got, r.out)
	}
	if doc := p.api.document(t, 1); doc["branch"] != "qory/wi_01HZX" {
		t.Errorf("the version's branch is %v, want qory/wi_01HZX", doc["branch"])
	}
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

// TestAWriteRunWhoseInputsListDestinationsPushes is a write run whose documents contain a
// harness document and whose inputs list destinations: qory fetches its target at the
// target version before the session, and the push step pushes what the agent left to the
// work item's branch, as for a run whose inputs list none.
func TestAWriteRunWhoseInputsListDestinationsPushes(t *testing.T) {
	files := harnessFiles(t, "write")
	p := newPushRepo(t, "write")
	harness := servedHarness(t, p.h, files)
	p.change = func(d map[string]any) {
		d["inputs"].(map[string]any)["destinations"] = siteDestinations
		d["harness"] = harness
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.out, "qory run: fetched acme/site at "+p.first[:12]+" in ")
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	name, state := r.step()
	if want := "push qory/" + workItem + " to " + proxygittest.Host + "/acme/site"; name != want || state != event.StateSucceeded {
		t.Errorf("the push step is %q %s, want %q succeeded\n%+v", name, state, want, r.steps)
	}
	if p.h.server.StackTunnels() == 0 {
		t.Error("the stack proxy led no connection, want the harness's fetch")
	}
}

const stepUnreviewedName = "unreviewed"

// TestASessionThatDidNotSucceedPushes is a write run whose agent left a change and whose
// session ended another way: the step pushes it, the commit shows that end, and no version
// of the output is stored. The run closes as the session ended.
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
		"paused":    {&session.Result{State: event.StatePaused, Reason: event.ReasonModelKeyLimit, ExitCode: 143}, event.StatePaused, 1, "The run ended paused: model_key_limit."},
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
			// A session that did not succeed stores no version of its output.
			if posts, _, _ := p.api.counts(); posts != 0 || len(p.api.notes) != 0 {
				t.Errorf("%d POST with the notes %q, want none", posts, p.api.notes)
			}
		})
	}
}

// TestASessionPastItsLimitStopsAndThePushStepRuns is a write run whose documents set
// time_limit_minutes 15, which lasts 1.5s here, and whose agent leaves changes and runs
// past that limit. The session gets what is left of the documents' limit, and ends as a
// run ends at its time limit: cancelled with the reason timeout, exit 124, and the line of
// the time limit. The step after the agent's exit, the push step, then runs, and pushes
// what the agent left with that end in its commit.
func TestASessionPastItsLimitStopsAndThePushStepRuns(t *testing.T) {
	defer cmd.SetSessionMinute(100 * time.Millisecond)()
	const limit = 1500 * time.Millisecond
	p := newPushRepo(t, "write")
	p.change = func(d map[string]any) { d["time_limit_minutes"] = 15 }
	var left time.Duration
	p.mountsOf = func(spec session.SessionSpec) { left = spec.Timeout }
	r := p.run(t, writesAndCommits(t), func() (*session.Result, error) {
		// The agent is still running at the limit, where Forager stops it.
		time.Sleep(left)
		return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
	})
	if left <= 0 || left > limit {
		t.Errorf("the session's time limit is %v, want what is left of %v", left, limit)
	}
	r.wantsClose(t, event.StateCancelled, 124)
	if got := r.closes[0]; got.Reason != event.ReasonTimeout {
		t.Errorf("the run closed with %+v, want the reason timeout", got)
	}
	wants(t, r.out, "the run was cancelled: it reached the time limit of 1.5s, and claude was stopped")
	if name, state := r.step(); name == "" || state != event.StateSucceeded {
		t.Errorf("the push step %q ended %q, want succeeded\n%s", name, state, r.out)
	}
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	if got, want := proxygittest.Git(t, p.bare, "log", "-1", "--format=%B", head), changesLeft+"\n\nThe run ended cancelled: timeout."; got != want {
		t.Errorf("the commit's message is %q, want %q", got, want)
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
// succeed, as a run with no push credential has it: the step fails and prints its line, and
// the run closes as the session ended.
func TestAFailedPushKeepsTheEndOfTheSession(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noPush = true
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
	wants(t, r.out, "this run has no push credential, so qory pushed nothing")
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

// TestAMovedBranchEndsTheRunWithPushHeadChanged is a branch on the forge that another push
// moved since the run's start commit: no commit of a cancelled run is on it, so qory does
// not push, the branch stays, no version is stored, and the run closes failed with the code
// push_head_changed and the text of the refusal.
func TestAMovedBranchEndsTheRunWithPushHeadChanged(t *testing.T) {
	p := newPushRepo(t, "write")
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	writeFile(t, filepath.Join(other, "moved.txt"), "moved\n")
	commitAll(t, other, "Move the branch")
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	moved := p.branchHead(t)
	p.api.pushedCommits = `[]`

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if got := r.closes[0].Reason; got != string(contracts.EndCodePushHeadChanged) {
		t.Errorf("the run closed with the reason %q, want %q", got, contracts.EndCodePushHeadChanged)
	}
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the step ended %q, want failed", state)
	}
	wants(t, r.out, "qory run: Set the branch to the expected commit or delete it; its head is not that commit.\n"+
		"  Branch qory/"+workItem+". Repository "+proxygittest.Host+"/acme/site. Head: "+moved+". Expected: "+p.first+", the start commit. qory did not push.\n"+
		"  Setting the branch to that commit or deleting it loses the commits on it that no version has.")
	lacks(t, r.out, proxygittest.Password)
	if got := p.branchHead(t); got != moved {
		t.Errorf("the branch moved to %s", got)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestTheOutputIsStoredWithItsMarksFilled is a session that writes the pull-request output
// with a title, a text and a member that the schema does not list: the stored version has
// them as written, and the members that qory fills from the push: the commits of the run
// oldest first, the head, the base and both branches. The outbox file is gone after the
// version is stored.
func TestTheOutputIsStoredWithItsMarksFilled(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noOutput = true
	var first, outbox string
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		first = gitHead(t, dir)
		outbox = filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json")
		writeFile(t, outbox, `{"title":"Add a file","text":"A file.","notes":{"steps":1}}`)
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	doc := p.api.document(t, 1)
	if doc["title"] != "Add a file" || doc["text"] != "A file." || doc["notes"] == nil {
		t.Errorf("the version is %+v, want the session's title, text and notes", doc)
	}
	if got := commitsOf(doc); !slices.Equal(got, []string{first, head}) {
		t.Errorf("the commits are %v, want %s then %s", got, first, head)
	}
	if headOf(doc) != head || pathOf(doc["base"]) != p.first || doc["branch"] != "qory/"+workItem || doc["base_branch"] != "main" {
		t.Errorf("the marks are %v %v %v %v", doc["head"], doc["base"], doc["branch"], doc["base_branch"])
	}
	if _, err := os.Lstat(outbox); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the outbox file is still there: %v", err)
	}
}

// TestAFileBesideTheOutputStaysInTheOutbox is a session that writes notes.md beside its
// pull-request output: the version is stored with pull-request.json alone, which leaves the
// outbox, and notes.md stays.
func TestAFileBesideTheOutputStaysInTheOutbox(t *testing.T) {
	p := newPushRepo(t, "write")
	var outbox string
	p.mountsOf = func(spec session.SessionSpec) {
		outbox = outboxOf(spec)
		writeFile(t, filepath.Join(outbox, "pull-request", "notes.md"), "# Notes\n")
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if _, _, stored := p.api.counts(); stored != 1 {
		t.Errorf("%d versions, want 1", stored)
	}
	if _, err := os.Stat(filepath.Join(outbox, "pull-request", "pull-request.json")); !os.IsNotExist(err) {
		t.Errorf("the stored output stays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outbox, "pull-request", "notes.md")); err != nil {
		t.Errorf("notes.md is gone: %v", err)
	}
}

// TestAnOutputWithAMemberThatQoryFillsStoresNothing is a session whose output sets branch,
// a member that qory fills: qory stores no version, and the run fails with the line that says which
// member to remove.
func TestAnOutputWithAMemberThatQoryFillsStoresNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noOutput = true
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		writeFile(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a file","branch":"main"}`)
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "Remove branch from the pull-request output: qory fills it at the push.")
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestAnOutputThatBreaksItsSchemaPostsNothing is a session whose output has no title, which
// the type requires: qory posts nothing, and the run fails with the line that names the type.
func TestAnOutputThatBreaksItsSchemaPostsNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noOutput = true
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		writeFile(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), `{"text":"No title."}`)
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "Fix the pull-request output: it breaks its schema at ")
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestAMissingRequiredOutputFailsTheRun is a stage that requires the pull-request output and
// a session that writes none: the run fails with the line that names the file to write.
func TestAMissingRequiredOutputFailsTheRun(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noOutput = true
	p.api.outputs = `[{"type":"pull-request","required":true,"version":null,"check":"missing"}]`
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "Write pull-request to the outbox: the stage requires it and the session wrote none.\n  .qory/outbox/pull-request/pull-request.json\n")
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestALinkedOutboxFolderIsNotRead is a session that links .qory/outbox/pull-request to a
// folder of this machine that has a pull-request.json with a title: the run stores no
// version, fails with the line that says the output is unreadable, and prints nothing of the
// host's file.
func TestALinkedOutboxFolderIsNotRead(t *testing.T) {
	p := newPushRepo(t, "write")
	p.noOutput = true
	outside := tempDir(t)
	writeFile(t, filepath.Join(outside, "pull-request.json"), `{"title":"From the host"}`)
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		commitAll(t, dir, "Add a file")
		if err := os.MkdirAll(filepath.Join(dir, ".qory", "outbox"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, ".qory", "outbox", "pull-request")); err != nil {
			t.Fatal(err)
		}
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
	wants(t, r.out, "Fix the pull-request output so that qory can read it.\n  ")
	lacks(t, r.out, "From the host")
}

// TestTheOutboxIsNeverInAPushedCommit is a session that writes its output to the outbox and
// then commits everything: git add -A with git commit, and git commit -a, push no commit that
// has a path under .qory/outbox, and the output is still stored.
func TestTheOutboxIsNeverInAPushedCommit(t *testing.T) {
	commits := map[string]func(t *testing.T, dir string){
		"git add -A and git commit": func(t *testing.T, dir string) { commitAll(t, dir, "Add everything") },
		"git commit -a": func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "README.md"), "changed\n")
			proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-a", "-m", "Change the readme")
		},
	}
	for name, commit := range commits {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			r := p.run(t, func(dir string) {
				writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
				writeFile(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a file"}`)
				exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
				if err != nil || strings.Count("\n"+string(exclude), "\n/.qory/outbox/\n") != 1 {
					t.Errorf("the exclude file has the line /.qory/outbox/ %d times, want once: %v", strings.Count("\n"+string(exclude), "\n/.qory/outbox/\n"), err)
				}
				commit(t, dir)
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
			}, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if got := proxygittest.Git(t, p.bare, "log", "--name-only", "--format=%H", "qory/"+workItem); strings.Contains(got, ".qory") {
				t.Errorf("a pushed commit has a path under .qory:\n%s", got)
			}
			if doc := p.api.document(t, 1); doc["title"] != "Add a file" {
				t.Errorf("the title is %v, want the output's", doc["title"])
			}
		})
	}
}

// TestAForcedAddOfTheOutboxStopsThePush is a session that stages its output file with
// git add -f and commits it: qory pushes nothing, stores no version, and the run closes
// failed with a line that states the cause, then the repository, the commit and the path on
// indented lines.
func TestAForcedAddOfTheOutboxStopsThePush(t *testing.T) {
	p := newPushRepo(t, "write")
	var commit string
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "committed.txt"), "committed\n")
		writeFile(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a file"}`)
		proxygittest.Git(t, dir, "add", "-f", ".qory/outbox/pull-request/pull-request.json", "committed.txt")
		proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add the output")
		commit = gitHead(t, dir)
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: Keep .qory/outbox/ out of the commits of this run; qory pushed nothing.\n"+
		"  A commit of this run has a path under .qory/outbox/.\n  Repository "+proxygittest.Host+"/acme/site.\n  Commit "+commit+".\n  Path .qory/outbox/pull-request/pull-request.json.")
	if got := p.branchHead(t); got != "" {
		t.Errorf("the forge has the branch at %s, want none", got)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// earlierPush pushes a commit on top of the commit that the run started at to the branch on
// the forge, as an earlier run of the work item did, and returns it.
func (p *pushRepo) earlierPush(t *testing.T) string {
	t.Helper()
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	proxygittest.Git(t, other, "checkout", "--quiet", p.first)
	writeFile(t, filepath.Join(other, "earlier.txt"), "earlier\n")
	commitAll(t, other, "An earlier run")
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	return p.branchHead(t)
}

// TestACancelledRunsCommitIsReplacedWithALease is a branch whose only commit above the start
// was pushed by a run that a person cancelled: the push replaces it, the branch is the run's
// commit on the start commit, and the version's base is the start commit.
func TestACancelledRunsCommitIsReplacedWithALease(t *testing.T) {
	p := newPushRepo(t, "write")
	old := p.earlierPush(t)
	p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":true,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if head == old {
		t.Fatalf("the branch is still at the cancelled run's commit\n%s", r.out)
	}
	if got := proxygittest.Git(t, p.bare, "merge-base", p.first, head); got != p.first {
		t.Errorf("the merge base is %s, want the start commit", got)
	}
	if got := proxygittest.Git(t, p.bare, "rev-list", "--count", old+"..."+head); got == "0" {
		t.Errorf("the cancelled commit is on the branch")
	}
	if base := pathOf(p.api.document(t, 1)["base"]); base != p.first {
		t.Errorf("the base is %s, want %s", base, p.first)
	}
}

// TestARunStartsAtAnEarlierRunsCommit is a branch whose commit an earlier run pushed and
// nobody cancelled: the run starts there, its push is a fast-forward, and the version's base
// is that commit.
func TestARunStartsAtAnEarlierRunsCommit(t *testing.T) {
	p := newPushRepo(t, "write")
	old := p.earlierPush(t)
	p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "merge-base", old, head); got != old {
		t.Errorf("the merge base with the earlier commit is %s, want it", got)
	}
	if base := pathOf(p.api.document(t, 1)["base"]); base != old {
		t.Errorf("the base is %s, want %s", base, old)
	}
}

// TestACommitOfNoCancelledRunStopsTheLease is a branch with a commit that no recorded run
// pushed: it counts as not cancelled, so the run moves to start there and a head that the
// list does not know is a commit above it only when it descends. The commit that the list
// has as not cancelled is the start, and a second unrecorded commit above it stops the push.
func TestACommitOfNoCancelledRunStopsTheLease(t *testing.T) {
	p := newPushRepo(t, "write")
	old := p.earlierPush(t)
	p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	proxygittest.Git(t, other, "checkout", "--quiet", "qory/"+workItem)
	writeFile(t, filepath.Join(other, "stray.txt"), "stray\n")
	commitAll(t, other, "A stray commit")
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	stray := p.branchHead(t)

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if got := r.closes[0].Reason; got != string(contracts.EndCodePushHeadChanged) {
		t.Errorf("the reason is %q", got)
	}
	if got := p.branchHead(t); got != stray {
		t.Errorf("the branch moved to %s", got)
	}
}

// TestACommitThatNoVersionConfirmsIsReplacedWithALease is a run whose session 1 stored a
// version with the head A and whose session 2 pushed B above A and stored no version: the next
// run starts at A, not at B, its push replaces B with a lease, and its version's base is A.
func TestACommitThatNoVersionConfirmsIsReplacedWithALease(t *testing.T) {
	p := newPushRepo(t, "write")
	first := p.run(t, writesAndCommits(t), nil)
	first.wantsClose(t, event.StateSucceeded, 0)
	a := p.branchHead(t)
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	proxygittest.Git(t, other, "checkout", "--quiet", "qory/"+workItem)
	writeFile(t, filepath.Join(other, "session2.txt"), "session 2\n")
	commitAll(t, other, "Session 2")
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	b := p.branchHead(t)
	p.api.mu.Lock()
	p.api.rows = append(p.api.rows, recordedRow{System: p.api.rows[0].System, Path: p.api.rows[0].Path, Commit: b})
	p.api.rowRuns = append(p.api.rowRuns, first.runID)
	p.api.mu.Unlock()

	p.again(t)
	p.api.number = 58
	var startedAt string
	second := p.run(t, func(dir string) {
		startedAt = gitOut(t, dir, "rev-parse", "HEAD")
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
	}, nil)
	second.wantsClose(t, event.StateSucceeded, 0)
	if startedAt != a {
		t.Errorf("the run started at %s, want A %s, not B %s\n%s", startedAt, a, b, second.out)
	}
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "merge-base", a, head); got != a || head == a {
		t.Errorf("the branch is at %s, want a commit above A", head)
	}
	if got := proxygittest.Git(t, p.bare, "merge-base", b, head); got == b {
		t.Errorf("B is still on the branch at %s", head)
	}
	if base := pathOf(p.api.document(t, 2)["base"]); base != a {
		t.Errorf("the base is %s, want A %s", base, a)
	}
}

// TestTheOutboxIsEmptiedBeforeTheSession is a run from a run code with no repository, in a
// person's checkout whose outbox has a reply output and a folder that an earlier run left,
// beside a file in .qory and one at the top. The session writes no reply: it starts with no
// outbox, no version is stored, and the two other files stay.
func TestTheOutboxIsEmptiedBeforeTheSession(t *testing.T) {
	p, _ := replyRepo(t)
	p.api.outputs = `[{"type":"reply","required":false,"version":null,"check":"missing"}]`
	left := true
	p.mountsOf = func(spec session.SessionSpec) {
		_, err := os.Lstat(outboxOf(spec))
		left = err == nil
	}
	writeFile(t, filepath.Join(p.h.dir, ".qory", "outbox", "reply", "reply.json"), `{"text":"Left by an earlier run."}`)
	writeFile(t, filepath.Join(p.h.dir, ".qory", "outbox", "notes", "a.md"), "# A\n")
	writeFile(t, filepath.Join(p.h.dir, ".qory", "kept.txt"), "kept\n")
	writeFile(t, filepath.Join(p.h.dir, "kept.txt"), "kept\n")
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if left {
		t.Error("the session starts with the outbox of an earlier run")
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
	for _, kept := range []string{filepath.Join(".qory", "kept.txt"), "kept.txt"} {
		if _, err := os.Stat(filepath.Join(p.h.dir, kept)); err != nil {
			t.Errorf("%s is gone: %v", kept, err)
		}
	}
}

// TestAQoryLinkStopsTheRunBeforeTheSession is the run of [TestTheOutboxIsEmptiedBeforeTheSession]
// whose .qory is a link to a folder outside with an outbox: the run stops before the session,
// exit 1, with the fix, and the outbox outside stays.
func TestAQoryLinkStopsTheRunBeforeTheSession(t *testing.T) {
	p, outbox := replyRepo(t)
	outside := tempDir(t)
	writeFile(t, filepath.Join(outside, "outbox", "reply", "reply.json"), `{"text":"Outside."}`)
	if err := os.Symlink(outside, filepath.Join(p.h.dir, ".qory")); err != nil {
		t.Fatal(err)
	}
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if *outbox != "" || r.err == nil {
		t.Fatalf("%v, the session's outbox %q, want the run to stop before the session", r.err, *outbox)
	}
	wants(t, r.err.Error(), "Remove .qory/outbox, then take the work item in again: a session starts with no outbox.\n  Emptying the outbox in ", " failed: .qory is not a folder; the code is used")
	if _, err := os.Stat(filepath.Join(outside, "outbox", "reply", "reply.json")); err != nil {
		t.Errorf("the outbox outside is gone: %v", err)
	}
}

// TestADeliveredTypeThatTheHarnessDoesNotDefineStopsTheRun is documents that deliver notes,
// which the composed process.yaml does not define, beside pull-request: the run stops before
// the session, exit 1, with the fix, and nothing is pushed or stored.
func TestADeliveredTypeThatTheHarnessDoesNotDefineStopsTheRun(t *testing.T) {
	p := newPushRepo(t, "write")
	p.change = func(d map[string]any) {
		d["deliver"] = []any{map[string]any{"type": "pull-request", "send": "after_gate"}, map[string]any{"type": "notes", "send": "after_gate"}}
	}
	joined := false
	p.mountsOf = func(session.SessionSpec) { joined = true }
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	if joined || r.err == nil {
		t.Fatalf("%v, joined %v, want the run to stop before the session\n%s", r.err, joined, r.out)
	}
	wants(t, r.err.Error(), "Define notes in the harness's process.yaml: the run documents deliver it.\n  The composed process.yaml has no artifact type notes, so qory cannot check or fill its output. The run code is used up.")
	if posts, _, _ := p.api.counts(); posts != 0 || p.branchHead(t) != "" {
		t.Errorf("%d POST and the branch at %q, want neither", posts, p.branchHead(t))
	}
}

// TestOnlyAWriteRunHasTheOutboxVariable is the variable QORY_OUTBOX in the session of a run
// at write, with the path of the checkout's .qory/outbox, and its absence in a run the
// workflow lowers to write-no-push: that run stores no outputs.
func TestOnlyAWriteRunHasTheOutboxVariable(t *testing.T) {
	for access, want := range map[string]bool{"write": true, "write-no-push": false} {
		t.Run(access, func(t *testing.T) {
			p := newPushRepo(t, access)
			var found string
			p.mountsOf = func(spec session.SessionSpec) { found = outboxOf(spec) }
			r := p.run(t, writesAndCommits(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if (found != "") != want || want && !strings.HasSuffix(found, filepath.Join(".qory", "outbox")) {
				t.Errorf("the variable is %q, want present: %v", found, want)
			}
		})
	}
}

// gitHead is the commit of HEAD in dir.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return proxygittest.Git(t, dir, "rev-parse", "HEAD")
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

// TestAPushThatARuleRefusesSaysWhatToAllow is a push proxy that returns 403 to the POST
// of the push, as it does for a path that no item of the push credential covers.
func TestAPushThatARuleRefusesSaysWhatToAllow(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Refuse("/acme/site.git/git-receive-pack")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: Contact support: the push was refused (403).\n"+
		"  One of these most likely stopped it:\n"+
		"  1. The run's push proxy refuses a push that is not a plain push to the push_ref of the push credential (for example a signed push or a push with push options), and qory pushes to refs/heads/qory/"+workItem+" (wall:push-ref).\n"+
		"  2. The run's push credential has no item for /acme/site.git/git-receive-pack on "+proxygittest.Host+", and the push proxy refuses every other host and path.\n"+
		"  3. The forge stopped the push: its permission for the push credential, or a branch rule.\n"+
		"  The push of acme/site to "+proxygittest.Host+" returned 403: ")
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
	wants(t, r.out, "qory run: "+deleteBranch+", then take the work item in again: qory pushed qory/"+workItem+", and the version of the artifact pull-request was not stored: ")
	lacks(t, r.out, "add a new version")
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
	wants(t, r.out, "qory run: check the artifact pull-request of the work item in Qory Apiary; with no version of this run, "+deleteBranch+"; then take the work item in again: qory pushed qory/"+workItem+
		", and the request that stores its version ended with no response, so the version may or may not be stored: ")
}

// TestAnOutputThatIsNotStoredStaysInTheOutbox is a POST that fails with run_ended, and a POST
// with no response whose index read fails too, so the store is unknown: the step fails, and
// the output's file stays in the outbox.
func TestAnOutputThatIsNotStoredStaysInTheOutbox(t *testing.T) {
	for name, c := range map[string]struct {
		post  func(n int, w http.ResponseWriter) (bool, bool)
		index func(n int, w http.ResponseWriter) bool
	}{
		"a failed store": {post: func(_ int, w http.ResponseWriter) (bool, bool) {
			failing(w, http.StatusConflict, "run_ended", "")
			return true, false
		}},
		"an unknown store": {
			post: func(_ int, w http.ResponseWriter) (bool, bool) {
				failing(w, http.StatusServiceUnavailable, "unavailable", "")
				return true, false
			},
			index: func(n int, w http.ResponseWriter) bool {
				if n == 1 {
					return false
				}
				failing(w, http.StatusServiceUnavailable, "unavailable", "")
				return true
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.api.post, p.api.index = c.post, c.index
			var outbox string
			p.mountsOf = func(spec session.SessionSpec) { outbox = outboxOf(spec) }
			r := p.run(t, writesAndCommits(t), nil)
			r.wantsClose(t, event.StateFailed, 1)
			if posts, _, _ := p.api.counts(); posts != 1 {
				t.Errorf("%d POST, want 1", posts)
			}
			if _, err := os.Stat(filepath.Join(outbox, "pull-request", "pull-request.json")); outbox == "" || err != nil {
				t.Errorf("the output is not in the outbox %q: %v", outbox, err)
			}
		})
	}
}

// TestAWorkspaceWithoutTheTypeFailsTheStep is unknown_type: the step fails after the push,
// with the line of the workflow model and the fix of a pushed branch.
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
	if headOf(doc) != head || pathOf(doc["base"]) != pushedHead {
		t.Errorf("the second version is %+v, want the head %s on the base %s", doc, head, pushedHead)
	}
	if kinds := p.api.kinds; kinds[0].name != kinds[1].name || kinds[1].name != pullRequestName {
		t.Errorf("the versions are of %+v, want both of the artifact %s", kinds, pullRequestName)
	}
}

// TestAWalledRunPushes is a walled run of two repositories: the check of the settings of a
// place the run used passes after the session, both branches reach the forge, and one version
// is stored, for the documents' target.
func TestAWalledRunPushes(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	libBare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, libBare, "config", "http.receivepack", "true")
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if p.branchHead(t) == "" || proxygittest.Git(t, libBare, "for-each-ref", "--format=%(objectname)", "refs/heads/qory/"+workItem) == "" {
		t.Errorf("a branch is missing on the forge\n%s", r.out)
	}
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Fatalf("%d POST, want 1\n%s", posts, r.out)
	}
	if doc := p.api.document(t, 1); headOf(doc) != p.branchHead(t) {
		t.Errorf("the version's head is %s, want the target's %s", headOf(doc), p.branchHead(t))
	}
}

// TestARunWithReadTargetsStoresOneVersionForItsTarget is a run whose documents list acme/lib
// in read_targets: one version is stored, for the target R_1001 alone, with the commits, the
// head, the base and both branches of the push to acme/site, and the outbox file is gone.
func TestARunWithReadTargetsStoresOneVersionForItsTarget(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	p.repositories = "acme/site,acme/lib"
	p.change = func(d map[string]any) {
		d["read_targets"] = []any{map[string]any{"collection": "R_2002", "address": map[string]any{"system": proxygittest.Host, "path": "acme/lib"}}}
	}
	var outbox string
	p.mountsOf = func(spec session.SessionSpec) { outbox = outboxOf(spec) }
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Fatalf("%d POST, want 1\n%s", posts, r.out)
	}
	if kind := p.api.kinds[0]; kind.name != pullRequestName {
		t.Errorf("the version is in %s, want %s, the artifact of R_1001", kind.name, pullRequestName)
	}
	doc := p.api.document(t, 1)
	head := p.branchHead(t)
	target, _ := doc["target"].(map[string]any)
	commits := commitsOf(doc)
	if target["collection"] != "R_1001" || headOf(doc) != head || pathOf(doc["base"]) != p.first ||
		doc["branch"] != "qory/"+workItem || doc["base_branch"] != "main" || len(commits) == 0 || commits[len(commits)-1] != head {
		t.Errorf("the version is %v, want R_1001 with head %s, base %s and both branches", doc, head, p.first)
	}
	if _, err := os.Lstat(filepath.Join(outbox, "pull-request", "pull-request.json")); outbox == "" || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the outbox file %q is still there: %v", outbox, err)
	}
}

// TestAWalledRunWithAnAddedKeyFailsTheStep is the settings check of a place a walled run
// used, which comes first: the line has its words.
func TestAWalledRunWithAnAddedKeyFailsTheStep(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		proxygittest.Git(t, dir, "config", "core.fsmonitor", "/bin/false")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "after the agent exited, the check of repository acme/site failed, so qory committed and pushed nothing: ", "core.fsmonitor")
	if got := p.branchHead(t); got != "" {
		t.Errorf("the forge has the branch at %s", got)
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
		"exec " + shellQuote(real) + " \"$@\"\n"
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
// of the push step of a walled run: each reads no system or global configuration file,
// and each has [proxygit.Hardened].
func TestEveryGitOfThePushStepIsHardened(t *testing.T) {
	// GIT_CONFIG_NOSYSTEM=0 below lets Apple's git read its gitconfig with
	// credential.helper=osxkeychain, which GIT_CONFIG_SYSTEM does not replace.
	if runtime.GOOS == "darwin" {
		if origin := gitCredentialHelperOrigin(t); origin != "" {
			t.Skipf("git reads credential.helper from %s even with GIT_CONFIG_SYSTEM set, so a git of this test would open the macOS keychain; CI on Linux runs this test", origin)
		}
	}
	p := newPushRepo(t, "write")
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
	p.setup = func(string) { startCalls() }
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
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
		// ls-remote and fetch read the head of the branch through the read proxy, push through
		// the push proxy; every other git reaches no remote.
		if strings.Contains(proxy, "@") && sub != "push" && sub != "ls-remote" && sub != "fetch" {
			t.Errorf("git %s reaches no remote and has the password of the proxy %q", all, proxy)
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
	for _, want := range []string{"config", "status", "reset", "add", "diff", "commit", "rev-parse", "merge-base", "push"} {
		if !seen[want] {
			t.Errorf("the step ran no git %s:\n%s", want, step)
		}
	}
}

// gitCredentialHelperOrigin is the file from which git reads a credential.helper outside
// the test's configuration file, or "" if it reads none. It runs git with an empty system
// file, no global file and an empty home, and with no repository, so what it finds comes
// from a file that GIT_CONFIG_SYSTEM does not replace, such as Apple's
// /Library/Developer/CommandLineTools/usr/share/git-core/gitconfig.
func gitCredentialHelperOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := tempDir(t)
	empty := filepath.Join(dir, "empty")
	writeFile(t, empty, "")
	git := exec.Command("git", "config", "--show-origin", "--get-all", "credential.helper")
	git.Dir = dir
	git.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_CONFIG_SYSTEM=" + empty,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CEILING_DIRECTORIES=" + dir,
		"GIT_TERMINAL_PROMPT=0",
	}
	out, err := git.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 {
		return ""
	}
	if err != nil {
		t.Fatalf("git config credential.helper failed: %v", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	origin, _, _ := strings.Cut(line, "\t")
	return origin
}

// TestACredentialHelperOfAConfigFileIsNotRead has a system or global configuration file
// with a helper that writes a file. The helper does not run in a run that fetches and
// pushes through a proxy with a password. A plain git with the same file and the same
// proxy runs it, so the test fails if the helper stops being reachable.
func TestACredentialHelperOfAConfigFileIsNotRead(t *testing.T) {
	for name, variable := range map[string]string{"system": "GIT_CONFIG_SYSTEM", "global": "GIT_CONFIG_GLOBAL"} {
		t.Run(name, func(t *testing.T) {
			if variable == "GIT_CONFIG_SYSTEM" && runtime.GOOS == "darwin" {
				if origin := gitCredentialHelperOrigin(t); origin != "" {
					t.Skipf("git reads credential.helper from %s even with GIT_CONFIG_SYSTEM set, so the plain git would open the macOS keychain; CI on Linux runs this subtest", origin)
				}
			}
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
			} else {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
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
	p.setup = func(dir string) {
		writeFile(t, filepath.Join(dir, "lock.txt"), "the setup\n")
		writeFile(t, filepath.Join(dir, "README.md"), "the setup\n")
	}
	return p
}

// TestTheIdentityOfTheAgentDoesNotLoseTheWork is an agent that set user.name and user.email
// to commit, behind a wall, whose two gates each take those keys. Another key stays
// stopped.
func TestTheIdentityOfTheAgentDoesNotLoseTheWork(t *testing.T) {
	p := walledPush(t)
	r := p.run(t, func(dir string) {
		proxygittest.Git(t, dir, "config", "user.name", "Agent")
		proxygittest.Git(t, dir, "config", "user.email", "agent@example.com")
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if p.branchHead(t) == "" {
		t.Errorf("the forge has no branch\n%s", r.out)
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

// TestAWalledRunRemovesAStaleIndexLock is a killed walled agent that left .git/index.lock,
// in the target and in a further repository: the join proved its stop, so the push goes on.
func TestAWalledRunRemovesAStaleIndexLock(t *testing.T) {
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
			wants(t, r.log(), "which the agent left when it stopped")
			// The session timed out, so the run stores no version of its output.
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
	got := proxygittest.Git(t, p.h.dir, "diff-tree", "-r", "--name-only", "--no-commit-id", "HEAD")
	if got != "README.md" {
		t.Errorf("the commit has %q, want README.md", got)
	}
}

// TestASetupThatAddsAKeyStopsTheRunBeforeTheAgent is a setup that wrote a key git would
// use to run a program: the run fails before the agent starts, with the line after the setup.
func TestASetupThatAddsAKeyStopsTheRunBeforeTheAgent(t *testing.T) {
	p := walledPush(t)
	p.setup = func(dir string) { proxygittest.Git(t, dir, "config", "core.fsmonitor", "/bin/false") }
	started := false
	r := p.run(t, func(string) { started = true }, nil)
	if started {
		t.Error("the agent started")
	}
	wants(t, r.out, "after the setup, the check of repository acme/site failed, so the agent did not start: ", "core.fsmonitor")
	if strings.Contains(r.out, "qory run: qory run:") {
		t.Errorf("the line has qory run: twice:\n%s", r.out)
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
	wants(t, r.out, "the version of the artifact pull-request was not stored: "+message+"; the code is used")
}

// TestASecondRepositoryThatFailsGivesTheFix is a run of two repositories whose first push
// landed and whose second failed: no version is stored, and the line has the fix for
// the first and the fix of the second.
func TestASecondRepositoryThatFailsGivesTheFix(t *testing.T) {
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
	wants(t, r.out, "qory run: "+deleteBranch+"\n  qory pushed acme/site and stored no version, because the push of the next repository failed: change the rule on "+proxygittest.Host)
	if p.branchHead(t) == "" {
		t.Error("the first repository has no branch")
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST went out", posts)
	}
}

// TestASecondRepositoryWhoseHeadMovedEndsTheRunWithPushHeadChanged is a run of two
// repositories whose first push landed and whose second repository got its branch from
// another push meanwhile: the second push is rejected as a moved head, no version is stored,
// and the run closes failed with the code push_head_changed, not as a plain failure.
func TestASecondRepositoryWhoseHeadMovedEndsTheRunWithPushHeadChanged(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	lib := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, lib, "config", "http.receivepack", "true")
	tree := proxygittest.Git(t, lib, "rev-parse", "main^{tree}")
	moved := proxygittest.Git(t, lib, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit-tree", tree, "-m", "Another push")
	hook := filepath.Join(p.bare, "hooks", "post-receive")
	writeFile(t, hook, "#!/bin/sh\ngit --git-dir="+lib+" update-ref refs/heads/qory/"+workItem+" "+moved+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if got := r.closes[0].Reason; got != string(contracts.EndCodePushHeadChanged) {
		t.Errorf("the run closed with the reason %q, want %q", got, contracts.EndCodePushHeadChanged)
	}
	wants(t, r.out, "qory run: "+deleteBranch+"\n  qory pushed acme/site and stored no version, because the push of the next repository failed: Set the branch to the expected commit or delete it; its head is not that commit.")
	if got := proxygittest.Git(t, lib, "rev-parse", "refs/heads/qory/"+workItem); got != moved {
		t.Errorf("the second repository's branch is at %s, want %s", got, moved)
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
	for name, c := range map[string]struct {
		result func() (*session.Result, error)
		// limited is whether the documents set time_limit_minutes 15, which lasts 3s here.
		limited    bool
		signals    int
		state, why string
		code       int
		cause      string
	}{
		"the run's time limit": {
			limited: true, state: event.StateCancelled, why: event.ReasonTimeout, code: 124,
			cause: "the run reached its time limit of 3s",
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
			p := newPushRepo(t, "write")
			if c.limited {
				p.change = func(d map[string]any) { d["time_limit_minutes"] = 15 }
				defer cmd.SetSessionMinute(200 * time.Millisecond)()
			}
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
			wants(t, r.out, "check the artifacts of the work item in Qory Apiary; with no version of this run, "+deleteBranch+
				"; then take the work item in again: qory pushed acme/site, and "+c.cause+", so the versions may or may not be stored; the code is used")
			lacks(t, r.out, "may not have reached the branch")
		})
	}
}

// TestThePushStepCountsTheLimitFromTheAgentsStart is a write run whose setup takes 1.5s,
// with a time limit of 3s, and a push step whose request that stores the version gets no
// response: the limit counts from the agent's start, so the step is cut 3s after it, not
// 3s after the run's open. A limit that counted from the open would end the run within
// 1.5s of the agent's start.
func TestThePushStepCountsTheLimitFromTheAgentsStart(t *testing.T) {
	p := newPushRepo(t, "write")
	p.change = func(d map[string]any) { d["time_limit_minutes"] = 15 }
	defer cmd.SetSessionMinute(200 * time.Millisecond)()
	defer cmd.SetPushTimes(200*time.Millisecond, 400*time.Millisecond)()
	var joined time.Time
	defer cmd.SetBeforeJoin(func() {
		time.Sleep(1500 * time.Millisecond)
		joined = time.Now()
	})()
	hangingPost(t, p, nil)
	r := p.run(t, writesAndCommits(t), nil)
	ended := time.Now()
	r.wantsClose(t, event.StateCancelled, 124)
	if took := ended.Sub(joined); took < 2700*time.Millisecond {
		t.Errorf("the run ended %s after the agent's start, with a time limit of 3s\n%s", took, r.out)
	}
	wants(t, r.out, "the run reached its time limit of 3s")
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

// TestTheStepClosesItsProxyBeforeTheRunCloses is the push proxy of the step, which ends
// before the close, and every run API and push proxy call before it, which fail after.
func TestTheStepClosesItsProxyBeforeTheRunCloses(t *testing.T) {
	p := newPushRepo(t, "write")
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	opens, open := 0, 0
	for _, call := range r.calls {
		switch call {
		case "push proxy":
			opens++
			open++
		case "push proxy closed":
			open--
		}
	}
	if opens != 1 || open != 0 {
		t.Errorf("%d push proxies opened and %d stayed open: %q, want 1 and 0", opens, open, r.calls)
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
			if got := gitOut(t, p.h.dir, "status", "--porcelain"); !strings.Contains(got, "left.txt") {
				t.Errorf("the agent's file was committed: %q", got)
			}
		})
	}
}

// TestAWallThatStaysRunsNoGit is a PushProxy that fails because the wall of a session could
// not be removed, for each end of the session: the agent may still run, so the step runs
// no git, and a lock stays.
func TestAWallThatStaysRunsNoGit(t *testing.T) {
	const refused = "Open a new run. The wall of session 1 could not be removed and may still run, so this run takes no more sessions."
	for name, result := range allSessionEnds {
		t.Run(name, func(t *testing.T) {
			p := walledPush(t)
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
			var lock string
			r := p.run(t, func(dir string) {
				lock = filepath.Join(dir, ".git", "index.lock")
				writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
				writeFile(t, lock, "")
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
			if got := string(log[after:]); got != "" {
				t.Errorf("git ran after the session:\n%s", got)
			}
			if _, err := os.Lstat(lock); err != nil {
				t.Errorf("the lock is gone: %v", err)
			}
			if head := p.branchHead(t); head != "" {
				t.Errorf("the forge has the branch at %s", head)
			}
		})
	}

	t.Run("a local run", func(t *testing.T) {
		const line = ", and run no git there: a container of the run could not be removed and may still run and write the copy, so nothing came back from the run"
		for name, c := range map[string]struct {
			wall  cmd.Nowhere
			wants string
		}{
			// The close failed to remove a container, and the git session ran before it.
			"the close": {wall: cmd.Nowhere{WallKept: true}, wants: line},
			// The agent's Unwrap failed, so the git session waits for the close.
			"Unwrap": {wall: cmd.Nowhere{ContainerKept: true, WallKept: true}, wants: line},
			// The removal of the setup container failed, so the git session waits for the close.
			"RemoveSetup": {
				wall:  cmd.Nowhere{WallKept: true, RemoveSetup: func(context.Context) error { return errors.New(refused) }},
				wants: line,
			},
		} {
			t.Run(name, func(t *testing.T) {
				root := copyCheckout(t)
				artifact := filepath.Join(root, ".qory", "artifacts", "run.txt")
				t.Cleanup(cmd.SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
					return &session.Result{}, nil
				}))
				empty := filepath.Join(tempDir(t), "gitconfig")
				writeFile(t, empty, "")
				calls := recordGit(t, empty)
				var after int64
				var work string
				agentInCopy(t, func(spec session.SessionSpec) {
					work = spec.Dir
					writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
					writeFile(t, filepath.Join(spec.Dir, ".qory", "artifacts", "run.txt"), "the run\n")
					info, err := os.Stat(calls)
					if err != nil {
						t.Fatal(err)
					}
					after = info.Size()
				})
				_, _, restore := cmd.OpenNowhereWith(c.wall)
				defer restore()
				out, err := run(t, "run", "claude", "--", "-p", "hi")
				if cmd.ExitCode(err) != 1 {
					t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
				}
				wants(t, out, "read the run's files in "+work+c.wants)
				log, err := os.ReadFile(calls)
				if err != nil {
					t.Fatal(err)
				}
				for line := range strings.Lines(string(log[after:])) {
					if !strings.Contains(line, " check-ref-format ") {
						t.Errorf("git ran after the session:\n%s", line)
					}
				}
				if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
					t.Errorf("the run's branch came back: %s", got)
				}
				if _, err := os.Lstat(artifact); err == nil {
					t.Error("the run's artifact came back")
				}
				wantDisarmed(t, work)
			})
		}
	})
	t.Run("a local run in a plain folder", func(t *testing.T) {
		root := emptyDir(t)
		copyFixture(t, "two-modules", root)
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		t.Setenv("QORY_RUN_KEY", "")
		var work string
		agentInCopy(t, func(spec session.SessionSpec) {
			work = spec.Dir
			writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		})
		_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{WallKept: true})
		defer restore()
		out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
		if cmd.ExitCode(err) != 1 {
			t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
		}
		wants(t, out, "read the run's files in "+work+", and run no git there: a container of the run could not be removed")
		if _, err := os.Lstat(filepath.Join(root, "left.txt")); err == nil {
			t.Error("the run's file came back")
		}
	})
}

// TestAMovedBranchEndsTheRunWithPushHeadChangedAfterAnySession is a branch that another push
// moved, after a session that passed, one that failed and one that the time limit stopped: each
// run closes failed with the code push_head_changed and the exit code of the session (1 for
// the passed one), the branch stays, and the printed refusal is the same.
func TestAMovedBranchEndsTheRunWithPushHeadChangedAfterAnySession(t *testing.T) {
	for name, c := range map[string]struct {
		result      func() (*session.Result, error)
		state, code string
		exit        int
	}{
		"passed": {succeeded, event.StateFailed, string(contracts.EndCodePushHeadChanged), 1},
		"failed": {func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 7}, nil },
			event.StateFailed, string(contracts.EndCodePushHeadChanged), 7},
		"timed out": {timedOut, event.StateFailed, string(contracts.EndCodePushHeadChanged), 124},
		"interrupted": {func() (*session.Result, error) {
			return &session.Result{State: event.StateCancelled, Cancelled: true, ExitCode: 130}, nil
		}, event.StateCancelled, string(event.ReasonInterrupted), 130},
	} {
		result := c.result
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			other := tempDir(t)
			proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
			writeFile(t, filepath.Join(other, "moved.txt"), "moved\n")
			commitAll(t, other, "Move the branch")
			proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
			moved := p.branchHead(t)
			p.api.pushedCommits = `[]`

			r := p.run(t, writesAndCommits(t), result)
			if len(r.closes) != 1 || r.closes[0].State != c.state || r.closes[0].Reason != c.code || r.closes[0].ExitCode != c.exit {
				t.Errorf("the run closed with %+v, want %s with %q and exit code %d\n%s", r.closes, c.state, c.code, c.exit, r.out)
			}
			wants(t, r.out, "qory run: Set the branch to the expected commit or delete it; its head is not that commit.")
			if got := p.branchHead(t); got != moved {
				t.Errorf("the branch moved to %s", got)
			}
		})
	}
}

// replyProcess is a process.yaml whose type reply is a structured text for a target with no
// address, such as a conversation.
const replyProcess = `artifact_types:
  - name: reply
    base_view: structured
    size_limit: 65536
    max_files: 1
    json_schema:
      type: object
      required: [target, text]
      properties:
        target: {type: object}
        text: {type: string}
`

// TestARunWithNoRepositoryStoresItsReply is a write run from a run code whose run
// configuration lists no repository, whose target has no address and whose documents deliver
// reply: the session writes reply to the outbox, qory pushes nothing, stores one version
// with the target filled in the artifact of the target, removes the file, and prints the line
// of a run with no repository.
func TestARunWithNoRepositoryStoresItsReply(t *testing.T) {
	p, outbox := replyRepo(t)
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if *outbox == "" {
		t.Fatalf("the session has no %s\n%s", "QORY_OUTBOX", r.out)
	}
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Fatalf("%d POST, want 1\n%s", posts, r.out)
	}
	sum := sha256.Sum256([]byte("C_2002"))
	if kind := p.api.kinds[0]; kind.typ != "reply" || kind.name != "t-"+hex.EncodeToString(sum[:])[:32] || kind.filename != "reply.json" {
		t.Errorf("the version is %+v, want reply in the artifact of C_2002", kind)
	}
	doc := p.api.document(t, 1)
	if got, _ := doc["target"].(map[string]any); got["collection"] != "C_2002" || doc["text"] != "The order ships on Monday." {
		t.Errorf("the version is %v, want the text with the target C_2002", doc)
	}
	if _, err := os.Lstat(filepath.Join(*outbox, "reply", "reply.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the outbox file is still there: %v", err)
	}
	wants(t, r.out, "qory run: stored the reply output; the run has no repository to push.\n")
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
}

// TestARunWithNoRepositoryStopsWhenItCannotReadItsWorkItem is the run of
// [TestARunWithNoRepositoryStoresItsReply] whose GET /work-item ends with a 500: the run
// stops before its session with the line of the read, and stores nothing.
func TestARunWithNoRepositoryStopsWhenItCannotReadItsWorkItem(t *testing.T) {
	p, outbox := replyRepo(t)
	p.api.workItemFails = true
	r := p.run(t, nil, nil)
	if r.err == nil || *outbox != "" {
		t.Fatalf("%v, the session's outbox %q, want the run to stop before the session\n%s", r.err, *outbox, r.out)
	}
	wants(t, r.out, "GET /work-item returned 500 internal.; the code is used")
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestAStageOutputTypeThatIsNoNameStopsTheRun is the run of
// [TestARunWithNoRepositoryStoresItsReply] whose stage lists the output type ../x: the run
// stops before its session, as the type would be a path out of the outbox.
func TestAStageOutputTypeThatIsNoNameStopsTheRun(t *testing.T) {
	p, outbox := replyRepo(t)
	p.api.outputs = `[{"type":"../x","required":true,"version":null,"check":"missing"}]`
	r := p.run(t, nil, nil)
	if r.err == nil || *outbox != "" {
		t.Fatalf("%v, the session's outbox %q, want the run to stop before the session\n%s", r.err, *outbox, r.out)
	}
	wants(t, r.out, "qory run: Report the run to the instance admin of Qory Apiary: an output type is no name.\n  The stage lists the output type ../x, not a name")
}

// replyRepo is a [pushRepo] of a run from a run code whose run configuration lists no
// repository, whose target C_2002 has no address, whose documents deliver reply after the
// gate, and whose stage requires reply. The harness is the stack examples/hello of the
// forge's acme/site, with the workflow hello at access write and [replyProcess]. The session
// writes reply to its outbox, whose path the second result is set to.
func replyRepo(t *testing.T) (*pushRepo, *string) {
	t.Helper()
	stackFiles := helloFiles(t)
	stackFiles["examples/hello/qory-stack.yaml"] = withExtendable(stackFiles["examples/hello/qory-stack.yaml"]) + "workflows:\n  hello:\n    access: write\n"
	stackFiles["examples/hello/modules/hello/process.yaml"] = replyProcess
	p := newPushRepoWith(t, "write", stackFiles)
	p.repositories = ""
	p.stackProxy = true
	p.noOutput = true
	p.api.outputs = `[{"type":"reply","required":true,"version":null,"check":"missing"}]`
	target := map[string]any{"collection": "C_2002"}
	p.change = func(d map[string]any) {
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		delete(d, "target_version")
		d["harness"] = map[string]any{"harness": map[string]any{"extends": map[string]any{"git": p.h.server.URL("acme/site"), "ref": p.first, "path": "examples/hello"}}}
		d["deliver"] = []any{map[string]any{"type": "reply", "send": "after_gate"}}
	}
	outbox := new(string)
	p.mountsOf = func(spec session.SessionSpec) {
		*outbox = outboxOf(spec)
		if *outbox != "" {
			writeFile(t, filepath.Join(*outbox, "reply", "reply.json"), `{"text":"The order ships on Monday."}`)
		}
	}
	return p, outbox
}
