package cmd

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/render"
)

// TestNoRuntimeIsNamedAfterAVerbTheWallRuns holds the runtimes this qory renders apart
// from the verbs that carry [inWall]: qory run takes a runtime as its argument, and cobra
// finds a verb of qory run before it reads an argument, so a runtime named after one of
// them could never be started with qory run <name>. The verbs are found by walking the
// command tree and the runtimes are the ones registered, so a new verb or a new runtime
// is held to it without a change here.
func TestNoRuntimeIsNamedAfterAVerbTheWallRuns(t *testing.T) {
	var verbs []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Annotations[inWall] != "" {
			verbs = append(verbs, c.Name())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(Root())
	for _, want := range []string{"forward", "nest", "relay"} {
		if !slices.Contains(verbs, want) {
			t.Errorf("the walk found the verbs %v, without %s: it no longer sees what carries %s", verbs, want, inWall)
		}
	}
	runtimes := render.Names()
	if len(runtimes) == 0 {
		t.Fatal("no runtime is registered")
	}
	for _, name := range runtimes {
		if slices.Contains(verbs, name) {
			t.Errorf("runtime %s has the name of a verb the wall starts inside a container: qory run %s runs the verb, so the runtime could not be started with qory run %s", name, name, name)
		}
	}
}

// TestLaunchSpecLeavesTheHomeOutBehindAWall is the launch's environment as the runner
// takes it until it reads the home on its own: QORY_HARNESS_HOME, then the fixed
// variables, then the defaults; behind a wall, where the runner refuses a QORY_ variable,
// the fixed ones and the defaults alone.
func TestLaunchSpecLeavesTheHomeOutBehindAWall(t *testing.T) {
	l := render.Launch{HarnessHome: "/work/home", Fixed: []string{"CODEX_HOME=/work/home/codex"}, Defaults: []string{"PROFILE=nextjs"}}
	if got, want := launchSpec(l, false), []string{"QORY_HARNESS_HOME=/work/home", "CODEX_HOME=/work/home/codex", "PROFILE=nextjs"}; !slices.Equal(got, want) {
		t.Errorf("launchSpec unwalled %q, want %q", got, want)
	}
	if got, want := launchSpec(l, true), []string{"CODEX_HOME=/work/home/codex", "PROFILE=nextjs"}; !slices.Equal(got, want) {
		t.Errorf("launchSpec walled %q, want %q", got, want)
	}
}
