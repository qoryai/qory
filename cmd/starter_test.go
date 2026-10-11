package cmd_test

import (
	"crypto/tls"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/receiver"
	"github.com/qoryai/forager/runkey"

	"github.com/qoryai/qory/cmd"
)

// keyPipe is a pipe whose write end, w, a command writes the run key to and closes;
// read reads what it wrote, once the command is done.
type keyPipe struct{ r, w int }

// newKeyPipe makes a pipe; its read end is closed when the test ends.
func newKeyPipe(t *testing.T) *keyPipe {
	t.Helper()
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	k := &keyPipe{r: p[0], w: p[1]}
	t.Cleanup(k.closeRead)
	return k
}

// closeRead closes the read end, once.
func (p *keyPipe) closeRead() {
	if p.r >= 0 {
		syscall.Close(p.r)
		p.r = -1
	}
}

// fd is the write end's number, as --run-key-fd takes it.
func (p *keyPipe) fd() string { return strconv.Itoa(p.w) }

// read reads the pipe to its end: what the command wrote, once it closed the write end.
func (p *keyPipe) read(t *testing.T) string {
	t.Helper()
	var b []byte
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(p.r, buf)
		if n <= 0 || err != nil {
			return string(b)
		}
		b = append(b, buf[:n]...)
	}
}

// starterServer is a server whose access key is a node pool's, testPool, that goes
// through its workspace's default gateway, testGatewayNode at gatewayURL, and
// forager.yaml naming that server, with more after its server section; the secret is in
// access-key-secret.
func starterServer(t *testing.T, gatewayURL, more string) *fakeServer {
	t.Helper()
	emptyDir(t)
	srv := newFakeServer(t, "")
	srv.choose(receiver.Default, gatewayURL, newKey(t))
	serverFile(t, srv, more)
	return srv
}

// sent is what the server was sent, by target, and the starters' outcomes.
func (f *fakeServer) sent() (targets, outcomes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...), append([]string(nil), f.outcomes...)
}

// TestRegisterGivesTheRunKeyToItsDescriptor is qory run register: it signs as the
// instance --instance-id names, the run id by default, registers the run with its labels
// and what it is about, writes the run key alone to --run-key-fd, says on stderr the
// node, the run, the gateway and when the run must start by, and that the key goes to
// the job, and leaves stdout empty, writing nothing on this machine. The run key is in
// no line it prints and no file.
func TestRegisterGivesTheRunKeyToItsDescriptor(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	before := snapshot(t, string(configDir()))
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	p := newKeyPipe(t)
	stdout, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", p.fd(), "--run-id", id, "--instance-id", "runner-07", "--attach-within", "15m", "--label", "issue=77", "--title", "Fix the parser")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	got := p.read(t)
	key, ok := strings.CutSuffix(got, "\n")
	if !ok || strings.ContainsAny(key, " \n") {
		t.Fatalf("the descriptor got %d bytes, not one run key and a line feed", len(got))
	}
	k, err := runkey.Verify(key, accesskey.Pin{{Alg: "ed25519", PublicKey: srv.signer.PublicKey().String()}})
	if err != nil {
		t.Fatalf("the run key does not verify: %v", err)
	}
	if k.RunID != id || k.InstanceID != "runner-07" || k.NodeID != testPool || k.Gateway != testGatewayNode || k.AttachBy.Sub(k.Issued) != 15*time.Minute {
		t.Errorf("the run key is for %s, instance %s, node %s, gateway %s, window %v", k.RunID, k.InstanceID, k.NodeID, k.Gateway, k.AttachBy.Sub(k.Issued))
	}
	if stdout != "" {
		t.Errorf("stdout got %q", stdout)
	}
	want := "qory run register: node " + testPool + ", instance runner-07\n" +
		"qory run register: run " + id + ", through the gateway gateway.example:8443, to start by " + k.AttachBy.UTC().Format(time.RFC3339) + "\n" +
		"The run key goes to the job as a secret: whoever holds it can act for this run until the run ends. Nothing was written on this machine.\n"
	if stderr != want {
		t.Errorf("stderr:\n%s\nwant:\n%s", stderr, want)
	}
	payload, signature, _ := strings.Cut(strings.TrimPrefix(key, runkey.Prefix), ".")
	for _, part := range []string{key, payload, signature} {
		lacks(t, stdout+stderr, part)
		noValueUnder(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory"), part)
		noValueUnder(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), part)
	}
	if after := snapshot(t, string(configDir())); len(after) != len(before) {
		t.Errorf("files before %v, after %v", before, after)
	}
	srv.mu.Lock()
	registered, abouts := srv.registered, srv.abouts
	srv.mu.Unlock()
	if len(registered) != 1 || registered[0] != "issue=77" || !strings.Contains(abouts[0], `"title":"Fix the parser"`) {
		t.Errorf("the server registered %v, about %v", registered, abouts)
	}

	// By default the run id is new, and the instance is the run's own.
	p = newKeyPipe(t)
	_, stderr, err = runSplit(t, "", "run", "register", "--run-key-fd", p.fd())
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	k, err = runkey.Parse(strings.TrimSpace(p.read(t)))
	if err != nil || k.InstanceID != k.RunID || k.RunID == id {
		t.Errorf("by default: run %s, instance %s, %v", k.RunID, k.InstanceID, err)
	}
	wants(t, stderr, "qory run register: node "+testPool+", instance "+k.RunID+"\n", "qory run register: run "+k.RunID+", ")

	// A descriptor that takes nothing more: the run is registered, and the key is lost.
	p = newKeyPipe(t)
	p.closeRead()
	const lost = "0192f0c1-7d4e-7a2b-8c3d-000000000009"
	_, stderr, err = runSplit(t, "", "run", "register", "--run-key-fd", p.fd(), "--run-id", lost)
	if want := "the run " + lost + " is registered, and its run key could not be written to --run-key-fd " + p.fd() + ": broken pipe; end the run with qory run end " + lost; err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("a broken descriptor: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	lacks(t, stderr, "The run key goes to the job")
}

// TestRegisterRefusesItsInput is what qory run register refuses before it sends
// anything, as input: no --run-key-fd, a descriptor that is a standard one or not open,
// a run id, an instance id or an attach window outside their forms, labels the run
// cannot have, an access key that is not there, and a forager.yaml without a server.
func TestRegisterRefusesItsInput(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no descriptor", nil, "qory run register writes the run key to a descriptor, and --run-key-fd names none: name one of 3 or above, such as --run-key-fd 3"},
		{"stdin", []string{"--run-key-fd", "0"}, "--run-key-fd 0: the standard input, output and error carry no run key; name a descriptor of 3 or above"},
		{"stdout", []string{"--run-key-fd", "1"}, "--run-key-fd 1: the standard input, output and error carry no run key; name a descriptor of 3 or above"},
		{"not open", []string{"--run-key-fd", "1000"}, "--run-key-fd 1000 is not an open descriptor"},
		{"run id", []string{"--run-key-fd", "@", "--run-id", "run-7"}, "--run-id: "},
		{"instance id", []string{"--run-key-fd", "@", "--instance-id", "-runner"}, "--instance-id: the instance id \"-runner\" is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit"},
		{"a part of a second", []string{"--run-key-fd", "@", "--attach-within", "1500ms"}, "--attach-within 1.5s is not a whole number of seconds, 1s or more"},
		{"no time", []string{"--run-key-fd", "@", "--attach-within", "0s"}, "--attach-within 0s is not a whole number of seconds, 1s or more"},
		{"a label", []string{"--run-key-fd", "@", "--label", "issue"}, "--label issue"},
	} {
		p := newKeyPipe(t)
		args := []string{"run", "register"}
		for _, a := range c.args {
			if a == "@" {
				a = p.fd()
			}
			args = append(args, a)
		}
		_, stderr, err := runSplit(t, "", args...)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d)\nwant %q", c.name, err, cmd.ExitCode(err), c.want)
		}
		if stderr != "" {
			t.Errorf("%s: stderr %q", c.name, stderr)
		}
	}
	if targets, _ := srv.sent(); len(targets) != 0 {
		t.Errorf("refused input reached the server: %v", targets)
	}

	os.Remove(configDir().Path("access-key-secret"))
	_, _, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd())
	if want := "no access key secret for the server " + srv.URL + ": qory access-key enrol makes one, or set QORY_ACCESS_KEY_SECRET"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("no secret: %v, want %q", err, want)
	}
	writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\n")
	_, _, err = runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd())
	if want := "forager.yaml defines no server to register the run with"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("no server: %v, want %q", err, want)
	}
}

// TestRegisterSaysTheServersRefusals is the server's refusal of a starter's
// registration: qory's words, then Forager's line, exit 1, and nothing on the
// descriptor. A gateway's access key, which discovery names, is refused as input before
// the registration.
func TestRegisterSaysTheServersRefusals(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	runURL := srv.URL + receiver.DefaultRunPath
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	if _, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd(), "--run-id", id); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	for _, c := range []struct {
		name  string
		setup func()
		args  []string
		want  string
	}{
		{"direct", func() {
			srv.handler.Nodes[testPool] = receiver.Node{Workspace: testWorkspace, Gateway: receiver.Direct}
		}, nil,
			`this key's node is set to "Direct": each of its machines registers its own runs, and a starter registers none. Choose a gateway for the node in Qory Apiary (register ` + runURL + `: node_is_direct (status 409))`},
		{"no default", func() {
			srv.handler.Nodes[testPool] = receiver.Node{Workspace: testWorkspace, Gateway: receiver.Default}
			srv.handler.Workspaces[testWorkspace] = receiver.Workspace{Organisation: "or_0123456789abcdef"}
		}, nil, `this key's node is set to "Default gateway", and no default gateway serves its workspace: mark a gateway as default in Qory Apiary, or choose one for the node (register ` + runURL + `: no_default_gateway (status 409))`},
		{"not serving", func() {
			srv.handler.Nodes[testPool] = receiver.Node{Workspace: testWorkspace, Gateway: "ng_0000000000000000"}
		}, nil,
			"the gateway this key's node names no longer serves the node's workspace: choose another gateway for the node in Qory Apiary (register " + runURL + ": gateway_not_serving (status 409))"},
		{"no gateway key", func() {
			srv.handler.Nodes[testPool] = receiver.Node{Workspace: testWorkspace, Gateway: testGatewayNode}
			srv.gatewayKey = nil
		}, nil, "the gateway this key's node goes through has no active access key, so no run could start there: enrol a key for the gateway, then register again (register " + runURL + ": gateway_without_key (status 409))"},
		{"the limit", func() { srv.gatewayKey, srv.full = newKey(t), true }, []string{"--run-id", "0192f0c1-7d4e-7a2b-8c3d-000000000001"},
			"the node's live instances have reached its limit, so the run 0192f0c1-7d4e-7a2b-8c3d-000000000001 is not registered: wait for a run to end, or have an owner or administrator in Qory Apiary clear an instance (register " + runURL + ": instance_limit (status 409))"},
		{"a run id used", func() { srv.full = false }, []string{"--run-id", id, "--label", "issue=8"},
			"the run id " + id + " is already used by another run; leave out --run-id, or give a new one"},
	} {
		srv.mu.Lock()
		c.setup()
		srv.mu.Unlock()
		p := newKeyPipe(t)
		_, stderr, err := runSplit(t, "", append([]string{"run", "register", "--run-key-fd", p.fd()}, c.args...)...)
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != 1 {
			t.Errorf("%s: %v (exit %d)\nwant %q", c.name, err, cmd.ExitCode(err), c.want)
		}
		wants(t, stderr, "qory run register: node "+testPool)
		if got := p.read(t); got != "" {
			t.Errorf("%s: the descriptor got %d bytes", c.name, len(got))
		}
	}

	srv.mu.Lock()
	srv.keyNode, srv.node = "", testGatewayNode
	srv.mu.Unlock()
	targets, _ := srv.sent()
	_, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd())
	if want := "this access key is a gateway's, and a gateway registers no runs: register with the key of the node pool the run belongs to"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a gateway's key: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	wants(t, stderr, "qory run register: node "+testGatewayNode)
	if after, _ := srv.sent(); len(after) != len(targets)+1 {
		t.Errorf("a gateway's key sent %v after discovery", after[len(targets):])
	}
}

// TestEndGivesTheRunsOutcome is qory run end: signed as the run's instance, it sends the
// outcome, completed as succeeded, with the reason, and says how the server records it;
// the same again gets the same line, another outcome is refused, and so is a run the
// server does not know. Without --outcome the run is cancelled, with no outcome given,
// and the request carries no state.
func TestEndGivesTheRunsOutcome(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	runURL := srv.URL + receiver.DefaultRunPath
	const id, other = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d", "0192f0c1-7d4e-7a2b-8c3d-000000000002"
	for _, run := range []string{id, other} {
		if _, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd(), "--run-id", run); err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
	}
	for i := range 2 {
		stdout, stderr, err := runSplit(t, "", "run", "end", id, "--outcome", "completed", "--reason", "all_checks_passed")
		if err != nil {
			t.Fatalf("%d: %v\n%s", i, err, stderr)
		}
		if want := "qory run end: node " + testPool + ", instance " + id + "\n✓ the server records the run " + id + " as completed: all checks passed\n"; stdout != "" || stderr != want {
			t.Errorf("%d: stdout %q, stderr:\n%s\nwant:\n%s", i, stdout, stderr, want)
		}
	}
	_, outcomes := srv.sent()
	if len(outcomes) != 2 || !strings.Contains(outcomes[0], `"state":"succeeded","reason":"all_checks_passed"`) {
		t.Errorf("the server was sent %v", outcomes)
	}
	_, _, err := runSplit(t, "", "run", "end", id, "--outcome", "failed")
	if want := "the run " + id + " has another outcome already, and a run has one (outcome " + runURL + "/" + id + ": outcome_given (status 409))"; err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("another outcome: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	const unknown = "0192f0c1-7d4e-7a2b-8c3d-000000000003"
	_, _, err = runSplit(t, "", "run", "end", unknown, "--outcome", "failed")
	if want := "the server knows no run " + unknown + " of this key's node: it registered none of that id, or no longer keeps its record (outcome " + runURL + "/" + unknown + ": not_found (status 404))"; err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("an unknown run: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	_, stderr, err := runSplit(t, "", "run", "end", other)
	if err != nil {
		t.Fatalf("no outcome: %v\n%s", err, stderr)
	}
	wants(t, stderr, "✓ the server records the run "+other+" as cancelled, with no outcome given\n")
	_, outcomes = srv.sent()
	if last := outcomes[len(outcomes)-1]; strings.Contains(last, "state") || strings.Contains(last, "reason") {
		t.Errorf("no outcome sent %s", last)
	}
	_, stderr, err = runSplit(t, "", "run", "end", other)
	if err != nil {
		t.Errorf("no outcome again: %v\n%s", err, stderr)
	}
}

// TestEndRefusesItsInput is what qory run end refuses before it sends anything, as
// input, and a gateway's access key, which discovery names, before the outcome.
func TestEndRefusesItsInput(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "qory run end takes the run id"},
		{[]string{id, id}, "qory run end takes the run id"},
		{[]string{"run-7"}, "the run id \"run-7\" "},
		{[]string{id, "--outcome", "succeeded"}, "--outcome succeeded is not completed, failed or cancelled"},
		{[]string{id, "--reason", "checks_failed"}, "--reason needs --outcome: without one, the run is cancelled, with no outcome given"},
		{[]string{id, "--outcome", "failed", "--reason", "Checks failed"}, "--reason Checks failed is not a code: a lower-case letter, then lower-case letters, digits and _, at most 64"},
		{[]string{id, "--outcome", "failed", "--reason", "timeout"}, "--reason timeout is one of Forager's own reasons; give one of yours"},
	} {
		_, stderr, err := runSplit(t, "", append([]string{"run", "end"}, c.args...)...)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v (exit %d)\nwant %q", c.args, err, cmd.ExitCode(err), c.want)
		}
		if stderr != "" {
			t.Errorf("%v: stderr %q", c.args, stderr)
		}
	}
	if targets, _ := srv.sent(); len(targets) != 0 {
		t.Errorf("refused input reached the server: %v", targets)
	}
	srv.mu.Lock()
	srv.keyNode, srv.node = "", testGatewayNode
	srv.mu.Unlock()
	_, stderr, err := runSplit(t, "", "run", "end", id)
	if want := "this access key is a gateway's, and a gateway gives no run's outcome: use the key of the node pool that registered the run"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a gateway's key: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	wants(t, stderr, "qory run end: node "+testGatewayNode+", instance "+id)
	if _, outcomes := srv.sent(); len(outcomes) != 0 {
		t.Errorf("a gateway's key sent an outcome: %v", outcomes)
	}
	writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\n")
	_, _, err = runSplit(t, "", "run", "end", id)
	if want := "forager.yaml defines no server to send the outcome to"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("no server: %v, want %q", err, want)
	}
}

// viaGateway stands in for a gateway that relays to the server: a loopback TLS 1.3
// server, whose certificate caFile holds, that passes every request on to the server
// with its target unchanged, and keeps each target it got.
type viaGateway struct {
	*httptest.Server
	caFile  string
	mu      sync.Mutex
	targets []string
}

// newViaGateway relays to srv until the test ends; its CA file is in a directory of
// its own.
func newViaGateway(t *testing.T, to string) *viaGateway {
	t.Helper()
	target, err := url.Parse(to)
	if err != nil {
		t.Fatal(err)
	}
	g := &viaGateway{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	g.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.targets = append(g.targets, r.URL.RequestURI())
		g.mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	g.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	g.Config.ErrorLog = log.New(io.Discard, "", 0)
	g.StartTLS()
	t.Cleanup(g.Close)
	g.caFile = filepath.Join(t.TempDir(), "gateway-ca.pem")
	writeFile(t, g.caFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.Certificate().Raw})))
	return g
}

// relayed is every target the gateway relayed.
func (g *viaGateway) relayed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.targets...)
}

// via is forager.yaml's gateway.server.via for the gateway.
func (g *viaGateway) via() string {
	return "    via:\n      url: " + g.URL + "\n      ca_file: " + g.caFile + "\n"
}

// TestTheStartersCommandsGoThroughVia is gateway.server.via set: qory run register and
// qory run end send discovery, the registration and the outcome to the gateway, over
// TLS checked against via.ca_file, with each target unchanged, and the server gets them
// all from the gateway. Without the CA file, the gateway's certificate is not trusted
// and nothing reaches the server.
func TestTheStartersCommandsGoThroughVia(t *testing.T) {
	emptyDir(t)
	srv := newFakeServer(t, "")
	srv.choose(receiver.Default, "https://gateway.example:8443", newKey(t))
	g := newViaGateway(t, srv.URL)
	serverFile(t, srv, g.via())
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	p := newKeyPipe(t)
	if _, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", p.fd(), "--run-id", id); err != nil {
		t.Fatalf("register: %v\n%s", err, stderr)
	}
	if _, err := runkey.Parse(strings.TrimSpace(p.read(t))); err != nil {
		t.Errorf("register through via: %v", err)
	}
	if _, stderr, err := runSplit(t, "", "run", "end", id, "--outcome", "completed"); err != nil {
		t.Fatalf("end: %v\n%s", err, stderr)
	}
	targets, _ := srv.sent()
	want := []string{"/.well-known/qory-configuration", receiver.DefaultRunPath, "/.well-known/qory-configuration", receiver.DefaultRunPath + "/" + id + "/outcome"}
	if relayed := g.relayed(); strings.Join(relayed, " ") != strings.Join(want, " ") || strings.Join(targets, " ") != strings.Join(want, " ") {
		t.Errorf("the gateway relayed %v, and the server got %v; want %v", relayed, targets, want)
	}

	serverFile(t, srv, "    via:\n      url: "+g.URL+"\n")
	_, _, err := runSplit(t, "", "run", "end", id, "--outcome", "completed")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a gateway the system does not trust: %v", err)
	}
	if after, _ := srv.sent(); len(after) != len(targets) {
		t.Errorf("a request reached the server past an untrusted gateway: %v", after[len(targets):])
	}
}
