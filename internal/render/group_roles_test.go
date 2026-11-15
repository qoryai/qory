package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestBuildWritesAGroupRole is a harness whose factory module declares the group role
// reviewers and adds two agents to it, and whose python module adds a third with
// frontmatter paths written as a block list, its items at the key's indentation. The
// home has roles.json with one row per member and the python reviewer's paths; a
// skill's reference to the group resolves in its body to the members' names joined by
// ", ", at each path's address, in prose, in a code span and in a fenced code block; and
// no rendered agent file has the paths key or a line of its list, while the keys after
// it stay.
func TestBuildWritesAGroupRole(t *testing.T) {
	res, _, home := composeFixtureNamed(t, "group-two-modules", "claude", "opencode")
	if err := render.Build(res, home, lookup(t, "claude"), lookup(t, "opencode")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, render.RolesFile))
	if err != nil {
		t.Fatal(err)
	}
	var got render.Roles
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []render.RoleRow{
		{Role: "agents/reviewers", Agent: "code-reviewer", From: "provides factory", Group: true},
		{Role: "agents/reviewers", Agent: "test-reviewer", From: "provides factory", Group: true},
		{Role: "agents/reviewers", Agent: "python-reviewer", From: "provides python", Group: true, Paths: []string{"**/*.py", "pyproject.toml"}},
	}
	if !reflect.DeepEqual(got.Roles, want) {
		t.Errorf("roles.json:\n%s\nwant %+v", data, want)
	}
	for file, body := range map[string]string{
		"claude/skills/review/SKILL.md":           "Start code-reviewer, test-reviewer, python-reviewer on the change.",
		"claude/plugin/skills/review/SKILL.md":    "Start harness:code-reviewer, harness:test-reviewer, harness:python-reviewer on the change.",
		"opencode/skills/review/SKILL.md":         "Start code-reviewer, test-reviewer, python-reviewer on the change.",
		"claude/skills/review/examples.md":        "The session starts `code-reviewer, test-reviewer, python-reviewer`.\n\n```sh\nstart code-reviewer, test-reviewer, python-reviewer\n```\n",
		"claude/plugin/skills/review/examples.md": "The session starts `harness:code-reviewer, harness:test-reviewer, harness:python-reviewer`.\n\n```sh\nstart harness:code-reviewer, harness:test-reviewer, harness:python-reviewer\n```\n",
	} {
		data, err := os.ReadFile(filepath.Join(home, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), body) {
			t.Errorf("%s:\n%s\nwant a body with %q", file, data, body)
		}
	}
	var agents []string
	err = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == filepath.Join(home, "modules") {
			return filepath.SkipDir
		}
		if strings.HasPrefix(d.Name(), "python-reviewer") {
			agents = append(agents, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code reads the checkout's agents and the plugin's, OpenCode its agents.
	if len(agents) != 3 {
		t.Fatalf("the agent python-reviewer is at %v, want 3 paths", agents)
	}
	for _, file := range agents {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "paths") || strings.Contains(text, "\n- ") || !strings.Contains(text, "description: python-reviewer") {
			t.Errorf("%s keeps the paths key or a line of its list, or lost its description:\n%s", file, data)
		}
		// Claude Code reads the file whole, and the key after paths stays.
		if strings.Contains(file, string(filepath.Separator)+"claude"+string(filepath.Separator)) && !strings.Contains(text, "\ntools: Read\n") {
			t.Errorf("%s lost the key after paths:\n%s", file, data)
		}
	}
}

// TestRolesFileRows is roles.json for a single role and for a group whose members the
// checkout's bind replaced: the single role has one row with no group mark and no paths,
// and the replaced member has no row.
func TestRolesFileRows(t *testing.T) {
	for _, c := range []struct {
		fixture string
		want    []render.RoleRow
	}{
		{"provides-fills-role", []render.RoleRow{{Role: "agents/developer", Agent: "typescript-developer", From: "provides typescript"}}},
		{"group-bind-replaces", []render.RoleRow{
			{Role: "agents/reviewers", Agent: "python-reviewer", From: "bind", Group: true, Paths: []string{"**/*.py", "pyproject.toml"}},
			{Role: "agents/reviewers", Agent: "code-reviewer", From: "bind", Group: true},
		}},
	} {
		got := rolesFile(t, c.fixture)
		if !reflect.DeepEqual(got.Roles, c.want) {
			t.Errorf("%s: roles.json = %+v, want %+v", c.fixture, got.Roles, c.want)
		}
	}
}

// TestGroupReferenceFollowsTheRolesFile is a group whose members a bind replaced, and one
// whose member an exclude left out: the reference in the skill's body lists the members
// in the order of the group's rows in roles.json.
func TestGroupReferenceFollowsTheRolesFile(t *testing.T) {
	for _, fixture := range []string{"group-bind-replaces", "group-exclude-removes-member", "group-two-modules"} {
		res, _, home := composeFixtureNamed(t, fixture, "claude")
		if err := render.Build(res, home, lookup(t, "claude")); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, row := range rolesFileAt(t, home).Roles {
			if row.Role == "agents/reviewers" {
				names = append(names, row.Agent)
			}
		}
		data, err := os.ReadFile(filepath.Join(home, "claude", "skills", "review", "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "Start " + strings.Join(names, ", ") + " on the change."; len(names) < 2 || !strings.Contains(string(data), want) {
			t.Errorf("%s: the skill is\n%s\nwant a body with %q", fixture, data, want)
		}
	}
}

// rolesFile builds the fixture's home for claude and reads its roles.json.
func rolesFile(t *testing.T, fixture string) render.Roles {
	t.Helper()
	res, _, home := composeFixtureNamed(t, fixture, "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	return rolesFileAt(t, home)
}

// rolesFileAt reads the roles.json of the home at home.
func rolesFileAt(t *testing.T, home string) render.Roles {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, render.RolesFile))
	if err != nil {
		t.Fatal(err)
	}
	var got render.Roles
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestBuildWritesNoRolesFileWithoutARole is a harness with no role: the home has no
// roles.json.
func TestBuildWritesNoRolesFileWithoutARole(t *testing.T) {
	res, _, home := composeFixture(t, "claude")
	if len(res.Roles) != 0 {
		t.Fatalf("the fixture has roles: %+v", res.Roles)
	}
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, render.RolesFile)); !os.IsNotExist(err) {
		t.Errorf("a harness without a role has %s: %v", render.RolesFile, err)
	}
}
