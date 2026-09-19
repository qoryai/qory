package cmd_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// staticELF writes the smallest file the wall takes for a static Linux executable: an
// ELF header with no program headers. Nothing in these tests executes it.
func staticELF(t *testing.T) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	h[16], h[18], h[20] = 2, 0x3e, 1 // an executable, x86-64, version 1
	h[52], h[54], h[58] = 64, 56, 64 // the sizes of the header, a program header, a section header
	path := filepath.Join(t.TempDir(), "qory-linux")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeDocker writes a program that stands in for docker: it logs every command line,
// answers the two the wall reads, and for run logs the environment file, prints a line
// and exits 4, as a container's runtime would.
func fakeDocker(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	script := filepath.Join(dir, "docker")
	writeFile(t, script, `#!/bin/sh
echo "$*" >> `+log+`
case "$1 $2" in
"network inspect") echo "172.30.0.1 " ;;
"logs "*) echo "relay: listening" ;;
"run "*)
	while [ $# -gt 0 ]; do
		if [ "$1" = --env-file ]; then sed 's/^/env: /' "$2" >> `+log+`; fi
		shift
	done
	echo "inside the container"
	exit 4 ;;
esac
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// TestRunBehindAWall runs the composed runtime behind the Docker wall with a program
// standing in for docker: the wall section and the flags choose the wall and the image,
// the container is shown the checkout and nothing of this machine's environment but
// the variables named, the relay and the forwarder are qory's Linux build inside, the
// record names the wall, and the exit status is the container's. The section names the
// container's user, because a machine that runs the tests as root has none to default to.
func TestRunBehindAWall(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	runnerFile := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml")
	writeFile(t, runnerFile, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+helper+"\n  env: [MODEL_KEY, NOT_SET_HERE]\n  user: \"1000:1000\"\n  cpus: \"2\"\n  memory: 4g\n  pids_limit: 4096\n")
	t.Setenv("MODEL_KEY", "not-a-real-key")
	t.Setenv("HOST_ONLY", "stays outside")
	t.Setenv("FLAG_NAMED", "goes in")

	sibling := t.TempDir()
	out, err := run(t, "run", "claude", "--image", "example.com/agent:2", "--env", "FLAG_NAMED", "--mount", sibling+":ro", "--memory", "8g", "--shm-size", "2g", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "inside the container", "claude exited 4")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := string(data)
	wants(t, lines,
		"network create --internal",
		"--user 1000:1000 --cap-drop ALL",
		"--mount type=bind,src="+sibling+",dst="+sibling+",readonly",
		"--cpus 2 --memory 8g --shm-size 2g --pids-limit 4096",
		"--entrypoint /qory/qory example.com/agent:2 run relay 3128=",
		"src="+helper+",dst=/qory/qory,readonly",
		"--mount type=bind,src="+root+",dst="+root+" ",
		"--entrypoint claude example.com/agent:2 --settings "+root,
		" -p hi",
		"env: MODEL_KEY=not-a-real-key", "env: FLAG_NAMED=goes in", "env: HTTPS_PROXY=http://qory-proxy:3128",
		"network rm",
	)
	lacks(t, lines, "HOST_ONLY", "NOT_SET_HERE", "env: PATH=", "env: HOME=")
	if strings.Count(lines, " --mount type=bind,src="+root+",") != 1 {
		t.Error("the checkout is mounted more than once")
	}
	if strings.Contains(strings.ReplaceAll(lines, "env: MODEL_KEY=not-a-real-key", ""), "not-a-real-key") {
		t.Error("a value of the environment is on a command line")
	}
	dir, evs := events(t, root)
	if started := evs["ai.qory.run.started"]; len(started) != 1 || started[0]["wall"] != "docker" || started[0]["image"] != "example.com/agent:2" {
		t.Errorf("run.started %v", started)
	}
	settings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(settings), "/qory/qory run forward")

	// --wall none runs without the section's wall, as this machine's process.
	if err := os.RemoveAll(filepath.Join(root, ".qory", "runs")); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, fakeRuntime(t))
	if out, err := run(t, "run", "claude", "--wall", "none"); cmd.ExitCode(err) != 3 {
		t.Fatalf("--wall none: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if _, evs := events(t, root); evs["ai.qory.run.started"][0]["wall"] != nil {
		t.Errorf("--wall none still walled: %v", evs["ai.qory.run.started"])
	}
}

// TestRunRefusesAWallItCannotBuild pins the refusals before anything starts: a wall
// that is not one, no image, a flag that means nothing without a wall.
func TestRunRefusesAWallItCannotBuild(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--wall", "bubblewrap"}, "the walls are docker"},
		{[]string{"run", "--wall", "docker"}, "needs the container's image"},
		{[]string{"run", "--image", "i"}, "behind a wall"},
		{[]string{"run", "--mount", "/srv"}, "behind a wall"},
		{[]string{"run", "--shm-size", "2g"}, "behind a wall"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--mount", "srv"}, "not an absolute path"},
		{[]string{"run", "--wall", "docker", "--image", "i", "--env", "QORY_WEBHOOK_SECRET"}, "the runner's own"},
		{[]string{"run", "--label", "issue"}, "not key=value"},
		{[]string{"run", "--label", "Issue=1"}, "label key"},
		{[]string{"run", "--run-id", "../x"}, "not a UUID"},
		{[]string{"run", "--policy", filepath.Join(root, "policy.yaml")}, "inside the checkout"},
	} {
		out, err := run(t, c.args...)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (exit %d), want %q and exit %d\n%s", c.args, err, cmd.ExitCode(err), c.want, cmd.ExitInput, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".qory", "runs")); err == nil {
		t.Error("a refused run left a record")
	}
}

// TestRunBehindARealWall is the run behind Docker itself, for a machine that has an
// engine: QORY_WALL_HELPER names a static Linux build of qory, and without it the test
// is skipped. A shell stands in for the runtime: what it fetches through the proxy
// arrives and is recorded, what it dials around the proxy goes nowhere, and qory's own
// build runs inside as the forwarder.
func TestRunBehindARealWall(t *testing.T) {
	helper := os.Getenv("QORY_WALL_HELPER")
	if helper == "" {
		t.Skip("QORY_WALL_HELPER names no Linux build of qory")
	}
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		t.Skipf("docker reaches no engine: %s", out)
	}
	// The origin is a container of its own: behind a wall the proxy never dials this
	// machine, so a listener here is what the run must not reach, through the proxy too.
	here := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "this machine") }))
	defer here.Close()
	name := fmt.Sprintf("qory-test-origin-%d", os.Getpid())
	if out, err := exec.Command("docker", "run", "--detach", "--rm", "--name", name, "busybox:stable", "sh", "-c", "mkdir /w && echo from the origin > /w/index.html && httpd -f -p 8080 -h /w").CombinedOutput(); err != nil {
		t.Fatalf("the origin: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", name).Run() })
	ip, err := exec.Command("docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + strings.TrimSpace(string(ip)) + ":8080/"
	root := newCheckout(t)
	sibling := t.TempDir()
	writeFile(t, filepath.Join(sibling, "note"), "from beside the checkout\n")
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: sh
      args:
        - - -c
          - |
            echo "proxied: $(wget -q -T 5 -O - `+origin+`)"
            echo "this machine: $(NO_PROXY= no_proxy= wget -q -T 5 -O - `+here.URL+` 2>&1)"
            nc -w 3 1.1.1.1 80 </dev/null && echo "direct: connected" || echo "direct: nowhere"
            echo '{"hook_event_name":"SessionEnd"}' | /qory/qory run forward; echo "forwarder: $?"
            test -r "$1" && echo "settings: readable"
            echo "sibling: $(cat `+sibling+`/note)"
            touch `+sibling+`/mine 2>/dev/null && echo "sibling: written" || echo "sibling: read-only"
            echo "shm: $(df -k /dev/shm | tail -1 | awk '{print $2}') pids: $(cat /sys/fs/cgroup/pids.max)"
            id -u
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "wall:\n  adapter: docker\n  image: busybox:stable\n  helper: "+helper+"\n")
	out, err := run(t, "run", "claude", "--mount", sibling+":ro", "--shm-size", "256m", "--pids-limit", "512", "--memory", "512m")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "proxied: from the origin", "403", "direct: nowhere", "forwarder: 0", "settings: readable",
		"sibling: from beside the checkout", "sibling: read-only", "shm: 262144 pids: 512")
	lacks(t, out, "direct: connected", "this machine: this machine", "sibling: written")
	_, evs := events(t, root)
	if egress := evs["ai.qory.run.egress"]; len(egress) != 2 || egress[0]["decision"] != "allowed" || egress[1]["decision"] != "denied" || egress[1]["rule"] != "wall:own-address" {
		t.Errorf("run.egress %v", egress)
	}
	if runtime.GOOS == "linux" && len(evs["ai.qory.session.ended"]) != 1 {
		t.Errorf("the hook did not reach the runner: %v", evs)
	}
}

// TestRunIsNamedLimitedAndUnderItsOwnPolicy is a run started by a system of its own: the
// id and the labels are the caller's, the run's policy file narrows the machine's and
// never widens it, the runtime is stopped at the limit with timeout(1)'s status, and
// the webhook's secret in qory's environment is not in the session's.
func TestRunIsNamedLimitedAndUnderItsOwnPolicy(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "slow-runtime")
	writeFile(t, script, "#!/bin/sh\ntest -z \"$QORY_WEBHOOK_SECRET\" || exit 7\nexec sleep 30\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	t.Setenv("QORY_WEBHOOK_SECRET", "sixteen-characters-at-least")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [\"*.github.com\", api.anthropic.com]\nwebhook:\n  url: https://example.com/events\n")
	policy := filepath.Join(t.TempDir(), "run-policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: enforce\n  allow: [api.github.com, pypi.org]\n")
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	out, err := run(t, "run", "--local", "--policy", policy, "--run-id", id, "--label", "run_key=erpy/1234", "--label", "issue=77", "--timeout", "300ms", "--stop-grace", "2s")
	if cmd.ExitCode(err) != 124 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "stopped at the limit of 300ms")
	dir, evs := events(t, root)
	if filepath.Base(dir) != id {
		t.Errorf("the run is recorded in %s", dir)
	}
	if labels, _ := evs["ai.qory.run.started"][0]["labels"].(map[string]any); labels["run_key"] != "erpy/1234" || labels["issue"] != "77" {
		t.Errorf("run.started %v", evs["ai.qory.run.started"])
	}
	applied := evs["ai.qory.run.policy_applied"][0]
	if allow, _ := applied["allow"].([]any); applied["mode"] != "enforce" || len(allow) != 1 || allow[0] != "api.github.com" {
		t.Errorf("run.policy_applied %v", applied)
	}
	if exited := evs["ai.qory.run.exited"][0]; exited["reason"] != "timeout" || exited["state"] != "failed" {
		t.Errorf("run.exited %v", exited)
	}
}
