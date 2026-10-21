package cmd_test

import (
	"errors"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// prebuildAgent is an agent image by digest.
const prebuildAgent = "example.com/acme/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestRunPrebuildWithoutConfigurationExitsApart is qory run prebuild on a repository
// with no development container configuration: one line, and the status that tells it
// apart from a build and from a failure.
func TestRunPrebuildWithoutConfigurationExitsApart(t *testing.T) {
	out, err := run(t, "run", "prebuild", "--repository", t.TempDir(), "--agent-image", prebuildAgent,
		"--user", "1234:2345", "--tag", "acme/shop:prebuild", "--helper", staticELF(t))
	want := "qory run prebuild: the repository has no development container configuration, .devcontainer/devcontainer.json or .devcontainer.json; no image is built\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) != cmd.ExitNoConfiguration || cmd.ExitNoConfiguration != 7 {
		t.Errorf("%v exits %d", err, cmd.ExitCode(err))
	}
}

// TestRunPrebuildRefusesAsAnInputError is a refusal: one line, printed once, and the
// status of a mistake in the input.
func TestRunPrebuildRefusesAsAnInputError(t *testing.T) {
	out, err := run(t, "run", "prebuild", "--repository", t.TempDir(), "--agent-image", prebuildAgent,
		"--user", "1000:0", "--tag", "acme/shop:prebuild", "--helper", staticELF(t))
	want := "qory run prebuild: --user is uid:gid, two numbers, neither of them 0\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v exits %d", err, cmd.ExitCode(err))
	}
}

// TestRunPrebuildAndSeedAreHidden is the two verbs: in the tree, hidden from the help
// and the command reference.
func TestRunPrebuildAndSeedAreHidden(t *testing.T) {
	for _, name := range []string{"prebuild", "seed"} {
		if c, _, _ := cmd.Root().Find([]string{"run", name}); c == nil || c.Name() != name || !c.Hidden {
			t.Errorf("qory run %s is not a hidden verb", name)
		}
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "prebuild", "seed")
}
