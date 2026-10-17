package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/integration"
)

// The fixtures under testdata/fixtures are contracts/integration/v1/fixtures of
// github.com/qoryai/integrations, copied unchanged with the schema: the ones of v0.3.0,
// which the contract as of commit 952eec3 keeps as they were; three under invalid that
// break the bounds of every value, an array of 1025 entries, an object of 1025 members
// and a string of 16385 characters; and the three under refused-by-reader, which the
// schema accepts once they are read and a reader refuses on their raw bytes.
// unknown-role.json plays acme_role as the role qory does not know.

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
	if len(valid) != 4 || len(invalid) != 12 {
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
	if err != nil || strings.Join(d.Roles, " ") != "acme_role credential" || d.Name != "acme-tracker" || d.ProgramVersion != "0.2.0" {
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
		{`{"app_id":123456,"private_key":"PEM-NOT-A-REAL-KEY"}`, "settings.private_key is a secret, and the settings go on a command line; set private_key_file, the path of a file that contains it, in its place"},
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
// the same JSON and leaves no $ in the word for the gateway to replace.
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
	_, err = integration.Describe(context.Background(), script(t, fmt.Sprintf("head -c %d /dev/zero\n", integration.MaxDescription+1)))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("describe printed more than %d bytes", integration.MaxDescription)) {
		t.Errorf("more than the cap: %v", err)
	}
	if _, err := integration.Describe(context.Background(), script(t, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' ' '\necho '{}'\n", integration.MaxDescription-10))); err == nil || !strings.Contains(err.Error(), "a description the integration contract refuses") {
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

// TestADescriptionThatNamesItsDomainsIsRead describes qory-github as it describes itself,
// with the domains it works with, and a machine's own program that names two: each reads
// as one that names none, since a declared integration expands the same whatever its
// domains.
func TestADescriptionThatNamesItsDomainsIsRead(t *testing.T) {
	for _, f := range []string{"testdata/fixtures/github.json", "testdata/fixtures/acme-chat.json"} {
		doc, _ := os.ReadFile(f)
		if !strings.Contains(string(doc), `"domains"`) {
			t.Fatalf("%s names no domains", f)
		}
		d, err := integration.Describe(context.Background(), program(t, string(doc)))
		if err != nil || d.Credential == nil {
			t.Errorf("%s: %+v, %v", f, d, err)
		}
	}
}

// TestDescribeNeverReceivesTheAccessKey runs describe with the access key's variables and
// the run key's in qory's environment: the program receives none of them.
func TestDescribeNeverReceivesTheAccessKey(t *testing.T) {
	for _, name := range []string{"QORY_ACCESS_KEY_SECRET", "QORY_ACCESS_KEY_ID", "QORY_APIARY_PUBLIC_KEY", "QORY_RUN_KEY"} {
		t.Setenv(name, "set")
	}
	t.Setenv("QORY_TEST_KEPT", "kept")
	doc, _ := os.ReadFile("testdata/fixtures/acme-tracker.json")
	body := "test -z \"$QORY_ACCESS_KEY_SECRET$QORY_ACCESS_KEY_ID$QORY_APIARY_PUBLIC_KEY$QORY_RUN_KEY\" || exit 7\ntest \"$QORY_TEST_KEPT\" = kept || exit 8\ncat <<'EOF'\n" + string(doc) + "\nEOF\n"
	if _, err := integration.Describe(context.Background(), script(t, body)); err != nil {
		t.Errorf("describe: %v", err)
	}
}

// printing writes a program that prints out, byte for byte, and exits 0.
func printing(t *testing.T, out []byte) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "description")
	if err := os.WriteFile(file, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return script(t, "cat '"+file+"'\n")
}

// TestADescriptionIsAtMost256KiB reads a description of exactly 262144 bytes, the most
// the integration contract takes, and refuses one byte more by its size alone.
func TestADescriptionIsAtMost256KiB(t *testing.T) {
	if integration.MaxDescription != 262144 {
		t.Fatalf("the cap is %d bytes, want 262144", integration.MaxDescription)
	}
	doc, err := os.ReadFile("testdata/fixtures/acme-tracker.json")
	if err != nil {
		t.Fatal(err)
	}
	padded := func(n int) []byte { return append(bytes.Clone(doc), bytes.Repeat([]byte(" "), n-len(doc))...) }
	if d, err := integration.Describe(context.Background(), printing(t, padded(262144))); err != nil || d.Name != "acme-tracker" {
		t.Errorf("262144 bytes: %+v, %v", d, err)
	}
	_, err = integration.Describe(context.Background(), printing(t, padded(262145)))
	if err == nil || !strings.HasSuffix(err.Error(), " describe printed more than 262144 bytes; a description is a few kilobytes") {
		t.Errorf("262145 bytes: %v", err)
	}
}

// TestTheContractsBoundsFixturesAreRefused describes the contract's three fixtures past
// its bounds: the schema refuses the array of 1025 entries and the object of 1025
// members, and the string of 16385 characters, 16385 bytes, is refused by its bytes,
// which are checked before the schema.
func TestTheContractsBoundsFixturesAreRefused(t *testing.T) {
	for f, want := range map[string]string{
		"description-settings-enum-1025-entries.json":       "a description the integration contract refuses: at '': 'allOf' failed; at '/settings/properties/level/enum': maxItems: got 1,025, want 1,024",
		"description-settings-properties-1025-members.json": "a description the integration contract refuses: at '': 'allOf' failed; at '/settings/properties': maxProperties: got 1,025, want 1,024",
		"description-string-16385-characters.json":          "a description the integration contract refuses: the string at /description is more than 16384 bytes",
	} {
		doc, err := os.ReadFile(filepath.Join("testdata/fixtures/invalid", f))
		if err != nil {
			t.Fatal(err)
		}
		_, err = integration.Describe(context.Background(), program(t, string(doc)))
		if err == nil || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", f, err, want)
		}
	}
}

// bounded is a description of 12 values whose settings schema's default is fill: it
// holds 12 values and as many as fill holds, and nests 4 deep, or 2 and as deep as fill
// nests when that is deeper.
func bounded(fill string) string {
	return `{"version": 1, "name": "acme-tracker", "title": "Acme tracker", "program_version": "0.1.0", "settings": {"type": "object", "default": ` + fill + `}, "roles": {"credential": {"argument": "[A-Z]+", "hosts": ["tracker.acme.example"]}}}`
}

// values is an array that holds n values, itself included, n at least 1: arrays of at
// most 1000 zeros, within the schema's bounds.
func values(n int) string {
	var parts []string
	for n--; n > 0; {
		k := min(n-1, 1000)
		parts = append(parts, "["+strings.TrimSuffix(strings.Repeat("0,", k), ",")+"]")
		n -= k + 1
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TestADescriptionIsHeldToTheContractsLimits describes a description at each limit the
// integration contract states and its schema cannot express, which is read, and one
// past it, which is refused with where, never what: a string and a member name of 16384
// bytes of UTF-8, under 16384 characters, and of 16385; 4096 values and 4097; and
// arrays or objects 64 deep and 65.
func TestADescriptionIsHeldToTheContractsLimits(t *testing.T) {
	refuses := " describe printed a description the integration contract refuses: "
	under := strings.Repeat("é", 8192) // 16384 bytes, 8192 characters
	over := under + "a"                // 16385 bytes, 8193 characters
	described := func(desc string) string {
		return strings.Replace(bounded("0"), `"title": "Acme tracker"`, `"title": "Acme tracker", "description": "`+desc+`"`, 1)
	}
	named := func(name string) string {
		return strings.Replace(bounded("0"), `"type": "object", "default": 0`, `"type": "object", "properties": {"`+name+`": {"description": "MARKED"}}`, 1)
	}
	nested := func(open, close string, n int) string { return strings.Repeat(open, n) + strings.Repeat(close, n) }
	for _, c := range []struct{ name, doc, want string }{
		{"a string of 16384 bytes", described(under), ""},
		{"a string of 16385 bytes", described(over), "the string at /description is more than 16384 bytes"},
		{"a member name of 16384 bytes", named(under), ""},
		{"a member name of 16385 bytes", named(over), "the member name at " + integration.Printable("/settings/properties/"+over) + " is more than 16384 bytes"},
		{"a string in an array, under a name with / and ~", bounded(`{"a/b~": [0, "` + over + `"]}`), "the string at /settings/default/a~1b~0/1 is more than 16384 bytes"},
		{"4096 values", bounded(values(4096 - 12)), ""},
		{"4097 values", bounded(values(4097 - 12)), "it holds more than 4096 values"},
		{"arrays 64 deep", bounded(nested("[", "]", 62)), ""},
		{"arrays 65 deep", bounded(nested("[", "]", 63)), "it nests arrays and objects more than 64 deep"},
		{"objects 64 deep", bounded(strings.Repeat(`{"a":`, 62) + "0" + strings.Repeat("}", 62)), ""},
		{"objects 65 deep", bounded(strings.Repeat(`{"a":`, 63) + "0" + strings.Repeat("}", 63)), "it nests arrays and objects more than 64 deep"},
		// One refusal, of the first limit broken: the values, the depth, then the strings,
		// and each before the schema.
		{"every limit", strings.Replace(described(over), `"default": 0`, `"default": [`+nested("[", "]", 63)+","+values(4097)+"]", 1), "it holds more than 4096 values"},
		{"the depth and a string", strings.Replace(described(over), `"default": 0`, `"default": `+nested("[", "]", 63), 1), "it nests arrays and objects more than 64 deep"},
		// Members are visited in the order of their names, not as they were printed.
		{"two strings", strings.Replace(described(over), `"title": "Acme tracker"`, `"title": "`+over+`"`, 1), "the string at /description is more than 16384 bytes"},
	} {
		prog := program(t, c.doc)
		_, err := integration.Describe(context.Background(), prog)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || err.Error() != prog+refuses+c.want):
			t.Errorf("%s: %v, want %q", c.name, err, prog+refuses+c.want)
		case err != nil && (strings.Contains(err.Error(), "MARKED") || strings.Contains(err.Error(), "éa") || strings.Count(err.Error(), "é") > 200):
			t.Errorf("%s: the refusal carries a value: %.300v", c.name, err)
		}
	}
}

// TestASourceRoleIsReadAsTheContractHasIt describes a program that plays the source
// role as the contract defines it, which is read with the role and no credential, and
// one whose source role misses a field or has one the contract does not define, which
// is refused.
func TestASourceRoleIsReadAsTheContractHasIt(t *testing.T) {
	source := `{"description": "Reads the open items of Acme Tracker projects.", "program": "qory-acme-tracker", "setup": {"command": "qory-acme-tracker setup", "help": "Asks for the address of the Acme Tracker site."}, "start": "qory-acme-tracker serve", "connect": "Acme Tracker asks you to sign in.", "access": "In Acme Tracker, open Settings to change the projects.", "disconnect": "In Acme Tracker, open Settings and remove the adapter."}`
	doc := func(role string) string {
		return `{"version": 1, "name": "acme-tracker", "title": "Acme tracker", "program_version": "0.1.0", "settings": {"type": "object"}, "roles": {"source": ` + role + `}}`
	}
	d, err := integration.Describe(context.Background(), program(t, doc(source)))
	if err != nil || strings.Join(d.Roles, " ") != "source" || d.Credential != nil {
		t.Errorf("a source role: %+v, %v", d, err)
	}
	for _, c := range []struct{ role, want string }{
		{strings.Replace(source, `"start": "qory-acme-tracker serve", `, "", 1), "at '/roles/source': missing property 'start'"},
		{strings.Replace(source, `"program"`, `"button": "Connect", "program"`, 1), "at '/roles/source': additional properties 'button' not allowed"},
	} {
		_, err := integration.Describe(context.Background(), program(t, doc(c.role)))
		if err == nil || !strings.HasSuffix(err.Error(), "a description the integration contract refuses: "+c.want) {
			t.Errorf("%s: %v, want %q", c.role, err, c.want)
		}
	}
}

// TestADescriptionReadsAsQoryApiaryReadsIt refuses what Qory Apiary's reader refuses and
// Go's reads otherwise: invalid UTF-8, a \u escape of a lone surrogate in a string or a
// member name, and an object that names a member twice, compared as the names read,
// each refused before the values are counted and before the schema, the first in the
// document, with where and never what. A surrogate pair, an escaped backslash before u,
// and one name in two objects are read.
func TestADescriptionReadsAsQoryApiaryReadsIt(t *testing.T) {
	notOne := " describe did not print one JSON document: "
	refuses := " describe printed a description the integration contract refuses: "
	described := func(desc string) string {
		return strings.Replace(bounded("0"), `"title": "Acme tracker"`, `"title": "Acme tracker", "description": "`+desc+`"`, 1)
	}
	over := strings.Repeat("é", 8192) + "a"
	// u is a backslash and u, which start a \u escape in the JSON a test prints.
	u := string([]byte{0x5c, 'u'})
	lone := notOne + "the string at /description holds a " + u + " escape of a lone surrogate"
	for _, c := range []struct{ name, doc, want string }{
		{"a surrogate pair", described("VALUE-MARK " + u + "d83d" + u + "de00"), ""},
		{"a character past U+FFFF", described("VALUE-MARK " + string(rune(0x1F600))), ""},
		{"an escaped backslash before u", described("VALUE-MARK " + string([]byte{0x5c}) + u + "d800"), ""},
		{"one name in two objects", bounded(`{"x": {"a": 1}, "y": {"a": 1, "b": [{"a": 1}, {"a": 2}]}}`), ""},
		{"a lone high surrogate at the end", described("VALUE-MARK " + u + "d800"), lone},
		{"a high surrogate before another escape", described("VALUE-MARK " + u + "d83d" + u + "0041"), lone},
		{"two high surrogates before a low one", described("VALUE-MARK " + u + "d83d" + u + "d83d" + u + "de00"), lone},
		{"a lone low surrogate", described("VALUE-MARK " + u + "dc00"), lone},
		{"a lone surrogate in an array", bounded(`[0, {"k": ["VALUE-MARK ` + u + `d800"]}]`), notOne + "the string at /settings/default/1/k/0 holds a " + u + " escape of a lone surrogate"},
		{"a lone surrogate in a member name", bounded(`{"x": {"a/` + u + `dfff": "VALUE-MARK"}}`), notOne + "the member name at " + integration.Printable("/settings/default/x/a~1"+string(rune(0xFFFD))) + " holds a " + u + " escape of a lone surrogate"},
		{"a name twice in a nested object", bounded(`{"x": {"a": 1, "b": "VALUE-MARK", "a": 2}}`), refuses + "the object at /settings/default/x names a twice"},
		{"a name twice, once escaped", bounded(`{"x": [{"a": 1, "` + u + `0061": "VALUE-MARK"}]}`), refuses + "the object at /settings/default/x/0 names a twice"},
		{"a name twice in the document itself", strings.Replace(bounded("0"), `"title": "Acme tracker"`, `"title": "Acme tracker", "title": "VALUE-MARK"`, 1), refuses + "the object at / names title twice"},
		// The first in the document is refused, and before the values and the strings.
		{"a name twice before a lone surrogate", bounded(`{"a": 1, "a": 2, "z": "VALUE-MARK ` + u + `d800"}`), refuses + "the object at /settings/default names a twice"},
		{"a lone surrogate before a name twice", bounded(`{"a": "VALUE-MARK ` + u + `d800", "b": 1, "b": 2}`), notOne + "the string at /settings/default/a holds a " + u + " escape of a lone surrogate"},
		{"a name twice and 4097 values", bounded(`{"a": ` + values(4097) + `, "a": 0}`), refuses + "the object at /settings/default names a twice"},
		{"a lone surrogate in a string too long", described(over + u + "d800"), lone},
	} {
		prog := program(t, c.doc)
		_, err := integration.Describe(context.Background(), prog)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || err.Error() != prog+c.want):
			t.Errorf("%s: %.300v, want %q", c.name, err, prog+c.want)
		case err != nil && (strings.Contains(err.Error(), "VALUE-MARK") || strings.Contains(err.Error(), "é")):
			t.Errorf("%s: the refusal carries a value: %.300v", c.name, err)
		}
	}
	// Invalid UTF-8, a byte that starts no character or a surrogate written as UTF-8
	// in three bytes, is refused before the JSON is read, and before a name twice.
	for _, bad := range [][]byte{{0xff}, {0xed, 0xa0, 0x80}} {
		doc := strings.Replace(described("VALUE-MARK "+string(bad)), `"version": 1`, `"version": 1, "version": 1`, 1)
		prog := printing(t, []byte(doc))
		_, err := integration.Describe(context.Background(), prog)
		if err == nil || err.Error() != prog+notOne+"it is not valid UTF-8" {
			t.Errorf("% x: %v", bad, err)
		}
	}
}

// TestTheScanOfADescriptionIsLinear scans descriptions of 256 KiB built to cost the
// most: many small objects, one object of many names, one name and one string of many
// escapes, and arrays nested deep. Each is read once, each object holds its names
// only while it is open, and none takes long.
func TestTheScanOfADescriptionIsLinear(t *testing.T) {
	const size = 262144
	fill := func(head, unit, sep, tail string) []byte {
		var b strings.Builder
		b.WriteString(head)
		for i := 0; b.Len() < size-len(tail)-len(unit)-len(sep)-16; i++ {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(strings.ReplaceAll(unit, "#", strconv.Itoa(i)))
		}
		b.WriteString(tail)
		return []byte(b.String())
	}
	// A pair, an escaped backslash, and an escaped a.
	u := string([]byte{0x5c, 'u'})
	escapes := u + "d83d" + u + "de00" + string([]byte{0x5c, 0x5c}) + u + "0061"
	for name, doc := range map[string][]byte{
		"many objects":         fill(`{"a": [`, `{"k#": 0, "j": [1, "x"]}`, ",", `]}`),
		"one object of names":  fill(`{`, `"k#": 0`, ",", `}`),
		"a name of escapes":    fill(`{"`, escapes, "", `": 0}`),
		"a string of escapes":  fill(`{"a": "`, escapes, "", `"}`),
		"arrays nested deep":   []byte(`{"a": ` + strings.Repeat("[", 9000) + strings.Repeat("]", 9000) + `}`),
		"objects nested deep":  []byte(strings.Repeat(`{"a": `, 9000) + "0" + strings.Repeat("}", 9000)),
		"many repeated levels": fill(`[`, `{"a": {"a": {"a": 0}}}`, ",", `]`),
	} {
		if !json.Valid(doc) || len(doc) > size {
			t.Fatalf("%s: %d bytes, valid %v", name, len(doc), json.Valid(doc))
		}
		began := time.Now()
		if err := integration.Scan("describe", doc); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if took := time.Since(began); took > 5*time.Second {
			t.Errorf("%s: the scan took %s", name, took)
		}
	}
}

// TestTheContractsReaderFixturesAreRefused describes the contract's three fixtures the
// schema accepts once they are read and a reader refuses on their raw bytes, each
// printed byte for byte: invalid UTF-8, a lone surrogate escape and a repeated member.
func TestTheContractsReaderFixturesAreRefused(t *testing.T) {
	u := string([]byte{0x5c, 'u'})
	want := map[string]string{
		"description-string-invalid-utf-8.json":  " describe did not print one JSON document: it is not valid UTF-8",
		"description-string-lone-surrogate.json": " describe did not print one JSON document: the string at /title holds a " + u + " escape of a lone surrogate",
		"description-member-repeated.json":       " describe printed a description the integration contract refuses: the object at /settings names type twice",
	}
	fixtures, _ := filepath.Glob("testdata/fixtures/refused-by-reader/*.json")
	if len(fixtures) != len(want) {
		t.Fatalf("fixtures %v", fixtures)
	}
	for _, f := range fixtures {
		doc, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		prog := printing(t, doc)
		_, err = integration.Describe(context.Background(), prog)
		if w, ok := want[filepath.Base(f)]; !ok || err == nil || err.Error() != prog+w {
			t.Errorf("%s: %v, want %q", f, err, prog+w)
		}
	}
}
