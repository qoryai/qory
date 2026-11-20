//go:build unix

package cmd_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/receiver"
	"github.com/qoryai/forager/server"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/render/codex"
)

// envRealCodexChild is set in the environment of the test binary that
// [TestARealWalledCodexRunReachesTheModelThroughTheGateway] starts for itself.
const envRealCodexChild = "QORY_TEST_REAL_CODEX_CHILD"

// The account header and claim of a subscription login's token, as the subscription
// entry of the model catalog sets them for Codex.
const (
	codexAccountHeader = "chatgpt-account-id"
	codexAccountClaim  = "/https:~1~1api.openai.com~1auth/chatgpt_account_id"
)

// codexScan is the launch command of the image of
// [TestARealWalledCodexRunReachesTheModelThroughTheGateway], with MARK replaced by the
// mark of the test's fake keys. It prints whether OPENAI_API_KEY is set and whether a
// variable of its environment has the mark, starts codex with its arguments, and then
// prints CODEX_HOME, whether a write into config.toml passes, each transcript below
// sessions/, each file of the home, each line from the first hooks.state table of the
// home's config.toml on, and whether a file of the home has the mark. It prints "scan: grep
// failed" with grep's status when a search fails. It exits with codex's status.
const codexScan = `#!/bin/sh
mark=MARK
if [ -n "$OPENAI_API_KEY" ]; then echo "scan: OPENAI_API_KEY is set"; fi
env | grep -qF -- "$mark"
case $? in 0) echo "scan: a variable has the key";; 1) echo "scan: no variable has the key";; *) echo "scan: grep failed on the environment";; esac
codex "$@"
status=$?
echo "scan: CODEX_HOME=$CODEX_HOME"
if { echo x >> "$CODEX_HOME/config.toml"; } 2>/dev/null; then echo "scan: wrote config.toml"; else echo "scan: cannot write config.toml"; fi
for f in "$CODEX_HOME"/sessions/*/*/*/rollout-*.jsonl; do [ -f "$f" ] && echo "scan: rollout $f"; done
find "$CODEX_HOME" -type f | while read -r f; do echo "scan: file $f"; done
sed -n '/^\[hooks\.state\./,$p' "$CODEX_HOME/config.toml" | while read -r l; do echo "scan: config $l"; done
grep -rlF -- "$mark" "$CODEX_HOME"
found=$?
case $found in 0) echo "scan: a file has the key";; 1) echo "scan: no file has the key";; *) echo "scan: grep failed with status $found";; esac
exit $status
`

// realCodexVersion is the Codex the test builds into its image: 0.170.0, the dist-tag
// latest of @openai/codex on npm on 2026-11-18. The walled run's settings and the keys
// its arguments may not set were first checked on 0.167.2. realCodexSums are npm's dist.integrity
// of its native build for each engine architecture, sha512 in hex.
const realCodexVersion = "0.170.0"

var realCodexSums = map[string]string{
	"amd64": "e9c09925a64bd7bb624c0be17753e2aedf2fb15d5d3fc09f1bde3e207ca050ee26db8a182d0ac82272492f22b370cbffb94a276eed5d176266717840fece5bb2",
	"arm64": "9892e6e5e89afc672e902ad17d51276150f974e5c613b726a5df5d27f8b191288a89fcc300f686abcdd9126c15864dcfc5b7533d448b4699d765029b352e47a0",
}

// systemSkillsLine is the text of the error that Codex 0.170.0 logs when it fails to
// install the system skills: "failed to install system skills: <cause>".
const systemSkillsLine = "install system skills"

// realCodexImage builds an image on alpine with git, the alpine packages given, Codex,
// and [codexScan] with mark at /usr/local/bin/codex-scan. Codex is npm's native build of @openai/codex for this
// engine's machines, at version and checked against sums, the sha512 of the build for
// each engine architecture in hex. The native build's folder stays whole, here at
// /opt/codex, and the codex command links to its bin/codex, so Codex finds its helpers
// beside it. Codex runs only in the image's build and its containers. The test removes
// the image at its end.
func realCodexImage(t *testing.T, version string, sums map[string]string, mark string, packages ...string) string {
	t.Helper()
	_, arch, _ := strings.Cut(dockerOut(t, "", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"), "/")
	npmArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[arch]
	if npmArch == "" {
		t.Fatalf("Codex has no native build for the engine's machines, %s", arch)
	}
	sum := sums[arch]
	if sum == "" {
		t.Fatalf("no checksum of Codex %s for %s", version, arch)
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "codex-scan"), strings.Replace(codexScan, "MARK", mark, 1))
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM public.ecr.aws/docker/library/alpine:3.22\n"+
		"RUN apk add --no-cache "+strings.Join(append([]string{"git"}, packages...), " ")+"\n"+
		"ADD https://registry.npmjs.org/@openai/codex/-/codex-"+version+"-linux-"+npmArch+".tgz /tmp/codex-native.tgz\n"+
		"RUN echo \""+sum+"  /tmp/codex-native.tgz\" | sha512sum -c - \\\n"+
		" && mkdir /tmp/x && tar -xzf /tmp/codex-native.tgz -C /tmp/x && mv \"/tmp/x/package/vendor/$(uname -m)-unknown-linux-musl\" /opt/codex \\\n"+
		" && ln -s /opt/codex/bin/codex /usr/local/bin/codex && rm -rf /tmp/x /tmp/codex-native.tgz \\\n"+
		" && test \"$(HOME=/tmp/c CODEX_HOME=/tmp/c/codex codex --version 2>/dev/null)\" = \"codex-cli "+version+"\" && rm -rf /tmp/c\n"+
		"COPY codex-scan /usr/local/bin/codex-scan\n"+
		"RUN chmod 755 /usr/local/bin/codex-scan && mkdir -p /home/agent && chmod 1777 /home/agent\n"+
		"ENV HOME=/home/agent\n"+
		"USER 1000:1000\n")
	image := "qory-test-codex:" + hex.EncodeToString(id[:])
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", image).Run() })
	dockerOut(t, "", "build", "--quiet", "--tag", image, dir)
	return image
}

// testAuthority is a certificate authority of the test's, and its key.
func testAuthority(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qory test upstream authority"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// trustOnly points this process's roots, and those of the programs it starts, at ca
// alone, with SSL_CERT_FILE and SSL_CERT_DIR: the gateway's proxy verifies the upstream's
// certificate against them. The process reads its roots once, at its first verification
// of a certificate; the test fails when that came before, so that ca is not a root.
func trustOnly(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "roots.pem")
	writeFile(t, file, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})))
	t.Setenv("SSL_CERT_FILE", file)
	t.Setenv("SSL_CERT_DIR", t.TempDir())
	ip := net.IPv4(192, 0, 2, 1)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{ip}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: ip.String()}); err != nil {
		t.Fatalf("this process read its roots before the test set them: %v", err)
	}
}

// upstreamEntry is one request the stub upstream logged: see cmd/testdata/codexupstream.
type upstreamEntry struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Bearer  string   `json:"bearer"`
	Account string   `json:"account"`
	Texts   []string `json:"texts"`
	Outputs []string `json:"outputs"`
}

// upstream is the stub upstream's container and the address the gateway reaches it on.
type upstream struct{ name, host string }

// upstreamBinary builds cmd/testdata/codexupstream for this engine's machines and
// returns its path.
func upstreamBinary(t *testing.T) string {
	t.Helper()
	_, arch, _ := strings.Cut(dockerOut(t, "", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"), "/")
	bin := filepath.Join(t.TempDir(), "upstream")
	build := exec.Command("go", "build", "-o", bin, "./cmd/testdata/codexupstream")
	build.Dir = ".."
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// startUpstream builds cmd/testdata/codexupstream for this engine's machines and starts
// it in a container on alpine, with a certificate of ca and the variables env, each
// NAME=value, removed at the test's end and when it stops. It returns once the stub
// listens and this machine reaches it at the container's address. A published port on
// 127.0.0.1 would not do: the wall's NO_PROXY has the loopback addresses, so Codex would
// not send to it through the gateway.
func startUpstream(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, env ...string) upstream {
	t.Helper()
	bin := upstreamBinary(t)
	der, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	u := upstream{name: "qory-test-upstream-" + hex.EncodeToString(id[:])}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", u.name).Run() })
	args := []string{"run", "--rm", "--detach", "--stop-timeout", "1", "--name", u.name, "--mount", "type=bind,src=" + bin + ",dst=/upstream,readonly",
		"--env", "QORY_TEST_UPSTREAM_CA=" + base64.StdEncoding.EncodeToString(ca.Raw),
		"--env", "QORY_TEST_UPSTREAM_CA_KEY=" + base64.StdEncoding.EncodeToString(der)}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	dockerOut(t, "", append(args, "--entrypoint", "/upstream", "public.ecr.aws/docker/library/alpine:3.22")...)
	for deadline := time.Now().Add(30 * time.Second); ; {
		logs, _ := exec.Command("docker", "logs", u.name).CombinedOutput()
		if bytes.Contains(logs, []byte("upstream: listening")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upstream did not listen within 30 seconds:\n%s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	u.host = dockerOut(t, "", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", u.name)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(u.host, "8443"), 5*time.Second)
	if err != nil {
		t.Fatalf("use an engine whose containers this machine reaches at the containers' addresses, such as OrbStack or Docker on Linux: "+
			"the gateway of this process dials the upstream at %s:8443, and the dial failed: %v", u.host, err)
	}
	conn.Close()
	return u
}

// log is every request the upstream logged.
func (u upstream) log(t *testing.T) []upstreamEntry {
	t.Helper()
	b, err := exec.Command("docker", "exec", u.name, "cat", "/tmp/upstream.jsonl").Output()
	if err != nil {
		t.Fatalf("the upstream's log: %v", err)
	}
	var out []upstreamEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e upstreamEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("the upstream's log: %v: %s", err, sc.Bytes())
		}
		out = append(out, e)
	}
	return out
}

// startModelControl starts a control plane in this process whose run configuration of
// every run allows egress to host alone, with Codex's model key model: the Responses
// format on https://<host>:8443/v1, set on /v1/responses alone, a subscription login's
// token with its account header when login is set, else an API key, whose value is
// value. It has the gateway of [codeAtGateway] take each run's configuration and the
// key's value from it, for the rest of the test.
func startModelControl(t *testing.T, host string, login bool, value string) {
	t.Helper()
	key, signer := newKey(t), newKey(t)
	store, err := receiver.OpenFile(filepath.Join(t.TempDir(), "received.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	auth := map[string]any{"scheme": "bearer"}
	if login {
		auth = map[string]any{"scheme": "bearer", "oauth": true, "account_header": codexAccountHeader, "account_claim": codexAccountClaim}
	}
	runDoc, err := json.Marshal(map[string]any{"version": 1,
		"security_policy":                        map[string]any{"version": 1, "egress": map[string]any{"mode": "enforce", "allow": []string{host}}},
		"default_model_provider_credential_name": "model",
		"credentials": []any{map[string]any{"name": "model", "group": "openai", "format": "openai-responses", "endpoint": "https://" + host + ":8443/v1",
			"auth": auth, "paths": []string{"/v1/responses"}, "pause_for_s": 3600}}})
	if err != nil {
		t.Fatal(err)
	}
	pin := accesskey.Pin{{Alg: "ed25519", PublicKey: signer.PublicKey().String()}}
	var srv *httptest.Server
	h := &receiver.Handler{
		Keys: func(id string) (receiver.AccessKey, bool) {
			return receiver.AccessKey{PublicKey: key.PublicKey(), StoredSecrets: true}, id == gatewayAccessKey
		},
		Signer: signer,
		Store:  store,
		Configuration: func() ([]byte, string) {
			doc := `{"version":1,"node_id":"nd_f1xt0re00000000e","workspaces":["ws_f1xt0re00000000e"]` +
				`,"apiary_public_key":[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]` +
				`,"events":{"url":"` + srv.URL + receiver.DefaultEventsPath + `","types":["*"]},"run":{"url":"` + srv.URL + receiver.DefaultRunPath + `"}` +
				`,"secrets":{"url":"` + srv.URL + receiver.DefaultSecretsPath + `"}}`
			return []byte(doc), digest(doc)
		},
		RunConfiguration: func(receiver.Run) ([]byte, string, *receiver.Refusal) {
			return runDoc, digest(string(runDoc)), nil
		},
		Secrets: func(_ receiver.Run, names []string) ([]receiver.Value, *receiver.Refusal) {
			var values []receiver.Value
			for range names {
				values = append(values, receiver.NewValue([]byte(value)))
			}
			return values, nil
		},
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The gateway reports the state of the run's model keys; the answer changes none.
		if _, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, receiver.DefaultRunPath+"/"), "/"); r.Method == http.MethodPost && path == "model-keys" {
			_, _ = io.Copy(io.Discard, r.Body)
			answer := []byte(`{"version":1,"keys":[]}`)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(server.HeaderSignature, signer.SignAnswer(accesskey.Answer{Status: http.StatusOK,
				RequestSignature: r.Header.Get(server.HeaderSignature), Body: answer}))
			_, _ = w.Write(answer)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	codeGatewayServer = &gateway.Server{Version: 1, URL: srv.URL, AccessKeyID: gatewayAccessKey, ApiaryPublicKey: pin, AccessKey: key, InstanceID: "i_test"}
	t.Cleanup(func() { codeGatewayServer = nil })
}

// fakeLoginToken is a fake subscription login's token: a JWT whose payload has the
// account id account under the claim of [codexAccountClaim], and whose signature part is
// sig.
func fakeLoginToken(account, sig string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": account}})
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + "." + sig
}

// filesWith are the files below roots that contain one of values.
func filesWith(roots []string, values ...string) []string {
	var found []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				if b, err := os.ReadFile(p); err == nil && slices.ContainsFunc(values, func(v string) bool { return bytes.Contains(b, []byte(v)) }) {
					found = append(found, p)
				}
			}
			return nil
		})
	}
	return found
}

// recordEvents are the data of the events of type typ in the record file at path.
func recordEvents(t *testing.T, path, typ string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for line := range strings.Lines(string(b)) {
		var e struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Type == typ {
			out = append(out, e.Data)
		}
	}
	return out
}

// realCodexSetup is what [runRealCodexChild] makes for the test binary it starts, which
// gets it as JSON in [envRealCodexChild]: the mark of the fake keys, the wall's helper and
// image, the upstream's container and address, and the authority of the upstream's
// certificate, its certificate and its PKCS #8 key, each DER in standard base64.
type realCodexSetup struct {
	Mark, Helper, Image, Upstream, Host, CA, CAKey string
}

// authority is the setup's authority and its key.
func (s realCodexSetup) authority(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certDER, err1 := base64.StdEncoding.DecodeString(s.CA)
	keyDER, err2 := base64.StdEncoding.DecodeString(s.CAKey)
	if err1 != nil || err2 != nil {
		t.Fatalf("the setup's authority: %v, %v", err1, err2)
	}
	ca, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("the setup's authority has a key of type %T, want ECDSA", parsed)
	}
	return ca, key
}

// codexChild are the choices of [runRealCodexChild] beyond Codex's version: the alpine
// packages the image adds, and the variables of the upstream, each NAME=value, such as
// its script.
type codexChild struct {
	packages, upstreamEnv []string
}

// runRealCodexChild builds the wall's helper and the image, with Codex at version and
// checked against sums and with child's packages, see [realCodexImage], starts the
// upstream with child's variables, and runs test t in a
// separate test binary, with the setup in [envRealCodexChild]: this
// process may have read its roots already, and that binary reads them from the
// upstream's authority. The test binary has until a minute before t's deadline, so this
// process removes the upstream and the image also when that binary runs out of time.
func runRealCodexChild(t *testing.T, version string, sums map[string]string, child codexChild) {
	t.Helper()
	var id [6]byte
	_, _ = rand.Read(id[:])
	setup := realCodexSetup{Mark: "qorytest-" + hex.EncodeToString(id[:]), Helper: wallHelper(t)}
	setup.Image = realCodexImage(t, version, sums, setup.Mark, child.packages...)
	ca, caKey := testAuthority(t)
	u := startUpstream(t, ca, caKey, child.upstreamEnv...)
	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	setup.Upstream, setup.Host = u.name, u.host
	setup.CA, setup.CAKey = base64.StdEncoding.EncodeToString(ca.Raw), base64.StdEncoding.EncodeToString(keyDER)
	doc, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	limit := 20 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		limit = time.Until(deadline) - time.Minute
	}
	if limit < 2*time.Minute {
		t.Fatalf("the test has %v left, want at least 3 minutes", limit+time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1", "-test.timeout="+(limit-30*time.Second).String())
	c.Env = append(os.Environ(), envRealCodexChild+"="+string(doc))
	out, err := c.CombinedOutput()
	t.Logf("the test binary:\n%s", out)
	if err != nil || !bytes.Contains(out, []byte("--- PASS: "+t.Name())) {
		t.Fatalf("the test binary: %v, want --- PASS: %s", err, t.Name())
	}
}

// TestARealWalledCodexRunReachesTheModelThroughTheGateway is a walled Codex run from a run
// code that only reads, behind a real wall on this machine's engine, with Codex itself in
// the image, at [realCodexVersion], at a gateway of Forager's
// in this process whose control plane has a fake model key for a stub of the Responses
// API in a container: an API key, and a subscription login's token. Codex starts on the
// run's fresh home at /var/lib/qory-codex-home, with config.toml bound read-only, and
// OPENAI_API_KEY set; no variable of its environment contains the key. The stub gets
// each request with the key as the bearer, and with the token the account header from
// its claim; the prompt mentions the skill workflow-hello at its path in the home. Codex
// prints the stub's text and no error, writes its transcript below sessions/ in the
// home, and the run ends succeeded, with egress to the stub alone. No file of the home,
// of the run's record or of qory's folders contains the key, nor does qory's output, and
// the home is gone once the run ends. It runs only with QORY_TEST_DOCKER=1 and docker on
// the PATH, in a separate test binary, which reads its roots from SSL_CERT_FILE. It
// needs an engine whose containers this machine reaches at the containers' addresses, such as
// OrbStack or Docker on Linux, and fails on Docker Desktop for Mac, which routes no
// container address to this machine.
func TestARealWalledCodexRunReachesTheModelThroughTheGateway(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if os.Getenv(envRealCodexChild) == "" {
		runRealCodexChild(t, realCodexVersion, realCodexSums, codexChild{})
		return
	}
	var setup realCodexSetup
	if err := json.Unmarshal([]byte(os.Getenv(envRealCodexChild)), &setup); err != nil {
		t.Fatal(err)
	}
	ca, caKey := setup.authority(t)
	trustOnly(t, ca, caKey)
	mark, helper, image := setup.Mark, setup.Helper, setup.Image
	u := upstream{name: setup.Upstream, host: setup.Host}
	for _, c := range []struct {
		name    string
		login   bool
		value   string
		account string
	}{
		{"api key", false, "sk-" + mark + "-api", ""},
		{"subscription login", true, fakeLoginToken("acct-qory-test", mark+"-login"), "acct-qory-test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sum := sha256.Sum256([]byte(c.value))
			bearer := hex.EncodeToString(sum[:])
			before := len(u.log(t))
			startModelControl(t, u.host, c.login, c.value)
			atGateway(t)
			// The upstream's address is a private network address, which the gateway's proxy
			// dials only when the policy of the gateway's machine allows it.
			codeGatewayAllow = []string{u.host}
			t.Cleanup(func() { codeGatewayAllow = nil })
			codeReal = true
			t.Cleanup(func() { codeReal = false })
			r := runFromCodeLinks(t, func(string) []string {
				machineConfig(t, "harness:\n  launch:\n    codex:\n      command: /usr/local/bin/codex-scan\n")
				writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
					"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
				return nil
			}, []string{"--home", filepath.Join(tempDir(t), "home")}, func(s string) string {
				return s + "workflows:\n  hello:\n    time_limit: 5m\n    access: read\n"
			}, func(d map[string]any) {
				d["run_settings"] = map[string]any{"runtime": "codex"}
			}, helloSkill, nil)
			records := filepath.Join(r.open.RunsDir, r.open.RunID)
			if !r.joined || r.err != nil {
				record, _ := os.ReadFile(filepath.Join(records, "session.jsonl"))
				t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
			}
			t.Logf("qory run:\n%s", r.out)
			home := checkHomeMounts(t, r.spec)
			wants(t, r.out, "scan: OPENAI_API_KEY is set\n", "scan: CODEX_HOME="+codex.HomeInWall+"\n", "scan: cannot write config.toml\n",
				"scan: rollout "+codex.HomeInWall+"/sessions/", "scan: no file has the key\n", `"text":"Hi"`)
			wants(t, r.out, "scan: no variable has the key\n")
			lacks(t, r.out, `"type":"error"`, "scan: a variable has the key", "scan: wrote config.toml", "scan: a file has the key", "scan: grep failed")
			// Codex logs this error when it fails to write its system skills into the home,
			// which it tries only with skills.bundled.enabled on
			// (codex-rs/ext/skills/src/host_service.rs:164, :293 and :504 at rust-v0.170.0).
			lacks(t, r.out, systemSkillsLine)
			log := u.log(t)[before:]
			mention := "[$workflow-hello](" + codex.HomeInWall + "/skills/workflow-hello/SKILL.md)"
			if !slices.ContainsFunc(log, func(e upstreamEntry) bool {
				return e.Method == http.MethodPost && e.Path == "/v1/responses" &&
					slices.ContainsFunc(e.Texts, func(s string) bool { return strings.HasPrefix(s, "user: ") && strings.Contains(s, mention) })
			}) {
				t.Errorf("the upstream got %+v, want a POST to /v1/responses with the prompt %s", log, mention)
			}
			for _, e := range log {
				if e.Bearer != bearer || e.Account != c.account {
					t.Errorf("the upstream got %s %s with a bearer of SHA-256 %s and account %q, want %s and %q", e.Method, e.Path, e.Bearer, e.Account, bearer, c.account)
				}
			}
			events := filepath.Join(records, "events.jsonl")
			b, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			if got := lastExited(t, b); got != "succeeded//0" {
				t.Errorf("the run ended %s, want succeeded//0", got)
			}
			egress := recordEvents(t, events, "dev.qory.run.egress")
			if len(egress) == 0 {
				t.Error("the record has no egress")
			}
			for _, d := range egress {
				if d["host"] != u.host {
					t.Errorf("the run reached %v:%v, want the upstream %s alone", d["host"], d["port"], u.host)
				}
			}
			started := recordEvents(t, events, "dev.qory.session.started")
			var args []any
			if len(started) == 1 {
				args, _ = started[0]["args"].([]any)
			}
			if !slices.Contains(args, any(`model_providers.qory.base_url="https://`+u.host+`:8443/v1"`)) {
				t.Errorf("dev.qory.session.started is %v, want one whose arguments set the upstream as the model provider's address", started)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Errorf("the Codex home %s is left after the run: %v", home, err)
			}
			roots := []string{records, os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CONFIG_HOME")}
			if found := filesWith(roots, c.value, mark); len(found) > 0 || strings.Contains(r.out, mark) {
				t.Errorf("the key is in %q, or in qory's output", found)
			}
		})
	}
}
