package cmd_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
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

// issuerSecret is what the introspection client's secret file holds in the tests: qory
// config must never print it.
const issuerSecret = "example-introspection-secret-value"

// runCredentials is a gateway.run_credentials section, indented to continue the gateway
// section, whose files are relative to forager.yaml's directory, where
// writeIssuerFiles writes them.
const runCredentials = `  run_credentials:
    - issuer: https://issuer.example
      audience: qory-gateway
      algorithms: [EdDSA]
      keys:
        - {kid: k1, alg: EdDSA, public_key_file: issuer-k1.pem}
      allow: {claim: namespace, values: [example-namespace]}
      labels:
        forge: {value: example-forge}
        repository: {claims: [namespace, project], join: "/"}
        run_key: {claim: sub}
      details:
        requester: {claim: requester}
      introspection:
        url: https://issuer.example/introspect
        client_id: example-gateway
        client_secret_file: issuer-introspection-secret
`

// writeIssuerFiles writes the issuer's public key and the introspection client's secret
// beside forager.yaml, and returns the key's PEM.
func writeIssuerFiles(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	dir := string(configDir())
	writeFile(t, filepath.Join(dir, "issuer-k1.pem"), key)
	secret := filepath.Join(dir, "issuer-introspection-secret")
	writeFile(t, secret, issuerSecret+"\n")
	if err := os.Chmod(secret, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

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
				serverFile(t, newFakeServer(t, ""), runCredentials)
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.listen is required to run the gateway as a service: the address the other machines reach, such as 0.0.0.0:8443; or --listen"
			},
		},
		{
			name: "a listen that is not host:port",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: gateway.example\n"+runCredentials)
				return []string{"gateway"}
			},
			want: func() string {
				return file() + `: gateway.listen "gateway.example" is not host:port, such as 0.0.0.0:8443`
			},
		},
		{
			name: "a --listen that is not host:port",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+runCredentials)
				return []string{"gateway", "--listen", "127.0.0.1"}
			},
			want: func() string { return `--listen "127.0.0.1" is not host:port, such as 0.0.0.0:8443` },
		},
		{
			name: "no server",
			setup: func(t *testing.T) []string {
				writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 127.0.0.1:0\n"+runCredentials)
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.server is required to run the gateway as a service: it reports every run to Qory Apiary and takes the runs' policies from it"
			},
		},
		{
			name: "no tls off loopback",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:8443\n"+runCredentials)
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.tls is required with a gateway.listen other machines reach: a run's events carry its prompts and terminal output; set gateway.tls.certificate and gateway.tls.key"
			},
		},
		{
			name: "no tls off loopback, by --listen",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n"+runCredentials)
				return []string{"gateway", "--listen", ":8443"}
			},
			want: func() string {
				return file() + ": gateway.tls is required with a gateway.listen other machines reach: a run's events carry its prompts and terminal output; set gateway.tls.certificate and gateway.tls.key"
			},
		},
		{
			name: "no run credentials",
			setup: func(t *testing.T) []string {
				serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n")
				return []string{"gateway"}
			},
			want: func() string {
				return file() + ": gateway.run_credentials is required to serve other machines: their runs bring run credentials, and the gateway verifies each one; see https://github.com/qoryai/qory/blob/main/docs/gateway.md"
			},
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
	serverFile(t, newFakeServer(t, ""), "  listen: "+addr+"\n"+runCredentials)
	writeIssuerFiles(t)
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

// TestGatewayServesLoopbackUntilSIGTERM is a gateway on a loopback address without
// TLS: --listen wins over gateway.listen, which names an address that would need TLS;
// the gateway prints the node, its instance and where it listens, and on SIGTERM it
// says it stops, says it stopped, and exits 0.
func TestGatewayServesLoopbackUntilSIGTERM(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:8443\n"+runCredentials)
	writeIssuerFiles(t)
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
	wants(t, text, "qory gateway: node "+testNode+", instance i_", "qory gateway: listening on "+addr, "qory gateway: stopping\n", "the gateway stopped\n")
	if i, j := strings.Index(text, "qory gateway: stopping"), strings.Index(text, "the gateway stopped"); i < 0 || j < i {
		t.Errorf("the stopping lines are out of order:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "gateway", "authority", "ca.pem")); err != nil {
		t.Errorf("the gateway's certificate authority is not in qory's state directory: %v", err)
	}
}

// TestGatewayServesTLSOffLoopback is a gateway on every address of the machine, with
// gateway.tls naming the certificate and key relative to forager.yaml's directory, the
// key a link to a file of the user's alone in a directory of theirs alone, as a
// certificate tool keeps one: it speaks TLS 1.3 with that certificate.
func TestGatewayServesTLSOffLoopback(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:0\n  tls:\n    certificate: gateway.pem\n    key: gateway-key.pem\n"+runCredentials)
	writeIssuerFiles(t)
	writeCertificate(t)
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
}

// TestConfigListsTheGatewaysSettings is qory config with the gateway's address, its TLS
// and its run credentials: every path as the file writes it, the defaults beside them,
// and nothing a key or secret file holds.
func TestConfigListsTheGatewaysSettings(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 0.0.0.0:8443\n  tls:\n    certificate: gateway.pem\n    key: gateway-key.pem\n"+runCredentials)
	issuerKey := writeIssuerFiles(t)
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
		{"gateway.run_credentials[0].issuer", "https://issuer.example"},
		{"gateway.run_credentials[0].audience", "qory-gateway"},
		{"gateway.run_credentials[0].algorithms", "[EdDSA]"},
		{"gateway.run_credentials[0].keys", "[{kid: k1, alg: EdDSA, public_key_file: issuer-k1.pem}]"},
		{"gateway.run_credentials[0].allow", "{claim: namespace, values: [example-namespace]}"},
		{"gateway.run_credentials[0].labels", `{forge: {value: example-forge}, repository: {claims: [namespace, project], join: "/"}, run_key: {claim: sub}}`},
		{"gateway.run_credentials[0].details", "{requester: {claim: requester}}"},
		{"gateway.run_credentials[0].introspection.url", "https://issuer.example/introspect"},
		{"gateway.run_credentials[0].introspection.client_id", "example-gateway"},
		{"gateway.run_credentials[0].introspection.client_secret_file", "issuer-introspection-secret"},
	} {
		wantsConfigRow(t, rows, r[0], r[1], file)
	}
	wantsConfigRow(t, rows, "gateway.run_credentials[0].leeway", "60s", "default")
	wantsConfigRow(t, rows, "gateway.run_credentials[0].max_lifetime", "(none)", "default")
	wantsConfigRow(t, rows, "gateway.run_credentials[0].introspection.cache", "30s", "default")
	if t.Failed() {
		t.Log(out)
	}
	lacks(t, out, issuerSecret, "BEGIN", strings.TrimSpace(strings.Split(issuerKey, "\n")[1]), strings.TrimSpace(strings.Split(tlsKey, "\n")[1]))
}

// TestConfigListsNoGatewayService is qory config without the gateway's address, TLS
// or run credentials: each is listed as none.
func TestConfigListsNoGatewayService(t *testing.T) {
	emptyDir(t)
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rows := configRows(out)
	for _, key := range []string{"gateway.listen", "gateway.tls.certificate", "gateway.tls.key", "gateway.run_credentials"} {
		wantsConfigRow(t, rows, key, "(none)", "default")
	}
}

// TestGatewayRefusesRunCredentialsTheSchemaRefuses is a gateway.run_credentials that
// Forager's schema refuses: every command that reads forager.yaml stops on it.
func TestGatewayRefusesRunCredentialsTheSchemaRefuses(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n  run_credentials:\n    - issuer: https://issuer.example\n      algorithms: [HS256]\n")
	_, err := run(t, "gateway")
	if err == nil || !strings.HasPrefix(err.Error(), foragerFile()+": gateway.run_credentials: ") {
		t.Errorf("error %v, want one that names gateway.run_credentials", err)
	}
	if code := cmd.ExitCode(err); code != cmd.ExitInput {
		t.Errorf("exit %d, want %d", code, cmd.ExitInput)
	}
}

// TestGatewayRefusesSecretFilesOthersMayRead is the TLS key and an introspection
// client's secret checked as access-key-secret is, before the gateway listens: a key
// the group or others may read, a key in a directory others may read, and a client
// secret that is a link, here to a file anyone may write, are each refused, and no
// refusal holds what the file does.
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
		{"a client secret linked to a file anyone may write", func(t *testing.T, dir string) string {
			secret := filepath.Join(dir, "issuer-introspection-secret")
			elsewhere := filepath.Join(t.TempDir(), "secret")
			writeFile(t, elsewhere, issuerSecret+"\n")
			if err := os.Chmod(elsewhere, 0o666); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(secret); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, secret); err != nil {
				t.Fatal(err)
			}
			return secret + " is a symbolic link; it must be the file itself"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			emptyDir(t)
			serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n  tls:\n    certificate: gateway.pem\n    key: gateway-key.pem\n"+runCredentials)
			writeIssuerFiles(t)
			key := writeCertificate(t)
			want := c.setup(t, string(configDir()))
			out, err := run(t, "gateway")
			if err == nil || err.Error() != want {
				t.Errorf("error\n got %v\nwant %q", err, want)
			}
			if code := cmd.ExitCode(err); code != cmd.ExitInput {
				t.Errorf("exit %d, want %d", code, cmd.ExitInput)
			}
			lacks(t, out+fmt.Sprint(err), "listening on", issuerSecret, strings.Split(key, "\n")[1])
		})
	}
}
