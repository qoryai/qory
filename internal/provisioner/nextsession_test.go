package provisioner_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// answerOf is the run/v1 next-session-answer fixture name, with change applied to its
// members first when change is not nil.
func answerOf(t *testing.T, name string, change func(map[string]any)) []byte {
	t.Helper()
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/next-session-answer/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if change == nil {
		return b
	}
	var a map[string]any
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	change(a)
	if b, err = json.Marshal(a); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEveryNextSessionAnswerFixtureIsRead reads each of the contract's next-session
// answer fixtures: each passes, a session with its workflow and inputs, an end with
// nothing else.
func TestEveryNextSessionAnswerFixtureIsRead(t *testing.T) {
	names, err := fs.Glob(contracts.RunFS, "fixtures/next-session-answer/*.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, name := range names {
		a, err := provisioner.ReadNextSessionAnswer(answerOf(t, path.Base(name), nil))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		switch a.Next {
		case contracts.NextSessionKindEnd:
			if a.Workflow != "" || a.Inputs != nil {
				t.Errorf("%s: an end has a session's members: %+v", name, a)
			}
		case contracts.NextSessionKindSession:
			if a.Workflow == "" || len(a.Inputs) == 0 || a.Session < 1 {
				t.Errorf("%s: a session lacks members: %+v", name, a)
			}
		default:
			t.Errorf("%s: next is %q", name, a.Next)
		}
	}
}

// TestANextSessionAnswerOutsideTheSchemaIsRefused is an answer with an unknown member,
// one with no workflow, one with both shapes, one that is not JSON, and one whose model
// choice lists an effort its model does not have: each is refused, and no error contains
// a value of the answer.
func TestANextSessionAnswerOutsideTheSchemaIsRefused(t *testing.T) {
	for name, body := range map[string][]byte{
		"unknown member": answerOf(t, "session.json", func(a map[string]any) { a["branch"] = "main" }),
		"no workflow":    answerOf(t, "session.json", func(a map[string]any) { delete(a, "workflow") }),
		"both shapes":    answerOf(t, "end.json", func(a map[string]any) { a["workflow"] = "hello" }),
		"not JSON":       []byte(`{"next":`),
		"effort not listed": answerOf(t, "session.json", func(a map[string]any) {
			a["model_choice"].(map[string]any)["effort"] = "max"
		}),
	} {
		_, err := provisioner.ReadNextSessionAnswer(body)
		if err == nil {
			t.Errorf("%s: read, want a refusal", name)
			continue
		}
		for _, v := range []string{"write-acceptance-checks", "example-large", "example-provider-1", "main", "hello"} {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: the error %q contains %q", name, err, v)
			}
		}
	}
}

// TestTheNextSessionBodyPassesItsSchema is the body of the request after a session that
// succeeded with no reason, and after one cancelled at its time limit: each passes the
// schema, reason null for none.
func TestTheNextSessionBodyPassesItsSchema(t *testing.T) {
	body, err := provisioner.NextSessionBody(1, "succeeded", "")
	if err != nil || string(body) != `{"session":1,"state":"succeeded","reason":null}` {
		t.Errorf("NextSessionBody = %s, %v", body, err)
	}
	body, err = provisioner.NextSessionBody(2, "cancelled", "timeout")
	if err != nil || string(body) != `{"session":2,"state":"cancelled","reason":"timeout"}` {
		t.Errorf("NextSessionBody = %s, %v", body, err)
	}
	if _, err := provisioner.NextSessionBody(0, "succeeded", ""); err == nil {
		t.Error("a request for session 0 passed")
	}
}

// TestALaterSessionsSettingsComeFromItsModelChoice is SettingsOf: a choice with a model,
// an effort and tiers gives their flags and its runtime; a choice with only a runtime
// gives that runtime and no flag; no choice gives the runtime of the run settings and no
// flag, so session 1's model and tiers do not carry over.
func TestALaterSessionsSettingsComeFromItsModelChoice(t *testing.T) {
	rs := &contracts.RunSettings{Runtime: "claude", Model: "example-large", Tiers: &contracts.Tiers{Light: &contracts.Tier{Model: "example-small"}}}
	runtime, flags := provisioner.SettingsOf(&contracts.ModelChoice{Runtime: "codex", Model: "example-medium", Effort: "high", Tiers: &contracts.Tiers{Thinking: &contracts.Tier{Model: "example-medium", Effort: "high"}}}, rs)
	want := []provisioner.Setting{{"model", "example-medium"}, {"effort", "high"}, {"tier", "thinking=example-medium"}, {"tier-effort", "thinking=high"}}
	if runtime != "codex" || !slices.Equal(flags, want) {
		t.Errorf("a full choice: %s %v, want codex %v", runtime, flags, want)
	}
	if runtime, flags = provisioner.SettingsOf(&contracts.ModelChoice{Runtime: "codex"}, rs); runtime != "codex" || flags != nil {
		t.Errorf("a runtime alone: %s %v, want codex and no flag", runtime, flags)
	}
	if runtime, flags = provisioner.SettingsOf(&contracts.ModelChoice{Model: "example-medium"}, rs); runtime != "claude" || len(flags) != 1 {
		t.Errorf("a choice with no runtime: %s %v, want claude and one flag", runtime, flags)
	}
	if runtime, flags = provisioner.SettingsOf(nil, rs); runtime != "claude" || flags != nil {
		t.Errorf("no choice: %s %v, want claude and no flag", runtime, flags)
	}
}
