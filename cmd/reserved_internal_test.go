package cmd

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/stack"
)

// TestReservedRuntimesAreTheVerbsTheWallRuns holds the names no runtime can have to the
// verbs that carry [inWall]: a verb added without its name, or a name left after its verb
// is gone, fails here. Each verb is found by qory run <name>, and so are the arguments
// that select them.
func TestReservedRuntimesAreTheVerbsTheWallRuns(t *testing.T) {
	var verbs []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if v := c.Annotations[inWall]; v != "" {
			if v != c.Name() {
				t.Errorf("%s carries %s %q, not its own name", c.CommandPath(), inWall, v)
			}
			if c.Parent() == nil || c.Parent().Name() != "run" {
				t.Errorf("%s is not a verb of qory run", c.CommandPath())
			}
			verbs = append(verbs, c.Name())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(Root())
	slices.Sort(verbs)
	if !slices.Equal(verbs, stack.ReservedRuntimes) {
		t.Errorf("the verbs the wall runs are %v; stack.ReservedRuntimes is %v", verbs, stack.ReservedRuntimes)
	}
	for _, args := range [][]string{relayArgs, nestArgs, forwardArgs} {
		if c, _, err := Root().Find(args); err != nil || !slices.Contains(stack.ReservedRuntimes, c.Name()) {
			t.Errorf("%v finds %v, %v", args, c, err)
		}
	}
}

// TestLaunchRefusesAReservedRuntime is a report composed for a runtime no runtime can be
// named, which no compose writes now, and the same name asked for: qory run and qory
// harness launch refuse it before its descriptor or its launch is looked up.
func TestLaunchRefusesAReservedRuntime(t *testing.T) {
	for _, name := range stack.ReservedRuntimes {
		rep := report.Report{Target: report.Target{Runtimes: report.Runtimes{name}}}
		_, _, err := resolveLaunch(rep, config.Config{}, "")
		want := "the harness is composed for " + name + ", a name no runtime can have: qory run " + name + " is the verb the wall starts inside a container; compose it for another runtime"
		if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
			t.Errorf("composed for %s: %v, want %q", name, err, want)
		}
		_, _, err = resolveLaunch(rep, config.Config{}, name)
		want = name + " is a name no runtime can have: qory run " + name + " is the verb the wall starts inside a container; name another runtime"
		if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
			t.Errorf("asked for %s: %v, want %q", name, err, want)
		}
	}
}
