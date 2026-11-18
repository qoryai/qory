package compose_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// notesTree is a base stack whose module core fills the roles developer and reviewers and
// whose module python ships the note python-naming, and a checkout whose module app ships
// the skill app-naming with the frontmatter keys in front.
func notesTree(t *testing.T, front string) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"base/" + stack.FileName:                            "apiVersion: qory.dev/v1alpha1\nname: base\nextending:\n  kinds: [skills]\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - name: python\n    source:\n      path: modules/python\n",
		"base/modules/core/qory-module.yaml":                "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    reviewers: many\nprovides:\n  agents:\n    developer: base-developer\n    reviewers: [code-reviewer]\n",
		"base/modules/core/agents/base-developer.md":        "---\nname: base-developer\ndescription: d\n---\n\nWrites.\n",
		"base/modules/core/agents/code-reviewer.md":         "---\nname: code-reviewer\ndescription: r\n---\n\nReviews.\n",
		"base/modules/python/qory-module.yaml":              "apiVersion: qory.dev/v1alpha1\nname: python\n",
		"base/modules/python/skills/python-naming/SKILL.md": "---\nname: python-naming\ndescription: n\nnotes_for: developer\npaths: [\"**/*.py\", pyproject.toml]\n---\n\nNotes.\n",
		"app/" + config.FileName:                            "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n  modules:\n    - name: app\n",
		"app/modules/app/qory-module.yaml":                  "apiVersion: qory.dev/v1alpha1\nname: app\n",
		"app/modules/app/skills/app-naming/SKILL.md":        "---\nname: app-naming\ndescription: n\n" + front + "---\n\nNotes.\n",
		"app/modules/app/skills/not-a-note/SKILL.md":        "---\nname: not-a-note\ndescription: n\npaths: [\"src/**\"]\n---\n\nA skill with paths and no notes_for is no note.\n",
	})
}

// TestANoteOfABaseModuleAndOneOfTheCheckoutAreListedWithTheirLayers is a technology module
// of the base stack with a note and a module of the checkout's qory.yaml with another: both
// are in RoleNotes, in the order of the entries, with their module and layer 1 and 0, the
// roles written agents/<role>; a skill with paths and no notes_for is no note.
func TestANoteOfABaseModuleAndOneOfTheCheckoutAreListedWithTheirLayers(t *testing.T) {
	res, err := composeExtending(t, notesTree(t, "notes_for: [developer, reviewers]\npaths: src/**\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []compose.RoleNote{
		{Name: "app-naming", Roles: []string{"agents/developer", "agents/reviewers"}, Paths: []string{"src/**"}, Module: "app", Layer: 0},
		{Name: "python-naming", Roles: []string{"agents/developer"}, Paths: []string{"**/*.py", "pyproject.toml"}, Module: "python", Layer: 1},
	}
	if got := res.RoleNotes(); !reflect.DeepEqual(got, want) {
		t.Errorf("RoleNotes = %+v\nwant %+v", got, want)
	}
}

// TestANoteInAStackThatExtendsNothingIsLayer0 is a stack with no base and a note in its
// module: the layer is 0.
func TestANoteInAStackThatExtendsNothingIsLayer0(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		stack.FileName:                    "apiVersion: qory.dev/v1alpha1\nbind:\n  agents:\n    developer: dev\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"modules/a/agents/dev.md":         "---\nname: dev\ndescription: d\n---\n\nWrites.\n",
		"modules/a/skills/style/SKILL.md": "---\nname: style\ndescription: s\nnotes_for: developer\npaths: [\"*.go\"]\n---\n\nNotes.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []compose.RoleNote{{Name: "style", Roles: []string{"agents/developer"}, Paths: []string{"*.go"}, Module: "a", Layer: 0}}
	if got := res.RoleNotes(); !reflect.DeepEqual(got, want) {
		t.Errorf("RoleNotes = %+v\nwant %+v", got, want)
	}
}

// TestAHarnessWithoutANoteHasNone is a stack with no skill that has notes_for.
func TestAHarnessWithoutANoteHasNone(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		stack.FileName:                    "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"modules/a/skills/style/SKILL.md": "---\nname: style\ndescription: s\npaths: [\"*.go\"]\n---\n\nNotes.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.RoleNotes(); len(got) != 0 {
		t.Errorf("RoleNotes = %+v, want none", got)
	}
}

// TestABadNoteIsRefusedWithItsFixFirst is a note for a role no agent fills, a note with no
// paths, with an empty list, with a value that is no glob, and with a glob that does not
// parse, and a notes_for that is no role: each fails the compose with the fix as line 1, at
// most 100 characters, then the cause on the next line.
func TestABadNoteIsRefusedWithItsFixFirst(t *testing.T) {
	const fillsDeveloper = "paths: [\"**/*.py\"]\n"
	for _, c := range []struct {
		name  string
		front string
		want  string
	}{
		{"a role no agent fills", "notes_for: tester\n" + fillsDeveloper,
			"Set notes_for in app-naming of module app to a role the stack fills, such as developer.\n  tester is none."},
		{"one role of a list that no agent fills", "notes_for: [developer, tester]\n" + fillsDeveloper,
			"Set notes_for in app-naming of module app to a role the stack fills, such as developer.\n  tester is none."},
		{"a group role with a member", "notes_for: reviewers\n" + fillsDeveloper, ""},
		{"no paths", "notes_for: developer\n",
			"Add paths to app-naming of module app.\n  A note needs one path or more.\n  The skill is in skills/app-naming/SKILL.md of module app."},
		{"an empty list of paths", "notes_for: developer\npaths: []\n",
			"Add paths to app-naming of module app.\n  A note needs one path or more, and the value is not a glob or a list of globs.\n  The skill is in skills/app-naming/SKILL.md of module app."},
		{"a path that is no string", "notes_for: developer\npaths: 3\n",
			"Add paths to app-naming of module app.\n  A note needs one path or more, and the value is not a glob or a list of globs.\n  The skill is in skills/app-naming/SKILL.md of module app."},
		{"a glob that does not parse", "notes_for: developer\npaths: [\"**/*.py\", \"src/[a\"]\n",
			"Add paths to app-naming of module app.\n  A note needs one path or more, and \"src/[a\" is no glob.\n  The skill is in skills/app-naming/SKILL.md of module app."},
		{"a notes_for that is no role", "notes_for: {a: b}\n" + fillsDeveloper,
			"Set notes_for in app-naming of module app to a role or a list of roles.\n  The value is not a role or a list of roles.\n  The skill is in skills/app-naming/SKILL.md of module app."},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeExtending(t, notesTree(t, c.front))
			if c.want == "" {
				if err != nil {
					t.Fatalf("a note for a group role with a member failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("composed without an error; want:\n%s", c.want)
			}
			if err.Error() != c.want {
				t.Errorf("error:\n%s\nwant:\n%s", err, c.want)
			}
			if first, _, _ := strings.Cut(err.Error(), "\n"); utf8.RuneCountInString(first) > 100 || !strings.HasPrefix(first, "Set notes_for") && !strings.HasPrefix(first, "Add paths") {
				t.Errorf("line 1 is not a fix of at most 100 characters: %s", first)
			}
		})
	}
}

// TestANoteForARoleNothingProvidesIsRefused is a note for planners, a role no module of the
// stack provides or binds.
func TestANoteForARoleNothingProvidesIsRefused(t *testing.T) {
	_, err := composeExtending(t, notesTree(t, "notes_for: planners\npaths: src/**\n"))
	want := "Set notes_for in app-naming of module app to a role the stack fills, such as developer.\n  planners is none."
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want:\n%s", err, want)
	}
}

// TestANoteTheCheckoutExcludesIsNotChecked is a note of the base's module python for a role
// nothing fills, left out by an exclude in the checkout's qory.yaml: the entry is no note,
// so the compose does not check it and RoleNotes does not list it.
func TestANoteTheCheckoutExcludesIsNotChecked(t *testing.T) {
	dir := notesTree(t, "notes_for: developer\npaths: src/**\n")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base/modules/python/skills/python-naming/SKILL.md", "---\nname: python-naming\ndescription: n\nnotes_for: tester\npaths: [\"*.py\"]\n---\n\nNotes.\n")
	if _, err := composeExtending(t, dir); err == nil {
		t.Fatal("a note for a role no agent fills composed")
	}
	write("app/"+config.FileName, "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target: {runtime: claude}\n  modules:\n    - name: python\n      exclude: {skills: [python-naming]}\n    - name: app\n")
	res, err := composeExtending(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.RoleNotes(); len(got) != 1 || got[0].Name != "app-naming" {
		t.Errorf("RoleNotes = %+v, want app-naming alone", got)
	}
}
