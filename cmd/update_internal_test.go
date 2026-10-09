package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// TestNoticeIsABoxWithTheVersionsAndTheCommand pins the notice a command ends with when
// a newer release exists: a frame, the two versions, the changelog and qory update.
func TestNoticeIsABoxWithTheVersionsAndTheCommand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out strings.Builder
	notice(ui.New(&out), "0.4.0", "0.5.0")
	for _, want := range []string{
		"╔═", "═╗", "╚═", "═╝",
		"║                  Update available! 0.4.0 → 0.5.0                  ║",
		"║   Changelog: https://github.com/qoryai/qory/releases/tag/v0.5.0   ║",
		"║                   Run \"qory update\" to update.                    ║",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the notice lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasPrefix(out.String(), "\n  ╔") || !strings.HasSuffix(out.String(), "╝\n\n") {
		t.Errorf("the notice is not set off by blank lines:\n%q", out.String())
	}
}

// TestStartUpdateCheckIsSilentOffATerminal pins that the daily look is skipped when the
// output is not a terminal, and when QORY_NO_UPDATE_CHECK or CI is set, by asking that
// the returned function prints nothing to a buffer with a release version behind
// anything.
func TestStartUpdateCheckIsSilentOffATerminal(t *testing.T) {
	was := Version
	t.Cleanup(func() { Version = was })
	Version = "0.0.1"
	var out strings.Builder
	StartUpdateCheck(&out, nil)()
	if out.Len() != 0 {
		t.Errorf("a buffer got a notice:\n%s", out.String())
	}
}

// TestNoUpdateCheckInsideTheWall feeds the command lines enclose gives the wall for
// qory's own binary, the relay, the start of a Docker of the agent's own and the hook
// forwarder, to the check that keeps the look for a release out of the container: each
// is a verb the wall runs inside, with -v before it too, and no verb a person runs is.
func TestNoUpdateCheckInsideTheWall(t *testing.T) {
	var spec session.Spec
	r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux"}}
	if err := enclose(&spec, r, wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d, ok := spec.Wall.(*wall.Docker)
	if !ok || spec.Forwarder[0] != wall.HelperPath {
		t.Fatalf("the wall is %#v, the forwarder %v", spec.Wall, spec.Forwarder)
	}
	for _, args := range [][]string{
		append(slices.Clone(d.RelayArgs), "3128=172.30.0.1:40000"),
		append(slices.Clone(d.NestArgs), "--user", "1000:1000", "--", "claude", "--settings", "/w/settings.json"),
		spec.Forwarder[1:],
	} {
		if !walledVerb(args) {
			t.Errorf("%v: not a verb the wall runs inside", args)
		}
		if !walledVerb(append([]string{"-v"}, args...)) {
			t.Errorf("-v %v: not a verb the wall runs inside", args)
		}
	}
	for _, args := range [][]string{
		{"run", "claude", "--", "-p", "hi"},
		{"run", "--image", "go-docker"},
		{"run", "resend", "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"},
		{"run"},
		{"config"},
		{"no-such-verb"},
		nil,
	} {
		if walledVerb(args) {
			t.Errorf("%v: read as a verb the wall runs inside", args)
		}
	}
}
