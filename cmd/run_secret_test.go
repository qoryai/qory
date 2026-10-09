package cmd_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noSecretIn fails the test when text holds secret, naming where without printing
// either.
func noSecretIn(t *testing.T, where, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Errorf("%s holds the run secret", where)
	}
}

// noRunSecretUnder fails the test when a file under dir other than a run directory's
// run-secret holds secret.
func noRunSecretUnder(t *testing.T, dir, secret string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() == "run-secret" {
			return nil
		}
		data, _ := os.ReadFile(path)
		noSecretIn(t, path, string(data), secret)
		return nil
	})
}

// keptSecret is the run secret the run directory dir keeps in run-secret, which must be
// mode 0600 and hold the secret and a newline.
func keptSecret(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "run-secret")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the run directory keeps no run-secret: %v", err)
	}
	if mode := info.Mode(); mode != 0o600 {
		t.Errorf("run-secret is mode %v, want 0600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := strings.CutSuffix(string(data), "\n")
	if !ok || len(secret) < 22 || strings.ContainsAny(secret, " \n") {
		t.Fatal("run-secret holds no run secret and a newline")
	}
	return secret
}

// TestTheRunSecretStaysWithTheLink is a run behind a separate gateway whose run answer
// gives a known run secret, here the fake link's, and whose later batches cannot be
// sent: the runtime takes the run credential's file away. The secret is in qory's
// output, the runtime's environment and arguments, qory config and the record nowhere;
// the run directory keeps it in run-secret alone, mode 0600, as the events are owed. A
// resend carries it on every batch, says it nowhere, and the file is gone once the
// gateway took everything.
func TestTheRunSecretStaysWithTheLink(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	const secret = "marker-run-secret-0123456789abcdefghijkl"
	file := filepath.Join(t.TempDir(), "run-credential")
	seen := t.TempDir()
	runtime := filepath.Join(t.TempDir(), "offline-runtime")
	writeFile(t, runtime, "#!/bin/sh\nenv > '"+filepath.Join(seen, "env")+"'\necho \"$@\" > '"+filepath.Join(seen, "args")+"'\nsleep 1\nrm -f '"+file+"'\nsleep 1\necho done\nexit 0\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link := newFakeLink(t)
	link.interval = 1
	link.runStatus = http.StatusOK
	link.runBody = `{"version":1,"run_id":"{run_id}","credential":"issuer","labels":{"forge":"git.example.com","repository":"acme/app"},"applied":{"mode":"observe","allow":[],"source":"none"},"proxy_secret":"example-proxy-secret-000000000000000000001","run_secret":"` + secret + `"}`
	writeFile(t, foragerFile(), sessionGateway(strings.TrimPrefix(link.URL, "https://"), link.ca, "    run_credential_file: "+file+"\n"))
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	writeRunCredential(t, file, credential+"\n")
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5eb0"
	out, err := run(t, "run", "--run-id", id)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	noSecretIn(t, "qory run's output", out, secret)
	for _, name := range []string{"env", "args"} {
		data, err := os.ReadFile(filepath.Join(seen, name))
		if err != nil {
			t.Fatalf("the runtime did not run: %v", err)
		}
		noSecretIn(t, "the runtime's "+name, string(data), secret)
	}
	link.wantSecret(t, "the run", secret)
	dir := filepath.Join(runsDir(t, root), id)
	if spooled, _ := os.ReadDir(filepath.Join(dir, "undelivered")); len(spooled) == 0 {
		t.Fatal("the run left nothing under undelivered/")
	}
	if keptSecret(t, dir) != secret {
		t.Error("run-secret holds another secret than the run answer's")
	}
	noRunSecretUnder(t, os.Getenv("XDG_STATE_HOME"), secret)
	if out, err := run(t, "config"); err != nil {
		t.Errorf("qory config: %v\n%s", err, out)
	} else {
		noSecretIn(t, "qory config's output", out, secret)
	}

	writeRunCredential(t, file, credential+"\n")
	link.answer(http.StatusOK, "")
	out, err = run(t, "run", "resend", id)
	if err != nil || !strings.Contains(out, " events were accepted; nothing is left to send to the gateway") {
		t.Fatalf("the resend: %v\n%s", err, out)
	}
	noSecretIn(t, "qory run resend's output", out, secret)
	link.mu.Lock()
	batches := link.batches
	link.mu.Unlock()
	if batches == 0 {
		t.Error("no batch of the resend reached the gateway")
	}
	link.wantSecret(t, "the resend", secret)
	if _, err := os.Stat(filepath.Join(dir, "run-secret")); err == nil {
		t.Error("run-secret is left after the gateway took everything")
	}
	noRunSecretUnder(t, os.Getenv("XDG_STATE_HOME"), secret)
}

// TestARunWithAGatewayOfItsOwnKeepsNoRunSecret is a run with the gateway qory starts
// for it, on the local link: its run directory keeps no run-secret.
func TestARunWithAGatewayOfItsOwnKeepsNoRunSecret(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir, _ := events(t, root)
	if _, err := os.Stat(filepath.Join(dir, "run-secret")); err == nil {
		t.Error("the run directory keeps a run-secret on the local link")
	}
}
