package cmd_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// exitedOf is the state and the reason of the run.exited in the session's record of the
// one run of the checkout root, and its exit code.
func exitedOf(t *testing.T, root string) (state, reason string, exitCode float64) {
	t.Helper()
	runs, err := os.ReadDir(runsDir(t, root))
	if err != nil || len(runs) != 1 {
		t.Fatalf("the runs: %v, %v", runs, err)
	}
	b, err := os.ReadFile(filepath.Join(runsDir(t, root), runs[0].Name(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				State    string  `json:"state"`
				Reason   string  `json:"reason"`
				ExitCode float64 `json:"exit_code"`
			} `json:"data"`
		}
		if json.Unmarshal(s.Bytes(), &ev) == nil && ev.Type == "dev.qory.run.exited" {
			return ev.Data.State, ev.Data.Reason, ev.Data.ExitCode
		}
	}
	t.Fatalf("no run.exited in the record:\n%s", b)
	return "", "", 0
}

// TestRunSaysTheExitOfARuntimeThatExitedBeforeASignal is a runtime that exits 0 by
// itself, with a gateway of the run's own, and SIGINT to qory run once the server has the
// run's dev.qory.run.exited: after the runtime exited, while the gateway delivers the
// run's last events. The signal did not end the run: qory says the runtime's exit, exit
// 0, and not that the run was cancelled.
func TestRunSaysTheExitOfARuntimeThatExitedBeforeASignal(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, "")
	walledServerFile(t, srv, "")
	var once sync.Once
	srv.mu.Lock()
	srv.onExited = func() {
		once.Do(func() {
			// the session has joined and closed well before this: the session's record is
			// closed once its gateway has the event, and the server has it after.
			time.Sleep(time.Second)
			syscall.Kill(os.Getpid(), syscall.SIGINT)
			time.Sleep(300 * time.Millisecond)
		})
	}
	srv.mu.Unlock()
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run")
	if err != nil || !strings.Contains(out, "✓ claude exited 0\n") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "cancelled", "qory run got")
	if state, _, exit := exitedOf(t, root); state != "succeeded" || exit != 0 {
		t.Errorf("the record's run.exited is %s, exit %v", state, exit)
	}
}

// TestRunSaysASignalThatStoppedTheRuntime is SIGINT to qory run while the runtime runs,
// with a gateway of the run's own: the signal ended the run, and qory says it was
// cancelled, names the signal, exit 1.
func TestRunSaysASignalThatStoppedTheRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "waiting-runtime")
	writeFile(t, runtime, "#!/bin/sh\ntouch '"+started+"'\nexec sleep 30\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	srv := newFakeServer(t, "")
	walledServerFile(t, srv, "")
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
		}
	}()
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n✗ the run was cancelled: qory run got SIGINT, and claude was stopped\n") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "exited 0")
	if state, reason, _ := exitedOf(t, root); state != "cancelled" || reason != "interrupted" {
		t.Errorf("the record's run.exited is %s, %q", state, reason)
	}
}

// TestRunSaysASignalThatStoppedARuntimeThatExited0 is SIGINT to qory run while the
// runtime runs, and a runtime that exits 0 at the stop: the signal ended the run, so qory
// says it was cancelled, exit 1, and the record says cancelled, interrupted, exit 0.
func TestRunSaysASignalThatStoppedARuntimeThatExited0(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "stopping-runtime")
	writeFile(t, runtime, "#!/bin/sh\ntrap 'exit 0' INT TERM\ntouch '"+started+"'\nwhile :; do sleep 0.1; done\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	srv := newFakeServer(t, "")
	walledServerFile(t, srv, "")
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
		}
	}()
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n✗ the run was cancelled: qory run got SIGINT, and claude was stopped\nqory run: the record is in ") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "exited 0", "interrupted", "context canceled")
	if state, reason, exit := exitedOf(t, root); state != "cancelled" || reason != "interrupted" || exit != 0 {
		t.Errorf("the record's run.exited is %s, %q, exit %v", state, reason, exit)
	}
}
