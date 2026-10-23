package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// TestAWalledRunCopiesTheSelectedModuleFiles is a stack of two modules outside the
// checkout, one with an exclude and one with an only, and a module from a git source,
// with an exclude, composed for claude into a home outside the checkout. A run without a
// wall links the home to the modules' folders and qory's cache. A walled run, whose
// container sees none of them, composes regular files in place of every link, leaves out
// of the home each skill the excludes and the only leave out, also from modules/<name>,
// keeps the modules' other files, and mounts the home read-only.
func TestAWalledRunCopiesTheSelectedModuleFiles(t *testing.T) {
	root := newCheckout(t)
	homes := tempDir(t)
	outside := tempDir(t)
	for _, name := range []string{"ext", "lib", "gitmod"} {
		dir := filepath.Join(outside, name)
		writeFile(t, filepath.Join(dir, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: "+name+"\n")
		for _, skill := range []string{"keep", "drop"} {
			writeFile(t, filepath.Join(dir, "skills", name+"-"+skill, "SKILL.md"), "---\nname: "+name+"-"+skill+"\ndescription: A skill of the test.\n---\n\nSteps.\n")
		}
		writeFile(t, filepath.Join(dir, "scripts", "run.sh"), "#!/bin/sh\n")
	}
	repo := filepath.Join(outside, "gitmod")
	runGit(t, repo, "init", "--quiet", "-b", "main")
	runGit(t, repo, "config", "user.name", "Tester")
	runGit(t, repo, "config", "user.email", "tester@example.com")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n"+
		"    - name: ext\n      source: {path: "+filepath.Join(outside, "ext")+"}\n      exclude: {skills: [ext-drop]}\n"+
		"    - name: lib\n      source: {path: "+filepath.Join(outside, "lib")+"}\n      only: {skills: [lib-keep]}\n"+
		"    - name: gitmod\n      source: {git: \"file://"+repo+"\", ref: main}\n      exclude: {skills: [gitmod-drop]}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  home: "+homes+"\n  launch:\n    claude:\n      command: "+fakeRuntime(t)+"\n")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a run without a wall: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	entries, err := os.ReadDir(homes)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no home under %s: %v", homes, err)
	}
	home := filepath.Join(homes, strings.TrimSuffix(entries[0].Name(), "-report.json"))
	for _, path := range []string{"modules/ext", "modules/lib", "modules/gitmod", "skills/ext-keep", "skills/lib-keep", "skills/gitmod-keep", "claude/plugin/skills/ext-keep"} {
		if info, err := os.Lstat(filepath.Join(home, path)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("without a wall, %s is not a link: %v", path, err)
		}
	}

	log := walledForager(t)
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a walled run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	var links, dropped []string
	err = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if d.Type()&fs.ModeSymlink != 0 {
			links = append(links, rel)
		}
		if strings.Contains(rel, "drop") {
			dropped = append(dropped, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 || len(dropped) != 0 {
		t.Errorf("behind a wall, the home has links %v and the left-out skills at %v", links, dropped)
	}
	for _, path := range []string{"modules/ext/scripts/run.sh", "modules/lib/scripts/run.sh", "modules/gitmod/scripts/run.sh", "modules/gitmod/skills/gitmod-keep/SKILL.md", "claude/plugin/skills/gitmod-keep/SKILL.md", "modules/ext/skills/ext-keep/SKILL.md", "modules/lib/skills/lib-keep/SKILL.md", "skills/ext-keep/SKILL.md", "skills/lib-keep/SKILL.md", "claude/plugin/skills/ext-keep/SKILL.md", "claude/plugin/skills/lib-keep/SKILL.md"} {
		if info, err := os.Lstat(filepath.Join(home, path)); err != nil || !info.Mode().IsRegular() {
			t.Errorf("behind a wall, %s is not a regular file: %v", path, err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--mount type=bind,src="+home+",dst="+home+",readonly")
}

// TestAWalledRunCopiesTheModulesItFetched is a walled run from a control plane whose
// stack lists a module from a git source, which qory run fetches into the run directory:
// the home outside the checkout contains a copy of the module's skill, as a regular file
// in each place Claude Code reads skills from, and no link.
func TestAWalledRunCopiesTheModulesItFetched(t *testing.T) {
	h := newHostFetch(t, "https://"+proxygittest.Host+"/acme/tools.git", "main")
	h.server.Repository(t, "acme/tools", "main", map[string]string{
		"modules/core/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"modules/core/skills/review/SKILL.md": "---\nname: review\ndescription: A skill of the test.\n---\n\nSteps.\n",
	})
	homes := tempDir(t)
	machineConfig(t, "harness:\n  home: "+homes+"\n  launch:\n    claude:\n      command: "+fakeRuntime(t)+"\n")
	walledForager(t)
	out, err, n, _ := h.run(t, "acme/shop")
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	if entries, err := os.ReadDir(filepath.Join(h.runDir, "sources")); err != nil || len(entries) == 0 {
		t.Fatalf("the run directory's sources: %v, %v", entries, err)
	}
	var files, links []string
	err = filepath.WalkDir(homes, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(homes, p)
		_, rel, _ = strings.Cut(filepath.ToSlash(rel), "/")
		if d.Type()&fs.ModeSymlink != 0 {
			links = append(links, rel)
		} else if d.Type().IsRegular() && strings.HasSuffix(rel, "/review/SKILL.md") {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude/plugin/skills/review/SKILL.md", "claude/skills/review/SKILL.md", "modules/core/skills/review/SKILL.md", "skills/review/SKILL.md"}
	if len(links) != 0 || !slices.Equal(files, want) {
		t.Errorf("behind a wall, the home has links %v and the skill files %v, want no link and %v", links, files, want)
	}
}

// leakingModule writes the module ext at dir with the skills keep and drop, a link alias
// at its top to skills/drop, and in keep a link more to ../drop and a link note to
// ../drop/SKILL.md, and returns the bytes of drop's SKILL.md.
func leakingModule(t *testing.T, dir string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: ext\n")
	writeFile(t, filepath.Join(dir, "skills", "keep", "SKILL.md"), "---\nname: keep\ndescription: A skill of the test.\n---\n\nSteps.\n")
	drop := "---\nname: drop\ndescription: The left-out skill of the test.\n---\n\nLeft-out steps.\n"
	writeFile(t, filepath.Join(dir, "skills", "drop", "SKILL.md"), drop)
	symlinkTo(t, filepath.Join("skills", "drop"), filepath.Join(dir, "alias"))
	symlinkTo(t, filepath.Join("..", "drop"), filepath.Join(dir, "skills", "keep", "more"))
	symlinkTo(t, filepath.Join("..", "drop", "SKILL.md"), filepath.Join(dir, "skills", "keep", "note"))
	return drop
}

// homeWith runs qory run claude in the checkout of [leakStack], which exits with want,
// and returns the home under homes that the run composed.
func homeWith(t *testing.T, homes string, want int) string {
	t.Helper()
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != want {
		t.Fatalf("qory run claude: %v (exit %d, want %d)\n%s", err, cmd.ExitCode(err), want, out)
	}
	entries, err := os.ReadDir(homes)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no home under %s: %v", homes, err)
	}
	return filepath.Join(homes, strings.TrimSuffix(entries[0].Name(), "-report.json"))
}

// leakStack writes the qory.yaml of the checkout root, with the module ext at ./mods/ext
// whose exclude leaves out the skill drop, for claude, and the machine's, with the homes
// at homes, outside the checkout.
func leakStack(t *testing.T, root, homes string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n"+
		"    - name: ext\n      source: {path: ./mods/ext}\n      exclude: {skills: [drop]}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  home: "+homes+"\n  launch:\n    claude:\n      command: "+fakeRuntime(t)+"\n")
}

// TestAWalledRunLeavesOutLinksToLeftOutFiles is a module whose exclude leaves out the
// skill drop, with a link at its top to drop and links in the kept skill keep to drop
// and to drop's SKILL.md. Behind a wall, no file in the home contains the bytes of drop's
// SKILL.md: the copy of modules/ext leaves out each link, and so do the copies of keep
// in skills and claude/plugin/skills, which keep keep's SKILL.md.
func TestAWalledRunLeavesOutLinksToLeftOutFiles(t *testing.T) {
	root := newCheckout(t)
	homes := tempDir(t)
	drop := leakingModule(t, filepath.Join(root, "mods", "ext"))
	leakStack(t, root, homes)
	walledForager(t)
	home := homeWith(t, homes, 4)
	var leaked []string
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if data, err := os.ReadFile(p); err == nil && string(data) == drop {
			rel, _ := filepath.Rel(home, p)
			leaked = append(leaked, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Errorf("behind a wall, the left-out SKILL.md is at %v", leaked)
	}
	for _, path := range []string{"modules/ext/alias", "modules/ext/skills/keep/more", "modules/ext/skills/keep/note", "skills/keep/more", "skills/keep/note", "claude/plugin/skills/keep/more", "claude/plugin/skills/keep/note"} {
		if _, err := os.Lstat(filepath.Join(home, path)); !os.IsNotExist(err) {
			t.Errorf("behind a wall, %s is in the home: %v", path, err)
		}
	}
	for _, path := range []string{"modules/ext/skills/keep/SKILL.md", "skills/keep/SKILL.md", "claude/plugin/skills/keep/SKILL.md"} {
		if info, err := os.Lstat(filepath.Join(home, path)); err != nil || !info.Mode().IsRegular() {
			t.Errorf("behind a wall, %s is not a regular file: %v", path, err)
		}
	}
}

// TestARunWithoutAWallCopiesLeftOutFiles is the module of
// [TestAWalledRunLeavesOutLinksToLeftOutFiles] in the checkout, composed into a home
// outside it by a run without a wall: the home contains copies of the module with what
// the exclude leaves out, in modules/ext and through the links in keep, as at 841ae6c.
func TestARunWithoutAWallCopiesLeftOutFiles(t *testing.T) {
	root := newCheckout(t)
	homes := tempDir(t)
	drop := leakingModule(t, filepath.Join(root, "mods", "ext"))
	leakStack(t, root, homes)
	home := homeWith(t, homes, 3)
	for _, path := range []string{"modules/ext/skills/drop/SKILL.md", "modules/ext/alias/SKILL.md", "modules/ext/skills/keep/more/SKILL.md", "modules/ext/skills/keep/note", "skills/keep/more/SKILL.md", "skills/keep/note"} {
		info, err := os.Lstat(filepath.Join(home, path))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("without a wall, %s is not a regular file: %v", path, err)
			continue
		}
		if data, err := os.ReadFile(filepath.Join(home, path)); err != nil || string(data) != drop {
			t.Errorf("without a wall, %s contains %q, want drop's SKILL.md: %v", path, data, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, "skills", "drop")); !os.IsNotExist(err) {
		t.Errorf("without a wall, skills/drop is in the home: %v", err)
	}
}

// TestAWalledRunLeavesOutATieredAgentLinkedToALeftOutFile is a module whose exclude leaves
// out the tiered agent secret, with a tiered agent file that links to secret's file.
// Behind a wall, the home contains no file with that agent's body: neither the copy of
// modules/ext nor the agent files the compose writes with the tier's model.
func TestAWalledRunLeavesOutATieredAgentLinkedToALeftOutFile(t *testing.T) {
	root := newCheckout(t)
	homes := tempDir(t)
	dir := filepath.Join(root, "mods", "ext")
	writeFile(t, filepath.Join(dir, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: ext\n")
	writeFile(t, filepath.Join(dir, "skills", "keep", "SKILL.md"), "---\nname: keep\ndescription: A skill of the test.\n---\n\nSteps.\n")
	const body = "Left-out agent body.\n"
	writeFile(t, filepath.Join(dir, "agents", "secret.md"), "---\nmodel: ${qory:light}\n---\n"+body)
	symlinkTo(t, "secret.md", filepath.Join(dir, "agents", "tiered.md"))
	leakStack(t, root, homes)
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n"+
		"    - name: ext\n      source: {path: ./mods/ext}\n      exclude: {agents: [secret]}\n")
	walledForager(t)
	home := homeWith(t, homes, 4)
	var leaked []string
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if data, err := os.ReadFile(p); err == nil && strings.Contains(string(data), body) {
			rel, _ := filepath.Rel(home, p)
			leaked = append(leaked, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Errorf("behind a wall, the left-out agent body is at %v", leaked)
	}
}
