package cmd

import (
	"errors"
	"os"
	"strconv"
	"testing"
)

// TestHiddenRunSecretsHidesAnEnrolmentCode is an error whose text contains an enrolment
// code, qecn_, qecp_, qecg_ or qeci_ in mixed case: its text is the enrolment code's
// phrase, and it unwraps to the error.
func TestHiddenRunSecretsHidesAnEnrolmentCode(t *testing.T) {
	for _, prefix := range []string{"QeCn_", "qECP_", "QecG_", "qEcI_"} {
		inner := errors.New("dial tcp: lookup " + prefix + "x.example: no such host")
		err := hiddenRunSecrets(inner)
		if err == nil || err.Error() != "(a value that contains an enrolment code)" || !errors.Is(err, inner) {
			t.Errorf("%s: %v", prefix, err)
		}
	}
}

// TestSameGateway is the comparison of a run key's gateway URL with --gateway: the host
// in any letter case, the port 443 when the URL has none, and a slash after the host,
// are one gateway; another port, another host and http are not.
func TestSameGateway(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"https://gateway.example:8443", "https://gateway.example:8443", true},
		{"https://Gateway.EXAMPLE:8443", "https://gateway.example:8443/", true},
		{"https://gateway.example", "https://gateway.example:443", true},
		{"https://gateway.example:8443", "https://gateway.example", false},
		{"https://gateway.example:8443", "https://gateway.example:9443", false},
		{"https://other.example:8443", "https://gateway.example:8443", false},
		{"http://gateway.example:8443", "https://gateway.example:8443", false},
		{"https://[::1]:8443", "https://[::1]:8443", true},
	} {
		if got := sameGateway(c.a, c.b); got != c.same {
			t.Errorf("sameGateway(%q, %q) = %t, want %t", c.a, c.b, got, c.same)
		}
	}
}

// TestNamesStdin is the paths of --run-code-file that qory reads as the standard input
// without opening them: /dev/stdin, and descriptor 0 under /dev/fd, /proc/self/fd,
// /proc/thread-self/fd and qory's process under /proc, with one or more zeros, once
// cleaned. Linux has no path with more than one zero, and macOS opens /dev/fd/00 as
// descriptor 0; qory reads each as stdin on both. Another descriptor, another process
// and what is not a number are not matched.
func TestNamesStdin(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	for _, c := range []struct {
		file  string
		stdin bool
	}{
		{"/dev/stdin", true},
		{"/dev/fd/0", true},
		{"/dev/fd/00", true},
		{"/dev//fd/./0", true},
		{"/proc/self/fd/0", true},
		{"/proc/thread-self/fd/0", true},
		{"/proc/" + pid + "/fd/0", true},
		{"/dev/fd/3", false},
		{"/dev/fd/03", false},
		{"/dev/fd/1", false},
		{"/dev/fd/", false},
		{"/dev/fd/+0", false},
		{"/dev/fd/0x0", false},
		{"/dev/stdout", false},
		{"/proc/self/fd/3", false},
		{"/proc/0" + pid + "/fd/0", false},
		{"/proc/" + strconv.Itoa(os.Getpid()+1) + "/fd/0", false},
		{"/proc/self/task/1/fd/0", false},
		{"/run/qory/run-code", false},
	} {
		if got := namesStdin(c.file); got != c.stdin {
			t.Errorf("namesStdin(%q) = %t, want %t", c.file, got, c.stdin)
		}
	}
}
