package cmd

import (
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// nextAnswerFixture is the next-session answer of the fixture name of contracts/run/v1.
func nextAnswerFixture(t *testing.T, name string) contracts.NextSessionAnswer {
	t.Helper()
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/next-session-answer/"+name)
	if err != nil {
		t.Fatal(err)
	}
	a, err := provisioner.ReadNextSessionAnswer(b)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// sharedCodeRun is the codeRun of documents that set shared_run, with a harness document
// that defines a stack of the module tools and the workflows example and
// write-acceptance-checks, on claude with the model example-small.
func sharedCodeRun(t *testing.T) *codeRun {
	t.Helper()
	h := &contracts.HarnessDocument{Harness: contracts.HarnessSection{
		Modules: []contracts.HarnessModule{{Name: "tools", Source: contracts.HarnessSource{Git: "https://code.example.com/team/tools.git", Ref: "main"}}},
		Workflows: map[string]contracts.HarnessWorkflow{
			"example":                 {Access: "write"},
			"write-acceptance-checks": {Access: "write", Plan: "build-plan", Tier: "thinking"},
		},
	}}
	r, err := codeRunOf(contracts.Documents{Inputs: json.RawMessage(`{"version":1}`), Harness: h, Workflow: "example", SharedRun: true, TimeLimitMinutes: 120, RunSettings: &contracts.RunSettings{Runtime: "claude", Model: "example-small"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestALaterSessionTakesItsAnswersRuntimeWorkflowAndKey is a run of claude whose
// next-session answer starts write-acceptance-checks on codex with the model
// example-large, its tiers, the key example-provider-1, 60 minutes and no full suite: the
// run's runtime, compose flags, harness, workflow, inputs, key, limit and suite are the
// answer's, and the harness's target is codex, with each workflow's plan and tier. The
// compose flags of an answer with no model choice keep the documents' runtime and set no
// model.
func TestALaterSessionTakesItsAnswersRuntimeWorkflowAndKey(t *testing.T) {
	r := sharedCodeRun(t)
	if !r.shared || r.modelCredential != "" {
		t.Fatalf("session 1: shared %v, key %q", r.shared, r.modelCredential)
	}
	if err := r.next(nextAnswerFixture(t, "session-runtime-with-model.json")); err != nil {
		t.Fatal(err)
	}
	if r.runtime != "codex" || r.workflow != "write-acceptance-checks" || r.modelCredential != "example-provider-1" || r.minutes != 60 || r.fullSuite {
		t.Errorf("session 2: runtime %q, workflow %q, key %q, %d minutes, full suite %v", r.runtime, r.workflow, r.modelCredential, r.minutes, r.fullSuite)
	}
	var inputs struct{ Run struct{ Attempt int } }
	if err := json.Unmarshal(r.inputs, &inputs); err != nil || inputs.Run.Attempt != 1 {
		t.Errorf("session 2's inputs are %s", r.inputs)
	}
	var doc struct {
		Harness struct {
			Target    struct{ Runtime string }             `json:"target"`
			Workflows map[string]contracts.HarnessWorkflow `json:"workflows"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(r.harness, &doc); err != nil {
		t.Fatal(err)
	}
	if w := doc.Harness.Workflows["write-acceptance-checks"]; doc.Harness.Target.Runtime != "codex" || w.Plan != "build-plan" || w.Tier != "thinking" {
		t.Errorf("session 2's harness.yaml has:\n%s", r.harness)
	}
	var o composeOptions
	o.tiers, o.effort = []string{"old=example-old"}, "low"
	r.applyTo(&o)
	if o.model != "example-large" || o.effort != "high" || !slices.Equal(o.tiers, []string{"light=example-small", "thinking=example-large"}) || !slices.Equal(o.tierEfforts, []string{"thinking=high"}) {
		t.Errorf("session 2's compose flags: model %q, effort %q, tiers %q, tier efforts %q", o.model, o.effort, o.tiers, o.tierEfforts)
	}

	if err := r.next(nextAnswerFixture(t, "session-without-provider.json")); err != nil {
		t.Fatal(err)
	}
	if r.runtime != "claude" || r.modelCredential != "" || !r.fullSuite || r.minutes != 15 {
		t.Errorf("session 3: runtime %q, key %q, full suite %v, %d minutes", r.runtime, r.modelCredential, r.fullSuite, r.minutes)
	}
	r.applyTo(&o)
	if o.model != "" || o.effort != "" || o.tiers != nil || o.tierEfforts != nil {
		t.Errorf("session 3's compose flags: model %q, effort %q, tiers %q, tier efforts %q", o.model, o.effort, o.tiers, o.tierEfforts)
	}
}

// TestALaterSessionOnARuntimeQoryDoesNotRunStops is a next-session answer of the runtime
// example-runtime: the run's session stays as it was and the error contains the runtime.
func TestALaterSessionOnARuntimeQoryDoesNotRunStops(t *testing.T) {
	r := sharedCodeRun(t)
	a := nextAnswerFixture(t, "session-runtime-only.json")
	a.ModelChoice.Runtime = "example-runtime"
	err := r.next(a)
	if err == nil || !strings.Contains(err.Error(), "example-runtime") {
		t.Fatalf("next: %v", err)
	}
	if r.runtime != "claude" || r.workflow != "example" {
		t.Errorf("after the error the run has %q and %q", r.runtime, r.workflow)
	}
}

// TestAPushBetweenSessionsNeedsProofThatTheAgentStopped is the proof before a push of a
// run of several sessions: a walled run with no container left passes; a run with no
// wall and a run with a container kept get the line that starts with their fix.
func TestAPushBetweenSessionsNeedsProofThatTheAgentStopped(t *testing.T) {
	if err := agentStopProof(true, false, "qory pushed nothing"); err != nil {
		t.Errorf("walled, nothing kept: %v", err)
	}
	for _, c := range []struct {
		walled, kept bool
		want         string
	}{
		{false, false, "Run the stage behind a wall.\n  Without one, "},
		{false, true, "Run the stage behind a wall.\n  Without one, "},
		{true, true, "Take the work item in again in Qory Apiary.\n  A container of the session may still run"},
	} {
		err := agentStopProof(c.walled, c.kept, "qory pushed nothing")
		var line *runLineError
		if !errors.As(err, &line) || !strings.HasPrefix(err.Error(), c.want) || !strings.HasSuffix(err.Error(), ", so qory pushed nothing"+codeUsed) {
			t.Errorf("walled %v, kept %v: %v, want a line that starts %q", c.walled, c.kept, err, c.want)
		}
	}
}
