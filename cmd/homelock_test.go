package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/foragerdir"
)

// holderText is the lock file text of a run that another process holds the home for.
const holderText = "run " + otherRunID + "\npid 4242\nstarted 2026-11-16T10:00:00Z\n"

// homeFix is the first line of the error of a second run of a home.
const homeFix = "Wait for the run that uses this home to end, or set another --home or harness.home."

// wantHomeInUse fails the test unless err is the error of a second run of the home: exit
// 1, the fix first on a line of at most 100 characters, then the run that holds the home
// and the home's path.
func wantHomeInUse(t *testing.T, err error, home string) {
	t.Helper()
	want := homeFix + "\nRun " + otherRunID + ", pid 4242, started 2026-11-16T10:00:00Z uses the home " + home
	if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("%v (exit %d), want exit 1 with %q", err, cmd.ExitCode(err), want)
	}
	if len(homeFix) > 100 {
		t.Errorf("the first line has %d characters, want at most 100", len(homeFix))
	}
}

// homeCase is a run of a home that is not the run's alone: where the home is, how the run
// is started and what the machine's configuration sets.
type homeCase struct {
	name string
	home func(root string) string
	args []string
	conf func(root string)
}

// sharedHomes are the runs whose home the lock covers: a person's unwalled run in the
// checkout's .qory/harness, and a walled run with --home or harness.home.
func sharedHomes(t *testing.T) []homeCase {
	given := tempDir(t)
	return []homeCase{
		{name: "a person's unwalled run",
			home: func(root string) string { return filepath.Join(root, ".qory", "harness") },
			args: []string{"run", "claude", "--wall", "none", "--", "-p", "hi"}},
		{name: "a walled run with --home",
			home: func(root string) string { return filepath.Join(given, "flag", checkout.Key(root)) },
			args: []string{"run", "claude", "--home", filepath.Join(given, "flag"), "--", "-p", "hi"}},
		{name: "a walled run with harness.home",
			home: func(root string) string { return filepath.Join(given, "setting", checkout.Key(root)) },
			args: []string{"run", "claude", "--", "-p", "hi"},
			conf: func(string) { machineConfig(t, "harness:\n  home: "+filepath.Join(given, "setting")+"\n") }},
	}
}

// TestASecondRunOfAHomeStopsBeforeItComposes is a run of a home that another run holds,
// for a person's unwalled run in the checkout's .qory/harness and for a walled run with
// --home or harness.home: it stops before the compose, exit 1, with the fix first and the
// holder on the second line, and nothing is composed. Once the holder lets go, the run
// composes and starts, and lets the home go at its end.
func TestASecondRunOfAHomeStopsBeforeItComposes(t *testing.T) {
	for _, c := range sharedHomes(t) {
		t.Run(c.name, func(t *testing.T) {
			root, _ := committedFixture(t)
			if c.conf != nil {
				c.conf(root)
			}
			home := c.home(root)
			if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
				t.Fatal(err)
			}
			held, err := foragerdir.LockFileText(home+".lock", holderText)
			if err != nil {
				t.Fatal(err)
			}
			ran := false
			agentInCopy(t, func(session.SessionSpec) { ran = true })
			out, err := run(t, c.args...)
			wantHomeInUse(t, err, home)
			if ran {
				t.Errorf("the agent ran in a home that another run holds\n%s", out)
			}
			if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the run composed the home it does not hold: %v", err)
			}
			held.Release()
			if out, err := run(t, c.args...); err != nil || !ran {
				t.Fatalf("the run after the holder ended: %v, the agent ran %v\n%s", err, ran, out)
			}
			again, err := foragerdir.LockFile(home + ".lock")
			if err != nil {
				t.Fatalf("the home is still held after the run ended: %v", err)
			}
			again.Release()
		})
	}
}

// TestARunHoldsItsHomeUntilItEnds is a run of a home that the lock covers: while the agent
// runs, the home's lock file is held and names the run's id, its process and its start.
func TestARunHoldsItsHomeUntilItEnds(t *testing.T) {
	for _, c := range sharedHomes(t) {
		t.Run(c.name, func(t *testing.T) {
			root, _ := committedFixture(t)
			if c.conf != nil {
				c.conf(root)
			}
			home := c.home(root)
			var held error
			var text string
			agentInCopy(t, func(session.SessionSpec) {
				_, held = foragerdir.LockFile(home + ".lock")
				text = foragerdir.LockText(home + ".lock")
			})
			if out, err := run(t, c.args...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !errors.Is(held, foragerdir.ErrLocked) {
				t.Errorf("a lock on the home during the run: %v, want %v", held, foragerdir.ErrLocked)
			}
			lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
			if len(lines) != 3 || !strings.HasPrefix(lines[0], "run ") || len(lines[0]) == len("run ") ||
				lines[1] != "pid "+strconv.Itoa(os.Getpid()) || !strings.HasPrefix(lines[2], "started 20") {
				t.Errorf("the lock file holds %q, want the run's id, this process's id and a start time", text)
			}
		})
	}
}

// TestARunsOwnHomeTakesNoLock is a run whose home is for that run alone: a person's
// walled run with no --home, and a run from a run code with none. While the agent runs,
// no lock file is beside the home, beside its folder of runs, or beside the checkout's
// folder of homes.
func TestARunsOwnHomeTakesNoLock(t *testing.T) {
	noLocks := func(t *testing.T, home string) {
		t.Helper()
		for dir := filepath.Dir(home); dir != filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(home)))); dir = filepath.Dir(dir) {
			if found, _ := filepath.Glob(filepath.Join(dir, "*.lock")); len(found) > 0 {
				t.Errorf("lock files %v beside the run's home %s", found, home)
			}
		}
	}
	t.Run("a person's walled run", func(t *testing.T) {
		committedFixture(t)
		var home string
		agentInCopy(t, func(s session.SessionSpec) { home = s.HarnessHome; noLocks(t, home) })
		if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil || home == "" {
			t.Fatalf("%v, home %q\n%s", err, home, out)
		}
	})
	t.Run("a run from a run code", func(t *testing.T) {
		var home string
		r := codeHarness{
			before: func() []string { walledByDocker(t); return nil },
			joined: func(s session.SessionSpec) { home = s.HarnessHome; noLocks(t, home) },
		}.run(t)
		if !errors.Is(r.err, cmd.ErrJoinedNowhere) || home == "" {
			t.Fatalf("%v (exit %d), home %q\n%s", r.err, cmd.ExitCode(r.err), home, r.out)
		}
	})
}

// TestASecondRunFromARunCodeLeavesTheFirstRunsInputs is a second run from a run code of
// the home that --home sets, which starts while the first run's agent runs: it stops
// before its compose, so the first run's home and its copy of inputs.json stay as they are.
func TestASecondRunFromARunCodeLeavesTheFirstRunsInputs(t *testing.T) {
	home := filepath.Join(tempDir(t), "homes")
	var nowhere cmd.Nowhere
	var documents []byte
	var before map[string]string
	var second error
	joins := 0
	r := codeHarness{
		before:  func() []string { walledByDocker(t); return []string{"--home", home} },
		nowhere: func(n *cmd.Nowhere) { nowhere = *n },
		relay:   func(g *codeRelay) { documents = g.documents },
		joined: func(s session.SessionSpec) {
			joins++
			inputsAtJoin(t, s)
			before = snapshot(t, s.HarnessHome)
			g := newCodeRelay(t)
			g.documents, g.runID = otherInputs(t, documents), otherRunID
			again := nowhere
			again.Dir, again.OnOpen, again.OnStep = tempDir(t), nil, nil
			_, _, restore := cmd.OpenNowhereWith(again)
			_, second = run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL, "--home", home)
			restore()
			if after := snapshot(t, s.HarnessHome); len(after) != len(before) || after["inputs.json"] != before["inputs.json"] || before["inputs.json"] == "" {
				t.Errorf("the second run changed the first run's home: %v, then %v", before, after)
			}
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || joins != 1 {
		t.Fatalf("%v (exit %d), %d joins, want the first run's only\n%s", r.err, cmd.ExitCode(r.err), joins, r.out)
	}
	if second == nil || cmd.ExitCode(second) != 1 || !strings.HasPrefix(second.Error(), homeFix+"\nRun "+keyRunID) || !strings.Contains(second.Error(), " uses the home ") {
		t.Errorf("the second run: %v (exit %d), want exit 1 with the home's holder", second, cmd.ExitCode(second))
	}
}
