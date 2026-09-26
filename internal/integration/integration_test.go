package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/integration"
)

// The fixtures under testdata/fixtures are contracts/integration/v1/fixtures of
// github.com/qoryai/integrations at 48a655307774073553a3ce9de5bbc30bdf9a09a6, copied
// with the schema.

// program writes a program that answers describe with doc and exits 0.
func program(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "describe")
	body := "#!/bin/sh\ntest \"$1\" = describe || exit 64\ncat <<'EOF'\n" + doc + "\nEOF\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTheSchemaCopyHoldsToTheContractsFixtures describes a program for each fixture:
// every accepted one reads, with its credential role and its roles, and every one the
// contract refuses is refused.
func TestTheSchemaCopyHoldsToTheContractsFixtures(t *testing.T) {
	valid, _ := filepath.Glob("testdata/fixtures/*.json")
	invalid, _ := filepath.Glob("testdata/fixtures/invalid/*.json")
	if len(valid) != 3 || len(invalid) != 6 {
		t.Fatalf("fixtures %v %v", valid, invalid)
	}
	for _, f := range valid {
		doc, _ := os.ReadFile(f)
		d, err := integration.Describe(context.Background(), program(t, string(doc)))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if d.Credential == nil || len(d.Credential.Hosts) == 0 || d.Credential.Argument == "" {
			t.Errorf("%s: credential role %+v", f, d.Credential)
		}
	}
	for _, f := range invalid {
		doc, _ := os.ReadFile(f)
		if _, err := integration.Describe(context.Background(), program(t, string(doc))); err == nil || !strings.Contains(err.Error(), "a description the integration contract refuses") {
			t.Errorf("%s: %v, want a refusal", f, err)
		}
	}
	doc, _ := os.ReadFile("testdata/fixtures/unknown-role.json")
	d, err := integration.Describe(context.Background(), program(t, string(doc)))
	if err != nil || strings.Join(d.Roles, " ") != "credential work_source" || d.Name != "acme-tracker" || d.ProgramVersion != "0.2.0" {
		t.Errorf("unknown-role: %+v, %v", d, err)
	}
}

// TestSettingsAreCheckedWithoutTheirValues checks settings against the GitHub
// description: a document it takes passes, a secret is refused by its name with its
// _file named in its place, and each refusal names the setting and the rule, never
// what was given.
func TestSettingsAreCheckedWithoutTheirValues(t *testing.T) {
	doc, _ := os.ReadFile("testdata/fixtures/github.json")
	d, err := integration.Describe(context.Background(), program(t, string(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CheckSettings([]byte(`{"app_id":123456,"private_key_file":"/etc/qory/github-app.pem","permissions":{"contents":"write"}}`)); err != nil {
		t.Errorf("settings it takes: %v", err)
	}
	for _, c := range []struct{ settings, want string }{
		{`{"app_id":123456,"private_key":"PEM-NOT-A-REAL-KEY"}`, "settings.private_key is a secret, and the settings go on a command line; give private_key_file, a file that holds it, in its place"},
		{`{"private_key_file":"/k.pem"}`, "the settings are not what github takes: settings.app_id is required"},
		{`{"app_id":1,"private_key_file":"/k.pem","region":"SECRET-REGION"}`, "settings.region is not a setting it takes"},
		{`{"app_id":"SECRET VALUE","private_key_file":"/k.pem"}`, "settings.app_id breaks the schema's pattern"},
		{`{"app_id":1,"private_key_file":"/k.pem","permissions":{"contents":"SECRET-LEVEL"}}`, "settings.permissions.contents breaks the schema's enum"},
		{`{"app_id":1,"private_key_file":"/k.pem","installation_id":"SECRET-ID"}`, "settings.installation_id is string, where integer is wanted"},
		{`{"app_id":1}`, "settings.private_key_file is required, or settings.private_key is required"},
		{`{"app_id":1,"private_key_file":"/k.pem","permissions":{"administration":"write"}}`, "settings.permissions.administration is not a setting it takes"},
	} {
		err := d.CheckSettings([]byte(c.settings))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.settings, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PEM") {
			t.Errorf("%s: the error carries a value: %v", c.settings, err)
		}
	}
}

// TestTheCredentialAdapterIsTheContracts is the credential role's adapter: the program,
// credential, --settings and the settings as one word, -- and ${argument}, with every $
// of the settings written as JSON's six-character escape for it, which still reads as
// the same JSON and leaves no $ in the word for the runner to replace.
func TestTheCredentialAdapterIsTheContracts(t *testing.T) {
	escape := string([]byte{'\\', 'u', '0', '0', '2', '4'})
	if integration.DollarEscape != escape {
		t.Fatalf("DollarEscape is %q, want %q", integration.DollarEscape, escape)
	}
	settings := `{"private_key_file":"/keys/${argument}/$HOME.pem","team":"$team"}`
	got := integration.CredentialAdapter("/usr/local/bin/qory-github", []byte(settings))
	word := `{"private_key_file":"/keys/` + escape + `{argument}/` + escape + `HOME.pem","team":"` + escape + `team"}`
	want := []string{"/usr/local/bin/qory-github", "credential", "--settings", word, "--", "${argument}"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adapter %q, want %q", got, want)
	}
	if strings.Contains(got[3], "$") || !bytes.Equal([]byte(got[3]), []byte(word)) || strings.Count(got[3], escape) != 3 {
		t.Errorf("the settings word %q holds a $, or is not %q byte for byte", got[3], word)
	}
	var a, b map[string]any
	if json.Unmarshal([]byte(got[3]), &a) != nil || json.Unmarshal([]byte(settings), &b) != nil || a["private_key_file"] != b["private_key_file"] || a["team"] != "$team" {
		t.Errorf("the escaped settings read as %v, want %v", a, b)
	}
}

// withSettings is the acme-tracker fixture with settings as its settings schema.
func withSettings(t *testing.T, settings string) string {
	t.Helper()
	doc, err := os.ReadFile("testdata/fixtures/acme-tracker.json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(doc), `"settings": {"type": "object"}`, `"settings": `+settings, 1)
}

// TestASecretIsAPropertyOfTheSettingsThemselves takes writeOnly on a property of the
// settings schema's own properties and refuses it anywhere else, wherever the schema
// nests it: under a nested object, behind a $ref into $defs, in an allOf, in items, and
// on the settings schema itself.
func TestASecretIsAPropertyOfTheSettingsThemselves(t *testing.T) {
	if _, err := integration.Describe(context.Background(), program(t, withSettings(t, `{"type": "object", "properties": {"token": {"type": "string", "writeOnly": true}}}`))); err != nil {
		t.Errorf("a secret of the settings themselves: %v", err)
	}
	for _, c := range []struct{ settings, at string }{
		{`{"type": "object", "properties": {"auth": {"type": "object", "properties": {"token": {"type": "string", "writeOnly": true}}}}}`, "/properties/auth/properties/token"},
		{`{"type": "object", "properties": {"auth": {"$ref": "#/$defs/auth"}}, "$defs": {"auth": {"type": "object", "properties": {"token": {"writeOnly": true}}}}}`, "/$defs/auth/properties/token"},
		{`{"type": "object", "allOf": [{"properties": {"token": {"type": "string", "writeOnly": true}}}]}`, "/allOf/0/properties/token"},
		{`{"type": "object", "properties": {"tokens": {"type": "array", "items": {"type": "string", "writeOnly": true}}}}`, "/properties/tokens/items"},
		{`{"type": "object", "if": {"required": ["a"]}, "then": {"properties": {"b": {"writeOnly": true}}}}`, "/then/properties/b"},
		{`{"type": "object", "additionalProperties": {"writeOnly": true}}`, "/additionalProperties"},
		{`{"type": "object", "writeOnly": true}`, "/"},
	} {
		_, err := integration.Describe(context.Background(), program(t, withSettings(t, c.settings)))
		if err == nil || !strings.Contains(err.Error(), "the settings schema marks "+c.at+" writeOnly; a secret is a property of the settings themselves") {
			t.Errorf("%s: %v, want a refusal naming %s", c.settings, err, c.at)
		}
	}
}

// TestTheSettingsSchemaIsDraft2020 takes a settings schema that names draft 2020-12,
// with or without an empty fragment, or no draft, and refuses one that names another,
// quoting it as a terminal takes it.
func TestTheSettingsSchemaIsDraft2020(t *testing.T) {
	for _, settings := range []string{`{"type": "object"}`, `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}`, `{"$schema": "https://json-schema.org/draft/2020-12/schema#", "type": "object"}`} {
		if _, err := integration.Describe(context.Background(), program(t, withSettings(t, settings))); err != nil {
			t.Errorf("%s: %v", settings, err)
		}
	}
	settings := `{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object"}`
	if _, err := integration.Describe(context.Background(), program(t, withSettings(t, settings))); err == nil || !strings.Contains(err.Error(), "the settings schema is written in http://json-schema.org/draft-07/schema#; the integration contract takes draft 2020-12") {
		t.Errorf("draft-07: %v", err)
	}
	// An escape in the $schema is printed as ?, and a long one cut.
	escape := string([]byte{0x5c}) + "u001b"
	settings = `{"$schema": "` + escape + `[31mred` + strings.Repeat("x", 300) + `", "type": "object"}`
	if _, err := integration.Describe(context.Background(), program(t, withSettings(t, settings))); err == nil || !strings.Contains(err.Error(), "written in ?[31mred"+strings.Repeat("x", 192)+"...;") {
		t.Errorf("a $schema with an escape: %v", err)
	}
}

// script writes a program with body after its #! line.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "describe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDescribeKeepsWhatItQuotesShort runs describe in /, refuses a program that prints
// more than the cap, and quotes the first line of a failure's standard error cut to 200
// runes, every character that does not print as ?.
func TestDescribeKeepsWhatItQuotesShort(t *testing.T) {
	doc, _ := os.ReadFile("testdata/fixtures/acme-tracker.json")
	d, err := integration.Describe(context.Background(), script(t, "cat <<EOF\n"+strings.Replace(string(doc), `"0.1.0"`, `"$(pwd)"`, 1)+"\nEOF\n"))
	if err != nil || d.ProgramVersion != "/" {
		t.Errorf("describe ran in %+v, %v; want /", d, err)
	}
	_, err = integration.Describe(context.Background(), script(t, fmt.Sprintf("head -c %d /dev/zero\n", integration.MaxOutput+1)))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("describe printed more than %d bytes", integration.MaxOutput)) {
		t.Errorf("more than the cap: %v", err)
	}
	if _, err := integration.Describe(context.Background(), script(t, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' ' '\necho '{}'\n", integration.MaxOutput-10))); err == nil || !strings.Contains(err.Error(), "a description the integration contract refuses") {
		t.Errorf("just under the cap is read: %v", err)
	}
	_, err = integration.Describe(context.Background(), script(t, "printf 'bad\\033thing\\n' >&2\nexit 1\n"))
	if err == nil || !strings.HasSuffix(err.Error(), "exit status 1: bad?thing") {
		t.Errorf("a control character: %v", err)
	}
	_, err = integration.Describe(context.Background(), script(t, "printf '%0300d\\n' 0 | tr 0 x >&2\nexit 1\n"))
	if err == nil || !strings.HasSuffix(err.Error(), "exit status 1: "+strings.Repeat("x", 200)+"...") {
		t.Errorf("a long line: %v", err)
	}
}

// TestANameOfTheDescriptionIsPrintedAsATerminalTakesIt checks settings against a schema
// whose required and dependentRequired names hold a terminal's escape: the error names
// them with ? in its place.
func TestANameOfTheDescriptionIsPrintedAsATerminalTakesIt(t *testing.T) {
	escape := string([]byte{0x5c}) + "u001b"
	settings := `{"type": "object", "required": ["a` + escape + `[2J"], "dependentRequired": {"x` + escape + `[2J": ["y` + escape + `[2J"]}}`
	d, err := integration.Describe(context.Background(), program(t, withSettings(t, settings)))
	if err != nil {
		t.Fatal(err)
	}
	err = d.CheckSettings([]byte(`{"x` + escape + `[2J": 1}`))
	if err == nil || !strings.Contains(err.Error(), "settings.a?[2J is required") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("required: %v", err)
	}
	if !strings.Contains(err.Error(), "settings.y?[2J is required with settings.x?[2J") {
		t.Errorf("dependentRequired: %v", err)
	}
}
