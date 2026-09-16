package cmd_test

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// fakeRuntime writes a program that stands in for a runtime: it prints its run id and
// its arguments, checks that the runner's proxy and socket are in its environment, and
// exits with the status QORY_TEST_EXIT names, 3 when unset.
func fakeRuntime(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, `#!/bin/sh
echo "hello from $QORY_RUN_ID with $*"
test -n "$HTTP_PROXY" || exit 9
test -n "$QORY_RUN_SOCKET" || exit 8
exit ${QORY_TEST_EXIT:-3}
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// composedForFake composes the two-modules fixture for claude with the fake runtime as
// claude's program, through harness.launch in the user's file, keeping --settings so
// the runner installs its hooks into a copy of the composed settings.
func composedForFake(t *testing.T, root, script string) {
	t.Helper()
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: `+script+`
      args:
        - [--settings, "${dir}/settings.json"]
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// events reads the events of the one run recorded under the checkout and returns them
// by type, with the run directory.
func events(t *testing.T, root string) (string, map[string][]map[string]any) {
	t.Helper()
	runs, err := os.ReadDir(filepath.Join(root, ".qory", "runs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs recorded, want 1", len(runs))
	}
	dir := filepath.Join(root, ".qory", "runs", runs[0].Name())
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		byType[ev.Type] = append(byType[ev.Type], ev.Data)
	}
	return dir, byType
}

// TestRunRecordsTheSession runs the composed runtime through the runner: the launch
// spec is the template with the configuration over it plus the arguments after --, the
// runtime sees the proxy and the socket, the policy in the configuration directory is
// applied, the hooks are installed into a copy of the composed settings, the record is
// written under .qory/runs, and the exit status is the runtime's, reported once.
func TestRunRecordsTheSession(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := fakeRuntime(t)
	composedForFake(t, root, script)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [example.com]\n")
	out, err := run(t, "run", "claude", "--", "--extra", "one")
	if err == nil || cmd.ExitCode(err) != 3 || !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello from ", " with --settings ", " --extra one", "claude exited 3; recorded in .qory/runs/")
	dir, evs := events(t, root)
	started := evs["ai.qory.run.started"]
	if len(started) != 1 || started[0]["command"] != script || started[0]["interactive"] != false {
		t.Errorf("run.started %v", started)
	}
	applied := evs["ai.qory.run.policy_applied"]
	if len(applied) != 1 || applied[0]["mode"] != "enforce" || applied[0]["source"] != "config" {
		t.Errorf("run.policy_applied %v", applied)
	}
	exited := evs["ai.qory.run.exited"]
	if len(exited) != 1 || exited[0]["exit_code"] != float64(3) || exited[0]["state"] != "failed" {
		t.Errorf("run.exited %v", exited)
	}
	if len(evs["ai.qory.ping"]) != 0 {
		t.Error("a ping was sent with no webhook configured")
	}
	log, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil || !strings.Contains(string(log), "hello from ") {
		t.Errorf("output.log: %v\n%s", err, log)
	}
	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	wants(t, string(settings), "QORY_HARNESS_HOME", exe+" run forward", "SessionEnd")
	if !strings.Contains(string(settings), "\"timeout\"") {
		t.Error("the hook has no timeout")
	}
}

// TestRunExitsZeroQuietly is a runtime that exited 0: no error, a success line naming
// the record, and no policy means observe.
func TestRunExitsZeroQuietly(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "claude exited 0; recorded in .qory/runs/")
	_, evs := events(t, root)
	if applied := evs["ai.qory.run.policy_applied"]; len(applied) != 1 || applied[0]["mode"] != "observe" || applied[0]["source"] != "none" {
		t.Errorf("run.policy_applied %v", applied)
	}
}

// TestRunRefusesWhatItCannotStart is the runs that never start: a runtime the harness
// is not composed for, two runtimes named, a runner file that does not read, and no
// composed harness at all; none of them leaves a record.
func TestRunRefusesWhatItCannotStart(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), "run qory harness compose") {
		t.Errorf("nothing composed: %v", err)
	}
	composedForFake(t, root, fakeRuntime(t))
	if _, err := run(t, "run", "codex"); cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("not composed for codex: %v", err)
	}
	if _, err := run(t, "run", "claude", "codex"); cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("two runtimes: %v", err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "apiVersion: qory.dev/v1alpha1\negress:\n  mode: log\n")
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), `runner.yaml: egress.mode "log" is not observe or enforce`) {
		t.Errorf("unreadable runner file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".qory", "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a run that did not start left a record")
	}
}

// TestForwardHandsAHookToTheRun is the hidden forward verb: it sends its stdin to the
// socket the environment names as one hooks record, prints nothing and exits 0; with
// no socket in the environment it still exits 0 and says why on stderr.
func TestForwardHandsAHookToTheRun(t *testing.T) {
	emptyDir(t)
	dir, err := os.MkdirTemp("", "qory-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got <- b.String()
	}()
	t.Setenv("QORY_RUN_SOCKET", path)
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs([]string{"run", "forward"})
	root.SetIn(strings.NewReader(`{"hook_event_name":"Stop","session_id":"s1"}`))
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("forward printed %q", out.String())
	}
	line := <-got
	wants(t, line, `"source":"hooks"`, `"hook_event_name":"Stop"`)
	if c, _, _ := root.Find([]string{"run", "forward"}); !c.Hidden {
		t.Error("forward is in the help")
	}
	t.Setenv("QORY_RUN_SOCKET", "")
	root = cmd.Root()
	root.SetArgs([]string{"run", "forward"})
	root.SetIn(strings.NewReader(`{}`))
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	wants(t, out.String(), "QORY_RUN_SOCKET")
}
