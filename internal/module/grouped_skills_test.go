package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkIn writes the link at the path link below dir, to target.
func linkIn(t *testing.T, dir, link, target string) {
	t.Helper()
	path := filepath.Join(dir, link)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// TestAGroupedSkillIsNamedByItsLastFolder reads skills beside a group and in groups of one
// and two levels: each entry has the last folder's name and the path of its real folder,
// and a loose file or a dot name in a group adds nothing.
func TestAGroupedSkillIsNamedByItsLastFolder(t *testing.T) {
	dir := tree(t, map[string]string{
		"skills/review/SKILL.md":             "# review\n",
		"skills/delivery/deploy/SKILL.md":    "# deploy\n",
		"skills/delivery/deploy/notes.md":    "notes\n",
		"skills/delivery/README.md":          "loose\n",
		"skills/delivery/ci/fix/SKILL.md":    "# fix\n",
		"skills/delivery/ci/notes.txt":       "loose\n",
		"skills/delivery/.hidden/x/SKILL.md": "# x\n",
		"skills/.hidden/y/SKILL.md":          "# y\n",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "skills/deploy skills/delivery/deploy\n" +
		"skills/fix skills/delivery/ci/fix\n" +
		"skills/review skills/review\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
}

// TestEverythingBelowASkillIsItsFiles is a skill with a folder that has a SKILL.md: one
// skill, whose files include the nested one, read as its documents.
func TestEverythingBelowASkillIsItsFiles(t *testing.T) {
	dir := tree(t, map[string]string{
		"skills/group/outer/SKILL.md":           "# outer\n",
		"skills/group/outer/inner/SKILL.md":     "see ${qory:agents/helper}\n",
		"skills/group/outer/empty-folder/x.txt": "x\n",
		"agents/helper.md":                      "helper\n",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "agents/helper agents/helper.md\nskills/outer skills/group/outer\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
	for _, e := range l.Entries {
		if e.Name == "outer" && strings.Join(e.References, " ") != "agents/helper" {
			t.Errorf("references %v: the nested document of a grouped skill is not read", e.References)
		}
	}
}

// noSkill is the stop for a folder with no skill below it: withFolder when it has a folder
// below it, whose fix then has a second half.
func noSkill(at string, withFolder bool) string {
	if withFolder {
		return "add SKILL.md to " + at + ", or put a skill in a folder below it: " + at + " has no SKILL.md and no skill below it"
	}
	return "add SKILL.md to " + at + ": " + at + " has no SKILL.md and no skill below it"
}

// TestAFolderThatIsNeitherASkillNorAGroupStops is each folder with no SKILL.md and no
// skill to read below it: the stop starts with its fix and shows the topmost such folder,
// and the fix offers a folder below it only for one that has a folder below it.
func TestAFolderThatIsNeitherASkillNorAGroupStops(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a folder with neither", map[string]string{"skills/review/notes.md": "n\n"}, noSkill("skills/review", false)},
		{"an empty folder", map[string]string{"skills/review/": ""}, noSkill("skills/review", false)},
		{"a folder of references", map[string]string{"skills/review/references/guide.md": "g\n"}, noSkill("skills/review", true)},
		{"a group of two levels with no skill", map[string]string{"skills/a/b/c/notes.md": "n\n"}, noSkill("skills/a", true)},
		{"a folder beside a skill in a group", map[string]string{"skills/a/ok/SKILL.md": "s\n", "skills/a/bad/notes.md": "n\n"}, noSkill("skills/a/bad", false)},
		{"a folder with a folder, beside a skill in a group", map[string]string{"skills/a/ok/SKILL.md": "s\n", "skills/a/bad/deep/notes.md": "n\n"}, noSkill("skills/a/bad", true)},
		{"a folder with only a dot folder", map[string]string{"skills/a/.git/config": "c\n"}, noSkill("skills/a", false)},
		{"a skill in a dot folder only", map[string]string{"skills/a/.hidden/x/SKILL.md": "s\n"}, noSkill("skills/a", false)},
	} {
		dir := tree(t, c.files)
		_, err := Read("core", dir, nil, "")
		if want := "module core: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", c.name, err, want)
		}
	}
}

// TestAFolderNamedAfterARuntimeIsNoGroup is every runtime's name as a folder with a skill
// below it, under skills and in a group: the stop, so a skill meant for one runtime does
// not compose on the others, at the runtime's folder and not at the folder above it. A
// skill with a runtime's name is still a skill.
func TestAFolderNamedAfterARuntimeIsNoGroup(t *testing.T) {
	for _, runtime := range RuntimeFolders() {
		for _, under := range []string{"skills", "skills/group"} {
			dir := tree(t, map[string]string{under + "/" + runtime + "/ship/SKILL.md": "# ship\n"})
			_, err := Read("core", dir, nil, "")
			at := under + "/" + runtime
			if want := "module core: rename " + at + ", or read it through a variant: a folder named after a runtime is not a group"; err == nil || err.Error() != want {
				t.Errorf("%s: %v\nwant %s", at, err, want)
			}
		}
		dir := tree(t, map[string]string{"skills/" + runtime + "/SKILL.md": "# skill\n"})
		l, err := Read("core", dir, nil, "")
		if err != nil || len(l.Entries) != 1 || l.Entries[0].Name != runtime {
			t.Errorf("the skill %s: %+v, %v", runtime, l, err)
		}
	}
}

// TestRuntimeFoldersGivesACopy changes what RuntimeFolders returned: the next call lists
// the runtimes as before, so no caller can change the list.
func TestRuntimeFoldersGivesACopy(t *testing.T) {
	got := RuntimeFolders()
	got[0] = "changed"
	if RuntimeFolders()[0] == "changed" {
		t.Error("a caller changed the list of runtime folders")
	}
}

// TestAVariantsFolderIsNoGroupForAnotherVariant is a module whose variants read skills
// from folders below skills, one named after a runtime and one not. The variant reads its
// skills, and a variant that reads skills itself stops at the folder of the others, in a
// group too, with the text for a variant's folder.
func TestAVariantsFolderIsNoGroupForAnotherVariant(t *testing.T) {
	const manifest = "apiVersion: qory.dev/v1alpha1\nname: core\nvariants:\n  codex: {skills: skills/codex}\n  modern: {skills: skills/modern}\n  deep: {skills: skills/g/deep}\n  base: {agents: agents}\n"
	for variant, want := range map[string]string{"modern": "skills/ship skills/modern/ship\n", "codex": "skills/ship skills/codex/ship\n"} {
		dir := tree(t, map[string]string{"qory-module.yaml": manifest, "skills/codex/ship/SKILL.md": "s\n", "skills/modern/ship/SKILL.md": "s\n"})
		m, err := ReadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		l, err := Read("core", dir, m, variant)
		if err != nil {
			t.Fatalf("%s: %v", variant, err)
		}
		if got := entryLines(l); got != want {
			t.Errorf("%s: %s, want %s", variant, got, want)
		}
	}
	for path, at := range map[string]string{"skills/modern/ship/SKILL.md": "skills/modern", "skills/codex/ship/SKILL.md": "skills/codex", "skills/g/deep/ship/SKILL.md": "skills/g/deep"} {
		dir := tree(t, map[string]string{"qory-module.yaml": manifest, path: "s\n"})
		m, err := ReadManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := "module core: select the variant that reads " + at + ", or change that variant's skills: a folder that a variant reads skills from is not a group"
		if _, err := Read("core", dir, m, "base"); err == nil || err.Error() != want {
			t.Errorf("the variant base, %s: %v\nwant %s", at, err, want)
		}
	}
}

// TestALinkIsNeverAGroup is a link under skills or in a group: one to a folder with
// SKILL.md is a skill, one to a folder of skills or to a folder above it is the stop, so
// no cycle can occur.
func TestALinkIsNeverAGroup(t *testing.T) {
	skill := func() string {
		return tree(t, map[string]string{"elsewhere/real/SKILL.md": "s\n", "elsewhere/group/inner/SKILL.md": "s\n", "skills/g/ok/SKILL.md": "s\n"})
	}
	dir := skill()
	linkIn(t, dir, "skills/linked", "../elsewhere/real")
	linkIn(t, dir, "skills/g/also", "../../elsewhere/real")
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatalf("links to a skill: %v", err)
	}
	if want := "skills/also skills/g/also\nskills/linked skills/linked\nskills/ok skills/g/ok\n"; entryLines(l) != want {
		t.Errorf("entries:\n%swant:\n%s", entryLines(l), want)
	}
	for link, target := range map[string]string{
		"skills/togroup": "../elsewhere/group",
		"skills/g/up":    "..",
		"skills/g/loop":  "../g",
	} {
		dir := skill()
		linkIn(t, dir, link, target)
		_, err := Read("core", dir, nil, "")
		if want := "module core: remove the link " + link + ", or point it at a skill: it leads to a folder with no SKILL.md"; err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", link, err, want)
		}
	}
}

// TestALinkThatLeadsNowhereStops is a link to nothing or to itself, under skills and in a
// group, beside a skill or alone: the stop has the link's path below the module and no
// absolute path. A link to a file is skipped as a loose file.
func TestALinkThatLeadsNowhereStops(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		link  string
	}{
		{map[string]string{"skills/": ""}, "skills/gone"},
		{map[string]string{"skills/g/ok/SKILL.md": "s\n"}, "skills/g/gone"},
		{map[string]string{"skills/g/": ""}, "skills/g/gone"},
		{map[string]string{"skills/g/h/": ""}, "skills/g/h/gone"},
	} {
		for _, target := range []string{"missing", filepath.Base(c.link)} {
			dir := tree(t, c.files)
			linkIn(t, dir, c.link, target)
			_, err := Read("core", dir, nil, "")
			if want := "module core: remove the link " + c.link + ", or point it at a path that exists: it leads nowhere"; err == nil || err.Error() != want {
				t.Errorf("%s to %s: %v\nwant %s", c.link, target, err, want)
			}
		}
	}
	dir := tree(t, map[string]string{"skills/g/ok/SKILL.md": "s\n", "notes.md": "n\n"})
	linkIn(t, dir, "skills/g/file", "../../notes.md")
	if _, err := Read("core", dir, nil, ""); err != nil {
		t.Errorf("a link to a file: %v", err)
	}
}

// TestTwoSkillsOfOneNameInAModuleStop is one name twice, in groups and beside a group: the
// stop starts with its fix and shows both folders, as they are in the module.
func TestTwoSkillsOfOneNameInAModuleStop(t *testing.T) {
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"skills/a/review", "skills/b/review"}, "rename skills/a/review or skills/b/review: each skill goes to skills/<name>, and both are the skill review"},
		{[]string{"skills/a/review", "skills/review"}, "rename skills/a/review or skills/review: each skill goes to skills/<name>, and both are the skill review"},
		{[]string{"skills/a/b/review", "skills/c/review"}, "rename skills/a/b/review or skills/c/review: each skill goes to skills/<name>, and both are the skill review"},
	} {
		files := map[string]string{}
		for _, f := range c.files {
			files[f+"/SKILL.md"] = "s\n"
		}
		_, err := Read("core", tree(t, files), nil, "")
		if err == nil || err.Error() != "module core: "+c.want {
			t.Errorf("%v: %v\nwant %s", c.files, err, c.want)
		}
	}
}

// TestSkillNamesThatDifferOnlyInCaseStop is two groups that hold names which differ only in
// case, which a file system of either kind can hold: the stop, on every file system,
// whichever name comes first. Names that are not equal in any case both read.
func TestSkillNamesThatDifferOnlyInCaseStop(t *testing.T) {
	for _, c := range []struct{ first, second string }{
		{"skills/a/Review", "skills/b/review"},
		{"skills/a/review", "skills/b/REVIEW"},
	} {
		dir := tree(t, map[string]string{c.first + "/SKILL.md": "s\n", c.second + "/SKILL.md": "s\n"})
		_, err := Read("core", dir, nil, "")
		if want := "module core: rename " + c.first + " or " + c.second + ": each skill goes to skills/<name>, and these two names differ only in case"; err == nil || err.Error() != want {
			t.Errorf("%v: %v\nwant %s", c, err, want)
		}
	}
	if _, err := Read("core", tree(t, map[string]string{"skills/a/review/SKILL.md": "s\n", "skills/b/reviews/SKILL.md": "s\n"}), nil, ""); err != nil {
		t.Errorf("different names: %v", err)
	}
}

// TestALinkOutOfAGroupedSkillShowsItsRealFolder is a link that leaves the module: the line
// shows the path a person can find, with the group, for the skill itself and for a file in
// it. A skill in a variant's directory shows that directory.
func TestALinkOutOfAGroupedSkillShowsItsRealFolder(t *testing.T) {
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "skill", "SKILL.md"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"skills/group/s":                 filepath.Join(outside, "skill"),
		"skills/group/ok/notes.md":       filepath.Join(outside, "skill", "SKILL.md"),
		"skills/group/deep/ok2/lib/f.md": filepath.Join(outside, "skill", "SKILL.md"),
	} {
		dir := tree(t, map[string]string{"skills/group/ok/SKILL.md": "s\n", "skills/group/deep/ok2/SKILL.md": "s\n"})
		linkIn(t, dir, link, target)
		_, err := Read("core", dir, nil, "")
		if want := "module core: Remove the link " + link + " or point it inside the module. It links to "; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v\nwant it to start with %s", link, err, want)
		}
	}
	dir := tree(t, map[string]string{"qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\nvariants:\n  v: {skills: v/skills}\n", "v/skills/ok/SKILL.md": "s\n"})
	linkIn(t, dir, "v/skills/ok/notes.md", filepath.Join(outside, "skill", "SKILL.md"))
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read("core", dir, m, "v"); err == nil || !strings.HasPrefix(err.Error(), "module core: Remove the link v/skills/ok/notes.md or point it") {
		t.Errorf("a variant's skill: %v", err)
	}
}

// TestAReferenceToAGroupedSkillHasOneSegment is the reference rule: a document refers to a
// grouped skill by its last folder, and a path with the group stops as any two segments do.
func TestAReferenceToAGroupedSkillHasOneSegment(t *testing.T) {
	dir := tree(t, map[string]string{"skills/a/deploy/SKILL.md": "s\n", "agents/x.md": "${qory:skills/deploy}\n"})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range l.Entries {
		found = found || e.Name == "x" && strings.Join(e.References, " ") == "skills/deploy"
	}
	if !found {
		t.Errorf("entries %+v", l.Entries)
	}
	dir = tree(t, map[string]string{"skills/a/deploy/SKILL.md": "s\n", "agents/x.md": "${qory:skills/a/deploy}\n"})
	if _, err := Read("core", dir, nil, ""); err == nil || !strings.Contains(err.Error(), "a name is one path segment") {
		t.Errorf("two segments: %v", err)
	}
}
