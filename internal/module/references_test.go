package module

import (
	"fmt"
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
		{"${qory:agent/coder}", "Change ${qory:agent/coder} to a reference.\n  ${qory:agent/coder} is not a reference: after ${qory: comes one of agents, commands, skills, then /<name>.\n  The reference is in the file of the module."},
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
	if err == nil || !strings.Contains(err.Error(), "Change ${qory:agent/coder} to a reference.\n  ${qory:agent/coder} is not a reference") {
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

// TestReadStopsAnAgentFrontmatterOver64KiB: an agent whose frontmatter has exactly 64 KiB
// reads, and one with a byte more stops the read before its frontmatter is parsed, also
// with CRLF line ends, a byte order mark, a fence that the parse does not read as one, or
// a paths key or a tier that the read would parse. Paths that are no globs and a model
// that is no tier get the error of the parse within the cap and the error of the cap over
// it, so the cap comes before both parses.
func TestReadStopsAnAgentFrontmatterOver64KiB(t *testing.T) {
	front := func(size int, key string) string {
		line := "description: "
		return "---\n" + key + line + strings.Repeat("a", size-len(key)-len(line)) + "\n---\n\nReviews.\n"
	}
	// bad are keys that a parse stops at, each with a fix line of its error.
	bad := map[string]string{
		"paths: {a: b}\n":         "Set paths to globs",
		"model: ${qory:nosuch}\n": "Use one of the tiers",
	}
	for _, key := range []string{"", "paths: [\"**\"]\n", "model: ${qory:light}\n", "paths: {a: b}\n", "model: ${qory:nosuch}\n"} {
		dir := tree(t, map[string]string{"agents/reviewer.md": front(MaxAgentFront, key)})
		if fix, isBad := bad[key]; !isBad {
			if _, err := Read("core", dir, nil, ""); err != nil {
				t.Errorf("%q, 64 KiB: %v", key, err)
			}
		} else if _, err := Read("core", dir, nil, ""); err == nil || !strings.HasPrefix(err.Error(), fix) {
			t.Errorf("%q, 64 KiB: %v, want the error of the parse, %q", key, err, fix)
		}
		want := "Shorten the frontmatter of the agent agents/reviewer.md to 64 KiB or less, then run again\n" +
			"  The frontmatter has 65537 bytes.\n  The agent is in module core."
		for name, text := range map[string]string{
			"LF":              front(MaxAgentFront+1, key),
			"CRLF":            strings.ReplaceAll(front(MaxAgentFront+1, key), "\n", "\r\n"),
			"byte order mark": "\ufeff" + front(MaxAgentFront+1, key),
			// ParseDocument reads no fence in ---\r\r and decodes on to the next fence.
			"a fence with two carriage returns": "---\nname: a\n---\r\r\n" + strings.TrimPrefix(front(MaxAgentFront+1-len("name: a\n---\r\n"), key), "---\n"),
		} {
			dir := tree(t, map[string]string{"agents/reviewer.md": text})
			if _, err := Read("core", dir, nil, ""); err == nil || err.Error() != want {
				t.Errorf("%q, %s: %v\nwant %q", key, name, err, want)
			}
		}
	}
}

// TestReadStopsASkillFrontmatterOver64KiB: a SKILL.md whose frontmatter has exactly 64 KiB
// reads, also as a note, and one with a byte more stops the read before its frontmatter is
// parsed, with a note's keys or without. The read drops the errors of a note's parse, so
// TestTheSkillCapComesBeforeTheParseOfTheNote shows the order.
func TestReadStopsASkillFrontmatterOver64KiB(t *testing.T) {
	front := func(size int, key string) string {
		line := "description: "
		return "---\n" + key + line + strings.Repeat("a", size-len(key)-len(line)) + "\n---\n\nNotes.\n"
	}
	for _, key := range []string{"name: s\n", "name: s\nnotes_for: developer\npaths: [\"**\"]\n"} {
		dir := tree(t, map[string]string{"skills/s/SKILL.md": front(MaxAgentFront, key)})
		if _, err := Read("core", dir, nil, ""); err != nil {
			t.Errorf("%q, 64 KiB: %v", key, err)
		}
		dir = tree(t, map[string]string{"skills/s/SKILL.md": front(MaxAgentFront+1, key)})
		want := "Shorten the frontmatter of the skill skills/s/SKILL.md to 64 KiB or less, then run again\n" +
			"  The frontmatter has 65537 bytes.\n  The skill is in module core."
		if _, err := Read("core", dir, nil, ""); err == nil || err.Error() != want {
			t.Errorf("%q, over 64 KiB: %v\nwant %q", key, err, want)
		}
	}
}

// TestTheSkillCapComesBeforeTheParseOfTheNote reads a SKILL.md with more than 64 KiB of
// keys of one line each. The read returns the error of the cap whether the cap comes
// before or after the parse of the note, since the read drops the errors of that parse.
// The parse allocates for each key, about 90,000 times for this file, and the read
// with no parse about 200 times. A count under 2,000 shows that the cap stops the read
// before the parse.
func TestTheSkillCapComesBeforeTheParseOfTheNote(t *testing.T) {
	var b strings.Builder
	b.WriteString("---\nname: s\n")
	for i := 0; b.Len() < MaxAgentFront+64; i++ {
		fmt.Fprintf(&b, "k%d: v\n", i)
	}
	b.WriteString("---\n\nNotes.\n")
	dir := tree(t, map[string]string{"skills/s/SKILL.md": b.String()})
	if _, err := Read("core", dir, nil, ""); err == nil || !strings.HasPrefix(err.Error(), "Shorten the frontmatter of the skill") {
		t.Fatalf("the read returned %v, want the error of the cap", err)
	}
	if n := testing.AllocsPerRun(3, func() { _, _ = Read("core", dir, nil, "") }); n >= 2000 {
		t.Errorf("the read allocates %.0f times, want under 2000: the note's frontmatter was parsed", n)
	}
}

// TestReadRefusesPathsThatAreNoGlobs is an agent whose frontmatter paths is a map, an
// empty list, a list with an item that is no string, or an empty string: each refused
// with the fix first and the agent's file.
func TestReadRefusesPathsThatAreNoGlobs(t *testing.T) {
	for _, value := range []string{"{a: b}", "[]", "[a, [b]]", `""`} {
		dir := tree(t, map[string]string{"agents/reviewer.md": "---\nname: reviewer\npaths: " + value + "\n---\n\nReviews.\n"})
		_, err := Read("core", dir, nil, "")
		want := "Set paths to globs, such as [\"**/*.py\"], or remove it.\n  The value is not a glob or a list of globs.\n  The agent is in agents/reviewer.md of module core."
		if err == nil || err.Error() != want {
			t.Errorf("paths: %s: %v\nwant %q", value, err, want)
		}
	}
}
