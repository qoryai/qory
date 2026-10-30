//go:build unix

package cmd_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/statusfd"
)

// TestARunWritesItsLinesToTheStatusDescriptor is qory run with QORY_STATUS_FD set to an
// open descriptor: qory's "qory run:" line of the compose, its lines of the runtime's
// exit and of the run's record reach the descriptor and the output. The compose's ✓
// line, which the output does not show, does not reach the descriptor. The runtime's
// output reaches only the output, a value of --env in it as ***. The runtime does not see the variable, and its
// write to the descriptor fails, since the descriptor is close-on-exec.
func TestARunWritesItsLinesToTheStatusDescriptor(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	const fd = 237
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, `#!/bin/sh
echo "hello from the runtime"
echo "token $STATUS_TOKEN"
if (echo leak >&`+strconv.Itoa(fd)+`) 2>/dev/null; then echo "descriptor open"; else echo "descriptor closed"; fi
echo "variable ${QORY_STATUS_FD:-unset}"
exit 0
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := unix.Dup2(int(w.Fd()), fd); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer statusfd.Close()
	t.Setenv(statusfd.Env, strconv.Itoa(fd))

	t.Setenv("STATUS_TOKEN", "status-token-value")
	out, err := run(t, "run", "claude", "--env", "STATUS_TOKEN")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	statusfd.Close()
	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	status, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "hello from the runtime\n", "token ***\n", "descriptor closed\n", "variable unset\n", "✓ claude exited 0\n")
	lacks(t, out, "status-token-value")
	lacks(t, out, "✓ composed")
	want := regexp.MustCompile("^qory run: composed 8 entries from 2 modules for claude\n✓ claude exited 0\nqory run: the record is in /[^\n]+\n$")
	if !want.Match(status) {
		t.Errorf("the descriptor got %q, want only qory's lines, matching %s", status, want)
	}
	if strings.Contains(string(status), "runtime") || strings.Contains(string(status), "leak") || strings.Contains(string(status), "token") {
		t.Errorf("the runtime's output reached the descriptor: %q", status)
	}
}

// TestARunMasksItsLinesOnTheStatusDescriptor is a run whose record is under a folder
// named after a value of --env: the "qory run:" line of the record reaches the
// descriptor with the value as ***.
func TestARunMasksItsLinesOnTheStatusDescriptor(t *testing.T) {
	const value = "descriptor-env-value"
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), value))
	const fd = 238
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := unix.Dup2(int(w.Fd()), fd); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer statusfd.Close()
	t.Setenv(statusfd.Env, strconv.Itoa(fd))
	t.Setenv("SHOP_TOKEN", value)
	out, err := run(t, "run", "claude", "--env", "SHOP_TOKEN")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	statusfd.Close()
	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	status, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(status), "qory run: the record is in ") || !strings.Contains(string(status), "/***/") || strings.Contains(string(status), value) {
		t.Errorf("the descriptor got %q, want the record's line with the value as ***", status)
	}
	lacks(t, out, value)
}
