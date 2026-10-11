package cmd_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// newGatewayServer starts a server, as newFakeServer does, whose discovery names a
// gateway's node: the access key it knows is a gateway's.
func newGatewayServer(t *testing.T) *fakeServer {
	t.Helper()
	srv := newFakeServer(t, "")
	srv.node = testGatewayNode
	return srv
}

// gatewayTLS is gateway.tls naming gateway.pem and gateway-key.pem, which
// writeCertificate writes, indented to continue the gateway section.
const gatewayTLS = "  tls:\n    certificate: gateway.pem\n    key: gateway-key.pem\n"

// writeCertificate writes a self-signed certificate for 127.0.0.1 and its key beside
// forager.yaml, as gateway.pem and gateway-key.pem, and returns the key's PEM.
func writeCertificate(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gateway.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"gateway.example"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	dir := string(configDir())
	writeFile(t, filepath.Join(dir, "gateway.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	writeFile(t, filepath.Join(dir, "gateway-key.pem"), key)
	if err := os.Chmod(filepath.Join(dir, "gateway-key.pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

// configRows reads qory config's table: each key to its value and its origin.
func configRows(out string) map[string][2]string {
	rows := map[string][2]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := columns.Split(strings.TrimSpace(line), -1); len(f) == 3 {
			rows[f[0]] = [2]string{f[1], f[2]}
		}
	}
	return rows
}

// wantsConfigRow fails the test when qory config's table does not list key with value
// and origin.
func wantsConfigRow(t *testing.T, rows map[string][2]string, key, value, origin string) {
	t.Helper()
	if got, want := rows[key], [2]string{value, origin}; got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

// TestGatewayRefusesWhatItCannotServe is every refusal of qory gateway before it
// listens, each in its exact words and an input error.
func TestGatewayRefusesWhatItCannotServe(t *testing.T) {
	file := func() string { return foragerFile() }
	cases := []struct {
		name  string
		setup func(t *testing.T) []string
		want  func() string
	}{
		{
			name:  "no forager.yaml",
			setup: func(*testing.T) []string { return []string{"gateway"} },
			want: func() string {
				return "~/.config/qory/forager.yaml has no gateway section, so there is no gateway to run; see https://github.com/qoryai/qory/blob/main/docs/gateway.md"
			},
		},
		{
			name: "no gateway section",
			setup: func(t *testing.T) []string {
				writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\nsession:\n  instance:\n    name: build-01\n")
				return []string{"gateway", "--listen", "127.0.0.1:0"}
			},
			want: func() string {
				return "~/.config/qory/forager.yaml has no gateway section, so there is no gateway to run; see https://github.com/qoryai/qory/blob/main/docs/gateway.md"
			},
		},
		{
			name: "no listen",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "")
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.listen is required to run the gateway as a service: the address the other machines reach, such as 0.0.0.0:8443; or --listen"
			},
		},
		{
			name: "a listen that is not host:port",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: gateway.example\n")
				return []string{"gateway"}
			},
			want: func() string {
				return file() + `: gateway.listen is not host:port, such as 0.0.0.0:8443`
			},
		},
		{
			name: "a --listen that is not host:port",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n")
				return []string{"gateway", "--listen", "127.0.0.1"}
			},
			want: func() string { return `--listen "127.0.0.1" is not host:port, such as 0.0.0.0:8443` },
		},
		{
			name: "no server",
			setup: func(t *testing.T) []string {
				writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 127.0.0.1:0\n")
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.server is required to run the gateway as a service: it reports every run to Qory Apiary and takes the runs' policies from it"
			},
		},
		{
			name: "no tls off loopback",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:8443\n")
				return []string{"gateway"}
			},
			want: func() string { return "gateway.tls is required with gateway.listen: a gateway serves other machines" },
		},
		{
			name: "no tls on loopback",
			setup: func(t *testing.T) []string {
				serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n")
				return []string{"gateway"}
			},
			want: func() string { return "gateway.tls is required with gateway.listen: a gateway serves other machines" },
		},
		{
			name: "no tls off loopback, by --listen",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n")
				return []string{"gateway", "--listen", ":8443"}
			},
			want: func() string { return "gateway.tls is required with gateway.listen: a gateway serves other machines" },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			emptyDir(t)
			out, err := run(t, c.setup(t)...)
			if err == nil {
				t.Fatalf("qory gateway did not refuse:\n%s", out)
			}
			if got, want := err.Error(), c.want(); got != want {
				t.Errorf("error\n got %q\nwant %q", got, want)
			}
			if code := cmd.ExitCode(err); code != cmd.ExitInput {
				t.Errorf("exit %d, want %d", code, cmd.ExitInput)
			}
			lacks(t, out, "listening on")
		})
	}
}

// TestGatewaySaysWhereItCannotListen is a gateway whose address is taken: it says
// where, after the server's discovery, before it serves anything.
func TestGatewaySaysWhereItCannotListen(t *testing.T) {
	emptyDir(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	addr := taken.Addr().String()
	serverFile(t, newGatewayServer(t), "  listen: "+addr+"\n"+gatewayTLS)
	writeCertificate(t)
	out, err := run(t, "gateway")
	if err == nil {
		t.Fatalf("qory gateway listened on a taken address:\n%s", out)
	}
	if !strings.HasPrefix(err.Error(), "listen on "+addr+": ") {
		t.Errorf("error %q does not start with %q", err, "listen on "+addr+": ")
	}
	lacks(t, out, "listening on")
}

// syncBuffer is a buffer a command writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// listeningOn matches the line qory gateway prints once it serves, with the address.
var listeningOn = regexp.MustCompile(`qory gateway: listening on (\S+)`)

// startGateway runs qory gateway with args in the background, waits for the line that
// says where it listens, and returns that address, its output and what its end returns.
// It fails the test when the gateway ends first.
func startGateway(t *testing.T, args ...string) (string, *syncBuffer, <-chan error) {
	t.Helper()
	out := &syncBuffer{}
	root := cmd.Root()
	root.SetArgs(append([]string{"gateway"}, args...))
	root.SetOut(out)
	root.SetErr(out)
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	deadline := time.After(30 * time.Second)
	for {
		if m := listeningOn.FindStringSubmatch(out.String()); m != nil {
			return m[1], out, done
		}
		select {
		case err := <-done:
			t.Fatalf("qory gateway ended before it listened: %v\n%s", err, out)
		case <-deadline:
			t.Fatalf("qory gateway did not listen:\n%s", out)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// stopGateway sends this process SIGTERM, which qory gateway is waiting for, and
// returns what its end returned.
func stopGateway(t *testing.T, done <-chan error) error {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("qory gateway did not stop on SIGTERM")
		return nil
	}
}

// TestGatewayServesLoopbackUntilSIGTERM is a gateway on a loopback address: --listen
// wins over gateway.listen; the gateway prints the node, a gateway's, its instance and
// where it listens, and on SIGTERM it says it stops, says it stopped, and exits 0.
func TestGatewayServesLoopbackUntilSIGTERM(t *testing.T) {
	emptyDir(t)
	srv := newGatewayServer(t)
	serverFile(t, srv, "  listen: 0.0.0.0:8443\n"+gatewayTLS)
	writeCertificate(t)
	addr, out, done := startGateway(t, "--listen", "127.0.0.1:0")
	if host, port, _ := net.SplitHostPort(addr); host != "127.0.0.1" || port == "0" || port == "8443" {
		t.Errorf("listening on %s, want 127.0.0.1 and the port the system chose", addr)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("the gateway's address does not answer: %v", err)
	}
	conn.Close()
	err = stopGateway(t, done)
	if err != nil {
		t.Fatalf("qory gateway ended with %v, want nil\n%s", err, out)
	}
	if code := cmd.ExitCode(err); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	text := out.String()
	wants(t, text, "qory gateway: node "+testGatewayNode+", instance i_", "qory gateway: listening on "+addr, "qory gateway: stopping\n", "the gateway stopped\n")
	if i, j := strings.Index(text, "qory gateway: stopping"), strings.Index(text, "the gateway stopped"); i < 0 || j < i {
		t.Errorf("the stopping lines are out of order:\n%s", text)
	}
	lacks(t, text, srv.key.Secret()[4:])
	if fi, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "gateway")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the gateway's directory is not in qory's state directory, the user's alone: %v", err)
	}
}

// TestGatewayServesTLSOffLoopback is a gateway on every address of the machine, with
// gateway.tls naming the certificate and key relative to forager.yaml's directory, the
// key a link to a file of the user's alone in a directory of theirs alone, as a
// certificate tool keeps one: it speaks TLS 1.3 with that certificate.
func TestGatewayServesTLSOffLoopback(t *testing.T) {
	emptyDir(t)
	srv := newGatewayServer(t)
	serverFile(t, srv, "  listen: 0.0.0.0:0\n"+gatewayTLS)
	key := writeCertificate(t)
	dir := string(configDir())
	archive := filepath.Join(dir, "archive")
	if err := os.Mkdir(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "gateway-key.pem"), filepath.Join(archive, "gateway-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("archive", "gateway-key.pem"), filepath.Join(dir, "gateway-key.pem")); err != nil {
		t.Fatal(err)
	}
	addr, out, done := startGateway(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Errorf("no TLS on the gateway's address: %v", err)
	} else {
		state := conn.ConnectionState()
		if state.Version != tls.VersionTLS13 {
			t.Errorf("TLS version %x, want 1.3", state.Version)
		}
		if len(state.PeerCertificates) == 0 || state.PeerCertificates[0].Subject.CommonName != "gateway.example" {
			t.Errorf("the gateway does not serve gateway.tls.certificate")
		}
		conn.Close()
	}
	if err := stopGateway(t, done); err != nil {
		t.Fatalf("qory gateway ended with %v\n%s", err, out)
	}
	lacks(t, out.String(), strings.Split(key, "\n")[1], srv.key.Secret()[4:])
}

// TestConfigListsTheGatewaysSettings is qory config with the gateway's address and its
// TLS: every path as the file writes it, and nothing the key file holds.
func TestConfigListsTheGatewaysSettings(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:8443\n"+gatewayTLS)
	tlsKey := writeCertificate(t)
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file := "~/.config/qory/forager.yaml"
	rows := configRows(out)
	for _, r := range [][2]string{
		{"gateway.listen", "0.0.0.0:8443"},
		{"gateway.tls.certificate", "gateway.pem"},
		{"gateway.tls.key", "gateway-key.pem"},
	} {
		wantsConfigRow(t, rows, r[0], r[1], file)
	}
	if t.Failed() {
		t.Log(out)
	}
	lacks(t, out, "BEGIN", strings.TrimSpace(strings.Split(tlsKey, "\n")[1]))
}

// TestConfigListsNoGatewayService is qory config without the gateway's address or
// TLS: each is listed as none, and gateway.run_credentials is no row.
func TestConfigListsNoGatewayService(t *testing.T) {
	emptyDir(t)
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rows := configRows(out)
	for _, key := range []string{"gateway.listen", "gateway.tls.certificate", "gateway.tls.key"} {
		wantsConfigRow(t, rows, key, "(none)", "default")
	}
	lacks(t, out, "run_credentials")
}

// TestGatewayRefusesRunCredentials is a gateway.run_credentials, which forager.yaml no
// longer has: every command that reads forager.yaml stops on it, as on any key it does
// not read.
func TestGatewayRefusesRunCredentials(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n  run_credentials:\n    - issuer: https://issuer.example\n")
	_, err := run(t, "gateway")
	if want := foragerFile() + `: line 8: key "run_credentials" is not one forager.yaml reads`; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
	if code := cmd.ExitCode(err); code != cmd.ExitInput {
		t.Errorf("exit %d, want %d", code, cmd.ExitInput)
	}
}

// TestGatewayRefusesSecretFilesOthersMayRead is the TLS key checked as
// access-key-secret is, before the gateway listens: a key the group or others may read
// and a key in a directory others may read are each refused, and no refusal holds what
// the file does.
func TestGatewayRefusesSecretFilesOthersMayRead(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string) string
	}{
		{"a key others may read", func(t *testing.T, dir string) string {
			key := filepath.Join(dir, "gateway-key.pem")
			if err := os.Chmod(key, 0o644); err != nil {
				t.Fatal(err)
			}
			key, _ = filepath.EvalSymlinks(key)
			return key + " is mode 0644, which grants access to the group or others: chmod 600 " + key + ", and replace the key if anyone else could read it"
		}},
		{"a key in a directory others may read", func(t *testing.T, dir string) string {
			keys := filepath.Join(dir, "keys")
			if err := os.Mkdir(keys, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(keys, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(dir, "gateway-key.pem"), filepath.Join(keys, "gateway-key.pem")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(keys, "gateway-key.pem"), filepath.Join(dir, "gateway-key.pem")); err != nil {
				t.Fatal(err)
			}
			keys, _ = filepath.EvalSymlinks(keys)
			return keys + " is mode 0755, which grants access to the group or others; it holds gateway.tls.key: chmod 700 " + keys
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			emptyDir(t)
			serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+gatewayTLS)
			key := writeCertificate(t)
			want := c.setup(t, string(configDir()))
			out, err := run(t, "gateway")
			if err == nil || err.Error() != want {
				t.Errorf("error\n got %v\nwant %q", err, want)
			}
			if code := cmd.ExitCode(err); code != cmd.ExitInput {
				t.Errorf("exit %d, want %d", code, cmd.ExitInput)
			}
			lacks(t, out+fmt.Sprint(err), "listening on", strings.Split(key, "\n")[1])
		})
	}
}

// TestGatewayMakesNoLinkSocket lists the system's temporary directory while the gateway
// serves: nothing on this machine uses its local link, so it makes no qory-link-*
// socket there.
func TestGatewayMakesNoLinkSocket(t *testing.T) {
	emptyDir(t)
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)
	_, out, done := startGateway(t)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "qory-link-") {
			t.Errorf("the gateway made %s in %s while it serves", e.Name(), tmp)
		}
	}
	if err := stopGateway(t, done); err != nil {
		t.Fatalf("qory gateway ended with %v\n%s", err, out)
	}
}

// TestGatewayRefusesALinkForItsDirectory is qory's state directory, or the gateway's
// directory in it, a link: the gateway refuses it before it writes anything there, so
// nothing of it lands where the link leads.
func TestGatewayRefusesALinkForItsDirectory(t *testing.T) {
	for _, name := range []string{"qory", filepath.Join("qory", "gateway")} {
		t.Run(name, func(t *testing.T) {
			emptyDir(t)
			serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+gatewayTLS)
			writeCertificate(t)
			link := filepath.Join(os.Getenv("XDG_STATE_HOME"), name)
			elsewhere := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, link); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "gateway")
			if want := link + " is a symbolic link; it must be the directory itself"; err == nil || err.Error() != want {
				t.Errorf("error\n got %v\nwant %q", err, want)
			}
			lacks(t, out, "listening on")
			if entries, err := os.ReadDir(elsewhere); err != nil || len(entries) > 0 {
				t.Errorf("the gateway wrote where the link leads: %v %v", entries, err)
			}
		})
	}
}

// TestGatewayRefusesAnotherUsersDirectory is qory's state directory, or the gateway's
// directory in it, another user's: each is refused, naming which it is. Only root can
// give a directory to another user, so the test needs root.
func TestGatewayRefusesAnotherUsersDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a directory to another user")
	}
	for _, c := range []struct{ name, whose string }{
		{"qory", "qory's state directory"},
		{filepath.Join("qory", "gateway"), "the directory of qory gateway"},
	} {
		t.Run(c.name, func(t *testing.T) {
			emptyDir(t)
			serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+gatewayTLS)
			writeCertificate(t)
			path := filepath.Join(os.Getenv("XDG_STATE_HOME"), c.name)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, 65534, 65534); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "gateway")
			if want := path + " belongs to another user; " + c.whose + " is yours alone"; err == nil || err.Error() != want {
				t.Errorf("error\n got %v\nwant %q", err, want)
			}
			lacks(t, out, "listening on")
		})
	}
}

// TestGatewayRefusesAKeyAnotherUserOwns is gateway.tls.key a file another user owns,
// root's included when qory gateway is not root: it is refused as another user's
// access-key-secret is. Only root can give a file to another user, so the test needs
// root.
func TestGatewayRefusesAKeyAnotherUserOwns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a file to another user")
	}
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)
	key, err := filepath.EvalSymlinks(filepath.Join(string(configDir()), "gateway-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(key, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "gateway")
	if want := key + " belongs to another user; gateway.tls.key is yours alone"; err == nil || err.Error() != want {
		t.Errorf("error\n got %v\nwant %q", err, want)
	}
	lacks(t, out, "listening on")
}

// TestGatewayNeedsAGatewaysKey is qory gateway with an access key whose node discovery
// names is not a gateway's: once the node line is printed, it is refused as input, in
// qory's words, before the gateway listens.
func TestGatewayNeedsAGatewaysKey(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)
	out, err := run(t, "gateway")
	if want := "qory gateway needs a gateway's access key, and this one is node " + testNode + "'s: enrol a key with a code from the gateway's page in Qory Apiary"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a node's key: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	wants(t, out, "qory gateway: node "+testNode+", instance ")
	lacks(t, out, "listening on")
}
