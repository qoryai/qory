package cmd

import (
	"strings"
	"testing"

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

// TestNoUpdateCheckInsideTheWall pins the verbs the wall runs inside a container, which
// never look for a release: there the look would be a connection in the run's record,
// made as the container's root by nest.
func TestNoUpdateCheckInsideTheWall(t *testing.T) {
	for _, c := range []struct {
		args   []string
		inside bool
	}{
		{[]string{"run", "nest", "--user", "1000:1000", "--", "claude"}, true},
		{[]string{"run", "relay", "3128=172.30.0.1:40000"}, true},
		{[]string{"run", "forward"}, true},
		{[]string{"run", "claude", "--", "-p", "hi"}, false},
		{[]string{"run"}, false},
		{[]string{"config"}, false},
		{nil, false},
	} {
		if walledVerb(c.args) != c.inside {
			t.Errorf("%v: inside the wall %v, want %v", c.args, !c.inside, c.inside)
		}
	}
}
