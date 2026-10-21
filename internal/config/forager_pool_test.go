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

// poolCommit is a commit id of the harness's repository, as pool.harness_ref takes one.
const poolCommit = "0123456789abcdef0123456789abcdef01234567"

// poolBody is a forager.yaml whose pool section is the lines given, each indented under
// it.
func poolBody(lines ...string) string {
	return "apiVersion: qory.dev/v1alpha1\npool:\n  " + strings.Join(lines, "\n  ") + "\n"
}

// TestForagerFileReadsThePoolSection reads the pool section: runs and prebuilds 1 each
// when the file sets none, else what it sets; the registry; harness_ref or harness_path,
// whichever the file sets; and run_config, cleaned. qory config lists each, the defaults
// as defaults, and a file without the section lists none of them.
func TestForagerFileReadsThePoolSection(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, poolBody("registry: localhost:5001/qory-env", "harness_ref: "+poolCommit, "run_config: /srv/pool/runs/"))
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	want := config.ForagerPool{Runs: 1, Prebuilds: 1, Registry: "localhost:5001/qory-env", HarnessRef: poolCommit, RunConfig: "/srv/pool/runs"}
	if p := r.Pool; p == nil || p.Runs != want.Runs || p.Prebuilds != want.Prebuilds || p.Registry != want.Registry || p.HarnessRef != want.HarnessRef || p.HarnessPath != "" || p.RunConfig != want.RunConfig {
		t.Fatalf("the pool section read as %+v, want %+v", r.Pool, want)
	}
	if config.DefaultPoolRuns != 1 || config.DefaultPoolPrebuilds != 1 {
		t.Errorf("the defaults are runs %d and prebuilds %d, want 1 and 1", config.DefaultPoolRuns, config.DefaultPoolPrebuilds)
	}
	rows := poolRows(r)
	for key, w := range map[string]config.Row{
		"pool.runs":        {Key: "pool.runs", Value: "1", Origin: config.Default},
		"pool.prebuilds":   {Key: "pool.prebuilds", Value: "1", Origin: config.Default},
		"pool.registry":    {Key: "pool.registry", Value: "localhost:5001/qory-env", Origin: path},
		"pool.harness_ref": {Key: "pool.harness_ref", Value: poolCommit, Origin: path},
		"pool.run_config":  {Key: "pool.run_config", Value: "/srv/pool/runs", Origin: path},
	} {
		if rows[key] != w {
			t.Errorf("%s: %+v, want %+v", key, rows[key], w)
		}
	}
	if len(rows) != 5 {
		t.Errorf("the pool section lists %d rows, want 5: %v", len(rows), rows)
	}

	foragerFile(t, poolBody("runs: 3", "prebuilds: 2", "registry: registry.example/acme/env", "harness_path: /src/qory-github/", "run_config: /srv/pool/runs"))
	r, err = config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Pool; p.Runs != 3 || p.Prebuilds != 2 || p.HarnessRef != "" || p.HarnessPath != "/src/qory-github" {
		t.Fatalf("the pool section read as %+v", p)
	}
	rows = poolRows(r)
	for key, value := range map[string]string{"pool.runs": "3", "pool.prebuilds": "2", "pool.harness_path": "/src/qory-github"} {
		if rows[key].Value != value || rows[key].Origin != r.File {
			t.Errorf("%s: %+v, want %s from the file", key, rows[key], value)
		}
	}
	if _, ok := rows["pool.harness_ref"]; ok {
		t.Error("pool.harness_ref is listed, and the file sets harness_path")
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
// run by, naming the file and the key, and never quoting a value that holds a secret.
func TestForagerFileRefusesAPoolSectionItCannotRun(t *testing.T) {
	hermetic(t)
	registry, ref, runs := "registry: localhost:5001/qory-env", "harness_ref: "+poolCommit, "run_config: /srv/pool/runs"
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"runs: 0", registry, ref, runs}, "pool.runs is below 1; a pool runs at least 1 at once"},
		{[]string{"prebuilds: 0", registry, ref, runs}, "pool.prebuilds is below 1 or above pool.runs; the builds are a share of the runs"},
		{[]string{"prebuilds: 2", registry, ref, runs}, "pool.prebuilds is below 1 or above pool.runs; the builds are a share of the runs"},
		{[]string{"runs: 2", "prebuilds: 3", registry, ref, runs}, "pool.prebuilds is below 1 or above pool.runs; the builds are a share of the runs"},
		{[]string{ref, runs}, "pool.registry is required"},
		{[]string{"registry: localhost:5001/qory-env:latest", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001/Qory-Env", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: localhost:5001/qory-env/", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: \"localhost:5001/qory-env@sha256:0000000000000000000000000000000000000000000000000000000000000000\"", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: \"\"", ref, runs}, "pool.registry is not an image name"},
		{[]string{"registry: registry.example/" + strings.Repeat("a", 222), ref, runs}, "pool.registry is not an image name"},
		{[]string{registry, runs}, "the pool section needs exactly one of harness_ref and harness_path"},
		{[]string{registry, ref, "harness_path: /src/qory-github", runs}, "the pool section needs exactly one of harness_ref and harness_path"},
		{[]string{registry, "harness_ref: main", runs}, "pool.harness_ref is neither a commit id nor a release tag"},
		{[]string{registry, "harness_ref: v1.2", runs}, "pool.harness_ref is neither a commit id nor a release tag"},
		{[]string{registry, "harness_ref: " + strings.ToUpper(poolCommit), runs}, "pool.harness_ref is neither a commit id nor a release tag"},
		{[]string{registry, "harness_path: src/qory-github", runs}, "pool.harness_path is not an absolute path"},
		{[]string{registry, ref}, "pool.run_config is required"},
		{[]string{registry, ref, "run_config: runs"}, "pool.run_config is not an absolute path"},
		{[]string{registry, ref, runs, "harness: /src"}, "harness"},
	} {
		path := foragerFile(t, poolBody(c.lines...))
		_, err := config.LoadForager()
		if err == nil || !strings.HasPrefix(err.Error(), path+": ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.lines, err, c.want)
		}
	}
	// A value that holds an access key secret or a run key is refused, and not quoted.
	for key, lines := range map[string][]string{
		"registry":     {"registry: localhost:5001/qak_0123456789", ref, runs},
		"harness_path": {registry, "harness_path: /src/qrk_0123456789", runs},
		"run_config":   {registry, ref, "run_config: /srv/QAK_0123456789"},
	} {
		path := foragerFile(t, poolBody(lines...))
		_, err := config.LoadForager()
		if err == nil || !strings.HasPrefix(err.Error(), path+": pool."+key+": ") || strings.Contains(err.Error(), "0123456789") {
			t.Errorf("pool.%s: error %v, want one naming the key and holding no value", key, err)
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
	registry, ref, runs := "registry: localhost:5001/qory-env", "harness_ref: "+poolCommit, "run_config: /srv/pool/runs"
	for _, c := range []struct {
		lines []string
		valid bool
	}{
		{[]string{registry, ref, runs}, true},
		{[]string{"runs: 4", "prebuilds: 2", "registry: registry.example/acme/env", "harness_ref: v1.2.3", runs}, true},
		{[]string{registry, "harness_path: /src/qory-github", runs}, true},
		{[]string{"runs: 0", registry, ref, runs}, false},
		{[]string{"prebuilds: 0", registry, ref, runs}, false},
		{[]string{ref, runs}, false},
		{[]string{"registry: localhost:5001/qory-env:latest", ref, runs}, false},
		{[]string{"registry: localhost:5001", ref, runs}, false},
		{[]string{"registry: registry.example/" + strings.Repeat("a", 222), ref, runs}, false},
		{[]string{registry, runs}, false},
		{[]string{registry, ref, "harness_path: /src/qory-github", runs}, false},
		{[]string{registry, "harness_ref: main", runs}, false},
		{[]string{registry, "harness_path: src/qory-github", runs}, false},
		{[]string{registry, ref}, false},
		{[]string{registry, ref, "run_config: runs"}, false},
		{[]string{registry, ref, runs, "harness: /src"}, false},
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
