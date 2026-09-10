package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const linked = `apiVersion: qory.ai/v1alpha1
target:
  runtime: claude
modules:
  - name: tree
    source: {path: modules/tree}
    link: harness
extensions:
  acme:
    sweep_floor: 160
    corpus_roots: [scripts]
`

func load(t *testing.T, body string) (*Stack, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// TestLoadReadsALinkAndExtensions is a module linked as harness and an extension block: both
// come back as written.
func TestLoadReadsALinkAndExtensions(t *testing.T) {
	p, err := load(t, linked)
	if err != nil {
		t.Fatal(err)
	}
	if p.Modules[0].Link != "harness" {
		t.Errorf("link = %q", p.Modules[0].Link)
	}
	if p.Extensions["acme"]["sweep_floor"] != 160 || len(p.Extensions["acme"]["corpus_roots"].([]any)) != 1 {
		t.Errorf("extensions = %v", p.Extensions)
	}
}

// TestLoadRefusesABadLinkOrExtension is a link with a slash, a leading dot, the qory
// directory's name, one name linked by two modules, and an extension that is not a map.
func TestLoadRefusesABadLinkOrExtension(t *testing.T) {
	for _, c := range []struct{ edit, want string }{
		{"link: tools/bin", `module tree: link "tools/bin" is not one path segment`},
		{"link: .harness", `module tree: link ".harness" is not one path segment`},
		{"link: .qory", `module tree: link ".qory" is not one path segment`},
		{"link: harness\n  - name: other\n    source: {path: modules/other}\n    link: harness", "module tree and module other both link harness"},
	} {
		_, err := load(t, strings.Replace(linked, "link: harness", c.edit, 1))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.edit, err, c.want)
		}
	}
	_, err := load(t, strings.Replace(linked, "  acme:\n    sweep_floor: 160\n    corpus_roots: [scripts]\n", "  acme: 160\n", 1))
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal !!int `160` into map") {
		t.Errorf("a scalar extension: err = %v", err)
	}
	_, err = load(t, strings.Replace(linked, "  acme:\n    sweep_floor: 160\n    corpus_roots: [scripts]\n", "  acme:\n", 1))
	if err == nil || !strings.Contains(err.Error(), "extensions.acme is empty") {
		t.Errorf("an empty extension: err = %v", err)
	}
}
