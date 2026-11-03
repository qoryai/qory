package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/link"
	"github.com/qoryai/forager/receiver"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// The tests of this file pin the commands of a work item in a read run, whose checkout
// is read-only: the store is a temporary folder, every command in the run finds it, and
// nothing under the checkout changes.

// tempStore sets the store of a read run to a folder of the test, not yet made.
func tempStore(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "qory-artifacts")
	t.Cleanup(cmd.SetTempStoreDir(dir))

	return dir
}

// readOnlyTree makes root read-only: the modes stop this test's user, and root, whom modes
// do not stop, takes the checkout as a read-only mount shows it.
func readOnlyTree(t *testing.T, root string) {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Cleanup(cmd.SetCheckoutUnwritable())
	}

	chmodTree(t, root, func(mode fs.FileMode) fs.FileMode { return mode &^ 0o222 })
	t.Cleanup(func() {
		chmodTree(t, root, func(mode fs.FileMode) fs.FileMode { return mode | 0o200 })
	})
}

// chmodTree sets the mode of every path under root, root included.
func chmodTree(t *testing.T, root string, mode func(fs.FileMode) fs.FileMode) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() && !info.IsDir() {
			return err
		}

		return os.Chmod(path, mode(info.Mode().Perm()))
	})
	if err != nil {
		t.Fatal(err)
	}
}

// treeOf lists each path under root with its type, mode, time and digest.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		line := fmt.Sprint(info.Mode(), info.ModTime().UnixNano())

		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			line += fmt.Sprintf(" %x", sha256.Sum256(content))
		}

		tree[path] = line

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

// wantSameTree fails unless the tree at root is before.
func wantSameTree(t *testing.T, root string, before map[string]string) {
	t.Helper()

	after := treeOf(t, root)

	for path, line := range after {
		if before[path] != line {
			t.Errorf("%s changed: %q, was %q", path, line, before[path])
		}
	}

	for path := range before {
		if _, ok := after[path]; !ok {
			t.Errorf("%s is gone", path)
		}
	}
}

// signedRunAPI is a fake Qory Apiary's run API behind a real gateway: it serves one
// artifact, and records the bodies of the pushes and verdicts that reach it, signed by
// the gateway.
type signedRunAPI struct {
	mu      sync.Mutex
	pushes  []map[string]any
	gaps    []string
	content []byte
}

// The run API's fixture: the artifact brief, version 1, with one file.
const (
	gatewaySnapshot = `{"version":1,"work_item":{"id":"wi_1","number":12},"inputs":[],` +
		`"outputs":[{"type":"brief","required":true,"version":null,"check":"missing"}]}`
	gatewayArtifactFile = "brief.md"
	gatewayArtifactText = "the brief"
	gatewayAccessKey    = "ak_f1xt0re00000000e"
)

// serve answers one request of the run API at path, under the run, and reports whether
// path is one of them.
func (a *signedRunAPI) serve(signer *accesskey.Key, w http.ResponseWriter, r *http.Request, path string) bool {
	sig := r.Header.Get(server.HeaderSignature)
	answer := func(status int, body string) bool {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(server.HeaderSignature, signer.SignAnswer(accesskey.Answer{Status: status, RequestSignature: sig, Body: []byte(body)}))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)

		return true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	switch {
	case path == "work-item":
		return answer(http.StatusOK, gatewaySnapshot)
	case path == "artifacts" && r.Method == http.MethodGet:
		return answer(http.StatusOK, indexOf(artifactEntry("brief", "main", "", 1,
			versionEntry("av_1", 1, fileEntry(gatewayArtifactFile, gatewayArtifactText)))))
	case path == "artifacts":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.pushes = append(a.pushes, body)

		return answer(http.StatusCreated, fmt.Sprintf(`{"version":1,"artifact":{"type":"brief","name":"main","version":{"id":"av_%d","number":%d}}}`,
			len(a.pushes)+1, len(a.pushes)+1))
	case path == "verdict":
		raw, _ := io.ReadAll(r.Body)
		a.gaps = append(a.gaps, string(raw))

		return answer(http.StatusCreated, `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"av_9","number":1}}}`)
	case path == "artifacts/av_1/files/"+gatewayArtifactFile:
		sum := sha256.Sum256([]byte(gatewayArtifactText))
		w.Header().Set("Content-Length", fmt.Sprint(len(gatewayArtifactText)))
		w.Header().Set(server.HeaderSignature, signer.SignAnswer(accesskey.Answer{Status: http.StatusOK, RequestSignature: sig, Digest: hex.EncodeToString(sum[:])}))
		_, _ = io.WriteString(w, gatewayArtifactText)

		return true
	}

	return false
}

// startGatewayRun starts a real gateway in this process in front of a fake Qory Apiary,
// opens a run there, and sends the HTTP requests of this process for the run API through
// the run's proxy, as HTTP_PROXY does for a command in the wall. It sets QORY_RUN_API.
func startGatewayRun(t *testing.T) *signedRunAPI {
	t.Helper()

	key, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	signer, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}

	store, err := receiver.OpenFile(filepath.Join(t.TempDir(), "received.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	pinned := accesskey.Pin{{Alg: "ed25519", PublicKey: signer.PublicKey().String()}}
	pin, _ := json.Marshal(pinned)
	api := &signedRunAPI{}

	var srv *httptest.Server

	h := &receiver.Handler{
		Keys: func(id string) (receiver.AccessKey, bool) {
			return receiver.AccessKey{PublicKey: key.PublicKey()}, id == gatewayAccessKey
		},
		Signer: signer,
		Store:  store,
		Configuration: func() ([]byte, string) {
			doc := []byte(`{"version":1,"node_id":"nd_f1xt0re00000000e","workspaces":["ws_f1xt0re00000000e"],"apiary_public_key":` + string(pin) +
				`,"events":{"url":"` + srv.URL + receiver.DefaultEventsPath + `","types":["*"]},"run":{"url":"` + srv.URL + receiver.DefaultRunPath + `"}}`)
			sum := sha256.Sum256(doc)

			return doc, "sha256=" + hex.EncodeToString(sum[:])
		},
		RunConfiguration: func(receiver.Run) ([]byte, string, *receiver.Refusal) { return receiver.NoPolicy, "", nil },
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, _ := strings.CutPrefix(r.URL.Path, receiver.DefaultRunPath+"/")
		if _, path, _ := strings.Cut(rest, "/"); !api.serve(signer, w, r, path) {
			h.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	runs := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	g, err := gateway.Start(ctx, gateway.Config{
		RunDir:       func(id string) string { return filepath.Join(runs, id) },
		NoLinkSocket: true,
		Report:       func(string) {},
		Heartbeat:    time.Second,
		Server:       &gateway.Server{Version: 1, URL: srv.URL, AccessKeyID: gatewayAccessKey, ApiaryPublicKey: pinned, AccessKey: key, InstanceID: "i_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closing, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = g.Close(closing)
	})

	r, err := session.Open(ctx, session.OpenSpec{
		RunID: event.NewRunID(), RunsDir: runs, Gateway: session.LocalGateway(g.LocalLink()), Report: func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closing, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = r.Close(closing, session.Outcome{State: event.StateSucceeded})
	})

	client, base, err := r.RunAPI()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("QORY_RUN_API", base)

	before := http.DefaultTransport
	http.DefaultTransport = runAPIRoute{run: client.Transport, other: before}
	t.Cleanup(func() { http.DefaultTransport = before })

	return api
}

// runAPIRoute sends a request for the run API's host to run, and every other to other.
type runAPIRoute struct{ run, other http.RoundTripper }

// RoundTrip sends r.
func (t runAPIRoute) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == link.RunAPIHost {
		return t.run.RoundTrip(r)
	}

	return t.other.RoundTrip(r)
}

// TestAReadRunStoresThroughARealGateway is the end of a read review: with the checkout
// read-only, a pull, two pushes and a verdict reach a fake Qory Apiary through a real
// gateway, signed by it. The files are in the temporary store, and nothing under the
// checkout changes.
func TestAReadRunStoresThroughARealGateway(t *testing.T) {
	root := inCheckout(t)
	api := startGatewayRun(t)
	dir := tempStore(t)

	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("from the checkout"), 0o644); err != nil {
		t.Fatal(err)
	}

	readOnlyTree(t, root)
	before := treeOf(t, root)

	out, err := run(t, "artifact", "pull")
	if err != nil {
		t.Fatal(err)
	}

	if want := dir + "/\n"; out != want {
		t.Errorf("pull printed %q, want %q", out, want)
	}

	wantFile(t, filepath.Join(dir, "brief", "main", "v1", gatewayArtifactFile), gatewayArtifactText)

	// An agent makes a file in the store, as the read rules allow, and pushes it.
	made := filepath.Join(dir, "made.md")
	if err := os.WriteFile(made, []byte("made in the store"), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err = run(t, "artifact", "push", "brief", made); err != nil || out != "Pushed brief/main version 2.\n" {
		t.Fatalf("push of a file: %q, %v", out, err)
	}

	if out, err = runIn(t, "# Read\n", "artifact", "push", "brief", "-"); err != nil || out != "Pushed brief/main version 3.\n" {
		t.Fatalf("push of standard input: %q, %v", out, err)
	}

	gaps := filepath.Join(dir, "gaps.md")
	if err := os.WriteFile(gaps, []byte("Add the empty cart."), 0o644); err != nil {
		t.Fatal(err)
	}

	if out, err = run(t, "run", "verdict", "changes-requested", "--gaps", gaps); err != nil {
		t.Fatalf("verdict: %q, %v", out, err)
	}

	wantFile(t, filepath.Join(dir, "brief", "main", "v2", "made.md"), "made in the store")
	wantFile(t, filepath.Join(dir, "brief", "main", "v3", "stdin.md"), "# Read\n")
	wantSameTree(t, root, before)

	api.mu.Lock()
	defer api.mu.Unlock()

	if len(api.pushes) != 2 {
		t.Fatalf("Qory Apiary has %d pushes: %v", len(api.pushes), api.pushes)
	}

	files, _ := api.pushes[0]["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("the first push: %v", api.pushes[0])
	}

	file, _ := files[0].(map[string]any)
	if text, _ := file["content_base64"].(string); text != base64.StdEncoding.EncodeToString([]byte("made in the store")) {
		t.Errorf("the first push's file: %v", file)
	}

	if want := `{"verdict":"changes-requested","gaps":"Add the empty cart."}`; len(api.gaps) != 1 || api.gaps[0] != want {
		t.Errorf("the verdicts: %q, want %q", api.gaps, want)
	}
}

// TestAPullInAReadRunPrintsTheTemporaryStore prints the store's whole path, from the root
// of the checkout and from a folder in it. The files and the index are there, the folder
// has no .gitignore, and only its user reads it.
func TestAPullInAReadRunPrintsTheTemporaryStore(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)

	dir := tempStore(t)
	inside := filepath.Join(root, "src")

	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	readOnlyTree(t, root)

	for _, at := range []string{root, inside} {
		t.Chdir(at)

		out, err := run(t, "artifact", "pull")
		if err != nil {
			t.Fatal(err)
		}

		if want := dir + "/\n"; out != want {
			t.Errorf("from %s: pull printed %q, want %q", at, out, want)
		}
	}

	wantFile(t, filepath.Join(dir, "demo", "main", "v2", "demo.md"), "new demo")
	wantFile(t, filepath.Join(dir, "index.json"), server.index)

	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("the store's .gitignore: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf("the checkout's .qory: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}

	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("the store's mode is %o, want 700", mode)
	}
}

// TestAPushAfterAPullInAReadRun is two commands in a row: the push finds the store of the
// pull, and keeps the version that the run API gives. A later pull of a version keeps both,
// and a version that does not exist is named against the index in that store, which the
// pull has written.
func TestAPushAfterAPullInAReadRun(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)
	server.response = `{"version":1,"artifact":{"type":"demo","name":"main","version":{"id":"av_d3","number":3}}}`

	dir := tempStore(t)
	readOnlyTree(t, root)

	if _, err := run(t, "artifact", "pull", "demo"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "artifact", "push", "demo", filepath.Join(dir, "demo", "main", "v2", "demo.md"))
	if err != nil || out != "Pushed demo/main version 3.\n" {
		t.Fatalf("push: %q, %v", out, err)
	}

	if _, err := run(t, "artifact", "pull", "demo", "--version", "1"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(dir, "demo", "main", "v1", "demo.md"), "old demo")
	wantFile(t, filepath.Join(dir, "demo", "main", "v2", "demo.md"), "new demo")
	wantFile(t, filepath.Join(dir, "demo", "main", "v3", "demo.md"), "new demo")

	if err := os.Remove(filepath.Join(dir, "index.json")); err != nil {
		t.Fatal(err)
	}

	_, err = run(t, "artifact", "pull", "demo", "--version", "9")
	wantExit(t, err, cmd.ExitInput, "Choose a version that "+filepath.Join(dir, "index.json")+" lists. demo has no version 9.")
	wantFile(t, filepath.Join(dir, "index.json"), server.index)
}

// TestAWriteRunKeepsTheStoreInTheCheckout is a run whose checkout it can write: the store
// stays .qory/artifacts, with its .gitignore, and no temporary folder appears.
func TestAWriteRunKeepsTheStoreInTheCheckout(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)

	dir := tempStore(t)

	out, err := run(t, "artifact", "pull")
	if err != nil {
		t.Fatal(err)
	}

	if want := ".qory/artifacts/\n"; out != want {
		t.Errorf("pull printed %q, want %q", out, want)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo", "main", "v2", "demo.md"), "new demo")
	wantFile(t, filepath.Join(root, ".qory", "artifacts", ".gitignore"), "*\n")

	_, err = run(t, "artifact", "pull", "demo", "--version", "9")
	wantExit(t, err, cmd.ExitInput, "Choose a version that .qory/artifacts/index.json lists. demo has no version 9.")

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the temporary store: %v", err)
	}
}

// TestALaptopKeepsTheStoreInTheCheckout is a command outside a run: with no run API, the
// store is .qory/artifacts, whatever the check of the checkout returns.
func TestALaptopKeepsTheStoreInTheCheckout(t *testing.T) {
	root := inCheckout(t)
	dir := tempStore(t)

	t.Cleanup(cmd.SetCheckoutUnwritable())

	if _, err := runIn(t, "# A heading\n", "artifact", "push", "doc", "-"); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "run", "verdict", "passed"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "doc", "main", "v1", "stdin.md"), "# A heading\n")
	wantFile(t, filepath.Join(root, ".qory", "artifacts", "verdict", "main", "v1", "verdict.json"), `{"verdict":"passed"}`)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the temporary store: %v", err)
	}
}

// TestALinkAtTheTemporaryStoreStopsAPush is a link at the store's path: a push sends
// nothing and writes nothing through the link.
func TestALinkAtTheTemporaryStoreStopsAPush(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	dir := tempStore(t)
	target := t.TempDir()

	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}

	readOnlyTree(t, root)

	_, err := runIn(t, "text", "artifact", "push", "doc", "-")
	wantExit(t, err, 1, "Remove "+dir+": it must be a directory, not a link or a file.")

	if len(server.pushes) != 0 {
		t.Errorf("the push reached the run API: %v", server.pushes)
	}

	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("the link's target has %v", entries)
	}
}
