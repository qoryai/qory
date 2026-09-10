package compose_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/layer"
)

// TestSchemas validates every fixture document against the contract's schemas: a profile
// that composes passes profile.schema.json, one refused for its apiVersion or kind fails it,
// every layer manifest passes layer.schema.json, and every MCP server file passes
// mcp.schema.json unless the fixture expects it refused.
func TestSchemas(t *testing.T) {
	c := jsonschema.NewCompiler()
	profileSchema, err := c.Compile("../../contracts/harness/v1/profile.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	layerSchema, err := c.Compile("../../contracts/harness/v1/layer.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	mcpSchema, err := c.Compile("../../contracts/harness/v1/mcp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := filepath.Glob(filepath.Join(fixtures, "*"))
	for _, dir := range dirs {
		errText, _ := os.ReadFile(filepath.Join(dir, "expected", "error.txt"))
		wantInvalid := strings.Contains(string(errText), "apiVersion") || strings.Contains(string(errText), "kind ")
		err := profileSchema.Validate(document(t, filepath.Join(dir, "harness-compose.yaml")))
		if wantInvalid && err == nil {
			t.Errorf("%s: profile passed the schema; want a failure", dir)
		}
		if !wantInvalid && err != nil {
			t.Errorf("%s: profile failed the schema: %v", dir, err)
		}
		manifests, _ := filepath.Glob(filepath.Join(dir, "layers", "*", "harness-layer.yaml"))
		for _, m := range manifests {
			if err := layerSchema.Validate(document(t, m)); err != nil {
				t.Errorf("%s: %v", m, err)
			}
		}
		servers, _ := filepath.Glob(filepath.Join(dir, "layers", "*", "mcp", "*.json"))
		// A refused server names its file, mcp/<name>.json; a collision or an exclude over an
		// mcp entry names mcp/<name> without the extension, and its files are valid.
		wantBadServer := strings.Contains(string(errText), "mcp/") && strings.Contains(string(errText), ".json")
		for _, f := range servers {
			err := mcpSchema.Validate(document(t, f))
			if wantBadServer && err == nil {
				t.Errorf("%s passed the schema; want a failure", f)
			}
			if !wantBadServer && err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
	}
}

// TestMCPSchemaMatchesTheReader keeps the schema and layer.Read in step on the server
// shapes: each document is accepted or refused by both.
func TestMCPSchemaMatchesTheReader(t *testing.T) {
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../contracts/harness/v1/mcp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{`{"command": "python3", "args": ["srv.py"], "env": {"A": "1"}}`, true},
		{`{"type": "http", "url": "https://mcp.example.com", "headers": {"X": "y"}}`, true},
		{`{"command": "x", "url": "https://mcp.example.com"}`, false},
		{`{"args": ["x"]}`, false},
		{`{"comand": "x"}`, false},
		{`{}`, false},
		{`{"command": "x", "type": ""}`, false},
		{`{"url": ""}`, false},
		{`{"command": "x", "env": {"A": ""}}`, true},
		{`{"command": "x", "description": "why"}`, true},
		{`{"command": "x", "description": 3}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.body, c.valid, err)
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "mcp"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp", "s.json"), []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := layer.Read("core", dir, nil, ""); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.body, c.valid, err)
		}
	}
}

// TestProfileSchemaKnowsBothSourceForms validates the source forms no fixture composes: a
// git source needs its ref, and a path source takes no ref.
func TestProfileSchemaKnowsBothSourceForms(t *testing.T) {
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../contracts/harness/v1/profile.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		source string
		valid  bool
	}{
		{`{"path": "layers/core"}`, true},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1"}`, true},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "path": "layers/core"}`, true},
		{`{"git": "https://git.example.com/acme/harness"}`, false},
		{`{"path": "layers/core", "ref": "v1"}`, false},
		{`{}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.ai/v1alpha1", "kind": "HarnessProfile", "target": {"runtime": "claude"}, "layers": [{"name": "core", "source": `+c.source+`}]}`), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("source %s: valid=%v, err=%v", c.source, c.valid, err)
		}
	}
}

// TestProfileSchemaKnowsExtends validates the shapes no fixture composes: a profile with
// extends and no target, one with both, one with neither, and a layer entry by name alone.
func TestProfileSchemaKnowsExtends(t *testing.T) {
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../contracts/harness/v1/profile.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{`{"extends": {"git": "https://git.example.com/acme/harness", "ref": "main", "path": "nextjs-15"}, "layers": [{"name": "app"}]}`, true},
		{`{"extends": {"path": "../harness/nextjs-15"}, "layers": [{"name": "app"}]}`, true},
		{`{"extends": {"path": "../harness/nextjs-15"}, "target": {"runtime": "claude"}, "layers": [{"name": "app"}]}`, false},
		{`{"layers": [{"name": "app"}]}`, false},
		{`{"target": {"runtime": "claude"}, "layers": [{"name": "core"}], "extending": {"kinds": ["skills"], "instructions": true, "settings": ["permissions.allow"]}}`, true},
		{`{"target": {"runtime": "claude"}, "layers": [{"name": "core"}], "extending": {"kinds": ["hooks"]}}`, false},
		{`{"target": {"runtime": "claude"}, "layers": [{"exclude": {"skills": ["x"]}}]}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.ai/v1alpha1", "kind": "HarnessProfile", `+c.body[1:]), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v, err=%v", c.body, c.valid, err)
		}
	}
}

func document(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON so the value holds JSON types.
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(j, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
