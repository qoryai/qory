package cmd_test

import (
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestRunTakesTheSecretFromADescriptor is --access-key-secret-fd: the secret read from
// the descriptor wins over QORY_ACCESS_KEY_SECRET and the file, for qory run and qory
// run resend; the standard descriptors and one that is not open are refused, and a
// value that is not a secret is refused without being quoted.
func TestRunTakesTheSecretFromADescriptor(t *testing.T) {
	root, srv := serverRun(t, "", "")
	writeSecret(t, newKey(t))
	t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	if out, err := run(t, "run", "--run-id", id, "--access-key-secret-fd", descriptor(t, srv.key.Secret()+"\n")); err != nil || srv.refused != 0 {
		t.Fatalf("the secret from a descriptor: %v, %d refused\n%s", err, srv.refused, out)
	}
	t.Setenv("QORY_ACCESS_KEY_SECRET", newKey(t).Secret())
	if out, err := run(t, "run", "resend", id, "--access-key-secret-fd", descriptor(t, srv.key.Secret())); err != nil || srv.refused != 0 {
		t.Errorf("resend with the secret from a descriptor: %v, %d refused\n%s", err, srv.refused, out)
	}
	clearRuns(t, root)
	for _, c := range []struct{ fd, want string }{
		{"1", "--access-key-secret-fd 1: the standard input, output and error carry no secret; name a descriptor of 3 or above"},
		{"0", "--access-key-secret-fd 0: the standard input"},
		{descriptor(t, fixtureSecret), "it holds the runner contract's published fixture key"},
		{descriptor(t, "qak_short"), "not an access key secret"},
		{descriptor(t, strings.Repeat("x", 5000)), "more than an access key secret"},
		{"1000", "--access-key-secret-fd 1000:"},
	} {
		_, err := run(t, "run", "--access-key-secret-fd", c.fd)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), fixtureSecret[4:]) {
			t.Errorf("%s: %v", c.fd, err)
		}
	}
}

// descriptor is the number of the read end of a pipe that holds value, its write end
// closed. The descriptor is raw, owned by no os.File, so the command that reads it
// closes it alone.
func descriptor(t *testing.T, value string) string {
	t.Helper()
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := syscall.Write(p[1], []byte(value)); err != nil {
		t.Fatal(err)
	}
	syscall.Close(p[1])
	return strconv.Itoa(p[0])
}
