package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cgi"
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
	"github.com/qoryai/forager/sealed"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// envRealPush makes the test binary the child of [TestForagerRunKeepsThePushCredentialOnThePushProxyAlone]:
// the folder with the test authority and the forge's certificate.
const envRealPush = "QORY_TEST_REAL_PUSH"

// The fake values the control seals for the work credential and the push credential, the
// branch of the work item, and the ids of the stand-in control.
const (
	realWorkSecret = "fake-read-token-of-the-real-push-test"
	realPushSecret = "fake-write-token-of-the-real-push-test"
	realBranch     = "refs/heads/qory/wi_01"
	realKey        = "ak_f1xt0re000000000"
	realNode       = "nd_f1xt0re000000000"
	realWorkspace  = "ws_f1xt0re000000000"
	realSecrets    = "/v1/secrets"
)

// realRunDocument is the run configuration of a write run: a read work credential,
// work-1, and a push credential, push-1, on the repository's git paths of localhost, with
// the work item's branch as the push's one ref.
const realRunDocument = `{"version":1,"security_policy":{"version":1,"egress":{"mode":"enforce","allow":["localhost"]}},"credentials":[` +
	`{"name":"work-1","apply":[{"hosts":["localhost"],"scheme":"basic","username":"x-access-token","paths":["/acme/site.git/*"]}]},` +
	`{"name":"push-1","purpose":"push","apply":[{"hosts":["localhost"],"scheme":"basic","username":"x-access-token","paths":["/acme/site.git/*"],"push_ref":"` + realBranch + `"}]}]}`

// narrowRunDocument is [realRunDocument] with a push credential whose items are the two
// paths of a git push, the ref list and receive-pack, and no path for git-upload-pack: the
// shape of the push list that the adapter forms for a push credential.
const narrowRunDocument = `{"version":1,"security_policy":{"version":1,"egress":{"mode":"enforce","allow":["localhost"]}},"credentials":[` +
	`{"name":"work-1","apply":[{"hosts":["localhost"],"scheme":"basic","username":"x-access-token","paths":["/acme/site.git/*"]}]},` +
	`{"name":"push-1","purpose":"push","apply":[{"hosts":["localhost"],"scheme":"basic","username":"x-access-token","paths":["/acme/site.git/info/refs","/acme/site.git/git-receive-pack"],"push_ref":"` + realBranch + `"}]}]}`

// TestForagerRunKeepsThePushCredentialOnThePushProxyAlone is a run at a real Forager
// gateway, with a stand-in control that serves a run configuration with a read credential
// and a push credential, and a forge that is git over https. It calls
// [foragerRun.HostProxy] and [foragerRun.PushProxy] itself and runs git through each, so
// it tests those two wrappers and Forager's gateway, not the push step: the tests with a
// fake run, such as TestThePushGoesThroughThePushProxy, cover which of the two [pushWork]
// calls. The git push through the run's proxy, which HostProxy reaches, fails and reaches
// the forge with no push credential and as no push of the branch; the push through
// PushProxy reaches the forge with the push credential and the branch is there.
//
// The gateway verifies the forge's certificate against this machine's authorities, which
// Go reads from SSL_CERT_FILE on Linux alone, and reads once per process: the test starts
// this binary again with SSL_CERT_FILE set to its authority, and the child runs the run.
func TestForagerRunKeepsThePushCredentialOnThePushProxyAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Go reads the authorities of SSL_CERT_FILE on Linux alone, and the gateway must trust the test forge's")
	}
	if dir := realSetup(t, envRealPush); dir != "" {
		realPush(t, dir)
		return
	}
	startRealChild(t, "q-rp", "TestForagerRunKeepsThePushCredentialOnThePushProxyAlone", envRealPush)
}

// realChildArg is the argument after "--" that marks the child process of a test of a real
// gateway. A child never starts a child: it takes the parent branch of its test only when its
// marker is missing and this argument is too.
const realChildArg = "qory-real-child"

// realSetup is the folder that the parent of a test of a real gateway gave its child in the
// variable env, "" in the parent. It fails the test when the child has no folder, as
// when TestMain does not keep the variable: the child would be a parent and start a child
// without end.
func realSetup(t *testing.T, env string) string {
	t.Helper()
	dir := os.Getenv(env)
	if dir == "" && slices.Contains(os.Args, realChildArg) {
		t.Fatalf("this child of %s has no %s, so it would start another child; TestMain must keep the variable", os.Args[0], env)
	}
	return dir
}

// startRealChild writes the certificates of a test forge in a new folder, whose name starts
// with prefix, and runs this binary again with only test, the folder in env, and SSL_CERT_FILE
// set to its authority. It fails the test when the child fails.
func startRealChild(t *testing.T, prefix, test, env string) {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	writeRealCertificates(t, dir)
	child := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1", "--", realChildArg)
	child.Env = append(os.Environ(), env+"="+dir, "SSL_CERT_FILE="+filepath.Join(dir, "ca.pem"))
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the child run failed: %v\n%s", err, out)
	}
}

// writeRealCertificates writes ca.pem, the authority, and forge.pem and forge.key, a
// certificate for localhost that it signed, in dir.
func writeRealCertificates(t *testing.T, dir string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qory test authority"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"ca.pem":    {Type: "CERTIFICATE", Bytes: caDER},
		"forge.pem": {Type: "CERTIFICATE", Bytes: leafDER},
		"forge.key": {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// realForge is git over https on localhost that records each request as "<method> <path>
// <Authorization header>".
type realForge struct {
	srv  *httptest.Server
	bare string
	mu   sync.Mutex
	got  []string
}

// startRealForge serves the repository acme/site, which has receive-pack on, with the
// certificate in dir.
func startRealForge(t *testing.T, dir string) *realForge {
	t.Helper()
	git := proxygittest.Start(t)
	git.Repository(t, "acme/site", "main", map[string]string{"README.md": "acme/site\n"})
	f := &realForge{bare: filepath.Join(git.Root, "acme", "site.git")}
	proxygittest.Git(t, f.bare, "config", "http.receivepack", "true")
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := &cgi.Handler{
		Path:   filepath.Join(strings.TrimSpace(string(out)), "git-http-backend"),
		Env:    []string{"GIT_PROJECT_ROOT=" + git.Root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull},
		Stderr: io.Discard,
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "forge.pem"), filepath.Join(dir, "forge.key"))
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.got = append(f.got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		f.mu.Unlock()
		backend.ServeHTTP(w, r)
	}))
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// requests are the requests the forge got, in order.
func (f *realForge) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// branch is the commit of the work item's branch on the forge, "" with none.
func (f *realForge) branch(t *testing.T) string {
	t.Helper()
	return proxygittest.Git(t, f.bare, "for-each-ref", "--format=%(objectname)", realBranch)
}

// realControl is a stand-in for the server of the gateway: the reference receiver in front
// of a file store, with the run configuration [realRunDocument] and the secrets of the
// two credentials, sealed to the control's access key.
type realControl struct {
	srv       *httptest.Server
	accessKey *accesskey.Key
	signer    *accesskey.Key
	// findings, when set, serves the findings of the run API in place of the receiver.
	findings http.HandlerFunc
}

func startRealControl(t *testing.T) *realControl {
	t.Helper()
	return startRealControlWith(t, realRunDocument)
}

// startRealControlWith is [startRealControl] with document as the run configuration.
func startRealControlWith(t *testing.T, document string) *realControl {
	t.Helper()
	accessKey, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c := &realControl{accessKey: accessKey, signer: signer}
	store, err := receiver.OpenFile(filepath.Join(t.TempDir(), "received.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	handler := &receiver.Handler{
		Keys: func(k string) (receiver.AccessKey, bool) {
			return receiver.AccessKey{PublicKey: accessKey.PublicKey(), StoredSecrets: true}, k == realKey
		},
		Signer: signer,
		Store:  store,
		Stop:   func(string) bool { return false },
		Admit:  func(string, string) bool { return true },
		Configuration: func() ([]byte, string) {
			pin, _ := json.Marshal(c.pin())
			doc := `{"version":1,"node_id":"` + realNode + `","workspaces":["` + realWorkspace + `"],"apiary_public_key":` + string(pin) +
				`,"events":{"url":"` + c.srv.URL + `/v1/events","types":["*"]}` +
				`,"run":{"url":"` + c.srv.URL + receiver.DefaultRunPath + `"}` +
				`,"secrets":{"url":"` + c.srv.URL + realSecrets + `"}}`
			return []byte(doc), "sha256=" + strconv.Itoa(len(doc))
		},
		RunConfiguration: func(receiver.Run) ([]byte, string, *receiver.Refusal) {
			return []byte(document), "", nil
		},
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == realSecrets {
			c.answerSecrets(w, r)
			return
		}
		if c.findings != nil && strings.HasSuffix(r.URL.Path, "/findings") {
			c.findings(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *realControl) pin() accesskey.Pin {
	return accesskey.Pin{{Alg: "ed25519", PublicKey: c.signer.PublicKey().String()}}
}

// server is the gateway's view of the control.
func (c *realControl) server() *gateway.Server {
	return &gateway.Server{Version: 1, URL: c.srv.URL, AccessKeyID: realKey, ApiaryPublicKey: c.pin(), AccessKey: c.accessKey, InstanceID: "i_test"}
}

// answerSecrets answers a secrets request as Qory Apiary does: values sealed to the
// access key for the request's run, digest and credentials, and signed.
func (c *realControl) answerSecrets(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sig := r.Header.Get(server.HeaderSignature)
	if r.Method != http.MethodPost || !c.accessKey.PublicKey().VerifyRequest(accesskey.Request{AccessKeyID: r.Header.Get(server.HeaderAccessKeyID),
		InstanceID: r.Header.Get(server.HeaderInstanceID), Method: r.Method, Target: r.URL.RequestURI(), Body: body}, sig) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req server.SecretsRequest
	json.Unmarshal(body, &req)
	values := map[string]string{"work-1": realWorkSecret, "push-1": realPushSecret}
	s := sealed.Sealing{AccessKeyID: realKey, PublicKey: c.accessKey.PublicKey(), RunID: req.RunID, RunConfiguration: req.RunConfiguration,
		Credentials: req.Credentials, Exp: sealed.Expiry(time.Now())}
	for _, name := range req.Credentials {
		s.Values = append(s.Values, sealed.NewValue(name, []byte(values[name])))
	}
	out, err := sealed.Seal(s, c.signer)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(server.HeaderSignature, c.signer.SignAnswer(accesskey.Answer{Status: http.StatusOK, RequestSignature: sig, Body: out}))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

// gitThrough runs git in dir through the proxy p, over https alone, with no other
// configuration, and returns its combined output and error.
func gitThrough(t *testing.T, dir string, p proxygit.Proxy, args ...string) (string, error) {
	t.Helper()
	full := slices.Concat(proxygit.Hardened, proxygit.HTTPSOnly,
		[]string{"-c", "user.name=Example", "-c", "user.email=example@example.com"}, args)
	c := exec.Command("git", full...)
	c.Dir = dir
	c.Env = proxygit.Env([]string{"PATH=" + os.Getenv("PATH")}, &p)
	out, err := c.CombinedOutput()
	return proxygit.Hide(string(out), &p), err
}

// realPush is the run of the child process of the test.
func realPush(t *testing.T, dir string) {
	ctx := context.Background()
	forge := startRealForge(t, dir)
	control := startRealControl(t)
	runs := t.TempDir()
	g, err := gateway.Start(ctx, gateway.Config{
		RunDir:       func(id string) string { return filepath.Join(runs, id) },
		NoLinkSocket: true,
		Report:       func(string) {},
		Server:       control.server(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(ctx)
	r, err := session.Open(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: runs, Report: func(string) {}, Gateway: session.LocalGateway(g.LocalLink())})
	if err != nil {
		t.Fatal(err)
	}
	run := foragerRun{r}
	_, port, _ := net.SplitHostPort(forge.srv.Listener.Addr().String())
	url := "https://localhost:" + port + "/acme/site.git"

	// The fetch goes through the run's proxy, which sets the work credential.
	work := t.TempDir()
	host, err := run.HostProxy()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := gitThrough(t, work, host.proxy, "clone", "--quiet", url, "."); err != nil {
		t.Fatalf("the fetch through the run's proxy failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "left.txt"), []byte("left\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proxygittest.Git(t, work, "add", "left.txt")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "Left by the agent")
	head := proxygittest.Git(t, work, "rev-parse", "HEAD")
	fetched := len(forge.requests())

	// The agent's push goes through the run's proxy as well, and gets no push credential.
	if out, err := gitThrough(t, work, host.proxy, "push", "--quiet", "origin", "HEAD:"+realBranch); err == nil {
		t.Errorf("the push through the run's proxy passed:\n%s", out)
	}
	if err := host.close(); err != nil {
		t.Error(err)
	}
	basic := func(secret string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret))
	}
	viaHost := forge.requests()[fetched:]
	for _, got := range viaHost {
		if strings.HasPrefix(got, "POST ") || strings.HasSuffix(got, basic(realPushSecret)) {
			t.Errorf("the push through the run's proxy reached the forge with %q", got)
		}
	}
	if got := forge.branch(t); got != "" {
		t.Errorf("the forge has the work item's branch at %s after the push through the run's proxy", got)
	}

	// The push step's push goes through the push proxy, which sets the push credential.
	push, err := run.PushProxy()
	if err != nil {
		t.Fatal(err)
	}
	before := len(forge.requests())
	if out, err := gitThrough(t, work, push.proxy, "push", "--quiet", "origin", "HEAD:"+realBranch); err != nil {
		t.Fatalf("the push through the push proxy failed: %v\n%s", err, out)
	}
	if err := push.close(); err != nil {
		t.Error(err)
	}
	if got := forge.branch(t); got != head {
		t.Errorf("the forge has the work item's branch at %q, want %s", got, head)
	}
	want := "POST /acme/site.git/git-receive-pack " + basic(realPushSecret)
	if got := forge.requests()[before:]; !slices.Contains(got, want) || slices.Contains(got, strings.TrimSuffix(want, basic(realPushSecret))+basic(realWorkSecret)) {
		t.Errorf("the forge got\n%q\nfrom the push through the push proxy, want %q and no push with the work credential", got, want)
	}
	if _, err := r.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
}

// envRealHead is [envRealPush] for [TestForagerRunReadsTheBranchHeadWithTheReadCredential].
const envRealHead = "QORY_TEST_REAL_HEAD"

// TestForagerRunReadsTheBranchHeadWithTheReadCredential is a run at a real Forager gateway
// whose push credential has no path for git-upload-pack. The read of the branch's head, which
// under git's protocol v2 posts to git-upload-pack, goes through the run's host proxy and
// reaches the forge with the read credential and never the push credential, both while the
// branch is missing and after the push; the push through the push proxy, a push with the
// lease on the head, succeeds. It is the setup of [TestForagerRunKeepsThePushCredentialOnThePushProxyAlone],
// and starts this binary again for the same reason; it skips off Linux.
func TestForagerRunReadsTheBranchHeadWithTheReadCredential(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Go reads the authorities of SSL_CERT_FILE on Linux alone, and the gateway must trust the test forge's")
	}
	if dir := realSetup(t, envRealHead); dir != "" {
		realHead(t, dir)
		return
	}
	startRealChild(t, "q-rh", "TestForagerRunReadsTheBranchHeadWithTheReadCredential", envRealHead)
}

// realHead is the run of the child process of [TestForagerRunReadsTheBranchHeadWithTheReadCredential].
func realHead(t *testing.T, dir string) {
	ctx := context.Background()
	forge := startRealForge(t, dir)
	control := startRealControlWith(t, narrowRunDocument)
	runs := t.TempDir()
	g, err := gateway.Start(ctx, gateway.Config{
		RunDir:       func(id string) string { return filepath.Join(runs, id) },
		NoLinkSocket: true,
		Report:       func(string) {},
		Server:       control.server(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(ctx)
	r, err := session.Open(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: runs, Report: func(string) {}, Gateway: session.LocalGateway(g.LocalLink())})
	if err != nil {
		t.Fatal(err)
	}
	run := foragerRun{r}
	_, port, _ := net.SplitHostPort(forge.srv.Listener.Addr().String())
	url := "https://localhost:" + port + "/acme/site.git"
	basic := func(secret string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+secret))
	}
	// wantRead fails the test for a request since mark that carries any credential but the read one.
	wantRead := func(what string, mark int) {
		t.Helper()
		got := forge.requests()[mark:]
		if len(got) == 0 {
			t.Errorf("%s: the forge got no request", what)
		}
		for _, request := range got {
			if !strings.HasSuffix(request, basic(realWorkSecret)) {
				t.Errorf("%s: the forge got %q, want the read credential alone", what, request)
			}
		}
	}

	host, err := run.HostProxy()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if out, err := gitThrough(t, work, host.proxy, "clone", "--quiet", url, "."); err != nil {
		t.Fatalf("the fetch through the run's proxy failed: %v\n%s", err, out)
	}
	mark := len(forge.requests())
	if out, err := gitThrough(t, work, host.proxy, "ls-remote", "--", url, "refs/heads/qory/wi_01"); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("the head read of a missing branch: %v, %q, want no head", err, out)
	}
	wantRead("the head read of a missing branch", mark)

	if err := os.WriteFile(filepath.Join(work, "left.txt"), []byte("left\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proxygittest.Git(t, work, "add", "left.txt")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "Left by the agent")
	head := proxygittest.Git(t, work, "rev-parse", "HEAD")

	push, err := run.PushProxy()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := gitThrough(t, work, push.proxy, "push", "--quiet", "origin", "HEAD:"+realBranch); err != nil {
		t.Fatalf("the first push through the push proxy failed: %v\n%s", err, out)
	}
	mark = len(forge.requests())
	out, err := gitThrough(t, work, host.proxy, "ls-remote", "--", url, realBranch)
	if err != nil || !strings.HasPrefix(out, head) {
		t.Fatalf("the head read after the push: %v, %q, want %s", err, out, head)
	}
	wantRead("the head read after the push", mark)

	// The lease push: a second commit, with a lease on the head that the read returned.
	if err := os.WriteFile(filepath.Join(work, "more.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proxygittest.Git(t, work, "add", "more.txt")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "More")
	next := proxygittest.Git(t, work, "rev-parse", "HEAD")
	mark = len(forge.requests())
	if out, err := gitThrough(t, work, push.proxy, "push", "--quiet", "--force-with-lease="+realBranch+":"+head, "origin", "HEAD:"+realBranch); err != nil {
		t.Fatalf("the lease push through the push proxy failed: %v\n%s", err, out)
	}
	if got := forge.branch(t); got != next {
		t.Errorf("the forge has the branch at %q, want %s", got, next)
	}
	want := "POST /acme/site.git/git-receive-pack " + basic(realPushSecret)
	if got := forge.requests()[mark:]; !slices.Contains(got, want) {
		t.Errorf("the forge got\n%q\nfrom the lease push, want %q", got, want)
	}
	if err := host.close(); err != nil {
		t.Error(err)
	}
	if err := push.close(); err != nil {
		t.Error(err)
	}
	if _, err := r.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
}
