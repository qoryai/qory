package integration_test

import (
	"context"
	"encoding/json"
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
// of the settings written $, which still reads as the same JSON.
func TestTheCredentialAdapterIsTheContracts(t *testing.T) {
	settings := `{"private_key_file":"/keys/${argument}/$HOME.pem"}`
	got := integration.CredentialAdapter("/usr/local/bin/qory-github", []byte(settings))
	want := []string{"/usr/local/bin/qory-github", "credential", "--settings", `{"private_key_file":"/keys/${argument}/$HOME.pem"}`, "--", "${argument}"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("adapter %q, want %q", got, want)
	}
	var a, b any
	if json.Unmarshal([]byte(got[3]), &a) != nil || json.Unmarshal([]byte(settings), &b) != nil || a.(map[string]any)["private_key_file"] != b.(map[string]any)["private_key_file"] {
		t.Errorf("the escaped settings read as %v, want %v", a, b)
	}
}
