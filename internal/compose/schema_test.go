package compose_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// TestSchemas validates every fixture document against the contract's schemas: a profile
// that composes passes profile.schema.json, one refused for its apiVersion or kind fails it,
// and every layer manifest passes layer.schema.json.
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
		manifests, _ := filepath.Glob(filepath.Join(dir, "layers", "*", "harness.yaml"))
		for _, m := range manifests {
			if err := layerSchema.Validate(document(t, m)); err != nil {
				t.Errorf("%s: %v", m, err)
			}
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
