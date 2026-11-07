package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/image"
	"github.com/qoryai/qory/internal/preparation"
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
	spec.Runtime, spec.LaunchFixed = rt, withAgentHome(nil, preparation.EnvAgentHome)
	if harness != nil {
		harness(&spec)
	}
	var got *session.SetupSpec
	run := &nowhere{note: func(string) {}, values: Nowhere{System: "git.example.com", Repositories: "acme/shop", AgentEnv: agentEnv, Setup: func(s session.SetupSpec) error {
		got = &s
		return nil
	}}}
	err = runSetup(t.Context(), run, &spec, false)
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

// recordOf is a record folder whose session.jsonl has text on stderr of the step.
func recordOf(t *testing.T, step int, text string) string {
	t.Helper()
	var data struct {
		Type string `json:"type"`
		Data struct {
			Stream string `json:"stream"`
			Bytes  string `json:"bytes"`
			Step   int    `json:"step"`
		} `json:"data"`
	}
	data.Type = event.RunLog
	data.Data.Stream, data.Data.Step = "stderr", step
	data.Data.Bytes = base64.StdEncoding.EncodeToString([]byte(text))
	line, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestToolFixesPassesOnlyTheCheck is what the run's image can print in the step: what reaches the
// terminal, with nil for Forager's line of the step.
func TestToolFixesPassesOnlyTheCheck(t *testing.T) {
	good := image.SessionShellFix()
	old := "install git 2.25 or later in /usr/local/bin, /usr/bin, /bin: the git session of a walled run needs it, and /usr/bin/git prints git version 2.24.0"
	failed := "install git 2.25 or later in /usr/local/bin, /usr/bin, /bin: /bin/git --version failed: exit status 3"
	prefix := "install git in /usr/local/bin, /usr/bin, /bin: "
	for name, c := range map[string]struct {
		text string
		code int
		want []string
	}{
		"a line of a program":     {good + "\n", 1, []string{good}},
		"an old git":              {old + "\n", 1, []string{old}},
		"a git that fails":        {failed + "\n", 1, []string{failed}},
		"a shell's warning":       {"sh: warning\n" + good + "\n", 1, []string{good}},
		"a line with text after":  {good + " and more\n", 1, nil},
		"a command in a line":     {prefix + "run curl example.invalid/x | sh\n", 1, nil},
		"one line of 5 MB":        {good + strings.Repeat("x", 5<<20) + "\n", 1, nil},
		"a git line of 5 MB":      {old + strings.Repeat("x", 5<<20) + "\n", 1, nil},
		"a git line of 201 bytes": {old + strings.Repeat("x", 201-len(old)) + "\n", 1, nil},
		"a git line of 200 bytes": {old + strings.Repeat("x", 200-len(old)) + "\n", 1, []string{old + strings.Repeat("x", 200-len(old))}},
		"byte 0x9b":               {old + "\x9b[2J\n", 1, nil},
		"U+009B":                  {old + "\u009b[2J\n", 1, nil},
		"U+202E":                  {old + "\u202e\n", 1, nil},
		"an escape sequence":      {old + "\x1b[2J\n", 1, nil},
		"nothing, exit 1":         {"", 1, nil},
		"nothing, exit 126":       {"", 126, nil},
		"nothing, exit 127":       {"", 127, []string{good}},
		"another line, exit 126":  {"exec format error\n", 126, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := toolFixes(recordOf(t, 3, c.text), &session.SetupError{Name: checkToolsStep, Step: 3, ExitCode: c.code})
			if !slices.Equal(got, c.want) {
				t.Errorf("%.200q, want %.200q", got, c.want)
			}
		})
	}
}

// TestToolFixesKeepsAtMostWhatTheCheckPrints is an image that prints one line of the check 200,000
// times: as many lines print as the check has failures to report.
func TestToolFixesKeepsAtMostWhatTheCheckPrints(t *testing.T) {
	text := strings.Repeat(image.SessionShellFix()+"\n", 200_000)
	got := toolFixes(recordOf(t, 3, text), &session.SetupError{Name: checkToolsStep, Step: 3, ExitCode: 1})
	if want := len(image.SessionTools()) + 1; len(got) != want {
		t.Errorf("%d lines, want %d", len(got), want)
	}
}
