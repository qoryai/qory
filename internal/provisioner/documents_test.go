package provisioner_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"

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
// is read with its step, its domain, its workflow and harness, its subject and trigger
// as written, its run settings as sent, and its inputs as the documents' bytes. A
// subject of a kind other than work_item, whose inputs have no work item, is read.
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
			if doc.Step != want.Step || doc.Domain != want.Domain || doc.Workflow != want.Workflow || !reflect.DeepEqual(doc.Harness, want.Harness) ||
				!reflect.DeepEqual(doc.Subject, want.Subject) || doc.Trigger != want.Trigger ||
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
	if err != nil || doc.TimeLimitMinutes != 90 {
		t.Errorf("the time limit: %d %v", doc.TimeLimitMinutes, err)
	}
	spaced := append([]byte(" \n"), documentsOf(t, "explore-workflow.json", nil)...)
	if _, err := provisioner.ReadDocuments(spaced); err != nil {
		t.Errorf("documents after white space: %v", err)
	}
}

// TestDocumentsWithSharedRunAndFullSuiteAreRead reads explore documents that set
// time_limit_minutes, shared_run and type_settings.full_suite: each member is read as set.
func TestDocumentsWithSharedRunAndFullSuiteAreRead(t *testing.T) {
	raw := documentsOf(t, "explore-run-settings.json", func(d map[string]any) {
		d["time_limit_minutes"], d["shared_run"], d["type_settings"] = 45, true, map[string]any{"full_suite": true}
	})
	doc, err := provisioner.ReadDocuments(raw)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		FullSuite bool `json:"full_suite"`
	}
	if doc.TimeLimitMinutes != 45 || !doc.SharedRun || json.Unmarshal(doc.TypeSettings, &settings) != nil || !settings.FullSuite {
		t.Errorf("read time_limit_minutes %d, shared_run %v, type_settings %s", doc.TimeLimitMinutes, doc.SharedRun, doc.TypeSettings)
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
// states, or the rules of the inputs of a work item's run, also when the inputs repeat
// domain, work_item or id in capitals with the matching value, and documents whose
// must_understand lists a member the schema does not define: each error contains no
// value of the documents.
func TestBrokenDocumentsAreRefused(t *testing.T) {
	const broken = "it breaks documents.schema.json"
	inputs := func(doc map[string]any) map[string]any { return doc["inputs"].(map[string]any) }
	settings := func(doc map[string]any) map[string]any { return doc["run_settings"].(map[string]any) }
	withSettings := func(edit func(d map[string]any)) json.RawMessage {
		return documentsOf(t, "explore-run-settings.json", edit)
	}
	// alsoAfter is raw with the member also after its one copy of member: the member in
	// capitals, which a decode that ignores letter case reads last.
	alsoAfter := func(raw json.RawMessage, member, also string) json.RawMessage {
		if bytes.Count(raw, []byte(member)) != 1 {
			t.Fatalf("%s is not once in %s", member, raw)
		}
		return bytes.Replace(raw, []byte(member), []byte(member+","+also), 1)
	}
	for _, c := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"not JSON", json.RawMessage(`{"step":`), ""},
		{"a member twice", json.RawMessage(`{"step":"workflow","step":"workflow"}`), ""},
		{"an array", json.RawMessage(`[]`), "it is not a JSON object"},
		{"a string", json.RawMessage(`"x"`), ""},
		{"null", json.RawMessage(`null`), ""},
		{"empty", json.RawMessage(``), ""},
		{"no member", json.RawMessage(`{}`), broken},
		{"a run key", documentsOf(t, "explore-workflow.json", func(d map[string]any) { d["run_key"] = "qrk_a.b" }), "it contains a run_key member"},
		{"a run code", documentsOf(t, "explore-workflow.json", func(d map[string]any) { d["run_code"] = "qecr_x" }), "it contains a run_code member"},
		{"a member the schema does not define that it must understand", documentsOf(t, "explore-workflow.json", func(d map[string]any) {
			d["prompt"], d["must_understand"] = "x", []any{"/prompt"}
		}), broken},
		{"a pointer to no member", documentsOf(t, "explore-workflow.json", func(d map[string]any) { d["must_understand"] = []any{"/zz"} }), broken},
		{"a key in the run settings that it must understand", withSettings(func(d map[string]any) {
			settings(d)["api_key"], d["must_understand"] = "example-key", []any{"/run_settings/api_key"}
		}), broken},
		{"harness_hidden without a workflow", documentsOf(t, "explore-workflow.json", func(d map[string]any) { delete(d, "workflow") }), broken},
		{"another domain in inputs", documentsOf(t, "explore-workflow.json", func(d map[string]any) { inputs(d)["domain"] = "marketing" }), "its inputs.domain is not its domain"},
		{"another work item in inputs", documentsOf(t, "explore-workflow.json", func(d map[string]any) {
			inputs(d)["work_item"].(map[string]any)["id"] = "wi_0000000000"
		}), "its inputs.work_item.id is not its subject's id"},
		{"no work item in inputs", documentsOf(t, "explore-workflow.json", func(d map[string]any) { delete(inputs(d), "work_item") }), "its inputs.work_item.id is not its subject's id"},
		{"another domain in inputs, and the domain as DOMAIN", alsoAfter(documentsOf(t, "explore-workflow.json", func(d map[string]any) { inputs(d)["domain"] = "marketing" }),
			`"domain":"marketing"`, `"DOMAIN":"software"`), "its inputs.domain is not its domain"},
		{"another work item in inputs, and the work item as WORK_ITEM", alsoAfter(documentsOf(t, "explore-workflow.json", func(d map[string]any) {
			inputs(d)["work_item"] = map[string]any{"id": "wi_0000000000"}
		}), `"work_item":{"id":"wi_0000000000"}`, `"WORK_ITEM":{"id":"wi_8Kx2mQ4vT9"}`), "its inputs.work_item.id is not its subject's id"},
		{"another work item id in inputs, and the id as ID", alsoAfter(documentsOf(t, "explore-workflow.json", func(d map[string]any) {
			inputs(d)["work_item"].(map[string]any)["id"] = "wi_0000000000"
		}), `"id":"wi_0000000000"`, `"ID":"wi_8Kx2mQ4vT9"`), "its inputs.work_item.id is not its subject's id"},
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
		{"a time limit of 0 minutes", withSettings(func(d map[string]any) { d["time_limit_minutes"] = 0 }), broken},
		{"a time limit of 1441 minutes", withSettings(func(d map[string]any) { d["time_limit_minutes"] = 1441 }), broken},
		{"a time limit with a fraction", withSettings(func(d map[string]any) { d["time_limit_minutes"] = 45.5 }), broken},
		{"shared_run that is not a boolean", withSettings(func(d map[string]any) { d["shared_run"] = "yes" }), broken},
		{"a prebuild with a time limit", documentsOf(t, "prebuild.json", func(d map[string]any) { d["time_limit_minutes"] = 45 }), broken},
		{"a model not listed", withSettings(func(d map[string]any) { settings(d)["model"] = "example-tiny" }), broken},
		{"an effort not listed for the model", withSettings(func(d map[string]any) { settings(d)["effort"] = "huge" }), broken},
		{"an effort for a model with none", withSettings(func(d map[string]any) {
			s := settings(d)
			s["model"] = "example-small"
			s["tiers"] = map[string]any{"light": "example-small"}
		}), broken},
		{"a tier's model not listed", withSettings(func(d map[string]any) { settings(d)["tiers"].(map[string]any)["balanced"] = "example-tiny" }), broken},
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

// TestDocumentsWithMembersTheSchemaDoesNotDefineAreRead reads documents with a member
// that the schema does not define, at the top or in run_settings: each is read as the
// fixture is, without the member. Documents whose must_understand lists only members the
// schema defines are read too, and a member in another case never fills the defined one.
func TestDocumentsWithMembersTheSchemaDoesNotDefineAreRead(t *testing.T) {
	settings := func(doc map[string]any) map[string]any { return doc["run_settings"].(map[string]any) }
	for _, c := range []struct {
		name    string
		fixture string
		edit    func(d map[string]any)
	}{
		{"a member at the top", "explore-workflow.json", func(d map[string]any) { d["prompt"] = "x" }},
		{"a run id", "explore-workflow.json", func(d map[string]any) { d["run_id"] = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d" }},
		{"a key in the run settings", "explore-run-settings.json", func(d map[string]any) { settings(d)["api_key"] = "example-key" }},
		{"a time limit in the run settings", "explore-run-settings.json", func(d map[string]any) { settings(d)["time_limit_minutes"] = 45 }},
		{"a prebuild with full_suite", "prebuild.json", func(d map[string]any) { d["full_suite"] = true }},
		{"a must_understand of members the schema defines", "explore-run-settings.json", func(d map[string]any) {
			d["must_understand"] = []any{"/run_settings/runtime", "/subject/kind", "/inputs/work_item"}
		}},
		{"a workflow in another case", "explore-workflow.json", func(d map[string]any) { d["WORKFLOW"] = "other" }},
	} {
		// The fixture through the same marshal, so its inputs have the same bytes.
		want, err := provisioner.ReadDocuments(documentsOf(t, c.fixture, func(map[string]any) {}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := provisioner.ReadDocuments(documentsOf(t, c.fixture, c.edit))
		want.MustUnderstand = got.MustUnderstand
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, %v, want %+v", c.name, got, err, want)
		}
	}
}

// TestADocumentsMemberInAnotherCaseIsNotRead reads the documents of a fixture with a
// second workflow member written in capitals, before and after the defined one: the
// documents keep the defined member's value. A build that matches member names in any
// case fails here.
func TestADocumentsMemberInAnotherCaseIsNotRead(t *testing.T) {
	body := documentsOf(t, "explore-workflow.json", nil)
	want, err := provisioner.ReadDocuments(body)
	if err != nil || want.Workflow == "" {
		t.Fatalf("%+v, %v", want, err)
	}
	const variant = `"WORKFLOW":"other"`
	for name, b := range map[string][]byte{
		"before": bytes.Replace(body, []byte("{"), []byte("{"+variant+","), 1),
		"after":  append(bytes.TrimSuffix(bytes.TrimSpace(body), []byte("}")), []byte(","+variant+"}")...),
	} {
		got, err := provisioner.ReadDocuments(b)
		if err != nil || got.Workflow != want.Workflow {
			t.Errorf("the variant %s: %q, %v, want %q", name, got.Workflow, err, want.Workflow)
		}
	}
}

// TestLargestPrebuiltImageBytes reads largest_prebuilt_image_bytes of a prebuild's
// documents as an integer of 1 or more within int64, and nil when the member is absent,
// which is no limit. A value that is 0, below 0, a string, a fraction or above int64,
// and the member in the documents of another step, break the schema.
func TestLargestPrebuiltImageBytes(t *testing.T) {
	const broken = "it breaks documents.schema.json"
	for _, c := range []struct {
		name string
		raw  json.RawMessage
		want int64
	}{
		{"absent", documentsOf(t, "prebuild.json", nil), 0},
		{"10 GB", documentsOf(t, "prebuild.json", func(d map[string]any) { d["largest_prebuilt_image_bytes"] = json.Number("10000000000") }), 10000000000},
		{"1", documentsOf(t, "prebuild.json", func(d map[string]any) { d["largest_prebuilt_image_bytes"] = 1 }), 1},
		{"the largest int64", documentsOf(t, "prebuild.json", func(d map[string]any) { d["largest_prebuilt_image_bytes"] = json.Number("9223372036854775807") }), 9223372036854775807},
	} {
		doc, err := provisioner.ReadDocuments(c.raw)
		switch {
		case err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want == 0 && doc.LargestPrebuiltImageBytes != nil:
			t.Errorf("%s: read %d, want none", c.name, *doc.LargestPrebuiltImageBytes)
		case c.want != 0 && (doc.LargestPrebuiltImageBytes == nil || *doc.LargestPrebuiltImageBytes != c.want):
			t.Errorf("%s: read %v, want %d", c.name, doc.LargestPrebuiltImageBytes, c.want)
		}
	}
	for _, c := range []struct {
		name  string
		value any
	}{
		{"0", 0},
		{"-1", -1},
		{"a string", "10000000000"},
		{"a fraction", 1.5},
		{"above int64", json.Number("9223372036854775808")},
		{"null", nil},
	} {
		raw := documentsOf(t, "prebuild.json", func(d map[string]any) { d["largest_prebuilt_image_bytes"] = c.value })
		if _, err := provisioner.ReadDocuments(raw); err == nil || err.Error() != broken {
			t.Errorf("%s: %v, want %q", c.name, err, broken)
		}
	}
	raw := documentsOf(t, "explore-workflow.json", func(d map[string]any) { d["largest_prebuilt_image_bytes"] = 10000000000 })
	if _, err := provisioner.ReadDocuments(raw); err == nil || err.Error() != broken {
		t.Errorf("an explore run: %v, want %q", err, broken)
	}
}

// TestRunSettingsWithTheTierBalancedMapToCompose maps the tiers light, balanced and
// thinking of a documents fixture to the tier and tier-effort flags of the compose. Run
// settings whose tier map has the key fast, which is no tier, are read without it, and
// refused when their must_understand lists it.
func TestRunSettingsWithTheTierBalancedMapToCompose(t *testing.T) {
	doc, err := provisioner.ReadDocuments(documentsOf(t, "explore-run-settings.json", nil))
	if err != nil {
		t.Fatal(err)
	}
	_, got := provisioner.Settings(doc.RunSettings)
	var tiers []string
	for _, g := range got {
		if g.Flag == "tier" || g.Flag == "tier-effort" {
			tiers = append(tiers, g.Flag+" "+g.Value)
		}
	}
	want := []string{"tier light=example-small", "tier balanced=example-large", "tier thinking=example-large", "tier-effort thinking=max"}
	if !reflect.DeepEqual(tiers, want) {
		t.Errorf("the tier flags are %q, want %q", tiers, want)
	}
	fast := func(d map[string]any) {
		d["run_settings"].(map[string]any)["tiers"].(map[string]any)["fast"] = "example-small"
	}
	if got, err := provisioner.ReadDocuments(documentsOf(t, "explore-run-settings.json", fast)); err != nil || !reflect.DeepEqual(got.RunSettings, doc.RunSettings) {
		t.Errorf("run settings with the tier fast: %+v, %v", got.RunSettings, err)
	}
	mustFast := documentsOf(t, "explore-run-settings.json", func(d map[string]any) {
		fast(d)
		d["must_understand"] = []any{"/run_settings/tiers/fast"}
	})
	if _, err := provisioner.ReadDocuments(mustFast); err == nil {
		t.Error("run settings that must understand the tier fast are read")
	}
}
