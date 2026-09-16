package module

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/runner/contracts"
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
		{"a port", "  - api.example.com:443\n", `egress: "api.example.com:443" is not a lower-case host name or a *. suffix`},
		{"a path", "  - api.example.com/v1\n", `egress: "api.example.com/v1" is not`},
		{"a scheme", "  - https://api.example.com\n", `egress: "https://api.example.com" is not`},
		{"upper case", "  - API.example.com\n", `egress: "API.example.com" is not`},
		{"a bare wildcard", "  - \"*\"\n", `egress: "*" is not`},
		{"an empty host", "  - \"\"\n", `egress: "" is not`},
		{"not a string", "  - {host: a}\n", "cannot unmarshal"},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.item})
		_, err := ReadManifest(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestEgressGrammarIsTheRunnersOnce keeps the one definition: the pattern the module
// schema gives a declared host, and the one this package matches, are the runner
// contract's pattern for a policy's allow entry, byte for byte.
func TestEgressGrammarIsTheRunnersOnce(t *testing.T) {
	policy, err := contracts.Document("policy.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	want := policy.(map[string]any)["properties"].(map[string]any)["egress"].(map[string]any)["properties"].(map[string]any)["allow"].(map[string]any)["items"].(map[string]any)["pattern"].(string)
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
		t.Errorf("module.schema.json egress pattern\n%s\nrunner policy allow pattern\n%s", got, want)
	}
	if EgressHost.String() != want {
		t.Errorf("EgressHost\n%s\nrunner policy allow pattern\n%s", EgressHost.String(), want)
	}
}
