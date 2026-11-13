package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestAPersonsRunLoadsAsBefore is a run without a run key, behind a wall and without
// one: the launch has no --strict-mcp-config and no MCP list qory adds; those are a
// dispatched run's. Without a wall, the agent's environment has no CLAUDE_CONFIG_DIR
// and the launch starts without the agent-env verb. Behind a wall, as on a dispatched
// walled run, CLAUDE_CONFIG_DIR is the agent's home, and the launch starts through the
// agent-env verb, which links the agent's transcripts to .claude/projects.
func TestAPersonsRunLoadsAsBefore(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\necho \"hello from $QORY_RUN_ID with $*\"\necho \"config [$CLAUDE_CONFIG_DIR]\"\nexit 3\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 3 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello from ", " with --settings ", " -p hi", "config []")
	lacks(t, out, "--strict-mcp-config", "--mcp-config", "mcpServers", "--setting-sources", "qory-agent-home", "agent-env")

	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	out, err = run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 4 {
		t.Fatalf("walled run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "/fake-runtime --settings ", " -p hi", "env: CLAUDE_CONFIG_DIR=/tmp/qory-agent-home\n",
		" run agent-env ", " projects .claude/projects -- ")
	lacks(t, string(data), "--strict-mcp-config", "--mcp-config", "mcpServers", "--setting-sources")
}

// TestAWalledRunTakesAHomeWhereARunWithARunKeyComposes is a person's walled run on a
// home under qory/homes in the cache directory, where a run with a run key composes when
// no --home or harness.home sets its home: Forager mounts it read-only, and the agent
// starts in the container. A home under qory's state directory would stop the run, since that
// directory contains the run records. That a run given a run key composes there is
// [TestARunKeyComposesOutsideTheFolder]; that a home inside the checkout stops it is
// [TestARunKeyWithAHomeInsideTheFolderStops].
func TestAWalledRunTakesAHomeWhereARunWithARunKeyComposes(t *testing.T) {
	root := runKeyCheckout(t)
	homes := filepath.Dir(runKeyHome(t, root))
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", homes); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("QORY_RUN_KEY", "")
	walledByDocker(t)
	out, err := run(t, "run", "claude", "--home", homes, "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 4 {
		t.Fatalf("walled run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "inside the container")
}

// TestACheckoutsLaunchArgumentsApplyToAPersonsRun is the checkout's qory.yaml setting
// harness.launch.claude.args: a person's run starts with them. A run given a run key
// reads no machine key of the checkout's file, see
// TestARunKeyReadsNoMachineKeyOfTheCheckout.
func TestACheckoutsLaunchArgumentsApplyToAPersonsRun(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := fakeRuntime(t)
	composedForFake(t, root, script)
	configure(t, root, []string{"launch:", "  claude:", "    command: " + script, "    args: [[--dangerously-skip-permissions]]"}, nil)
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude")
	if err == nil || cmd.ExitCode(err) != 3 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello from ", " with --dangerously-skip-permissions")
}
