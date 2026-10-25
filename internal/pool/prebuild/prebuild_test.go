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
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/pool/prebuild"
	devimage "github.com/qoryai/qory/internal/prebuild"
	"github.com/qoryai/qory/internal/proxygit"
)

// The fakes' script and reports are in the runs' configuration directory, the one
// place of the test's that their allow-listed environment names.
const (
	scriptFile = "fake.json"
	readyFile  = "ready"
	askedFile  = "asked"
)

// script is what the fakes do: the open, the fetch and the build, in this process, and
// docker, a program.
type script struct {
	// Open is the open's end: "" opens the run, "fail" refuses it, "block" waits for
	// its context. Record makes the open make the run's record.
	Open   string
	Record bool
	// Fetch is the fetch's end: "" fetches, leaving Head and Commit in work/.git;
	// "fail" fails; "none" fetches nothing, as for a run configuration that lists no
	// repository; "block" waits for its context, which "limit" does too at a time limit
	// of 50 ms. HostProxy "fail" makes the host proxy fail to open.
	Fetch     string
	HostProxy string
	// Head is what the fetch leaves in work/.git: "branch", a loose ref, "packed", a
	// packed ref, "detached", or "garbage"; Commit is the id it leaves.
	Head   string
	Commit string
	// Close is the close's end: "" closes the run, "fail" fails, "gateway" finds it
	// closed by the gateway, failed, and "undelivered" closes it with one event the
	// gateway did not accept.
	Close string
	// Build is the build's end: "" builds, "none" finds no configuration, "fail"
	// fails, "refused" refuses, "limit" reaches its limit, "block" waits for its
	// context, "missing" finds no environment image in its registry, "unreachable"
	// reaches no registry for the environment image.
	Build string
	// EnvironmentImage is the run's environment image, "" for none.
	EnvironmentImage string
	// Push is docker push's exit status; Block "push" makes it wait for SIGTERM and
	// exit 1, and Block "manifest" makes docker buildx imagetools inspect wait so.
	Push  int
	Block string
	// Inspect is what docker image inspect prints.
	Inspect string
	// Manifests are what docker buildx imagetools inspect --raw prints of each digest:
	// the bytes of its manifest. A digest not in it is not found, and exits 1.
	Manifests map[string]string
	// Environment is the environment image the fake build keeps apart, "" for none,
	// and Base the base image it reports.
	Environment string
	Base        string
}

// report is what a fake saw, written to report-<step>.json.
type report struct {
	Env      []string
	Args     []string
	Dir      string
	FD3Pipe  bool
	Problems []string
}

// fd3Pipe reports whether descriptor 3 is open and a pipe, as a key's would be.
func fd3Pipe() bool {
	var st syscall.Stat_t
	return syscall.Fstat(3, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFIFO
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(fakeGit())
		case "push", "image", "buildx":
			os.Exit(fakeDocker(fd3Pipe()))
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

// stepOf is the step a fake docker's arguments are.
func stepOf(args []string) string {
	switch {
	case len(args) > 0 && args[0] == "push":
		return "push"
	case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
		return "inspect"
	case len(args) > 1 && args[0] == "image" && args[1] == "rm":
		return "rm"
	case len(args) > 2 && args[0] == "buildx" && args[1] == "imagetools" && args[2] == "inspect":
		return "manifest"
	}
	return "unknown"
}

// readScript is the script in dir.
func readScript(dir string) (script, error) {
	var s script
	b, err := os.ReadFile(filepath.Join(dir, scriptFile))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// writeReport writes rep as the report of step in dir.
func writeReport(dir, step string, rep report) error {
	out, _ := json.Marshal(rep)
	return os.WriteFile(filepath.Join(dir, "report-"+step+".json"), out, 0o600)
}

// fakeDocker is docker push, docker image inspect, docker buildx imagetools inspect or
// docker image rm, as the script in $XDG_CONFIG_HOME sets it. Each reference that
// docker buildx imagetools inspect is run with is appended to the file asked there.
func fakeDocker(fd3 bool) int {
	config := os.Getenv("XDG_CONFIG_HOME")
	s, err := readScript(config)
	if err != nil {
		return 90
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
	case "push":
		code = s.Push
	case "inspect":
		fmt.Println(s.Inspect)
	case "manifest":
		ref := os.Args[len(os.Args)-1]
		f, err := os.OpenFile(filepath.Join(config, askedFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 93
		}
		fmt.Fprintln(f, ref)
		f.Close()
		_, digest, _ := strings.Cut(ref, "@")
		if body, ok := s.Manifests[digest]; ok {
			fmt.Print(body)
		} else {
			fmt.Fprintf(os.Stderr, "ERROR: %s: not found\n", ref)
			code = 1
		}
	}
	if err := writeReport(config, step, rep); err != nil {
		return 92
	}
	// Each tag docker image rm and docker push ran with is appended to the file removed
	// or pushed.
	for file, at := range map[string]string{"removed": "rm", "pushed": "push"} {
		if step != at {
			continue
		}
		f, err := os.OpenFile(filepath.Join(config, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 92
		}
		fmt.Fprintln(f, os.Args[len(os.Args)-1])
		f.Close()
	}
	if step != "inspect" && step != "manifest" {
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

// fakeProxy is the host proxy's URL that the fake run returns, with a password.
const fakeProxy = "http://qory:hostproxypassword@127.0.0.1:9"

// fakeRun is a run the fake open opened: it records the host proxy's start, the
// build's result Built was called with, and the close's outcome.
type fakeRun struct {
	e       *env
	proxies int
	closed  []session.Outcome
	// built is how many builds had started at each close.
	built []int
	// reported is each result Built was called with, and reportedOpen whether the run
	// was open at each call.
	reported     []session.Built
	reportedOpen []bool
}

func (r *fakeRun) Built(b session.Built) error {
	r.reported = append(r.reported, b)
	r.reportedOpen = append(r.reportedOpen, len(r.closed) == 0)
	return r.e.reportErr
}

func (r *fakeRun) HostProxy() (session.HostProxy, error) {
	if r.e.script.HostProxy == "fail" {
		return session.HostProxy{}, errors.New("listen tcp 127.0.0.1:0: no port")
	}
	r.proxies++
	return session.HostProxy{ProxyURL: fakeProxy, CABundle: "/run/ca-bundle.pem"}, nil
}

func (r *fakeRun) System() string       { return "github.com" }
func (r *fakeRun) Repositories() string { return "acme/shop" }

func (r *fakeRun) Close(_ context.Context, out session.Outcome) (*session.Closed, error) {
	r.closed = append(r.closed, out)
	r.e.mu.Lock()
	r.built = append(r.built, len(r.e.built))
	r.e.mu.Unlock()
	switch r.e.script.Close {
	case "fail":
		return nil, errors.New("the run is closed already")
	case "gateway":
		return &session.Closed{State: "failed", RunClosed: true}, nil
	case "undelivered":
		return &session.Closed{State: out.State, Reason: out.Reason, ExitCode: out.ExitCode, Undelivered: 1}, nil
	}
	return &session.Closed{State: out.State, Reason: out.Reason, ExitCode: out.ExitCode}, nil
}

// open is the fake open, in this process: it checks the spec, then does what the
// script is set to.
func (e *env) open(ctx context.Context, spec session.OpenSpec) (prebuild.Opened, error) {
	e.mu.Lock()
	e.specs = append(e.specs, spec)
	e.mu.Unlock()
	if err := writeReport(e.settings.RunConfig, "fetch", report{Args: []string{"open"}}); err != nil {
		return nil, err
	}
	if e.script.Record {
		if err := os.MkdirAll(filepath.Join(spec.RunsDir, spec.RunID), 0o700); err != nil {
			return nil, err
		}
	}
	switch e.script.Open {
	case "fail":
		return nil, errors.New("the gateway refused the run request: run_not_found")
	case "block":
		e.ready()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	e.run = &fakeRun{e: e}
	return e.run, nil
}

// fetch is the fake fetch, in this process.
func (e *env) fetch(ctx context.Context, dir string, base []string, getenv func(string) string, p proxygit.Proxy, out io.Writer) (bool, error) {
	e.mu.Lock()
	e.fetched = append(e.fetched, fetched{dir: dir, base: base, system: getenv("QORY_RUN_SYSTEM"), list: getenv("QORY_RUN_REPOSITORIES"), proxy: p})
	e.mu.Unlock()
	switch e.script.Fetch {
	case "fail":
		return false, errors.New("qory run: add a credential for github.com that reads acme/shop to the run configuration, then run again. The fetch of acme/shop through the run's gateway failed: fatal: could not read from " + p.URL)
	case "none":
		return false, nil
	case "block", "limit":
		e.ready()
		<-ctx.Done()
		return false, errors.New("qory run: git fetch was stopped")
	}
	if err := writeHead(filepath.Join(dir, ".git"), e.script.Head, e.script.Commit); err != nil {
		return false, err
	}
	fmt.Fprintln(out, "qory run: fetched acme/shop at 8c9d0e1f2a3b in 0.1s")
	return true, nil
}

// build is the fake build, in this process.
func (e *env) build(ctx context.Context, o devimage.Options) (devimage.Result, error) {
	e.mu.Lock()
	e.built = append(e.built, o)
	e.mu.Unlock()
	if err := writeReport(e.settings.RunConfig, "build", report{Env: o.Env, Args: []string{"build"}}); err != nil {
		return devimage.Result{}, err
	}
	fmt.Fprintln(o.Out, "prebuild: built "+o.Tag+" in 1.0s: 123 B")
	fmt.Fprintln(o.Log, "build warns")
	switch e.script.Build {
	case "none":
		return devimage.Result{}, devimage.ErrNoConfiguration
	case "fail":
		return devimage.Result{}, errors.New("devcontainer build did not build the image: exit 1")
	case "refused":
		return devimage.Result{}, &devimage.RefusedError{Err: errors.New("set wall.user to uid:gid, two numbers, neither of them 0")}
	case "limit":
		return devimage.Result{}, devimage.ErrLimit
	case "missing":
		return devimage.Result{}, pullError{why: "Error response from daemon: manifest for r.example/env:1 not found: manifest unknown: manifest unknown", missing: true}
	case "unreachable":
		return devimage.Result{}, pullError{why: `Error response from daemon: Get "https://r.example/v2/": dial tcp: lookup r.example: no such host`}
	case "block":
		e.ready()
		<-ctx.Done()
		return devimage.Result{}, devimage.ErrStopped
	}
	return devimage.Result{Target: o.Tag, Environment: e.script.Environment, Base: e.script.Base}, nil
}

// pullError is the fake build's failed pull of the environment image, as the build
// returns it: [devimage.ErrImageMissing] when missing is set.
type pullError struct {
	why     string
	missing bool
}

// Error is the build's text of the pull.
func (e pullError) Error() string {
	return "check that the Docker engine reaches the registry of r.example/env:1: docker pull exited with an error: " + e.why
}

// Is reports whether target is [devimage.ErrImageMissing], for a missing image.
func (e pullError) Is(target error) bool { return e.missing && target == devimage.ErrImageMissing }

// ready marks that a fake waits for its context.
func (e *env) ready() {
	os.WriteFile(filepath.Join(e.settings.RunConfig, readyFile), nil, 0o600)
}

// fetched is what the fake fetch was given.
type fetched struct {
	dir, system, list string
	base              []string
	proxy             proxygit.Proxy
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

// testDestination and testEnvironmentDestination are the run's image_destination and
// environment_destination.
const (
	testDestination            = "localhost:5001/targets/0123456789abcdef"
	testEnvironmentDestination = "localhost:5001/environments/0123456789abcdef"
)

// testTag and testEnvironmentTag are the tags of the target image and of the
// environment image.
const (
	testTag            = testDestination + ":" + testRunID
	testEnvironmentTag = testEnvironmentDestination + ":" + testRunID
)

// testBase is a base image the build reports.
const testBase = "registry.example.com/base@sha256:2222222222222222222222222222222222222222222222222222222222222222"

// holder is a key holder, as the provisioner client's.
type holder struct {
	b       []byte
	cleared int
}

func (h *holder) Bytes() []byte { return h.b }
func (h *holder) Clear()        { clear(h.b); h.cleared++ }

// put makes docker buildx imagetools inspect --raw print body for its digest, or for
// digest when that is set, and returns the digest it prints body for.
func (e *env) put(body, digest string) string {
	if digest == "" {
		digest = "sha256:" + sum([]byte(body))
	}
	if e.script.Manifests == nil {
		e.script.Manifests = map[string]string{}
	}
	e.script.Manifests[digest] = body
	return digest
}

// asked is each reference docker buildx imagetools inspect ran with, in order.
func (e *env) asked(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.settings.RunConfig, askedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

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
	home      string
	elsewhere string
	script    script
	mu        sync.Mutex
	logs      []string
	// reportErr is what the fake run's Built returns.
	reportErr error
	// specs are the open's specs, run the run it opened, fetched what the fetch was
	// given, and built the build's options.
	specs   []session.OpenSpec
	run     *fakeRun
	fetched []fetched
	built   []devimage.Options
}

func (e *env) log(line string) { e.mu.Lock(); e.logs = append(e.logs, line); e.mu.Unlock() }

func (e *env) lines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.logs)
}

// newEnv is a launch's directories, with HOME at one of them, and a script whose
// digest is that of an image manifest of 100 + 20 + 3 bytes, which docker buildx
// imagetools inspect prints: a run that succeeds. The program's environment holds what
// no command may get.
func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{home: filepath.Join(root, "home"), elsewhere: filepath.Join(root, "elsewhere")}
	e.settings = prebuild.Settings{
		State: filepath.Join(root, "state"), RunConfig: filepath.Join(root, "runconfig"),
		AgentImage: testAgent, User: "1000:1000", Helper: "/opt/qory/qory-linux",
		Git: os.Args[0], Docker: os.Args[0], Log: e.log, Grace: time.Second, Version: "qory 0.0.0-test",
		Machine: "aws-ec2",
	}
	e.settings.Open, e.settings.Fetch, e.settings.Build = e.open, e.fetch, e.build
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
	e.script = script{Record: true, Head: "branch", Commit: testCommit}
	digest := e.put(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3), "")
	e.inspect(digest, "linux", "arm64", "")
	return e
}

// inspect makes docker image inspect name the image in the registry by digest, of the
// platform given, under the image_destination and the environment_destination alike.
func (e *env) inspect(digest, os, arch, variant string) {
	b, _ := json.Marshal(map[string]any{
		"repo_digests": []string{"elsewhere.example.com/other@sha256:" + strings.Repeat("e", 64), testDestination + "@" + digest, testEnvironmentDestination + "@" + digest},
		"os":           os, "architecture": arch, "variant": variant,
	})
	e.script.Inspect = string(b)
}

// launch runs Launch with the test's run, and cancels ctx once a fake waits when stop
// is set.
func (e *env) launch(t *testing.T, stop bool) (prebuild.Result, error) {
	t.Helper()
	return e.launchIn(t, context.Background(), stop)
}

// launchIn is launch with a ctx under parent.
func (e *env) launchIn(t *testing.T, parent context.Context, stop bool) (prebuild.Result, error) {
	t.Helper()
	b, _ := json.Marshal(e.script)
	if err := os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if e.script.Fetch == "limit" {
		defer prebuild.SetFetchTimeout(50 * time.Millisecond)()
	}
	ctx, cancel := context.WithCancel(parent)
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
	res, err := prebuild.Launch(ctx, e.settings, testRun(h, e.script.EnvironmentImage))
	if h.cleared == 0 || !bytes.Equal(h.b, make([]byte, len(testKey))) {
		t.Error("the key's holder was not cleared")
	}
	return res, err
}

// testRun is the test's run with the key's holder h: with the environment_destination,
// or with environment_image envImage when that is set.
func testRun(h *holder, envImage string) prebuild.Run {
	r := prebuild.Run{RunID: testRunID, Key: h, Inputs: []byte(testInputs), ImageDestination: testDestination, EnvironmentImage: envImage}
	if envImage == "" {
		r.EnvironmentDestination = testEnvironmentDestination
	}
	return r
}

// tags is each tag in the file name, removed or pushed, in order.
func (e *env) tags(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.settings.RunConfig, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// closed is the outcomes the fake run closed with, nil when it did not open.
func (e *env) closed() []session.Outcome {
	if e.run == nil {
		return nil
	}
	return e.run.closed
}

// reported is what the fake run's Built was called with.
func (e *env) reported() []session.Built {
	if e.run == nil {
		return nil
	}
	return e.run.reported
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
	for _, s := range []string{"fetch", "build", "push", "inspect", "manifest", "rm"} {
		if _, ok := e.report(t, s); ok {
			ran = append(ran, s)
		}
	}
	return ran
}

// noLeak fails when the key, what starts a secret or the host proxy's password is in a
// log line, in err, in a command's arguments or environment, or in a file under the
// launch's directories, and when the run directory is left.
func (e *env) noLeak(t *testing.T, err error) {
	t.Helper()
	texts := e.lines()
	if err != nil {
		texts = append(texts, err.Error())
	}
	for _, step := range []string{"fetch", "build", "push", "inspect", "manifest", "rm"} {
		if r, ok := e.report(t, step); ok {
			texts = append(append(texts, r.Args...), r.Env...)
			for _, p := range r.Problems {
				t.Errorf("%s: %s", step, p)
			}
			if r.FD3Pipe {
				t.Errorf("%s got a descriptor 3", step)
			}
		}
	}
	for _, s := range texts {
		if strings.Contains(s, testKey[4:24]) || accesskey.ContainsSecret(s) || strings.Contains(s, "hostproxypassword") {
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

// The outcomes the run closes with.
var (
	closedSucceeded = session.Outcome{State: "succeeded", ExitCode: -1}
	closedFailed    = session.Outcome{State: "failed", ExitCode: -1}
	closedTimeout   = session.Outcome{State: "cancelled", Reason: "timeout", ExitCode: 124}
	closedStopped   = session.Outcome{State: "cancelled", Reason: "interrupted", ExitCode: -1}
	// closedImageMissing is the close Forager's Run.Close takes for an image not in its
	// registry: failed, image_missing and -1, in a run no session joined.
	closedImageMissing = session.Outcome{State: "failed", Reason: "image_missing", ExitCode: -1}
)

// testEnvImage is a run's environment image, by digest.
const testEnvImage = "r.example/env@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestLaunchRows follows each end of a prebuild: whether it failed, the steps that ran,
// the outcome the run closed with, none when it did not open, and whether Built was
// called, with the run open.
func TestLaunchRows(t *testing.T) {
	all := []string{"fetch", "build", "push", "inspect", "manifest", "rm"}
	noManifest := []string{"fetch", "build", "push", "inspect", "rm"}
	built := []string{"fetch", "build", "rm"}
	for _, c := range []struct {
		name     string
		edit     func(*env)
		stop     bool
		failed   bool
		steps    []string
		closed   []session.Outcome
		reported bool
	}{
		{"succeeded", func(*env) {}, false, false, all, []session.Outcome{closedSucceeded}, true},
		{"open refused", func(e *env) { e.script.Open, e.script.Record = "fail", false }, false, true, []string{"fetch"}, nil, false},
		{"open refused after its record", func(e *env) { e.script.Open = "fail" }, false, true, []string{"fetch"}, nil, false},
		{"open made no record", func(e *env) { e.script.Record = false }, false, true, []string{"fetch"}, []session.Outcome{closedFailed}, false},
		{"host proxy did not open", func(e *env) { e.script.HostProxy = "fail" }, false, true, []string{"fetch"}, []session.Outcome{closedFailed}, false},
		{"fetch failed", func(e *env) { e.script.Fetch = "fail" }, false, true, []string{"fetch"}, []session.Outcome{closedFailed}, false},
		{"no repository listed", func(e *env) { e.script.Fetch = "none" }, false, true, []string{"fetch"}, []session.Outcome{closedFailed}, false},
		{"fetch at its time limit", func(e *env) { e.script.Fetch = "limit" }, false, true, []string{"fetch"}, []session.Outcome{closedTimeout}, false},
		{"close failed", func(e *env) { e.script.Close = "fail" }, false, true, all, []session.Outcome{closedSucceeded}, true},
		{"gateway closed the run", func(e *env) { e.script.Close = "gateway" }, false, true, all, []session.Outcome{closedSucceeded}, true},
		{"no commit", func(e *env) { e.script.Head = "garbage" }, false, true, []string{"fetch"}, []session.Outcome{closedFailed}, false},
		{"packed ref", func(e *env) { e.script.Head = "packed" }, false, false, all, []session.Outcome{closedSucceeded}, true},
		{"no configuration", func(e *env) { e.script.Build = "none" }, false, true, built, []session.Outcome{closedFailed}, false},
		{"build failed", func(e *env) { e.script.Build = "fail" }, false, true, built, []session.Outcome{closedFailed}, false},
		{"build refused", func(e *env) { e.script.Build = "refused" }, false, true, built, []session.Outcome{closedFailed}, false},
		{"build at its time limit", func(e *env) { e.script.Build = "limit" }, false, true, built, []session.Outcome{closedFailed}, false},
		{"built from an environment image", func(e *env) { e.script.EnvironmentImage = testEnvImage }, false, false, all, []session.Outcome{closedSucceeded}, true},
		{"environment image not in its registry", func(e *env) { e.script.EnvironmentImage, e.script.Build = testEnvImage, "missing" }, false, true, built, []session.Outcome{closedImageMissing}, false},
		{"environment image's registry unreachable", func(e *env) { e.script.EnvironmentImage, e.script.Build = testEnvImage, "unreachable" }, false, true, built, []session.Outcome{closedFailed}, false},
		{"stopped during the build from an environment image", func(e *env) { e.script.EnvironmentImage, e.script.Build = testEnvImage, "block" }, true, true, built, []session.Outcome{closedStopped}, false},
		{"push failed", func(e *env) { e.script.Push = 1 }, false, true, []string{"fetch", "build", "push", "rm"}, []session.Outcome{closedFailed}, false},
		{"no digest in the registry", func(e *env) {
			e.script.Inspect = `{"repo_digests":["elsewhere.example.com/x@sha256:` + strings.Repeat("e", 64) + `"],"os":"linux","architecture":"arm64","variant":""}`
		}, false, true, noManifest, []session.Outcome{closedFailed}, false},
		{"digest not of the shape", func(e *env) { e.inspect("sha256:ABC", "linux", "arm64", "") }, false, true, noManifest, []session.Outcome{closedFailed}, false},
		{"manifest not in the registry", func(e *env) { e.inspect("sha256:"+strings.Repeat("0", 64), "linux", "arm64", "") }, false, true, all, []session.Outcome{closedFailed}, false},
		{"manifest's bytes not of its digest", func(e *env) {
			d := e.put(manifestOf("", 1, 1), "sha256:"+strings.Repeat("a", 64))
			e.inspect(d, "linux", "arm64", "")
		}, false, true, all, []session.Outcome{closedFailed}, false},
		{"built refused", func(e *env) { e.reportErr = errors.New("write the build's result once per run") }, false, true, all, []session.Outcome{closedFailed}, true},
		{"stopped during the open", func(e *env) { e.script.Open, e.script.Record = "block", false }, true, true, []string{"fetch"}, nil, false},
		{"stopped during the fetch", func(e *env) { e.script.Fetch = "block" }, true, true, []string{"fetch"}, []session.Outcome{closedStopped}, false},
		{"stopped during the build", func(e *env) { e.script.Build = "block" }, true, true, built, []session.Outcome{closedStopped}, false},
		{"stopped during the push", func(e *env) { e.script.Block = "push" }, true, true, []string{"fetch", "build", "push", "rm"}, []session.Outcome{closedStopped}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.edit(e)
			_, err := e.launch(t, c.stop)
			if (err != nil) != c.failed {
				t.Errorf("err %v, want failed %v", err, c.failed)
			}
			if got := e.steps(t); !slices.Equal(got, c.steps) {
				t.Errorf("steps %q, want %q", got, c.steps)
			}
			if !slices.Equal(e.closed(), c.closed) {
				t.Errorf("the run closed with %+v, want %+v", e.closed(), c.closed)
			}
			// The run closes once the build ended, when one started.
			if e.run != nil && len(e.run.built) == 1 && (e.run.built[0] == 1) != slices.Contains(c.steps, "build") {
				t.Errorf("the run closed with %d builds started", e.run.built[0])
			}
			if e.script.EnvironmentImage != "" && (len(e.built) != 1 || e.built[0].EnvironmentImage != testEnvImage) {
				t.Errorf("the build was given %+v", e.built)
			}
			if got := len(e.reported()); got != map[bool]int{true: 1}[c.reported] {
				t.Errorf("Built called %d times, want %v", got, c.reported)
			}
			if e.run != nil && slices.Contains(e.run.reportedOpen, false) {
				t.Error("Built was called on a closed run")
			}
			e.noLeak(t, err)
		})
	}
}

// TestLaunchFetchErrors gives the error of each failed fetch step without qory run's
// prefix, its fix first, and with the host proxy's password hidden.
func TestLaunchFetchErrors(t *testing.T) {
	for _, c := range []struct {
		edit func(*env)
		want string
	}{
		{func(e *env) { e.script.Open, e.script.Record = "fail", false }, "check that this machine reaches the run's gateway: the run did not open there: the gateway refused the run request: run_not_found"},
		{func(e *env) { e.script.HostProxy = "fail" }, "check that qory run can listen on a port of 127.0.0.1: the run's host proxy, through which qory run fetches the run's code, did not open: listen tcp 127.0.0.1:0: no port"},
		{func(e *env) { e.script.Fetch = "none" }, "list the run's repositories in QORY_RUN_REPOSITORIES in the run configuration: a prebuild fetches them, and the variable is empty"},
		{func(e *env) { e.script.Close = "fail" }, "update qory: the run could not be closed: the run is closed already"},
		{func(e *env) { e.script.Close = "gateway" }, "check the run's record in Qory Apiary: the gateway closed the run as failed before qory run closed it"},
	} {
		e := newEnv(t)
		c.edit(e)
		if _, err := e.launch(t, false); err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	e := newEnv(t)
	e.script.Fetch = "fail"
	_, err := e.launch(t, false)
	if err == nil || !strings.HasPrefix(err.Error(), "add a credential for github.com that reads acme/shop") {
		t.Errorf("%v, want the fetch's line without its prefix", err)
	}
	e.noLeak(t, err)
}

func TestLaunchDigestNotOfTheShape(t *testing.T) {
	for _, d := range []string{"sha256:ABC", "sha256:" + strings.Repeat("a", 63) + "/x", "sha512:" + strings.Repeat("a", 64)} {
		e := newEnv(t)
		e.inspect(d, "linux", "arm64", "")
		_, err := e.launch(t, false)
		if err == nil || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
			t.Errorf("%s: closed %+v, err %v", d, e.closed(), err)
		}
		if got := e.asked(t); len(got) != 0 {
			t.Errorf("%s: docker buildx imagetools inspect ran with %q", d, got)
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
	res, err := prebuild.Launch(ctx, e.settings, testRun(h, ""))
	if err == nil || res.Record || e.run != nil {
		t.Errorf("result %+v, opened %v, err %v", res, e.run != nil, err)
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
// docker, when it is a name alone, on the PATH every command gets: with an empty file of
// each name in an entry in the runs folder alone, none is found, the launch fails with
// exec's error at that program's step, and no later step runs.
func TestHostProgramsByNameAreFoundOnTheCleanedPATH(t *testing.T) {
	for _, c := range []struct {
		name, want string
		edit       func(*prebuild.Settings)
		ran        []string
	}{
		{"git", "git init: ", func(s *prebuild.Settings) { s.Git = "" }, nil},
		{"docker", "docker push could not be started: ", func(s *prebuild.Settings) { s.Docker = "docker" }, []string{"fetch", "build"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.edit(&e.settings)
			hostbin := filepath.Join(explore.RunsDir(e.settings.State), "hostbin")
			if err := os.MkdirAll(hostbin, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"git", "docker"} {
				if err := os.WriteFile(filepath.Join(hostbin, n), nil, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", hostbin)
			_, err := e.launch(t, false)
			if !errors.Is(err, exec.ErrNotFound) || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v, want exec.ErrNotFound after %q", err, c.want)
			}
			for _, step := range []string{"fetch", "build", "push", "inspect", "manifest"} {
				if _, ran := e.report(t, step); ran != slices.Contains(c.ran, step) {
					t.Errorf("%s ran %v", step, ran)
				}
			}
		})
	}
}

// TestTheFetchRunsNoGitFromTheRunsFolder runs the fetch of [preparation.Run] with a
// git in an entry of the runs folder first on the pool's PATH, where a walled run could
// write it: that git does not run, because the fetch's git starts from the environment
// of the pool's commands, and the fetch through the closed proxy port fails after
// git on the cleaned PATH ran.
func TestTheFetchRunsNoGitFromTheRunsFolder(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on this machine")
	}
	e := newEnv(t)
	e.settings.Fetch, e.settings.Git = nil, ""
	hostbin := filepath.Join(explore.RunsDir(e.settings.State), "hostbin")
	if err := os.MkdirAll(hostbin, 0o700); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(e.home, "planted-git-ran")
	planted := "#!/bin/sh\necho ran >> '" + ran + "'\nexec '" + git + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(hostbin, "git"), []byte(planted), 0o700); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{hostbin, filepath.Dir(git), "/usr/bin", "/bin"}, sep))
	// The fetch's hold on work is in the records of walled runs under the pool's
	// XDG_STATE_HOME.
	t.Setenv("XDG_STATE_HOME", filepath.Join(e.home, "pool-state"))
	_, err = e.launch(t, false)
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the git in the runs folder ran")
	}
	if err == nil || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) || len(e.built) != 0 {
		t.Errorf("%v, closed %+v: want the fetch through the closed proxy port to fail", err, e.closed())
	}
	if !strings.Contains(err.Error(), "acme/shop") {
		t.Errorf("%v, want the fetch's line for acme/shop", err)
	}
	e.noLeak(t, err)
}

// TestLaunchCommands runs the fetch and the build in this process, and docker with its
// arguments, in the pool's folder, outside every run directory: the run opens at the
// gateway of its run key, with no wall, the machine of Settings, and its record in the
// run's state, the fetch
// goes into work through the run's host proxy, and the fetch and the build get the
// allowed environment, with a PATH that has no entry in the pool's runs folder, as every
// docker command does.
func TestLaunchCommands(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "orbstack")
	t.Setenv("CI", "true")
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
	tag := testTag
	digest := "sha256:" + sum([]byte(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3)))
	if len(e.specs) != 1 {
		t.Fatalf("opened %d times", len(e.specs))
	}
	spec := e.specs[0]
	gw, ok := spec.Gateway.(session.RemoteGateway)
	if !ok || gw.RunKey != testKey || spec.Wall != nil || spec.RunID != testRunID || spec.ForagerVersion != "qory 0.0.0-test" ||
		spec.RunsDir != filepath.Join(state, "qory", "runs", "work-"+sum([]byte(work))[:12]) || len(spec.Labels) != 0 || spec.About != nil || spec.Machine != "aws-ec2" {
		t.Errorf("the open's spec: run %s, wall %v, runs %s, version %s, machine %q, by run key %v", spec.RunID, spec.Wall, spec.RunsDir, spec.ForagerVersion, spec.Machine, ok && gw.RunKey == testKey)
	}
	if !res.Record {
		t.Error("no record")
	}
	if len(e.fetched) != 1 || e.fetched[0].dir != work || e.fetched[0].system != "github.com" || e.fetched[0].list != "acme/shop" ||
		e.fetched[0].proxy != (proxygit.Proxy{URL: fakeProxy, CABundle: "/run/ca-bundle.pem"}) || e.run.proxies != 1 {
		t.Errorf("the fetch was given %+v", e.fetched)
	}
	if len(e.built) != 1 {
		t.Fatalf("built %d times", len(e.built))
	}
	o := e.built[0]
	if !slices.Equal(e.fetched[0].base, o.Env) {
		t.Errorf("the fetch starts git from\n%q\nwant the build's environment\n%q", e.fetched[0].base, o.Env)
	}
	if o.Repository != work || o.AgentImage != testAgent || o.User != "1000:1000" || o.Tag != tag || o.Helper != "/opt/qory/qory-linux" || !slices.Equal(o.SeedArgs, []string{"run", "seed"}) ||
		o.EnvironmentTag != testEnvironmentTag || o.EnvironmentImage != "" {
		t.Errorf("the build's options %+v", o)
	}
	for _, c := range []struct {
		step string
		args []string
		dir  string
	}{
		{"push", []string{"push", tag}, pool},
		{"inspect", []string{"image", "inspect", "--format", `{"repo_digests":{{json .RepoDigests}},"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json .Variant}}}`, tag}, pool},
		{"manifest", []string{"buildx", "imagetools", "inspect", "--raw", testDestination + "@" + digest}, pool},
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
	}
	for _, step := range []string{"build", "push", "inspect", "manifest", "rm"} {
		r, _ := e.report(t, step)
		for _, kv := range r.Env {
			n := kv[:strings.IndexByte(kv, '=')]
			if !slices.Contains([]string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}, n) {
				t.Errorf("%s got %s", step, n)
			}
		}
		for _, want := range []string{"XDG_CONFIG_HOME=" + e.settings.RunConfig, "XDG_STATE_HOME=" + state, "DOCKER_CONTEXT=orbstack", "PATH=/usr/bin" + sep + "/bin"} {
			if !slices.Contains(r.Env, want) {
				t.Errorf("%s's environment has no %s: %q", step, want, r.Env)
			}
		}
	}
	want := session.Built{Image: testDestination + "@" + digest, Size: 123, EnvironmentRef: "3f2a1b0c9d8e", Commit: testCommit}
	if res.Image != want || !slices.Equal(e.reported(), []session.Built{want}) {
		t.Errorf("image %+v, reported %+v, want %+v", res.Image, e.reported(), want)
	}
	if got := e.tags(t, "pushed"); !slices.Equal(got, []string{tag}) {
		t.Errorf("docker push pushed %q, want the target image alone, since the build kept no environment image", got)
	}
	if got := e.asked(t); !slices.Equal(got, []string{testDestination + "@" + digest}) {
		t.Errorf("docker buildx imagetools inspect ran with %q", got)
	}
	lines := e.lines()
	for _, want := range []string{testRunID + ": fetched acme/shop at 8c9d0e1f2a3b in 0.1s", testRunID + ": prebuild: built " + tag + " in 1.0s: 123 B", testRunID + ": build warns", testRunID + ": push warns"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no log line %q in %q", want, lines)
		}
	}
	e.noLeak(t, err)
}

// TestLaunchPushesTheEnvironmentImage is a build that keeps an environment image apart,
// tagged <environment_destination>:<run_id>: the launch pushes it, then the target image
// at <image_destination>:<run_id>, writes dev.qory.run.built with both by digest and the
// base image, and removes both local tags.
func TestLaunchPushesTheEnvironmentImage(t *testing.T) {
	e := newEnv(t)
	e.script.Environment, e.script.Base = testEnvironmentTag, testBase
	res, err := e.launch(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Built != (devimage.Result{Target: testTag, Environment: testEnvironmentTag, Base: testBase}) {
		t.Errorf("the result's build is %+v", res.Built)
	}
	if got := e.tags(t, "pushed"); !slices.Equal(got, []string{testEnvironmentTag, testTag}) {
		t.Errorf("docker push pushed %q, want the environment image, then the target image", got)
	}
	digest := "sha256:" + sum([]byte(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3)))
	want := session.Built{
		Image: testDestination + "@" + digest, Size: 123, Commit: testCommit, EnvironmentRef: "3f2a1b0c9d8e",
		EnvironmentImage: testEnvironmentDestination + "@" + digest, Base: testBase,
	}
	if !slices.Equal(e.reported(), []session.Built{want}) {
		t.Errorf("Built with %+v, want %+v", e.reported(), want)
	}
	if !slices.Equal(e.closed(), []session.Outcome{closedSucceeded}) {
		t.Errorf("the run closed with %+v", e.closed())
	}
	if got := e.tags(t, "removed"); !slices.Equal(got, []string{testEnvironmentTag, testTag}) {
		t.Errorf("docker image rm removed %q", got)
	}
	e.noLeak(t, err)
}

// TestLaunchFromAnEnvironmentImagePushesNoEnvironmentImage is a run with
// environment_image: the build starts from it with no environment tag, the launch
// pushes the target image alone, and dev.qory.run.built has environment_image as the
// run set it.
func TestLaunchFromAnEnvironmentImagePushesNoEnvironmentImage(t *testing.T) {
	e := newEnv(t)
	e.script.EnvironmentImage = testEnvImage
	if _, err := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	if len(e.built) != 1 || e.built[0].EnvironmentImage != testEnvImage || e.built[0].EnvironmentTag != "" || e.built[0].Tag != testTag {
		t.Errorf("the build was given %+v", e.built)
	}
	if got := e.tags(t, "pushed"); !slices.Equal(got, []string{testTag}) {
		t.Errorf("docker push pushed %q, want the target image alone", got)
	}
	if r := e.reported(); len(r) != 1 || r[0].EnvironmentImage != testEnvImage || r[0].Base != "" {
		t.Errorf("Built with %+v", r)
	}
}

// TestLaunchEnvironmentPushFails is a push of the environment image that fails: the
// target image is not pushed, nothing is reported, and the run closes failed.
func TestLaunchEnvironmentPushFails(t *testing.T) {
	e := newEnv(t)
	e.script.Environment, e.script.Push = testEnvironmentTag, 1
	_, err := e.launch(t, false)
	if err == nil || err.Error() != "docker push exited 1" {
		t.Errorf("%v, want docker push's exit", err)
	}
	if got := e.tags(t, "pushed"); !slices.Equal(got, []string{testEnvironmentTag}) {
		t.Errorf("docker push pushed %q", got)
	}
	if len(e.reported()) != 0 || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
		t.Errorf("reported %+v, closed %+v", e.reported(), e.closed())
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
			img := e.put(manifestOf(oci, 10, 5), "")
			att := e.put(manifestOf(oci, 1, 1), "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + img + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}`,
				`{"mediaType":"` + oci + `","digest":"` + att + `","size":1,"platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}}`,
			}
		}, "", 15},
		{"the platform's image among two", func(e *env) []string {
			amd := e.put(manifestOf(oci, 30, 30), "")
			arm := e.put(manifestOf(oci, 7, 7), "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + amd + `","size":1,"platform":{"os":"linux","architecture":"amd64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + arm + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}`,
			}
		}, "v8", 14},
		{"the platform's image among two, no variant", func(e *env) []string {
			amd := e.put(manifestOf(oci, 30, 30), "")
			arm := e.put(manifestOf(oci, 8, 8), "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + amd + `","size":1,"platform":{"os":"linux","architecture":"amd64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + arm + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
			}
		}, "", 16},
		{"an attestation of the platform", func(e *env) []string {
			att := e.put(manifestOf(oci, 1, 1), "")
			return []string{`{"mediaType":"` + oci + `","digest":"` + att + `","size":1,"platform":{"os":"linux","architecture":"arm64"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}}`}
		}, "", 0},
		{"two of the platform", func(e *env) []string {
			a := e.put(manifestOf(oci, 1, 2), "")
			b := e.put(manifestOf(oci, 3, 4), "")
			return []string{
				`{"mediaType":"` + oci + `","digest":"` + a + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
				`{"mediaType":"` + oci + `","digest":"` + b + `","size":1,"platform":{"os":"linux","architecture":"arm64"}}`,
			}
		}, "", 0},
		{"another variant", func(e *env) []string {
			a := e.put(manifestOf(oci, 1, 2), "")
			return []string{`{"mediaType":"` + oci + `","digest":"` + a + `","size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v7"}}`}
		}, "v8", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			idx := e.put(`{"schemaVersion":2,"mediaType":"`+index+`","manifests":[`+strings.Join(c.entries(e), ",")+`]}`, "")
			e.inspect(idx, "linux", "arm64", c.variant)
			res, err := e.launch(t, false)
			if c.size == 0 {
				if err == nil || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
					t.Errorf("closed %+v, err %v", e.closed(), err)
				}
				return
			}
			if err != nil || res.Image.Size != c.size || res.Image.Image != testDestination+"@"+idx {
				t.Errorf("image %+v, err %v; want size %d of the index's digest", res.Image, err, c.size)
			}
			if got := e.asked(t); len(got) != 2 || got[0] != testDestination+"@"+idx {
				t.Errorf("docker buildx imagetools inspect ran with %q; want the index, then its manifest", got)
			}
		})
	}
}

// TestLaunchNoMediaType reads a manifest and an index that have no media type, as the
// OCI image specification allows: one that lists manifests is an index, any other an
// image's manifest.
func TestLaunchNoMediaType(t *testing.T) {
	e := newEnv(t)
	e.inspect(e.put(manifestOf("", 4, 5, 6), ""), "linux", "arm64", "")
	if res, err := e.launch(t, false); err != nil || res.Image.Size != 15 {
		t.Errorf("a manifest: size %d, err %v; want 15", res.Image.Size, err)
	}
	e = newEnv(t)
	img := e.put(manifestOf("", 2, 3), "")
	idx := e.put(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"`+img+`","size":1,"platform":{"os":"linux","architecture":"arm64"}}]}`, "")
	e.inspect(idx, "linux", "arm64", "")
	if res, err := e.launch(t, false); err != nil || res.Image.Size != 5 {
		t.Errorf("an index: size %d, err %v; want 5", res.Image.Size, err)
	}
}

// TestLaunchStoppedDuringTheManifestRead stops the pool program while docker buildx
// imagetools inspect runs, by a cancel and by the end of the launch's deadline: the
// error is a stop during the push, not a fix for docker, and the run closes cancelled.
func TestLaunchStoppedDuringTheManifestRead(t *testing.T) {
	for _, c := range []struct {
		name string
		stop bool
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"cancelled", true, func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{"deadline", false, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Second)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.script.Block = "manifest"
			ctx, cancel := c.ctx()
			defer cancel()
			_, err := e.launchIn(t, ctx, c.stop)
			if err == nil || !strings.HasPrefix(err.Error(), "start the run again with a new run code: the run was cancelled during the push: ") {
				t.Errorf("%v, want the stop during the push", err)
			}
			if !slices.Equal(e.closed(), []session.Outcome{closedStopped}) || len(e.reported()) != 0 {
				t.Errorf("closed %+v, reported %+v", e.closed(), e.reported())
			}
			if got := e.asked(t); len(got) != 1 {
				t.Errorf("docker buildx imagetools inspect ran with %q; want one read", got)
			}
			e.noLeak(t, err)
		})
	}
}

// TestLaunchManifestReadAtItsTimeLimit fails the store when docker buildx imagetools
// inspect runs past the read's time limit, while the launch goes on: the error starts
// with its fix, then the limit.
func TestLaunchManifestReadAtItsTimeLimit(t *testing.T) {
	defer prebuild.SetManifestTimeout(200 * time.Millisecond)()
	e := newEnv(t)
	e.script.Block = "manifest"
	_, err := e.launch(t, false)
	want := "check that docker buildx is installed and that docker reaches the registry localhost:5001: docker buildx imagetools inspect --raw of the pushed image failed: it ran past its time limit of 200ms"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
		t.Errorf("closed %+v", e.closed())
	}
	e.noLeak(t, err)
}

// TestLaunchManifestNotRead fails the store when docker buildx imagetools inspect
// exits 1: the error starts with its fix, then the registry's host and how the command
// ended, and docker's error line is in the log.
func TestLaunchManifestNotRead(t *testing.T) {
	e := newEnv(t)
	missing := "sha256:" + strings.Repeat("0", 64)
	e.inspect(missing, "linux", "arm64", "")
	_, err := e.launch(t, false)
	want := "check that docker buildx is installed and that docker reaches the registry localhost:5001: docker buildx imagetools inspect --raw of the pushed image failed: exit status 1"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if !slices.Equal(e.closed(), []session.Outcome{closedFailed}) || len(e.reported()) != 0 {
		t.Errorf("closed %+v, reported %+v", e.closed(), e.reported())
	}
	line := testRunID + ": ERROR: " + testDestination + "@" + missing + ": not found"
	if !slices.Contains(e.lines(), line) {
		t.Errorf("no log line %q in %q", line, e.lines())
	}
	e.noLeak(t, err)
}

func TestLaunchRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*prebuild.Settings, *prebuild.Run)
	}{
		{"run id", func(_ *prebuild.Settings, r *prebuild.Run) { r.RunID = "../escape" }},
		{"relative state", func(s *prebuild.Settings, _ *prebuild.Run) { s.State = "state" }},
		{"relative run config", func(s *prebuild.Settings, _ *prebuild.Run) { s.RunConfig = "runconfig" }},
		{"image destination with a tag", func(_ *prebuild.Settings, r *prebuild.Run) { r.ImageDestination += ":1" }},
		{"image destination with no host", func(_ *prebuild.Settings, r *prebuild.Run) { r.ImageDestination = "qory/env" }},
		{"no image destination", func(_ *prebuild.Settings, r *prebuild.Run) { r.ImageDestination = "" }},
		{"environment destination with a digest", func(_ *prebuild.Settings, r *prebuild.Run) { r.EnvironmentDestination = testEnvImage }},
		{"both environment members", func(_ *prebuild.Settings, r *prebuild.Run) { r.EnvironmentImage = testEnvImage }},
		{"neither environment member", func(_ *prebuild.Settings, r *prebuild.Run) { r.EnvironmentDestination = "" }},
		{"environment image by tag", func(_ *prebuild.Settings, r *prebuild.Run) {
			r.EnvironmentDestination, r.EnvironmentImage = "", "r.example/env:1"
		}},
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
			r := testRun(h, "")
			c.edit(&e.settings, &r)
			res, err := prebuild.Launch(context.Background(), e.settings, r)
			if err == nil {
				t.Fatal("launched")
			}
			if res.Dir != "" || res.Record || e.run != nil {
				t.Errorf("result %+v, opened %v", res, e.run != nil)
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

// TestLaunchRefusedPlaces gives the error of a run whose places at the registry no
// prebuild takes, its fix first.
func TestLaunchRefusedPlaces(t *testing.T) {
	const (
		notAPlace = "ask Qory Apiary's instance admin: the run's %s is not a place at a registry, a host and a path of at most 255 characters with no tag and no digest"
		upper     = "ask Qory Apiary's instance admin: the run's %s has upper-case letters, and qory writes images to dev.qory.run.built by lower-case references alone"
		secret    = "ask Qory Apiary's instance admin: the run's %s contains qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_, which start a secret, and qory writes no image with them to dev.qory.run.built"
		notPinned = "ask Qory Apiary's instance admin: the run's environment_image is not an image pinned by digest, NAME@sha256: and 64 hexadecimal digits with no tag, NAME at most 255 characters"
	)
	digest := "@sha256:" + strings.Repeat("a", 64)
	fromImage := func(image string) func(*prebuild.Run) {
		return func(r *prebuild.Run) { r.EnvironmentDestination, r.EnvironmentImage = "", image }
	}
	for _, c := range []struct {
		name string
		edit func(*prebuild.Run)
		want string
	}{
		{"no registry host", func(r *prebuild.Run) { r.ImageDestination = "qory/env" }, fmt.Sprintf(notAPlace, "image_destination")},
		{"a tag", func(r *prebuild.Run) { r.EnvironmentDestination += ":1" }, fmt.Sprintf(notAPlace, "environment_destination")},
		{"too long", func(r *prebuild.Run) { r.ImageDestination = "registry.example.com/" + strings.Repeat("a", 240) }, fmt.Sprintf(notAPlace, "image_destination")},
		{"upper-case host", func(r *prebuild.Run) { r.ImageDestination = "Registry.Example.com/targets/x" }, fmt.Sprintf(upper, "image_destination")},
		{"upper-case path", func(r *prebuild.Run) { r.EnvironmentDestination = "registry.example.com/Environments/x" }, fmt.Sprintf(upper, "environment_destination")},
		{"qak_", func(r *prebuild.Run) { r.ImageDestination = "registry.example.com/qak_x" }, fmt.Sprintf(secret, "image_destination")},
		{"qecr_", func(r *prebuild.Run) { r.EnvironmentDestination = "registry.example.com/qecr_x" }, fmt.Sprintf(secret, "environment_destination")},
		{"both", func(r *prebuild.Run) { r.EnvironmentImage = testEnvImage }, "ask Qory Apiary's instance admin: the run has both or neither of environment_destination and environment_image, and a prebuild takes one"},
		{"environment image by tag", fromImage("r.example/env:1"), notPinned},
		{"environment image with tag and digest", fromImage("r.example/env:1" + digest), notPinned},
		{"environment image upper-case host", fromImage("Registry.example.com/env" + digest), fmt.Sprintf(upper, "environment_image")},
		{"environment image qrk_", fromImage("registry.example.com/qrk_env" + digest), fmt.Sprintf(secret, "environment_image")},
		{"QeCn_", func(r *prebuild.Run) { r.ImageDestination = "registry.example.com/QeCn_x" }, fmt.Sprintf(secret, "image_destination")},
		{"qECP_", func(r *prebuild.Run) { r.EnvironmentDestination = "registry.example.com/qECP_x" }, fmt.Sprintf(secret, "environment_destination")},
		{"environment image QecG_", fromImage("registry.example.com/QecG_env" + digest), fmt.Sprintf(secret, "environment_image")},
		{"qEcI_", func(r *prebuild.Run) { r.ImageDestination = "registry.example.com/qEcI_x" }, fmt.Sprintf(secret, "image_destination")},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			r := testRun(&holder{b: []byte(testKey)}, "")
			c.edit(&r)
			if _, err := prebuild.Launch(context.Background(), e.settings, r); err == nil || err.Error() != c.want {
				t.Errorf("%v, want %q", err, c.want)
			}
			if len(e.specs) != 0 {
				t.Error("the run opened")
			}
			if _, err := os.Stat(e.settings.State); !os.IsNotExist(err) {
				t.Errorf("the state directory is there: %v", err)
			}
		})
	}
}

// TestCheckRunTakesTheContractsPlaces is a run whose image_destination and
// environment_destination are each a lower-case place that image_place of the run
// contract's documents.schema.json accepts, hyphens inside a host's label included: the
// check passes. A place that image_place refuses is not a place at a registry.
func TestCheckRunTakesTheContractsPlaces(t *testing.T) {
	const notAPlace = "ask Qory Apiary's instance admin: the run's %s is not a place at a registry, a host and a path of at most 255 characters with no tag and no digest"
	e := newEnv(t)
	for _, place := range []string{
		"xn--bcher-kva.example.com/targets/x",
		"my--registry.example.com/targets/x",
		"my-registry.example.com:5000/targets/x",
		"registry.example.com/a.b/c_d/e__f/g-h/i---j",
		"registry:5000/env",
		"localhost/env",
		"localhost:5000/env",
		"registry.example.com/" + strings.Repeat("a", 234),
	} {
		for _, member := range []string{"image_destination", "environment_destination"} {
			r := testRun(&holder{b: []byte(testKey)}, "")
			if member == "image_destination" {
				r.ImageDestination = place
			} else {
				r.EnvironmentDestination = place
			}
			if _, err := prebuild.CheckRun(e.settings, r); err != nil {
				t.Errorf("%s %s: %v", member, place, err)
			}
		}
	}
	for _, place := range []string{
		"-registry.example.com/env",
		"registry-.example.com/env",
		"registry..example.com/env",
		"reg_istry.example.com/env",
		"registry.example.com:123456/env",
		"registry.example.com:/env",
		"localhost.:5000/env",
		"registry/env",
		"registry.example.com/env-",
		"registry.example.com/a___b",
	} {
		r := testRun(&holder{b: []byte(testKey)}, "")
		r.ImageDestination = place
		if _, err := prebuild.CheckRun(e.settings, r); err == nil || err.Error() != fmt.Sprintf(notAPlace, "image_destination") {
			t.Errorf("%s: %v, want %q", place, err, fmt.Sprintf(notAPlace, "image_destination"))
		}
	}
}

// TestLaunchUndelivered is a prebuild whose close leaves one event the gateway did not
// accept, on a run the gateway did not close: Launch returns the line that starts with
// the fix and contains the path of the record's undelivered/, and leaves the run
// directory, with the record, in place. The local tags go.
func TestLaunchUndelivered(t *testing.T) {
	e := newEnv(t)
	e.script.Close = "undelivered"
	res, err := e.launch(t, false)
	record := filepath.Join(explore.Dir(e.settings.State, testRunID), "state")
	want := "check that this machine reaches the run's gateway: 1 event of the run did not reach it; "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "/undelivered contains them") || !strings.Contains(err.Error(), record) {
		t.Fatalf("%v, want %q, the record under %s", err, want, record)
	}
	if !res.Record {
		t.Error("the result has no record")
	}
	if len(e.reported()) != 1 || !slices.Equal(e.closed(), []session.Outcome{closedSucceeded}) {
		t.Errorf("Built %d times, closed with %+v", len(e.reported()), e.closed())
	}
	if _, statErr := os.Stat(explore.Dir(e.settings.State, testRunID)); statErr != nil {
		t.Errorf("the run directory is gone: %v", statErr)
	}
	if got := e.tags(t, "removed"); !slices.Equal(got, []string{testTag}) {
		t.Errorf("docker image rm removed %q", got)
	}
	if accesskey.ContainsSecret(err.Error()) || strings.Contains(err.Error(), testKey[4:24]) {
		t.Errorf("a secret is in %q", err)
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
