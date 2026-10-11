package cmd_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/receiver"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
)

// keptKeyGateway stands in for the address a run key names: each connection is closed
// unanswered, so the session cannot go on, and at each one it looks at the run key qory
// keeps, then, for the run.
type keptKeyGateway struct {
	ln    net.Listener
	mu    sync.Mutex
	modes []os.FileMode
	keys  []string
}

// newKeptKeyGateway listens until the test ends; path is the kept run key's path.
func newKeptKeyGateway(t *testing.T, path func() string) *keptKeyGateway {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &keptKeyGateway{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
			mode, key := os.FileMode(0), ""
			if info, err := os.Lstat(path()); err == nil {
				mode = info.Mode()
				b, _ := os.ReadFile(path())
				key = string(b)
			}
			g.mu.Lock()
			g.modes, g.keys = append(g.modes, mode), append(g.keys, key)
			g.mu.Unlock()
		}
	}()
	return g
}

func (g *keptKeyGateway) url() string  { return "https://" + g.ln.Addr().String() }
func (g *keptKeyGateway) host() string { return g.ln.Addr().String() }

// seen is the kept key's mode and contents at each connection.
func (g *keptKeyGateway) seen() ([]os.FileMode, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]os.FileMode(nil), g.modes...), append([]string(nil), g.keys...)
}

// TestAMachineBehindViaRegistersItsOwnRuns is a machine with its own access key whose
// forager.yaml sets gateway.server.via: qory run is walled only; it reads the server's
// discovery and registers the run, labels and what it is about included, through via,
// with each target unchanged; it keeps the run key in run-keys/<run-id> beside
// forager.yaml, mode 0600, while the session runs through the gateway the key names,
// with no gateway of its own; and it removes the key once nothing of the run is left
// to send. The key is in no output and in no file under the run records.
func TestAMachineBehindViaRegistersItsOwnRuns(t *testing.T) {
	root := runKeyCheckout(t)
	const id = "0192f0c1-7d4e-7a2b-8c3d-444444444444"
	kept := filepath.Join(string(configDir()), foragerdir.RunKeysDir, id)
	g := newKeptKeyGateway(t, func() string { return kept })
	srv := newFakeServer(t, "")
	srv.choose(receiver.Default, g.url(), newKey(t))
	via := newViaGateway(t, srv.URL)

	serverFile(t, srv, via.via())
	_, err := run(t, "run")
	if err == nil || !strings.HasSuffix(err.Error(), "holds this machine's access key, so every run needs a wall: --wall docker, or wall in forager.yaml; or keep the key with a starter, and run this machine without it, behind a gateway (key_needs_wall)") {
		t.Errorf("an unwalled run: %v", err)
	}
	if relayed := via.relayed(); len(relayed) != 0 {
		t.Errorf("an unwalled run sent %v", relayed)
	}

	walledServerFile(t, srv, via.via())
	out, err := run(t, "run", "--run-id", id, "--label", "issue=77", "--title", "Fix the parser")
	if err == nil {
		t.Fatalf("the run went on without its gateway:\n%s", out)
	}
	wants(t, out, "qory run: node "+testPool+", instance ", "qory run: through the gateway "+g.host()+", run "+id+"\n")
	want := []string{"/.well-known/qory-configuration", receiver.DefaultRunPath}
	if relayed, targets := via.relayed(), srvTargets(srv); strings.Join(relayed, " ") != strings.Join(want, " ") || strings.Join(targets, " ") != strings.Join(want, " ") {
		t.Errorf("via relayed %v, and the server got %v; want %v", relayed, targets, want)
	}
	srv.mu.Lock()
	registered, abouts := srv.registered, srv.abouts
	srv.mu.Unlock()
	if len(registered) != 1 || !strings.Contains(registered[0], "issue=77") || !strings.Contains(registered[0], "repository=") || !strings.Contains(abouts[0], `"title":"Fix the parser"`) {
		t.Errorf("the server registered %v, about %v", registered, abouts)
	}
	modes, keys := g.seen()
	if len(modes) == 0 {
		t.Fatalf("the session did not reach the gateway the run key names:\n%s", out)
	}
	if modes[0] != 0o600 || !strings.HasPrefix(keys[0], "qrk_") {
		t.Errorf("while the run ran, %s was mode %v", kept, modes[0])
	}
	if m := mode(t, filepath.Dir(kept)); m != 0o700 {
		t.Errorf("%s is mode %v", filepath.Dir(kept), m)
	}
	key := strings.TrimSpace(keys[0])
	lacks(t, out+err.Error(), key, key[4:40])
	noValueUnder(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), key[4:40])
	noValueUnder(t, root, key[4:40])
	if _, err := os.Lstat(kept); !os.IsNotExist(err) {
		t.Errorf("the run left nothing to send, and its run key is still kept: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runsDir(t, root), id)); !os.IsNotExist(err) {
		t.Errorf("the run that never reached its gateway left a record: %v", err)
	}
}

// srvTargets is every target the server got.
func srvTargets(srv *fakeServer) []string {
	targets, _ := srv.sent()
	return targets
}

// TestAMachineBehindViaSaysItsNodesChoice is the server refusing a registration because
// the node's choice and gateway.server.via disagree: a machine with via whose node is
// "Direct" is told to remove via or choose a gateway, and a machine without via whose
// node goes through a gateway is told to set via, or set the node to "Direct". Either is
// refused before anything starts, and nothing is kept.
func TestAMachineBehindViaSaysItsNodesChoice(t *testing.T) {
	runKeyCheckout(t)
	srv := newFakeServer(t, "")
	srv.choose(receiver.Direct, "https://gateway.example:8443", newKey(t))
	via := newViaGateway(t, srv.URL)
	runURL := srv.URL + receiver.DefaultRunPath

	walledServerFile(t, srv, via.via())
	_, err := run(t, "run")
	if want := `this machine's node is set to "Direct", and gateway.server.via names a gateway: remove gateway.server.via, or choose a gateway for the node in Qory Apiary (register ` + runURL + `: node_is_direct (status 409))`; err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("via, and a direct node: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if entries, _ := os.ReadDir(filepath.Join(string(configDir()), foragerdir.RunKeysDir)); len(entries) != 0 {
		t.Errorf("a refused run kept %d run keys", len(entries))
	}

	srv.mu.Lock()
	srv.handler.Nodes[testPool] = receiver.Node{Workspace: testWorkspace, Gateway: receiver.Default}
	srv.mu.Unlock()
	walledServerFile(t, srv, "")
	_, err = run(t, "run")
	if want := `this machine's node goes through a gateway, and gateway.server.via is not set: set gateway.server.via.url to the gateway's address, or set the node to "Direct" in Qory Apiary (register ` + runURL + `: node_uses_gateway (status 409))`; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("no via, and a node through a gateway: %v\nwant %q", err, want)
	}
}

// TestResendTakesTheKeptRunKey is qory run resend of a run this machine registered
// through gateway.server.via: without QORY_RUN_KEY it sends with the run key qory kept,
// which stays while events remain; once nothing is left to send, it is removed. A run
// with a gateway of its own on a machine with via is refused, since its record would
// reach the server through no gateway.
func TestResendTakesTheKeptRunKey(t *testing.T) {
	root := runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	gw := newKeyedGateway(t)
	srv := newFakeServer(t, "")
	via := newViaGateway(t, srv.URL)
	walledServerFile(t, srv, via.via())
	dir := foragerdir.Dir(configDir())

	key := mintRunKey(t, keyRunID, gw.url())
	behindRecord(t, root, keyRunID, true)
	if err := dir.WriteRunKey(keyRunID, key); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run", "resend", keyRunID)
	if err == nil || cmd.ExitCode(err) == 0 {
		t.Fatalf("the resend went on without its gateway: %v\n%s", err, out)
	}
	if _, n := gw.received(); n == 0 {
		t.Errorf("the resend did not reach the gateway with the kept key: %v\n%s", err, out)
	}
	lacks(t, out+err.Error(), key[4:40])
	if _, err := os.Lstat(dir.RunKeyPath(keyRunID)); err != nil {
		t.Errorf("events remain, and the kept run key is gone: %v", err)
	}

	notOpened := "0192f0c1-7d4e-7a2b-8c3d-555555555555"
	behindRecord(t, root, notOpened, false)
	if err := dir.WriteRunKey(notOpened, mintRunKey(t, notOpened, gw.url())); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "run", "resend", notOpened); err != nil {
		t.Fatalf("a run that never opened: %v\n%s", err, out)
	}
	if _, err := os.Lstat(dir.RunKeyPath(notOpened)); !os.IsNotExist(err) {
		t.Errorf("nothing is left to send, and the kept run key is still there: %v", err)
	}

	writeFile(t, dir.RunKeyPath(notOpened), "not a key\n")
	os.Chmod(dir.RunKeyPath(notOpened), 0o600)
	_, err = run(t, "run", "resend", notOpened)
	if want := dir.RunKeyPath(notOpened) + " is not a run key: "; err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "not a key\n") {
		t.Errorf("a kept file that is no run key: %v, want %q", err, want)
	}

	own := "0192f0c1-7d4e-7a2b-8c3d-666666666666"
	if err := os.MkdirAll(filepath.Join(runsDir(t, root), own), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "run", "resend", own)
	if want := "the run " + own + " ran with a gateway of its own on this machine, so its record goes to the server, and qory sends it there through no gateway, while gateway.server.via is set: resend it with a forager.yaml without gateway.server.via"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a run with its own gateway, and via: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if relayed := via.relayed(); len(relayed) != 0 {
		t.Errorf("a resend sent %v through via", relayed)
	}
}
