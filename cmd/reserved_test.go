package cmd_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// reservedWhy is what every refusal of a reserved runtime name says after naming it.
func reservedWhy(name string) string {
	return name + ", a name no runtime can have: qory run " + name + " is the verb the wall starts inside a container"
}

// TestMachineConfigurationRefusesAReservedRuntime is harness.runtime and harness.launch in
// the machine's qory.yaml naming a verb the wall runs: qory config refuses the file, as it
// refuses any other mistake in it, and so does qory run, which reads it for its runtime.
func TestMachineConfigurationRefusesAReservedRuntime(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{"harness: {runtime: nest}\n", "harness.runtime lists " + reservedWhy("nest") + "; name another runtime"},
		{"harness: {runtime: [claude, relay]}\n", "harness.runtime lists " + reservedWhy("relay") + "; name another runtime"},
		{"harness: {launch: {forward: {command: forward}}}\n", "harness.launch lists " + reservedWhy("forward") + "; leave it out"},
	} {
		root := newCheckout(t)
		copyFixture(t, "two-modules", root)
		machineConfig(t, c.doc)
		for _, args := range [][]string{{"config"}, {"run"}} {
			_, err := run(t, args...)
			if err == nil || !strings.HasSuffix(err.Error(), c.want) || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%s with %s: %v, want exit %d and %q", args[0], c.doc, err, cmd.ExitInput, c.want)
			}
		}
	}
}

// TestStackRefusesAReservedRuntime is a checkout's own stack targeting a verb the wall
// runs: the compose refuses it, the way it refuses a runtime listed twice, and writes
// nothing.
func TestStackRefusesAReservedRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, "target:\n  runtime: [claude, nest]\nmodules:\n  - name: core\n    source: {path: modules/core}\n")
	want := "target.runtime lists " + reservedWhy("nest") + "; name another runtime"
	_, err := run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v, want exit %d and %q", err, cmd.ExitInput, want)
	}
	gone(t, root, ".qory")
}

// TestRuntimeFlagRefusesAReservedRuntime is --runtime naming a verb the wall runs, on
// every command that takes it: the compose writes nothing, and launch and remove leave
// the composed harness as it is.
func TestRuntimeFlagRefusesAReservedRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose", "--runtime", "claude"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	before := snapshot(t, root)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"harness", "compose", "--runtime", "claude,forward"}, "--runtime lists " + reservedWhy("forward") + "; name another runtime"},
		{[]string{"harness", "launch", "--runtime", "relay"}, "relay is a name no runtime can have: qory run relay is the verb the wall starts inside a container; name another runtime"},
		{[]string{"harness", "remove", "--runtime", "nest"}, "--runtime names " + reservedWhy("nest") + "; name another runtime"},
	} {
		_, err := run(t, c.args...)
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v, want exit %d and %q", c.args, err, cmd.ExitInput, c.want)
		}
	}
	if got := snapshot(t, root); len(got) != len(before) {
		t.Errorf("the checkout holds %d paths after the refusals, %d before", len(got), len(before))
	}
}
