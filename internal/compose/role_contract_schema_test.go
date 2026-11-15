package compose_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/module"
)

// TestModuleSchemaMatchesTheReaderOnRoleContracts keeps module.schema.json and
// module.ReadManifest in step on the map form of a role: each roles value passes both or
// fails both.
func TestModuleSchemaMatchesTheReaderOnRoleContracts(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		roles string
		valid bool
	}{
		{`{"agents": {"developer": "one"}}`, true},
		{`{"agents": {"developer": {"fill": "one"}}}`, true},
		{`{"agents": {"developer": {"fill": "one", "contract": "roles/developer.yaml"}}}`, true},
		{`{"agents": {"developer": {"fill": "one", "contract": "developer.yaml"}}}`, true},
		{`{"agents": {"developer": {"fill": "one", "contract": "a..b/developer.yaml"}}}`, true},
		{`{"agents": {"developer": {"fill": "many"}}}`, false},
		{`{"agents": {"developer": {"contract": "roles/developer.yaml"}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "paths": ["a"]}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": ""}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": "/roles/developer.yaml"}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": "../developer.yaml"}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": "roles/../developer.yaml"}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": "roles/.."}}}`, false},
		{`{"agents": {"developer": {"fill": "one", "contract": 3}}}`, false},
		{`{"skills": {"lint": {"fill": "one"}}}`, false},
		{`{"skills": {"lint": {"fill": "one", "contract": "roles/lint.yaml"}}}`, false},
	} {
		var roles any
		if err := json.Unmarshal([]byte(c.roles), &roles); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "name": "core", "roles": roles}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.roles, c.valid, err)
		}
		dir := t.TempDir()
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.ManifestName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := module.ReadManifest(dir); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.roles, c.valid, err)
		}
	}
}

// TestRoleContractSchemaMatchesTheReader keeps role-contract.schema.json and
// module.ParseContract in step: each contract passes both or fails both.
func TestRoleContractSchemaMatchesTheReader(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/role-contract.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		contract string
		valid    bool
	}{
		{`{}`, true},
		{`{"sections": ["Guardrails", "The work"], "front_matter": ["name", "model"], "tools": ["Read", "Bash(git:*)"], "tier": true, "report": {}}`, true},
		{`{"sections": [], "tier": false, "report": {"kind": "list"}}`, true},
		{`{"sections": ["C#"]}`, true},
		{`{"paths": ["a"]}`, false},
		{`{"sections": "Guardrails"}`, false},
		{`{"sections": null}`, false},
		{`{"sections": [3]}`, false},
		{`{"sections": [""]}`, false},
		{`{"sections": ["## Guardrails"]}`, false},
		{`{"tools": ["Read\nBash"]}`, false},
		{`{"tools": ["Read", "Read"]}`, false},
		{`{"front_matter": [true]}`, false},
		{`{"tier": "yes"}`, false},
		{`{"report": []}`, false},
		{`[]`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.contract), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.contract, c.valid, err)
		}
		if _, err := module.ParseContract([]byte(c.contract)); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.contract, c.valid, err)
		}
	}
}
