package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// setupRun is what a walled run from a control plane that lists repositories came to:
// the specs its AgentEnv, its Setup and its session's join got, its output, its error,
// the outcomes its close got and the calls the run got.
type setupRun struct {
	agents []session.SessionSpec
	setups []session.SetupSpec
	joins  []session.SessionSpec
	out    string
	err    error
	closes []session.Outcome
	calls  []string
}

// runSetUp runs qory run claude with a run key behind the fake wall, with limits and
// wall.setup set to where, "" for none, its run opened nowhere with the repositories
// given, which qory run fetches from h into its working folder, or with none, the
// working folder holding acme/shop's qory.yaml. setupErr is the error of the run's Setup.
func runSetUp(t *testing.T, h *hostFetch, where, repositories string, setupErr error) setupRun {
	t.Helper()
	return runSetUpWith(t, h, where, repositories, setupErr, nil)
}

// runSetUpWith is [runSetUp] with agentEnv as the result of the run's AgentEnv, nil for
// the fake's: the server's system and repositories.
func runSetUpWith(t *testing.T, h *hostFetch, where, repositories string, setupErr error, agentEnv func() ([]string, error)) setupRun {
	t.Helper()
	docker, _ := fakeDocker(t)
	section := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n  cpus: \"2\"\n  memory: 8g\n  pids_limit: 512\n  shm_size: 1g\n"
	if where != "" {
		section += "  setup: " + where + "\n"
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), section)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	var r setupRun
	defer cmd.SetJoinSession(func(s session.SessionSpec) { r.joins = append(r.joins, s) })()
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: repositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		Setup: func(s session.SetupSpec) error {
			r.setups = append(r.setups, s)
			return setupErr
		},
		AgentEnv: func(s session.SessionSpec) ([]string, error) {
			r.agents = append(r.agents, s)
			if agentEnv != nil {
				return agentEnv()
			}
			return []string{"QORY_RUN_REPOSITORIES=" + repositories, "QORY_RUN_SYSTEM=" + proxygittest.Host}, nil
		},
	})
	defer restore()
	// A run with no repositories composes the checkout as it is: acme/shop's qory.yaml.
	if repositories == "" {
		writeFile(t, filepath.Join(h.dir, "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: \""+h.server.URL("acme/harness")+"\", ref: main, path: modules/core}\n"))
	}
	// A run given a run key behind a wall composes the harness outside the checkout.
	r.out, r.err = run(t, "run", "claude", "--home", t.TempDir(), "--run-key-fd", descriptor(t, key))
	r.closes, r.calls = closes(), calls()
	return r
}

// prepareCommand is the one setup command of a run that lists repositories.
var prepareCommand = []session.SetupCommand{{Name: "prepare", Args: []string{wall.HelperPath, "run", "prepare"}}}

// TestAWalledRunSetsUpOutsideWithTheJoinsPlaces is a walled run from a control plane
// that lists acme/shop, with wall.setup absent and set to outside: once qory run fetched
// the repository and composed the harness, AgentEnv gets the session's spec as the join
// gets it, and the run's Setup gets one command, prepare, which runs qory run prepare
// from the qory inside, AgentEnv's result as its variables, and the session's working
// folder, ForagerFiles, Written and Limits as the join gets them, so the setup
// container is checked and limited as the agent's container is. The session that
// joins has no setup commands.
func TestAWalledRunSetsUpOutsideWithTheJoinsPlaces(t *testing.T) {
	for _, where := range []string{"", "outside"} {
		t.Run("setup "+where, func(t *testing.T) {
			h := newHostFetch(t, "", "main")
			r := runSetUp(t, h, where, "acme/shop", nil)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.agents) != 1 || len(r.setups) != 1 || len(r.joins) != 1 {
				t.Fatalf("%d AgentEnvs, %d setups, %d joins, %v (exit %d)\n%s", len(r.agents), len(r.setups), len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			if want := []string{"host proxy", "host proxy closed", "stack proxy", "stack proxy closed", "fetch image", "agent env", "setup", "join"}; !slices.Equal(r.calls, want) {
				t.Errorf("the calls: %v, want %v", r.calls, want)
			}
			s, j := r.setups[0], r.joins[0]
			if !slices.EqualFunc(s.Commands, prepareCommand, sameCommand) {
				t.Errorf("the setup commands: %v, want %v", s.Commands, prepareCommand)
			}
			if want := []string{"QORY_RUN_REPOSITORIES=acme/shop", "QORY_RUN_SYSTEM=" + proxygittest.Host}; !slices.Equal(s.Env, want) {
				t.Errorf("the setup's variables: %q, want %q", s.Env, want)
			}
			if a := r.agents[0]; a.Dir != j.Dir || a.Runtime != j.Runtime || !slices.Equal(a.LaunchFixed, j.LaunchFixed) || !slices.Equal(a.LaunchDefaults, j.LaunchDefaults) || !slices.Equal(a.Variables.Machine, j.Variables.Machine) || !slices.Equal(a.Variables.Run, j.Variables.Run) {
				t.Errorf("AgentEnv got a spec other than the join's:\n%+v\n%+v", a, j)
			}
			if s.Dir == "" || s.Dir != j.Dir {
				t.Errorf("the setup's working folder is %q, the join's %q", s.Dir, j.Dir)
			}
			if len(j.ForagerFiles) == 0 || !slices.Equal(s.ForagerFiles, j.ForagerFiles) {
				t.Errorf("the setup's ForagerFiles %q\nthe join's %q", s.ForagerFiles, j.ForagerFiles)
			}
			if len(j.Written) == 0 || !slices.Equal(s.Written, j.Written) {
				t.Errorf("the setup's Written %q\nthe join's %q", s.Written, j.Written)
			}
			if want := (wall.Limits{CPUs: "2", Memory: "8g", PIDs: 512, ShmSize: "1g"}); s.Limits != want || j.Limits != want {
				t.Errorf("the setup's limits %+v, the join's %+v, want %+v", s.Limits, j.Limits, want)
			}
			if len(j.Setup) != 0 || len(j.SetupRunner) != 0 {
				t.Errorf("the join has setup commands %v through %v", j.Setup, j.SetupRunner)
			}
		})
	}
}

// TestAWalledRunSetsUpInsideTheWall is a walled run from a control plane that lists
// acme/shop, with wall.setup inside: the run's Setup is not called, and the session that
// joins has the setup command prepare, run through qory run setup-command from the qory
// inside.
func TestAWalledRunSetsUpInsideTheWall(t *testing.T) {
	h := newHostFetch(t, "", "main")
	r := runSetUp(t, h, "inside", "acme/shop", nil)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	if want := []string{"host proxy", "host proxy closed", "stack proxy", "stack proxy closed", "fetch image", "join"}; !slices.Equal(r.calls, want) || len(r.setups) != 0 {
		t.Errorf("the calls: %v, want %v", r.calls, want)
	}
	j := r.joins[0]
	if !slices.EqualFunc(j.Setup, prepareCommand, sameCommand) {
		t.Errorf("the session's setup commands: %v, want %v", j.Setup, prepareCommand)
	}
	if want := []string{wall.HelperPath, "run", "setup-command"}; !slices.Equal(j.SetupRunner, want) {
		t.Errorf("the setup runner: %v, want %v", j.SetupRunner, want)
	}
}

// TestAWalledRunWithNoRepositoriesHasNoSetup is a walled run from a control plane whose
// run configuration lists no repositories, outside and inside: nothing is fetched, the
// run's Setup is not called, and the session that joins has no setup commands.
func TestAWalledRunWithNoRepositoriesHasNoSetup(t *testing.T) {
	for _, where := range []string{"outside", "inside"} {
		t.Run(where, func(t *testing.T) {
			h := newHostFetch(t, "", "main")
			r := runSetUp(t, h, where, "", nil)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
				t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			if slices.Contains(r.calls, "setup") || len(r.setups) != 0 {
				t.Errorf("the calls: %v", r.calls)
			}
			if j := r.joins[0]; len(j.Setup) != 0 || len(j.SetupRunner) != 0 {
				t.Errorf("the join has setup commands %v through %v", j.Setup, j.SetupRunner)
			}
		})
	}
}

// TestAFailedSetupCommandFailsTheRun is a setup command that exits 7 in the setup
// container: qory run prints Forager's line, the fix first, once, closes the run as
// failed with no session, and exits 1. No session joins.
func TestAFailedSetupCommandFailsTheRun(t *testing.T) {
	h := newHostFetch(t, "", "main")
	r := runSetUp(t, h, "", "acme/shop", &session.SetupError{Name: "prepare", Step: 3, ExitCode: 7})
	if r.err == nil || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if got := (keyRun{closes: r.closes, joined: len(r.joins) > 0}); !got.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", r.closes, got.joined)
	}
	// The run opened nowhere has no record, so no line says where it is.
	wants(t, r.out, "\nqory run: Fix prepare: setup step 3 exited with 7.\n")
	if strings.Count(r.out, "Fix prepare") != 1 {
		t.Errorf("the line is printed more than once:\n%s", r.out)
	}
}

// TestAnAgentEnvErrorFailsTheRunBeforeTheSetup is AgentEnv's error for a setup
// outside: qory run prints its line, the fix first, closes the run as failed with no
// session, and exits 1. Neither the setup nor a session runs.
func TestAnAgentEnvErrorFailsTheRunBeforeTheSetup(t *testing.T) {
	h := newHostFetch(t, "", "main")
	const line = "open a new run for the agent's environment: the run is closed"
	r := runSetUpWith(t, h, "", "acme/shop", nil, func() ([]string, error) { return nil, errors.New(line) })
	if r.err == nil || cmd.ExitCode(r.err) != 1 || r.err.Error() != line {
		t.Fatalf("%v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if len(r.setups) != 0 || slices.Contains(r.calls, "setup") {
		t.Errorf("the calls: %v", r.calls)
	}
	if got := (keyRun{closes: r.closes, joined: len(r.joins) > 0}); !got.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", r.closes, got.joined)
	}
}

// TestASetupVariableRefusalFailsTheRun is Setup's refusal of a variable of AgentEnv's
// result, from a fake AgentEnv: Setup's refusal reaches the user unchanged, as qory
// run's error, the fix first; the run closes as failed with no session, and no session
// joins.
func TestASetupVariableRefusalFailsTheRun(t *testing.T) {
	h := newHostFetch(t, "", "main")
	refused := &session.Refusal{Code: "variable_reserved", Names: []string{"QORY_EXTRA"}, Text: "Leave QORY_EXTRA out of the setup's variables: the QORY_ names are Forager's."}
	r := runSetUpWith(t, h, "", "acme/shop", refused, func() ([]string, error) {
		return []string{"QORY_EXTRA=x", "QORY_RUN_REPOSITORIES=acme/shop", "QORY_RUN_SYSTEM=" + proxygittest.Host}, nil
	})
	if r.err == nil || cmd.ExitCode(r.err) != 1 || !strings.HasPrefix(r.err.Error(), "Leave QORY_EXTRA out of the setup's variables: ") {
		t.Fatalf("%v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if len(r.setups) != 1 || !slices.Contains(r.setups[0].Env, "QORY_EXTRA=x") {
		t.Errorf("the setups: %+v", r.setups)
	}
	if got := (keyRun{closes: r.closes, joined: len(r.joins) > 0}); !got.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", r.closes, got.joined)
	}
}

// sameCommand reports whether a and b are the same setup command.
func sameCommand(a, b session.SetupCommand) bool {
	return a.Name == b.Name && slices.Equal(a.Args, b.Args)
}
