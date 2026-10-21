package cmd_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
)

// keyRunID is the run the tests' run keys name.
const keyRunID = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"

// keyedGateway stands in for the separate gateway a run key names: a TCP listener that
// takes each connection, keeps the first bytes it is sent, and closes it. It speaks no
// TLS, and the session trusts a gateway by the system's roots, so a session gets no
// further than its handshake: what it sent is what reached the gateway's address.
type keyedGateway struct {
	ln    net.Listener
	mu    sync.Mutex
	got   []byte
	conns int
}

// newKeyedGateway listens on a loopback port until the test ends.
func newKeyedGateway(t *testing.T) *keyedGateway {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &keyedGateway{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			b := make([]byte, 64<<10)
			n, _ := c.Read(b)
			g.mu.Lock()
			g.got, g.conns = append(g.got, b[:n]...), g.conns+1
			g.mu.Unlock()
			c.Close()
		}
	}()
	return g
}

// url is the gateway's URL, as a run key names it.
func (g *keyedGateway) url() string { return "https://" + g.ln.Addr().String() }

// host is the gateway's host and port, as qory run's first line names it.
func (g *keyedGateway) host() string { return g.ln.Addr().String() }

// received is what the gateway was sent, and over how many connections.
func (g *keyedGateway) received() ([]byte, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return bytes.Clone(g.got), g.conns
}

// mintRunKey is a run key for the run runID through the gateway at gatewayURL, signed
// as Qory Apiary signs one, under a server key of its own.
func mintRunKey(t *testing.T, runID, gatewayURL string) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	s, err := runkey.Key{
		RunID: runID, Workspace: "ws_0123456789abcdef", NodeID: "np_0123456789abcdef",
		Gateway: testGatewayNode, InstanceID: "i_gYKDhIWGh4iJiouMjY6PkA", GatewayURL: gatewayURL,
		GatewayKeys: []accesskey.PublicKey{newKey(t).PublicKey()},
		Issued:      now, AttachBy: now.Add(10 * time.Minute),
	}.Sign(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// noValueUnder fails the test when a file under dir holds value.
func noValueUnder(t *testing.T, dir, value string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), value) {
			t.Errorf("%s holds the run key", path)
		}
		return nil
	})
}

// runKeyCheckout is a checkout composed for the fake runtime, which exits 0.
func runKeyCheckout(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	return root
}

// TestARunKeyThatIsNotOneIsRefused is a QORY_RUN_KEY that does not read as a run key:
// qory run and qory run resend refuse it as input, naming the part that is wrong and
// never the value, and start nothing.
func TestARunKeyThatIsNotOneIsRefused(t *testing.T) {
	root := runKeyCheckout(t)
	const marker = "run-key-marker-7d1f0c94"
	for _, c := range []struct{ value, reason string }{
		{marker, "it does not start with qrk_"},
		{"qrk_" + marker, "it has no dot between its payload and its signature"},
		{"qrk_" + marker + " x.y", "it holds a character outside base64url and the dot, such as white space"},
		{"qrk_" + marker + ".AAAA", "its signature is not 64 bytes in base64url without padding"},
	} {
		for _, args := range [][]string{{"run"}, {"run", "resend", keyRunID}} {
			t.Setenv("QORY_RUN_KEY", c.value)
			out, err := run(t, args...)
			if want := "QORY_RUN_KEY is not a run key: " + c.reason; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v %q: %v (exit %d), want %q", args, c.value, err, cmd.ExitCode(err), want)
			}
			lacks(t, out, marker, "hello from")
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
}

// TestARunKeyTakesTheRunThroughItsGateway is a run key given by --run-key-fd, from a
// descriptor, white space around it, or from stdin, 0: qory run starts no gateway of its
// own, says first that the run goes through the gateway the key names, by its host and
// port, and the run the key names, and its session goes to that gateway's address. A
// --run-id that is the key's is the same run; another is Forager's refusal, as input,
// before anything is reached.
func TestARunKeyTakesTheRunThroughItsGateway(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"run", "--run-key-fd", descriptor(t, " "+key+"\n")}, ""},
		{[]string{"run", "--run-id", keyRunID, "--run-key-fd", descriptor(t, key)}, ""},
		{[]string{"run", "--run-key-fd", "0"}, key + "\n"},
	} {
		args := c.args
		out, err := runIn(t, c.stdin, args...)
		if err == nil {
			t.Fatalf("%v: the run went on without its gateway:\n%s", args, out)
		}
		if first, _, _ := strings.Cut(out, "\n"); first != "qory run: through the gateway "+gw.host()+", run "+keyRunID {
			t.Errorf("%v: the first line is %q\n%s", args, first, out)
		}
		lacks(t, out, "qory run: node", "hello from")
		lacks(t, out+err.Error(), key, key[4:40])
	}
	if _, n := gw.received(); n < 3 {
		t.Errorf("the gateway's address was reached %d times, want once a run", n)
	}

	const other = "0192f0c1-7d4e-7a2b-8c3d-000000000000"
	_, before := gw.received()
	out, err := run(t, "run", "--run-id", other, "--run-key-fd", descriptor(t, key))
	if want := "the run id " + other + " is not the one QORY_RUN_KEY names, " + keyRunID; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("--run-id %s: %v (exit %d), want %q", other, err, cmd.ExitCode(err), want)
	}
	lacks(t, out, key[4:40])
	if _, n := gw.received(); n != before {
		t.Error("a run whose id is not the key's reached the gateway")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("runs that never reached their gateway left records %v", ids)
	}
}

// TestARunKeyRefusesWhatItsRunCannotHave is QORY_RUN_KEY beside what a run whose
// starter registered it cannot have: --local, --relabel and --policy, an access key on this
// machine, by its file, a variable or the descriptor, a gateway section in forager.yaml,
// and the flags of the run's registration. Each is refused as input, before the
// gateway's address is reached and before anything is recorded; the access key's file
// before the refusal of an unwalled run on a machine that holds an access key.
func TestARunKeyRefusesWhatItsRunCannotHave(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	registered := func(flags string) string {
		return flags + " into the run's registration, and a run key is given, by QORY_RUN_KEY or --run-key-fd: its starter registered the run already, so give "
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) []string
		want  func() string
	}{
		{"--local", func(*testing.T) []string { return []string{"run", "--local"} }, func() string {
			return "--local runs with a gateway the run starts itself and no server, and a run key is given, by QORY_RUN_KEY or --run-key-fd: the run goes through the gateway its run key names; give no run key to run --local"
		}},
		{"--relabel", func(*testing.T) []string { return []string{"run", "--relabel"} }, func() string {
			return "--relabel keeps a checkout's new labels, and a run key is given, by QORY_RUN_KEY or --run-key-fd: the run's starter chose its labels; leave out --relabel"
		}},
		{"--relabel and --local", func(*testing.T) []string { return []string{"run", "--relabel", "--local"} }, func() string {
			return "--local runs with a gateway the run starts itself and no server, and a run key is given, by QORY_RUN_KEY or --run-key-fd: the run goes through the gateway its run key names; give no run key to run --local"
		}},
		{"--policy", func(t *testing.T) []string {
			writeFile(t, filepath.Join(root, "policy.yaml"), "egress:\n  mode: observe\n")
			return []string{"run", "--policy", filepath.Join(root, "policy.yaml")}
		}, func() string {
			return "--policy is a run's policy file for a gateway the run starts itself, and a run key is given, by QORY_RUN_KEY or --run-key-fd: the gateway its run key names takes the run's policy from the server"
		}},
		{"an access key's file", func(t *testing.T) []string {
			writeSecret(t, newKey(t))
			return []string{"run"}
		}, func() string {
			return configDir().Path(foragerdir.SecretFile) + " exists, and a run key is given, by QORY_RUN_KEY or --run-key-fd: a machine whose runs a starter registers holds no access key; remove the file"
		}},
		{"QORY_ACCESS_KEY_SECRET", func(t *testing.T) []string {
			t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
			return []string{"run"}
		}, func() string {
			return "QORY_ACCESS_KEY_SECRET is set, and a run key is given, by QORY_RUN_KEY or --run-key-fd: a machine whose runs a starter registers holds no access key; unset QORY_ACCESS_KEY_SECRET"
		}},
		{"QORY_ACCESS_KEY_ID", func(t *testing.T) []string {
			t.Setenv("QORY_ACCESS_KEY_ID", testAccessKey)
			return []string{"run"}
		}, func() string {
			return "QORY_ACCESS_KEY_ID is set, and a run key is given, by QORY_RUN_KEY or --run-key-fd: a machine whose runs a starter registers holds no access key; unset QORY_ACCESS_KEY_ID"
		}},
		{"--access-key-secret-fd", func(t *testing.T) []string {
			return []string{"run", "--access-key-secret-fd", descriptor(t, newKey(t).Secret())}
		}, func() string {
			return "--access-key-secret-fd and a run key are both given: a machine whose runs a starter registers holds no access key; leave out --access-key-secret-fd"
		}},
		{"a gateway section", func(t *testing.T) []string {
			writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 127.0.0.1:8443\n")
			return []string{"run"}
		}, func() string {
			return file + ": gateway: a run key is given, by QORY_RUN_KEY or --run-key-fd, so the run goes through the gateway its run key names, which holds the server, the policy, the credentials and the tools, and this machine runs no gateway: remove the gateway section, or give no run key"
		}},
		{"a gateway section of tools", func(t *testing.T) []string {
			writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  tools:\n    files:\n      command: [/opt/tools/files]\n      serves: [files.tools.internal]\n")
			return []string{"run"}
		}, func() string {
			return file + ": gateway: a run key is given, by QORY_RUN_KEY or --run-key-fd, so the run goes through the gateway its run key names, which holds the server, the policy, the credentials and the tools, and this machine runs no gateway: remove the gateway section, or give no run key"
		}},
		{"--label", func(*testing.T) []string { return []string{"run", "--label", "team=a"} }, func() string {
			return registered("--label goes") + "it to qory run register"
		}},
		{"--label and --title", func(*testing.T) []string { return []string{"run", "--label", "team=a", "--title", "t"} }, func() string {
			return registered("--label and --title go") + "them to qory run register"
		}},
		{"every flag of the registration", func(*testing.T) []string {
			return []string{"run", "--details", "d=1", "--subject", "s", "--kind", "k", "--title", "t", "--label", "team=a"}
		}, func() string {
			return registered("--label, --kind, --title, --subject and --details go") + "them to qory run register"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, by := range []string{"QORY_RUN_KEY", "--run-key-fd"} {
				os.Remove(file)
				os.Remove(configDir().Path(foragerdir.SecretFile))
				args := c.setup(t)
				t.Setenv("QORY_RUN_KEY", key)
				if by == "--run-key-fd" {
					t.Setenv("QORY_RUN_KEY", "")
					args = append(args, "--run-key-fd", descriptor(t, key))
				}
				_, before := gw.received()
				out, err := run(t, args...)
				if want := c.want(); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
					t.Errorf("by %s: %v (exit %d)\nwant %q", by, err, cmd.ExitCode(err), want)
				}
				lacks(t, out, key[4:40], "hello from", "through the gateway")
				if _, n := gw.received(); n != before {
					t.Error("a refused run reached the gateway")
				}
			}
		})
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// behindRecord writes the record a session behind a separate gateway leaves of the run
// id under the checkout root: its mark, one event of session.jsonl and, when opened,
// an empty delivered.log, so the gateway accepted none of it. It returns the run's
// directory.
func behindRecord(t *testing.T, root, id string, opened bool) string {
	t.Helper()
	dir := filepath.Join(runsDir(t, root), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, session.GatewayMark), "")
	writeFile(t, filepath.Join(dir, sink.SessionFile), `{"specversion":"1.0","type":"dev.qory.run.started","id":"e1","source":"forager","sequence":"1","data":{}}`+"\n")
	if opened {
		writeFile(t, filepath.Join(dir, sink.DeliveredFile), "")
	}
	return dir
}

// TestResendGoesWhereTheRunWent is qory run resend by where the run's record says it
// went. A record made behind a separate gateway goes to that gateway with QORY_RUN_KEY:
// without it, Forager says the run key is needed; with another run's, Forager refuses
// it; with the run's, the session goes to the gateway's address, and the key reaches
// nothing else; a run that never opened there has nothing to send, with a key or
// without. A run that had a gateway of its own goes to the server: with QORY_RUN_KEY
// set it is refused, and a run not recorded is said to be so.
func TestResendGoesWhereTheRunWent(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	dir := behindRecord(t, root, keyRunID, true)

	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "resend", keyRunID)
	if want := "resend " + dir + ": the run key is needed to send to its gateway; set QORY_RUN_KEY"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("without a run key: %v (exit %d)\nwant %q\n%s", err, cmd.ExitCode(err), want, out)
	}

	const other = "0192f0c1-7d4e-7a2b-8c3d-000000000000"
	t.Setenv("QORY_RUN_KEY", mintRunKey(t, other, gw.url()))
	out, err = run(t, "run", "resend", keyRunID)
	if want := "resend " + dir + ": QORY_RUN_KEY names the run " + other + ", not this one"; err == nil || err.Error() != want {
		t.Errorf("another run's key: %v\nwant %q\n%s", err, want, out)
	}
	if _, n := gw.received(); n != 0 {
		t.Error("a resend reached the gateway without the run's key")
	}

	t.Setenv("QORY_RUN_KEY", key)
	out, err = run(t, "run", "resend", keyRunID)
	if err == nil || cmd.ExitCode(err) == 0 {
		t.Fatalf("the resend went on without its gateway: %v\n%s", err, out)
	}
	got, n := gw.received()
	if n == 0 {
		t.Errorf("the resend did not reach the gateway's address: %v\n%s", err, out)
	}
	if bytes.Contains(got, []byte(key[4:40])) {
		t.Error("the run key reached the gateway's address before its TLS")
	}
	lacks(t, out+err.Error(), key[4:40])

	notOpened := "0192f0c1-7d4e-7a2b-8c3d-111111111111"
	behindRecord(t, root, notOpened, false)
	for _, k := range []string{"", key} {
		t.Setenv("QORY_RUN_KEY", k)
		out, err = run(t, "run", "resend", notOpened)
		if err != nil {
			t.Errorf("a run that never opened, key %v: %v\n%s", k != "", err, out)
		}
		wants(t, out, "the gateway never opened run "+notOpened+", so there is nothing to send; its record stays in ")
	}

	t.Setenv("QORY_RUN_KEY", "")
	out, err = run(t, "run", "--local")
	if err != nil {
		t.Fatalf("the local run: %v\n%s", err, out)
	}
	var own string
	for _, id := range recorded(t, root) {
		if id != keyRunID && id != notOpened {
			own = id
		}
	}
	if behind, err := session.BehindGateway(filepath.Join(runsDir(t, root), own)); err != nil || behind {
		t.Fatalf("the local run's record reads as behind a gateway: %v %v", behind, err)
	}
	t.Setenv("QORY_RUN_KEY", key)
	_, before := gw.received()
	out, err = run(t, "run", "resend", own)
	if want := "the run " + own + " started its gateway itself on this machine, so its record goes to the server, not to a gateway: resend it without a run key"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a run with its own gateway: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	const missing = "0192f0c1-7d4e-7a2b-8c3d-222222222222"
	t.Setenv("QORY_RUN_KEY", key)
	out2, err := run(t, "run", "resend", missing)
	if want := "no run " + missing + " is recorded in this checkout"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a run not recorded: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if _, n := gw.received(); n != before {
		t.Error("a resend of a run with its own gateway reached the separate gateway")
	}
	lacks(t, out+out2, key[4:40])

	t.Setenv("QORY_RUN_KEY", "")
	_, err = run(t, "run", "resend", own)
	if want := "forager.yaml defines no server to send the record to"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a run with its own gateway, without QORY_RUN_KEY: %v, want today's %q", err, want)
	}
}

// TestTheRunKeyIsInNoOutputOrFile is the run key through qory run, unwalled and walled,
// qory run resend and qory config: it is never in their output, in a file under the
// checkout, qory's state directory or its configuration directory, nor in what reached
// the gateway's address, and qory's environment no longer holds it.
func TestTheRunKeyIsInNoOutputOrFile(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	const id = "0192f0c1-7d4e-7a2b-8c3d-333333333333"
	key := mintRunKey(t, id, gw.url())
	behindRecord(t, root, id, true)
	docker, _ := fakeDocker(t)
	var all string
	for _, args := range [][]string{
		{"run"},
		{"run", "--wall", "docker", "--image", "example.com/agent:1"},
		{"run", "resend", id},
		{"config"},
	} {
		if len(args) > 1 && args[1] == "--wall" {
			writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
		}
		t.Setenv("QORY_RUN_KEY", key)
		out, err := run(t, args...)
		all += out
		if err != nil {
			all += err.Error()
		}
		if _, ok := os.LookupEnv("QORY_RUN_KEY"); ok {
			t.Errorf("%v: QORY_RUN_KEY stayed in qory's environment", args)
		}
	}
	// The key's payload and its signature are each the key's.
	payload, signature, _ := strings.Cut(strings.TrimPrefix(key, runkey.Prefix), ".")
	got, _ := gw.received()
	for _, part := range []string{key, payload, signature} {
		lacks(t, all, part)
		noValueUnder(t, root, part)
		noValueUnder(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), part)
		noValueUnder(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory"), part)
		if bytes.Contains(got, []byte(part)) {
			t.Error("the run key reached the gateway's address before its TLS")
		}
	}
}

// TestTheRunKeyIsNoAgentsVariable is QORY_RUN_KEY named for the agent: --env and
// wall.env refuse it with the words they refuse the access key's secret with, with a
// run key set or without, and neither refusal holds the value.
func TestTheRunKeyIsNoAgentsVariable(t *testing.T) {
	root := runKeyCheckout(t)
	key := mintRunKey(t, keyRunID, newKeyedGateway(t).url())
	for _, name := range []string{"QORY_ACCESS_KEY_SECRET", "QORY_RUN_KEY"} {
		for _, k := range []string{"", key} {
			t.Setenv("QORY_RUN_KEY", k)
			out, err := run(t, "run", "--env", name)
			if want := "--env " + name + ": the variable is Forager's and never the agent's"; cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
				t.Errorf("--env %s, key %v: %v (exit %d), want %q", name, k != "", err, cmd.ExitCode(err), want)
			}
			lacks(t, out, key[4:40], "hello from")
		}
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	for _, name := range []string{"QORY_ACCESS_KEY_SECRET", "QORY_RUN_KEY"} {
		writeFile(t, file, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  helper: "+staticELF(t)+"\n  env: ["+name+"]\n")
		for _, k := range []string{"", key} {
			t.Setenv("QORY_RUN_KEY", k)
			out, err := run(t, "run")
			if want := file + ": wall.env[0] names a variable that is Forager's and never the agent's"; cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
				t.Errorf("wall.env [%s], key %v: %v (exit %d), want %q", name, k != "", err, cmd.ExitCode(err), want)
			}
			lacks(t, out, key[4:40], "hello from")
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
}

// TestRunRefusesAGatewaysAccessKey is qory run under an access key whose node,
// discovery says, is a gateway's: it is refused in qory's words, after the node line and
// before the run registers, and the agent never starts.
func TestRunRefusesAGatewaysAccessKey(t *testing.T) {
	_, srv := serverRun(t, "", "")
	srv.node = testGatewayNode
	out, err := run(t, "run")
	if want := "this machine's gateway serves other machines and runs no sessions; give this machine a separate node"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q\n%s", err, want, out)
	}
	if cmd.ExitCode(err) == 0 {
		t.Error("exit 0")
	}
	wants(t, out, "qory run: node "+testGatewayNode+", instance i_")
	lacks(t, out, "hello from")
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.registered) != 0 {
		t.Errorf("the run registered: %v", srv.registered)
	}
}

// TestARunKeyFromADescriptorIsReadOnce is --run-key-fd on qory run and qory run resend:
// the standard output and error are refused, so are a descriptor that is not open, more
// than a run key, a run key given both ways, and one that does not read as a run key,
// naming the part that is wrong and never the value; each as input, before anything
// starts, and the descriptor is closed.
func TestARunKeyFromADescriptorIsReadOnce(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	const marker = "run-key-marker-5be1a7c3"
	for _, verb := range [][]string{{"run"}, {"run", "resend", keyRunID}} {
		for _, c := range []struct {
			name, env string
			fd        func() string
			want      func(fd string) string
		}{
			{"stdout", "", func() string { return "1" }, func(string) string {
				return "--run-key-fd 1: the standard output and error carry no run key; name 0 for stdin, or a descriptor of 3 or above"
			}},
			{"stderr", "", func() string { return "2" }, func(string) string {
				return "--run-key-fd 2: the standard output and error carry no run key; name 0 for stdin, or a descriptor of 3 or above"
			}},
			{"not open", "", func() string { return "1000" }, func(string) string {
				return "--run-key-fd 1000: read --run-key-fd: bad file descriptor"
			}},
			{"more than a run key", "", func() string { return descriptor(t, strings.Repeat(marker, 400)) }, func(fd string) string {
				return "--run-key-fd " + fd + ": more than a run key"
			}},
			{"both ways", key, func() string { return descriptor(t, key) }, func(string) string {
				return "--run-key-fd and QORY_RUN_KEY both give a run key; give it one way"
			}},
			{"not a run key", "", func() string { return descriptor(t, " qrk_"+marker+"\n") }, func(fd string) string {
				return "--run-key-fd " + fd + " gives no run key: it has no dot between its payload and its signature"
			}},
		} {
			t.Setenv("QORY_RUN_KEY", c.env)
			fd := c.fd()
			out, err := run(t, append(append([]string{}, verb...), "--run-key-fd", fd)...)
			if want := c.want(fd); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v %s: %v (exit %d)\nwant %q", verb, c.name, err, cmd.ExitCode(err), want)
			}
			lacks(t, out, marker, key[4:40], "hello from")
			if n, _ := strconv.Atoi(fd); n > 2 && n != 1000 {
				var st syscall.Stat_t
				if syscall.Fstat(n, &st) == nil {
					t.Errorf("%v %s: the descriptor %d is still open", verb, c.name, n)
				}
			}
		}
	}
	if _, n := gw.received(); n != 0 {
		t.Error("a refused run key reached the gateway")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// TestARunKeyInTheEnvironmentNeedsAWall is a run key in QORY_RUN_KEY, which an unwalled agent can
// read on macOS, and on Linux from the program that started qory: an unwalled run is
// refused, run_key_needs_wall, as input, before anything starts; a walled one goes on to
// the gateway, and so does an unwalled run whose run key came by --run-key-fd.
func TestARunKeyInTheEnvironmentNeedsAWall(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", key)
	out, err := run(t, "run")
	if want := "QORY_RUN_KEY is in the environment qory started with, which an unwalled agent can read (on macOS always, on Linux through the program that started qory), so a run with it needs a wall: --wall docker, or wall in forager.yaml; or give the run key with --run-key-fd, such as --run-key-fd 0 for stdin (run_key_needs_wall)"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("unwalled: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	lacks(t, out, key[4:40], "through the gateway", "hello from")
	if _, n := gw.received(); n != 0 {
		t.Error("the unwalled run reached the gateway")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the refused run left records %v", ids)
	}

	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	// Behind a wall, a run given a run key needs its harness outside the checkout.
	outside := t.TempDir()
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("QORY_RUN_KEY", key)
	out, err = run(t, "run", "--home", outside, "--image", "example.com/agent:1")
	if err != nil && strings.Contains(err.Error(), "run_key_needs_wall") {
		t.Errorf("walled: %v", err)
	}
	wants(t, out, "qory run: through the gateway "+gw.host()+", run "+keyRunID)
	lacks(t, out, key[4:40])
}

// TestARunKeyStillNeedsAWallBesideTheMarker is a run key in QORY_RUN_KEY on a machine
// that has the stored-secrets marker and no access key: an unwalled run is still refused,
// run_key_needs_wall, as input, before anything starts.
func TestARunKeyStillNeedsAWallBesideTheMarker(t *testing.T) {
	root := runKeyCheckout(t)
	dir := configDir()
	if _, err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := dir.WriteMarker(); err != nil {
		t.Fatal(err)
	}
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", key)
	out, err := run(t, "run")
	if err == nil || !strings.HasSuffix(err.Error(), "(run_key_needs_wall)") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("unwalled beside the marker: %v (exit %d)", err, cmd.ExitCode(err))
	}
	lacks(t, out, key[4:40], "hello from")
	if _, n := gw.received(); n != 0 {
		t.Error("the unwalled run reached the gateway")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the refused run left records %v", ids)
	}
}

// TestResendTakesTheRunKeyFromADescriptor is qory run resend with --run-key-fd: the
// record goes to the gateway the key names, as with QORY_RUN_KEY, and the key reaches
// nothing before its TLS.
func TestResendTakesTheRunKeyFromADescriptor(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	behindRecord(t, root, keyRunID, true)
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "resend", keyRunID, "--run-key-fd", descriptor(t, key+"\n"))
	if err == nil || cmd.ExitCode(err) == 0 {
		t.Fatalf("the resend went on without its gateway: %v\n%s", err, out)
	}
	got, n := gw.received()
	if n == 0 {
		t.Errorf("the resend did not reach the gateway's address: %v\n%s", err, out)
	}
	if bytes.Contains(got, []byte(key[4:40])) {
		t.Error("the run key reached the gateway's address before its TLS")
	}
	lacks(t, out+err.Error(), key[4:40])
}
