package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestAPersonsRunLoadsAsBefore is a run without a run key, behind a wall and without
// one: the launch has no --strict-mcp-config and no MCP list qory adds, the agent's
// environment no CLAUDE_CONFIG_DIR, and the container starts the launch itself, not
// through the agent-env verb; those are a dispatched walled run's.
func TestAPersonsRunLoadsAsBefore(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 3 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello from ", " with --settings ", " -p hi")
	lacks(t, out, "--strict-mcp-config", "--mcp-config", "mcpServers", "--setting-sources")

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
	wants(t, string(data), "/fake-runtime --settings ", " -p hi")
	lacks(t, string(data), "--strict-mcp-config", "--mcp-config", "mcpServers", "--setting-sources", "CLAUDE_CONFIG_DIR=", "qory-agent-home", "agent-env")
}

// TestARunKeyBehindAWallNeedsTheHarnessOutsideTheCheckout is a run given a run key
// behind a wall: with the harness home inside the checkout, .qory/harness, it is refused
// as input before the gateway's address is reached and before anything is recorded; with
// the home outside, composed and run with --home, it goes on to the gateway. A person's
// walled run with the home inside starts as before, see [TestAPersonsRunLoadsAsBefore].
func TestARunKeyBehindAWallNeedsTheHarnessOutsideTheCheckout(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")

	out, err := run(t, "run", "--run-key-fd", descriptor(t, key))
	home := filepath.Join(root, ".qory", "harness")
	want := "the harness home " + home + " is inside the checkout, which the repository's files and commands can change, so a run given a run key behind a wall does not start. Compose the harness outside the checkout with --home, such as qory harness compose --home ~/.cache/qory/homes, and run with the same --home"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("home inside: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if _, n := gw.received(); n != 0 {
		t.Error("a refused run reached the gateway")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}

	outside := t.TempDir()
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = run(t, "run", "--home", outside, "--run-key-fd", descriptor(t, key))
	if err == nil || strings.Contains(err.Error(), "inside the checkout") {
		t.Fatalf("home outside: %v\n%s", err, out)
	}
	if first, _, _ := strings.Cut(out, "\n"); first != "qory run: through the gateway "+gw.host()+", run "+keyRunID {
		t.Errorf("home outside: the first line is %q\n%s", first, out)
	}
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
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: "+script+"\n      args: [[--dangerously-skip-permissions]]\n")
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude")
	if err == nil || cmd.ExitCode(err) != 3 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello from ", " with --dangerously-skip-permissions")
}
