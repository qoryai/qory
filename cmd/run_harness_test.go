package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// exportingCheckout is a checkout whose stack composes module core for codex, core
// exporting CORE_SCRIPTS, LOG_DIR and DOCKER_HOST, a name the deny list holds, with
// command the program codex's launch starts through harness.launch in the user's file,
// which leaves the template's own CODEX_HOME in place. It returns the composed home.
func exportingCheckout(t *testing.T, command string) (root, home string) {
	t.Helper()
	root = newCheckout(t)
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  CORE_SCRIPTS: scripts\n  LOG_DIR: logs\n  DOCKER_HOST: sockets\n")
	writeFile(t, filepath.Join(root, "modules", "core", "scripts", "run.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "modules", "core", "logs", ".keep"), "")
	writeFile(t, filepath.Join(root, "modules", "core", "sockets", ".keep"), "")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    codex:\n      command: "+command+"\n")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return root, filepath.Join(root, ".qory", "harness")
}

// TestRunTakesAModuleExportAsADefault is a walled run whose server sets LOG_DIR, a name
// module core exports, and whose --env sets CORE_SCRIPTS, another: an export is a written
// default, so the server's value and the run's win over it, the deny list leaves out the
// export DOCKER_HOST, and the template's CODEX_HOME stays fixed over --env. The report
// records every export as a default.
func TestRunTakesAModuleExportAsADefault(t *testing.T) {
	root, home := exportingCheckout(t, "codex")
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.LaunchEnv["codex"] {
		if v.Fixed {
			t.Errorf("the report records %s as fixed", v.Name)
		}
	}
	docker, log := fakeDocker(t)
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe"}}`)
	srv.variables = `{"LOG_DIR":{"value":"from-server"}}`
	serverFile(t, srv, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	t.Setenv("CORE_SCRIPTS", "/elsewhere")
	t.Setenv("CODEX_HOME", "/not-the-template")
	out, err := run(t, "run", "codex", "--env", "CORE_SCRIPTS", "--env", "CODEX_HOME")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: CODEX_HOME from --env is not used: the harness sets it\n")
	lacks(t, out, "CORE_SCRIPTS from --env")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "env: CORE_SCRIPTS=/elsewhere\n", "env: LOG_DIR=from-server\n", "env: CODEX_HOME="+home+"/codex\n")
	lacks(t, string(data), "DOCKER_HOST=", "/not-the-template", "LOG_DIR="+home)
	_, evs := events(t, root)
	got, _ := json.Marshal(evs["dev.qory.run.policy_applied"][0]["variables"])
	if want := `[{"from":"fixed","lost":[{"from":"run","why":"fixed"}],"name":"CODEX_HOME"},{"from":"run","lost":[{"from":"harness","why":"overridden"}],"name":"CORE_SCRIPTS"},{"lost":[{"from":"harness","why":"denied"}],"name":"DOCKER_HOST"},{"from":"apiary","lost":[{"from":"harness","why":"overridden"}],"name":"LOG_DIR"}]`; string(got) != want {
		t.Errorf("run.policy_applied variables %s, want %s", got, want)
	}
}

// TestRunFixesNoNameTheReportMarks is a report in the checkout that marks two variables
// fixed: TOOL_PRELOAD, a name no template sets, and CODEX_HOME, the template's own, with a
// value of its own. Neither mark fixes anything: --env sets TOOL_PRELOAD over the
// report's value, and CODEX_HOME is the template's, which --env does not replace.
func TestRunFixesNoNameTheReportMarks(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\necho \"TOOL_PRELOAD=$TOOL_PRELOAD CODEX_HOME=$CODEX_HOME\"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	root, home := exportingCheckout(t, script)
	path := filepath.Join(root, ".qory", "harness-report.json")
	rep, err := report.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep.LaunchEnv["codex"] = append(rep.LaunchEnv["codex"],
		report.Var{Name: "TOOL_PRELOAD", Value: "$QORY_HARNESS_HOME/modules/core/scripts/run.sh", From: "module core", Fixed: true},
		report.Var{Name: "CODEX_HOME", Value: "/forged", From: "module core", Fixed: true})
	if err := report.Write(path, rep); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOOL_PRELOAD", "from-env")
	t.Setenv("CODEX_HOME", "/not-the-template")
	out, err := run(t, "run", "codex", "--env", "TOOL_PRELOAD", "--env", "CODEX_HOME")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "TOOL_PRELOAD=from-env CODEX_HOME="+home+"/codex\n", "qory run: CODEX_HOME from --env is not used: the harness sets it\n")
	lacks(t, out, "TOOL_PRELOAD from --env", "/forged")
	_, evs := events(t, root)
	got, _ := json.Marshal(evs["dev.qory.run.policy_applied"][0]["variables"])
	if !strings.Contains(string(got), `{"from":"run","lost":[{"from":"harness","why":"overridden"},{"from":"shell","why":"overridden"}],"name":"TOOL_PRELOAD"}`) {
		t.Errorf("run.policy_applied variables %s: TOOL_PRELOAD is not the run's over the report's default", got)
	}
}

// walledRunner writes a runner file whose wall is a program standing in for docker, and
// returns that program's log, which stays absent until the wall runs a command.
func walledRunner(t *testing.T) string {
	t.Helper()
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	return log
}

// startedNothing fails the test when the wall ran a command or a run left a record.
func startedNothing(t *testing.T, root, log string) {
	t.Helper()
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		data, _ := os.ReadFile(log)
		t.Errorf("the wall ran a command:\n%s", data)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left a record: %v", ids)
	}
}

// TestRunRefusesAReportNamingAnotherHome is a report in the checkout that names another
// home, as a repository could commit or a walled agent write: the run computes the home
// itself, refuses before the wall runs anything, and says to compose again. --home that
// names the composed home, with a report naming another checkout, is refused too. A
// compose writes the report again, and the run then mounts the home it computes.
func TestRunRefusesAReportNamingAnotherHome(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	log := walledRunner(t)
	home := filepath.Join(root, ".qory", "harness")
	path := filepath.Join(root, ".qory", "harness-report.json")
	rep, err := report.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(os.Getenv("HOME"), ".ssh")
	rep.Home = forged
	if err := report.Write(path, rep); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "run", "claude")
	want := "the harness report .qory/harness-report.json names the home " + forged + ", and this checkout's home is " + home + "; a run uses only the home qory computes, so the run does not start. Run qory harness compose again"
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("a report naming another home: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	rep.Home, rep.Checkout = home, os.Getenv("HOME")
	if err := report.Write(path, rep); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "run", "claude", "--home", home)
	want = "the harness report " + path + " names the checkout " + os.Getenv("HOME") + ", and " + home + " is not that checkout's home; a run uses only a checkout's own home, so the run does not start. Run qory harness compose again"
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("--home with a report naming another checkout: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	composedForFake(t, root, "claude")
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("run after a compose: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+root+",dst="+root+" ")
	lacks(t, string(data), forged)
	if out, err := run(t, "run", "claude", "--home", home); cmd.ExitCode(err) != 4 {
		t.Fatalf("--home naming the composed home: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}

// TestWalledRunTakesTheHomeFromYourOwnFile is a checkout whose own qory.yaml sets
// harness.home outside it: the compose puts the home there, a run without a wall starts
// on it, and a walled run refuses before the wall runs anything, since behind a wall the
// home is a mount and comes from your own qory.yaml alone. The same value in your file
// starts it, and so does a checkout whose harness.home leaves the home where your files
// put it.
func TestWalledRunTakesTheHomeFromYourOwnFile(t *testing.T) {
	root := newCheckout(t)
	homes := tempDir(t)
	script := fakeRuntime(t)
	stack := "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n"
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), stack+"  home: "+homes+"\n")
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	launch := "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: " + script + "\n"
	writeFile(t, user, launch)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a run without a wall: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	log := walledRunner(t)
	_, err := run(t, "run", "claude")
	want := "the checkout's qory.yaml sets harness.home, and a walled run takes the home from your own qory.yaml alone, so the run does not start. Set harness.home in ~/.config/qory/qory.yaml, or remove it from the checkout's qory.yaml"
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("a walled run with the checkout's harness.home: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	writeFile(t, user, strings.Replace(launch, "harness:\n", "harness:\n  home: "+homes+"\n", 1))
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run with harness.home in your file too: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+homes+string(filepath.Separator))
}
