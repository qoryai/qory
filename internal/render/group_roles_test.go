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
// frontmatter paths. The home has roles.json with one row per member and the python
// reviewer's paths; a skill's reference to the group resolves in its body to the
// members' names joined by ", ", at each path's address; and no rendered agent file has
// the paths key.
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
		"claude/skills/review/SKILL.md":        "Start code-reviewer, test-reviewer, python-reviewer on the change.",
		"claude/plugin/skills/review/SKILL.md": "Start harness:code-reviewer, harness:test-reviewer, harness:python-reviewer on the change.",
		"opencode/skills/review/SKILL.md":      "Start code-reviewer, test-reviewer, python-reviewer on the change.",
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
		if strings.Contains(string(data), "paths") || !strings.Contains(string(data), "description: python-reviewer") {
			t.Errorf("%s keeps the paths key or lost its description:\n%s", file, data)
		}
	}
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
