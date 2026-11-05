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

// TestAFolderThatIsNeitherASkillNorAGroupStops is each folder with no SKILL.md and no
// skill to read below it: the stop shows the full path below the module.
func TestAFolderThatIsNeitherASkillNorAGroupStops(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"a folder with neither", map[string]string{"skills/review/notes.md": "n\n"}, "skills/review"},
		{"an empty folder", map[string]string{"skills/review/": ""}, "skills/review"},
		{"a group with no skill", map[string]string{"skills/a/b/notes.md": "n\n"}, "skills/a/b"},
		{"a group of two levels with no skill", map[string]string{"skills/a/b/c/notes.md": "n\n"}, "skills/a/b/c"},
		{"a folder beside a skill in a group", map[string]string{"skills/a/ok/SKILL.md": "s\n", "skills/a/bad/notes.md": "n\n"}, "skills/a/bad"},
		{"a folder with only a dot folder", map[string]string{"skills/a/.git/config": "c\n"}, "skills/a"},
	} {
		dir := tree(t, c.files)
		_, err := Read("core", dir, nil, "")
		if want := "module core: " + c.want + " has no SKILL.md"; err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", c.name, err, want)
		}
	}
}

// TestAFolderNamedAfterARuntimeIsNoGroup is every runtime's name as a folder with a skill
// below it, under skills and in a group: the stop, so a skill meant for one runtime does
// not compose on the others. A skill with a runtime's name is still a skill.
func TestAFolderNamedAfterARuntimeIsNoGroup(t *testing.T) {
	for _, runtime := range RuntimeFolders {
		for _, under := range []string{"skills", "skills/group"} {
			dir := tree(t, map[string]string{under + "/" + runtime + "/ship/SKILL.md": "# ship\n"})
			_, err := Read("core", dir, nil, "")
			if want := "module core: " + under + "/" + runtime + " has no SKILL.md"; err == nil || err.Error() != want {
				t.Errorf("%s: %v\nwant %s", under+"/"+runtime, err, want)
			}
		}
		dir := tree(t, map[string]string{"skills/" + runtime + "/SKILL.md": "# skill\n"})
		l, err := Read("core", dir, nil, "")
		if err != nil || len(l.Entries) != 1 || l.Entries[0].Name != runtime {
			t.Errorf("the skill %s: %+v, %v", runtime, l, err)
		}
	}
}

// TestAVariantsFolderIsNoGroupForAnotherVariant is a module whose variants read skills
// from folders below skills, one named after a runtime and one not. The variant reads its
// skills, and a variant that reads skills itself stops at the folder of the others.
func TestAVariantsFolderIsNoGroupForAnotherVariant(t *testing.T) {
	const manifest = "apiVersion: qory.dev/v1alpha1\nname: core\nvariants:\n  codex: {skills: skills/codex}\n  modern: {skills: skills/modern}\n  base: {agents: agents}\n"
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
	dir := tree(t, map[string]string{"qory-module.yaml": manifest, "skills/modern/ship/SKILL.md": "s\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read("core", dir, m, "base"); err == nil || err.Error() != "module core: skills/modern has no SKILL.md" {
		t.Errorf("the variant base: %v", err)
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
		if want := "module core: " + link + " has no SKILL.md"; err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", link, err, want)
		}
	}
}

// TestTwoSkillsOfOneNameInAModuleStop is one name twice, in groups and beside a group: the
// stop starts with its fix and shows both folders, as they are in the module.
func TestTwoSkillsOfOneNameInAModuleStop(t *testing.T) {
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"skills/a/review", "skills/b/review"}, "rename skills/a/review or skills/b/review: both are the skill review, and qory writes skills flat"},
		{[]string{"skills/a/review", "skills/review"}, "rename skills/a/review or skills/review: both are the skill review, and qory writes skills flat"},
		{[]string{"skills/a/b/review", "skills/c/review"}, "rename skills/a/b/review or skills/c/review: both are the skill review, and qory writes skills flat"},
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
