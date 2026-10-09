package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/receiver"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/ui"
)

// testAccessKey is the access key id of every test's server.
const testAccessKey = "ak_f1xt0re000000000"

// testNode is the node every test's server names in discovery.
const testNode = "nd_0123456789abcdef"

// fakeServer stands in for the server Forager reports to: Forager's own
// receiver, which verifies every request under the machine's access key and signs
// every answer under a key of its own, with the configuration document, the run
// configuration with the policy it was given, and a store that keeps the events. It
// keeps the queries it saw, and counts the requests it refused with a 401.
type fakeServer struct {
	*httptest.Server
	// signer is the server's signing key, which forager.yaml pins; key is the
	// machine's access key, whose secret serverFile writes.
	signer, key *accesskey.Key
	mu          sync.Mutex
	policy      string
	// variables is the JSON of the run configuration's variables, none when empty.
	variables string
	queries   []string
	events    []map[string]any
	refused   int
	// instances are the X-Qory-Instance-Id and X-Qory-Instance-Name of every request.
	instances [][2]string
	// revoked, secrets and full make the server know no access key, list secrets in
	// discovery, and answer the ping with instance_limit. stopPing and stopRun make it
	// want nothing more of a run, a signed 410 without a code: to every delivery, the
	// ping's included, and to every delivery once it holds an event, after the ping.
	revoked, secrets, full, stopPing, stopRun bool
	// onStop, when set, is called each time the server answers a delivery with its 410.
	onStop func()
	// unavailable answers every delivery an unsigned 503, which is no answer.
	unavailable bool
}

// newFakeServer starts a server whose run configuration carries policy, the JSON of a
// security_policy, or names no run section when policy is empty.
func newFakeServer(t *testing.T, policy string) *fakeServer {
	t.Helper()
	f := &fakeServer{policy: policy, signer: newKey(t), key: newKey(t)}
	h := &receiver.Handler{
		Signer: f.signer,
		Store:  f,
		Keys: func(id string) (receiver.AccessKey, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return receiver.AccessKey{PublicKey: f.key.PublicKey()}, id == testAccessKey && !f.revoked
		},
		Configuration: func() ([]byte, string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			doc := `{"version":1,"node_id":"` + testNode + `","events":{"url":"` + f.URL + `/v1/events","types":["*"]}`
			if f.policy != "" {
				doc += `,"run":{"url":"` + f.URL + `/v1/run-configuration"}`
			}
			if f.secrets {
				doc += `,"secrets":{"url":"` + f.URL + `/v1/secrets"}`
			}
			doc += `,"apiary_public_key":[{"alg":"ed25519","public_key":"` + f.signer.PublicKey().String() + `"}]}`
			return []byte(doc), digest(doc)
		},
		RunConfiguration: func(map[string]string) ([]byte, string, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			doc := `{"version":1,"security_policy":` + f.policy
			if f.variables != "" {
				doc += `,"variables":` + f.variables
			}
			doc += `}`
			return []byte(doc), digest(doc), f.policy != ""
		},
		Admit: func(string, string) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return !f.full
		},
		Stop: func(string) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			stop := f.stopPing || f.stopRun && len(f.events) > 0
			if stop && f.onStop != nil {
				f.onStop()
			}
			return stop
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if r.URL.Path == "/v1/run-configuration" {
			f.queries = append(f.queries, r.URL.RawQuery)
		}
		f.instances = append(f.instances, [2]string{r.Header.Get("X-Qory-Instance-Id"), r.Header.Get("X-Qory-Instance-Name")})
		unavailable := f.unavailable && r.URL.Path == "/v1/events"
		f.mu.Unlock()
		if unavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		h.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized {
			f.mu.Lock()
			f.refused++
			f.mu.Unlock()
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// newKey is a new Ed25519 key, for an access key or a server's signing key.
func newKey(t *testing.T) *accesskey.Key {
	t.Helper()
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// statusRecorder remembers the status an answer was written with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Seen reports whether the server stored an event id before.
func (f *fakeServer) Seen(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev["id"] == id {
			return true
		}
	}
	return false
}

// Append keeps one event.
func (f *fakeServer) Append(_ string, line []byte) error {
	var ev map[string]any
	if err := json.Unmarshal(line, &ev); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

// digest is the server's digest of a document, as the contract's headers carry it.
func digest(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return "sha256=" + hex.EncodeToString(sum[:])
}

// byType is the events the server accepted, by type.
func (f *fakeServer) byType() map[string][]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]map[string]any{}
	for _, ev := range f.events {
		typ, _ := ev["type"].(string)
		data, _ := ev["data"].(map[string]any)
		out[typ] = append(out[typ], data)
	}
	return out
}

// pinLine is forager.yaml's apiary_public_key for a server's signing key.
func pinLine(k *accesskey.Key) string {
	return "[{alg: ed25519, public_key: " + k.PublicKey().String() + "}]"
}

// serverFile writes forager.yaml with the fake server as gateway.server, its access key
// id and its pin, and what more the test wants after it, and the machine's access key
// secret beside it, the directory mode 0700. more continues the gateway section when it
// is indented by two spaces, and starts a section of its own when it is not.
func serverFile(t *testing.T, srv *fakeServer, more string) {
	t.Helper()
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  server:\n    url: "+srv.URL+"\n    access_key_id: "+testAccessKey+"\n    apiary_public_key: "+pinLine(srv.signer)+"\n"+more)
	writeSecret(t, srv.key)
}

// writeSecret writes the machine's access-key-secret, replacing one there, in the
// forager.yaml's directory made mode 0700.
func writeSecret(t *testing.T, k *accesskey.Key) {
	t.Helper()
	dir := foragerdir.Dir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory"))
	if _, err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir.Path(foragerdir.SecretFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := dir.WriteSecret(k); err != nil {
		t.Fatal(err)
	}
}

// fakeRuntime writes a program that stands in for a runtime: it prints its run id and
// its arguments, checks that the proxy and the run's socket are in its environment, and
// exits with the status QORY_TEST_EXIT names, 3 when unset.
func fakeRuntime(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, `#!/bin/sh
echo "hello from $QORY_RUN_ID with $*"
echo "harness $QORY_HARNESS_HOME A=$A"
test -n "$HTTP_PROXY" || exit 9
test -n "$QORY_RUN_SOCKET" || exit 8
exit ${QORY_TEST_EXIT:-3}
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// composedForFake composes the two-modules fixture for claude with the fake runtime as
// claude's program, through harness.launch in the user's file, keeping --settings so
// Forager installs its hooks into a copy of the composed settings.
func composedForFake(t *testing.T, root, script string) {
	t.Helper()
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: `+script+`
      args:
        - [--settings, "${dir}/settings.json"]
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// events reads the events of the one run recorded under the checkout and returns them
// by type, with the run directory.
func events(t *testing.T, root string) (string, map[string][]map[string]any) {
	t.Helper()
	runs, err := os.ReadDir(runsDir(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs recorded, want 1", len(runs))
	}
	dir := filepath.Join(runsDir(t, root), runs[0].Name())
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		byType[ev.Type] = append(byType[ev.Type], ev.Data)
	}
	return dir, byType
}

// TestRunRecordsTheSession runs the composed runtime through Forager: the launch
// spec is the template with the configuration over it plus the arguments after --, the
// runtime sees the proxy, the socket and the launch's variables, the policy in the configuration directory is
// applied, the hooks are installed into a copy of the composed settings, the record is
// written in the checkout's folder under the state directory, the last line names it,
// and the exit status is the runtime's, reported once.
func TestRunRecordsTheSession(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := fakeRuntime(t)
	composedForFake(t, root, script)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  egress:\n    mode: enforce\n    allow: [example.com]\n")
	out, err := run(t, "run", "claude", "--", "--extra", "one")
	if err == nil || cmd.ExitCode(err) != 3 || !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	// The launch's variables reach the runtime: the home, and the fragment's A, which the
	// settings do not hold.
	wants(t, out, "harness "+filepath.Join(root, ".qory", "harness")+" A=core")
	dir, evs := events(t, root)
	wants(t, out, "hello from ", " with --settings ", " --extra one", "claude exited 3\n")
	if !strings.HasSuffix(out, "\nqory run: the record is in "+dir+"\n") {
		t.Errorf("the last line does not name the record %s:\n%s", dir, out)
	}
	started := evs["dev.qory.run.started"]
	if len(started) != 1 || started[0]["command"] != script || started[0]["interactive"] != false {
		t.Errorf("run.started %v", started)
	}
	if labels, _ := started[0]["labels"].(map[string]any); len(labels) != 2 || labels["forge"] != "git.example.com" || labels["repository"] != "acme/app" {
		t.Errorf("the labels from the origin remote: %v", started[0]["labels"])
	}
	applied := evs["dev.qory.run.policy_applied"]
	if len(applied) != 1 || applied[0]["mode"] != "enforce" || applied[0]["source"] != "config" {
		t.Errorf("run.policy_applied %v", applied)
	}
	exited := evs["dev.qory.run.exited"]
	if len(exited) != 1 || exited[0]["exit_code"] != float64(3) || exited[0]["state"] != "failed" {
		t.Errorf("run.exited %v", exited)
	}
	if len(evs["dev.qory.ping"]) != 0 {
		t.Error("a ping was sent with no server configured")
	}
	log, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil || !strings.Contains(string(log), "hello from ") {
		t.Errorf("output.log: %v\n%s", err, log)
	}
	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	wants(t, string(settings), exe+" run forward", "SessionEnd")
	lacks(t, string(settings), "QORY_HARNESS_HOME", `"env"`)
	if !strings.Contains(string(settings), "\"timeout\"") {
		t.Error("the hook has no timeout")
	}
}

// TestRunExitsZeroQuietly is a runtime that exited 0: no error, a success line naming
// the record, the last line naming it in full, and no policy means observe. The state
// directory, its runs directory and the checkout's folder are this user's alone, mode
// 0700, made so where they were not.
func TestRunExitsZeroQuietly(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	state := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory")
	if err := os.MkdirAll(filepath.Join(state, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	out, err := run(t, "run", "--run-id", id)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	record := filepath.Join(runsDir(t, root), id)
	wants(t, out, "claude exited 0\n")
	if strings.Count(out, "recorded in") != 0 {
		t.Errorf("the record is named twice:\n%s", out)
	}
	if !strings.HasSuffix(out, "\nqory run: the record is in "+record+"\n") {
		t.Errorf("the last line does not name the record %s:\n%s", record, out)
	}
	for _, d := range []string{state, filepath.Join(state, "runs"), runsDir(t, root)} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v", d, info.Mode(), err)
		}
	}
	_, evs := events(t, root)
	if applied := evs["dev.qory.run.policy_applied"]; len(applied) != 1 || applied[0]["mode"] != "observe" || applied[0]["source"] != "none" {
		t.Errorf("run.policy_applied %v", applied)
	}
}

// TestRunRefusesWhatItCannotStart is the runs that never start: a runtime the harness
// is not composed for, two runtimes named, a forager.yaml that does not read, and no
// composed harness at all; none of them leaves a record.
func TestRunRefusesWhatItCannotStart(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), "run qory harness compose") {
		t.Errorf("nothing composed: %v", err)
	}
	composedForFake(t, root, fakeRuntime(t))
	if _, err := run(t, "run", "codex"); cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("not composed for codex: %v", err)
	}
	if _, err := run(t, "run", "claude", "codex"); cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("two runtimes: %v", err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  egress:\n    mode: log\n")
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), `forager.yaml: gateway.egress.mode is not observe or enforce`) {
		t.Errorf("unreadable forager.yaml: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a run that did not start left a record")
	}
}

// TestRunExpandsTheIntegrationsTheMachineDeclares declares integrations found on the
// PATH. qory config describes every one and lists it and the credential it defines. A
// run under a policy of its own describes the ones the policy selects and names the
// program it found: one that does not describe stops the run before it starts, with the
// program's line, and leaves no record, and one the policy does not select is not
// described. With a server, which supplies the policy, every one is described. A name
// the credentials section defines as well is named on a line of its own, and a run does
// not describe that integration, which config still does.
func TestRunExpandsTheIntegrationsTheMachineDeclares(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(bin, "qory-tracker"), "#!/bin/sh\ntest \"$1\" = describe || exit 64\necho '{\"version\": 1, \"name\": \"tracker\", \"title\": \"Tracker\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Z]+\", \"hosts\": [\"tracker.acme.example\"]}}}'\n")
	writeFile(t, filepath.Join(bin, "qory-broken"), "#!/bin/sh\necho 'the settings file is missing' >&2\nexit 1\n")
	for _, p := range []string{"qory-tracker", "qory-broken"} {
		if err := os.Chmod(filepath.Join(bin, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tracker, err := filepath.EvalSymlinks(filepath.Join(bin, "qory-tracker"))
	if err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(filepath.Dir(tracker), "qory-broken")
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  integrations:\n    tracker: {settings: {project: SHOP}}\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "gateway.credentials.tracker", "integration tracker", "gateway.integrations.tracker", tracker+" 0.3.0")
	policy := func(name string) string {
		path := filepath.Join(t.TempDir(), "policy.yaml")
		writeFile(t, path, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: "+name+", argument: SHOP}\n")
		return path
	}
	t.Setenv("QORY_TEST_EXIT", "0")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  integrations:\n    tracker: {settings: {project: SHOP}}\n    broken: {}\n")
	out, err = run(t, "run")
	if err != nil {
		t.Fatalf("a run that selects no integration: %v\n%s", err, out)
	}
	lacks(t, out, "integration tracker", "integration broken")
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	// A policy that selects a credential needs a wall, which Forager asks for once
	// the integration it selects is described.
	out, err = run(t, "run", "--policy", policy("tracker"))
	wants(t, out, "qory run: integration tracker: "+tracker+" 0.3.0\n")
	lacks(t, out, "integration broken")
	if err == nil || !strings.Contains(err.Error(), "need a wall") {
		t.Errorf("a run that selects tracker: %v", err)
	}
	// Forager stopped the run once it had started its record: qory prints the error,
	// then the line that names the record.
	if ids := recorded(t, root); len(ids) != 1 || !strings.HasSuffix(out, "need a wall: without one a program that ignores the proxy is bound by none of them\nqory run: the record is in "+filepath.Join(runsDir(t, root), ids[0])+"\n") || !errors.Is(err, cmd.ErrReported) {
		t.Errorf("a run stopped after its record started: %v, %v\n%s", ids, err, out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	want := "forager.yaml: gateway.integrations.broken: " + broken + " describe: exit status 1: the settings file is missing"
	if _, err := run(t, "run", "--policy", policy("broken")); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), want) {
		t.Errorf("a run that selects broken: %v", err)
	}
	if _, err := run(t, "config"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), want) {
		t.Errorf("config with an integration that does not describe: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a run that did not start left a record")
	}
	srv := newFakeServer(t, "")
	serverFile(t, srv, "  integrations:\n    broken: {}\n")
	if _, err := run(t, "run"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), want) {
		t.Errorf("a run whose policy the server supplies: %v", err)
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  credentials:\n    tracker:\n      env: TRACKER_TOKEN\n      hosts: [tracker.acme.example]\n      auth: {scheme: bearer}\n  integrations:\n    tracker: {settings: {project: SHOP}}\n")
	line := "forager.yaml: gateway.credentials.tracker defines the credential tracker, and gateway.integrations.tracker defines none\n"
	out, err = run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "qory config: "+line, tracker+" 0.3.0, shadowed by gateway.credentials.tracker")
	out, err = run(t, "run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "qory run: "+line)
	// A run describes every integration when the server supplies the policy, but one
	// the credentials section shadows defines nothing for it and is not described.
	serverFile(t, srv, "  credentials:\n    broken:\n      env: BROKEN_TOKEN\n      hosts: [broken.acme.example]\n      auth: {scheme: bearer}\n  integrations:\n    broken: {}\n")
	out, err = run(t, "run")
	if err != nil {
		t.Fatalf("a run with a shadowed integration that does not describe: %v\n%s", err, out)
	}
	wants(t, out, "qory run: forager.yaml: gateway.credentials.broken defines the credential broken, and gateway.integrations.broken defines none\n")
	if _, err := run(t, "config"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), want) {
		t.Errorf("config describes a shadowed integration: %v", err)
	}
}

// TestConfigRefusesAProgramInTheCheckoutAlone runs qory config from a directory that is
// no git working tree, above the program forager.yaml names: the program is
// described. From a checkout that holds the program, it is refused.
func TestConfigRefusesAProgramInTheCheckoutAlone(t *testing.T) {
	dir := emptyDir(t)
	program := filepath.Join(dir, "tools", "acme-tracker")
	writeFile(t, program, "#!/bin/sh\necho '{\"version\": 1, \"name\": \"tracker\", \"title\": \"Tracker\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Z]+\", \"hosts\": [\"tracker.acme.example\"]}}}'\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  integrations:\n    tracker: {program: "+program+"}\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("config outside a checkout: %v\n%s", err, out)
	}
	wants(t, out, program+" 0.3.0")
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	if _, err := run(t, "config"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "inside "+dir+", which a run may write") {
		t.Errorf("config in the checkout that holds the program: %v", err)
	}
}

// TestForwardHandsAHookToTheRun is the hidden forward verb: it sends its stdin to the
// socket the environment names as one hooks record, prints nothing and exits 0; with
// no socket in the environment it still exits 0 and says why on stderr.
func TestForwardHandsAHookToTheRun(t *testing.T) {
	emptyDir(t)
	dir, err := os.MkdirTemp("", "qory-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got <- b.String()
	}()
	t.Setenv("QORY_RUN_SOCKET", path)
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs([]string{"run", "forward"})
	root.SetIn(strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1"}`))
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("forward printed %q", out.String())
	}
	line := <-got
	wants(t, line, `"source":"hooks"`, `"hook_event_name":"Stop"`)
	if c, _, _ := root.Find([]string{"run", "forward"}); !c.Hidden {
		t.Error("forward is in the help")
	}
	t.Setenv("QORY_RUN_SOCKET", "")
	root = cmd.Root()
	root.SetArgs([]string{"run", "forward"})
	root.SetIn(strings.NewReader(`{}`))
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	wants(t, out.String(), "QORY_RUN_SOCKET")
}

// staticELF writes the smallest file the wall takes for a static Linux executable: an
// ELF header with no program headers. Nothing in these tests executes it.
func staticELF(t *testing.T) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	h[16], h[18], h[20] = 2, 0x3e, 1 // an executable, x86-64, version 1
	h[52], h[54], h[58] = 64, 56, 64 // the sizes of the header, a program header, a section header
	path := filepath.Join(t.TempDir(), "qory-linux")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeDocker writes a program that stands in for docker: it logs every command line,
// answers the two the wall reads, and for run logs the environment file, prints a line
// and exits 4, as a container's runtime would.
func fakeDocker(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	script := filepath.Join(dir, "docker")
	writeFile(t, script, `#!/bin/sh
echo "$*" >> `+log+`
case "$1 $2" in
"network inspect") echo "172.30.0.1 " ;;
"logs "*) echo "relay: listening" ;;
"run "*)
	while [ $# -gt 0 ]; do
		if [ "$1" = --env-file ]; then sed 's/^/env: /' "$2" >> `+log+`; fi
		shift
	done
	echo "inside the container"
	exit 4 ;;
esac
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// TestRunRefusesAProgramUnderAReadWriteMount declares an integration whose program is
// in a directory the wall mounts: mounted read-write, --mount or wall.mounts, the
// container could rewrite it, and run and config refuse it; mounted read-only, the run
// describes it and names it.
func TestRunRefusesAProgramUnderAReadWriteMount(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	tools := t.TempDir()
	program := filepath.Join(tools, "bin", "acme-tracker")
	writeFile(t, program, "#!/bin/sh\ntest \"$1\" = describe || exit 64\necho '{\"version\": 1, \"name\": \"tracker\", \"title\": \"Tracker\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Z]+\", \"hosts\": [\"tracker.acme.example\"]}}}'\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedProgram, err := filepath.EvalSymlinks(program)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + helper + "\n"
	integrations := "gateway:\n  integrations:\n    tracker: {program: " + program + "}\n"
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+wallSection+integrations)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: tracker, argument: SHOP}\n")
	refused := "inside " + tools + ", which a run may write"
	if _, err := run(t, "run", "claude", "--policy", policy, "--mount", tools); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), refused) {
		t.Errorf("a program under a read-write --mount: %v", err)
	}
	out, err := run(t, "run", "claude", "--policy", policy, "--mount", tools+":ro")
	wants(t, out, "qory run: integration tracker: "+resolvedProgram+" 0.3.0\n")
	if err != nil && strings.Contains(err.Error(), refused) {
		t.Errorf("a program under a read-only --mount: %v", err)
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+wallSection+"  mounts: ["+tools+"]\n"+integrations)
	if _, err := run(t, "config"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), refused) {
		t.Errorf("config with the program under a read-write wall.mounts: %v", err)
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+wallSection+"  mounts: [\""+tools+":ro\"]\n"+integrations)
	if out, err := run(t, "config"); err != nil {
		t.Errorf("config with the program under a read-only wall.mounts: %v\n%s", err, out)
	}
}

// TestRunBehindAWall runs the composed runtime behind the Docker wall with a program
// standing in for docker: the wall section and the flags choose the wall and the image,
// the container is shown the checkout and nothing of this machine's environment but
// the variables named, the relay and the forwarder are qory's Linux build inside, the
// record, in the state directory, names the wall, and the exit status is the
// container's. The section names the container's user, because a machine that runs
// the tests as root has none to default to.
func TestRunBehindAWall(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	foragerFile := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, foragerFile, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+helper+"\n  env: [MODEL_KEY, NOT_SET_HERE]\n  user: \"1000:1000\"\n  cpus: \"2\"\n  memory: 4g\n  pids_limit: 4096\n")
	t.Setenv("MODEL_KEY", "not-a-real-key")
	t.Setenv("HOST_ONLY", "stays outside")
	t.Setenv("FLAG_NAMED", "goes in")

	sibling := t.TempDir()
	out, err := run(t, "run", "claude", "--image", "example.com/agent:2", "--env", "FLAG_NAMED", "--mount", sibling+":ro", "--memory", "8g", "--shm-size", "2g", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "inside the container", "claude exited 4")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := string(data)
	wants(t, lines,
		"network create --internal",
		"--user 1000:1000 --cap-drop ALL",
		"--mount type=bind,src="+sibling+",dst="+sibling+",readonly",
		"--cpus 2 --memory 8g --shm-size 2g --pids-limit 4096",
		"--entrypoint /qory/qory example.com/agent:2 run relay 3128=",
		"src="+helper+",dst=/qory/qory,readonly",
		"--mount type=bind,src="+root+",dst="+root+" ",
		"--entrypoint claude example.com/agent:2 --settings "+runsDir(t, root),
		" -p hi",
		"env: MODEL_KEY=not-a-real-key", "env: FLAG_NAMED=goes in", "env: HTTPS_PROXY=http://qory-proxy:3128",
		"network rm",
	)
	lacks(t, lines, "HOST_ONLY", "NOT_SET_HERE", "env: PATH=", "env: HOME=", ".qory/runs")
	// The record is bound read-only from the state directory, outside the checkout.
	wants(t, lines, "--mount type=bind,src="+runsDir(t, root)+string(filepath.Separator))
	if strings.Count(lines, " --mount type=bind,src="+root+",") != 1 {
		t.Error("the checkout is mounted more than once")
	}
	if strings.Contains(strings.ReplaceAll(lines, "env: MODEL_KEY=not-a-real-key", ""), "not-a-real-key") {
		t.Error("a value of the environment is on a command line")
	}
	dir, evs := events(t, root)
	if started := evs["dev.qory.run.started"]; len(started) != 1 || started[0]["wall"] != "docker" || started[0]["image"] != "example.com/agent:2" {
		t.Errorf("run.started %v", started)
	}
	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(settings), "/qory/qory run forward")

	// --wall none runs without the section's wall, as this machine's process.
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, fakeRuntime(t))
	if out, err := run(t, "run", "claude", "--wall", "none"); cmd.ExitCode(err) != 3 {
		t.Fatalf("--wall none: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if _, evs := events(t, root); evs["dev.qory.run.started"][0]["wall"] != nil {
		t.Errorf("--wall none still walled: %v", evs["dev.qory.run.started"])
	}
}

// TestTheServersVariableWinsOverALaunchDefault is a walled run whose server's run
// configuration sets a variable of the name harness.launch sets too, a default its
// author wrote: the agent gets the server's value, the launch's is left out and reported
// as overridden in dev.qory.run.policy_applied, and a server variable of another name
// reaches the agent; the fragment's A and B are the harness's own defaults. A value of
// --env the server's overrides is left out too, and qory says which host set the name.
func TestTheServersVariableWinsOverALaunchDefault(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: claude
      args:
        - [--settings, "${dir}/settings.json"]
      env: {SHARED_NAME: from-launch}
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	docker, log := fakeDocker(t)
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe"}}`)
	srv.variables = `{"SHARED_NAME":{"value":"from-server"},"SERVER_ONLY":{"value":"from-server"},"LOG_LEVEL":{"value":"debug"}}`
	t.Setenv("LOG_LEVEL", "info")
	serverFile(t, srv, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	out, err := run(t, "run", "claude", "--env", "LOG_LEVEL")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	wants(t, out, "qory run: LOG_LEVEL from --env is not used: "+host+" sets it\n")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "env: SHARED_NAME=from-server\n", "env: SERVER_ONLY=from-server\n", "env: LOG_LEVEL=debug\n")
	lacks(t, string(data), "SHARED_NAME=from-launch")
	_, evs := events(t, root)
	for _, applied := range [][]map[string]any{evs["dev.qory.run.policy_applied"], srv.byType()["dev.qory.run.policy_applied"]} {
		if len(applied) != 1 {
			t.Fatalf("run.policy_applied %v", applied)
		}
		got, _ := json.Marshal(applied[0]["variables"])
		if want := `[{"from":"harness","lost":[],"name":"A"},{"from":"harness","lost":[],"name":"B"},{"from":"apiary","lost":[{"from":"run","why":"overridden"}],"name":"LOG_LEVEL"},{"from":"apiary","lost":[],"name":"SERVER_ONLY"},{"from":"apiary","lost":[{"from":"harness","why":"overridden"}],"name":"SHARED_NAME"}]`; string(got) != want {
			t.Errorf("run.policy_applied variables %s, want %s", got, want)
		}
	}
}

// TestRunRefusesAMountOfAnIntegrationsSettingFile is a walled run whose policy selects
// an integration with a <name>_file setting: a mount that is or contains that file is
// refused by Forager before anything starts, as one that holds one of Forager's
// files, in either mode, and so is one that holds a link on the way to it, named as the
// link. A refused run starts no container.
func TestRunRefusesAMountOfAnIntegrationsSettingFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	program := filepath.Join(tempDir(t), "bin", "acme-tracker")
	writeFile(t, program, "#!/bin/sh\ntest \"$1\" = describe || exit 64\necho '{\"version\": 1, \"name\": \"tracker\", \"title\": \"Tracker\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Z]+\", \"hosts\": [\"tracker.acme.example\"]}}}'\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	keys := tempDir(t)
	token := filepath.Join(keys, "tracker-token")
	writeFile(t, token, "not read\n")
	// The setting's path goes through a link in a directory of its own.
	links := tempDir(t)
	link := filepath.Join(links, "tracker-token")
	if err := os.Symlink(token, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	policy := filepath.Join(tempDir(t), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: tracker, argument: SHOP}\n")
	for _, c := range []struct{ setting, mount, want string }{
		{token, keys, "the mount " + keys + " contains " + token + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{token, keys + ":ro", "the mount " + keys + " contains " + token + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{token, token + ":ro", "the mount " + token + " is " + token + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{link, links, "the mount " + links + " contains " + link + ", which leads to one of Forager's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{link, keys + ":ro", "the mount " + keys + " contains " + token + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
	} {
		writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+wallSection+"gateway:\n  integrations:\n    tracker: {program: "+program+", settings: {project: SHOP, token_file: "+c.setting+"}}\n")
		out, err := run(t, "run", "claude", "--policy", policy, "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("token_file %s, --mount %s: %v (exit %d), want %q\n%s", c.setting, c.mount, err, cmd.ExitCode(err), c.want, out)
		}
		lacks(t, out, "the record is in")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left a record: %v", ids)
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunRefusesAMountOfForagersFiles is a walled run with a mount of the home,
// which contains qory's configuration directory, and one of that directory itself:
// Forager refuses both before anything starts, and qory says the agent could read the
// access key, with how the mount and the directory stand to each other. Without
// access-key-secret in the directory, it says the agent could change one of
// Forager's files, or read it through a read-only mount. A mount of a directory Forager keeps its own files in, the tools'
// sockets, says so, in either mode, and one of qory's state directory says the agent could change the
// run records, or read them through a read-only mount, as does the workspace when the
// state directory lies in the checkout. A run refused so has no record, and no line
// names one.
func TestRunRefusesAMountOfForagersFiles(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory")
	writeFile(t, filepath.Join(configDir, "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	home := os.Getenv("HOME")
	// The tools' sockets are made in the system's temporary directory: one apart from
	// the home, so a mount of it holds Forager's files and not the access key.
	tmp := tempDir(t)
	t.Setenv("TMPDIR", tmp)
	secret := filepath.Join(configDir, "access-key-secret")
	stateHome := os.Getenv("XDG_STATE_HOME")
	for _, c := range []struct {
		secret              bool
		mount, want, ending string
	}{
		{false, home, "the mount " + home + " contains " + configDir + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
		{false, home + ":ro", "the mount " + home + " contains " + configDir + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
		{true, home, "the mount " + home + " contains " + configDir + ", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
		{true, configDir + ":ro", "the mount " + configDir + " is " + configDir + ", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
		{true, tmp, "the mount " + tmp + " contains " + filepath.Join(tmp, "qory-tool-"), ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{true, tmp + ":ro", "the mount " + tmp + " contains " + filepath.Join(tmp, "qory-tool-"), ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{true, stateHome, "the mount " + stateHome + " contains " + filepath.Join(stateHome, "qory") + ", which holds qory's run records; the agent could change them, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
		{true, filepath.Join(stateHome, "qory", "runs") + ":ro", "the mount " + filepath.Join(stateHome, "qory", "runs") + " lies inside " + filepath.Join(stateHome, "qory") + ", which holds qory's run records; the agent could read them, so the run does not start. Mount a narrower path (mount_contains_forager_files)", ""},
	} {
		if c.secret {
			writeFile(t, secret, "not read\n")
		}
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || !strings.HasPrefix(err.Error(), c.want) || !strings.HasSuffix(err.Error(), c.ending) || c.ending == "" && err.Error() != c.want {
			t.Errorf("--mount %s: %v (exit %d), want %q ... %q\n%s", c.mount, err, cmd.ExitCode(err), c.want, c.ending, out)
		}
		lacks(t, out, "inside the container", "the record is in")
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	want := "the workspace " + root + " contains " + filepath.Join(root, "state", "qory") + ", which holds qory's run records; the agent could change them, so the run does not start. Mount a narrower path (mount_contains_forager_files)"
	if out, err := run(t, "run", "claude"); err == nil || err.Error() != want {
		t.Errorf("the state directory in the checkout: %v, want %q\n%s", err, want, out)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left a record: %v", ids)
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunRefusesAMountOfALinkOnTheWayToTheRecords is a walled run whose state home is
// a link, and one whose state home lies under a link: a mount of the directory that
// holds the link is refused before anything starts, since the agent could point it at
// a directory of its own, and qory names the link as leading to the run records, which
// a writable mount could point elsewhere.
func TestRunRefusesAMountOfALinkOnTheWayToTheRecords(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	links, real := tempDir(t), tempDir(t)
	home := filepath.Join(links, "state")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	above, realAbove := tempDir(t), tempDir(t)
	user := filepath.Join(above, "user")
	if err := os.Symlink(realAbove, user); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ state, mount, link, what string }{
		{home, links, home, "; the agent could point it elsewhere"},
		{home, links + ":ro", home, ""},
		{filepath.Join(user, ".local", "state"), above, user, "; the agent could point it elsewhere"},
	} {
		t.Setenv("XDG_STATE_HOME", c.state)
		mount, _, _ := strings.Cut(c.mount, ":")
		want := "the mount " + mount + " contains " + c.link + ", which leads to qory's run records" + c.what + ", so the run does not start. Mount a narrower path (mount_contains_forager_files)"
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != want {
			t.Errorf("XDG_STATE_HOME=%s, --mount %s: %v (exit %d), want %q\n%s", c.state, c.mount, err, cmd.ExitCode(err), want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunRefusesAMountOfAnotherModeInside is a walled run with a read-only mount inside
// the checkout, which the container sees writable, and one with a read-only mount
// reached through a link inside the checkout that leads out of it, which the agent could
// repoint: Forager refuses each before anything starts, and qory gives both modes, or
// names the link. A mount reached through a link that leads back into the checkout is
// reached through the checkout, and that run starts.
func TestRunRefusesAMountOfAnotherModeInside(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	vendor := filepath.Join(root, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := tempDir(t)
	if err := os.MkdirAll(filepath.Join(elsewhere, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "third_party")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(link, "lib")
	for _, c := range []struct{ mount, want string }{
		{vendor, "the mount " + vendor + " (read-only) lies inside " + root + ", which is writable: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave " + vendor + " out (mount_mode_conflict)"},
		{lib, "the mount " + lib + " is reached through the link " + link + " inside the workspace " + root + ", which a walled agent can change, so the run does not start. List the link's target itself (mount_through_link)"},
	} {
		out, err := run(t, "run", "claude", "--mount", c.mount+":ro")
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("--mount %s:ro: %v (exit %d), want %q\n%s", c.mount, err, cmd.ExitCode(err), c.want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "real", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "inlink")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inlink", "x")
	out, err := run(t, "run", "claude", "--mount", inside)
	if cmd.ExitCode(err) != 4 || !strings.Contains(out, "inside the container") {
		t.Errorf("--mount %s, through a link that leads into the checkout: %v (exit %d)\n%s", inside, err, cmd.ExitCode(err), out)
	}
}

// TestRunRefusesAMountOfWhereAConfigLinkLeads is a walled run whose forager.yaml is a
// link to a file in another directory, and a descriptor in runtimes/ a link to one whose
// target does not exist yet: a mount of either directory is refused before anything
// starts, since the agent could change what the next run reads, and qory says it holds
// one of Forager's files.
func TestRunRefusesAMountOfWhereAConfigLinkLeads(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory")
	dotfiles := tempDir(t)
	target := filepath.Join(dotfiles, "qory", "forager.yaml")
	writeFile(t, target, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	if err := os.MkdirAll(filepath.Join(configDir, "runtimes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configDir, "forager.yaml")); err != nil {
		t.Fatal(err)
	}
	later := tempDir(t)
	if err := os.Symlink(filepath.Join(later, "goose.yaml"), filepath.Join(configDir, "runtimes", "goose.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mount, want string }{
		{dotfiles, "the mount " + dotfiles + " contains " + target + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{later, "the mount " + later + " contains " + filepath.Join(configDir, "runtimes", "goose.yaml") + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
	} {
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", c.mount, err, cmd.ExitCode(err), c.want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunRefusesAMountOfALinkOnTheWayToAConfigFile is a walled run whose forager.yaml is
// a link into a directory that is itself a link, and a descriptor in runtimes/ the first
// of two links: a mount of the directory that holds either link on the way is refused
// before anything starts, as one of where they lead is, since the agent could point the
// link elsewhere, and qory names the link as leading to one of Forager's files, which
// a read-only mount could not point elsewhere.
func TestRunRefusesAMountOfALinkOnTheWayToAConfigFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory")
	real, linked := tempDir(t), tempDir(t)
	writeFile(t, filepath.Join(real, "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	if err := os.Symlink(real, filepath.Join(linked, "dotfiles")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "runtimes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(linked, "dotfiles", "forager.yaml"), filepath.Join(configDir, "forager.yaml")); err != nil {
		t.Fatal(err)
	}
	hop, last := tempDir(t), tempDir(t)
	writeFile(t, filepath.Join(last, "goose.yaml"), "name: goose\n")
	if err := os.Symlink(filepath.Join(last, "goose.yaml"), filepath.Join(hop, "goose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(hop, "goose.yaml"), filepath.Join(configDir, "runtimes", "goose.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mount, path, what string }{
		{hop, filepath.Join(hop, "goose.yaml"), "leads to one of Forager's files; the agent could point it elsewhere"},
		{hop + ":ro", filepath.Join(hop, "goose.yaml"), "leads to one of Forager's files"},
		{last, filepath.Join(last, "goose.yaml"), "holds one of Forager's files; the agent could change it"},
		{linked, filepath.Join(linked, "dotfiles"), "leads to one of Forager's files; the agent could point it elsewhere"},
		{real, filepath.Join(real, "forager.yaml"), "holds one of Forager's files; the agent could change it"},
	} {
		mount, _, _ := strings.Cut(c.mount, ":")
		want := "the mount " + mount + " contains " + c.path + ", which " + c.what + ", so the run does not start. Mount a narrower path (mount_contains_forager_files)"
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != want {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", c.mount, err, cmd.ExitCode(err), want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunRefusesAMountOfALinkToTheConfigDir is a walled run whose qory configuration
// directory is a link to one elsewhere: a mount of the directory that holds the link is
// refused before anything starts, since the agent could point it at a directory of its
// own, and qory names the link as leading to one of Forager's files, writable or
// read-only, not as holding the access key; a mount of the directory where it leads is
// refused as one that holds the key.
func TestRunRefusesAMountOfALinkToTheConfigDir(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	configHome := os.Getenv("XDG_CONFIG_HOME")
	configDir := filepath.Join(configHome, "qory")
	elsewhere := tempDir(t)
	real := filepath.Join(elsewhere, "qory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(configDir, real); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(real, "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	writeFile(t, filepath.Join(real, "access-key-secret"), "not read\n")
	if err := os.Symlink(real, configDir); err != nil {
		t.Fatal(err)
	}
	// qory names the link where it is, past the links above it.
	physical, err := filepath.EvalSymlinks(configHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mount, want string }{
		{configHome, "the mount " + configHome + " contains " + filepath.Join(physical, "qory") + ", which leads to one of Forager's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{configHome + ":ro", "the mount " + configHome + " contains " + filepath.Join(physical, "qory") + ", which leads to one of Forager's files, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{elsewhere, "the mount " + elsewhere + " contains " + configDir + ", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
	} {
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", c.mount, err, cmd.ExitCode(err), c.want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), " run ") {
			t.Errorf("a container was started:\n%s", data)
		}
	}
}

// TestRunTakesEnvWithoutAWall is --env on a run without a wall: a value no other
// source sets reaches the agent and is recorded as the run's, a value of a name a module
// exports wins over the export, a default, and qory says that a value of a name no source
// may set, PATH, is not used.
func TestRunTakesEnvWithoutAWall(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  CORE_SCRIPTS: scripts\n")
	writeFile(t, filepath.Join(root, "modules", "core", "scripts", "run.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n")
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\necho \"FLAG_NAMED=$FLAG_NAMED CORE_SCRIPTS=$CORE_SCRIPTS\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	t.Setenv("FLAG_NAMED", "goes in")
	t.Setenv("CORE_SCRIPTS", "/elsewhere")
	out, err := run(t, "run", "claude", "--env", "FLAG_NAMED", "--env", "CORE_SCRIPTS", "--env", "PATH")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out,
		"qory run: PATH from --env is not used: no source may set it\n",
		"FLAG_NAMED=goes in CORE_SCRIPTS=/elsewhere\n")
	lacks(t, out, "FLAG_NAMED from --env", "CORE_SCRIPTS from --env")
	_, evs := events(t, root)
	got, _ := json.Marshal(evs["dev.qory.run.policy_applied"][0]["variables"])
	if want := `[{"from":"run","lost":[{"from":"harness","why":"overridden"},{"from":"shell","why":"overridden"}],"name":"CORE_SCRIPTS"},{"from":"run","lost":[{"from":"shell","why":"overridden"}],"name":"FLAG_NAMED"},{"from":"shell","lost":[{"from":"run","why":"denied"}],"name":"PATH"}]`; string(got) != want {
		t.Errorf("run.policy_applied variables %s, want %s", got, want)
	}
}

// TestRunRefusesAWallItCannotBuild pins the refusals before anything starts: a wall
// that is not one, no image, a flag that means nothing without a wall.
func TestRunRefusesAWallItCannotBuild(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--wall", "bubblewrap"}, "the walls are docker"},
		{[]string{"run", "--wall", "docker"}, "needs the container's image"},
		{[]string{"run", "--image", "i"}, "behind a wall"},
		{[]string{"run", "--mount", "/srv"}, "behind a wall"},
		{[]string{"run", "--shm-size", "2g"}, "behind a wall"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--mount", "srv"}, "not an absolute path"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--env", "QORY_ACCESS_KEY_SECRET"}, "--env QORY_ACCESS_KEY_SECRET: the variable is Forager's own"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--env", "QORY_ACCESS_KEY_ID"}, "Forager's own"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--env", "QORY_APIARY_PUBLIC_KEY"}, "Forager's own"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--env", "QORY_SERVER_SECRET"}, "--env QORY_SERVER_SECRET: the variable is Forager's own and never the agent's"},
		{[]string{"run", "--env", "QORY_RUN_CREDENTIAL_SECRET"}, "--env QORY_RUN_CREDENTIAL_SECRET: the variable is Forager's own and never the agent's"},
		{[]string{"run", "--label", "issue"}, "not key=value"},
		{[]string{"run", "--label", "Issue=1"}, "label key"},
		{[]string{"run", "--run-id", "../x"}, "not a UUID"},
		{[]string{"run", "--policy", filepath.Join(root, "policy.yaml")}, "inside the checkout"},
	} {
		out, err := run(t, c.args...)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (exit %d), want %q and exit %d\n%s", c.args, err, cmd.ExitCode(err), c.want, cmd.ExitInput, out)
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
}

// TestRunBehindARealWall is the run behind Docker itself, for a machine that has an
// engine: QORY_WALL_HELPER names a static Linux build of qory, and without it the test
// is skipped. A shell stands in for the runtime: what it fetches through the proxy
// arrives and is recorded, what it dials around the proxy goes nowhere, and qory's own
// build runs inside as the forwarder.
func TestRunBehindARealWall(t *testing.T) {
	helper := os.Getenv("QORY_WALL_HELPER")
	if helper == "" {
		t.Skip("QORY_WALL_HELPER names no Linux build of qory")
	}
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		t.Skipf("docker reaches no engine: %s", out)
	}
	// The origin is a container of its own: behind a wall the proxy never dials this
	// machine, so a listener here is what the run must not reach, through the proxy too.
	here := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "this machine") }))
	defer here.Close()
	name := fmt.Sprintf("qory-test-origin-%d", os.Getpid())
	if out, err := exec.Command("docker", "run", "--detach", "--rm", "--name", name, "busybox:stable", "sh", "-c", "mkdir /w && echo from the origin > /w/index.html && httpd -f -p 8080 -h /w").CombinedOutput(); err != nil {
		t.Fatalf("the origin: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", name).Run() })
	ip, err := exec.Command("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + strings.TrimSpace(string(ip)) + ":8080/"
	root := newCheckout(t)
	sibling := t.TempDir()
	writeFile(t, filepath.Join(sibling, "note"), "from beside the checkout\n")
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: sh
      args:
        - - -c
          - |
            echo "proxied: $(wget -q -T 5 -O - `+origin+`)"
            echo "this machine: $(NO_PROXY= no_proxy= wget -q -T 5 -O - `+here.URL+` 2>&1)"
            nc -w 3 1.1.1.1 80 </dev/null && echo "direct: connected" || echo "direct: nowhere"
            echo '{"hook_event_name":"SessionEnd"}' | /qory/qory run forward; echo "forwarder: $?"
            test -r "$1" && echo "settings: readable"
            echo "sibling: $(cat `+sibling+`/note)"
            touch `+sibling+`/mine 2>/dev/null && echo "sibling: written" || echo "sibling: read-only"
            echo "shm: $(df -k /dev/shm | tail -1 | awk '{print $2}') pids: $(cat /sys/fs/cgroup/pids.max)"
            id -u
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: busybox:stable\n  helper: "+helper+"\n")
	out, err := run(t, "run", "claude", "--mount", sibling+":ro", "--shm-size", "256m", "--pids-limit", "512", "--memory", "512m")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "proxied: from the origin", "403", "direct: nowhere", "forwarder: 0", "settings: readable",
		"sibling: from beside the checkout", "sibling: read-only", "shm: 262144 pids: 512")
	lacks(t, out, "direct: connected", "this machine: this machine", "sibling: written")
	_, evs := events(t, root)
	if egress := evs["dev.qory.run.egress"]; len(egress) != 2 || egress[0]["decision"] != "allowed" || egress[1]["decision"] != "denied" || egress[1]["rule"] != "wall:own-address" {
		t.Errorf("run.egress %v", egress)
	}
	if runtime.GOOS == "linux" && len(evs["dev.qory.session.ended"]) != 1 {
		t.Errorf("the hook did not reach Forager: %v", evs)
	}
}

// TestRunIsNamedLimitedAndUnderItsOwnPolicy is a run started by a system of its own: the
// id and the labels are the caller's, and win over the origin remote's, the run's
// policy file narrows the machine's and never widens it, the runtime is stopped at the
// limit with timeout(1)'s status, and the access key's variables and QORY_SERVER_SECRET
// in qory's environment are not in the session's. With a server configured the run's own
// policy is refused, unless --local keeps the run to the files.
func TestRunIsNamedLimitedAndUnderItsOwnPolicy(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "slow-runtime")
	writeFile(t, script, "#!/bin/sh\ntest -z \"$QORY_ACCESS_KEY_SECRET$QORY_ACCESS_KEY_ID$QORY_APIARY_PUBLIC_KEY$QORY_SERVER_SECRET\" || exit 7\nexec sleep 30\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	setVariables := func() {
		t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
		t.Setenv("QORY_ACCESS_KEY_ID", testAccessKey)
		t.Setenv("QORY_APIARY_PUBLIC_KEY", `[{"alg":"ed25519","public_key":"`+newKey(t).PublicKey().String()+`"}]`)
	}
	setVariables()
	machine := "apiVersion: qory.dev/v1alpha1\ngateway:\n  egress:\n    mode: enforce\n    allow: [\"*.github.com\", api.anthropic.com]\n"
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), machine+"  server:\n    url: https://qory.example\n")
	policy := filepath.Join(t.TempDir(), "run-policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: enforce\n  allow: [api.github.com, pypi.org]\n")
	if _, err := run(t, "run", "--policy", policy); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "--policy is the run's own policy without a server; with server configured the server's run configuration is the policy") {
		t.Errorf("--policy with a server: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
	// Without a server section, QORY_SERVER_SECRET is taken and removed as the others are.
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), machine)
	setVariables()
	t.Setenv("QORY_SERVER_SECRET", "a-workspace-secret")
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	out, err := run(t, "run", "--local", "--policy", policy, "--run-id", id, "--label", "run_key=queue/1234", "--label", "issue=77", "--label", "repository=acme/shop", "--timeout", "300ms", "--stop-grace", "2s")
	if cmd.ExitCode(err) != 124 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	// The time limit is said once, in qory's line: the session's own line is left out.
	wants(t, out, "✗ the run was cancelled: it reached the time limit of 300ms, and claude was stopped\n")
	lacks(t, out, "stopped at the limit")
	dir, evs := events(t, root)
	if filepath.Base(dir) != id {
		t.Errorf("the run is recorded in %s", dir)
	}
	if labels, _ := evs["dev.qory.run.started"][0]["labels"].(map[string]any); labels["run_key"] != "queue/1234" || labels["issue"] != "77" || labels["repository"] != "acme/shop" || labels["forge"] != "git.example.com" {
		t.Errorf("run.started %v", evs["dev.qory.run.started"])
	}
	applied := evs["dev.qory.run.policy_applied"][0]
	if allow, _ := applied["allow"].([]any); applied["mode"] != "enforce" || len(allow) != 1 || allow[0] != "api.github.com" {
		t.Errorf("run.policy_applied %v", applied)
	}
	if exited := evs["dev.qory.run.exited"][0]; exited["reason"] != "timeout" || exited["state"] != "cancelled" {
		t.Errorf("run.exited %v", exited)
	}
}

// TestRunReportsToTheServer is a run with a server configured: Forager fetches the
// server's configuration, signed with the key and the secret, pings, takes the server's
// run configuration as the policy, asked for with every label of the run (the
// checkout's forge and repository among them), and posts every event where the configuration says; the record says the policy was
// fetched and from where.
func TestRunReportsToTheServer(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"enforce","allow":["api.example"]}}`)
	serverFile(t, srv, "  egress:\n    mode: observe\n")
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run", "--label", "issue=77")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "claude exited 0")
	_, evs := events(t, root)
	applied := evs["dev.qory.run.policy_applied"]
	if allow, _ := applied[0]["allow"].([]any); len(applied) != 1 || applied[0]["source"] != "fetched" || applied[0]["mode"] != "enforce" || len(allow) != 1 || allow[0] != "api.example" || applied[0]["url"] != srv.URL+"/v1/run-configuration" || applied[0]["run_configuration"] != digest(`{"version":1,"security_policy":`+srv.policy+`}`) {
		t.Errorf("run.policy_applied %v", applied)
	}
	if srv.refused != 0 || strings.Join(srv.queries, " ") != "forge=git.example.com&issue=77&repository=acme%2Fapp" {
		t.Errorf("the server refused %d requests and was asked %q", srv.refused, srv.queries)
	}
	got := srv.byType()
	if ping := got["dev.qory.ping"]; len(ping) != 1 || ping[0]["contract_version"] != float64(1) {
		t.Errorf("the ping: %v", ping)
	}
	if started := got["dev.qory.run.started"]; len(started) != 1 || started[0]["labels"].(map[string]any)["issue"] != "77" || started[0]["labels"].(map[string]any)["repository"] != "acme/app" {
		t.Errorf("the server's run.started: %v", started)
	}
	if len(got["dev.qory.run.exited"]) != 1 || len(got["dev.qory.run.policy_applied"]) != 1 {
		t.Errorf("the server's events: %v", got)
	}

	// A server that names no run configuration leaves the machine's policy; a server
	// that does not answer is no run.
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	srv = newFakeServer(t, "")
	serverFile(t, srv, "  egress:\n    mode: enforce\n    allow: [api.example]\n")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, evs := events(t, root); evs["dev.qory.run.policy_applied"][0]["source"] != "config" || len(srv.byType()["dev.qory.run.exited"]) != 1 {
		t.Errorf("without a run section: %v", evs["dev.qory.run.policy_applied"])
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	gone := newFakeServer(t, "")
	gone.Close()
	serverFile(t, gone, "")
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), gone.URL) {
		t.Errorf("a server that does not answer: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a run the server did not answer left a record")
	}
}

// TestResendClosesAndDeliversARunItsForagerLeft is a job's last step: the record of a
// run nobody received, cut short the way a Forager process that died leaves it, is closed with
// the reason and sent whole, once, to the server's events endpoint after its
// configuration was fetched; a run that is not there is the user's mistake. The record
// is found from the checkout, reached through a link too.
func TestResendClosesAndDeliversARunItsForagerLeft(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	if out, err := run(t, "run", "--local", "--run-id", id); cmd.ExitCode(err) != 3 {
		t.Fatalf("%v\n%s", err, out)
	}
	file := filepath.Join(runsDir(t, root), id, "events.jsonl")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	writeFile(t, file, strings.Join(lines[:len(lines)-1], ""))
	// A delivered.log, as a run that opened at its server leaves: without it the run
	// had no server, and nothing of it is sent.
	writeFile(t, filepath.Join(runsDir(t, root), id, "delivered.log"), "")

	out, err := run(t, "run", "resend", id)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "✓ the record had no end, and now ends as lost: its end was never recorded\n", fmt.Sprintf("%d events were accepted", len(lines)))
	lacks(t, out, "gateway_lost", "closed with the reason")
	got := srv.events
	if last := got[len(got)-1]; len(got) != len(lines) || last["type"] != "dev.qory.run.exited" || last["data"].(map[string]any)["reason"] != "gateway_lost" {
		t.Errorf("the server got %d events, the last %v", len(got), last)
	}
	if out, err := run(t, "run", "resend", id); err != nil || !strings.Contains(out, "0 events were accepted") || len(srv.events) != len(lines) {
		t.Errorf("a second resend: %v\n%s", err, out)
	}
	if _, err := run(t, "run", "resend", "0191f2a4-3c5e-7b8d-9e0f-000000000000"); cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a run that is not recorded: %v", err)
	}
	// The checkout reached through a link has the same folder.
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	if out, err := run(t, "run", "resend", id); err != nil || !strings.Contains(out, "0 events were accepted") {
		t.Errorf("a resend through a link: %v\n%s", err, out)
	}
}

// The lines qory run resend says of a resend that sends nothing of a run the server
// never opened: Forager's own line that says so is left out.
const (
	resendNotOpen = "✓ the server never opened run %s, so there is nothing to send; its record stays in %s"
	resendStopped = "qory run resend: the server said stop during the run; nothing is sent"
)

// TestResendSendsNothingOfARunTheServerNeverOpened is a record of a run that never
// opened at the server: one whose ping it never accepted, and one of a run with no
// server, --local. Each is sent nothing and left as it is; qory says that the server
// never opened the run, Forager's own line that says why is left out, and the resend is
// exit 0. A record's torn lines are still said, and so is every line of a run that did
// open: the torn lines' and the server's stop.
func TestResendSendsNothingOfARunTheServerNeverOpened(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	// record runs a run with args and returns its directory, its events.jsonl and what
	// that held.
	record := func(t *testing.T, id string, args ...string) (string, string, []byte) {
		t.Helper()
		if out, err := run(t, append([]string{"run", "--run-id", id}, args...)...); cmd.ExitCode(err) != 3 {
			t.Fatalf("%v\n%s", err, out)
		}
		dir := filepath.Join(runsDir(t, root), id)
		file := filepath.Join(dir, "events.jsonl")
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return dir, file, before
	}
	// unopen leaves dir as a run whose ping the server never accepted does: no
	// delivered.log.
	unopen := func(t *testing.T, dir string) {
		t.Helper()
		for _, name := range []string{"delivered.log", "undelivered"} {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// tear puts a line that is no whole event after the first one of file.
	tear := func(t *testing.T, file string, before []byte) []byte {
		t.Helper()
		first, rest, _ := strings.Cut(string(before), "\n")
		torn := []byte(first + "\nnot an event\n" + rest)
		writeFile(t, file, string(torn))
		return torn
	}
	unchanged := func(t *testing.T, name, dir, file string, before []byte, sent int) {
		t.Helper()
		if len(srv.events) != sent {
			t.Errorf("%s: the server got %d events", name, len(srv.events)-sent)
		}
		if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
			t.Errorf("%s: the record changed: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "delivered.log")); err == nil {
			t.Errorf("%s: the resend wrote a delivered.log", name)
		}
	}

	for _, c := range []struct {
		name, id, forager string
		args              []string
	}{
		{"a ping the server never accepted", "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5eb0", gateway.ResendNotOpened, nil},
		{"a run with no server", "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5eb1", gateway.ResendNoServer, []string{"--local"}},
	} {
		dir, file, before := record(t, c.id, c.args...)
		unopen(t, dir)
		sent := len(srv.events)
		out, err := run(t, "run", "resend", c.id)
		want := fmt.Sprintf(resendNotOpen, c.id, ui.Short(dir, root)) + "\n"
		if err != nil || out != want {
			t.Errorf("%s: %v (exit %d)\n%q\nwant\n%q", c.name, err, cmd.ExitCode(err), out, want)
		}
		lacks(t, out, c.forager)
		unchanged(t, c.name, dir, file, before, sent)
	}

	// A torn line of a record the server never opened is still said, before qory's line.
	const tornID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5eb2"
	dir, file, before := record(t, tornID)
	unopen(t, dir)
	before = tear(t, file, before)
	sent := len(srv.events)
	out, err := run(t, "run", "resend", tornID)
	want := "qory run resend: " + fmt.Sprintf(gateway.ResendTorn, 1, file) + "\n" + fmt.Sprintf(resendNotOpen, tornID, ui.Short(dir, root)) + "\n"
	if err != nil || out != want {
		t.Errorf("torn lines of a run never opened: %v (exit %d)\n%q\nwant\n%q", err, cmd.ExitCode(err), out, want)
	}
	unchanged(t, "torn lines of a run never opened", dir, file, before, sent)

	// A run that opened: nothing is left out, the torn lines' line nor the server's stop.
	const stopID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5eb3"
	dir, file, before = record(t, stopID)
	tear(t, file, before)
	writeFile(t, filepath.Join(dir, "delivered.log"), "stopped\n")
	out, err = run(t, "run", "resend", stopID)
	if cmd.ExitCode(err) != 1 {
		t.Fatalf("a run the server stopped: %v\n%s", err, out)
	}
	wants(t, out, "qory run resend: "+fmt.Sprintf(gateway.ResendTorn, 1, file)+"\n"+resendStopped+"\n")
	lacks(t, out, "never opened")
}

// The lines qory run resend says of a server that wants no more events of a run.
const (
	// resendStoppedNow is the server's signed 410 during the resend, a format of the run
	// id, what it accepted, what was not sent and the run directory.
	resendStoppedNow = "✗ the server wants no more events of the run %s; %d were accepted and %d were not sent; they stay in %s"
	// resendAnswered410 is Forager's line of a 410 with no code.
	resendAnswered410 = "qory run resend: the server answered 410; no further batch is sent for this run, which goes on"
	// resendNotAccepted is a server that did not accept within --wait, a format of what
	// it accepted, what it did not and the run directory.
	resendNotAccepted = "✗ %d events were accepted and %d were not; %s/undelivered contains them"
)

// TestResendSaysTheServerWantsNoMoreEvents is a resend to the server, with no
// session.gateway, of a record that owes the server more events than one batch holds. A
// server that answers a signed 410 during the resend is sent nothing more: qory says what
// it accepted before and what was not sent, which stays in the run directory, none of it
// under undelivered/, exit 1. A server that stopped the run during the run is sent
// nothing, and Forager's line says so alone, exit 1. One that accepts nothing within
// --wait is said as before.
func TestResendSaysTheServerWantsNoMoreEvents(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	// record runs a run with no server, and makes its record that of a run that opened
	// at its server, which accepted nothing of it, with more events than one batch
	// holds. It returns the run directory, its events.jsonl, what that holds and how many
	// events it owes.
	record := func(t *testing.T, id string) (string, string, []byte, int) {
		t.Helper()
		if out, err := run(t, "run", "--local", "--run-id", id); cmd.ExitCode(err) != 3 {
			t.Fatalf("%v\n%s", err, out)
		}
		dir := filepath.Join(runsDir(t, root), id)
		file := filepath.Join(dir, "events.jsonl")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
		decode := func(line string) map[string]any {
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatal(err)
			}
			return ev
		}
		exited := decode(lines[len(lines)-1])
		seq, err := strconv.Atoi(exited["sequence"].(string))
		if err != nil || exited["type"] != "dev.qory.run.exited" {
			t.Fatalf("the record's last event: %v", exited)
		}
		// Copies of the event after run.started, each of an id and a sequence of its
		// own, before run.exited.
		var b strings.Builder
		for _, l := range lines[:len(lines)-1] {
			b.WriteString(l)
		}
		const more = 2 * sink.BatchEvents
		for i := range more + 1 {
			ev := decode(lines[1])
			if i == more {
				ev = exited
			}
			ev["id"], ev["sequence"] = event.NewID(), strconv.Itoa(seq+i)
			line, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(append(line, '\n'))
		}
		writeFile(t, file, b.String())
		writeFile(t, filepath.Join(dir, "delivered.log"), "")
		return dir, file, []byte(b.String()), len(lines) + more
	}
	// stays checks that a resend left the record as it was, and spooled nothing.
	stays := func(t *testing.T, name, dir, file string, before []byte) {
		t.Helper()
		if after, err := os.ReadFile(file); err != nil || string(after) != string(before) {
			t.Errorf("%s: the record changed: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "undelivered")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: undelivered/ is there: %v", name, err)
		}
	}

	// A 410 after the server accepted a batch: what it accepted and what was not sent.
	srv.stopRun = true
	const laterID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ec0"
	dir, file, before, owed := record(t, laterID)
	out, err := run(t, "run", "resend", laterID)
	sent := len(srv.events)
	want := resendAnswered410 + "\n" + fmt.Sprintf(resendStoppedNow, laterID, sent, owed-sent, ui.Short(dir, root)) + "\n"
	if cmd.ExitCode(err) != 1 || out != want || sent == 0 || sent == owed {
		t.Errorf("a 410 after a batch: %v (exit %d), %d of %d sent\n%q\nwant\n%q", err, cmd.ExitCode(err), sent, owed, out, want)
	}
	lacks(t, out, "undelivered")
	stays(t, "a 410 after a batch", dir, file, before)

	// A 410 to the first batch: nothing was accepted.
	srv.stopRun, srv.stopPing = false, true
	const firstID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ec1"
	dir, file, before, owed = record(t, firstID)
	out, err = run(t, "run", "resend", firstID)
	want = resendAnswered410 + "\n" + fmt.Sprintf(resendStoppedNow, firstID, 0, owed, ui.Short(dir, root)) + "\n"
	if cmd.ExitCode(err) != 1 || out != want || len(srv.events) != sent {
		t.Errorf("a 410 to the first batch: %v (exit %d)\n%q\nwant\n%q", err, cmd.ExitCode(err), out, want)
	}
	stays(t, "a 410 to the first batch", dir, file, before)

	// A server that stopped the run during the run is sent nothing: Forager's line alone
	// says so, and the resend fails.
	srv.stopPing = false
	const duringID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ec2"
	dir, file, before, _ = record(t, duringID)
	writeFile(t, filepath.Join(dir, "delivered.log"), "stopped\n")
	out, err = run(t, "run", "resend", duringID)
	want = resendStopped + "\n"
	if cmd.ExitCode(err) != 1 || out != want || len(srv.events) != sent {
		t.Errorf("a stop during the run: %v (exit %d)\n%q\nwant\n%q", err, cmd.ExitCode(err), out, want)
	}
	stays(t, "a stop during the run", dir, file, before)

	// A server that accepts nothing within --wait: what was not accepted is spooled, and
	// qory's line alone says so: Forager's line of it is left out.
	srv.unavailable = true
	const awayID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ec3"
	dir, _, _, owed = record(t, awayID)
	out, err = run(t, "run", "resend", awayID, "--wait", "2s")
	want = fmt.Sprintf(resendNotAccepted, 0, owed, ui.Short(dir, root)) + "\n"
	if cmd.ExitCode(err) != 1 || out != want {
		t.Errorf("a server away: %v (exit %d)\n%q\nwant\n%q", err, cmd.ExitCode(err), out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "undelivered")); err != nil {
		t.Errorf("a server away: %v", err)
	}
}

// TestRunRunsARuntimeTheForagerShipsNothingFor pins that qory run is not Claude Code's:
// a runtime with no descriptor runs bare, the run recorded and the session not, with no
// settings written for it; and a descriptor of the machine's makes its output events
// and says which signal asks it to leave.
func TestRunRunsARuntimeTheForagerShipsNothingFor(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "fake-codex")
	writeFile(t, script, `#!/bin/sh
echo '{"kind":"done","text":"all good","session":"s-1","ok":true}'
test -n "$QORY_TEST_WAIT" || exit 0
trap 'exit 7' HUP
trap '' TERM
sleep 30 & wait
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    codex:
      command: `+script+`
      args: []
`)
	if out, err := run(t, "harness", "compose", "--runtime", "codex", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "run", "--headless")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "codex exited 0")
	dir, evs := events(t, root)
	started := evs["dev.qory.run.started"]
	if len(started) != 1 || started[0]["runtime"] != "codex" || started[0]["runtime_version"] != nil {
		t.Errorf("run.started %v", started)
	}
	if len(evs["dev.qory.session.result"]) != 0 || len(evs["dev.qory.run.exited"]) != 1 {
		t.Errorf("a bare runtime's events: %v", evs)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Error("Claude Code's settings were written for another runtime")
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", cmd.DescriptorsDir, "codex.yaml"), `version: 1
runtime: codex
runtime_version: "0.9"
sources:
  output: {format: jsonl}
stop: {signal: SIGHUP}
rules:
  - source: output
    match: {kind: done}
    type: dev.qory.session.result
    data: {session_id: session, outcome: text, is_error: ok}
`)
	t.Setenv("QORY_TEST_WAIT", "1")
	out, err = run(t, "run", "--headless", "--timeout", "500ms")
	if cmd.ExitCode(err) != 124 {
		t.Fatalf("%v\n%s", err, out)
	}
	_, evs = events(t, root)
	if started := evs["dev.qory.run.started"]; len(started) != 1 || started[0]["runtime_version"] != "0.9" {
		t.Errorf("run.started %v", started)
	}
	if result := evs["dev.qory.session.result"]; len(result) != 1 || result[0]["outcome"] != "all good" {
		t.Errorf("session.result %v", result)
	}
	if exited := evs["dev.qory.run.exited"]; len(exited) != 1 || exited[0]["exit_code"] != float64(7) {
		t.Errorf("the descriptor's signal did not ask it to leave: %v", exited)
	}
}

// TestRunRefusesWhenTheEngineCannotBeAsked is an earlier walled run whose wall could not
// be removed, so Forager keeps its entry. While the engine lists that run's
// containers, it has none, and the next walled run of the same checkout starts. Once
// listing them fails, the next run cannot tell whether an earlier one is still going,
// and is refused before the wall runs anything, in qory's words, giving the command
// that lists the earlier run's containers by its id, and the registry entry to delete.
func TestRunRefusesWhenTheEngineCannotBeAsked(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	docker := filepath.Join(dir, "docker")
	// As fakeDocker, with an engine id and a context, but removing a container or a
	// network fails, and once the file "unreachable" exists, so does listing a run's
	// containers.
	writeFile(t, docker, `#!/bin/sh
echo "$*" >> `+log+`
case "$*" in
"ps --all "*label=dev.qory.run=*) test -e `+filepath.Join(dir, "unreachable")+` && { echo "cannot connect" >&2; exit 1; } ;;
esac
case "$1 $2" in
"info --format") echo "ENGINE-0001" ;;
"context show") echo "default" ;;
"network inspect") echo "172.30.0.1 " ;;
"network rm") echo "busy" >&2; exit 1 ;;
"rm "*) echo "busy" >&2; exit 1 ;;
"logs "*) echo "relay: listening" ;;
"run "*) echo "inside the container"; exit 4 ;;
esac
`)
	if err := os.Chmod(docker, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	out, err := run(t, "run", "claude")
	if err == nil {
		t.Fatalf("the earlier run succeeded\n%s", out)
	}
	wants(t, out, "inside the container")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a run beside an earlier one the engine says is over: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if ids := recorded(t, root); len(ids) != 2 {
		t.Fatalf("earlier runs %v", ids)
	}
	writeFile(t, filepath.Join(dir, "unreachable"), "")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "run", "claude")
	// Forager names the one entry left in its registry under the state directory:
	// the second run's, whose wall was left too. The first run's went once the engine
	// said it held none of its containers.
	walled := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory-forager", "walled")
	ids := recorded(t, root)
	slices.Sort(ids)
	entry := filepath.Join(walled, ids[len(ids)-1])
	if _, statErr := os.Stat(entry); statErr != nil {
		t.Fatalf("no registry entry for the second run: %v", statErr)
	}
	want := "Docker could not be asked whether an earlier walled run is still going, so the run does not start. If docker ps --all --filter label=dev.qory.run=" + ids[len(ids)-1] + " lists no container, or that Docker is gone for good, delete " + entry + " (engine_unreachable)"
	if err == nil || err.Error() != want {
		t.Fatalf("a run beside an earlier one the engine cannot be asked about: %v, want %q", err, want)
	}
	data, _ := os.ReadFile(log)
	lacks(t, string(data), "run ", "network create")
	if ids := recorded(t, root); len(ids) != 2 {
		t.Errorf("the refused run left a record: %v", ids)
	}
}

// TestRunRefusesARunIDUsedBefore gives --run-id a run of this checkout already used: the
// session refuses it before the run makes anything, qory says so and how to go on, names
// no record, since the folder of that id is the other run's, and leaves that folder as
// it was.
func TestRunRefusesARunIDUsedBefore(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	if out, err := run(t, "run", "--run-id", id); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	folder := filepath.Join(runsDir(t, root), id)
	before := snapshot(t, folder)
	out, err := run(t, "run", "--run-id", id)
	want := "the run id " + id + " is already used by another run; leave out --run-id, or give a new one"
	if cmd.ExitCode(err) != 1 || err == nil || err.Error() != want {
		t.Fatalf("a run id used before: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	lacks(t, out, "the record is in", "the server closed the run", "hello from")
	if after := snapshot(t, folder); !maps.Equal(before, after) {
		t.Errorf("the refused run changed the first run's folder:\nbefore %v\nafter  %v", before, after)
	}
}
