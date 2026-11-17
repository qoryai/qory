package module

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/contracts"
)

// TestReadManifestReadsEgress reads the egress key: the hosts sorted and each once, an
// empty list as an empty declaration, and no key as no declaration.
func TestReadManifestReadsEgress(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\negress:\n  - registry.npmjs.org\n  - \"*.github.com\"\n  - api.example.com\n  - registry.npmjs.org\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(m.Egress, " "), "*.github.com api.example.com registry.npmjs.org"; got != want {
		t.Errorf("egress %q, want %q", got, want)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\negress: []\n"})
	if m, err = ReadManifest(dir); err != nil || m.Egress == nil || len(m.Egress) != 0 {
		t.Errorf("an empty declaration read as %v, %v", m.Egress, err)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n"})
	if m, err = ReadManifest(dir); err != nil || m.Egress != nil {
		t.Errorf("no declaration read as %v, %v", m.Egress, err)
	}
}

// TestReadManifestRefusesEgress refuses a host that is not in the grammar: a port, a
// path, a scheme, upper case, a bare wildcard, and an item that is not a string.
func TestReadManifestRefusesEgress(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: core\negress:\n"
	for _, c := range []struct{ name, item, want string }{
		{"a port", "  - api.example.com:443\n", "Set each egress entry to a lower-case host name or a *. suffix.\n  \"api.example.com:443\" is not one, and an entry has no port, path, or scheme."},
		{"a path", "  - api.example.com/v1\n", `"api.example.com/v1" is not one`},
		{"a scheme", "  - https://api.example.com\n", `"https://api.example.com" is not one`},
		{"upper case", "  - API.example.com\n", `"API.example.com" is not one`},
		{"a bare wildcard", "  - \"*\"\n", `"*" is not one`},
		{"an empty host", "  - \"\"\n", `"" is not one`},
		{"not a string", "  - {host: a}\n", "cannot unmarshal"},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.item})
		_, err := ReadManifest(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestEgressGrammarIsTheForagersOnce keeps the one definition: the pattern the module
// schema gives a declared host, and the one this package matches, are the Forager
// contract's pattern for a policy's allow entry, byte for byte.
func TestEgressGrammarIsTheForagersOnce(t *testing.T) {
	policy, err := contracts.Document("policy.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc := policy.(map[string]any)
	items := doc["properties"].(map[string]any)["egress"].(map[string]any)["properties"].(map[string]any)["allow"].(map[string]any)["items"].(map[string]any)
	// Forager's schema may define the entry once, under $defs, for egress.allow and for
	// a run configuration's stack_hosts.
	if ref, ok := items["$ref"].(string); ok {
		items = doc["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
	}
	want := items["pattern"].(string)
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "harness", "v1", "module.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	got := schema["properties"].(map[string]any)["egress"].(map[string]any)["items"].(map[string]any)["pattern"].(string)
	if got != want {
		t.Errorf("module.schema.json egress pattern\n%s\nForager policy allow pattern\n%s", got, want)
	}
	if EgressHost.String() != want {
		t.Errorf("EgressHost\n%s\nForager policy allow pattern\n%s", EgressHost.String(), want)
	}
}
