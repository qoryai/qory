package stack

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestBindIsReadByKindThenRole is a stack file whose bind nests roles under their kinds:
// the stack reads it as written, and Flat keys it the way a reference writes a role.
func TestBindIsReadByKindThenRole(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, "apiVersion: qory.dev/v1alpha1\nbind:\n  agents:\n    coder: rails-coder\n  skills:\n    entry: implement\nmodules:\n  - name: core\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Roles{"agents": {"coder": "rails-coder"}, "skills": {"entry": "implement"}}
	if !reflect.DeepEqual(p.Bind, want) {
		t.Fatalf("bind = %v, want %v", p.Bind, want)
	}
	if got := p.Bind.Flat(); !reflect.DeepEqual(got, map[string]string{"agents/coder": "rails-coder", "skills/entry": "implement"}) {
		t.Fatalf("flat = %v", got)
	}
	if p.Bind.Len() != 2 {
		t.Fatalf("len = %d, want 2", p.Bind.Len())
	}
}

// TestBindRefusesEachShapeItDoesNotRead is each bind a stack file may write and qory
// does not read: a flat <kind>/<role> key, a kind that is no kind of role, a kind that is
// no map, a list as a role's value, and a value that is no name. Each refusal starts with
// its fix and contains the file's name, with no path in front.
func TestBindRefusesEachShapeItDoesNotRead(t *testing.T) {
	isolate(t)
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nbind:\n"
	for _, c := range []struct{ body, want string }{
		{"  agents/coder: rails-coder\n", "Write bind as bind.agents.coder: rails-coder in qory-stack.yaml: bind keys are kinds, and agents/coder is a kind and a role."},
		{"  agents/coder: [a, b]\n", "Write bind as bind.agents.coder: <name> in qory-stack.yaml: bind keys are kinds, and agents/coder is a kind and a role."},
		{"  coders:\n    a: b\n", `Use agents, commands or skills as a kind under bind in qory-stack.yaml: "coders" is none of them.`},
		{"  agents: rails-coder\n", "Write bind.agents in qory-stack.yaml as a map of roles: bind.agents is not a map."},
		{"  agents:\n    developer: [a, b]\n", "Write one name for bind.agents.developer in qory-stack.yaml: a role is filled by one entry, and the value is a list."},
		{"  agents:\n    developer: {a: b}\n", "Set bind.agents.developer in qory-stack.yaml to the name of one entry: the value is empty or not a name."},
		{"  agents:\n    developer:\n", "Set bind.agents.developer in qory-stack.yaml to the name of one entry: the value is empty or not a name."},
		{"  - agents\n", "Write bind in qory-stack.yaml as a map of kinds, then roles: bind is not a map."},
	} {
		_, err := Load(write(t, head+c.body))
		if err == nil || err.Error() != c.want {
			t.Errorf("%q: %v\nwant %q", c.body, err, c.want)
		}
	}
}

// TestReadRolesWithCountsTakesOne is the roles key of a module's manifest: one is read,
// and many and a list are refused, each with the fix first.
func TestReadRolesWithCountsTakesOne(t *testing.T) {
	r, err := ReadRoles(node(t, "agents:\n  developer: one\n"), "roles", true)
	if err != nil || r["agents"]["developer"] != "one" {
		t.Fatalf("roles = %v, %v", r, err)
	}
	for _, c := range []struct{ body, want string }{
		{"agents:\n  developer: many\n", `Set roles.agents.developer to one in m.yaml: a role is filled by one entry, and the value is "many".`},
		{"agents:\n  developer: [one]\n", "Set roles.agents.developer to one in m.yaml: a role is filled by one entry, and the value is a list."},
	} {
		_, err := ReadRoles(node(t, c.body), "roles", true)
		re, ok := err.(*RolesError)
		if !ok || re.In("m.yaml").Error() != c.want {
			t.Errorf("%q: %v\nwant %q", c.body, err, c.want)
		}
	}
}

// TestCheckRolesChecksABuiltStack is a stack built in code with a role that is no path
// segment: validate refuses it with the file's name and no path in front.
func TestCheckRolesChecksABuiltStack(t *testing.T) {
	err := CheckRoles(Roles{"agents": {"a/b": "c"}}, "bind")
	if err == nil || !strings.HasPrefix(err.(*RolesError).In("qory.yaml").Error(), "Rename role a/b of bind.agents in qory.yaml") {
		t.Fatalf("err = %v", err)
	}
	if err := CheckRoles(Roles{"agents": {"coder": "rails-coder"}}, "bind"); err != nil {
		t.Fatal(err)
	}
}

// node is body read as a YAML document's root mapping.
func node(t *testing.T, body string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}
