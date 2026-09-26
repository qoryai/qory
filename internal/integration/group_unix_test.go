//go:build unix

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/integration"
)

// TestDescribeLeavesNoProcessBehind runs a describe that starts a process in the
// background and hangs, and one that exits and leaves that process holding its output:
// each is refused, the first once the wait is over and the second a second after it
// exits, before the wait is over, and neither leaves a process of its group.
func TestDescribeLeavesNoProcessBehind(t *testing.T) {
	for _, c := range []struct {
		wait       time.Duration
		last, want string
	}{
		{2 * time.Second, "sleep 30\n", "describe did not answer within 2s"},
		{5 * time.Second, "exit 0\n", "describe exited and left a process that keeps its output open"},
	} {
		integration.ShortenDescribeWait(t, c.wait)
		pidFile := filepath.Join(t.TempDir(), "pid")
		program := script(t, "test \"$1\" = describe || exit 0\necho $$ > "+pidFile+"\nsleep 30 &\n"+c.last)
		// The first start of a new program may be slow while the system checks it; this
		// one comes before the wait starts.
		if err := exec.Command(program).Run(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := integration.Describe(context.Background(), program)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.last, err, c.want)
		}
		if took := time.Since(start); took > 8*time.Second {
			t.Errorf("%q: describe returned after %s", c.last, took)
		}
		b, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		gone(t, pgid)
	}
}

// gone waits for the process group pgid to hold no process that runs. A process the
// group's leader started is reparented when the leader dies, and a container's first
// process may never reap it, so on Linux a group of zombies alone counts as gone.
func gone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) || zombiesOnly(pgid) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still runs: %v", pgid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// zombiesOnly reports whether every process of the group pgid that /proc lists has
// exited and waits to be reaped; false where there is no /proc.
func zombiesOnly(pgid int) bool {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	seen := false
	for _, f := range stats {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp ...: comm may hold spaces, so the fields are read
		// after its closing parenthesis.
		fields := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
		if len(fields) < 3 || fields[2] != strconv.Itoa(pgid) {
			continue
		}
		if fields[0] != "Z" {
			return false
		}
		seen = true
	}
	return seen
}
