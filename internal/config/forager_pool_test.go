package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// poolBody is a forager.yaml whose pool section is the lines given, each indented under
// it.
func poolBody(lines ...string) string {
	return "apiVersion: qory.dev/v1alpha1\npool:\n  " + strings.Join(lines, "\n  ") + "\n"
}

// TestForagerFileReadsThePoolSection reads the pool section: runs 1 when the file sets
// none, else what it sets; the registry; and run_config, cleaned. qory config lists each,
// the default as a default, and a file without the section lists none of them.
func TestForagerFileReadsThePoolSection(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, poolBody("registry: localhost:5001/qory-env", "run_config: /srv/pool/runs/"))
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	want := config.ForagerPool{Runs: 1, Registry: "localhost:5001/qory-env", RunConfig: "/srv/pool/runs"}
	if p := r.Pool; p == nil || p.Runs != want.Runs || p.Registry != want.Registry || p.RunConfig != want.RunConfig {
		t.Fatalf("the pool section read as %+v, want %+v", r.Pool, want)
	}
	if config.DefaultPoolRuns != 1 {
		t.Errorf("the default is runs %d, want 1", config.DefaultPoolRuns)
	}
	rows := poolRows(r)
	for key, w := range map[string]config.Row{
		"pool.runs":       {Key: "pool.runs", Value: "1", Origin: config.Default},
		"pool.registry":   {Key: "pool.registry", Value: "localhost:5001/qory-env", Origin: path},
		"pool.run_config": {Key: "pool.run_config", Value: "/srv/pool/runs", Origin: path},
	} {
		if rows[key] != w {
			t.Errorf("%s: %+v, want %+v", key, rows[key], w)
		}
	}
	if len(rows) != 3 {
		t.Errorf("the pool section lists %d rows, want 3: %v", len(rows), rows)
	}

	foragerFile(t, poolBody("runs: 3", "registry: registry.example/acme/env", "run_config: /srv/pool/runs"))
	r, err = config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Pool; p.Runs != 3 {
		t.Fatalf("the pool section read as %+v", p)
	}
	if row := poolRows(r)["pool.runs"]; row.Value != "3" || row.Origin != r.File {
		t.Errorf("pool.runs: %+v, want 3 from the file", row)
	}

	foragerFile(t, "apiVersion: qory.dev/v1alpha1\n")
	if r, err = config.LoadForager(); err != nil || r.Pool != nil {
		t.Fatalf("a file without the section read %+v, %v", r, err)
	}
	if rows := poolRows(r); len(rows) != 0 {
		t.Errorf("a file without the section lists %v", rows)
	}
}

// poolRows are the rows of r whose key starts with pool., by key.
func poolRows(r *config.Forager) map[string]config.Row {
	rows := map[string]config.Row{}
	for _, row := range r.Rows() {
		if strings.HasPrefix(row.Key, "pool.") {
			rows[row.Key] = row
		}
	}
	return rows
}

// TestForagerFileRefusesAPoolSectionItCannotRun refuses each pool section the pool cannot
// run by, naming the file and the key, and never quoting a value that contains a secret.
func TestForagerFileRefusesAPoolSectionItCannotRun(t *testing.T) {
	hermetic(t)
	registry, runs := "registry: localhost:5001/qory-env", "run_config: /srv/pool/runs"
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"runs: 0", registry, runs}, "pool.runs is below 1; a pool runs at least 1 at once"},
		{[]string{runs}, "pool.registry is required"},
		{[]string{"registry: localhost:5001/qory-env:latest", runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001/Qory-Env", runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001", runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001/qory-env/", runs}, "pool.registry is not an image name"},
		{[]string{"registry: \"localhost:5001/qory-env@sha256:0000000000000000000000000000000000000000000000000000000000000000\"", runs}, "pool.registry is not an image name"},
		{[]string{"registry: \"\"", runs}, "pool.registry is not an image name"},
		{[]string{"registry: registry.example/" + strings.Repeat("a", 222), runs}, "pool.registry is not an image name"},
		{[]string{registry}, "pool.run_config is required"},
		{[]string{registry, "run_config: runs"}, "pool.run_config is not an absolute path"},
		{[]string{registry, runs, "harness: /src"}, "harness"},
	} {
		path := foragerFile(t, poolBody(c.lines...))
		_, err := config.LoadForager()
		if err == nil || !strings.HasPrefix(err.Error(), path+": ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.lines, err, c.want)
		}
	}
	// A value that contains an access key secret or a run key is refused, and not quoted.
	for key, lines := range map[string][]string{
		"registry":   {"registry: localhost:5001/qak_0123456789", runs},
		"run_config": {registry, "run_config: /srv/QAK_0123456789"},
	} {
		path := foragerFile(t, poolBody(lines...))
		_, err := config.LoadForager()
		if err == nil || !strings.HasPrefix(err.Error(), path+": pool."+key+": ") || strings.Contains(err.Error(), "0123456789") {
			t.Errorf("pool.%s: error %v, want one naming the key and containing no value", key, err)
		}
	}
}

// TestForagerFileStopsAtTheRemovedPoolKeys returns the error of any unknown key, which
// contains the file, the line and the key, for prebuilds, harness_ref and harness_path in
// the pool section, which an earlier qory read.
func TestForagerFileStopsAtTheRemovedPoolKeys(t *testing.T) {
	hermetic(t)
	registry, runs := "registry: localhost:5001/qory-env", "run_config: /srv/pool/runs"
	for _, line := range []string{"prebuilds: 1", "harness_ref: 0123456789abcdef0123456789abcdef01234567", "harness_path: /src/qory-github"} {
		key, _, _ := strings.Cut(line, ":")
		path := foragerFile(t, poolBody(registry, line, runs))
		_, err := config.LoadForager()
		if want := path + ": line 4: key \"" + key + "\" is not one forager.yaml reads"; err == nil || err.Error() != want {
			t.Errorf("pool.%s: error %v, want %q", key, err, want)
		}
	}
}

// TestLoadForagerInReadsAnotherDirectory reads the forager.yaml of a directory other than
// the machine's, as the machine's is read, and nil where it has none.
func TestLoadForagerInReadsAnotherDirectory(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	if r, err := config.LoadForagerIn(dir); r != nil || err != nil {
		t.Fatalf("a directory without forager.yaml read %+v, %v", r, err)
	}
	path := filepath.Join(dir, config.ForagerFileName)
	if err := os.WriteFile(path, []byte("apiVersion: qory.dev/v1alpha1\ngateway: {listen: \"127.0.0.1:8443\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadForagerIn(dir)
	if err != nil || r.File != path || !r.Gateway || r.Listen != "127.0.0.1:8443" {
		t.Fatalf("read %+v, %v", r, err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: qory.dev/v1alpha1\nwall: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadForagerIn(dir); err == nil || !strings.HasPrefix(err.Error(), path+": wall.adapter is required") {
		t.Errorf("a file that does not read: %v", err)
	}
}

// TestTheForagerSchemaTakesThePoolSection holds forager.schema.json to what the reader
// takes of the pool section.
func TestTheForagerSchemaTakesThePoolSection(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry, runs := "registry: localhost:5001/qory-env", "run_config: /srv/pool/runs"
	for _, c := range []struct {
		lines []string
		valid bool
	}{
		{[]string{registry, runs}, true},
		{[]string{"runs: 4", "registry: registry.example/acme/env", runs}, true},
		{[]string{"runs: 0", registry, runs}, false},
		{[]string{runs}, false},
		{[]string{"registry: localhost:5001/qory-env:latest", runs}, false},
		{[]string{"registry: localhost:5001", runs}, false},
		{[]string{"registry: registry.example/" + strings.Repeat("a", 222), runs}, false},
		{[]string{registry}, false},
		{[]string{registry, "run_config: runs"}, false},
		{[]string{registry, runs, "harness: /src"}, false},
		{[]string{registry, runs, "prebuilds: 1"}, false},
		{[]string{registry, runs, "harness_ref: v1.2.3"}, false},
		{[]string{registry, runs, "harness_path: /src/qory-github"}, false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(poolBody(c.lines...)), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.lines, err, c.valid)
		}
	}
}
