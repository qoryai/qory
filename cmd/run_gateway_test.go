package cmd_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// sessionGateway is a session.gateway section for the gateway at addr, its certificate
// trusted through ca, with more after it, indented to continue the section.
func sessionGateway(addr, ca, more string) string {
	return "apiVersion: qory.dev/v1alpha1\nsession:\n  gateway:\n    url: https://" + addr + "\n    ca_file: " + ca + "\n" + more
}

// gatewayCredentials is gateway.run_credentials for the checkouts newCheckout makes,
// git.example.com and acme/app, with no introspection: the issuer's key is
// issuer-k1.pem beside forager.yaml, and the run credential's requester claim is its
// requester detail.
const gatewayCredentials = `  run_credentials:
    - issuer: https://issuer.example
      audience: qory-gateway
      algorithms: [EdDSA]
      keys:
        - {kid: k1, alg: EdDSA, public_key_file: issuer-k1.pem}
      allow: {claim: namespace, values: [acme]}
      labels:
        forge: {value: git.example.com}
        repository: {claims: [namespace, project], join: "/"}
        run_key: {claim: sub}
      details:
        requester: {claim: requester}
`

// issuer signs run credentials for the gateway gatewayCredentials sets.
type issuer struct{ key ed25519.PrivateKey }

// credential is a run credential the issuer signs, its claims those of a run of acme/app
// with a run key of its own, with what more sets over them; a key whose value is nil is
// left out.
func (i issuer) credential(t *testing.T, more map[string]any) string {
	t.Helper()
	now := time.Now().Unix()
	claims := map[string]any{"iss": "https://issuer.example", "aud": "qory-gateway", "sub": "queue/" + strconv.FormatInt(time.Now().UnixNano(), 10), "namespace": "acme", "project": "app", "requester": "example-requester", "iat": now, "exp": now + 600}
	for k, v := range more {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"alg":"EdDSA","kid":"k1","typ":"JWT"}`)) + "." + enc.EncodeToString(payload)
	return signing + "." + enc.EncodeToString(ed25519.Sign(i.key, []byte(signing)))
}

// separateGateway starts qory gateway on a loopback port, over TLS, as another machine
// would run it: under a configuration directory of its own, with the fake server, its
// access key and gatewayCredentials, and its certificate for 127.0.0.1. It switches the
// test back to the configuration directory it found, which holds no access key, and
// returns the gateway's address, its certificate's file, the issuer, the gateway's
// output and the server. The gateway stops when the test ends.
func separateGateway(t *testing.T) (addr, ca string, iss issuer, out *syncBuffer, srv *fakeServer) {
	t.Helper()
	session := os.Getenv("XDG_CONFIG_HOME")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "gateway-config"))
	srv = newFakeServer(t, "")
	serverFile(t, srv, "  tls: {certificate: gateway.pem, key: gateway-key.pem}\n"+gatewayCredentials)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(string(configDir()), "issuer-k1.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	writeCertificate(t)
	ca = filepath.Join(string(configDir()), "gateway.pem")
	addr, out, done := startGateway(t, "--listen", "127.0.0.1:0")
	t.Cleanup(func() {
		if err := stopGateway(t, done); err != nil {
			t.Errorf("qory gateway: %v\n%s", err, out)
		}
	})
	t.Setenv("XDG_CONFIG_HOME", session)
	return addr, ca, issuer{key: priv}, out, srv
}

// credentialRuntime writes a program that stands in for a runtime and fails, exit 7,
// when its environment holds QORY_RUN_CREDENTIAL_SECRET or the access key's variables,
// or when its environment or its arguments hold any line of the file forbidden, under
// any name: the test writes there the run credentials the runtime must never have.
func credentialRuntime(t *testing.T, forbidden string) string {
	t.Helper()
	writeFile(t, forbidden, "")
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, `#!/bin/sh
test -z "$QORY_RUN_CREDENTIAL_SECRET$QORY_ACCESS_KEY_SECRET$QORY_ACCESS_KEY_ID$QORY_APIARY_PUBLIC_KEY" || exit 7
env | grep -q QORY_RUN_CREDENTIAL && exit 7
if [ -s `+forbidden+` ]; then
	env | grep -qF -f `+forbidden+` && exit 7
	printf '%s\n' "$@" | grep -qF -f `+forbidden+` && exit 7
fi
echo "runtime with $*"
exit 0
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// writeRunCredential writes a run credential's file, mode 0600, as qory reads it.
func writeRunCredential(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noCredentialUnder fails the test when a file under dir holds the run credential.
func noCredentialUnder(t *testing.T, dir, credential string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), credential) {
			t.Errorf("%s holds the run credential", path)
		}
		return nil
	})
}

// TestRunThroughASeparateGateway is a run on a machine whose runs go through the gateway
// session.gateway names, here qory gateway on a loopback port: qory starts no gateway of
// its own and holds no access key, names the gateway and the run, and sends the run
// credential from its file, from QORY_RUN_CREDENTIAL_SECRET or from
// --run-credential-fd, the descriptor first and the file last. The runtime never has the
// credential, under any name, in its environment or its arguments, nor does the record or
// qory's output.
func TestRunThroughASeparateGateway(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	forbidden := filepath.Join(t.TempDir(), "forbidden")
	composedForFake(t, root, credentialRuntime(t, forbidden))
	addr, ca, iss, gwOut, srv := separateGateway(t)
	bad := iss.credential(t, map[string]any{"aud": "another-service"})
	file := filepath.Join(t.TempDir(), "run-credential")
	foragerYAML := filepath.Join(string(configDir()), "forager.yaml")
	writeFile(t, foragerYAML, sessionGateway(addr, ca, "    run_credential_file: "+file+"\n"))
	var sent []string
	for i, c := range []struct {
		name, file, env, fd string
	}{
		{name: "the file", file: "good"},
		{name: "the variable over the file", file: bad, env: "good"},
		{name: "the descriptor over the variable and the file", file: bad, env: bad, fd: "good"},
	} {
		runKey := "queue/" + strconv.Itoa(1234+i)
		good := iss.credential(t, map[string]any{"sub": runKey})
		sent = append(sent, good)
		writeFile(t, forbidden, strings.Join(sent, "\n")+"\n")
		for _, v := range []*string{&c.file, &c.env, &c.fd} {
			if *v == "good" {
				*v = good
			}
		}
		writeRunCredential(t, file, c.file+"\n")
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", c.env)
		args := []string{"run"}
		if c.fd != "" {
			args = append(args, "--run-credential-fd", descriptor(t, c.fd))
		}
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s\n%s", c.name, err, out, gwOut)
		}
		ids := recorded(t, root)
		if len(ids) != 1 {
			t.Fatalf("%s: %d runs recorded", c.name, len(ids))
		}
		wants(t, out, "qory run: through the gateway "+addr+", run "+ids[0]+"\n", "claude exited 0")
		lacks(t, out, good, "qory run: node ")
		if _, ok := os.LookupEnv("QORY_RUN_CREDENTIAL_SECRET"); ok {
			t.Errorf("%s: QORY_RUN_CREDENTIAL_SECRET stayed in qory's environment", c.name)
		}
		// The gateway's record is on its machine, and reaches the server.
		var started map[string]any
		for deadline := time.Now().Add(10 * time.Second); started == nil && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			for _, ev := range srv.byType()["dev.qory.run.started"] {
				if labels, _ := ev["labels"].(map[string]any); labels["run_key"] == runKey {
					started = ev
				}
			}
		}
		if labels, _ := started["labels"].(map[string]any); started == nil || labels["repository"] != "acme/app" || labels["forge"] != "git.example.com" {
			t.Errorf("%s: run.started %v, in %v", c.name, started, srv.byType()["dev.qory.run.started"])
		}
		noCredentialUnder(t, os.Getenv("XDG_STATE_HOME"), good)
		clearRuns(t, root)
	}
	lacks(t, gwOut.String(), sent...)
	if exists(filepath.Join(string(configDir()), "access-key-secret")) {
		t.Error("the machine behind the gateway holds an access key")
	}
}

// TestRunThroughASeparateGatewayWithTheDescriptorOpen is --run-credential-fd as a
// stream: the writer keeps the descriptor open for the whole run, and qory runs with the
// first line, without waiting for the descriptor's end; the runtime does not have the
// descriptor, and when the run ends qory has closed it.
func TestRunThroughASeparateGatewayWithTheDescriptorOpen(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	forbidden := filepath.Join(t.TempDir(), "forbidden")
	inner := credentialRuntime(t, forbidden)
	// A descriptor number high enough that the shell running the runtime opens nothing
	// there itself.
	const n = 200
	runtime := filepath.Join(t.TempDir(), "fd-runtime")
	writeFile(t, runtime, "#!/bin/sh\ntest -e /dev/fd/"+strconv.Itoa(n)+" && exit 9\nexec "+inner+" \"$@\"\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	addr, ca, iss, gwOut, _ := separateGateway(t)
	writeFile(t, filepath.Join(string(configDir()), "forager.yaml"), sessionGateway(addr, ca, ""))
	good := iss.credential(t, nil)
	writeFile(t, forbidden, good+"\n")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// The read end as qory inherits it: without close-on-exec, at n.
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, r.Fd(), syscall.F_DUPFD, n); e != 0 {
		t.Fatal(e)
	}
	r.Close()
	if _, err := w.WriteString(good + "\n"); err != nil {
		t.Fatal(err)
	}
	// Were qory to wait for the descriptor's end, it would come only after this.
	const wait = 20 * time.Second
	closed := make(chan time.Time, 1)
	timer := time.AfterFunc(wait, func() { closed <- time.Now(); w.Close() })
	defer timer.Stop()
	out, err := run(t, "run", "--run-credential-fd", strconv.Itoa(n))
	select {
	case <-closed:
		t.Fatalf("qory run waited %s for the descriptor's end", wait)
	default:
	}
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, gwOut)
	}
	wants(t, out, "qory run: through the gateway "+addr+", run ", "claude exited 0")
	lacks(t, out, good)
	if _, err := w.WriteString("after.the.run\n"); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("a write after the run: %v, want a broken pipe: qory and what it started hold no read end", err)
	}
	noCredentialUnder(t, os.Getenv("XDG_STATE_HOME"), good)
	clearRuns(t, root)
}

// TestRunThroughASeparateGatewayWithoutItsFile is a machine whose forager.yaml names a
// run credential file that is not there, and a run whose credential comes from
// QORY_RUN_CREDENTIAL_SECRET or --run-credential-fd: the file is not the source in use,
// so it is not read, and the run goes on.
func TestRunThroughASeparateGatewayWithoutItsFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, credentialRuntime(t, filepath.Join(t.TempDir(), "forbidden")))
	addr, ca, iss, gwOut, _ := separateGateway(t)
	missing := filepath.Join(t.TempDir(), "run-credential")
	writeFile(t, filepath.Join(string(configDir()), "forager.yaml"), sessionGateway(addr, ca, "    run_credential_file: "+missing+"\n"))
	for _, from := range []string{"the variable", "the descriptor"} {
		good := iss.credential(t, nil)
		args := []string{"run"}
		if from == "the variable" {
			t.Setenv("QORY_RUN_CREDENTIAL_SECRET", good)
		} else {
			t.Setenv("QORY_RUN_CREDENTIAL_SECRET", "")
			args = append(args, "--run-credential-fd", descriptor(t, good))
		}
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s\n%s", from, err, out, gwOut)
		}
		wants(t, out, "qory run: through the gateway "+addr+", run ", "claude exited 0")
		lacks(t, out, good)
		clearRuns(t, root)
	}
}

// TestRunThroughASeparateGatewayIsRefusedByIt is each refusal of the gateway's that qory
// words: a run credential it does not take; a checkout that is not the credential's
// target; a key of --details that the credential decides otherwise. Each is the run's
// failure, exit 1.
func TestRunThroughASeparateGatewayIsRefusedByIt(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, credentialRuntime(t, filepath.Join(t.TempDir(), "forbidden")))
	addr, ca, iss, _, _ := separateGateway(t)
	writeFile(t, filepath.Join(string(configDir()), "forager.yaml"), sessionGateway(addr, ca, ""))
	details := filepath.Join(t.TempDir(), "details.json")
	writeFile(t, details, `{"requester": "a", "ticket": 7}`)
	for _, c := range []struct {
		name       string
		credential string
		args       []string
		want       string
	}{
		{"another audience", iss.credential(t, map[string]any{"aud": "another-service"}), nil, "the gateway refused this run credential"},
		{"expired", iss.credential(t, map[string]any{"iat": time.Now().Add(-time.Hour).Unix(), "exp": time.Now().Add(-30 * time.Minute).Unix()}), nil, "the gateway refused this run credential"},
		{"another repository", iss.credential(t, map[string]any{"project": "other"}), nil, "this checkout is git.example.com/acme/app, and the run credential is for git.example.com/acme/other"},
		{"another requester", iss.credential(t, map[string]any{"requester": "b"}), []string{"--details", details}, `--details ` + details + ` sets requester to "a", and the run credential says "b": the credential decides; remove the key`},
	} {
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", c.credential)
		out, err := run(t, append([]string{"run"}, c.args...)...)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), c.want, out)
		}
		lacks(t, out, c.credential)
		clearRuns(t, root)
	}
}

// TestRunBehindAGatewayRefusesBeforeAnythingStarts is every refusal of a run on a
// machine whose runs go through a gateway, before anything starts, each in its exact
// words and an input error that leaves no record: an access key in a file, a variable or
// a descriptor; --local, --label and --policy, which are for a gateway of the run's own;
// a CA file that cannot be read, a directory included, or holds no certificate; a run
// credential file that is not there, is a directory, worded as the CA file's is, or whose
// mode grants the group or others read or write; and no run credential.
func TestRunBehindAGatewayRefusesBeforeAnythingStarts(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, credentialRuntime(t, filepath.Join(t.TempDir(), "forbidden")))
	file := foragerFile()
	dir := string(configDir())
	ca := filepath.Join(dir, "gateway-ca.pem")
	writeFile(t, ca, "not a certificate\n")
	credential := filepath.Join(t.TempDir(), "run-credential")
	writeRunCredential(t, credential, "header.claims.signature\n")
	writeCertificate(t)
	with := sessionGateway("gateway.example:8443", "gateway.pem", "    run_credential_file: "+credential+"\n")
	const through = "this machine's runs go through the gateway session.gateway.url names"
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\n")
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) []string
		want  string
	}{
		{"--local", func(*testing.T) []string { return []string{"run", "--local"} },
			"--local runs with a gateway of this run's own and no server, and this machine has no gateway section: its runs go through the gateway session.gateway.url names"},
		{"an access-key-secret file", func(t *testing.T) []string {
			writeSecret(t, newKey(t))
			t.Cleanup(func() { os.Remove(filepath.Join(dir, "access-key-secret")) })
			return []string{"run"}
		}, filepath.Join(dir, "access-key-secret") + " exists, and " + through + ": a machine behind a gateway holds no access key; remove the file"},
		{"QORY_ACCESS_KEY_ID", func(t *testing.T) []string { t.Setenv("QORY_ACCESS_KEY_ID", testAccessKey); return []string{"run"} },
			"QORY_ACCESS_KEY_ID is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"QORY_ACCESS_KEY_SECRET", func(t *testing.T) []string {
			t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
			return []string{"run"}
		}, "QORY_ACCESS_KEY_SECRET is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"QORY_APIARY_PUBLIC_KEY", func(t *testing.T) []string { t.Setenv("QORY_APIARY_PUBLIC_KEY", "[]"); return []string{"run"} },
			"QORY_APIARY_PUBLIC_KEY is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"--access-key-secret-fd", func(t *testing.T) []string {
			return []string{"run", "--access-key-secret-fd", descriptor(t, newKey(t).Secret())}
		}, "--access-key-secret-fd is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"--label", func(*testing.T) []string { return []string{"run", "--label", "issue=77"} },
			"--label works only when this machine runs its own gateway; behind a gateway the run credential sets the labels"},
		{"--policy", func(*testing.T) []string { return []string{"run", "--policy", policy} },
			"--policy is the run's own policy for a gateway the run starts itself, and " + through},
		{"a CA file that holds no certificate", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway-ca.pem", "    run_credential_file: "+credential+"\n"))
			return []string{"run"}
		},
			file + ": session.gateway.ca_file " + ca + " holds no PEM certificate"},
		{"a CA file that is not there", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "missing-ca.pem", "    run_credential_file: "+credential+"\n"))
			return []string{"run"}
		}, file + ": session.gateway.ca_file " + filepath.Join(dir, "missing-ca.pem") + ": no such file or directory"},
		{"a CA file that is a directory", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", dir, "    run_credential_file: "+credential+"\n"))
			return []string{"run"}
		}, file + ": session.gateway.ca_file " + dir + ": is a directory"},
		{"a run credential file the group may read", func(t *testing.T) []string {
			os.Chmod(credential, 0o640)
			t.Cleanup(func() { os.Chmod(credential, 0o600) })
			return []string{"run"}
		}, credential + " is mode 0640, which grants access to the group or others: chmod 600 " + credential},
		{"a run credential file others may read", func(t *testing.T) []string {
			os.Chmod(credential, 0o644)
			t.Cleanup(func() { os.Chmod(credential, 0o600) })
			return []string{"run"}
		}, credential + " is mode 0644, which grants access to the group or others: chmod 600 " + credential},
		{"a run credential file that is not there", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway.pem", "    run_credential_file: "+filepath.Join(dir, "missing-credential")+"\n"))
			return []string{"run"}
		}, file + ": session.gateway.run_credential_file " + filepath.Join(dir, "missing-credential") + ": no such file or directory"},
		{"a run credential file that is a directory", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway.pem", "    run_credential_file: "+dir+"\n"))
			return []string{"run"}
		}, file + ": session.gateway.run_credential_file " + dir + ": is a directory"},
		{"no run credential", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway.pem", ""))
			return []string{"run"}
		}, through + ", and there is no run credential: set session.gateway.run_credential_file, --run-credential-fd or QORY_RUN_CREDENTIAL_SECRET"},
		{"an empty --run-credential-fd", func(t *testing.T) []string {
			return []string{"run", "--run-credential-fd", descriptor(t, "")}
		}, through + ", and there is no run credential: set session.gateway.run_credential_file, --run-credential-fd or QORY_RUN_CREDENTIAL_SECRET"},
		{"--run-credential-fd 1", func(*testing.T) []string { return []string{"run", "--run-credential-fd", "1"} },
			"--run-credential-fd 1: the standard input, output and error carry no run credential; name a descriptor of 3 or above"},
		{"--run-credential-fd too long", func(t *testing.T) []string {
			// More than a pipe holds: a file, by a raw descriptor the command closes.
			long := filepath.Join(t.TempDir(), "long")
			writeFile(t, long, strings.Repeat("x", 70000))
			fd, err := syscall.Open(long, syscall.O_RDONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			return []string{"run", "--run-credential-fd", strconv.Itoa(fd)}
		}, "--run-credential-fd * more than a run credential"},
		{"a --run-credential-fd that is not open", func(*testing.T) []string { return []string{"run", "--run-credential-fd", "1000"} },
			"--run-credential-fd 1000: *"},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, file, with)
			args := c.setup(t)
			out, err := run(t, args...)
			// A * in want stands for any text: the descriptor's number, or the system's error.
			before, after, wild := strings.Cut(c.want, "*")
			matches := err != nil && (err.Error() == c.want || wild && strings.HasPrefix(err.Error(), before) && strings.HasSuffix(err.Error(), after))
			if cmd.ExitCode(err) != cmd.ExitInput || !matches {
				t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), c.want, out)
			}
			if ids := recorded(t, root); len(ids) != 0 {
				t.Errorf("a refused run left a record: %v", ids)
			}
		})
	}
}

// TestRunBehindAGatewayKeepsTheCredentialFileFromTheWall is a walled run whose run
// credential's file is in a directory it mounts: Forager refuses it as one of its own
// files, in either mode, before anything starts.
func TestRunBehindAGatewayKeepsTheCredentialFileFromTheWall(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	keys := tempDir(t)
	credential := filepath.Join(keys, "run-credential")
	writeRunCredential(t, credential, "header.claims.signature\n")
	writeCertificate(t)
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	writeFile(t, foragerFile(), sessionGateway("127.0.0.1:1", "gateway.pem", "    run_credential_file: "+credential+"\n")+wallSection)
	for _, c := range []struct{ mount, want string }{
		{keys, "the mount " + keys + " contains " + credential + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
		{keys + ":ro", "the mount " + keys + " contains " + credential + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"},
	} {
		out, err := run(t, "run", "claude", "--mount", c.mount)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", c.mount, err, cmd.ExitCode(err), c.want, out)
		}
	}
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), " run ") {
		t.Errorf("a container was started:\n%s", data)
	}
}

// TestRunBehindAWallThroughASeparateGateway is a walled run through the gateway, its run
// credential in QORY_RUN_CREDENTIAL_SECRET: the container's environment file holds
// neither the variable nor the credential, and no command line does.
func TestRunBehindAWallThroughASeparateGateway(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	addr, ca, iss, _, _ := separateGateway(t)
	docker, log := fakeDocker(t)
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  env: [MODEL_KEY]\n  user: \"1000:1000\"\n"
	writeFile(t, foragerFile(), sessionGateway(addr, ca, "")+wallSection)
	credential := iss.credential(t, nil)
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	t.Setenv("MODEL_KEY", "not-a-real-key")
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "env: MODEL_KEY=not-a-real-key")
	lacks(t, string(data), "QORY_RUN_CREDENTIAL", credential)
	lacks(t, out, credential)
}

// TestACredentialAdapterNeverReceivesTheRunCredential is a run with a gateway of its own
// whose policy selects a credential an adapter answers for, with
// QORY_RUN_CREDENTIAL_SECRET in qory's environment: the adapter, which fails when it
// receives the variable, answers, and the walled run goes on to its container.
func TestACredentialAdapterNeverReceivesTheRunCredential(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	asked := filepath.Join(t.TempDir(), "asked")
	adapter := filepath.Join(t.TempDir(), "adapter")
	writeFile(t, adapter, "#!/bin/sh\ntest -z \"$QORY_RUN_CREDENTIAL_SECRET\" || exit 7\nenv | grep -q QORY_RUN_CREDENTIAL && exit 7\ntouch "+asked+"\necho '{\"version\":1,\"token\":\"t\",\"apply\":[{\"hosts\":[\"tracker.acme.example\"],\"scheme\":\"bearer\"}]}'\n")
	if err := os.Chmod(adapter, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, foragerFile(), "apiVersion: qory.dev/v1alpha1\ngateway:\n  credentials:\n    tracker: {adapter: ["+adapter+"]}\nwall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: tracker}\n")
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", "header.claims.signature")
	out, err := run(t, "run", "claude", "--local", "--policy", policy)
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if !exists(asked) {
		t.Errorf("the adapter was never asked\n%s", out)
	}
}
