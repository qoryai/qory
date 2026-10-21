//go:build unix

package devcontainer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/devcontainer"
)

// pathEnv is the PATH of each test's commands.
const pathEnv = "PATH=/usr/bin:/bin"

// command parses member, a lifecycle command written as JSON, and expands it with vars.
func command(t *testing.T, member, value string, vars devcontainer.Vars) devcontainer.Command {
	t.Helper()
	c, err := devcontainer.Parse(".devcontainer.json", []byte(`{"`+member+`": `+value+`}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range c.Commands() {
		if cmd.Member == member {
			out, err := cmd.Expand(vars)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatalf("no lifecycle command %s", member)
	return devcontainer.Command{}
}

// TestRunRunsEachForm is the string form through a shell, the array form without one,
// and the object form, each in dir, with the environment given alone and no standard
// input.
func TestRunRunsEachForm(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vars := devcontainer.Vars{WorkspaceFolder: dir, WorkspaceFolderBasename: filepath.Base(dir), Env: []string{"MODE=test"}}
	env := []string{pathEnv, "SHOP=acme"}
	for _, c := range []struct{ member, value, want string }{
		{"onCreateCommand", `"cd ${containerWorkspaceFolder} && echo \"$SHOP ${containerEnv:MODE}\" | cat; cat; env | sort"`, ""},
		{"updateContentCommand", `["printf", "%s|%s\n", "$SHOP", "${containerWorkspaceFolderBasename}"]`, "$SHOP|" + filepath.Base(dir) + "\n"},
		{"postCreateCommand", `["/bin/sh", "-c", "pwd; cat"]`, dir + "\n"},
		{"postStartCommand", `{"one": "echo one"}`, "one\n"},
	} {
		var stdout, stderr bytes.Buffer
		took, err := devcontainer.Run(context.Background(), command(t, c.member, c.value, vars), dir, env, &stdout, &stderr)
		if err != nil {
			t.Errorf("%s: %v, stderr %q", c.member, err, stderr.String())
			continue
		}
		got := stdout.String()
		if c.member == "onCreateCommand" {
			// The shell may set variables such as PWD; the given ones are there, and
			// nothing else of this process's environment.
			if !strings.HasPrefix(got, "acme test\n") || !strings.Contains(got, "\nSHOP=acme\n") || strings.Contains(got, "HOME=") {
				t.Errorf("%s: %q", c.member, got)
			}
		} else if got != c.want {
			t.Errorf("%s: %q, want %q", c.member, got, c.want)
		}
		if took <= 0 {
			t.Errorf("%s: took %s", c.member, took)
		}
	}
	empty, err := devcontainer.Parse(".devcontainer.json", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if took, err := devcontainer.Run(context.Background(), empty.PostStart, dir, env, nil, nil); took != 0 || err != nil {
		t.Errorf("empty: %s, %v", took, err)
	}
}

// TestRunRunsNamedCommandsInParallel is two named commands that each wait for the
// other's file: they finish only when they run at once.
func TestRunRunsNamedCommandsInParallel(t *testing.T) {
	dir := t.TempDir()
	cmd := command(t, "postCreateCommand", `{
		"a": "touch a; while [ ! -e b ]; do sleep 0.05; done; echo a done",
		"b": "touch b; while [ ! -e a ]; do sleep 0.05; done; echo b done"}`, devcontainer.Vars{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	if _, err := devcontainer.Run(ctx, cmd, dir, []string{pathEnv}, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "a done\nb done\n" && got != "b done\na done\n" {
		t.Errorf("stdout %q", got)
	}
}

// TestRunNamesEachFailure is named commands of which three fail, each its way, and one
// that succeeds: the error names each failed one by its name, in the order written,
// and quotes no command's text and no value of the environment.
func TestRunNamesEachFailure(t *testing.T) {
	dir := t.TempDir()
	cmd := command(t, "postCreateCommand", `{
		"build": "echo secret-text; exit 3",
		"lint": ["secret-program", "--secret-flag"],
		"ok": "true",
		"crash": "kill -9 $$",
		"missing": ["/nonexistent/secret-program"]}`, devcontainer.Vars{})
	env := []string{pathEnv, "TOKEN=secret-value"}
	_, err := devcontainer.Run(context.Background(), cmd, dir, env, nil, nil)
	want := "postCreateCommand's command build exited with status 3; " +
		"postCreateCommand's command lint could not start: its program is not on the PATH of its environment; " +
		"postCreateCommand's command crash was ended by a signal; " +
		"postCreateCommand's command missing could not start: no such file or directory"
	if err == nil || err.Error() != want {
		t.Fatalf("%v\nwant %s", err, want)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the error quotes a command or a value: %v", err)
	}
	one := command(t, "postStartCommand", `"echo secret-text; exit 2"`, devcontainer.Vars{})
	if _, err := devcontainer.Run(context.Background(), one, dir, env, nil, nil); err == nil || err.Error() != "postStartCommand exited with status 2" {
		t.Errorf("one: %v", err)
	}
	notExpanded, err := devcontainer.Parse(".devcontainer.json", []byte(`{"postStartCommand": "true"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devcontainer.Run(context.Background(), notExpanded.PostStart, dir, env, nil, nil); err == nil || err.Error() != "postStartCommand is run before its variables are replaced" {
		t.Errorf("not expanded: %v", err)
	}
}

// TestRunStopsTheWholeGroup is a command whose shell starts two processes and waits:
// one that SIGTERM stops, and one that ignores SIGTERM and holds the output open. When
// the context ends, the first stops at once and the second is killed after the grace,
// and Run returns soon after.
func TestRunStopsTheWholeGroup(t *testing.T) {
	devcontainer.ShortenGrace(t, 500*time.Millisecond)
	dir := t.TempDir()
	cmd := command(t, "postStartCommand", `{
		"serve": "sleep 60 & echo $! > obeys; (trap '' TERM; exec sleep 60) & echo $! > ignores; wait"}`, devcontainer.Vars{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var stdout bytes.Buffer
	go func() {
		_, err := devcontainer.Run(ctx, cmd, dir, []string{pathEnv}, &stdout, nil)
		done <- err
	}()
	obeys, ignores := pidIn(t, filepath.Join(dir, "obeys")), pidIn(t, filepath.Join(dir, "ignores"))
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "postStartCommand's command serve was stopped before it finished" {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Run returned %s after the cancel", took)
	}
	gone(t, obeys)
	gone(t, ignores)
}

// TestRunLeavesWhatASucceededCommandStarted is a command that starts a service and
// exits 0, once with the service's output elsewhere and once with the service holding
// the command's output: Run succeeds, after the grace in the second case, and the
// service keeps running.
func TestRunLeavesWhatASucceededCommandStarted(t *testing.T) {
	devcontainer.ShortenGrace(t, 300*time.Millisecond)
	for _, value := range []string{
		`"sleep 60 > /dev/null 2>&1 & echo $! > pid"`,
		`"sleep 60 & echo $! > pid"`,
	} {
		dir := t.TempDir()
		var stdout bytes.Buffer
		if _, err := devcontainer.Run(context.Background(), command(t, "postStartCommand", value, devcontainer.Vars{}), dir, []string{pathEnv}, &stdout, nil); err != nil {
			t.Errorf("%s: %v", value, err)
		}
		pid := pidIn(t, filepath.Join(dir, "pid"))
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("%s: the service stopped: %v", value, err)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// TestRunHandsAFileToEachStep is the object form with stdout and stderr files, and a
// step that leaves a service that keeps writing to stdout: each step writes to the
// file itself, so Run returns once the steps exit, well before the grace, and
// succeeds, and the service stays alive and keeps writing after Run returned.
func TestRunHandsAFileToEachStep(t *testing.T) {
	devcontainer.ShortenGrace(t, time.Second)
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	cmd := command(t, "postStartCommand", `{
		"serve": "(while :; do echo serving; sleep 0.1; done) & echo $! > pid",
		"note": "echo noted >&2"}`, devcontainer.Vars{})
	start := time.Now()
	if _, err := devcontainer.Run(context.Background(), cmd, dir, []string{pathEnv}, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Errorf("Run returned after %s, held by the service", took)
	}
	pid := pidIn(t, filepath.Join(dir, "pid"))
	defer syscall.Kill(pid, syscall.SIGKILL)
	// Past the grace, after which a pipe would be closed.
	time.Sleep(1500 * time.Millisecond)
	before, _ := os.ReadFile(stdout.Name())
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadFile(stdout.Name())
	if len(after) <= len(before) || strings.ReplaceAll(string(after), "serving\n", "") != "" {
		t.Errorf("the service stopped writing: %q, then %q", before, after)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the service stopped: %v", err)
	}
	if got, _ := os.ReadFile(stderr.Name()); string(got) != "noted\n" {
		t.Errorf("stderr %q", got)
	}
}

// pidIn waits for file to hold a process ID and returns it.
func pidIn(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(file)
		if err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s holds no process ID", file)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gone waits for the process pid to run no more. A process whose parent died is
// reparented, and a container's first process may never reap it, so on Linux a zombie
// counts as gone.
func gone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) || zombie(pid) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d still runs", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// zombie reports whether /proc lists pid as exited and waiting to be reaped; false
// where there is no /proc.
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// pid (comm) state ...: comm may hold spaces, so the state is read after its
	// closing parenthesis.
	fields := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}
