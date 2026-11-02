package module

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

// TestReadManifestReadsRolesAndProvides is a manifest with roles and provides by kind,
// then by role: both are read as written, and a manifest with neither has nil for both.
func TestReadManifestReadsRolesAndProvides(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: typescript\nroles:\n  agents:\n    reviewer: one\nprovides:\n  agents:\n    developer: typescript-developer\n  skills:\n    lint: eslint\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (stack.Roles{"agents": {"reviewer": "one"}}); !reflect.DeepEqual(m.Roles, want) {
		t.Errorf("roles = %v, want %v", m.Roles, want)
	}
	if want := (stack.Roles{"agents": {"developer": "typescript-developer"}, "skills": {"lint": "eslint"}}); !reflect.DeepEqual(m.Provides, want) {
		t.Errorf("provides = %v, want %v", m.Provides, want)
	}
	m, err = ReadManifest(tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n"}))
	if err != nil || m.Roles != nil || m.Provides != nil {
		t.Errorf("a manifest without either: %+v, %v", m, err)
	}
}

// TestReadManifestRefusesRolesItDoesNotRead is each roles and provides value qory does
// not read: many, a list, a flat <kind>/<role> key and an unknown kind. Each refusal
// starts with its fix and contains the manifest's path, with no path in front, and a
// name with an access key secret is printed as Forager's phrase.
func TestReadManifestRefusesRolesItDoesNotRead(t *testing.T) {
	const head = "apiVersion: qory.dev/v1alpha1\nname: typescript\n"
	for _, c := range []struct{ body, want string }{
		{"roles:\n  agents:\n    developer: many\n", `Set roles.agents.developer to one in {path}: a role is filled by one entry, and the value is "many".`},
		{"roles:\n  agents:\n    developer: [one, one]\n", "Set roles.agents.developer to one in {path}: a role is filled by one entry, and the value is a list."},
		{"provides:\n  agents:\n    developer: [a, b]\n", "Write one name for provides.agents.developer in {path}: a role is filled by one entry, and the value is a list."},
		{"provides:\n  agents/developer: typescript-developer\n", "Write provides as provides.agents.developer: typescript-developer in {path}: provides keys are kinds, and agents/developer is a kind and a role."},
		{"provides:\n  mcp:\n    a: b\n", `Use agents, commands or skills as a kind under provides in {path}: "mcp" is none of them.`},
		{"provides:\n  agents:\n    developer: qak_/x\n", "Set provides.agents.developer in {path} to an entry's name"},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.body})
		_, err := ReadManifest(dir)
		want := strings.ReplaceAll(c.want, "{path}", filepath.Join(dir, ManifestName))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v\nwant %q", c.body, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "qak_") {
			t.Errorf("%q: the refusal contains the secret: %v", c.body, err)
		}
	}
}
