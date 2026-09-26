package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// fixture is one of the integration contract's fixtures, the copy the integration
// package tests against: github.json is what qory-github describe prints, and
// acme-tracker.json the least a description of a machine's own holds.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "integration", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// setupPrints is what qory-github setup prints for the runner file, as the
// integrations README has it at 48a655307774073553a3ce9de5bbc30bdf9a09a6: the definition
// the declaration below expands to.
const setupPrints = `credentials:
  github:
    adapter: [qory-github, credential, --settings, '{"app_id":123456,"private_key_file":"/home/dev/.config/qory/github-app.pem"}', --, "${argument}"]
    argument: '[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}(,[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100})*'
    hosts: [github.com, api.github.com]
`

// fakeIntegration writes a program named name into dir that prints doc for describe,
// and exits 64 for anything else.
func fakeIntegration(t *testing.T, dir, name, doc string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	write := "#!/bin/sh\ntest \"$1\" = describe || exit 64\ncat <<'EOF'\n" + doc + "\nEOF\n"
	if err := os.WriteFile(path, []byte(write), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// onPath puts a new directory at the head of the PATH and returns it.
func onPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// expand loads the runner file and expands its integrations.
func expand(t *testing.T) (*config.Runner, error) {
	t.Helper()
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		return nil, err
	}
	return c.Runner, c.Runner.Expand(context.Background())
}

// TestAnIntegrationExpandsAsItsSetupPrints declares qory-github with no program, finds
// it on the PATH, and expands it to the definition qory-github setup prints for the
// same settings, the program by the path it was found at; config lists both.
func TestAnIntegrationExpandsAsItsSetupPrints(t *testing.T) {
	hermetic(t)
	program := fakeIntegration(t, onPath(t), "qory-github", fixture(t, "github.json"))
	path := runnerFile(t, "integrations:\n  github:\n    settings: {\"app_id\":123456,\"private_key_file\":\"/home/dev/.config/qory/github-app.pem\"}\n")
	r, err := expand(t)
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Credentials map[string]struct {
			Adapter  []string
			Argument string
			Hosts    []string
		}
	}
	if err := yaml.Unmarshal([]byte(setupPrints), &want); err != nil {
		t.Fatal(err)
	}
	w := want.Credentials["github"]
	w.Adapter[0] = program
	if len(r.Credentials) != 1 {
		t.Fatalf("credentials %+v", r.Credentials)
	}
	got := r.Credentials[0]
	if got.Name != "github" || strings.Join(got.Adapter, "\n") != strings.Join(w.Adapter, "\n") || got.Argument != w.Argument || strings.Join(got.Hosts, " ") != strings.Join(w.Hosts, " ") || got.Integration != "github" {
		t.Errorf("expanded to %+v\nwant %+v", got, w)
	}
	rows := map[string]config.Row{}
	c, _ := config.Load(t.TempDir(), true)
	if err := c.Runner.Expand(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"runner.credentials.github": "integration github", "runner.integrations.github": program + " dev"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
}

// TestAnIntegrationOfYourOwnIsNamedByItsPath declares a machine's own program by its
// path, under a key other than its name, with no settings, and keeps the settings'
// order as written, every $ in them written $.
func TestAnIntegrationOfYourOwnIsNamedByItsPath(t *testing.T) {
	hermetic(t)
	program := fakeIntegration(t, t.TempDir(), "acme-tracker", fixture(t, "acme-tracker.json"))
	runnerFile(t, "integrations:\n  tracker:\n    program: "+program+"\n  board:\n    program: "+program+"\n    settings:\n      url: https://tracker.acme.example/$team\n      depth: 2\n      labels: [a, b]\n      nested: {on: true, none: null, ratio: 1.5}\n")
	r, err := expand(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Credentials) != 2 {
		t.Fatalf("credentials %+v", r.Credentials)
	}
	for i, want := range []struct{ name, settings string }{
		{"tracker", `{}`},
		{"board", `{"url":"https://tracker.acme.example/$team","depth":2,"labels":["a","b"],"nested":{"on":true,"none":null,"ratio":1.5}}`},
	} {
		c := r.Credentials[i]
		adapter := []string{program, "credential", "--settings", want.settings, "--", "${argument}"}
		if c.Name != want.name || strings.Join(c.Adapter, "\n") != strings.Join(adapter, "\n") || c.Argument != "[A-Z]+" || strings.Join(c.Hosts, " ") != "tracker.acme.example" {
			t.Errorf("%s expanded to %+v, want the adapter %q", want.name, c, adapter)
		}
	}
}

// TestTheFilesOwnCredentialWins declares an integration under a name the credentials
// section defines too: the file's definition stands, and the integration is still
// described and its settings checked.
func TestTheFilesOwnCredentialWins(t *testing.T) {
	hermetic(t)
	fakeIntegration(t, onPath(t), "qory-github", fixture(t, "github.json"))
	runnerFile(t, "credentials:\n  github:\n    env: GH_TOKEN\n    hosts: [api.github.com]\n    auth: {scheme: bearer}\nintegrations:\n  github: {settings: {app_id: 1, private_key_file: /k.pem}}\n")
	r, err := expand(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Credentials) != 1 || r.Credentials[0].Env != "GH_TOKEN" || r.Credentials[0].Integration != "" {
		t.Errorf("credentials %+v", r.Credentials)
	}
	runnerFile(t, "credentials:\n  github:\n    env: GH_TOKEN\n    hosts: [api.github.com]\n    auth: {scheme: bearer}\nintegrations:\n  github: {settings: {app_id: 1}}\n")
	if _, err := expand(t); err == nil || !strings.Contains(err.Error(), "settings.private_key_file is required") {
		t.Errorf("settings of a shadowed integration: %v", err)
	}
}

// TestAnUnknownRoleIsLeftAlone expands the credential role of a program that plays
// another as well, and refuses a program that plays none qory knows, since it would
// define nothing.
func TestAnUnknownRoleIsLeftAlone(t *testing.T) {
	hermetic(t)
	dir := onPath(t)
	fakeIntegration(t, dir, "qory-tracker", strings.Replace(fixture(t, "acme-tracker.json"), `"roles": {`, `"roles": {"work_source": {"events": ["issue.opened"]},`, 1))
	runnerFile(t, "integrations:\n  tracker: {}\n")
	if r, err := expand(t); err != nil || len(r.Credentials) != 1 || r.Credentials[0].Name != "tracker" {
		t.Errorf("a role beside the credential: %+v, %v", r, err)
	}
	fakeIntegration(t, dir, "qory-queue", `{"version": 1, "name": "queue", "title": "Queue", "program_version": "1", "settings": {"type": "object"}, "roles": {"work_source": {}, "output": {}}}`)
	path := runnerFile(t, "integrations:\n  queue:\n")
	if _, err := expand(t); err == nil || err.Error() != path+": integrations.queue: qory-queue plays no role qory knows, output, work_source, and would define nothing" {
		t.Errorf("no known role: %v", err)
	}
}

// TestExpandRefusesWhatDoesNotDescribe is every program that gives no description:
// one not found, one that fails with its line on standard error, one that prints what
// is not JSON, one whose description the contract refuses, and settings the description
// refuses, a secret among them, named without its value.
func TestExpandRefusesWhatDoesNotDescribe(t *testing.T) {
	hermetic(t)
	dir := onPath(t)
	failing := filepath.Join(dir, "qory-failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho 'the key file /k.pem is readable by others' >&2\necho 'a second line' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeIntegration(t, dir, "qory-garbled", "{\"version\": 1,")
	fakeIntegration(t, dir, "qory-v2", strings.Replace(fixture(t, "acme-tracker.json"), `"version": 1,`, `"version": 2,`, 1))
	fakeIntegration(t, dir, "qory-github", fixture(t, "github.json"))
	for _, c := range []struct{ body, want string }{
		{"integrations:\n  absent: {}\n", "integrations.absent: qory-absent is not on the PATH; install it there, or name the program by its path with program"},
		{"integrations:\n  x: {program: /nonexistent/acme-x}\n", "integrations.x: /nonexistent/acme-x is not a program this user may run"},
		{"integrations:\n  failing: {}\n", "integrations.failing: " + failing + " describe: exit status 3: the key file /k.pem is readable by others"},
		{"integrations:\n  garbled: {}\n", "describe did not print one JSON document"},
		{"integrations:\n  v2: {}\n", "describe printed a description the integration contract refuses: at '/version': value must be 1"},
		{"integrations:\n  github: {settings: {app_id: 1, private_key_file: /k.pem, private_key: NOT-A-REAL-KEY}}\n", "integrations.github: settings.private_key is a secret, and the settings go on a command line; give private_key_file, a file that holds it, in its place"},
		{"integrations:\n  github: {settings: {app_id: NOT A VALID ID, private_key_file: /k.pem}}\n", "integrations.github: the settings are not what github takes: settings.app_id breaks the schema's pattern"},
		{"integrations:\n  github: {settings: {app_id: 1, private_key_file: /k.pem, permissions: {contents: NOT-A-LEVEL}}}\n", "settings.permissions.contents breaks the schema's enum"},
	} {
		path := runnerFile(t, c.body)
		_, err := expand(t)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path+": ") {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "NOT") || strings.Contains(err.Error(), "a second line") {
			t.Errorf("%q: the error carries more than it should: %v", c.body, err)
		}
	}
}

// TestIntegrationsSectionRefusesAMistake is every refusal of the section as written,
// before any program runs.
func TestIntegrationsSectionRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"integrations: [github]\n", "integrations is a mapping from a name to an integration"},
		{"integrations: {acme.tracker: {}}\n", `integrations: "acme.tracker" is not 1 to 64 of a-z, 0-9, underscore and dash`},
		{"integrations: {GitHub: {}}\n", `integrations: "GitHub" is not 1 to 64`},
		{"integrations:\n  github: {}\n  github: {}\n", "integrations.github is declared twice"},
		{"integrations: {github: qory-github}\n", "integrations.github is a mapping: program and settings"},
		{"integrations: {github: {command: /x}}\n", `integrations.github: key "command" is not one`},
		{"integrations: {github: {program: bin/qory-github}}\n", `integrations.github.program "bin/qory-github" is not an absolute path`},
		{"integrations: {github: {program: 7}}\n", "integrations.github.program is a path or a name on the PATH"},
		{"integrations: {github: {settings: [a]}}\n", "integrations.github.settings is a mapping, the settings document"},
		{"integrations: {github: {settings: {a: 1, a: 2}}}\n", "integrations.github.settings.a is given twice"},
		{"integrations: {github: {settings: {1: x}}}\n", "integrations.github.settings: line 1: a key that is not a string"},
		{"integrations: {github: {settings: {ratio: .nan}}}\n", "integrations.github.settings.ratio is not a number JSON holds"},
		{"integrations: {github: {settings: {a: &b {x: 1}, c: *b}}}\n", "integrations.github.settings.c: line 1: *b is a YAML alias"},
		{"integrations: {github: {settings: {<<: {app_id: 1}}}}\n", "a YAML alias or merge, which the settings may not hold"},
	} {
		path := runnerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestTheRunnerSchemaTakesTheIntegrationsSection holds runner.schema.json to what the
// reader takes: the declarations of the docs pass it, and a key or an entry the reader
// refuses fails it.
func TestTheRunnerSchemaTakesTheIntegrationsSection(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "runner.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"integrations:\n  github:\n    settings: {app_id: 123456, private_key_file: /etc/qory/github-app.pem, permissions: {contents: write}}\n  tracker:\n    program: /opt/acme/bin/acme-tracker\n  board:\n", true},
		{"integrations: {tracker: {program: acme-tracker, settings: {}}}\n", true},
		{"integrations: {acme.tracker: {}}\n", false},
		{"integrations: {github: {command: /x}}\n", false},
		{"integrations: {github: {program: bin/qory-github}}\n", false},
		{"integrations: {github: {settings: [a]}}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}
