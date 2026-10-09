package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
)

// fixtureSecret is the Forager contract's published fixture access key secret, which
// qory refuses as a machine's own.
const fixtureSecret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// serverRun is a checkout composed for the fake runtime, which exits 0, with a fake
// server in forager.yaml and the machine's access key beside it.
func serverRun(t *testing.T, policy, more string) (string, *fakeServer) {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	srv := newFakeServer(t, policy)
	serverFile(t, srv, more)
	return root, srv
}

// configDir is the directory of forager.yaml of the test's environment.
func configDir() foragerdir.Dir {
	return foragerdir.Dir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory"))
}

// clearRuns removes the checkout's run records, so the next run is the one recorded.
func clearRuns(t *testing.T, root string) {
	t.Helper()
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
}

var instanceShape = regexp.MustCompile(`^i_[A-Za-z0-9_-]{22}$`)

// TestRunSignsWithTheAccessKeyAndNamesTheInstance is a run against a server: every
// request is signed under the machine's access key, carries the instance id qory keeps
// in instance-id, mode 0600, and the display name, session.instance.name or the host name; qory
// prints the node discovery lists. The next run keeps the id.
func TestRunSignsWithTheAccessKeyAndNamesTheInstance(t *testing.T) {
	root, srv := serverRun(t, "", "session:\n  instance:\n    name: build-01\n")
	out, err := run(t, "run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := configDir()
	b, err := os.ReadFile(dir.Path(foragerdir.InstanceFile))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.SplitN(string(b), "\n", 2)[0]
	if !instanceShape.MatchString(id) {
		t.Fatalf("instance-id holds %q", b)
	}
	if info, _ := os.Stat(dir.Path(foragerdir.InstanceFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("instance-id is mode %v", info.Mode())
	}
	wants(t, out, "qory run: node "+testNode+", instance "+id, "claude exited 0")
	if srv.refused != 0 || len(srv.instances) < 3 {
		t.Fatalf("refused %d, requests %v", srv.refused, srv.instances)
	}
	for _, in := range srv.instances {
		if in != [2]string{id, "build-01"} {
			t.Errorf("a request named the instance %v", in)
		}
	}
	clearRuns(t, root)
	serverFile(t, srv, "")
	srv.instances = nil
	if out, err := run(t, "run"); err != nil || !strings.Contains(out, "instance "+id) {
		t.Fatalf("the second run: %v\n%s", err, out)
	}
	host, _ := os.Hostname()
	if want := accesskey.DefaultName(host); srv.instances[0] != [2]string{id, want} {
		t.Errorf("without session.instance.name the request named %v, want %q", srv.instances[0], want)
	}
}

// TestRunRegeneratesAnInstanceIDThatIsNotThisMachines is an instance-id file whose id
// fails the pattern, one whose hash is another machine's, and one that is a link: each
// is replaced by a new id. In a directory that cannot be written, the id is the
// process's own and qory says so.
func TestRunRegeneratesAnInstanceIDThatIsNotThisMachines(t *testing.T) {
	root, srv := serverRun(t, "", "")
	dir := configDir()
	path := dir.Path(foragerdir.InstanceFile)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, elsewhere, string(accesskey.InstanceFile("i_fromElsewhere", foragerdir.MachineID())))
	for name, write := range map[string]func(){
		"a bad id": func() { writeFile(t, path, "../x\n"+accesskey.MachineHash(foragerdir.MachineID())+"\n") },
		"another machine's": func() {
			writeFile(t, path, string(accesskey.InstanceFile("i_copiedWithTheHome", []byte("another-machine"))))
		},
		"a link":             func() { os.Remove(path); os.Symlink(elsewhere, path) },
		"one line":           func() { writeFile(t, path, "i_justTheId\n") },
		"an empty directory": func() { os.Remove(path) },
	} {
		write()
		clearRuns(t, root)
		srv.instances = nil
		out, err := run(t, "run")
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		id, ok := dir.ReadInstanceID(foragerdir.MachineID())
		if !ok || !instanceShape.MatchString(id) || srv.instances[0][0] != id {
			t.Errorf("%s: the file holds %q, the server saw %v", name, id, srv.instances)
		}
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: instance-id is %v, %v", name, info, err)
		}
	}
	if b, _ := os.ReadFile(elsewhere); !strings.HasPrefix(string(b), "i_fromElsewhere\n") {
		t.Errorf("the link's target was written: %q", b)
	}

	t.Run("a read-only directory", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root writes a directory whatever its mode")
		}
		os.Remove(path)
		if err := os.Chmod(string(dir), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(string(dir), 0o700) })
		clearRuns(t, root)
		srv.instances = nil
		out, err := run(t, "run")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) || !instanceShape.MatchString(srv.instances[0][0]) {
			t.Errorf("%v, the server saw %v", err, srv.instances)
		}
		wants(t, out, "qory run: instance "+srv.instances[0][0]+", this process's own: "+path+" cannot be written")
	})
}

// TestRunRefusesAnAccessKeySecretTheRulesRefuse is access-key-secret that grants the
// group or others anything, is a link, holds the published fixture key or no secret, a
// directory others may read, and a server with no secret or no access key id: each is
// refused before any request, and no error contains a secret.
func TestRunRefusesAnAccessKeySecretTheRulesRefuse(t *testing.T) {
	root, srv := serverRun(t, "", "")
	dir := configDir()
	path := dir.Path(foragerdir.SecretFile)
	elsewhere := filepath.Join(t.TempDir(), "secret")
	writeFile(t, elsewhere, srv.key.Secret()+"\n")
	if err := os.Chmod(elsewhere, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		setup func()
		want  string
	}{
		{"group-readable", func() { os.Chmod(path, 0o640) }, path + " is mode 0640, which grants access to the group or others: chmod 600 " + path},
		{"world-readable", func() { os.Chmod(path, 0o604) }, "is mode 0604"},
		{"a link", func() { os.Remove(path); os.Symlink(elsewhere, path) }, path + " is a symbolic link; it must be the file itself"},
		{"the fixture", func() { os.Remove(path); writeFile(t, path, fixtureSecret+"\n"); os.Chmod(path, 0o600) }, path + ": it holds the Forager contract's published fixture key, whose secret anyone can read"},
		{"no secret", func() { os.Remove(path); writeFile(t, path, "ak_f1xt0re000000000\n"); os.Chmod(path, 0o600) }, path + ": not an access key secret: one line, qak_ and 43 characters of base64url"},
		{"two lines", func() { os.Remove(path); writeFile(t, path, srv.key.Secret()+"\n\n"); os.Chmod(path, 0o600) }, "not an access key secret"},
		{"the directory", func() { os.Chmod(string(dir), 0o755) }, string(dir) + " is mode 0755, which grants access to the group or others; it holds the access key's secret: chmod 700 " + string(dir)},
		{"none", func() { os.Remove(path) }, "no access key secret for the server " + srv.URL + ": qory access-key enrol makes one, or set QORY_ACCESS_KEY_SECRET"},
	} {
		writeSecret(t, srv.key)
		c.setup()
		clearRuns(t, root)
		out, err := run(t, "run")
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), c.want, out)
		}
		if err != nil && (strings.Contains(err.Error(), srv.key.Secret()[4:]) || strings.Contains(err.Error(), fixtureSecret[4:])) {
			t.Errorf("%s: the error contains the secret", c.name)
		}
		os.Chmod(string(dir), 0o700)
	}
	if srv.refused != 0 || len(srv.instances) != 0 {
		t.Errorf("a refused run sent %d requests", len(srv.instances))
	}
	writeSecret(t, srv.key)
	writeFile(t, filepath.Join(string(dir), "forager.yaml"), "gateway:\n  server:\n    url: "+srv.URL+"\n    apiary_public_key: "+pinLine(srv.signer)+"\n")
	if _, err := run(t, "run"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "forager.yaml: the server has no access_key_id: qory access-key enrol writes it, or set gateway.server.access_key_id or QORY_ACCESS_KEY_ID") {
		t.Errorf("no access key id: %v", err)
	}
}

// TestRunTakesTheSecretFromTheEnvironment is QORY_ACCESS_KEY_SECRET, which wins over the
// file, and one that is not a secret, refused without being quoted. The variables are
// gone from what the run starts.
func TestRunTakesTheSecretFromTheEnvironment(t *testing.T) {
	root, srv := serverRun(t, "", "")
	writeSecret(t, newKey(t))
	t.Setenv("QORY_ACCESS_KEY_SECRET", srv.key.Secret())
	if out, err := run(t, "run"); err != nil || srv.refused != 0 {
		t.Fatalf("the secret from the environment: %v, %d refused\n%s", err, srv.refused, out)
	}
	if _, ok := os.LookupEnv("QORY_ACCESS_KEY_SECRET"); ok {
		t.Error("the secret stayed in qory's environment")
	}
	for _, v := range []string{fixtureSecret, "qak_short", srv.key.Secret() + " "} {
		t.Setenv("QORY_ACCESS_KEY_SECRET", v)
		clearRuns(t, root)
		_, err := run(t, "run")
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.HasPrefix(err.Error(), "QORY_ACCESS_KEY_SECRET: ") || strings.Contains(err.Error(), v[4:]) {
			t.Errorf("%.8s…: %v", v, err)
		}
	}
}

// TestRunSaysWhatARefusalMeans is each refusal of the server and of Forager at the
// start, said with what to do, its code and exit status 1, and nothing run: an access
// key the server does not know, from access-key-secret, which enrol --replace moves
// from, or from QORY_ACCESS_KEY_SECRET, an instance beyond the node's limit, an answer
// that does not verify under the pin, and no pin.
func TestRunSaysWhatARefusalMeans(t *testing.T) {
	root, srv := serverRun(t, "", "")
	refusal := func(name string, want ...string) {
		t.Helper()
		clearRuns(t, root)
		out, err := run(t, "run")
		for _, w := range want {
			if cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %v (exit %d), want %q\n%s", name, err, cmd.ExitCode(err), w, out)
			}
		}
		if strings.Contains(out, "hello from") {
			t.Errorf("%s: the runtime ran", name)
		}
	}
	writeSecret(t, newKey(t))
	refusal("unknown", "the server refused a request signed with the access key ", ": it does not know the key, has revoked it, or this machine's clock is more than five minutes off; check the clock, else move this machine to a new key: qory access-key enrol --replace "+srv.URL+" <code> (", "unauthorized (status 401)")
	writeSecret(t, srv.key)
	t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
	refusal("unknown, from the environment", "the server refused a request signed with the access key ", ": it does not know the key, has revoked it, or this machine's clock is more than five minutes off; check the clock, else enrol a new key with qory access-key enrol (", "unauthorized (status 401)")
	os.Unsetenv("QORY_ACCESS_KEY_SECRET")

	srv.full = true
	refusal("full", "the node's live instances have reached its limit, so the instance i_", "does not start: wait for a run of another instance to end, or have an owner or administrator in Qory Apiary clear that instance (", "instance_limit (status 409)")
	srv.full = false

	serverFile(t, srv, "")
	writeFile(t, filepath.Join(string(configDir()), "forager.yaml"), "gateway:\n  server:\n    url: "+srv.URL+"\n    access_key_id: "+testAccessKey+"\n    apiary_public_key: "+pinLine(newKey(t))+"\n")
	refusal("another pin", "an answer of the server does not verify under the pinned apiary_public_key, so the run does not start: check gateway.server.url and the pin (", "answer_unsigned")

	writeFile(t, filepath.Join(string(configDir()), "forager.yaml"), "gateway:\n  server:\n    url: "+srv.URL+"\n    access_key_id: "+testAccessKey+"\n")
	refusal("no pin", "the server has no pinned apiary_public_key, so no answer of it could be verified: qory access-key enrol writes it, or set gateway.server.apiary_public_key in forager.yaml or QORY_APIARY_PUBLIC_KEY (", "apiary_public_key_missing")
	if srv.refused != 2 {
		t.Errorf("the server refused %d requests, want the unknown keys' two", srv.refused)
	}
}

// TestA410ToThePingIsNoRun is a server that answers the ping with a signed 410: it
// wants nothing of the run, so the run does not start. That is no refusal, and no
// server closes a run: qory prints the gateway's error, which names the events URL and
// the status, then the record, and exits 1. The runtime never runs.
func TestA410ToThePingIsNoRun(t *testing.T) {
	_, srv := serverRun(t, "", "")
	srv.stopPing = true
	out, err := run(t, "run")
	want := "ping " + srv.URL + "/v1/events: status 410: the server did not accept the ping"
	if cmd.ExitCode(err) != 1 || err == nil || err.Error() != want {
		t.Errorf("a 410 to the ping: %v (exit %d), want %q, exit 1\n%s", err, cmd.ExitCode(err), want, out)
	}
	wants(t, out, "✗ "+want+"\n", "qory run: the record is in ")
	lacks(t, out, "hello from", "closed the run")
	if got := srv.byType(); len(got) != 0 {
		t.Errorf("the server stored events of a run it did not accept: %v", got)
	}
}

// TestA410MidRunLeavesTheRuntimeRunning is a server that answers a signed 410 once it
// holds the run's ping: the gateway sends it nothing more and says so once, and the
// runtime runs on, here until the 410 has come and after, to its own exit, which is the
// run's. The run's record keeps every event, and delivered.log says stopped.
func TestA410MidRunLeavesTheRuntimeRunning(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	stopped := filepath.Join(t.TempDir(), "stopped")
	script := filepath.Join(t.TempDir(), "patient-runtime")
	writeFile(t, script, "#!/bin/sh\nfor i in $(seq 300); do\n  if [ -e '"+stopped+"' ]; then echo 'hello from after the 410'; exit 0; fi\n  sleep 0.1\ndone\nexit 7\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	srv.stopRun = true
	// onStop runs on the server's goroutine, where t.Fatal would not end the test.
	srv.onStop = func() {
		if err := os.WriteFile(stopped, nil, 0o644); err != nil {
			t.Errorf("writing %s: %v", stopped, err)
		}
	}
	out, err := run(t, "run")
	if err != nil {
		t.Fatalf("a 410 mid-run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: the server answered 410; no further batch is sent for this run, which goes on\n", "hello from after the 410\n", "✓ claude exited 0\n")
	lacks(t, out, "closed the run", "was stopped", "did not reach the server")
	if n := strings.Count(out, "the server answered 410"); n != 1 {
		t.Errorf("the 410 is said %d times, want once\n%s", n, out)
	}
	if got := srv.byType(); len(got) != 1 || len(got["dev.qory.ping"]) != 1 {
		t.Errorf("the server stored more than the ping: %v", got)
	}
	dir, byType := events(t, root)
	for _, typ := range []string{"dev.qory.run.started", "dev.qory.run.exited"} {
		if len(byType[typ]) != 1 {
			t.Errorf("the record holds %d %s, want 1", len(byType[typ]), typ)
		}
	}
	if ex := byType["dev.qory.run.exited"]; len(ex) == 1 && (ex[0]["exit_code"] != float64(0) || ex[0]["reason"] != nil) {
		t.Errorf("run.exited: %v, want exit_code 0 and no reason", ex[0])
	}
	if b, err := os.ReadFile(filepath.Join(dir, "delivered.log")); err != nil || !strings.HasSuffix(string(b), "\nstopped\n") {
		t.Errorf("delivered.log: %q, %v; want it to end stopped", b, err)
	}
}

// TestTheMarkerKeepsEveryUnwalledRunOut is the stored-secrets marker: an unwalled run
// that contacts no server is refused before it starts, --local included, and one with a
// server once discovery is read, under a key that leaves the marker as it is: the
// secret from the environment. (Under the secret of access-key-secret a discovery that
// lists no secrets removes the marker, TestDiscoverySettlesTheMarker.) A run's lock
// file names it unwalled and stays for a key command to remove.
func TestTheMarkerKeepsEveryUnwalledRunOut(t *testing.T) {
	root, srv := serverRun(t, "", "")
	dir := configDir()
	if err := dir.WriteMarker(); err != nil {
		t.Fatal(err)
	}
	marker := dir.Path(foragerdir.MarkerFile)
	want := "the stored-secrets marker " + marker + " exists: this machine's access key may receive stored secrets, so every run needs a wall: --wall docker, or wall in forager.yaml (server_needs_wall)"
	for _, c := range []struct {
		name, secret string
		args         []string
	}{
		{"--local", "", []string{"run", "--local"}},
		{"a key from the environment", srv.key.Secret(), []string{"run"}},
	} {
		t.Setenv("QORY_ACCESS_KEY_SECRET", c.secret)
		clearRuns(t, root)
		out, err := run(t, c.args...)
		if cmd.ExitCode(err) != 1 || err == nil || err.Error() != want {
			t.Errorf("%s: %v (exit %d)\n%s", c.name, err, cmd.ExitCode(err), out)
		}
		if strings.Contains(out, "hello from") {
			t.Errorf("%s: the runtime ran", c.name)
		}
		if has, _ := dir.HasMarker(); !has {
			t.Errorf("%s: the marker is gone", c.name)
		}
	}
	if got := srv.byType(); len(got["dev.qory.ping"]) != 0 {
		t.Errorf("a refused run pinged: %v", got)
	}
	t.Setenv("QORY_ACCESS_KEY_SECRET", "")
	writeFile(t, filepath.Join(string(dir), "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n")
	clearRuns(t, root)
	if _, err := run(t, "run"); cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), "server_needs_wall") {
		t.Errorf("a forager.yaml without a server: %v", err)
	}

	dir.RemoveMarker()
	clearRuns(t, root)
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("without the marker: %v\n%s", err, out)
	}
	entries, _ := os.ReadDir(runsDir(t, root))
	b, err := os.ReadFile(filepath.Join(dir.Path(foragerdir.LocksDir), entries[0].Name()+".lock"))
	if err != nil || string(b) != "unwalled\n" {
		t.Errorf("the run's lock file: %q, %v", b, err)
	}
}

// TestDiscoverySettlesTheMarker is the marker rule of a signed discovery under the
// secret of access-key-secret: one that lists secrets writes it and refuses the
// unwalled run after reading it; one that lists none deletes the moved-aside secrets
// and removes the marker, so the run goes on; a key the server has revoked leaves both.
func TestDiscoverySettlesTheMarker(t *testing.T) {
	root, srv := serverRun(t, "", "")
	dir := configDir()
	srv.secrets = true
	if _, err := run(t, "run"); cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), "the server lists stored secrets for this machine's access key, so every run needs a wall") {
		t.Errorf("discovery with secrets: %v", err)
	}
	if has, _ := dir.HasMarker(); !has {
		t.Error("discovery with secrets wrote no marker")
	}
	srv.secrets = false
	old := dir.Path(foragerdir.OldPrefix + "1700000000")
	writeFile(t, old, newKey(t).Secret()+"\n")
	srv.revoked = true
	clearRuns(t, root)
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), "unauthorized (status 401)") {
		t.Errorf("a revoked key: %v", err)
	}
	if has, _ := dir.HasMarker(); !has {
		t.Error("an unsuccessful discovery removed the marker")
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("an unsuccessful discovery deleted the secret moved aside")
	}
	srv.revoked = false
	clearRuns(t, root)
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("discovery without secrets: %v\n%s", err, out)
	}
	if has, _ := dir.HasMarker(); has {
		t.Error("discovery without secrets kept the marker")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the secret moved aside is still there")
	}
}

// TestARunWaitsForAKeyCommand is a run that starts while a key command holds the key
// lock: it waits, and goes on once the lock is dropped.
func TestARunWaitsForAKeyCommand(t *testing.T) {
	_, _ = serverRun(t, "", "")
	lock, err := configDir().LockKey(true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := run(t, "run", "--local")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the run did not wait: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	lock.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the run after the lock: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run never started")
	}
}
