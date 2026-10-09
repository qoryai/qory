package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/link"

	"github.com/qoryai/qory/cmd"
)

// linkSecret makes every run in the test hand the test the gateway's link secret too,
// and returns where it lands; the hook restores itself.
func linkSecret(t *testing.T) *string {
	t.Helper()
	var secret string
	t.Cleanup(cmd.SetLinkHanded(func(l link.Local) { secret = l.Secret }))
	return &secret
}

// noSecretUnder fails the test for every file under dir that holds secret.
func noSecretUnder(t *testing.T, dir, secret string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), secret) {
			t.Errorf("%s holds the link secret", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRunKeepsTheLinkSecretInMemory runs a runtime that writes down its environment and
// its arguments: the gateway's link secret, which qory hands its session in memory, is
// in neither, in no file of the run's record or of qory's state, and in nothing qory
// prints.
func TestRunKeepsTheLinkSecretInMemory(t *testing.T) {
	secret := linkSecret(t)
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	seen := filepath.Join(t.TempDir(), "seen")
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\nenv > "+seen+"\necho \"$0 $*\" >> "+seen+"\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *secret == "" {
		t.Fatal("the run handed its session no link secret")
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "QORY_RUN_SOCKET=") {
		t.Fatalf("the runtime's environment lacks the run's socket:\n%s", data)
	}
	lacks(t, string(data), *secret)
	lacks(t, out, *secret)
	noSecretUnder(t, runsDir(t, root), *secret)
	noSecretUnder(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), *secret)
}

// TestRunBehindAWallKeepsTheLinkSecretInMemory runs behind the wall, with docker's stand-in
// writing down every command line and environment file it is given: the link secret is
// in none of them, in no file of the run's record or of qory's state, and in nothing qory
// prints.
func TestRunBehindAWallKeepsTheLinkSecretInMemory(t *testing.T) {
	secret := linkSecret(t)
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if *secret == "" {
		t.Fatal("the run handed its session no link secret")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "env: ")
	lacks(t, string(data), *secret)
	lacks(t, out, *secret)
	if err != nil {
		lacks(t, err.Error(), *secret)
	}
	noSecretUnder(t, runsDir(t, root), *secret)
	noSecretUnder(t, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), *secret)
}
