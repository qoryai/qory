//go:build unix

package cmd_test

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// denyHook is a PreToolUse hook that denies a tool call whose input names locked/, and
// spawn_agent, with exit status 2 and the reason on stderr, which Codex returns to the
// model as the tool's output.
const denyHook = `#!/bin/sh
input=$(cat)
case "$input" in
  *'"tool_name":"spawn_agent"'*) echo "denied by the test hook: no background agents" >&2; exit 2 ;;
  *locked/*) echo "denied by the test hook: locked/ is read-only" >&2; exit 2 ;;
esac
exit 0
`

// denyCommand starts [denyHook] from the harness's hooks folder.
const denyCommand = `sh "$QORY_HARNESS_HOME/hooks/deny.sh"`

// denyConfig is a Codex config.toml fragment with a PreToolUse hook of every tool that
// runs [denyCommand], and the lines extra after it.
func denyConfig(extra string) string {
	return "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = '" + denyCommand + "'\ntimeout = 30\n" + extra
}

// guardSteps is the script of the stub upstream for a guard test in the folder dir: it
// makes dir/free and dir/locked, writes a file into each with apply_patch and with a
// shell command, starts a shell with a terminal, starts a shell without one and sends it
// a write with write_stdin, starts an agent with spawn_agent, lists the files under dir,
// and ends with a message.
func guardSteps(t *testing.T, dir string) string {
	t.Helper()
	patch := func(name string) string {
		return "*** Begin Patch\n*** Add File: " + dir + "/" + name + "\n+from apply_patch\n*** End Patch\n"
	}
	steps := []map[string]any{
		{"call": "exec_command", "args": map[string]any{"cmd": "mkdir -p " + dir + "/free " + dir + "/locked"}},
		{"custom": "apply_patch", "input": patch("locked/patch.txt")},
		{"custom": "apply_patch", "input": patch("free/patch.txt")},
		{"call": "exec_command", "args": map[string]any{"cmd": "echo from-shell > " + dir + "/locked/echo.txt"}},
		{"call": "exec_command", "args": map[string]any{"cmd": "echo from-shell > " + dir + "/free/echo.txt"}},
		{"call": "exec_command", "args": map[string]any{"cmd": "sh", "tty": true, "yield_time_ms": 1000}},
		{"call": "exec_command", "args": map[string]any{"cmd": "sh", "yield_time_ms": 1000}},
		{"call": "write_stdin", "args": map[string]any{"session_id": "__SESSION__", "chars": "echo from-stdin > " + dir + "/locked/stdin.txt; exit\n", "yield_time_ms": 1500}},
		{"call": "spawn_agent", "ns": "multi_agent_v1", "args": map[string]any{"message": "say done"}},
		{"call": "exec_command", "args": map[string]any{"cmd": "find " + dir + " -type f"}},
		{"text": "done"},
	}
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkGuard checks the tool outputs that the model got in a run of [guardSteps] in dir,
// those of the last request: the apply_patch and the shell write to locked/ and
// spawn_agent are each blocked by the hook, the shell with a terminal is refused, and the
// files under dir after the run are the two in free/ alone.
func checkGuard(t *testing.T, outputs []string, dir string) {
	t.Helper()
	all := strings.Join(outputs, "\n---\n")
	for _, want := range []string{
		"blocked by PreToolUse hook: denied by the test hook: locked/ is read-only. Command: *** Begin Patch\n*** Add File: " + dir + "/locked/patch.txt",
		"Command blocked by PreToolUse hook: denied by the test hook: locked/ is read-only. Command: echo from-shell > " + dir + "/locked/echo.txt",
		"TTY execution is disabled by config",
		"Tool call blocked by PreToolUse hook: denied by the test hook: no background agents. Tool: spawn_agent",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the tool outputs lack %q:\n%s", want, all)
		}
	}
	if len(outputs) == 0 {
		t.Fatal("the model got no tool output")
	}
	found := outputs[len(outputs)-1]
	for _, want := range []string{dir + "/free/patch.txt", dir + "/free/echo.txt"} {
		if !strings.Contains(found, want) {
			t.Errorf("the files under %s lack %s:\n%s", dir, want, found)
		}
	}
	if strings.Contains(found, "/locked/") {
		t.Errorf("a file under %s/locked was written:\n%s", dir, found)
	}
}

// TestACodexWithoutAWallRunsTheTrustedHooks composes a harness for codex alone whose
// module has a PreToolUse hook that runs [denyHook] and a PreModelRequest hook in
// config.toml, and a Stop hook in hooks.json, the last two each touching a file. It runs
// Codex at [realCodexVersion] in a container as a run without a wall starts it: with the
// variables of qory harness launch --json, so CODEX_HOME is the home's codex folder, the
// checkout bound at its path on this machine, and the stub upstream in the container,
// on plain HTTP, scripted with [guardSteps]. Codex runs each hook by its trusted hash:
// the run of [guardSteps] passes [checkGuard], no file is under locked/, and both
// files exist. It passes with CODEX_HOME at its path and through a link to the
// checkout, which Codex resolves as qory does. It runs only with QORY_TEST_DOCKER=1 and
// docker on the PATH.
func TestACodexWithoutAWallRunsTheTrustedHooks(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	// The stub builds from qory's folder, before the test moves to the checkout.
	image := realCodexImage(t, realCodexVersion, realCodexSums, "unused")
	bin := upstreamBinary(t)
	root := newCheckout(t)
	guard := filepath.Join(root, "harness", "guard")
	writeFile(t, filepath.Join(guard, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: guard\n")
	writeFile(t, filepath.Join(guard, "hooks", "deny.sh"), denyHook)
	writeFile(t, filepath.Join(guard, "settings", "codex", "config.toml"), denyConfig(
		"\n[[hooks.PreModelRequest]]\n[[hooks.PreModelRequest.hooks]]\ntype = \"command\"\ncommand = 'touch \"$QORY_GUARD_DIR/ran-pre-model-request\"'\n"))
	writeFile(t, filepath.Join(guard, "settings", "codex", "hooks.json"),
		`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "touch \"$QORY_GUARD_DIR/ran-stop\"", "timeout": 30}]}]}}`)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: codex\nmodules:\n  - name: guard\n    source: {path: ./harness/guard}\n")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "launch", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var launch struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(out), &launch); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	if launch.Env["CODEX_HOME"] != home+"/codex" {
		t.Fatalf("the launch sets CODEX_HOME=%q, want %s/codex", launch.Env["CODEX_HOME"], home)
	}
	parent := filepath.Dir(root)
	link := filepath.Join(parent, "link-to-checkout")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, codexHome string }{
		{"at its path", home + "/codex"},
		{"through a link", link + "/.qory/harness/codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(root, "guard-"+strings.ReplaceAll(c.name, " ", "-"))
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			env := maps.Clone(launch.Env)
			env["CODEX_HOME"] = c.codexHome
			env["QORY_GUARD_DIR"] = dir
			env["OPENAI_API_KEY"] = "sk-test-fake"
			env["QORY_TEST_UPSTREAM_PLAIN"] = "1"
			env["QORY_TEST_UPSTREAM_SCRIPT"] = guardSteps(t, dir)
			args := []string{"run", "--rm", "--network", "none", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
				"--mount", "type=bind,src=" + parent + ",dst=" + parent, "--mount", "type=bind,src=" + bin + ",dst=/upstream,readonly", "--workdir", root}
			for _, name := range slices.Sorted(maps.Keys(env)) {
				args = append(args, "--env", name+"="+env[name])
			}
			// The stub writes its empty log before it listens, then prints its ready line.
			script := `/upstream > /tmp/upstream.out 2>&1 &
until grep -q listening /tmp/upstream.out 2>/dev/null; do sleep 0.1; done
codex exec --json --skip-git-repo-check --sandbox danger-full-access --model gpt-5.5 \
  -c model_provider=qory -c model_providers.qory.name=qory -c model_providers.qory.wire_api=responses \
  -c model_providers.qory.env_key=OPENAI_API_KEY -c 'model_providers.qory.base_url="http://127.0.0.1:8080/v1"' \
  -c features.plugins=false -c features.apps=false -c analytics.enabled=false -c feedback.enabled=false \
  -c check_for_update_on_startup=false go > /tmp/codex.out 2>&1
echo "codex exit=$?"
cat /tmp/codex.out
echo "== upstream log"
cat /tmp/upstream.jsonl
`
			args = append(args, "--entrypoint", "sh", image, "-c", script)
			b, err := exec.Command("docker", args...).CombinedOutput()
			logs := string(b)
			t.Logf("the container:\n%s", logs)
			if err != nil {
				t.Fatalf("docker run: %v", err)
			}
			wants(t, logs, "codex exit=0\n")
			_, log, _ := strings.Cut(logs, "== upstream log\n")
			var last upstreamEntry
			sc := bufio.NewScanner(strings.NewReader(log))
			sc.Buffer(nil, 1<<20)
			for sc.Scan() {
				if err := json.Unmarshal(sc.Bytes(), &last); err != nil {
					t.Fatalf("the upstream's log: %v: %s", err, sc.Bytes())
				}
			}
			checkGuard(t, last.Outputs, dir)
			var locked []string
			_ = filepath.WalkDir(filepath.Join(dir, "locked"), func(p string, e fs.DirEntry, err error) error {
				if err == nil && !e.IsDir() {
					locked = append(locked, p)
				}
				return nil
			})
			if len(locked) > 0 {
				t.Errorf("files under locked/: %q", locked)
			}
			for _, marker := range []string{"ran-pre-model-request", "ran-stop"} {
				if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
					t.Errorf("the hook that touches %s did not run: %v", marker, err)
				}
			}
		})
	}
}

// walledGuard runs a walled Codex run from a run code that only reads, behind a real wall
// on this machine's engine, with Codex at [realCodexVersion] and the packages given in
// the image, at a gateway of Forager's in this process whose control plane has a fake API
// key for the stub upstream, scripted with script. The hello stack has the files extra,
// and stackFile, when not nil, changes the stack's file. It runs t in a separate test
// binary, as [TestARealWalledCodexRunReachesTheModelThroughTheGateway] does, and needs
// QORY_TEST_DOCKER=1, docker on the PATH, and an engine whose containers this machine
// reaches at the containers' addresses. It returns the tool outputs of the last request
// to the upstream, and false in the process that starts the test binary, which checks
// nothing more.
func walledGuard(t *testing.T, packages []string, script string, extra map[string]string, stackFile func(string) string) ([]string, bool) {
	t.Helper()
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if os.Getenv(envRealCodexChild) == "" {
		runRealCodexChild(t, realCodexVersion, realCodexSums, codexChild{packages: packages, upstreamEnv: []string{"QORY_TEST_UPSTREAM_SCRIPT=" + script}})
		return nil, false
	}
	var setup realCodexSetup
	if err := json.Unmarshal([]byte(os.Getenv(envRealCodexChild)), &setup); err != nil {
		t.Fatal(err)
	}
	ca, caKey := setup.authority(t)
	trustOnly(t, ca, caKey)
	u := upstream{name: setup.Upstream, host: setup.Host}
	startModelControl(t, u.host, false, "sk-"+setup.Mark+"-api")
	atGateway(t)
	codeGatewayAllow = []string{u.host}
	t.Cleanup(func() { codeGatewayAllow = nil })
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	r := runFromCodeLinks(t, func(string) []string {
		machineConfig(t, "harness:\n  launch:\n    codex:\n      command: /usr/local/bin/codex-scan\n")
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  helper: "+setup.Helper+"\n  user: \"1000:1000\"\n  image: "+setup.Image+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, func(s string) string {
		if stackFile != nil {
			s = stackFile(s)
		}
		return s + "workflows:\n  hello:\n    time_limit: 5m\n    access: read\n"
	}, func(d map[string]any) {
		// Codex 0.170.0 offers spawn_agent to a model such as gpt-5.5, as a run without a
		// wall in TestACodexWithoutAWallRunsTheTrustedHooks starts it.
		d["run_settings"] = map[string]any{"runtime": "codex", "model": "gpt-5.5", "provider_models": []any{map[string]any{"id": "gpt-5.5"}}}
	}, extra, nil)
	records := filepath.Join(r.open.RunsDir, r.open.RunID)
	if !r.joined || r.err != nil {
		record, _ := os.ReadFile(filepath.Join(records, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	t.Logf("qory run:\n%s", r.out)
	log := u.log(t)
	if len(log) == 0 {
		t.Fatal("the upstream got no request")
	}
	return log[len(log)-1].Outputs, true
}

// TestARealWalledCodexRunsTheGuardHook is [walledGuard] with a module whose config.toml
// has a PreToolUse hook that runs [denyHook], which a walled run binds at /etc/codex, and
// the script [guardSteps] in /tmp/qory-guard, a folder that the wall lets the agent
// write: the run passes [checkGuard].
func TestARealWalledCodexRunsTheGuardHook(t *testing.T) {
	const dir = "/tmp/qory-guard"
	extra := maps.Clone(helloSkill)
	extra["examples/hello/modules/hello/hooks/deny.sh"] = denyHook
	extra["examples/hello/modules/hello/settings/codex/config.toml"] = denyConfig("")
	outputs, ok := walledGuard(t, nil, guardSteps(t, dir), extra, nil)
	if ok {
		checkGuard(t, outputs, dir)
	}
}

// TestARealWalledCodexRunsTheReportsGuardOfTheHarness is [walledGuard] with the module
// core of the harness checkout at QORY_TEST_HARNESS, whose
// settings/codex/config.toml has the tool guard as a PreToolUse hook, and node in the
// image, which the guard runs on. The model writes .reports/x.json in the checkout with
// apply_patch, which the guard denies with the reason of reports-guard.cjs, and then runs
// a shell command that writes nothing, which the guard allows. It is skipped without
// QORY_TEST_HARNESS.
func TestARealWalledCodexRunsTheReportsGuardOfTheHarness(t *testing.T) {
	harness := os.Getenv("QORY_TEST_HARNESS")
	if harness == "" {
		t.Skip("QORY_TEST_HARNESS=<a harness checkout> runs it")
	}
	core := filepath.Join(harness, "modules", "core")
	extra := maps.Clone(helloSkill)
	err := filepath.WalkDir(core, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(core, p)
		extra["examples/hello/modules/core/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extra["examples/hello/modules/core/settings/codex/config.toml"]; !ok {
		t.Fatalf("%s has no settings/codex/config.toml", core)
	}
	patch := func(path string) string {
		return "*** Begin Patch\n*** Add File: " + path + "\n+{}\n*** End Patch\n"
	}
	steps, err := json.Marshal([]map[string]any{
		{"custom": "apply_patch", "input": patch(".reports/x.json")},
		{"call": "exec_command", "args": map[string]any{"cmd": "echo the guard allows this"}},
		{"text": "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs, ok := walledGuard(t, []string{"nodejs"}, string(steps), extra, func(s string) string {
		return strings.Replace(s, "modules:\n", "modules:\n  - name: core\n    source: {path: modules/core}\n", 1)
	})
	if !ok {
		return
	}
	if len(outputs) != 2 {
		t.Fatalf("the tool outputs are %q, want two", outputs)
	}
	if !strings.Contains(outputs[0], "Write no file under .reports/ that ends in .json") {
		t.Errorf("the write of .reports/x.json got %q, want the guard's denial", outputs[0])
	}
	if !strings.Contains(outputs[1], "Output:\nthe guard allows this") {
		t.Errorf("the shell command got %q, want its output", outputs[1])
	}
}
