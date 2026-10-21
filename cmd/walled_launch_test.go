package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// forgetPlaces removes the records of every place, as a person may, so a test that ran
// walled can run without a wall in the same checkout.
func forgetPlaces(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places")); err != nil {
		t.Fatal(err)
	}
}

// TestNoAgentWithoutAWallInARecordedPlace refuses qory harness launch and an unwalled
// qory run in a checkout a walled run used, and from a working folder inside one.
func TestNoAgentWithoutAWallInARecordedPlace(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	if out, err := run(t, "harness", "launch"); err != nil {
		t.Fatalf("before a walled run: %v\n%s", err, out)
	}
	recordCheckout(t, root)
	want := "a walled run has used " + root + ", so qory runs no agent there without a wall, since the run could have left settings, hooks or scripts the agent would run; run it behind a wall, or in another clone or worktree"
	for _, args := range [][]string{{"harness", "launch"}, {"harness", "launch", "--json"}, {"run", "claude", "--local"}, {"run", "claude", "--local", "--wall", "none"}} {
		out, err := run(t, args...)
		if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
			t.Errorf("%v: %v (exit %d)\n%s", args, err, cmd.ExitCode(err), out)
		}
		if strings.Contains(out, "env ") {
			t.Errorf("%v printed a launch:\n%s", args, out)
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}
}

// TestLaunchLeavesOutWhatTheDenyListHolds composes a harness whose launch sets PATH and
// a proxy among its variables: the launch line leaves both out, as a run does, and keeps
// the rest.
func TestLaunchLeavesOutWhatTheDenyListHolds(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: claude
      env:
        PATH: /opt/elsewhere/bin
        HTTPS_PROXY: http://proxy.example.com:3128
        LOG_LEVEL: debug
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "launch")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "LOG_LEVEL=debug")
	lacks(t, out, "PATH=", "HTTPS_PROXY=", "proxy.example.com")
	out, err = run(t, "harness", "launch", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, `"LOG_LEVEL": "debug"`)
	lacks(t, out, `"PATH"`, `"HTTPS_PROXY"`)
}

// TestLaunchTakesOnlyTheHomeQoryComputes is a report that names another home than the
// checkout's: the launch refuses it, as a run does, and prints no launch.
func TestLaunchTakesOnlyTheHomeQoryComputes(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	report := filepath.Join(root, ".qory", "harness-report.json")
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, ".qory", "harness")
	other := tempDir(t)
	writeFile(t, report, strings.ReplaceAll(string(data), `"`+home+`"`, `"`+other+`"`))
	out, err := run(t, "harness", "launch")
	if err == nil || !strings.Contains(err.Error(), "names the home "+other+", and this checkout's home is "+home+"; a launch uses only the home qory computes, so qory prints no launch. Run qory harness compose again") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, other)
}

// TestRunGuardsTheRunnersStepFiles is a walled run on a GitHub Actions runner: each file
// GITHUB_ENV, GITHUB_PATH, GITHUB_OUTPUT, GITHUB_STATE and GITHUB_STEP_SUMMARY names is
// one of Forager's files, so a mount holding one, writable or read-only, is refused
// before anything starts, with a line that names the variable; with the variables unset
// the same mount runs.
func TestRunGuardsTheRunnersStepFiles(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	for _, name := range []string{"GITHUB_ENV", "GITHUB_PATH", "GITHUB_OUTPUT", "GITHUB_STATE", "GITHUB_STEP_SUMMARY"} {
		t.Run(name, func(t *testing.T) {
			runner := tempDir(t)
			file := filepath.Join(runner, strings.ToLower(name))
			writeFile(t, file, "")
			t.Setenv(name, file)
			out, err := run(t, "run", "claude", "--mount", runner, "--", "-p", "hi")
			want := "the mount " + runner + " contains " + file + ", the file " + name + " names for the runner's next steps; the agent could change what they run, so the run does not start. Mount a narrower path (mount_contains_forager_files)"
			if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
				t.Errorf("writable: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
			}
			out, err = run(t, "run", "claude", "--mount", runner+":ro", "--", "-p", "hi")
			want = "the mount " + runner + " contains " + file + ", the file " + name + " names for the runner's next steps; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"
			if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
				t.Errorf("read-only: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
			}
		})
	}
	runner := tempDir(t)
	writeFile(t, filepath.Join(runner, "github_env"), "")
	for _, name := range []string{"GITHUB_ENV", "GITHUB_PATH", "GITHUB_OUTPUT", "GITHUB_STATE", "GITHUB_STEP_SUMMARY"} {
		t.Setenv(name, "")
	}
	if out, err := run(t, "run", "claude", "--mount", runner, "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Errorf("unset: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}

// TestRunGuardsALinkToARunnersStepFile names a runner's step file through a link inside
// the mount, which leads out of it: the agent could point the link elsewhere, so the
// mount is refused before anything starts, with a line that names the file and its
// variable, writable or read-only.
func TestRunGuardsALinkToARunnersStepFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	runner, commands := tempDir(t), tempDir(t)
	writeFile(t, filepath.Join(commands, "set_env"), "")
	if err := os.Symlink(commands, filepath.Join(runner, "commands")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(runner, "commands", "set_env")
	t.Setenv("GITHUB_ENV", file)
	out, err := run(t, "run", "claude", "--mount", runner, "--", "-p", "hi")
	want := "the mount " + runner + " leads through a link to " + file + ", the file GITHUB_ENV names for the runner's next steps; the agent could point it elsewhere, so the run does not start. Mount a narrower path (mount_contains_forager_files)"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("writable: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	out, err = run(t, "run", "claude", "--mount", runner+":ro", "--", "-p", "hi")
	want = "the mount " + runner + " leads through a link to " + file + ", the file GITHUB_ENV names for the runner's next steps; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_forager_files)"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("read-only: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
}

// TestAWalledRunActsOnNothingInTheWorkspaceAfterItsSession pins that once a walled run's
// session ends, qory run runs no file of the workspace and runs no git on this machine
// in it: a walled run may have written anything there, its git configuration, its
// qory.yaml and its scripts among them, and qory reads what it wrote only through the
// checks of its next command. The stand-in for the container writes a key into the
// checkout's git configuration, a script, and a qory.yaml command that runs the script;
// git on this machine is a stand-in that logs where it runs.
func TestAWalledRunActsOnNothingInTheWorkspaceAfterItsSession(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := tempDir(t)
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"git in $PWD: $*\" >> "+log+"\nexec "+realGit+" \"$@\"\n")
	script := filepath.Join(root, "tools", "setup.sh")
	agent := filepath.Join(tempDir(t), "docker")
	writeFile(t, agent, `#!/bin/sh
case "$1" in run)
	echo "the session starts" >> `+log+`
	git -C `+root+` config alias.x log
	mkdir -p `+filepath.Dir(script)+`
	printf '#!/bin/sh\necho "a workspace file ran" >> `+log+`\n' > `+script+`
	chmod +x `+script+`
	printf 'worktree:\n  run:\n    add: [`+script+`]\n' >> `+filepath.Join(root, "qory.yaml")+`
	echo "the session ends" >> `+log+` ;;
esac
exec `+docker+` "$@"
`)
	for _, f := range []string{filepath.Join(bin, "git"), agent} {
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+agent+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	before, after, ok := strings.Cut(string(data), "the session ends\n")
	if !ok {
		t.Fatalf("the session did not run:\n%s", data)
	}
	// The stand-in is the git qory runs: qory ran it in the checkout before the session.
	wants(t, before, "git in "+root)
	lacks(t, after, "a workspace file ran", "git in "+root)
	if strings.Contains(string(data), "a workspace file ran") {
		t.Errorf("a file of the workspace ran:\n%s", data)
	}
}
