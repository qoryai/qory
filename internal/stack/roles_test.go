package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
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
	if !reflect.DeepEqual(p.Bind.One, want) || p.Bind.Many != nil {
		t.Fatalf("bind = %v, want %v", p.Bind, want)
	}
	if got := p.Bind.One.Flat(); !reflect.DeepEqual(got, map[string]string{"agents/coder": "rails-coder", "skills/entry": "implement"}) {
		t.Fatalf("flat = %v", got)
	}
	if p.Bind.Len() != 2 {
		t.Fatalf("len = %d, want 2", p.Bind.Len())
	}
}

// TestBindReadsAListForAGroup is a bind that sets an agents role to a list: the stack
// reads the names in the order written, each once, apart from the roles set to one name.
// Whether the role is a group is the compose's check.
func TestBindReadsAListForAGroup(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, "apiVersion: qory.dev/v1alpha1\nbind:\n  agents:\n    coder: rails-coder\n    reviewers: [test-reviewer, code-reviewer, test-reviewer]\nmodules:\n  - name: core\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Groups{"agents": {"reviewers": {"test-reviewer", "code-reviewer"}}}); !reflect.DeepEqual(p.Bind.Many, want) {
		t.Fatalf("many = %v, want %v", p.Bind.Many, want)
	}
	if want := (Roles{"agents": {"coder": "rails-coder"}}); !reflect.DeepEqual(p.Bind.One, want) {
		t.Fatalf("one = %v, want %v", p.Bind.One, want)
	}
	if p.Bind.Len() != 2 || !p.Bind.Sets("agents", "reviewers") || !p.Bind.Sets("agents", "coder") || p.Bind.Sets("agents", "developer") {
		t.Fatalf("bind = %+v", p.Bind)
	}
}

// TestBindRefusesEachShapeItDoesNotRead is each bind a stack file may write and qory
// does not read: a flat <kind>/<role> key, a kind that is no kind of role, a kind that is
// no map, a list for a kind other than agents, an empty list, a list item that is no
// name, and a value that is no name. Each refusal starts with its fix and contains the
// file's path, with no path in front.
func TestBindRefusesEachShapeItDoesNotRead(t *testing.T) {
	isolate(t)
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nbind:\n"
	// Each text has {path}, the stack file's path as Load was given it.
	for _, c := range []struct{ body, want string }{
		{"  agents/coder: rails-coder\n", "Write bind as bind.agents.coder: rails-coder.\n  The keys of bind are kinds, and agents/coder is a kind and a role.\n  bind is in {path}."},
		{"  agents/coder: [a, b]\n", "Write bind as bind.agents.coder: <name>.\n  The keys of bind are kinds, and agents/coder is a kind and a role.\n  bind is in {path}."},
		{"  coders:\n    a: b\n", "Use agents, commands, or skills as a kind under bind: \"coders\" is none of them.\n  bind is in {path}."},
		{"  agents: rails-coder\n", "Write bind.agents as a map of roles: bind.agents is not a map.\n  bind is in {path}."},
		{"  skills:\n    entry: [a, b]\n", "Write one name for bind.skills.entry.\n  Only an agents role can be a group, and the value is a list.\n  bind is in {path}."},
		{"  agents:\n    reviewers: []\n", "Write one name or more in bind.agents.reviewers.\n  The list is empty.\n  bind is in {path}."},
		{"  agents:\n    reviewers: [a, {b: c}]\n", "Write entry names in bind.agents.reviewers.\n  An entry's name is one path segment with no space, @ or leading dot, and an item is not a name.\n  bind is in {path}."},
		{"  agents:\n    reviewers: [a, b/c]\n", "Write entry names in bind.agents.reviewers.\n  An entry's name is one path segment with no space, @ or leading dot, and an item is \"b/c\".\n  bind is in {path}."},
		{"  agents:\n    developer: {a: b}\n", "Set bind.agents.developer to the name of one entry: the value is empty or not a name.\n  bind is in {path}."},
		{"  agents:\n    developer:\n", "Set bind.agents.developer to the name of one entry: the value is empty or not a name.\n  bind is in {path}."},
		{"  - agents\n", "Write bind as a map of kinds, then roles: bind is not a map.\n  bind is in {path}."},
	} {
		path := write(t, head+c.body)
		_, err := Load(path)
		if want := strings.ReplaceAll(c.want, "{path}", path); err == nil || err.Error() != want {
			t.Errorf("%q: %v\nwant %q", c.body, err, want)
		}
	}
}

// TestBindRefusalsKeepTheFixLineShortUnderALongPath is each shape of bind that qory does
// not read, in a stack file under a directory of 150 characters: the first line is the
// fix, at most 100 characters and with no path, and the last line has the path.
func TestBindRefusalsKeepTheFixLineShortUnderALongPath(t *testing.T) {
	isolate(t)
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nbind:\n"
	for _, body := range []string{
		"  agents/coder: rails-coder\n",
		"  coders:\n    a: b\n",
		"  agents: rails-coder\n",
		"  skills:\n    entry: [a, b]\n",
		"  agents:\n    reviewers: []\n",
		"  agents:\n    reviewers: [a, b/c]\n",
		"  agents:\n    developer: {a: b}\n",
		"  agents:\n    \"a b\": c\n",
		"  - agents\n",
	} {
		dir := filepath.Join(t.TempDir(), strings.Repeat("d", 150))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(head+body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Errorf("%q: no refusal", body)
			continue
		}
		lines := strings.Split(err.Error(), "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 || strings.Contains(lines[0], dir) {
			t.Errorf("%q: the first line has %d characters, or the path: %s", body, n, lines[0])
		}
		if last := lines[len(lines)-1]; !strings.HasSuffix(last, " is in "+path+".") {
			t.Errorf("%q: the last line is %q, want it to end with the path", body, last)
		}
	}
}

// TestReadRolesWithCountsTakesOneOrMany is the roles key of a module's manifest: one is
// read, and so is many for an agents role; many for another kind, another value and a
// list return an error, each with the fix first.
func TestReadRolesWithCountsTakesOneOrMany(t *testing.T) {
	r, _, err := ReadRoles(node(t, "agents:\n  developer: one\n  reviewers: many\n"), "roles", true)
	if err != nil || r["agents"]["developer"] != "one" || r["agents"]["reviewers"] != "many" {
		t.Fatalf("roles = %v, %v", r, err)
	}
	for _, c := range []struct{ body, want string }{
		{"skills:\n  x: many\n", "Set roles.skills.x of the module to one.\n  Only an agents role can be a group. The map is in m.yaml."},
		{"agents:\n  developer: two\n", "Set roles.agents.developer to one or many.\n  One entry fills a role and several fill a group, and the value is \"two\".\n  roles is in m.yaml."},
		{"agents:\n  developer: [one]\n", "Set roles.agents.developer to one or many.\n  One entry fills a role and several fill a group, and the value is a list.\n  roles is in m.yaml."},
	} {
		_, _, err := ReadRoles(node(t, c.body), "roles", true)
		in, ok := err.(exports.InFile)
		if !ok || in.In("m.yaml").Error() != c.want {
			t.Errorf("%q: %v\nwant %q", c.body, err, c.want)
		}
	}
}

// TestCheckRolesChecksABuiltStack is a stack built in code with a role that is no path
// segment: validate returns an error with the file's path and no path in front.
func TestCheckRolesChecksABuiltStack(t *testing.T) {
	err := CheckRoles(Roles{"agents": {"a/b": "c"}}, "bind")
	if err == nil || !strings.HasPrefix(err.(*RolesError).In("qory.yaml").Error(), "Rename role a/b of bind.agents to one path segment") {
		t.Fatalf("err = %v", err)
	}
	if err := CheckRoles(Roles{"agents": {"coder": "rails-coder"}}, "bind"); err != nil {
		t.Fatal(err)
	}
	// A list is checked the way a file's is: only for agents, with one name or more.
	for _, c := range []struct {
		f    Fills
		want string
	}{
		{Fills{Many: Groups{"skills": {"entry": {"a"}}}}, "Write one name for bind.skills.entry.\n  Only an agents role can be a group, and the value is a list.\n  bind is in qory.yaml."},
		{Fills{Many: Groups{"agents": {"reviewers": nil}}}, "Write one name or more in bind.agents.reviewers.\n  The list is empty.\n  bind is in qory.yaml."},
		{Fills{Many: Groups{"agents": {"reviewers": {"a b"}}}}, "Write entry names in bind.agents.reviewers.\n  An entry's name is one path segment with no space, @ or leading dot, and an item is \"a b\".\n  bind is in qory.yaml."},
	} {
		err := CheckFills(c.f, "bind")
		if re, ok := err.(*RolesError); !ok || re.In("qory.yaml").Error() != c.want {
			t.Errorf("%+v: %v\nwant %q", c.f, err, c.want)
		}
	}
	if err := CheckFills(Fills{One: Roles{"agents": {"coder": "rails-coder"}}, Many: Groups{"agents": {"reviewers": {"code-reviewer"}}}}, "bind"); err != nil {
		t.Fatal(err)
	}
	// A role with an access key secret is printed as Forager's phrase.
	err = CheckRoles(Roles{"agents": {"qak_x": "a/b"}}, "bind")
	if err == nil || !strings.Contains(err.Error(), "Set bind.agents."+nameWithSecret+" to an entry's name") || strings.Contains(err.Error(), "qak_") {
		t.Fatalf("err = %v", err)
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
