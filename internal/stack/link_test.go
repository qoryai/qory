package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const linked = `apiVersion: qory.dev/v1alpha1
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
	if !reflect.DeepEqual(p.Extensions["acme"], map[string]any{"sweep_floor": 160, "corpus_roots": []any{"scripts"}}) {
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
	_, err := load(t, strings.Replace(linked, "extensions:\n  acme:\n", "extensions: [scripts]\n  acme:\n", 1))
	if err == nil {
		t.Errorf("a list for extensions: loaded, want a refusal")
	}
}

// TestLoadReadsExtensionsOfAnyShape is an extension block of a scalar, a list, a map
// two levels deep and a key with no value: every one loads as written.
func TestLoadReadsExtensionsOfAnyShape(t *testing.T) {
	p, err := load(t, strings.Replace(linked, "  acme:\n    sweep_floor: 160\n    corpus_roots: [scripts]\n", "  sweep_floor: 40\n  corpus_roots: [scripts, infra]\n  resolve_ci:\n    watched: {workflows: [Lint]}\n  later:\n", 1))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"sweep_floor": 40, "corpus_roots": []any{"scripts", "infra"}, "resolve_ci": map[string]any{"watched": map[string]any{"workflows": []any{"Lint"}}}, "later": nil}
	if !reflect.DeepEqual(p.Extensions, want) {
		t.Errorf("extensions = %#v, want %#v", p.Extensions, want)
	}
}
