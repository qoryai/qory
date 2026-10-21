package prebuild_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/provisioner"
)

// The fakes' script and reports are in the runs' configuration directory, the one
// place of the test's that their allow-listed environment names.
const (
	scriptFile = "fake.json"
	readyFile  = "ready"
)

// script is what the fake qory and the fake docker do.
type script struct {
	// RunID is the run whose record qory run --fetch-only makes, with Record.
	RunID string
	// KeySum is the SHA-256 of the key qory run expects on descriptor 3.
	KeySum string
	// Fetch is qory run --fetch-only's exit status; Record makes it make the run's
	// record; Head is what it leaves in work/.git: "branch", a loose ref, "packed", a
	// packed ref, "detached", or "garbage"; Commit is the id it leaves.
	Fetch  int
	Record bool
	Head   string
	Commit string
	// Build is qory run prebuild's exit status, Push docker push's.
	Build, Push int
	// Inspect is what docker image inspect prints.
	Inspect string
	// Block is the step that waits for SIGTERM and exits 1, as qory run does: "fetch",
	// "build" or "push".
	Block string
}

// report is what a fake saw, written to report-<step>.json.
type report struct {
	Env      []string
	Args     []string
	Dir      string
	FD3Pipe  bool
	KeyOK    bool
	Problems []string
}

// fd3Pipe reports whether descriptor 3 is open and a pipe, as the key's is.
func fd3Pipe() bool {
	var st syscall.Stat_t
	return syscall.Fstat(child.KeyFD, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFIFO
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(fakeGit())
		case "run", "push", "image":
			os.Exit(fake(fd3Pipe()))
		}
	}
	os.Exit(m.Run())
}

// fakeGit is git init --quiet <dir>: it makes <dir>/.git.
func fakeGit() int {
	if err := os.MkdirAll(filepath.Join(os.Args[len(os.Args)-1], ".git"), 0o700); err != nil {
		return 96
	}
	return 0
}

// stepOf is the step a fake's arguments are.
func stepOf(args []string) string {
	switch {
	case len(args) > 1 && args[0] == "run" && args[1] == "--fetch-only":
		return "fetch"
	case len(args) > 1 && args[0] == "run" && args[1] == "prebuild":
		return "build"
	case len(args) > 0 && args[0] == "push":
		return "push"
	case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
		return "inspect"
	case len(args) > 1 && args[0] == "image" && args[1] == "rm":
		return "rm"
	}
	return "unknown"
}

// fake is qory run --fetch-only, qory run prebuild, docker push, docker image inspect or
// docker image rm, as the script in $XDG_CONFIG_HOME says.
func fake(fd3 bool) int {
	config := os.Getenv("XDG_CONFIG_HOME")
	b, err := os.ReadFile(filepath.Join(config, scriptFile))
	if err != nil {
		return 90
	}
	var s script
	if err := json.Unmarshal(b, &s); err != nil {
		return 91
	}
	step := stepOf(os.Args[1:])
	cwd, _ := os.Getwd()
	rep := report{Env: os.Environ(), Args: os.Args[1:], Dir: cwd, FD3Pipe: fd3}
	for _, a := range os.Args {
		if accesskey.ContainsSecret(a) {
			rep.Problems = append(rep.Problems, "an argument holds a secret")
		}
	}
	for _, kv := range rep.Env {
		if accesskey.ContainsSecret(kv) {
			rep.Problems = append(rep.Problems, "a variable holds a secret")
		}
	}
	code := 0
	switch step {
	case "fetch":
		code = s.Fetch
		if fd3 {
			got := make(chan []byte, 1)
			go func() {
				k, _ := io.ReadAll(os.NewFile(uintptr(child.KeyFD), "key"))
				got <- k
			}()
			select {
			case k := <-got:
				rep.KeyOK = sum(k) == s.KeySum
			case <-time.After(5 * time.Second):
				rep.Problems = append(rep.Problems, "descriptor 3 never reached its end")
			}
		}
		if s.Record {
			if err := os.MkdirAll(fakeRecord(cwd, s.RunID), 0o700); err != nil {
				return 94
			}
		}
		if err := writeHead(filepath.Join(cwd, ".git"), s.Head, s.Commit); err != nil {
			return 95
		}
	case "build":
		code = s.Build
	case "push":
		code = s.Push
	case "inspect":
		fmt.Println(s.Inspect)
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(filepath.Join(config, "report-"+step+".json"), out, 0o600); err != nil {
		return 92
	}
	if step != "inspect" {
		fmt.Println(step, "says hello")
		fmt.Fprintln(os.Stderr, step, "warns")
	}
	if s.Block == step {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		os.WriteFile(filepath.Join(config, readyFile), nil, 0o600)
		<-c
		return 1
	}
	return code
}

// writeHead leaves in git, a .git folder, what a fetch of commit leaves, as head says.
func writeHead(git, head, commit string) error {
	switch head {
	case "branch":
		if err := os.MkdirAll(filepath.Join(git, "refs", "heads"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(git, "refs", "heads", "main"), []byte(commit+"\n"), 0o600)
	case "packed":
		if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(git, "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted \n"+commit+" refs/heads/main\n"), 0o600)
	case "detached":
		return os.WriteFile(filepath.Join(git, "HEAD"), []byte(commit+"\n"), 0o600)
	case "garbage":
		return os.WriteFile(filepath.Join(git, "HEAD"), []byte("not a head\n"), 0o600)
	}
	return nil
}

// sum is the SHA-256 of b, in hex.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRecord is the folder of the record of the run runID started in cwd, as qory run's
// documentation says it, computed apart from the launcher's.
func fakeRecord(cwd, runID string) string {
	state := filepath.Join(os.Getenv("HOME"), ".local", "state", "qory")
	if base := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(base) {
		state = filepath.Join(base, "qory")
	}
	real, _ := filepath.EvalSymlinks(cwd)
	return filepath.Join(state, "runs", filepath.Base(real)+"-"+sum([]byte(real))[:12], runID)
}

// testKey is a run key's shape, with no meaning.
const testKey = "qrk_eyJ0ZXN0IjoidGhlIHByZWJ1aWxkIGxhdW5jaGVyIn0.c2lnbmF0dXJlLW9mLW5vdGhpbmc"

// testRunID is a run id.
const testRunID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"

// testCommit is the commit the fake fetch leaves.
const testCommit = "8c9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f70819"

// testInputs are a prebuild's inputs.
const testInputs = `{"version":1,"environment_ref":"3f2a1b0c9d8e"}`

// testAgent is the agent image, pinned by digest.
const testAgent = "ghcr.io/qoryai/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// testTarget is the run's target.
var testTarget = contracts.Target{System: "github.com", Path: "acme/shop"}

// testName is the target's images' name: sha256("github.com/acme/shop"), 16 digits.
var testName = sum([]byte("github.com/acme/shop"))[:16]

// holder is a key holder, as the provisioner client's.
type holder struct {
	b       []byte
	cleared int
}

func (h *holder) Bytes() []byte { return h.b }
func (h *holder) Clear()        { clear(h.b); h.cleared++ }

// registry is a fake registry: it serves the manifests it holds by digest.
type registry struct {
	mu        sync.Mutex
	manifests map[string]served
	gets      []string
	srv       *httptest.Server
}

// served is a manifest's bytes and Content-Type.
type served struct {
	body, mediaType string
}

// put holds body under its digest, or under digest when that is set, and returns the
// digest it is served at.
func (r *registry) put(body, mediaType, digest string) string {
	if digest == "" {
		digest = "sha256:" + sum([]byte(body))
	}
	r.mu.Lock()
	r.manifests[digest] = served{body, mediaType}
	r.mu.Unlock()
	return digest
}

func newRegistry(t *testing.T) *registry {
	r := &registry{manifests: map[string]served{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.gets = append(r.gets, req.URL.Path)
		r.mu.Unlock()
		prefix := "/v2/qory-env/" + testName + "/manifests/"
		digest, ok := strings.CutPrefix(req.URL.Path, prefix)
		r.mu.Lock()
		m, have := r.manifests[digest]
		r.mu.Unlock()
		if !ok || !have || !strings.Contains(req.Header.Get("Accept"), "application/vnd.oci.image.manifest.v1+json") {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", m.mediaType)
		io.WriteString(w, m.body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// host is the registry's host and port.
func (r *registry) host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// manifestOf is an image manifest whose config and layers have the sizes given.
func manifestOf(mediaType string, config int64, layers ...int64) string {
	var ls []string
	for i, l := range layers {
		ls = append(ls, fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:%064d","size":%d}`, i+1, l))
	}
	mt := ""
	if mediaType != "" {
		mt = `"mediaType":"` + mediaType + `",`
	}
	return `{"schemaVersion":2,` + mt + `"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("c", 64) + `","size":` + fmt.Sprint(config) + `},"layers":[` + strings.Join(ls, ",") + `]}`
}

// env is one launch's directories, fakes and log.
type env struct {
	settings  prebuild.Settings
	reg       *registry
	home      string
	elsewhere string
	script    script
	mu        sync.Mutex
	logs      []string
	reported  []provisioner.Image
	reportErr error
}

func (e *env) log(line string) { e.mu.Lock(); e.logs = append(e.logs, line); e.mu.Unlock() }

func (e *env) lines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.logs)
}

// newEnv is a launch's directories, with HOME at one of them, a registry holding an
// image manifest of 100 + 20 + 3 bytes, and a script whose digest is that manifest's:
// a run that succeeds. The program's environment holds what no command may get.
func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{home: filepath.Join(root, "home"), elsewhere: filepath.Join(root, "elsewhere"), reg: newRegistry(t)}
	e.settings = prebuild.Settings{
		State: filepath.Join(root, "state"), RunConfig: filepath.Join(root, "runconfig"),
		Registry: e.reg.host() + "/qory-env", AgentImage: testAgent, User: "1000:1000", Helper: "/opt/qory/qory-linux",
		Qory: os.Args[0], Git: os.Args[0], Docker: os.Args[0], Log: e.log, Grace: time.Second,
	}
	for _, d := range []string{e.home, e.settings.RunConfig} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", e.home)
	t.Setenv("QORY_RUN_KEY", testKey)
	t.Setenv("QORY_ACCESS_KEY_SECRET", "qak_notakey")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("XDG_STATE_HOME", e.elsewhere)
	digest := e.reg.put(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3), "application/vnd.oci.image.manifest.v1+json", "")
	e.script = script{RunID: testRunID, KeySum: sum([]byte(testKey)), Record: true, Head: "branch", Commit: testCommit}
	e.inspect(digest, "linux", "arm64", "")
	return e
}

// inspect makes docker image inspect name the image in the registry by digest, of the
// platform given.
func (e *env) inspect(digest, os, arch, variant string) {
	b, _ := json.Marshal(map[string]any{
		"repo_digests": []string{"elsewhere.example.com/other@sha256:" + strings.Repeat("e", 64), e.settings.Registry + "/" + testName + "@" + digest},
		"os":           os, "architecture": arch, "variant": variant,
	})
	e.script.Inspect = string(b)
}

// launch runs Launch with the test's run, and cancels ctx once a fake waits when stop
// is set.
func (e *env) launch(t *testing.T, stop bool) (prebuild.Result, error) {
	t.Helper()
	b, _ := json.Marshal(e.script)
	if err := os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stop {
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(e.settings.RunConfig, readyFile)); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
		}()
	}
	h := &holder{b: []byte(testKey)}
	res, err := prebuild.Launch(ctx, e.settings, prebuild.Run{
		RunID: testRunID, Target: testTarget, Key: h, Inputs: []byte(testInputs),
		Report: func(_ context.Context, img provisioner.Image) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.reported = append(e.reported, img)
			return e.reportErr
		},
	})
	if h.cleared == 0 || !bytes.Equal(h.b, make([]byte, len(testKey))) {
		t.Error("the key's holder was not cleared")
	}
	return res, err
}

// report is a fake's report of the step, ok false when it did not run.
func (e *env) report(t *testing.T, step string) (report, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.settings.RunConfig, "report-"+step+".json"))
	if err != nil {
		return report{}, false
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, true
}

// steps is the steps that ran, in the order of the pipeline.
func (e *env) steps(t *testing.T) []string {
	var ran []string
	for _, s := range []string{"fetch", "build", "push", "inspect", "rm"} {
		if _, ok := e.report(t, s); ok {
			ran = append(ran, s)
		}
	}
	return ran
}

// noLeak fails when the key, or what starts a secret, is in a log line, in err, in a
// command's arguments or environment, or in a file under the launch's directories, and
// when the run directory is left.
func (e *env) noLeak(t *testing.T, err error) {
	t.Helper()
	texts := e.lines()
	if err != nil {
		texts = append(texts, err.Error())
	}
	for _, step := range []string{"fetch", "build", "push", "inspect", "rm"} {
		if r, ok := e.report(t, step); ok {
			texts = append(append(texts, r.Args...), r.Env...)
			for _, p := range r.Problems {
				t.Errorf("%s: %s", step, p)
			}
			if step != "fetch" && r.FD3Pipe {
				t.Errorf("%s got a descriptor 3", step)
			}
		}
	}
	for _, s := range texts {
		if strings.Contains(s, testKey[4:24]) || accesskey.ContainsSecret(s) {
			t.Errorf("a secret is in %q", s)
		}
	}
	for _, dir := range []string{filepath.Join(e.home, ".local", "state"), e.elsewhere, explore.Dir(e.settings.State, testRunID)} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("%s is there: %v", dir, err)
		}
	}
	for _, dir := range []string{e.settings.State, e.settings.RunConfig, e.home} {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			if bytes.Contains(b, []byte(testKey[4:24])) || accesskey.ContainsSecret(string(b)) {
				t.Errorf("a secret is in the file %s", path)
			}
			return nil
		})
	}
}

// The endings of the table.
var (
	succeeded  = explore.Ending{Post: true, State: "succeeded"}
	notStarted = explore.Ending{Release: true, ReleaseReason: "the run could not be started on this machine"}
)

// releasedFailed is released with retry false for reason, posted failed with code.
func releasedFailed(reason, code string) explore.Ending {
	return explore.Ending{Post: true, State: "failed", Reason: code, Release: true, ReleaseReason: reason}
}

// releasedOnly is released with retry false for reason, and not posted.
func releasedOnly(reason string) explore.Ending {
	return explore.Ending{Release: true, ReleaseReason: reason}
}

const (
	t10 = "the pool program stopped during the build"
	t11 = "the code could not be fetched"
	t12 = "the repository has no development container configuration"
	t13 = "the environment could not be built"
	t14 = "the image could not be stored in the registry"
)

func TestLaunchRows(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(*env)
		stop  bool
		want  explore.Ending
		steps []string
	}{
		{"succeeded", func(*env) {}, false, succeeded, []string{"fetch", "build", "push", "inspect", "rm"}},
		{"fetch failed", func(e *env) { e.script.Fetch = 1 }, false, releasedFailed(t11, ""), []string{"fetch"}},
		{"fetch failed, no record", func(e *env) { e.script.Fetch, e.script.Record = 2, false }, false, releasedOnly(t11), []string{"fetch"}},
		{"fetch at its time limit", func(e *env) { e.script.Fetch = 124 }, false, releasedFailed(t11, "time_limit"), []string{"fetch"}},
		{"fetch exit 0, no record", func(e *env) { e.script.Record = false }, false, releasedOnly(t11), []string{"fetch"}},
		{"no commit", func(e *env) { e.script.Head = "garbage" }, false, releasedFailed(t11, ""), []string{"fetch"}},
		{"packed ref", func(e *env) { e.script.Head = "packed" }, false, succeeded, []string{"fetch", "build", "push", "inspect", "rm"}},
		{"no configuration", func(e *env) { e.script.Build = 7 }, false, releasedFailed(t12, ""), []string{"fetch", "build", "rm"}},
		{"build failed", func(e *env) { e.script.Build = 1 }, false, releasedFailed(t13, ""), []string{"fetch", "build", "rm"}},
		{"build refused", func(e *env) { e.script.Build = 2 }, false, releasedFailed(t13, ""), []string{"fetch", "build", "rm"}},
		{"build at its time limit", func(e *env) { e.script.Build = 124 }, false, releasedFailed(t13, "time_limit"), []string{"fetch", "build", "rm"}},
		{"push failed", func(e *env) { e.script.Push = 1 }, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "rm"}},
		{"no digest in the registry", func(e *env) {
			e.script.Inspect = `{"repo_digests":["elsewhere.example.com/x@sha256:` + strings.Repeat("e", 64) + `"],"os":"linux","architecture":"arm64","variant":""}`
		}, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "inspect", "rm"}},
		{"digest not of the shape", func(e *env) { e.inspect("sha256:ABC", "linux", "arm64", "") }, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "inspect", "rm"}},
		{"manifest not in the registry", func(e *env) { e.inspect("sha256:"+strings.Repeat("0", 64), "linux", "arm64", "") }, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "inspect", "rm"}},
		{"manifest's bytes not of its digest", func(e *env) {
			d := e.reg.put(manifestOf("", 1, 1), "application/vnd.oci.image.manifest.v1+json", "sha256:"+strings.Repeat("a", 64))
			e.inspect(d, "linux", "arm64", "")
		}, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "inspect", "rm"}},
		{"prebuilt refused", func(e *env) { e.reportErr = errors.New("prebuilt for dp_x: Qory Apiary answered 409 run_mismatch") }, false, releasedFailed(t14, ""), []string{"fetch", "build", "push", "inspect", "rm"}},
		{"stopped during the fetch", func(e *env) { e.script.Block = "fetch" }, true, releasedFailed(t10, "pool_stopped"), []string{"fetch"}},
		{"stopped during the fetch, no record", func(e *env) { e.script.Block, e.script.Record = "fetch", false }, true, releasedOnly(t10), []string{"fetch"}},
		{"stopped during the build", func(e *env) { e.script.Block = "build" }, true, releasedFailed(t10, "pool_stopped"), []string{"fetch", "build", "rm"}},
		{"stopped during the push", func(e *env) { e.script.Block = "push" }, true, releasedFailed(t10, "pool_stopped"), []string{"fetch", "build", "push", "rm"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.edit(e)
			res, err := e.launch(t, c.stop)
			if (err == nil) != (c.want == succeeded) {
				t.Errorf("err %v", err)
			}
			if res.Ending != c.want {
				t.Errorf("ending %+v, want %+v", res.Ending, c.want)
			}
			if res.Ending.Retry {
				t.Error("released with retry true after the handover")
			}
			if got := e.steps(t); !slices.Equal(got, c.steps) {
				t.Errorf("steps %q, want %q", got, c.steps)
			}
			wantReports := 0
			if slices.Contains(c.steps, "inspect") && (c.want == succeeded || strings.HasPrefix(c.name, "prebuilt")) {
				wantReports = 1
			}
			if len(e.reported) != wantReports {
				t.Errorf("prebuilt sent %d times, want %d", len(e.reported), wantReports)
			}
			e.noLeak(t, err)
		})
	}
}

func TestLaunchDigestNotOfTheShape(t *testing.T) {
	for _, d := range []string{"sha256:ABC", "sha256:" + strings.Repeat("a", 63) + "/x", "sha512:" + strings.Repeat("a", 64)} {
		e := newEnv(t)
		e.inspect(d, "linux", "arm64", "")
		res, err := e.launch(t, false)
		if err == nil || res.Ending != releasedFailed(t14, "") {
			t.Errorf("%s: ending %+v, err %v", d, res.Ending, err)
		}
		if len(e.reg.gets) != 0 {
			t.Errorf("%s: the registry was asked for %q", d, e.reg.gets)
		}
	}
	e := newEnv(t)
	e.script.Inspect = `{"repo_digests":["elsewhere.example.com/x@sha256:` + strings.Repeat("e", 64) + `"],"os":"linux","architecture":"arm64","variant":""}`
	if _, err := e.launch(t, false); err == nil || err.Error() != "docker image inspect names no digest of the image in the registry" {
		t.Errorf("no digest: %v", err)
	}
}

func TestLaunchStoppedBeforeItStarted(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &holder{b: []byte(testKey)}
	res, err := prebuild.Launch(ctx, e.settings, prebuild.Run{RunID: testRunID, Target: testTarget, Key: h, Inputs: []byte(testInputs)})
	if err == nil || res.Ending != releasedOnly(t10) {
		t.Errorf("ending %+v, err %v", res.Ending, err)
	}
	if h.cleared == 0 {
		t.Error("the key's holder was not cleared")
	}
	if got := e.steps(t); len(got) != 0 {
		t.Errorf("steps %q ran", got)
	}
	e.noLeak(t, err)
}

// TestHostProgramsByNameAreFoundOnTheCleanedPATH finds git, when Git is not set, and
// qory and docker, when each is a name alone, on the PATH every command gets: with an
// empty file of each name in an entry in the runs folder alone, none is found, the
// launch fails with exec's error at that program's step, and no later step runs.
func TestHostProgramsByNameAreFoundOnTheCleanedPATH(t *testing.T) {
	for _, c := range []struct {
		name, want string
		edit       func(*prebuild.Settings)
		ran        []string
	}{
		{"git", "git init: ", func(s *prebuild.Settings) { s.Git = "" }, nil},
		{"qory", "qory run could not be started: ", func(s *prebuild.Settings) { s.Qory = "qory" }, nil},
		{"docker", "docker push could not be started: ", func(s *prebuild.Settings) { s.Docker = "docker" }, []string{"fetch", "build"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.edit(&e.settings)
			hostbin := filepath.Join(explore.RunsDir(e.settings.State), "hostbin")
			if err := os.MkdirAll(hostbin, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"git", "qory", "docker"} {
				if err := os.WriteFile(filepath.Join(hostbin, n), nil, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", hostbin)
			_, err := e.launch(t, false)
			if !errors.Is(err, exec.ErrNotFound) || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v, want exec.ErrNotFound after %q", err, c.want)
			}
			for _, step := range []string{"fetch", "build", "push", "inspect"} {
				if _, ran := e.report(t, step); ran != slices.Contains(c.ran, step) {
					t.Errorf("%s ran %v", step, ran)
				}
			}
		})
	}
}

// TestLaunchCommands runs each step's command with its arguments, in its folder, with
// the allowed environment: the docker commands in the pool's folder, outside every run
// directory, and every command with a PATH that has no entry in the pool's runs folder,
// the others kept as written.
func TestLaunchCommands(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "orbstack")
	e := newEnv(t)
	runs := explore.RunsDir(e.settings.State)
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(runs, testRunID, "work"), "/usr/bin", "", "bin", e.settings.State + "/pool/x/../runs", "/bin"}, sep))
	res, err := e.launch(t, false)
	if err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(e.settings.State, "pool")
	dir := explore.Dir(e.settings.State, testRunID)
	work, state := filepath.Join(dir, "work"), filepath.Join(dir, "state")
	tag := e.settings.Registry + "/" + testName + ":" + testRunID
	for _, c := range []struct {
		step string
		args []string
		dir  string
	}{
		{"fetch", []string{"run", "--fetch-only", "--run-key-fd", "3", "--wall", "docker", "--timeout", "15m"}, work},
		{"build", []string{"run", "prebuild", "--repository", work, "--agent-image", testAgent, "--user", "1000:1000", "--tag", tag, "--helper", "/opt/qory/qory-linux"}, dir},
		{"push", []string{"push", tag}, pool},
		{"inspect", []string{"image", "inspect", "--format", `{"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json .Variant}}}`, tag}, pool},
		{"rm", []string{"image", "rm", tag}, pool},
	} {
		r, ok := e.report(t, c.step)
		if !ok {
			t.Errorf("%s did not run", c.step)
			continue
		}
		if !slices.Equal(r.Args, c.args) {
			t.Errorf("%s's arguments\n%q\nwant\n%q", c.step, r.Args, c.args)
		}
		if r.Dir != c.dir {
			t.Errorf("%s ran in %s, want %s", c.step, r.Dir, c.dir)
		}
		if (c.step == "fetch") != r.FD3Pipe || (c.step == "fetch") != r.KeyOK {
			t.Errorf("%s: descriptor 3 a pipe %v, the key %v", c.step, r.FD3Pipe, r.KeyOK)
		}
		for _, kv := range r.Env {
			n := kv[:strings.IndexByte(kv, '=')]
			if !slices.Contains([]string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}, n) {
				t.Errorf("%s got %s", c.step, n)
			}
		}
		for _, want := range []string{"XDG_CONFIG_HOME=" + e.settings.RunConfig, "XDG_STATE_HOME=" + state, "DOCKER_CONTEXT=orbstack", "PATH=/usr/bin" + sep + "/bin"} {
			if !slices.Contains(r.Env, want) {
				t.Errorf("%s's environment has no %s: %q", c.step, want, r.Env)
			}
		}
	}
	digest := "sha256:" + sum([]byte(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3)))
	want := provisioner.Image{Image: e.settings.Registry + "/" + testName + "@" + digest, Size: 123, EnvironmentRef: "3f2a1b0c9d8e", Commit: testCommit}
	if res.Image != want || !slices.Equal(e.reported, []provisioner.Image{want}) {
		t.Errorf("image %+v, reported %+v, want %+v", res.Image, e.reported, want)
	}
	if !slices.Equal(e.reg.gets, []string{"/v2/qory-env/" + testName + "/manifests/" + digest}) {
		t.Errorf("the registry was asked for %q", e.reg.gets)
	}
	lines := e.lines()
	for _, want := range []string{testRunID + ": fetch says hello", testRunID + ": fetch warns", testRunID + ": build says hello", testRunID + ": push warns"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no log line %q in %q", want, lines)
		}
	}
	e.noLeak(t, err)
}

func TestLaunchIndex(t *testing.T) {
	const oci, index = "application/vnd.oci.image.manifest.v1+json", "application/vnd.oci.image.index.v1+json"
	for _, c := range []struct {
		name    string
		entries func(e *env) []string
		variant string
		size    int64
	}{
		{"one image and its attestation", func(e *env) []string {
			img := e.reg.put(manifestOf(oci, 10, 5), oci, "")
			att := e.reg.put(manifestOf(oci, 1, 1), oci, "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + img + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}`,
				`{"mediaType":"` + oci + `","digest":"` + att + `","size":1,"platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}}`,
			}
		}, "", 15},
		{"the platform's image among two", func(e *env) []string {
			amd := e.reg.put(manifestOf(oci, 30, 30), oci, "")
			arm := e.reg.put(manifestOf(oci, 7, 7), oci, "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + amd + `","size":1,"platform":{"os":"linux","architecture":"amd64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + arm + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}`,
			}
		}, "v8", 14},
		{"the platform's image among two, no variant", func(e *env) []string {
			amd := e.reg.put(manifestOf(oci, 30, 30), oci, "")
			arm := e.reg.put(manifestOf(oci, 8, 8), oci, "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + amd + `","size":1,"platform":{"os":"linux","architecture":"amd64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + arm + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
			}
		}, "", 16},
		{"an attestation of the platform", func(e *env) []string {
			att := e.reg.put(manifestOf(oci, 1, 1), oci, "")
			return []string{`{"mediaType":"` + oci + `","digest":"` + att + `","size":1,"platform":{"os":"linux","architecture":"arm64"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}}`}
		}, "", 0},
		{"two of the platform", func(e *env) []string {
			a := e.reg.put(manifestOf(oci, 1, 2), oci, "")
			b := e.reg.put(manifestOf(oci, 3, 4), oci, "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + a + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + b + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
			}
		}, "", 0},
		{"another variant", func(e *env) []string {
			a := e.reg.put(manifestOf(oci, 1, 2), oci, "")
			return []string{`{"mediaType":"` + oci + `","digest":"` + a + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v7"}}`}
		}, "v8", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			idx := e.reg.put(`{"schemaVersion":2,"mediaType":"`+index+`","manifests":[`+strings.Join(c.entries(e), ",")+`]}`, index, "")
			e.inspect(idx, "linux", "arm64", c.variant)
			res, err := e.launch(t, false)
			if c.size == 0 {
				if err == nil || res.Ending != releasedFailed(t14, "") {
					t.Errorf("ending %+v, err %v", res.Ending, err)
				}
				return
			}
			if err != nil || res.Image.Size != c.size || res.Image.Image != e.settings.Registry+"/"+testName+"@"+idx {
				t.Errorf("image %+v, err %v; want size %d of the index's digest", res.Image, err, c.size)
			}
		})
	}
}

func TestLaunchRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*prebuild.Settings, *prebuild.Run)
	}{
		{"run id", func(_ *prebuild.Settings, r *prebuild.Run) { r.RunID = "../escape" }},
		{"no target", func(_ *prebuild.Settings, r *prebuild.Run) { r.Target = contracts.Target{} }},
		{"a target's system that is no host", func(_ *prebuild.Settings, r *prebuild.Run) { r.Target.System = "GitHub.com/x" }},
		{"relative state", func(s *prebuild.Settings, _ *prebuild.Run) { s.State = "state" }},
		{"relative run config", func(s *prebuild.Settings, _ *prebuild.Run) { s.RunConfig = "runconfig" }},
		{"registry with a tag", func(s *prebuild.Settings, _ *prebuild.Run) { s.Registry += ":1" }},
		{"registry with no host", func(s *prebuild.Settings, _ *prebuild.Run) { s.Registry = "qory/env" }},
		{"agent image by tag", func(s *prebuild.Settings, _ *prebuild.Run) { s.AgentImage = "ghcr.io/qoryai/agent:1" }},
		{"no user", func(s *prebuild.Settings, _ *prebuild.Run) { s.User = "" }},
		{"no helper", func(s *prebuild.Settings, _ *prebuild.Run) { s.Helper = "" }},
		{"inputs of version 2", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.Inputs = []byte(`{"version":2,"environment_ref":"x"}`)
		}},
		{"inputs with no environment_ref", func(_ *prebuild.Settings, r *prebuild.Run) { r.Inputs = []byte(`{"version":1}`) }},
		{"inputs with another member", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.Inputs = []byte(`{"version":1,"environment_ref":"x","target":"y"}`)
		}},
		{"inputs with a member twice", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.Inputs = []byte(`{"version":1,"environment_ref":"x","environment_ref":"y"}`)
		}},
		{"inputs with an empty environment_ref", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.Inputs = []byte(`{"version":1,"environment_ref":""}`)
		}},
		{"inputs with a long environment_ref", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.Inputs = []byte(`{"version":1,"environment_ref":"` + strings.Repeat("é", 201) + `"}`)
		}},
		{"inputs that are no object", func(_ *prebuild.Settings, r *prebuild.Run) { r.Inputs = []byte(`[1]`) }},
		{"two documents", func(_ *prebuild.Settings, r *prebuild.Run) { r.Inputs = []byte(testInputs + testInputs) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			b, _ := json.Marshal(e.script)
			os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600)
			h := &holder{b: []byte(testKey)}
			r := prebuild.Run{RunID: testRunID, Target: testTarget, Key: h, Inputs: []byte(testInputs)}
			c.edit(&e.settings, &r)
			res, err := prebuild.Launch(context.Background(), e.settings, r)
			if err == nil {
				t.Fatal("launched")
			}
			if res.Dir != "" || res.Record || res.Ending != notStarted {
				t.Errorf("result %+v", res)
			}
			if h.cleared == 0 {
				t.Error("the key's holder was not cleared")
			}
			if _, err := os.Stat(filepath.Join(e.settings.State, "pool")); err == nil {
				t.Error("a run directory was made")
			}
			if got := e.steps(t); len(got) != 0 {
				t.Errorf("steps %q ran", got)
			}
			e.noLeak(t, err)
		})
	}
}

func TestEnvironmentRef(t *testing.T) {
	ref, err := prebuild.EnvironmentRef([]byte(` {"environment_ref":"a b","version":1} `))
	if err != nil || ref != "a b" {
		t.Errorf("%q, %v", ref, err)
	}
	ref, err = prebuild.EnvironmentRef([]byte(`{"version":1,"environment_ref":"` + strings.Repeat("é", 200) + `"}`))
	if err != nil || ref != strings.Repeat("é", 200) {
		t.Errorf("200 characters refused: %v", err)
	}
}

func TestName(t *testing.T) {
	if got := prebuild.Name(testTarget); got != testName || len(got) != 16 {
		t.Errorf("Name %s, want %s", got, testName)
	}
	if prebuild.Name(contracts.Target{System: "github.com/acme", Path: "shop"}) != testName {
		t.Error("the name is not of system/path")
	}
	if prebuild.Name(contracts.Target{System: "gitlab.com", Path: "acme/shop"}) == testName {
		t.Error("two targets have one name")
	}
}

func TestLauncher(t *testing.T) {
	e := newEnv(t)
	e.script.Build = 1
	b, _ := json.Marshal(e.script)
	os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600)
	h := &holder{b: []byte(testKey)}
	got := prebuild.Launcher(e.settings)(context.Background(), testRunID, testTarget, h, []byte(testInputs), func(context.Context, provisioner.Image) error {
		t.Error("a build that failed was reported")
		return nil
	})
	if got != releasedFailed(t13, "") {
		t.Errorf("ending %+v", got)
	}
	if !slices.Contains(e.lines(), testRunID+": qory run prebuild exited 1") {
		t.Errorf("the failure was not logged: %q", e.lines())
	}
	if h.cleared == 0 {
		t.Error("the key's holder was not cleared")
	}
	e.noLeak(t, nil)
}
