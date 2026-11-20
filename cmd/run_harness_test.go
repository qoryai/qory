package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/render/codex"
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
// export DOCKER_HOST, and CODEX_HOME stays fixed over --env, at the walled Codex home,
// which Codex's deny list keeps from --env. The report records every export as a
// default.
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
	wants(t, out, "qory run: CODEX_HOME from --env is not used: no source may set it\n")
	lacks(t, out, "CORE_SCRIPTS from --env")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "env: CORE_SCRIPTS=/elsewhere\n", "env: LOG_DIR=from-server\n")
	wants(t, string(data), "env: CODEX_HOME=/var/lib/qory-codex-home\n")
	lacks(t, string(data), "DOCKER_HOST=", "/not-the-template", "LOG_DIR="+home)
	_, evs := events(t, root)
	got, _ := json.Marshal(sessionApplied(t, evs)["variables"])
	if want := `[{"from":"fixed","lost":[{"from":"run","why":"denied"}],"name":"CODEX_HOME"},{"from":"run","lost":[{"from":"harness","why":"overridden"}],"name":"CORE_SCRIPTS"},{"lost":[{"from":"harness","why":"denied"}],"name":"DOCKER_HOST"},{"from":"apiary","lost":[{"from":"harness","why":"overridden"}],"name":"LOG_DIR"}]`; string(got) != want {
		t.Errorf("run.policy_applied variables %s, want %s", got, want)
	}
}

// TestRunComposesOverTheReportsMarks is a report in the checkout that marks two variables
// fixed: TOOL_PRELOAD, a name no template sets, and CODEX_HOME, the template's, with a
// value of its own. The run composes before it starts, which writes the report again
// without the marks: --env sets TOOL_PRELOAD, which qory run prints as ***, and
// CODEX_HOME is the template's, which --env does not replace.
func TestRunComposesOverTheReportsMarks(t *testing.T) {
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
	wants(t, out, "TOOL_PRELOAD=*** CODEX_HOME="+home+"/codex\n", "qory run: CODEX_HOME from --env is not used: no source may set it\n")
	lacks(t, out, "TOOL_PRELOAD from --env", "/forged")
	again, err := report.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range again.LaunchEnv["codex"] {
		if v.Name == "TOOL_PRELOAD" || v.Value == "/forged" {
			t.Errorf("the report the run composed has %+v", v)
		}
	}
	_, evs := events(t, root)
	got, _ := json.Marshal(sessionApplied(t, evs)["variables"])
	if !strings.Contains(string(got), `{"from":"run","lost":[{"from":"shell","why":"overridden"}],"name":"TOOL_PRELOAD"}`) {
		t.Errorf("run.policy_applied variables %s: TOOL_PRELOAD is not the run's", got)
	}
}

// walledForager writes a forager.yaml whose wall is a program standing in for docker, and
// returns that program's log, which stays absent until the wall runs a command.
func walledForager(t *testing.T) string {
	t.Helper()
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
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

// TestRunComposesOverAReportNamingAnotherHome is a report in the checkout that contains
// another home, as a repository could commit or a walled agent write: the run composes
// under the cache folder, and mounts the home it computes.
// --home set to the composed home, with a report for another checkout, is refused
// before the wall runs anything, since the run would mount that checkout.
func TestRunComposesOverAReportNamingAnotherHome(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	log := walledForager(t)
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
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a report naming another home: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), forged)
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	for _, id := range recorded(t, root) {
		if err := os.RemoveAll(filepath.Join(runsDir(t, root), id)); err != nil {
			t.Fatal(err)
		}
	}

	outside := filepath.Join(tempDir(t), "homes")
	if out, err := run(t, "hc", "--runtime", "claude", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home = filepath.Join(outside, checkout.Key(root))
	path = home + "-report.json"
	if rep, err = report.Read(path); err != nil {
		t.Fatal(err)
	}
	rep.Checkout = os.Getenv("HOME")
	if err := report.Write(path, rep); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "run", "claude", "--home", home)
	want := "remove the harness report " + path + ", or give --home the home of the checkout " + os.Getenv("HOME") + ". The report is for that checkout, and " + home + " is not its home; a run uses only its checkout's home, so the run does not start"
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("--home with a report naming another checkout: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	if out, err := run(t, "hc", "--runtime", "claude", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "hc", "--runtime", "claude", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "run", "claude", "--home", home)
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("--home naming the composed home: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+copyWork(t, string(data))+",dst="+copyWork(t, string(data))+" ")
	lacks(t, string(data), forged)
}

// TestWalledRunTakesTheHomeFromYourOwnFile is a checkout whose own qory.yaml sets
// harness.home outside it: the compose puts the home there, a run without a wall starts
// on it, and a walled run refuses before the wall runs anything, since behind a wall the
// home is a mount and comes from the qory.yaml in qory's configuration directory alone.
// A qory.yaml you own in the directory above the checkout that sets it is refused the
// same way. The same value in the configuration directory's file starts the walled run,
// the other file still setting it.
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
	log := walledForager(t)
	_, err := run(t, "run", "claude")
	want := "the checkout's qory.yaml sets harness.home, and a walled run takes the home from ~/.config/qory/qory.yaml alone, so the run does not start. Set harness.home in ~/.config/qory/qory.yaml, or remove it from the checkout's qory.yaml"
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("a walled run with the checkout's harness.home: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	ancestor := filepath.Join(filepath.Dir(root), "qory.yaml")
	writeFile(t, ancestor, "apiVersion: qory.dev/v1alpha1\nharness:\n  home: "+homes+"\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), stack)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, err = run(t, "run", "claude")
	want = ancestor + " sets harness.home, and a walled run takes the home from ~/.config/qory/qory.yaml alone, so the run does not start. Set harness.home in ~/.config/qory/qory.yaml, or remove it from " + ancestor
	if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("a walled run with an ancestor's harness.home: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	startedNothing(t, root, log)

	writeFile(t, user, strings.Replace(launch, "harness:\n", "harness:\n  home: "+homes+"\n", 1))
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run with harness.home in the configuration directory's file too: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+homes+string(filepath.Separator))
}

// TestAWalledCodexRunRefusesAReservedConfigArgument is a run of codex given -c
// model_provider=x after qory run's --: without a wall Forager's join stops the run
// before the program starts, with exit status 1, and a walled run stops before the
// agent's container starts, in the compose step, with exit status 1, as a harness's
// config.toml that sets the key stops it. Given -c model_verbosity=low, a run without a
// wall passes the argument to the program, and a walled run passes it to the agent's
// container.
func TestAWalledCodexRunRefusesAReservedConfigArgument(t *testing.T) {
	root, _ := exportingCheckout(t, fakeRuntime(t))
	out, err := run(t, "run", "codex", "--", "-c", "model_provider=x")
	fix := "remove the -c that sets model_provider from the launch's arguments"
	cause := "the session sets that setting for Codex, or the setting changes where Codex sends its requests"
	if cmd.ExitCode(err) != 1 || err == nil || err.Error() != fix+"\n"+cause {
		t.Fatalf("a run without a wall: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), fix+"\n"+cause, out)
	}
	wants(t, out, "✗ "+fix+"\n  "+cause+"\n")
	lacks(t, out, " -c model_provider=x\n")
	out, err = run(t, "run", "codex", "--", "-c", "model_verbosity=low")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("a run without a wall with an allowed key: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, " -c model_verbosity=low\n")
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	log := walledForager(t)
	_, err = run(t, "run", "codex", "--", "-c", "model_provider=x")
	want := "Remove `--config model_provider` from the run's Codex arguments.\n  A Codex run takes this key from qory and Forager, not from the arguments."
	if cmd.ExitCode(err) != 1 || err == nil || err.Error() != want {
		t.Errorf("a walled run: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), "model_provider")
	if out, err := run(t, "run", "codex", "--", "-c", "model_verbosity=low"); err != nil && cmd.ExitCode(err) == 1 {
		t.Fatalf("a walled run with an allowed key: %v\n%s", err, out)
	}
	if data, err = os.ReadFile(log); err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "model_verbosity=low")
}

// TestACodexRunWithoutAWallStopsOnTheHookArguments is a run of codex without a wall given,
// after qory run's --, an argument that turns off the hooks' trust or a hook setting:
// each stops the run before Forager's join, with exit status 1 and the text of a walled
// run, whose lines are each at most 100 characters. An argument that a walled run alone
// refuses, such as another feature or another key, reaches the program.
func TestACodexRunWithoutAWallStopsOnTheHookArguments(t *testing.T) {
	exportingCheckout(t, fakeRuntime(t))
	config := func(key string) string {
		return "Remove `--config " + key + "` from the run's Codex arguments.\n  A Codex run takes this key from qory and Forager, not from the arguments."
	}
	feature := func(flag, name string) string {
		return "Remove `" + flag + " " + name + "` from the run's Codex arguments.\n  A Codex run takes this key from qory and Forager, not from the arguments."
	}
	hooks := "Remove `--dangerously-bypass-hook-trust` from the run's Codex arguments.\n  A Codex run takes its hook trust from qory and Forager, not from the arguments."
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--dangerously-bypass-hook-trust"}, hooks},
		{[]string{"--dangerously-bypass-hook-trust=true"}, hooks},
		{[]string{"-c", "features.unified_exec_tty=true"}, config("features.unified_exec_tty")},
		{[]string{"--config=hooks.x=1"}, config("hooks.x")},
		{[]string{"-c", "features={hooks=false}"}, config("features")},
		{[]string{"-c", "features.codex_hooks=true"}, config("features.codex_hooks")},
		{[]string{"--enable", "unified_exec_tty"}, feature("--enable", "unified_exec_tty")},
		{[]string{"--disable=codex_hooks"}, feature("--disable", "codex_hooks")},
		{[]string{"--enable", "codex_hooks"}, feature("--enable", "codex_hooks")},
	} {
		out, err := run(t, append([]string{"run", "codex", "--"}, c.args...)...)
		if cmd.ExitCode(err) != 1 || err == nil || err.Error() != c.want {
			t.Errorf("%q: %v (exit %d), want %q\n%s", c.args, err, cmd.ExitCode(err), c.want, out)
			continue
		}
		fix, _, _ := strings.Cut(c.want, "\n")
		wants(t, out, "✗ "+fix+"\n")
		for _, line := range strings.Split(err.Error(), "\n") {
			if len(line) > 100 {
				t.Errorf("%q: a line of %d characters, want at most 100: %q", c.args, len(line), line)
			}
		}
	}
	for _, args := range [][]string{{"-c", "model=x"}, {"--enable", "some_other_feature"}} {
		out, err := run(t, append([]string{"run", "codex", "--"}, args...)...)
		if cmd.ExitCode(err) != 3 {
			t.Fatalf("%q: %v (exit %d), want the program's exit 3\n%s", args, err, cmd.ExitCode(err), out)
		}
		wants(t, out, " "+strings.Join(args, " ")+"\n")
	}
}

// joinsNoting makes the join of each run note the session's spec in specs and end as a
// session that succeeded, in a record in a new temp folder.
func joinsNoting(t *testing.T, specs *[]session.SessionSpec) {
	t.Helper()
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		*specs = append(*specs, spec)
		return &session.Result{Dir: t.TempDir(), State: event.StateSucceeded}, nil
	}))
}

// etcCodexMounts are the mounts of spec whose target is /etc/codex or lies in it.
func etcCodexMounts(spec session.SessionSpec) []wall.Mount {
	var out []wall.Mount
	for _, m := range spec.Mounts {
		if m.Target == codex.SystemInWall || strings.HasPrefix(m.Target, codex.SystemInWall+"/") {
			out = append(out, m)
		}
	}
	return out
}

// TestAWalledCodexRunBindsEtcCodex is a person's walled Codex run, a walled Claude Code
// run and an unwalled Codex run: the walled Codex session mounts one folder read-only at
// /etc/codex, etc-codex beside codex-home in the run's folder, with the harness's hooks in
// hooks.json while the session runs, and has no setup: Forager, not a setup step, stops
// an image with a link at /etc/codex. Neither other session mounts anything there or has
// a setup.
func TestAWalledCodexRunBindsEtcCodex(t *testing.T) {
	root, _ := exportingCheckout(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	var specs []session.SessionSpec
	var hooks string
	joinsNoting(t, &specs)
	if out, err := run(t, "run", "codex", "--", "hi"); len(specs) != 1 {
		t.Fatalf("an unwalled run: %v\n%s", err, out)
	}
	walledForager(t)
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		specs = append(specs, spec)
		if ms := etcCodexMounts(spec); len(ms) == 1 {
			data, err := os.ReadFile(filepath.Join(ms[0].Path, "hooks.json"))
			if err != nil {
				t.Errorf("the bound hooks.json: %v", err)
			}
			hooks = string(data)
		}
		return &session.Result{Dir: t.TempDir(), State: event.StateSucceeded}, nil
	}))
	if out, err := run(t, "run", "codex", "--", "hi"); err != nil || len(specs) != 2 {
		t.Fatalf("a walled Codex run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	ms := etcCodexMounts(specs[1])
	if len(ms) != 1 || ms[0].Target != "/etc/codex" || !ms[0].ReadOnly || filepath.Base(ms[0].Path) != "etc-codex" || filepath.Base(filepath.Dir(ms[0].Path)) != "codex-fixed" {
		t.Errorf("the mounts at /etc/codex: %+v", ms)
	}
	if !strings.HasPrefix(hooks, "{\n  \"hooks\": {") {
		t.Errorf("the bound hooks.json: %q", hooks)
	}
	if len(specs[1].Setup) != 0 || len(specs[1].SetupRunner) != 0 {
		t.Errorf("a walled Codex run has a setup: %q through %q", specs[1].Setup, specs[1].SetupRunner)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: "+fakeRuntime(t)+"\n")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); len(specs) != 3 {
		t.Fatalf("a walled Claude Code run: %v\n%s", err, out)
	}
	for i, name := range map[int]string{0: "an unwalled Codex run", 2: "a walled Claude Code run"} {
		if ms := etcCodexMounts(specs[i]); len(ms) != 0 {
			t.Errorf("%s mounts at /etc/codex: %+v", name, ms)
		}
		if len(specs[i].Setup) != 0 || len(specs[i].SetupRunner) != 0 {
			t.Errorf("%s has a setup: %q through %q", name, specs[i].Setup, specs[i].SetupRunner)
		}
	}
}
