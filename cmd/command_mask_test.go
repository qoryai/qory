//go:build unix

package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/mask"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/secretmask"
)

// shapedKey is a key of a shape that Forager's mask package lists, a GitHub personal
// access token, which no command reads: each command prints it as ***.
const shapedKey = "ghp_0123456789abcdefghijABCDEFGHIJklmnop"

// proxyURL is a forwarder's URL with Forager's placeholder for its password, which
// each command prints as it is.
const proxyURL = "http://qory:" + mask.ProxyPassword + "@127.0.0.1:3128"

// TestGatewayMasksItsLines is qory gateway with a line of Forager's gateway that
// contains the access key's secret, which qory read from access-key-secret, a key of a
// listed shape and the placeholder of a forwarder's password: the secret and the key
// print as ***, and the placeholder as it is. The test's start of the gateway prints the
// line in place of Forager's gateway.
func TestGatewayMasksItsLines(t *testing.T) {
	emptyDir(t)
	srv := newGatewayServer(t)
	serverFile(t, srv, "  listen: 127.0.0.1:0\n"+gatewayTLS)
	writeCertificate(t)
	defer cmd.SetStartGateway(func(cfg gateway.Config) error {
		cfg.Report("sent with " + srv.key.Secret() + " and " + shapedKey + " through " + proxyURL)
		return errors.New("the test stops the gateway")
	})()
	out, err := run(t, "gateway")
	if err == nil {
		t.Fatalf("the gateway did not stop:\n%s", out)
	}
	wants(t, out, "qory gateway: sent with *** and *** through "+proxyURL+"\n")
	lacks(t, out, srv.key.Secret()[4:], shapedKey[4:])
}

// TestRegisterMasksItsLines is qory run register with an --instance-id that starts
// with a key of a listed shape and ends with the placeholder of a forwarder's password:
// its node line prints the key as *** and the placeholder as it is. The access key's
// secret, which it read from access-key-secret, and the run key that it wrote to its
// descriptor, are in its mask list. Forager stops on a discovery that contains the
// secret, and register prints no run key, so the test checks the list for those two.
func TestRegisterMasksItsLines(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	instance := shapedKey + "." + mask.ProxyPassword
	p := newKeyPipe(t)
	stdout, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", p.fd(), "--run-id", id, "--instance-id", instance)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	wants(t, stderr, "qory run register: node "+testPool+", instance ***."+mask.ProxyPassword+"\n")
	lacks(t, stdout+stderr, shapedKey[4:])
	if key := p.read(t); secretmask.Default().Replace(key) != "***\n" {
		t.Error("the run key is not in qory run register's mask list")
	}
	if secretmask.Default().Replace(srv.key.Secret()) != "***" {
		t.Error("the access key's secret is not in qory run register's mask list")
	}
}

// TestEndPutsTheAccessKeysSecretInItsMaskList is qory run end of a registered run: the
// access key's secret, which it read from access-key-secret, is in its mask list. The
// lines of qory run end contain only the node, the run id and the reason, whose formats
// fit neither the secret nor a key of a listed shape, so the test checks the list.
func TestEndPutsTheAccessKeysSecretInItsMaskList(t *testing.T) {
	srv := starterServer(t, "https://gateway.example:8443", "")
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	if _, stderr, err := runSplit(t, "", "run", "register", "--run-key-fd", newKeyPipe(t).fd(), "--run-id", id); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if _, stderr, err := runSplit(t, "", "run", "end", id, "--outcome", "completed"); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if secretmask.Default().Replace(srv.key.Secret()) != "***" {
		t.Error("the access key's secret is not in qory run end's mask list")
	}
}

// TestResendMasksItsLines is qory run resend of a run this machine registered through
// gateway.server.via, whose wall's container engine fails with a key of a listed shape
// and the placeholder of a forwarder's password: the key prints as ***, the placeholder
// as it is. Forager replaces a run key in its lines with a phrase, so the test checks
// that the run key qory kept is in the resend's mask list.
func TestResendMasksItsLines(t *testing.T) {
	root := runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	gw := newKeyedGateway(t)
	srv := newFakeServer(t, "")
	via := newViaGateway(t, srv.URL)
	docker := filepath.Join(t.TempDir(), "docker")
	writeFile(t, docker, "#!/bin/sh\necho 'cannot list for "+shapedKey+" through "+proxyURL+"' >&2\nexit 1\n")
	if err := os.Chmod(docker, 0o755); err != nil {
		t.Fatal(err)
	}
	serverFile(t, srv, via.via()+"wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	const id = "0192f0c1-7d4e-7a2b-8c3d-555555555555"
	behindRecord(t, root, id, false)
	key := mintRunKey(t, id, gw.url())
	if err := foragerdir.Dir(configDir()).WriteRunKey(id, key); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run", "resend", id)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "qory run resend: removing what the wall left: ", "cannot list for *** through "+proxyURL)
	lacks(t, out, shapedKey[4:], key[4:40])
	if got := secretmask.Default().Replace(key); got != "***" {
		t.Errorf("the kept run key is not in qory run resend's mask list: %d bytes come out", len(got))
	}
}
