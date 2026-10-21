package render_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// appModule is the module app at a new temporary directory with its links resolved, with
// the skill deploy holding SKILL.md, the way a module in the checkout looks to a compose.
func appModule(t *testing.T) compose.Module {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "skills", "deploy", "SKILL.md"), "---\nname: deploy\n---\n\nSteps.\n")
	root, info := openRoot(t, dir)
	return compose.Module{Name: "app", Dir: dir, Source: dir, Copy: true, Root: root, RealDir: dir, DirInfo: info}
}

// openRoot is the directory dir opened as a root until the test ends, as a compose opens
// a module's directory, and what the root is.
func openRoot(t *testing.T, dir string) (*os.Root, os.FileInfo) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	return root, info
}

// retargeted is the module name read at <checkout>/<link>, a link to the directory
// harness-real, after which the link leads to harness-other, which holds the same files:
// the directory the link leads to is another than the one the compose opened.
func retargeted(t *testing.T, name, link string) compose.Module {
	t.Helper()
	ck, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"harness-real", "harness-other"} {
		write(t, filepath.Join(ck, d, "skills", "deploy", "SKILL.md"), "---\nname: deploy\n---\n\nSteps.\n")
	}
	path := filepath.Join(ck, link)
	symlink(t, "harness-real", path)
	root, info := openRoot(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	symlink(t, "harness-other", path)
	return compose.Module{Name: name, Dir: path, Source: "./" + link, Copy: true, Root: root, RealDir: filepath.Join(ck, "harness-real"), DirInfo: info}
}

// fill writes n empty files into dir.
func fill(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// wantError fails the test unless err's text is want.
func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("error = %v\nwant    %s", err, want)
	}
}

// TestCopyRefusesALinkOutOfTheModule is a skill with a script that links to a file
// outside the module: the copy is refused with the text a module read gives for it.
func TestCopyRefusesALinkOutOfTheModule(t *testing.T) {
	m := appModule(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outside, "x"), "secret\n")
	symlink(t, filepath.Join(outside, "x"), filepath.Join(m.Dir, "skills", "deploy", "run.sh"))
	err = render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy"))
	wantError(t, err, "module app: skills/deploy/run.sh links to "+filepath.Join(outside, "x")+", outside the module")
}

// TestCopyFollowsALinkInsideTheModule is a skill whose script and whose directory of
// helpers are links to places inside the module: the copy holds the bytes and the files
// they lead to, and no link.
func TestCopyFollowsALinkInsideTheModule(t *testing.T) {
	m := appModule(t)
	write(t, filepath.Join(m.Dir, "scripts", "run.sh"), "echo run\n")
	write(t, filepath.Join(m.Dir, "lib", "util.sh"), "echo util\n")
	symlink(t, "../../scripts/run.sh", filepath.Join(m.Dir, "skills", "deploy", "run.sh"))
	symlink(t, filepath.Join(m.Dir, "lib"), filepath.Join(m.Dir, "skills", "deploy", "lib"))
	dst := filepath.Join(t.TempDir(), "deploy")
	if err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"SKILL.md": "---\nname: deploy\n---\n\nSteps.\n", "run.sh": "echo run\n", "lib/util.sh": "echo util\n"} {
		path := filepath.Join(dst, name)
		if isLink(t, path) {
			t.Errorf("%s is a link", name)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Errorf("%s = %q (%v), want %q", name, data, err, want)
		}
	}
	if isLink(t, filepath.Join(dst, "lib")) {
		t.Error("lib is a link")
	}
}

// TestCopyRefusesALoop is a link in a skill to the skill's parent directory, which a
// copy would follow without end.
func TestCopyRefusesALoop(t *testing.T) {
	m := appModule(t)
	symlink(t, "..", filepath.Join(m.Dir, "skills", "deploy", "loop"))
	err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy"))
	wantError(t, err, "module app: Remove skills/deploy/loop, or point it at a directory that is not above it. It leads to a directory above it, which qory cannot copy into the home.")
}

// TestCopyRefusesAFIFO is a named pipe in a skill, which is no file, directory or link.
func TestCopyRefusesAFIFO(t *testing.T) {
	m := appModule(t)
	pipe := filepath.Join(m.Dir, "skills", "deploy", "pipe")
	if out, err := exec.Command("mkfifo", pipe).CombinedOutput(); err != nil {
		t.Skipf("mkfifo: %v\n%s", err, out)
	}
	err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy"))
	wantError(t, err, "module app: Remove skills/deploy/pipe from the module. It is not a file, a directory or a link, so qory cannot copy it into the home.")
	// A link to the pipe leads to no file either.
	if err := os.Remove(pipe); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mkfifo", filepath.Join(m.Dir, "pipe")).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v\n%s", err, out)
	}
	symlink(t, "../../pipe", pipe)
	err = render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy"))
	wantError(t, err, "module app: Remove skills/deploy/pipe from the module. It is not a file, a directory or a link, so qory cannot copy it into the home.")
}

// TestCopyOfAModuleStopsAt10000Entries is a module of 10,001 files, directories and
// links, one more than the most qory copies, and the same module with one file fewer,
// which is copied whole without its .git.
func TestCopyOfAModuleStopsAt10000Entries(t *testing.T) {
	m := appModule(t)
	// skills, skills/deploy and SKILL.md are three entries; data and its files are the
	// rest.
	fill(t, filepath.Join(m.Dir, "data"), 10_001-4)
	write(t, filepath.Join(m.Dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	err := render.CopyModule(m, filepath.Join(t.TempDir(), "app"))
	wantError(t, err, "module app: Move the module out of the checkout, or ship fewer files. It contains more than 10,000 files, directories and links, the most qory copies into the home.")

	if err := os.Remove(filepath.Join(m.Dir, "data", "f00000")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "app")
	if err := render.CopyModule(m, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "f09996")); err != nil {
		t.Error(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf("the copy holds .git: %v", err)
	}
}

// TestPluginStopsAt10000Entries is two modules whose skills put 10,001 files, directories
// and links under a plugin directory, one more than a walled Claude Code launch takes,
// and the same plugin with one file fewer.
func TestPluginStopsAt10000Entries(t *testing.T) {
	plugin := t.TempDir()
	// skills, skills/a and skills/b are three entries, and each skill holds SKILL.md and
	// the files fill writes.
	var modules []compose.Module
	for _, name := range []string{"a", "b"} {
		m := appModule(t)
		m.Name = name
		fill(t, filepath.Join(m.Dir, "skills", "deploy"), 4_998)
		modules = append(modules, m)
	}
	if err := os.MkdirAll(filepath.Join(plugin, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range modules {
		if err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(plugin, "skills", m.Name)); err != nil {
			t.Fatal(err)
		}
	}
	wantError(t, render.CheckPluginSize(plugin), "Move a module out of the checkout, or ship fewer files. claude/plugin contains more than 10,000 files, directories and links, the most a walled Claude Code run takes.")
	if err := os.Remove(filepath.Join(plugin, "skills", "b", "f00000")); err != nil {
		t.Fatal(err)
	}
	if err := render.CheckPluginSize(plugin); err != nil {
		t.Errorf("a plugin of 10,000 entries: %v", err)
	}
}

// TestCopyKeepsTheModeWithoutWideBits is a skill with files and directories of several
// modes: each copy has the original's permission bits without setuid, setgid, sticky and
// write for group and others, each directory with read, write and search for its owner,
// and each file one name.
func TestCopyKeepsTheModeWithoutWideBits(t *testing.T) {
	m := appModule(t)
	skill := filepath.Join(m.Dir, "skills", "deploy")
	files := map[string]os.FileMode{
		"run.sh":     0o750,
		"secret.txt": 0o600,
		"wide.sh":    0o777,
		"setuid.sh":  0o755 | os.ModeSetuid,
		"group.sh":   0o775 | os.ModeSetgid,
	}
	for name, mode := range files {
		write(t, filepath.Join(skill, name), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(skill, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	dirs := map[string]os.FileMode{"private": 0o700, "locked": 0o500, "shared": 0o777 | os.ModeSticky}
	for name, mode := range dirs {
		dir := filepath.Join(skill, name)
		write(t, filepath.Join(dir, "x"), "x\n")
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
	}
	dst := filepath.Join(t.TempDir(), "deploy")
	t.Cleanup(func() { os.Chmod(filepath.Join(dst, "locked"), 0o755) })
	if err := render.CopyEntry(m, skill, dst); err != nil {
		t.Fatal(err)
	}
	want := map[string]os.FileMode{
		".":          0o755 | os.ModeDir,
		"SKILL.md":   0o644,
		"run.sh":     0o750,
		"secret.txt": 0o600,
		"wide.sh":    0o755,
		"setuid.sh":  0o755,
		"group.sh":   0o755,
		"private":    0o700 | os.ModeDir,
		"locked":     0o700 | os.ModeDir,
		"shared":     0o755 | os.ModeDir,
	}
	for name, mode := range want {
		info, err := os.Lstat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != mode {
			t.Errorf("%s has mode %v, want %v", name, info.Mode(), mode)
		}
		if links := hardLinks(info); !info.IsDir() && links > 1 {
			t.Errorf("%s has %d names, want 1", name, links)
		}
	}
}

// TestCopyWritesNothingThroughALinkInTheHome is a files entry of a copied module at the
// path of a skill the home links to another module: copilot links skills at
// workspace/.github/skills/<name>, and the entry's copy would land in the other module's
// directory through that link. The build fails, and nothing is written there.
func TestCopyWritesNothingThroughALinkInTheHome(t *testing.T) {
	hermetic(t)
	core := appModule(t).Dir
	m := appModule(t)
	write(t, filepath.Join(core, "skills", "review", "SKILL.md"), "---\nname: review\n---\n")
	evil := filepath.Join(m.Dir, "files", "copilot", "workspace", ".github", "skills", "review", "evil.sh")
	write(t, evil, "planted\n")
	res := &compose.Result{
		Stack:   &stack.Stack{},
		Modules: []compose.Module{{Name: "core", Dir: core}, m},
		Entries: []compose.Entry{
			{Kind: "files", Name: "copilot/workspace/.github/skills/review/evil.sh", Module: "app", Path: evil},
			{Kind: "skills", Name: "review", Module: "core", Path: filepath.Join(core, "skills", "review")},
		},
	}
	if err := render.CheckFiles(res); err != nil {
		t.Fatalf("CheckFiles refuses: %v", err)
	}
	stage := t.TempDir()
	err := render.BuildAt(res, stage, stage, lookup(t, "copilot"))
	if err == nil {
		t.Error("the build wrote the files entry through the skill's link")
	}
	t.Logf("build: %v", err)
	if _, err := os.Lstat(filepath.Join(core, "skills", "review", "evil.sh")); err == nil {
		t.Error("the copy landed in module core's directory")
	}
}

// TestLinkWritesNothingThroughALinkInTheHome is the build of
// [TestCopyWritesNothingThroughALinkInTheHome] for a home that copies nothing: the files
// entry is a link, and the build fails before it writes the link into module core's
// directory through the skill's link.
func TestLinkWritesNothingThroughALinkInTheHome(t *testing.T) {
	hermetic(t)
	core := appModule(t).Dir
	m := appModule(t)
	m.Copy = false
	write(t, filepath.Join(core, "skills", "review", "SKILL.md"), "---\nname: review\n---\n")
	evil := filepath.Join(m.Dir, "files", "copilot", "workspace", ".github", "skills", "review", "evil.sh")
	write(t, evil, "planted\n")
	res := &compose.Result{
		Stack:   &stack.Stack{},
		Modules: []compose.Module{{Name: "core", Dir: core}, m},
		Entries: []compose.Entry{
			{Kind: "files", Name: "copilot/workspace/.github/skills/review/evil.sh", Module: "app", Path: evil},
			{Kind: "skills", Name: "review", Module: "core", Path: filepath.Join(core, "skills", "review")},
		},
	}
	if err := render.CheckFiles(res); err != nil {
		t.Fatalf("CheckFiles refuses: %v", err)
	}
	stage := t.TempDir()
	err := render.BuildAt(res, stage, stage, lookup(t, "copilot"))
	if err == nil {
		t.Error("the build wrote the files entry through the skill's link")
	}
	t.Logf("build: %v", err)
	if _, err := os.Lstat(filepath.Join(core, "skills", "review", "evil.sh")); err == nil {
		t.Error("the link landed in module core's directory")
	}
}

// TestCopyOfAnEntryLeavesOutGit is a skill holding a .git directory: its copy leaves the
// directory out, as the copy of the module does, so each entry copy is part of the module
// copy that the module's limit counts.
func TestCopyOfAnEntryLeavesOutGit(t *testing.T) {
	m := appModule(t)
	fill(t, filepath.Join(m.Dir, "skills", "deploy", ".git", "objects"), 3)
	dst := filepath.Join(t.TempDir(), "deploy")
	if err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf("the copy of the skill holds .git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		t.Error(err)
	}
}

// TestPluginCountFollowsALink is a plugin with a copied skill of 6,000 files and a link,
// as qory writes for a module outside the checkout, to a skill of 5,000 files: Forager's
// check of a walled launch counts what the link leads to, so the plugin is refused. With
// a linked skill of 3,000 files it is not, and a second link to a directory already
// counted adds only the link.
func TestPluginCountFollowsALink(t *testing.T) {
	plugin := t.TempDir()
	fill(t, filepath.Join(plugin, "skills", "copied"), 6_000)
	linked := t.TempDir()
	fill(t, filepath.Join(linked, "big"), 5_000)
	fill(t, filepath.Join(linked, "small"), 3_000)
	symlink(t, filepath.Join(linked, "big"), filepath.Join(plugin, "skills", "linked"))
	wantError(t, render.CheckPluginSize(plugin), "Move a module out of the checkout, or ship fewer files. claude/plugin contains more than 10,000 files, directories and links, the most a walled Claude Code run takes.")

	if err := os.Remove(filepath.Join(plugin, "skills", "linked")); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(linked, "small"), filepath.Join(plugin, "skills", "linked"))
	symlink(t, filepath.Join(linked, "small"), filepath.Join(plugin, "skills", "again"))
	symlink(t, filepath.Join(plugin, "skills", "copied"), filepath.Join(plugin, "skills", "back"))
	if err := render.CheckPluginSize(plugin); err != nil {
		t.Errorf("a plugin of 9,005 entries: %v", err)
	}
}

// TestCopyOfAModuleSkipsALinkThatLeadsNowhere is a module with a link to nothing inside
// it: the copy of the module goes on without it. A link to nothing outside the module is
// still refused, and so is the copy of an entry that holds a link to nothing.
func TestCopyOfAModuleSkipsALinkThatLeadsNowhere(t *testing.T) {
	m := appModule(t)
	if err := os.MkdirAll(filepath.Join(m.Dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "missing.sh", filepath.Join(m.Dir, "scripts", "dangle"))
	dst := filepath.Join(t.TempDir(), "app")
	if err := render.CopyModule(m, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "scripts", "dangle")); !os.IsNotExist(err) {
		t.Errorf("the copy has scripts/dangle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "skills", "deploy", "SKILL.md")); err != nil {
		t.Error(err)
	}

	symlink(t, "../../nowhere-outside", filepath.Join(m.Dir, "scripts", "out"))
	if err := render.CopyModule(m, filepath.Join(t.TempDir(), "app")); err == nil {
		t.Error("the copy of the module took a link to nothing outside it")
	}
	if err := os.Remove(filepath.Join(m.Dir, "scripts", "out")); err != nil {
		t.Fatal(err)
	}
	symlink(t, "missing.sh", filepath.Join(m.Dir, "skills", "deploy", "dangle"))
	if err := render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy")); err == nil {
		t.Error("the copy of an entry took a link to nothing")
	}
}

// TestCopyOfAModuleLeavesOutQoryAtTheTop is a module that is a checkout, with the qory
// directory at its top and a folder of that name deeper down: the copy leaves out the
// first and keeps the second, which is the module's content.
func TestCopyOfAModuleLeavesOutQoryAtTheTop(t *testing.T) {
	m := appModule(t)
	write(t, filepath.Join(m.Dir, ".qory", "harness-report.json"), "{}\n")
	write(t, filepath.Join(m.Dir, "docs", ".qory", "note.md"), "# note\n")
	dst := filepath.Join(t.TempDir(), "app")
	if err := render.CopyModule(m, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".qory")); !os.IsNotExist(err) {
		t.Errorf("the copy holds .qory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "docs", ".qory", "note.md")); err != nil {
		t.Errorf("the copy lost docs/.qory: %v", err)
	}
}

// TestCopyRefusesAModuleThatLeadsElsewhereSinceTheRead is a module whose directory is a
// link that leads to another directory after the module read: the copy of the module and
// the copy of an entry are refused, and nothing is written.
func TestCopyRefusesAModuleThatLeadsElsewhereSinceTheRead(t *testing.T) {
	m := retargeted(t, "app", "harness")
	const want = "module app: Compose again. ./harness leads to another directory than when qory read the module, so qory did not copy it into the home."
	dst := filepath.Join(t.TempDir(), "app")
	wantError(t, render.CopyModule(m, dst), want)
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Errorf("the copy wrote %s: %v", dst, err)
	}
	wantError(t, render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy")), want)
}

// TestCopyRefusalOfAModuleThatLeadsElsewhereHidesASecret is that refusal for a module
// whose name and source contain an access key secret: the phrase stands in for both.
func TestCopyRefusalOfAModuleThatLeadsElsewhereHidesASecret(t *testing.T) {
	m := retargeted(t, "qak_m", "qak_m")
	const want = "module (a name that contains an access key secret): Compose again. ./(a name that contains an access key secret) leads to another directory than when qory read the module, so qory did not copy it into the home."
	wantError(t, render.CopyModule(m, filepath.Join(t.TempDir(), "app")), want)
}

// TestCopyRefusesAModuleTheComposeDidNotOpen is a module marked to copy whose directory
// the compose did not open: the copy is refused as one whose directory changed.
func TestCopyRefusesAModuleTheComposeDidNotOpen(t *testing.T) {
	m := appModule(t)
	m.Source, m.Root, m.DirInfo = "./harness", nil, nil
	wantError(t, render.CopyModule(m, filepath.Join(t.TempDir(), "app")), "module app: Compose again. ./harness leads to another directory than when qory read the module, so qory did not copy it into the home.")
}

// sparse makes the file at path size bytes long without writing them.
func sparse(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCopyRefusesAFileOver64MiB is a module with a file one byte over 64 MiB: the copy of
// the module is refused, and so are the copy of a skill and the read of an agent that
// large, whose Markdown a copy edits.
func TestCopyRefusesAFileOver64MiB(t *testing.T) {
	m := appModule(t)
	sparse(t, filepath.Join(m.Dir, "scripts", "big.bin"), 64<<20+1)
	wantError(t, render.CopyModule(m, filepath.Join(t.TempDir(), "app")),
		"module app: Move the module out of the checkout, or ship scripts/big.bin under 64 MiB. It is larger than 64 MiB, the most qory copies into the home.")

	m = appModule(t)
	sparse(t, filepath.Join(m.Dir, "skills", "deploy", "notes.md"), 64<<20+1)
	wantError(t, render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), filepath.Join(t.TempDir(), "deploy")),
		"module app: Move the module out of the checkout, or ship skills/deploy/notes.md under 64 MiB. It is larger than 64 MiB, the most qory copies into the home.")
	agent := filepath.Join(m.Dir, "agents", "big.md")
	sparse(t, agent, 64<<20+1)
	_, err := render.ReadEntry(m, agent)
	wantError(t, err, "module app: Move the module out of the checkout, or ship agents/big.md under 64 MiB. It is larger than 64 MiB, the most qory copies into the home.")
}

// TestCopyOfAModuleCountsAsItReads is a module with a directory of 10,001 files: the
// copy is refused while it reads the directory, before it writes any file of it.
func TestCopyOfAModuleCountsAsItReads(t *testing.T) {
	m := appModule(t)
	fill(t, filepath.Join(m.Dir, "many"), 10_001)
	dst := filepath.Join(t.TempDir(), "app")
	if err := render.CopyModule(m, dst); err == nil {
		t.Fatal("the copy took 10,001 files")
	}
	files := 0
	_ = filepath.WalkDir(dst, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files++
		}
		return nil
	})
	if files != 0 {
		t.Errorf("the copy wrote %d files before it refused", files)
	}
}

// writtenBytes is the size of every file under dir.
func writtenBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// TestCopyStopsAtTheModulesTotal is a module of two files of 600 KiB with the total a copy
// writes lowered to 1 MiB: the copy of the module, and of an entry holding both files, is
// refused, and neither writes more than 1 MiB. A module of 900 KiB is copied.
func TestCopyStopsAtTheModulesTotal(t *testing.T) {
	render.SetMaxModuleBytes(t, 1<<20)
	const want = "module app: Move the module out of the checkout, or ship fewer files. Its files add up to more than 1 GiB, the most qory copies into the home."
	m := appModule(t)
	for _, name := range []string{"a.bin", "b.bin"} {
		write(t, filepath.Join(m.Dir, "skills", "deploy", name), strings.Repeat("x", 600<<10))
	}
	dst := filepath.Join(t.TempDir(), "app")
	wantError(t, render.CopyModule(m, dst), want)
	if n := writtenBytes(t, dst); n > 1<<20 {
		t.Errorf("the copy of the module wrote %d bytes", n)
	}
	entry := filepath.Join(t.TempDir(), "deploy")
	wantError(t, render.CopyEntry(m, filepath.Join(m.Dir, "skills", "deploy"), entry), want)
	if n := writtenBytes(t, entry); n > 1<<20 {
		t.Errorf("the copy of the entry wrote %d bytes", n)
	}

	m = appModule(t)
	write(t, filepath.Join(m.Dir, "skills", "deploy", "a.bin"), strings.Repeat("x", 900<<10))
	if err := render.CopyModule(m, filepath.Join(t.TempDir(), "app")); err != nil {
		t.Error(err)
	}
}
