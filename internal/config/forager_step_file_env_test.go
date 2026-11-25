package config_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestForagerReadsTheStepFileEnv reads wall.step_file_env in the file's order and lists it
// in one row with the file as origin; absent, it is none and has no row.
func TestForagerReadsTheStepFileEnv(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "wall:\n  adapter: docker\n  step_file_env: [CI_STEP_ENV_FILE, GITHUB_ENV, ci_path_file]\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CI_STEP_ENV_FILE", "GITHUB_ENV", "ci_path_file"}; !slices.Equal(r.Wall.StepFileEnv, want) {
		t.Errorf("wall.step_file_env read as %q, want %q", r.Wall.StepFileEnv, want)
	}
	if !slices.Contains(r.Rows(), config.Row{Key: "wall.step_file_env", Value: "CI_STEP_ENV_FILE, GITHUB_ENV, ci_path_file", Origin: path}) {
		t.Errorf("no row wall.step_file_env in %+v", r.Rows())
	}
	foragerFile(t, "wall:\n  adapter: docker\n")
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	if r.Wall.StepFileEnv != nil {
		t.Errorf("wall.step_file_env read as %q, want none", r.Wall.StepFileEnv)
	}
	for _, row := range r.Rows() {
		if row.Key == "wall.step_file_env" {
			t.Errorf("a row %+v for an absent wall.step_file_env", row)
		}
	}
}

// TestForagerStopsAtAStepFileEnvThatIsNoName is an entry of wall.step_file_env that is no
// variable's name: every command that reads forager.yaml stops, with an error that has
// the entry's index and not its value.
func TestForagerStopsAtAStepFileEnvThatIsNoName(t *testing.T) {
	hermetic(t)
	for _, name := range []string{"", "1CI_FILE", "CI-FILE", "CI_FILE=/tmp/x", "$CI_FILE", "CI FILE"} {
		path := foragerFile(t, "wall:\n  adapter: docker\n  step_file_env: [CI_STEP_ENV_FILE, \""+name+"\"]\n")
		_, err := config.LoadForager()
		want := "Set wall.step_file_env[1] to a variable's name.\n  The entry is not one.\n  wall.step_file_env is in " + path + "."
		if err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", name, err, want)
		}
		if err != nil && name != "" && strings.Contains(err.Error(), name) {
			t.Errorf("%q: the error contains the value: %v", name, err)
		}
	}
}

// TestTheForagerSchemaDescribesTheStepFileEnv checks that forager.schema.json describes
// wall.step_file_env as the reader reads it: a list of variable names.
func TestTheForagerSchemaDescribesTheStepFileEnv(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"wall: {adapter: docker, step_file_env: [CI_STEP_ENV_FILE, ci_path_file]}\n", true},
		{"wall: {adapter: docker, step_file_env: []}\n", true},
		{"wall: {adapter: docker, step_file_env: [CI-FILE]}\n", false},
		{"wall: {adapter: docker, step_file_env: CI_STEP_ENV_FILE}\n", false},
		{"wall: {adapter: docker, step_file_env: [7]}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}
