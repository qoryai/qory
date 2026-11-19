package cmd

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/preparation"
)

// TestTheSetupGetsNoRouteToQory is a run with a route that Forager opens at a gateway in
// this process, whose session's environment sets a QORY_HOST_URL: the
// variables qory gives the run's Setup, which Forager's AgentEnv resolves, have no
// QORY_HOST_URL. Forager's Setup adds no QORY_ name to them, and refuses one.
func TestTheSetupGetsNoRouteToQory(t *testing.T) {
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	route := newQoryRoute()
	at, err := openAtGateway(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}, QoryRoute: route}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		at.run.Close(ctx, session.Outcome{State: event.StateSucceeded})
		at.g.Close(ctx)
	})
	spec := runSpec{SessionSpec: session.SessionSpec{
		Runtime: rt, Dir: t.TempDir(), Command: "sh",
		Env: []string{"PATH=/usr/bin:/bin", session.EnvQoryHost + "=http://elsewhere.example", "KEPT=1"},
	}}
	env, err := setupEnv(foragerRun{at.run}, &spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "KEPT=1") {
		t.Fatalf("the setup's variables lack the session's KEPT")
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, session.EnvQoryHost+"=") {
			t.Errorf("the setup's variables have %s", session.EnvQoryHost)
		}
	}
}

// TestTheAgentEnvKeepsTheRoutesVariable is a dispatched walled run of Claude Code started
// through the agent-env verb: with a route to qory, the kept names have QORY_HOST_URL,
// which Forager sets in the launch, so the verb keeps it; with none, they do not.
func TestTheAgentEnvKeepsTheRoutesVariable(t *testing.T) {
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, withRoute := range []bool{true, false} {
		spec := runSpec{OpenSpec: session.OpenSpec{Wall: &wall.Docker{}}, SessionSpec: session.SessionSpec{Runtime: rt, Command: "sh", HarnessHome: "/h", LaunchFixed: withAgentHome(nil, preparation.EnvAgentHome), Mounts: []wall.Mount{{Path: "/work"}}}}
		if withRoute {
			spec.QoryRoute = newQoryRoute()
		}
		done, err := wrapAgentEnv(&spec, false, transcriptLink{}, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(spec.Args[2])
		done()
		if err != nil {
			t.Fatal(err)
		}
		if kept := strings.Contains("\n"+string(data), "\n"+session.EnvQoryHost+"\n"); kept != withRoute {
			t.Errorf("with a route %v, the kept names have %s: %v", withRoute, session.EnvQoryHost, kept)
		}
	}
}
