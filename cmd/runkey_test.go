package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
)

// runKeyMarker stands in for a run key: no output, file or environment may hold it.
const runKeyMarker = "run-key-marker-7d1f0c94"

// noValueUnder fails the test when a file under dir holds value.
func noValueUnder(t *testing.T, dir, value string) {
	t.Helper()
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), value) {
			t.Errorf("%s holds the run key", path)
		}
		return nil
	})
}

// holdsRunKey fails the test when text names QORY_RUN_KEY or holds its value, without
// printing text, which may be a machine's environment.
func holdsRunKey(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range []string{"QORY_RUN_KEY", runKeyMarker} {
		if strings.Contains(text, s) {
			t.Errorf("%s holds %s", what, s)
		}
	}
}

// TestNoAgentHasTheRunKey is QORY_RUN_KEY in qory's environment: qory takes it when it
// starts and keeps it, so an unwalled runtime's environment lacks it, as do a walled
// run's docker command and its container's environment file. qory's output, the
// record, the state directory and the configuration directory never hold its value,
// nor does what qory config prints.
func TestNoAgentHasTheRunKey(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	seen := filepath.Join(t.TempDir(), "seen")
	script := filepath.Join(t.TempDir(), "env-runtime")
	writeFile(t, script, "#!/bin/sh\nenv >> '"+seen+"'\nprintf '%s\\n' \"$@\" >> '"+seen+"'\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)

	t.Setenv("QORY_RUN_KEY", runKeyMarker)
	out, err := run(t, "run")
	if err != nil {
		t.Fatalf("the unwalled run: %v\n%s", err, out)
	}
	if got := config.TakenRunKey(); got != runKeyMarker {
		t.Errorf("qory kept the run key %q", got)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the runtime did not run: %v\n%s", err, out)
	}
	// The environment is never printed: it is the machine's.
	if !strings.Contains(string(data), "QORY_RUN_ID=") {
		t.Error("the runtime's environment has no QORY_RUN_ID")
	}
	holdsRunKey(t, "the unwalled runtime's environment", string(data))
	lacks(t, out, runKeyMarker)

	docker, log := fakeDocker(t)
	dockerEnv := filepath.Join(t.TempDir(), "docker-env")
	wrapper := filepath.Join(t.TempDir(), "docker")
	writeFile(t, wrapper, "#!/bin/sh\nenv >> '"+dockerEnv+"'\nexec '"+docker+"' \"$@\"\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+wrapper+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	t.Setenv("QORY_RUN_KEY", runKeyMarker)
	out, err = run(t, "run")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("the walled run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "inside the container")
	lacks(t, out, runKeyMarker)
	for _, f := range []string{log, dockerEnv} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		holdsRunKey(t, f, string(data))
	}

	t.Setenv("QORY_RUN_KEY", runKeyMarker)
	out, err = run(t, "config")
	if err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	lacks(t, out, runKeyMarker)

	state := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory")
	noValueUnder(t, state, runKeyMarker)
	noValueUnder(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory"), runKeyMarker)
	noValueUnder(t, root, runKeyMarker)
}

// TestTheRunKeyIsNoAgentsVariable is QORY_RUN_KEY named for the agent: --env and
// wall.env refuse it with the words they refuse the access key's secret with, and
// neither refusal holds the value.
func TestTheRunKeyIsNoAgentsVariable(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	for _, name := range []string{"QORY_ACCESS_KEY_SECRET", "QORY_RUN_KEY"} {
		t.Setenv("QORY_RUN_KEY", runKeyMarker)
		out, err := run(t, "run", "--env", name)
		if want := "--env " + name + ": the variable is Forager's own and never the agent's"; cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
			t.Errorf("--env %s: %v (exit %d), want %q", name, err, cmd.ExitCode(err), want)
		}
		lacks(t, out, runKeyMarker, "hello from")
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	for _, name := range []string{"QORY_ACCESS_KEY_SECRET", "QORY_RUN_KEY"} {
		writeFile(t, file, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  helper: "+staticELF(t)+"\n  env: ["+name+"]\n")
		t.Setenv("QORY_RUN_KEY", runKeyMarker)
		out, err := run(t, "run")
		if want := file + ": wall.env[0] names a variable that is Forager's own and never the agent's"; cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
			t.Errorf("wall.env [%s]: %v (exit %d), want %q", name, err, cmd.ExitCode(err), want)
		}
		lacks(t, out, runKeyMarker, "hello from")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
}
