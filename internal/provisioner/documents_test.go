package provisioner_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// fixtureValues are values of the documents fixtures that no error may contain.
var fixtureValues = []string{"TRK-1", "acme", "hello", "qrk_", "qecr_", "marketing", "example-large", "registry.example.com", "code.example.com"}

// documentsOf is the run/v1 documents fixture name, with change applied to its members
// first.
func documentsOf(t *testing.T, name string, change func(map[string]any)) json.RawMessage {
	t.Helper()
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/documents/"+name)
	if err != nil {
		t.Fatal(err)
	}
	if change == nil {
		return b
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	change(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// noValue fails the test when err contains a value of the fixtures.
func noValue(t *testing.T, name string, err error) {
	t.Helper()
	for _, v := range fixtureValues {
		if strings.Contains(err.Error(), v) {
			t.Errorf("%s: the error %q contains %s", name, err, v)
		}
	}
}

// TestEveryDocumentsFixtureIsRead reads each of the contract's documents fixtures: each
// is read with its step, its domain, its stack as written, its run settings as sent,
// and its inputs as the documents' bytes.
func TestEveryDocumentsFixtureIsRead(t *testing.T) {
	names, err := fs.Glob(contracts.RunFS, "fixtures/documents/*.json")
	if err != nil || len(names) < 18 {
		t.Fatalf("the fixtures %v: %v", names, err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			raw, err := fs.ReadFile(contracts.RunFS, name)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := provisioner.ReadDocuments(raw)
			if err != nil {
				t.Fatal(err)
			}
			var want contracts.Documents
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			var settings struct {
				RunSettings json.RawMessage `json:"run_settings"`
			}
			if err := json.Unmarshal(raw, &settings); err != nil {
				t.Fatal(err)
			}
			if doc.Step != want.Step || doc.Domain != want.Domain || doc.Stack != want.Stack || doc.Workflow != want.Workflow ||
				(doc.Target == nil) != (doc.Access == "") || doc.ImageDestination != want.ImageDestination {
				t.Errorf("read %+v", doc)
			}
			if !bytes.Contains(raw, doc.Inputs) {
				t.Errorf("the inputs are not the documents' bytes: %s", doc.Inputs)
			}
			if settings.RunSettings != nil {
				got, _ := json.Marshal(doc.RunSettings)
				if !sameJSON(t, got, settings.RunSettings) {
					t.Errorf("the run settings are %s, want %s", got, settings.RunSettings)
				}
			}
		})
	}
	doc, err := provisioner.ReadDocuments(documentsOf(t, "explore-time-limit.json", nil))
	if err != nil || doc.RunSettings == nil || doc.RunSettings.TimeLimitMinutes != 90 {
		t.Errorf("the time limit: %+v %v", doc.RunSettings, err)
	}
	spaced := append([]byte(" \n"), documentsOf(t, "explore-stack.json", nil)...)
	if _, err := provisioner.ReadDocuments(spaced); err != nil {
		t.Errorf("documents after white space: %v", err)
	}
}

// TestEveryInvalidDocumentsFixtureFails reads each of the contract's invalid documents
// fixtures: each is refused, and no error contains a value of the documents.
func TestEveryInvalidDocumentsFixtureFails(t *testing.T) {
	names, err := fs.Glob(contracts.RunFS, "fixtures/invalid/documents-*.json")
	if err != nil || len(names) < 60 {
		t.Fatalf("the fixtures %v: %v", names, err)
	}
	for _, name := range names {
		raw, err := fs.ReadFile(contracts.RunFS, name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = provisioner.ReadDocuments(raw)
		if err == nil {
			t.Errorf("%s: read", name)
			continue
		}
		noValue(t, name, err)
	}
}

// TestBrokenDocumentsAreRefused refuses documents that break the schema, a rule it
// states, or the explore inputs' rules: each error contains no value of the documents.
func TestBrokenDocumentsAreRefused(t *testing.T) {
	const broken = "it breaks documents.schema.json"
	inputs := func(doc map[string]any) map[string]any { return doc["inputs"].(map[string]any) }
	settings := func(doc map[string]any) map[string]any { return doc["run_settings"].(map[string]any) }
	withSettings := func(edit func(d map[string]any)) json.RawMessage {
		return documentsOf(t, "explore-run-settings.json", edit)
	}
	for _, c := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"not JSON", json.RawMessage(`{"step":`), ""},
		{"a member twice", json.RawMessage(`{"step":"explore","step":"explore"}`), ""},
		{"an array", json.RawMessage(`[]`), "it is not a JSON object"},
		{"a string", json.RawMessage(`"x"`), ""},
		{"null", json.RawMessage(`null`), ""},
		{"empty", json.RawMessage(``), ""},
		{"no member", json.RawMessage(`{}`), broken},
		{"a run key", documentsOf(t, "explore-stack.json", func(d map[string]any) { d["run_key"] = "qrk_a.b" }), "it contains a run_key member"},
		{"a run code", documentsOf(t, "explore-stack.json", func(d map[string]any) { d["run_code"] = "qecr_x" }), "it contains a run_code member"},
		{"a member the schema does not define", documentsOf(t, "explore-stack.json", func(d map[string]any) { d["prompt"] = "x" }), broken},
		{"a run id", documentsOf(t, "explore-stack.json", func(d map[string]any) { d["run_id"] = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d" }), broken},
		{"a stack without a workflow", documentsOf(t, "explore-stack.json", func(d map[string]any) { delete(d, "workflow") }), broken},
		{"another domain in inputs", documentsOf(t, "explore-stack.json", func(d map[string]any) { inputs(d)["domain"] = "marketing" }), "its inputs.domain is not its domain"},
		{"another work item in inputs", documentsOf(t, "explore-stack.json", func(d map[string]any) {
			inputs(d)["work_item"].(map[string]any)["id"] = "wi_0000000000"
		}), "its inputs.work_item.id is not its work item's id"},
		{"no work item in inputs", documentsOf(t, "explore-stack.json", func(d map[string]any) { delete(inputs(d), "work_item") }), "its inputs.work_item.id is not its work item's id"},
		{"inputs too large", documentsOf(t, "explore-without-target.json", func(d map[string]any) {
			inputs(d)["x"] = strings.Repeat("a", contracts.MaxInputs)
		}), "its inputs are larger than 786432 bytes"},
		{"target null", documentsOf(t, "explore-without-target.json", func(d map[string]any) { d["target"] = nil }), broken},
		{"access empty", documentsOf(t, "explore-without-target.json", func(d map[string]any) { d["access"] = "" }), broken},
		{"access without target", documentsOf(t, "explore-without-target.json", func(d map[string]any) { d["access"] = "read" }), broken},
		{"domain empty", documentsOf(t, "explore-without-target.json", func(d map[string]any) { d["domain"] = "" }), broken},
		{"a prebuild with run settings", documentsOf(t, "prebuild.json", func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "claude"} }), broken},
		{"run settings null", withSettings(func(d map[string]any) { d["run_settings"] = nil }), broken},
		{"a model with a trailing line feed", withSettings(func(d map[string]any) {
			s := settings(d)
			s["model"] = "example-large\n"
			s["provider_models"].([]any)[0].(map[string]any)["id"] = "example-large\n"
			s["tiers"] = map[string]any{"light": "example-small"}
		}), broken},
		{"a runtime with a trailing line feed", withSettings(func(d map[string]any) { settings(d)["runtime"] = "claude\n" }), broken},
		{"a key in the run settings", withSettings(func(d map[string]any) { settings(d)["api_key"] = "example-key" }), broken},
		{"a time limit of 14 minutes", withSettings(func(d map[string]any) { settings(d)["time_limit_minutes"] = 14 }), broken},
		{"a time limit with a fraction", withSettings(func(d map[string]any) { settings(d)["time_limit_minutes"] = 45.5 }), broken},
		{"a model not listed", withSettings(func(d map[string]any) { settings(d)["model"] = "example-tiny" }), broken},
		{"an effort not listed for the model", withSettings(func(d map[string]any) { settings(d)["effort"] = "huge" }), broken},
		{"an effort for a model with none", withSettings(func(d map[string]any) {
			s := settings(d)
			s["model"] = "example-small"
			s["tiers"] = map[string]any{"light": "example-small"}
		}), broken},
		{"a tier's model not listed", withSettings(func(d map[string]any) { settings(d)["tiers"].(map[string]any)["fast"] = "example-tiny" }), broken},
		{"a tier's effort not listed", withSettings(func(d map[string]any) {
			settings(d)["tiers"].(map[string]any)["thinking"] = map[string]any{"model": "example-large", "effort": "huge"}
		}), broken},
		{"a model listed twice", withSettings(func(d map[string]any) {
			s := settings(d)
			s["provider_models"] = append(s["provider_models"].([]any), map[string]any{"id": "example-small"})
		}), broken},
	} {
		_, err := provisioner.ReadDocuments(c.raw)
		if err == nil {
			t.Errorf("%s: read", c.name)
			continue
		}
		if c.want != "" && err.Error() != c.want {
			t.Errorf("%s: %q, want %q", c.name, err, c.want)
		}
		noValue(t, c.name, err)
	}
}

// TestTimeLimit takes the run settings' minutes first, then the workflow's time limit,
// then 3 hours.
func TestTimeLimit(t *testing.T) {
	for _, c := range []struct {
		minutes  int
		workflow time.Duration
		want     time.Duration
	}{
		{0, 0, 3 * time.Hour},
		{45, 0, 45 * time.Minute},
		{0, 7 * time.Hour, 7 * time.Hour},
		{45, 7 * time.Hour, 45 * time.Minute},
		{-1, 0, 3 * time.Hour},
	} {
		if got := provisioner.TimeLimit(c.minutes, c.workflow); got != c.want {
			t.Errorf("TimeLimit(%d, %v) = %v, want %v", c.minutes, c.workflow, got, c.want)
		}
	}
	if provisioner.FallbackTimeLimit != 3*time.Hour {
		t.Errorf("the fallback is %v", provisioner.FallbackTimeLimit)
	}
}
