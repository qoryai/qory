package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeSkill writes a SKILL.md with the frontmatter name name at rel below dir.
func writeSkill(t *testing.T, dir, rel, name string) {
	t.Helper()
	p := filepath.Join(dir, rel, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillNamesStayInTheCheckout is a checkout whose .agents/skills has a link to a
// folder inside the checkout and one to a folder outside it: the names come from the
// skill in place and the one behind the inner link, and none from outside.
func TestSkillNamesStayInTheCheckout(t *testing.T) {
	checkout, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(checkout, ".agents/skills")
	writeSkill(t, root, "here", "here")
	writeSkill(t, checkout, "tools/inner", "inner")
	writeSkill(t, outside, "away", "away")
	if err := os.Symlink(filepath.Join(checkout, "tools"), filepath.Join(root, "tools")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	want := []string{"here", "inner"}
	if got := skillNames(root, checkout); !reflect.DeepEqual(got, want) {
		t.Errorf("skill names %v, want %v", got, want)
	}
}

// TestSkillNamesOfARootThatLeavesTheCheckout is a checkout whose .agents/skills is a link
// to a folder outside it: no name.
func TestSkillNamesOfARootThatLeavesTheCheckout(t *testing.T) {
	checkout, outside := t.TempDir(), t.TempDir()
	writeSkill(t, outside, "away", "away")
	if err := os.MkdirAll(filepath.Join(checkout, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(checkout, ".agents/skills")); err != nil {
		t.Fatal(err)
	}
	if got := skillNames(filepath.Join(checkout, ".agents/skills"), checkout); got != nil {
		t.Errorf("skill names %v, want none", got)
	}
}

// TestSkillNamesOfAFileRoot is a checkout whose .agents/skills is a file: no name, and no
// error, as Codex reads no skill there.
func TestSkillNamesOfAFileRoot(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".agents/skills"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := skillNames(filepath.Join(checkout, ".agents/skills"), checkout); got != nil {
		t.Errorf("skill names %v, want none", got)
	}
}

// TestSkillNamesPassOverAnUnreadableFolder is a folder that cannot be read beside a
// skill: the skill's name comes back, and the folder is passed over.
func TestSkillNamesPassOverAnUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 000")
	}
	checkout := t.TempDir()
	root := filepath.Join(checkout, ".agents/skills")
	writeSkill(t, root, "here", "here")
	writeSkill(t, root, "closed/inside", "inside")
	closed := filepath.Join(root, "closed")
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	if got, want := skillNames(root, checkout), []string{"here"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skill names %v, want %v", got, want)
	}
}

// TestSkillNamesStopAtCodexsDepth is a skill in a folder at depth 6 below the root and
// one at depth 7: Codex reads the first and not the second, and so does qory.
func TestSkillNamesStopAtCodexsDepth(t *testing.T) {
	checkout := t.TempDir()
	root := filepath.Join(checkout, ".agents/skills")
	six := strings.Repeat("d/", 5) + "six"
	writeSkill(t, root, six, "six")
	writeSkill(t, root, six+"/seven", "seven")
	if got, want := skillNames(root, checkout), []string{"six"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skill names %v, want %v", got, want)
	}
}

// TestSkillNamesOfARenamingLink is a skill with no frontmatter name in a folder that a
// link of another name leads to: Codex names it after the real folder, so the names
// contain both folders' names.
func TestSkillNamesOfARenamingLink(t *testing.T) {
	checkout := t.TempDir()
	root := filepath.Join(checkout, ".agents/skills")
	p := filepath.Join(checkout, "tools/real-name/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ndescription: no name\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(p), filepath.Join(root, "link-name")); err != nil {
		t.Fatal(err)
	}
	if got, want := skillNames(root, checkout), []string{"link-name", "real-name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skill names %v, want %v", got, want)
	}
}

// TestSkillNamesReadOnlyRegularFiles is a SKILL.md that is a link to a regular file, one
// that is a link to /dev/zero, and a plugin manifest that is a link to /dev/zero, beside
// a skill in a regular file: Codex passes over a SKILL.md that is a link, and qory reads
// no device, so the names come from the regular file alone.
func TestSkillNamesReadOnlyRegularFiles(t *testing.T) {
	checkout := t.TempDir()
	root := filepath.Join(checkout, ".agents/skills")
	writeSkill(t, root, "here", "here")
	writeSkill(t, checkout, "elsewhere", "elsewhere")
	for rel, target := range map[string]string{
		"linked/SKILL.md":                filepath.Join(checkout, "elsewhere/SKILL.md"),
		"zero/SKILL.md":                  "/dev/zero",
		"here/.codex-plugin/plugin.json": "/dev/zero",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := skillNames(root, checkout), []string{"here"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skill names %v, want %v", got, want)
	}
}

// TestFrontNameReadsTheHead is a SKILL.md whose frontmatter ends within the bytes qory
// reads and one whose frontmatter does not: the first has its name, the second none.
func TestFrontNameReadsTheHead(t *testing.T) {
	dir := t.TempDir()
	short, long := filepath.Join(dir, "short.md"), filepath.Join(dir, "long.md")
	body := strings.Repeat("x", maxFrontmatter)
	if err := os.WriteFile(short, []byte("---\nname: short\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(long, []byte("---\ndescription: "+body+"\nname: long\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := frontName(short); got != "short" {
		t.Errorf("frontName(short) %q, want short", got)
	}
	if got := frontName(long); got != "" {
		t.Errorf("frontName(long) %q, want none", got)
	}
}
