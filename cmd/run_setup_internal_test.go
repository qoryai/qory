package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"

	"github.com/qoryai/qory/internal/config"
)

// TestRunStartedHasSetupForARunFromAControlPlane holds enclose to setting
// OpenSpec.Setup, run.started's setup, to wall.setup, outside when absent, for a walled
// run from a control plane, whose run configuration may list repositories, and to
// nothing for a run without one, which has no setup commands.
func TestRunStartedHasSetupForARunFromAControlPlane(t *testing.T) {
	for _, c := range []struct {
		setup      string
		fromServer bool
		want       string
	}{
		{"", true, event.SetupOutside},
		{config.SetupOutside, true, event.SetupOutside},
		{config.SetupInside, true, event.SetupInside},
		{"", false, ""},
		{config.SetupInside, false, ""},
	} {
		var spec runSpec
		r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux", Setup: c.setup}}
		if err := enclose(&spec, r, wallOptions{}, "", c.fromServer, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if spec.OpenSpec.Setup != c.want {
			t.Errorf("wall.setup %q, from a server %v: run.started's setup is %q, want %q", c.setup, c.fromServer, spec.OpenSpec.Setup, c.want)
		}
	}
}

// setupSpecOf is a walled run of Claude Code with wall.env and --env set to the names
// given and wall.setup to where, set up through runSetup with the server's system and
// repositories, the harness's values from harness, nil for none, and agentEnv as the
// run's AgentEnv, nil for the fake's: the spec the run's Setup got, nil when it was not
// called, the run's spec after it, and runSetup's error.
func setupSpecOf(t *testing.T, where string, wallEnv, flagEnv []string, harness func(*runSpec), agentEnv func(session.SessionSpec) ([]string, error)) (*session.SetupSpec, runSpec, error) {
	t.Helper()
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var spec runSpec
	r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux", Env: wallEnv, Setup: where}}
	if err := enclose(&spec, r, wallOptions{env: flagEnv}, "", true, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	spec.Runtime, spec.LaunchFixed = rt, withAgentHome(nil)
	if harness != nil {
		harness(&spec)
	}
	var got *session.SetupSpec
	run := &nowhere{note: func(string) {}, values: Nowhere{System: "git.example.com", Repositories: "acme/shop", AgentEnv: agentEnv, Setup: func(s session.SetupSpec) error {
		got = &s
		return nil
	}}}
	err = runSetup(t.Context(), run, &spec)
	return got, spec, err
}

// TestASetupOutsideGetsTheAgentsEnvironment is a setup outside with wall.env and --env
// set: AgentEnv gets the session's spec, with wall.env's values as the machine's
// variables and --env's as the run's, and the setup container's variables are what
// AgentEnv returns, as it returns them.
func TestASetupOutsideGetsTheAgentsEnvironment(t *testing.T) {
	t.Setenv("SETUP_MACHINE", "m")
	t.Setenv("SETUP_RUN", "r")
	agent := []string{"QORY_RUN_REPOSITORIES=acme/shop", "QORY_RUN_SYSTEM=git.example.com", "SERVER_VALUE=s", "SETUP_MACHINE=m", "SETUP_RUN=r"}
	var asked []session.SessionSpec
	got, spec, err := setupSpecOf(t, "", []string{"SETUP_MACHINE"}, []string{"SETUP_RUN"}, nil, func(s session.SessionSpec) ([]string, error) {
		asked = append(asked, s)
		return slices.Clone(agent), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(asked) != 1 {
		t.Fatalf("the run's Setup got %v, AgentEnv was called %d times", got, len(asked))
	}
	if !slices.Equal(got.Env, agent) {
		t.Errorf("the setup's variables: %q, want %q", got.Env, agent)
	}
	a := asked[0]
	if !slices.Equal(a.Variables.Machine, []string{"SETUP_MACHINE=m"}) || !slices.Equal(a.Variables.Run, []string{"SETUP_RUN=r"}) || a.Runtime != spec.Runtime || !slices.Equal(a.LaunchFixed, spec.LaunchFixed) {
		t.Errorf("AgentEnv got machine %q, run %q, fixed %q", a.Variables.Machine, a.Variables.Run, a.LaunchFixed)
	}
}

// TestASetupOutsideStopsOnAgentEnvsError is AgentEnv's error for a setup outside: the
// run's Setup is not called. A placeholder_conflict or variable_reserved whose every
// name wall.env or --env passes, and neither LaunchFixed nor LaunchDefaults sets, is
// the line that starts with its fix, then Forager's text, and unwraps to the refusal.
// A refusal of a name that wall.env and --env do not pass, or that the harness's
// values set too, and any other error, is AgentEnv's error as it is.
func TestASetupOutsideStopsOnAgentEnvsError(t *testing.T) {
	t.Setenv("TOOL_TOKEN", "t")
	t.Setenv("OTHER_TOKEN", "o")
	t.Setenv("QORY_EXTRA", "q")
	conflict := func(names ...string) error {
		return refusal.New(refusal.PlaceholderConflict, names, "the run passes a value for %s, a placeholder of what the gateway holds outside the enclosure", strings.Join(names, ", "))
	}
	reserved := func(names ...string) error {
		return refusal.New(refusal.VariableReserved, names, "the run passes %s into the enclosure, Forager's names or what a value of the machine's is read from", strings.Join(names, ", "))
	}
	both := conflict("OTHER_TOKEN", "TOOL_TOKEN")
	qory := reserved("QORY_EXTRA")
	inDefaults := conflict("TOOL_TOKEN")
	inFixed := conflict("TOOL_TOKEN")
	oneHarness := conflict("HARNESS_TOKEN", "TOOL_TOKEN")
	harnessOnly := reserved("HARNESS_ONLY")
	closed := errors.New("open a new run for the agent's environment: the run is closed")
	for _, c := range []struct {
		name    string
		err     error
		harness func(*runSpec)
		want    string
	}{
		{"both names passed", both, nil, "leave OTHER_TOKEN, TOOL_TOKEN out of wall.env and --env, then run again: " + both.Error()},
		{"a QORY_ name of wall.env", qory, nil, "leave QORY_EXTRA out of wall.env and --env, then run again: " + qory.Error()},
		{"a harness default too", inDefaults, func(s *runSpec) { s.LaunchDefaults = []string{"TOOL_TOKEN=h"} }, inDefaults.Error()},
		{"a fixed harness value too", inFixed, func(s *runSpec) { s.LaunchFixed = append(s.LaunchFixed, "TOOL_TOKEN=h") }, inFixed.Error()},
		{"one name of the harness alone", oneHarness, func(s *runSpec) { s.LaunchDefaults = []string{"HARNESS_TOKEN=h"} }, oneHarness.Error()},
		{"a name passed by neither", harnessOnly, nil, harnessOnly.Error()},
		{"another error", closed, nil, closed.Error()},
	} {
		got, _, err := setupSpecOf(t, "", []string{"TOOL_TOKEN", "QORY_EXTRA"}, []string{"OTHER_TOKEN"}, c.harness, func(session.SessionSpec) ([]string, error) { return nil, c.err })
		if got != nil {
			t.Errorf("%s: the run's Setup got %+v", c.name, *got)
		}
		if err == nil || err.Error() != c.want || !errors.Is(err, c.err) {
			t.Errorf("%s: the error: %v\nwant %q", c.name, err, c.want)
		}
	}
}

// TestASetupInsideKeepsTheAgentsVariables is a setup inside: the run's Setup is not
// called, and the session's variables, wall.env and --env, are as enclose made them.
func TestASetupInsideKeepsTheAgentsVariables(t *testing.T) {
	t.Setenv("SETUP_MACHINE", "m")
	t.Setenv("SETUP_RUN", "r")
	got, spec, err := setupSpecOf(t, config.SetupInside, []string{"SETUP_MACHINE"}, []string{"SETUP_RUN"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("the run's Setup got %+v", *got)
	}
	if !slices.Equal(spec.Variables.Machine, []string{"SETUP_MACHINE=m"}) || !slices.Equal(spec.Variables.Run, []string{"SETUP_RUN=r"}) || len(spec.Env) != 0 {
		t.Errorf("the session's variables: machine %q, run %q, env %q", spec.Variables.Machine, spec.Variables.Run, spec.Env)
	}
	if len(spec.SessionSpec.Setup) != 1 {
		t.Errorf("the session's setup commands: %v", spec.SessionSpec.Setup)
	}
}
