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
		{"${qory:agent/coder}", "${qory:agent/coder} is not a reference; after ${qory: comes one of agents, commands, skills, then /<name>"},
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
	if err == nil || !strings.Contains(err.Error(), "module core: agents/reviewer.md: ${qory:agent/coder} is not a reference") {
		t.Fatalf("err = %v", err)
	}
}
