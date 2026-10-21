package provisioner

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"testing"

	"github.com/qoryai/integrations/contracts"
)

// handoverRefused and claimRefused are the errors of an answer the schema, or a rule it
// states, refuses.
const (
	handoverRefused = "it breaks handover-answer.schema.json"
	claimRefused    = "it breaks claim-answer.schema.json"
)

// noTarget is a handover's 200 at revision 3 of an explore run with no target, in the
// domain marketing, with run settings. Each case changes it in one place.
func noTarget() map[string]any {
	return map[string]any{
		"version": 1, "revision": 3, "run_id": testRun, "run_key": "qrk_abc.def",
		"step": "explore", "domain": "marketing",
		"work_item": map[string]any{"id": "wi_8Kx2mQ4vT9", "url": "https://control.example.com/work-items/wi_8Kx2mQ4vT9", "title": "A title"},
		"run_settings": map[string]any{
			"runtime": "claude", "model": "example-large", "effort": "high",
			"provider_models": []any{
				map[string]any{"id": "example-large", "effort": []any{"low", "high", "max"}},
				map[string]any{"id": "example-small"},
			},
			"tiers": map[string]any{"light": "example-small", "thinking": map[string]any{"model": "example-large", "effort": "max"}},
		},
		"inputs": map[string]any{"version": 1, "domain": "marketing"},
	}
}

// aPrebuild is a handover's 200 at revision 3 of the step prebuild, with its target.
func aPrebuild() map[string]any {
	return map[string]any{
		"version": 1, "revision": 3, "run_id": testRun, "run_key": "qrk_abc.def",
		"step": "prebuild", "domain": "software", "access": "read", "target": target(),
		"inputs": map[string]any{"version": 1, "environment_ref": "env-3f2a91c4"},
	}
}

func target() map[string]any {
	return map[string]any{"system": "code.example.com", "path": "acme/shop"}
}

// sameJSON reports whether a and b are the same JSON value.
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(marshal(t, a), &x) != nil || json.Unmarshal(marshal(t, b), &y) != nil {
		t.Fatal("not JSON")
	}
	return reflect.DeepEqual(x, y)
}

// settingsOf is the run_settings of d.
func settingsOf(d map[string]any) map[string]any { return d["run_settings"].(map[string]any) }

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAnExploreRunWithNoTargetIsGivenNoAccess reads an explore run's handover with no
// target, then the same with a target: with none, the run has no target and no access;
// either way the domain and the run settings are kept as sent. A run_settings of the
// runtime alone is read too.
func TestAnExploreRunWithNoTargetIsGivenNoAccess(t *testing.T) {
	d := noTarget()
	h, err := readHandover(marshal(t, d), testRun)
	if err != nil {
		t.Fatal(err)
	}
	if h.Target != nil || h.Access != "" || h.Domain != "marketing" {
		t.Errorf("no target: target %v, access %q, domain %q", h.Target, h.Access, h.Domain)
	}
	if rs := h.RunSettings; rs == nil || !sameJSON(t, rs, d["run_settings"]) {
		t.Errorf("the run settings are %s, want %s", marshal(t, rs), marshal(t, d["run_settings"]))
	}

	d["target"], d["access"] = target(), "write"
	h, err = readHandover(marshal(t, d), testRun)
	if err != nil {
		t.Fatal(err)
	}
	if h.Target == nil || *h.Target != (contracts.Target{System: "code.example.com", Path: "acme/shop"}) || h.Access != "write" || h.Domain != "marketing" || h.RunSettings == nil {
		t.Errorf("a target: %+v", h)
	}

	d = noTarget()
	d["run_settings"] = map[string]any{"runtime": "claude"}
	if h, err = readHandover(marshal(t, d), testRun); err != nil || h.RunSettings == nil || string(marshal(t, h.RunSettings)) != `{"runtime":"claude"}` {
		t.Errorf("the runtime alone: %+v %v", h.RunSettings, err)
	}

	if h, err = readHandover(marshal(t, aPrebuild()), testRun); err != nil || h.Target == nil || h.Access != "read" || h.RunSettings != nil {
		t.Errorf("a prebuild: %+v %v", h, err)
	}
}

// TestAHandoverOutsideRevision3IsRefused changes an answer that is read, in one place
// each: a target of null, an access of "", one without the other, no domain, revision 2,
// a prebuild with no target, and run settings that break a rule of the schema or one
// it states. The schema reads the bytes before they are decoded, so a "target":null or
// an "access":"" that Go decodes as no target is refused. A value with a trailing line
// feed passes a pattern under PCRE's or Python's $, and is refused here. Each is
// refused, and the error quotes no value.
func TestAHandoverOutsideRevision3IsRefused(t *testing.T) {
	cases := []struct {
		name string
		base func() map[string]any
		edit func(d map[string]any)
	}{
		{"target null", noTarget, func(d map[string]any) { d["target"] = nil }},
		{"access empty", noTarget, func(d map[string]any) { d["access"] = "" }},
		{"access empty and target null", noTarget, func(d map[string]any) { d["access"], d["target"] = "", nil }},
		{"access without target", noTarget, func(d map[string]any) { d["access"] = "read" }},
		{"target without access", noTarget, func(d map[string]any) { d["target"] = target() }},
		{"target null with access", noTarget, func(d map[string]any) { d["access"], d["target"] = "read", nil }},
		{"no domain", noTarget, func(d map[string]any) { delete(d, "domain") }},
		{"domain empty", noTarget, func(d map[string]any) { d["domain"] = "" }},
		{"domain null", noTarget, func(d map[string]any) { d["domain"] = nil }},
		{"revision 2", noTarget, func(d map[string]any) { d["revision"] = 2 }},
		{"revision 2 with a target", noTarget, func(d map[string]any) {
			d["revision"], d["target"], d["access"] = 2, target(), "read"
			delete(d, "domain")
			delete(d, "run_settings")
		}},
		{"a prebuild with no target", aPrebuild, func(d map[string]any) { delete(d, "target"); delete(d, "access") }},
		{"a prebuild with target null", aPrebuild, func(d map[string]any) { d["target"] = nil; delete(d, "access") }},
		{"a prebuild with run settings", aPrebuild, func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "claude"} }},
		{"run settings null", noTarget, func(d map[string]any) { d["run_settings"] = nil }},
		{"a model with a trailing line feed", noTarget, func(d map[string]any) {
			s := settingsOf(d)
			s["model"] = "example-large\n"
			s["provider_models"].([]any)[0].(map[string]any)["id"] = "example-large\n"
			s["tiers"] = map[string]any{"light": "example-small"}
		}},
		{"an effort with a trailing line feed", noTarget, func(d map[string]any) {
			s := settingsOf(d)
			s["effort"] = "high\n"
			s["provider_models"].([]any)[0].(map[string]any)["effort"] = []any{"low", "high\n"}
			s["tiers"] = map[string]any{"light": "example-small"}
		}},
		{"a tier with a trailing line feed", noTarget, func(d map[string]any) {
			s := settingsOf(d)
			s["provider_models"].([]any)[1].(map[string]any)["id"] = "example-small\n"
			s["tiers"] = map[string]any{"light": "example-small\n"}
		}},
		{"a runtime with a trailing line feed", noTarget, func(d map[string]any) { settingsOf(d)["runtime"] = "claude\n" }},
		{"an endpoint in the run settings", noTarget, func(d map[string]any) { settingsOf(d)["endpoint"] = "https://models.example.com" }},
		{"a key in the run settings", noTarget, func(d map[string]any) { settingsOf(d)["api_key"] = "example-key" }},
		{"no runtime", noTarget, func(d map[string]any) { delete(settingsOf(d), "runtime") }},
		{"a model with no provider models", noTarget, func(d map[string]any) { delete(settingsOf(d), "provider_models") }},
		{"a model not listed", noTarget, func(d map[string]any) { settingsOf(d)["model"] = "example-medium" }},
		{"an effort not listed for the model", noTarget, func(d map[string]any) { settingsOf(d)["effort"] = "medium" }},
		{"an effort for a model with none", noTarget, func(d map[string]any) {
			s := settingsOf(d)
			s["model"] = "example-small"
			s["tiers"] = map[string]any{"light": "example-small"}
		}},
		{"a tier's model not listed", noTarget, func(d map[string]any) {
			settingsOf(d)["tiers"].(map[string]any)["fast"] = "example-tiny"
		}},
		{"a tier's effort not listed", noTarget, func(d map[string]any) {
			settingsOf(d)["tiers"].(map[string]any)["thinking"] = map[string]any{"model": "example-large", "effort": "medium"}
		}},
		{"a model listed twice", noTarget, func(d map[string]any) {
			s := settingsOf(d)
			s["provider_models"] = append(s["provider_models"].([]any), map[string]any{"id": "example-small"})
		}},
		{"an unknown tier", noTarget, func(d map[string]any) { settingsOf(d)["tiers"].(map[string]any)["huge"] = "example-large" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := readHandover(marshal(t, c.base()), testRun); err != nil {
				t.Fatalf("the answer before the change is refused: %v", err)
			}
			d := c.base()
			c.edit(d)
			h, err := readHandover(marshal(t, d), testRun)
			if err == nil || err.Error() != handoverRefused || h.RunKey.Len() != 0 {
				t.Errorf("got %v, want %s", err, handoverRefused)
			}
		})
	}
}

// TestAClaimOutsideRevision3IsRefused reads the contract's claim of an explore run with
// no target, which has no access, then changes it in one place each: a target of null,
// an access of "", one without the other, revision 2, and a prebuild with no target.
// Each is refused, as the schema reads the bytes before they are decoded.
func TestAClaimOutsideRevision3IsRefused(t *testing.T) {
	base := func() map[string]any {
		b, err := fs.ReadFile(contracts.ProvisionerFS, "fixtures/claim-answer/explore-without-target.json")
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	cl, err := readClaim(marshal(t, base()))
	if err != nil {
		t.Fatal(err)
	}
	if cl.Target != nil || cl.Access != "" || cl.Step != StepExplore || cl.WorkItem == nil {
		t.Errorf("no target: %+v", cl)
	}
	d := base()
	d["target"], d["access"] = target(), "read"
	if cl, err := readClaim(marshal(t, d)); err != nil || cl.Target == nil || cl.Target.Path != "acme/shop" || cl.Access != "read" {
		t.Errorf("a target: %+v %v", cl, err)
	}
	for name, edit := range map[string]func(d map[string]any){
		"target null":                  func(d map[string]any) { d["target"] = nil },
		"access empty":                 func(d map[string]any) { d["access"] = "" },
		"access empty and target null": func(d map[string]any) { d["access"], d["target"] = "", nil },
		"access without target":        func(d map[string]any) { d["access"] = "write" },
		"target without access":        func(d map[string]any) { d["target"] = target() },
		"revision 2":                   func(d map[string]any) { d["revision"] = 2 },
		"a prebuild with no target":    func(d map[string]any) { d["step"] = StepPrebuild; delete(d, "work_item") },
	} {
		d := base()
		edit(d)
		if _, err := readClaim(marshal(t, d)); err == nil || err.Error() != claimRefused {
			t.Errorf("%s: got %v, want %s", name, err, claimRefused)
		}
	}
}
