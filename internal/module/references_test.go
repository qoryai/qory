package module

import (
	"reflect"
	"strings"
	"testing"
)

// TestReferencesListsEachOnceSorted reads references out of a document: each key once,
// sorted, whatever order and repetition the text has, and prose around them untouched.
func TestReferencesListsEachOnceSorted(t *testing.T) {
	text := "Run ${qory:skills/test}, dispatch ${qory:agents/coder} and ${qory:agents/coder} again, then ${qory:commands/ship}."
	got, err := References(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agents/coder", "commands/ship", "skills/test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("references %v, want %v", got, want)
	}
	if got, _ := References("no reference here, $QORY_HARNESS_HOME included"); got != nil {
		t.Fatalf("references %v, want none", got)
	}
}

// TestReferencesRefusesWhatIsNotOne is a misspelt kind, a missing name and a name that
// is not one segment: each refused naming the reference as written.
func TestReferencesRefusesWhatIsNotOne(t *testing.T) {
	for _, c := range [][2]string{
		{"${qory:agent/coder}", "Change ${qory:agent/coder} in the file of the module to a reference.\n  ${qory:agent/coder} is not a reference: after ${qory: comes one of agents, commands, skills, then /<name>."},
		{"${qory:coder}", "${qory:coder} is not a reference"},
		{"${qory:agents/}", `${qory:agents/} refers to "", which is not an entry name`},
		{"${qory:agents/a/b}", `${qory:agents/a/b} refers to "a/b", which is not an entry name`},
	} {
		_, err := References("Dispatch " + c[0] + ".")
		if err == nil || !strings.Contains(err.Error(), c[1]) {
			t.Errorf("%s: err = %v, want %q", c[0], err, c[1])
		}
	}
}

// TestSubstituteResolvesEveryReference hands each reference to the resolver and keeps
// everything else, a malformed reference included, as written.
func TestSubstituteResolvesEveryReference(t *testing.T) {
	text := "Run /${qory:skills/test}, then ${qory:agents/coder}; ${qory:agent/x} stays.\n"
	got := Substitute(text, func(kind, name string) string { return "harness:" + name })
	want := "Run /harness:test, then harness:coder; ${qory:agent/x} stays.\n"
	if got != want {
		t.Fatalf("substitute:\n%s\nwant:\n%s", got, want)
	}
	if !HasReference([]byte(text)) || HasReference([]byte("plain")) {
		t.Error("HasReference disagrees with the text")
	}
}

// TestReadCollectsReferencesPerEntry reads a module whose skill references from SKILL.md
// and from a file beside it, and whose agent references nothing: the skill's entry lists
// the union, the agent's lists nil, and a malformed reference fails the read naming the
// file.
func TestReadCollectsReferencesPerEntry(t *testing.T) {
	dir := tree(t, map[string]string{
		ManifestName:                       "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"skills/deploy/SKILL.md":           "---\nname: deploy\n---\nDispatch ${qory:agents/coder}.\n",
		"skills/deploy/steps/checklist.md": "Then ${qory:skills/test} and ${qory:agents/coder}.\n",
		"skills/deploy/notes.txt":          "${qory:agents/ignored} is not read in a text file.\n",
		"agents/reviewer.md":               "---\nname: reviewer\n---\nReviews.\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Read("core", dir, m, "")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string][]string{}
	for _, e := range l.Entries {
		refs[e.Kind+"/"+e.Name] = e.References
	}
	if want := []string{"agents/coder", "skills/test"}; !reflect.DeepEqual(refs["skills/deploy"], want) {
		t.Errorf("skills/deploy references %v, want %v", refs["skills/deploy"], want)
	}
	if refs["agents/reviewer"] != nil {
		t.Errorf("agents/reviewer references %v, want none", refs["agents/reviewer"])
	}
	dir = tree(t, map[string]string{
		ManifestName:         "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"agents/reviewer.md": "Dispatch ${qory:agent/coder}.\n",
	})
	m, _ = ReadManifest(dir)
	_, err = Read("core", dir, m, "")
	if err == nil || !strings.Contains(err.Error(), "Change ${qory:agent/coder} in agents/reviewer.md of module core to a reference.\n  ${qory:agent/coder} is not a reference") {
		t.Fatalf("err = %v", err)
	}
}

// TestReadRecordsWhereEachReferenceIs is a skill whose frontmatter and body reference
// roles, and agents with frontmatter paths: each entry has the first file of each
// reference, the references a frontmatter makes, and an agent's globs, one string read
// as a list of one.
func TestReadRecordsWhereEachReferenceIs(t *testing.T) {
	dir := tree(t, map[string]string{
		"skills/review/SKILL.md":        "---\nname: review\ndescription: Starts ${qory:agents/lead}.\n---\n\nStart ${qory:agents/reviewers}.\n",
		"skills/review/steps.md":        "Then ${qory:agents/reviewers} and ${qory:agents/lead}.\n",
		"agents/python-reviewer.md":     "---\nname: python-reviewer\npaths: [\"**/*.py\", pyproject.toml]\n---\n\nReviews.\n",
		"agents/docs-reviewer.md":       "---\nname: docs-reviewer\npaths: docs/**\n---\n\nReviews.\n",
		"agents/everything-reviewer.md": "---\nname: everything-reviewer\n---\n\nReviews paths: all of them.\n",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range l.Entries {
		got[e.Kind+"/"+e.Name] = e
	}
	skill := got["skills/review"]
	if want := map[string]string{"agents/lead": "skills/review/SKILL.md", "agents/reviewers": "skills/review/SKILL.md"}; !reflect.DeepEqual(skill.In, want) {
		t.Errorf("in = %v, want %v", skill.In, want)
	}
	if want := map[string]string{"agents/lead": "skills/review/SKILL.md"}; !reflect.DeepEqual(skill.Front, want) {
		t.Errorf("front = %v, want %v", skill.Front, want)
	}
	for name, want := range map[string][]string{
		"agents/python-reviewer":     {"**/*.py", "pyproject.toml"},
		"agents/docs-reviewer":       {"docs/**"},
		"agents/everything-reviewer": nil,
	} {
		if !reflect.DeepEqual(got[name].Paths, want) {
			t.Errorf("%s: paths = %v, want %v", name, got[name].Paths, want)
		}
	}
}

// TestReadRefusesPathsThatAreNoGlobs is an agent whose frontmatter paths is a map, an
// empty list, a list with an item that is no string, or an empty string: each refused
// with the fix first and the agent's file.
func TestReadRefusesPathsThatAreNoGlobs(t *testing.T) {
	for _, value := range []string{"{a: b}", "[]", "[a, [b]]", `""`} {
		dir := tree(t, map[string]string{"agents/reviewer.md": "---\nname: reviewer\npaths: " + value + "\n---\n\nReviews.\n"})
		_, err := Read("core", dir, nil, "")
		want := "module core: agents/reviewer.md: set paths to globs, such as [\"**/*.py\"], or remove it\n  the value is not a glob or a list of globs"
		if err == nil || err.Error() != want {
			t.Errorf("paths: %s: %v\nwant %q", value, err, want)
		}
	}
}
