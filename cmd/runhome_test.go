package cmd_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// runHomes are the folders of the runs' homes in the folder at, sorted.
func runHomes(t *testing.T, at string) []string {
	t.Helper()
	entries, err := os.ReadDir(at)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var homes []string
	for _, e := range entries {
		homes = append(homes, filepath.Join(at, e.Name()))
	}
	return homes
}

// oneRunHome is the one home in the folder at.
func oneRunHome(t *testing.T, at string) string {
	t.Helper()
	homes := runHomes(t, at)
	if len(homes) != 1 {
		t.Fatalf("the homes in %s: %v, want one", at, homes)
	}
	return homes[0]
}

// TestTwoRunsOfOneCheckoutKeepTheirHomes is a second walled run of a checkout that starts while
// the first run's agent runs: each run composes into a home for that run alone, so the first
// home keeps every file while the second run composes and after it ends. Each home is gone after
// its run, and the report stays per checkout, valid, beside them.
func TestTwoRunsOfOneCheckoutKeepTheirHomes(t *testing.T) {
	root, _ := committedFixture(t)
	homes := runKeyHome(t, root)
	var first, second string
	var whole map[string]string
	joins := 0
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		spec.OnVariables(session.Applied{})
		joins++
		if joins == 2 {
			all := runHomes(t, homes)
			if len(all) != 2 || !slices.Contains(all, first) {
				t.Errorf("the homes while two runs run: %v, the first %s", all, first)
			}
			for _, home := range all {
				if home != first {
					second = home
				}
			}
			if !maps.Equal(snapshot(t, first), whole) {
				t.Error("the second run's compose changed the first run's home")
			}
			return &session.Result{}, nil
		}
		first = oneRunHome(t, homes)
		whole = snapshot(t, first)
		if len(whole) == 0 {
			t.Error("the first run's home is empty")
		}
		if out, err := run(t, "run", "claude", "--", "-p", "second"); err != nil {
			t.Errorf("the second run: %v\n%s", err, out)
		}
		if !maps.Equal(snapshot(t, first), whole) {
			t.Error("the second run's end changed the first run's home")
		}
		if second == "" {
			t.Error("the second run did not join")
		} else if _, err := os.Lstat(second); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the second run's home stays after its run: %v", err)
		}
		return &session.Result{}, nil
	}))
	if out, err := run(t, "run", "claude", "--", "-p", "first"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if joins != 2 {
		t.Fatalf("%d joins, want 2", joins)
	}
	if left := runHomes(t, homes); len(left) != 0 {
		t.Errorf("the homes after both runs: %v", left)
	}
	rep, err := report.Read(homes + "-report.json")
	if err != nil {
		t.Fatalf("the report beside the homes: %v", err)
	}
	if at := filepath.Base(rep.Home); at != filepath.Base(first) && at != filepath.Base(second) {
		t.Errorf("the report has the home %s, want %s or %s", rep.Home, first, second)
	}
}

// TestALaterWalledRunReadsTheReport is the report that stays per checkout: the compose of a later
// walled run reads the report of the last one, whose home is gone.
func TestALaterWalledRunReadsTheReport(t *testing.T) {
	root, _ := committedFixture(t)
	agentInCopy(t, func(session.SessionSpec) {})
	path := runKeyHome(t, root) + "-report.json"
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rep, err := report.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	rep.Replaced = []string{"kept-by-the-next-compose"}
	if err := report.Write(path, rep); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if next, err := report.Read(path); err != nil || len(next.Replaced) != 1 {
		t.Errorf("the next compose did not read the report: %+v, %v", next.Replaced, err)
	}
}

// TestARunWithAnIDInUseLeavesTheHomeOfTheRunWithThatID is a second run that gives the id of a run
// that is running: its open fails, and the home at that id stays the first run's.
func TestARunWithAnIDInUseLeavesTheHomeOfTheRunWithThatID(t *testing.T) {
	root, _ := committedFixture(t)
	homes := runKeyHome(t, root)
	agentInCopy(t, func(session.SessionSpec) {
		home := oneRunHome(t, homes)
		whole := snapshot(t, home)
		out, err := run(t, "run", "claude", "--run-id", filepath.Base(home), "--", "-p", "second")
		if err == nil {
			t.Errorf("the second run with the id passed:\n%s", out)
		}
		if !maps.Equal(snapshot(t, home), whole) {
			t.Error("the second run changed the home of the run with the id")
		}
	})
	if out, err := run(t, "run", "claude", "--", "-p", "first"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestARunWithAUsedIDLeavesAHomeAtThatID is a run that gives the id of a run with a record: its open
// fails, so a folder at that id is not this run's to remove.
func TestARunWithAUsedIDLeavesAHomeAtThatID(t *testing.T) {
	root, _ := committedFixture(t)
	agentInCopy(t, func(session.SessionSpec) {})
	const id = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"
	if out, err := run(t, "run", "claude", "--run-id", id, "--", "-p", "first"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	other := filepath.Join(runKeyHome(t, root), id, "other")
	writeFile(t, other, "another run's\n")
	out, err := run(t, "run", "claude", "--run-id", id, "--", "-p", "second")
	if err == nil {
		t.Fatalf("the run with the used id passed:\n%s", out)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the run with the used id removed the home at that id: %v\n%s", err, out)
	}
}

// TestARunsHomeIsGoneAfterAKeptCopy is a run whose agent left work that does not come back: the
// copy stays with its line, which does not depend on the home, and the home is gone.
func TestARunsHomeIsGoneAfterAKeptCopy(t *testing.T) {
	root, _ := committedFixture(t)
	homes := runKeyHome(t, root)
	var copyDir string
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		spec.OnVariables(session.Applied{})
		copyDir = spec.Dir
		oneRunHome(t, homes)
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		return nil, errors.New("the wait for the agent failed")
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	wants(t, out, "read the run's files in "+copyDir)
	if data, err := os.ReadFile(filepath.Join(copyDir, "left.txt")); err != nil || string(data) != "left\n" {
		t.Errorf("the kept copy has %q, %v", data, err)
	}
	if left := runHomes(t, homes); len(left) != 0 {
		t.Errorf("the homes after the run: %v\n%s", left, out)
	}
}

// TestARunsHomeIsGoneAfterAStop is a signal during the agent's session of a walled run.
func TestARunsHomeIsGoneAfterAStop(t *testing.T) {
	root := copyCheckout(t)
	homes := runKeyHome(t, root)
	agentInCopy(t, func(session.SessionSpec) {
		oneRunHome(t, homes)
		term(t)
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		term(t)
		return cmd.GitSessionOnHost(ctx, spec)
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "qory run: got SIGTERM")
	if left := runHomes(t, homes); len(left) != 0 {
		t.Errorf("the homes after the stop: %v\n%s", left, out)
	}
}

// TestAWalledRunWithAGivenHomeKeepsIt is --home and harness.home on a walled run: one home per
// checkout, as before, which stays after the run.
func TestAWalledRunWithAGivenHomeKeepsIt(t *testing.T) {
	for _, how := range []string{"--home", "harness.home"} {
		t.Run(how, func(t *testing.T) {
			root, _ := committedFixture(t)
			homes := tempDir(t)
			args := []string{"run", "claude"}
			if how == "--home" {
				args = append(args, "--home", homes)
			} else {
				machineConfig(t, "harness:\n  home: "+homes+"\n")
			}
			agentInCopy(t, func(session.SessionSpec) {})
			if out, err := run(t, append(args, "--", "-p", "hi")...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			home := filepath.Join(homes, filepath.Base(runKeyHome(t, root)))
			if _, err := os.Stat(filepath.Join(home, "AGENTS.md")); err != nil {
				t.Errorf("the home stays: %v", err)
			}
		})
	}
}

// TestAWalledRunPrintsWhatDoesNotReachTheSession is a walled run on each runtime that reads part
// of a harness from the checkout alone, with a stack that has every part: one line before the
// agent starts, on stderr. Other runtimes print none.
func TestAWalledRunPrintsWhatDoesNotReachTheSession(t *testing.T) {
	const reason = ": a walled run has the harness in a home outside the checkout, and "
	for runtime, parts := range map[string]string{
		"gemini":   "skills, agents, commands and instructions",
		"amp":      "skills and instructions",
		"opencode": "instructions",
		"cursor":   "instructions",
		"copilot":  "hooks and instructions",
		"claude":   "",
		"codex":    "",
	} {
		t.Run(runtime, func(t *testing.T) {
			committedFixture(t)
			var at int
			var printed string
			var out output
			root := cmd.Root()
			root.SetArgs([]string{"run", runtime, "--", "-p", "hi"})
			root.SetOut(&out)
			root.SetErr(&out)
			agentInCopy(t, func(session.SessionSpec) {
				printed = out.String()
				at++
			})
			if err := root.Execute(); err != nil || at != 1 {
				t.Fatalf("%d joins, %v\n%s", at, err, out.String())
			}
			line := "qory run: run with --wall none for " + runtime + "'s " + parts + reason + runtime + " reads those from the checkout.\n"
			if parts == "" {
				if strings.Contains(out.String(), reason) {
					t.Errorf("a line for %s:\n%s", runtime, out.String())
				}
				return
			}
			if strings.Count(printed, line) != 1 || strings.Count(out.String(), reason) != 1 {
				t.Errorf("want once before the agent starts:\n%s\nprinted by then:\n%s\nall:\n%s", line, printed, out.String())
			}
		})
	}
}

// TestAnUnwalledRunPrintsNoLineOfTheCheckoutsParts is a run with no wall, which has its links.
func TestAnUnwalledRunPrintsNoLineOfTheCheckoutsParts(t *testing.T) {
	committedFixture(t)
	agentInCopy(t, func(session.SessionSpec) {})
	out, err := run(t, "run", "gemini", "--wall", "none", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "reads those from the checkout") {
		t.Errorf("a line on a run with no wall:\n%s", out)
	}
}

// TestAWalledRunWithLinksCheckoutStarts is harness.links: checkout on a walled run, with the home
// of the cache and with a home given: a walled run is a run with no links, so the run starts and
// the folder gets nothing.
func TestAWalledRunWithLinksCheckoutStarts(t *testing.T) {
	for name, flags := range map[string]func() []string{
		"the cache": func() []string { return nil },
		"--home":    func() []string { return []string{"--home", tempDir(t)} },
	} {
		t.Run(name, func(t *testing.T) {
			root, _ := committedFixture(t)
			machineConfig(t, "harness:\n  links: checkout\n")
			ran := false
			agentInCopy(t, func(session.SessionSpec) { ran = true })
			args := append(append([]string{"run", "claude"}, flags()...), "--", "-p", "hi")
			if out, err := run(t, args...); err != nil || !ran {
				t.Fatalf("the agent ran: %v: %v\n%s", ran, err, out)
			}
			noQoryFolder(t, root)
			if _, err := os.Lstat(filepath.Join(root, ".claude")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the folder has .claude: %v", err)
			}
		})
	}
}

// TestAWalledRunWithLinksCheckoutAndAHomeInsideStops is --home .qory/harness with the key: the
// line of a home inside the folder.
func TestAWalledRunWithLinksCheckoutAndAHomeInsideStops(t *testing.T) {
	root, log := committedFixture(t)
	machineConfig(t, "harness:\n  links: checkout\n")
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "run", "claude", "--home", ".qory/harness")
	want := "set --home to a folder outside " + root + ": " + home + " is inside " + root
	if err == nil || !strings.HasPrefix(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	startedNothing(t, root, log)
}

// TestAnUnwalledRunWithLinksCheckoutKeepsItsLinks is the key on a run with no wall.
func TestAnUnwalledRunWithLinksCheckoutKeepsItsLinks(t *testing.T) {
	root, _ := committedFixture(t)
	machineConfig(t, "harness:\n  links: checkout\n")
	agentInCopy(t, func(session.SessionSpec) {})
	if out, err := run(t, "run", "claude", "--wall", "none", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if info, err := os.Lstat(filepath.Join(root, ".mcp.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link .mcp.json: %v, %v", info, err)
	}
}

// TestADispatchedRunPrintsNoLineOfTheCheckoutsParts is a walled run given a run key on a runtime
// that reads part of the harness from the checkout: the run has no person who could act on it.
func TestADispatchedRunPrintsNoLineOfTheCheckoutsParts(t *testing.T) {
	keyedFolder(t, true)
	walledByDocker(t)
	out, err, n := inCheckoutRun(t, "gemini")
	if !n.joined {
		t.Fatalf("the run did not go on to the join: %v\n%s", err, out)
	}
	if strings.Contains(out, "reads those from the checkout") {
		t.Errorf("a line on a dispatched run:\n%s", out)
	}
}

// TestADispatchedWalledRunWithLinksCheckoutStarts is the key on a walled run given a run key.
func TestADispatchedWalledRunWithLinksCheckoutStarts(t *testing.T) {
	keyedFolder(t, true)
	walledByDocker(t)
	machineConfig(t, "harness:\n  links: checkout\n")
	out, err, n := inCheckoutRun(t, "claude")
	if !n.joined {
		t.Fatalf("the run did not go on to the join: %v\n%s", err, out)
	}
}
