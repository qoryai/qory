package cmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestRunPassesTheHarnessSettingsInline runs with --model, so the Claude Code settings it
// composes contain that model: Forager gets their JSON, and the agent the copy Forager
// wrote with its hooks, which contains the model. The run's record contains that copy's
// path and no JSON of the settings. A link planted at the settings file is replaced by
// the run's compose, so the agent gets the composed settings, not the link's.
func TestRunPassesTheHarnessSettingsInline(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	settings := filepath.Join(root, ".qory", "harness", "claude", "settings.json")
	out, err := run(t, "run", "claude", "--local", "--model", "a-model-of-the-settings", "--", "-p", "hi")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v\n%s", err, out)
	}
	dir, evs := events(t, root)
	copied := filepath.Join(firstSession(dir), "settings.json")
	wants(t, out, "--settings "+copied+" -p hi")
	if strings.Count(out, "--settings") != 1 {
		t.Errorf("the agent got --settings more than once:\n%s", out)
	}
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), `"model": "a-model-of-the-settings"`, "run forward")
	args := fmt.Sprint(sessionStarted(t, evs)["args"])
	wants(t, args, copied)
	lacks(t, args, "{", "a-model-of-the-settings")
	record, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(record), "a-model-of-the-settings")

	clearRuns(t, root)
	elsewhere := filepath.Join(tempDir(t), "settings.json")
	writeFile(t, elsewhere, `{"model": "a-model-from-elsewhere"}`)
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, settings); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "run", "claude", "--local", "--model", "a-model-of-the-settings", "--", "-p", "hi")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("a linked settings file: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if info, err := os.Lstat(settings); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the run's compose left the link at %s: %v", settings, err)
	}
	dir, _ = events(t, root)
	data, err = os.ReadFile(filepath.Join(firstSession(dir), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), `"model": "a-model-of-the-settings"`)
	lacks(t, string(data), "a-model-from-elsewhere")
}

// TestAWalledRunStartsWithTheHomeInTheCheckout is a walled run of Claude Code with the
// harness's home where qory composes it by default, <checkout>/.qory/harness, inside the
// workspace the wall binds writable. Forager reads no settings file in a place a walled
// run can write; qory passes the settings as their JSON, so the run starts, and the
// agent gets the copy Forager wrote with its hooks, which contains the model --model set.
func TestAWalledRunStartsWithTheHomeInTheCheckout(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	seen := filepath.Join(t.TempDir(), "args")
	runtime := filepath.Join(t.TempDir(), "args-runtime")
	writeFile(t, runtime, "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+seen+"'\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	settings := filepath.Join(root, ".qory", "harness", "claude", "settings.json")
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("the home is not at the checkout's .qory/harness: %v", err)
	}
	t.Setenv("QORY_RUN_KEY", "")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n"+passingWall(t))
	out, err := run(t, "run", "claude", "--model", "a-model-of-the-settings", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the agent did not start: %v\n%s", err, out)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if strings.Count(string(data), "--settings") != 1 {
		t.Fatalf("the agent got --settings other than once: %q", args)
	}
	at := slices.Index(args, "--settings")
	dir, _ := events(t, root)
	if at < 0 || at+1 >= len(args) || args[at+1] != filepath.Join(firstSession(dir), "settings.json") {
		t.Fatalf("the agent's arguments %q name no copy in %s", args, dir)
	}
	copied, err := os.ReadFile(args[at+1])
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(copied), `"model": "a-model-of-the-settings"`, "run forward")
}

// TestRunRefusesSettingsInTheAgentsArguments refuses a run whose agent's arguments set
// --settings, with no value as the last argument, with a value, or as --settings=value,
// under a launch that keeps --settings and one that drops it, where Forager appends
// --settings and its copy. Nothing starts, and the run's record says it failed with no
// session started.
func TestRunRefusesSettingsInTheAgentsArguments(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := fakeRuntime(t)
	settings := filepath.Join(root, ".qory", "harness", "claude", "settings.json")
	keeps := func() {
		composedForFake(t, root, script)
		writeFile(t, settings, `{"model": "a-model-of-the-settings"}`)
	}
	drops := func() {
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: "+script+"\n      args:\n        - [--verbose]\n")
		if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		writeFile(t, settings, `{"model": "a-model-of-the-settings"}`)
	}
	want := "the agent's arguments set --settings, which the harness's launch sets, so the run does not start"
	for name, compose := range map[string]func(){"a launch with --settings": keeps, "a launch without --settings": drops} {
		compose()
		out, err := run(t, "run", "claude", "--local", "--", "-p", "hi")
		if cmd.ExitCode(err) != 3 {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		dir, _ := events(t, root)
		wants(t, out, "--settings "+filepath.Join(firstSession(dir), "settings.json"))
		if strings.Count(out, "--settings") != 1 {
			t.Errorf("%s: the agent got --settings other than once:\n%s", name, out)
		}
		clearRuns(t, root)
		for _, extra := range [][]string{
			{"-p", "hi", "--settings"},
			{"-p", "hi", "--settings", settings},
			{"-p", "hi", "--settings=" + settings},
		} {
			out, err := run(t, append([]string{"run", "claude", "--local", "--"}, extra...)...)
			if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%s, agent's arguments %q: %v (exit %d)\n%s", name, extra, err, cmd.ExitCode(err), out)
			}
			lacks(t, out, "hello from")
			if ends := endsOf(t, root); !slices.Equal(ends, []string{"failed/no_session/-1"}) {
				t.Errorf("%s, agent's arguments %q: the refused run ended %v", name, extra, ends)
			}
			clearRuns(t, root)
		}
	}
}

// TestRunRefusesALaunchThatEndsInABareSettings refuses a run whose harness.launch ends in
// --settings with no file after it, with the agent's arguments and without them: Forager
// would give Claude Code --settings --settings and its copy, or take the agent's first
// argument as the settings. A launch that sets --settings a second time with no value,
// or as the value of the first, is refused as setting it more than once. Nothing starts,
// and the run's record says it failed with no session started.
func TestRunRefusesALaunchThatEndsInABareSettings(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := fakeRuntime(t)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	tail := ", so the run does not start"
	for _, c := range []struct{ args, want string }{
		{"        - [--verbose, --settings]\n", "the launch sets --settings with no file after it" + tail},
		{"        - [--settings, \"${dir}/settings.json\", --settings]\n", "the launch sets --settings more than once" + tail},
		{"        - [--settings, --settings, \"${dir}/settings.json\"]\n", "the launch sets --settings more than once" + tail},
	} {
		writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: "+script+"\n      args:\n"+c.args)
		if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, extra := range [][]string{nil, {"-p", "hi"}} {
			args := []string{"run", "claude", "--local"}
			if extra != nil {
				args = append(append(args, "--"), extra...)
			}
			out, err := run(t, args...)
			if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("launch %s with agent's arguments %q: %v (exit %d), want %q\n%s", strings.TrimSpace(c.args), extra, err, cmd.ExitCode(err), c.want, out)
			}
			lacks(t, out, "hello from")
			if ends := endsOf(t, root); !slices.Equal(ends, []string{"failed/no_session/-1"}) {
				t.Errorf("launch %s with agent's arguments %q: the refused run ended %v", strings.TrimSpace(c.args), extra, ends)
			}
			clearRuns(t, root)
		}
	}
}
