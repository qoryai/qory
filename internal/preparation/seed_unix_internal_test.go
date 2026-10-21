//go:build unix

package preparation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// seeded makes the repository base/path.git as [remote] does, with .gitignore naming
// deps/, a script and a link among its files, and the image's copy of it: a clone at
// depth one with an ignored dependency, an untracked file, a tracked file changed and
// a hook the build left. It then pushes a newer commit to the remote, so the run's
// commit is not the copy's, and returns the copy's folder and the newer commit.
func seeded(t *testing.T, base, path, branch string) (seed, head string) {
	t.Helper()
	remote(t, base, path, branch, map[string]string{
		".gitignore": "deps/\n",
		"README.md":  "first\n",
		"run.sh":     "#!/bin/sh\necho run\n",
	})
	seed = filepath.Join(t.TempDir(), "seed")
	run(t, base, "clone", "--quiet", "--depth", "1", "--branch", branch, "--", baseURL+"/"+path+".git", seed)
	write(t, filepath.Join(seed, "deps", "lib", "index.js"), "dependency\n")
	write(t, filepath.Join(seed, "notes.txt"), "untracked\n")
	write(t, filepath.Join(seed, "README.md"), "changed by the build\n")
	if err := os.Chmod(filepath.Join(seed, "deps", "lib", "index.js"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("lib/index.js", filepath.Join(seed, "deps", "main.js")); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	run(t, work, "clone", "--quiet", "--", baseURL+"/"+path+".git", ".")
	write(t, filepath.Join(work, "README.md"), "second\n")
	write(t, filepath.Join(work, "added.txt"), "added\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "newer")
	run(t, work, "push", "--quiet", "origin", branch)
	return seed, run(t, work, "rev-parse", "HEAD")
}

// TestStartsFromTheImagesCopy is a run that starts from the image's copy: the copy's
// files are in the working folder, the tracked ones at the run's commit, on the
// default branch tracking origin, and the ignored and untracked ones as the build left
// them, a link as a link, each with its mode. The working tree is clean but for the
// untracked file, and the lines say the copy was used.
func TestStartsFromTheImagesCopy(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, head := seeded(t, base, "acme/shop", "trunk")
	remote(t, base, "acme/lib", "main", nil)
	dir := emptyFolder(t)
	run(t, dir, "config", "qory.made", "by the runner")
	lines, err := prepareFrom(context.Background(), dir, seed, env("github.com", "acme/shop,acme/lib"))
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "config", "qory.made"); got != "by the runner" {
		t.Errorf("the working folder's repository is not the runner's: qory.made is %q", got)
	}
	if want := "qory run prepare: copied the working tree of acme/shop from the image\nqory run prepare: fetched acme/shop and 1 further repository in Xs"; lines != want {
		t.Errorf("the lines are %q, want %q", lines, want)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD is %s, want the run's commit %s", got, head)
	}
	if got := run(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "trunk" {
		t.Errorf("the branch is %s, want trunk", got)
	}
	if got := run(t, dir, "rev-parse", "--abbrev-ref", "trunk@{upstream}"); got != "origin/trunk" {
		t.Errorf("trunk tracks %s, want origin/trunk", got)
	}
	if got := run(t, dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the repository is shallow: %s", got)
	}
	if got := run(t, dir, "remote", "get-url", "origin"); got != baseURL+"/acme/shop.git" {
		t.Errorf("origin is %s", got)
	}
	if got := run(t, dir, "status", "--porcelain"); got != "?? notes.txt" {
		t.Errorf("git status: %q", got)
	}
	for name, want := range map[string]string{
		"README.md":                             "second\n",
		"added.txt":                             "added\n",
		"notes.txt":                             "untracked\n",
		"deps/lib/index.js":                     "dependency\n",
		".qory/repositories/acme/lib/README.md": "acme/lib\n",
	} {
		if got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s holds %q, %v; want %q", name, got, err, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "deps", "lib", "index.js")); err != nil || fi.Mode().Perm() != 0o751 {
		t.Errorf("deps/lib/index.js: %v, %v; want mode 0751", fi, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "deps", "main.js")); err != nil || target != "lib/index.js" {
		t.Errorf("deps/main.js links to %q, %v; want lib/index.js", target, err)
	}
	if got := run(t, dir, "check-ignore", "deps/lib/index.js"); got != "deps/lib/index.js" {
		t.Errorf("deps/ is not ignored: %q", got)
	}
	// The copy is as it was.
	if got, err := os.ReadFile(filepath.Join(seed, "README.md")); err != nil || string(got) != "changed by the build\n" {
		t.Errorf("the copy's README.md holds %q, %v", got, err)
	}
}

// TestRefusesACopyOfAnotherRepository refuses an image's copy whose origin is not the
// run's repository, has no url, has two, or has the right one only in a file the
// configuration includes; it takes the url with .git at the end or without, and on
// github.com in any case. A refused copy leaves the working folder as it was.
func TestRefusesACopyOfAnotherRepository(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, _ := seeded(t, base, "acme/shop", "main")
	want := "qory run prepare: the image's copy in " + seed + " is not of acme/shop: its origin is not " + baseURL + "/acme/shop"
	gitConfig := filepath.Join(seed, ".git", "config")
	included := filepath.Join(t.TempDir(), "included")
	write(t, included, "[remote \"origin\"]\n\turl = "+baseURL+"/acme/shop.git\n")
	for _, c := range []struct {
		name  string
		setup []string
		ok    bool
	}{
		{"another repository", []string{"remote.origin.url", baseURL + "/acme/other.git"}, false},
		{"a longer path", []string{"remote.origin.url", baseURL + "/acme/shop/more.git"}, false},
		{"another host", []string{"remote.origin.url", "https://git.example.com/acme/shop.git"}, false},
		{"a user in the URL", []string{"remote.origin.url", strings.Replace(baseURL, "file://", "file://someone@", 1) + "/acme/shop.git"}, false},
		{"no .git", []string{"remote.origin.url", baseURL + "/acme/shop"}, true},
		{"another case", []string{"remote.origin.url", baseURL + "/Acme/Shop.git"}, true},
	} {
		run(t, seed, "config", "--file", gitConfig, c.setup[0], c.setup[1])
		dir := emptyFolder(t)
		_, err := prepareFrom(context.Background(), dir, seed, env("github.com", "acme/shop"))
		if c.ok {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %s", c.name, err, want)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("%s: the working folder holds %d entries", c.name, len(entries))
		}
		if strings.Contains(err.Error(), c.setup[1]) && !strings.HasSuffix(c.setup[1], "/acme/shop") {
			t.Errorf("%s: the error quotes the copy's url", c.name)
		}
	}
	for _, c := range []struct {
		name  string
		setup func()
	}{
		{"two urls", func() {
			run(t, seed, "config", "--file", gitConfig, "--add", "remote.origin.url", baseURL+"/acme/shop.git")
		}},
		{"no url", func() { run(t, seed, "config", "--file", gitConfig, "--unset-all", "remote.origin.url") }},
		{"the url of a remote Origin", func() {
			run(t, seed, "config", "--file", gitConfig, "remote.Origin.url", baseURL+"/acme/shop.git")
		}},
		{"a url in an included file", func() { run(t, seed, "config", "--file", gitConfig, "include.path", included) }},
	} {
		c.setup()
		_, err := prepareFrom(context.Background(), emptyFolder(t), seed, env("github.com", "acme/shop"))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %s", c.name, err, want)
		}
	}
	// Elsewhere than github.com, the path's case counts.
	run(t, seed, "config", "--file", gitConfig, "--unset-all", "include.path")
	run(t, seed, "config", "--file", gitConfig, "remote.origin.url", baseURL+"/Acme/Shop.git")
	if _, err := prepareFrom(context.Background(), emptyFolder(t), seed, env("git.example.com", "acme/shop")); err == nil || !strings.Contains(err.Error(), "is not of acme/shop") {
		t.Errorf("another case on git.example.com: %v", err)
	}
}

// hostileConfig is a configuration with each key that would run a program, read
// another file or reach a host otherwise.
const hostileConfig = `[include]
	path = /nonexistent/included
[includeIf "gitdir:/"]
	path = /nonexistent/included
[core]
	hooksPath = /nonexistent/hooks
	sshCommand = /nonexistent/ssh
	gitProxy = /nonexistent/proxy
[filter "lfs"]
	smudge = /nonexistent/smudge
[credential]
	helper = /nonexistent/helper
[url "https://elsewhere.example.com/"]
	insteadOf = https://
`

// linkIn replaces link with a symbolic link to target.
func linkIn(t *testing.T, target, link string) {
	t.Helper()
	if err := os.RemoveAll(link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestTakesNothingOfTheCopysGit starts from an image's copy whose .git holds
// [hostileConfig], a commondir, hooks and a monitor, links out of it in the places git
// writes to or reads from, an alternates file, a replace ref and a planted object for a
// file the run's commit keeps, and whose .qory holds a file: none of it is taken. The
// working folder's repository is the runner's, nothing is written where the links
// point, and the tracked files are the fetched commit's.
func TestTakesNothingOfTheCopysGit(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, head := seeded(t, base, "acme/shop", "main")
	remote(t, base, "acme/lib", "main", nil)
	marker := filepath.Join(t.TempDir(), "ran")
	gitDir := filepath.Join(seed, ".git")

	// A replace ref and a planted loose object, each for the blob of run.sh.
	blob := run(t, seed, "rev-parse", "HEAD:run.sh")
	evil := filepath.Join(t.TempDir(), "evil")
	write(t, evil, "#!/bin/sh\necho evil\n")
	evilBlob := run(t, seed, "hash-object", "-w", evil)
	run(t, seed, "replace", blob, evilBlob)
	data, err := os.ReadFile(filepath.Join(gitDir, "objects", evilBlob[:2], evilBlob[2:]))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(gitDir, "objects", blob[:2], blob[2:]), string(data))
	write(t, filepath.Join(seed, "run.sh"), "#!/bin/sh\necho evil\n")
	// The hooks and the monitor, once the test's gits of the copy are done.
	hostile(t, gitDir, marker)

	// Links out of .git, an alternates file, a commondir and the configuration.
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	write(t, victim, "original\n")
	logs := filepath.Join(outside, "logs")
	if err := os.Mkdir(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	linkIn(t, victim, filepath.Join(gitDir, "info", "exclude"))
	linkIn(t, victim, filepath.Join(gitDir, "FETCH_HEAD"))
	linkIn(t, victim, filepath.Join(gitDir, "shallow"))
	linkIn(t, logs, filepath.Join(gitDir, "logs"))
	write(t, filepath.Join(gitDir, "objects", "info", "alternates"), "/nonexistent/objects\n")
	gitConfig := filepath.Join(gitDir, "config")
	config, err := os.ReadFile(gitConfig)
	if err != nil {
		t.Fatal(err)
	}
	write(t, gitConfig, string(config)+hostileConfig)
	write(t, filepath.Join(gitDir, "commondir"), "/nonexistent/common\n")
	write(t, filepath.Join(seed, ".qory", "settings"), "the copy's\n")

	dir := emptyFolder(t)
	if _, err := prepareFrom(context.Background(), dir, seed, env("git.example.com", "acme/shop,acme/lib")); err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD is %s, want %s", got, head)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "run.sh")); string(got) != "#!/bin/sh\necho run\n" {
		t.Errorf("run.sh holds %q", got)
	}
	if got := run(t, dir, "rev-parse", "HEAD:run.sh"); got != blob {
		t.Errorf("HEAD:run.sh is %s, want %s", got, blob)
	}
	if got := run(t, dir, "status", "--porcelain"); got != "?? notes.txt" {
		t.Errorf("git status: %q", got)
	}
	if got, _ := os.ReadFile(victim); string(got) != "original\n" {
		t.Errorf("the file a link pointed at holds %q", got)
	}
	if entries, _ := os.ReadDir(logs); len(entries) != 0 {
		t.Errorf("%d entries were written to the folder a link pointed at", len(entries))
	}
	for _, name := range []string{"commondir", "objects/info/alternates", "refs/replace"} {
		if _, err := os.Lstat(filepath.Join(dir, ".git", filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf(".git/%s: %v", name, err)
		}
	}
	for _, name := range []string{"info", "info/exclude", "logs", "shallow", "FETCH_HEAD", "hooks"} {
		if fi, err := os.Lstat(filepath.Join(dir, ".git", filepath.FromSlash(name))); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf(".git/%s is a link", name)
		}
	}
	if keys := run(t, dir, "config", "--file", filepath.Join(dir, ".git", "config"), "--name-only", "--list"); strings.Contains(keys, "include") || strings.Contains(keys, "hookspath") || strings.Contains(keys, "fsmonitor") || strings.Contains(keys, "insteadof") || strings.Contains(keys, "credential") || strings.Contains(keys, "filter") {
		t.Errorf("the configuration holds a key of the copy's:\n%s", keys)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".qory", "settings")); !os.IsNotExist(err) {
		t.Errorf(".qory/settings of the copy was taken: %v", err)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("a hook or the monitor of the copy ran: %v\n%s", err, got)
	}
}

// TestKeepsTheWorkingFoldersQory starts from an image's copy with a .qory, a folder or
// a link, in a working folder that holds .qory already: the working folder's is kept,
// the copy's is not taken, and the further repository is cloned under it.
func TestKeepsTheWorkingFoldersQory(t *testing.T) {
	needGit(t)
	for _, linked := range []bool{false, true} {
		base := t.TempDir()
		seed, _ := seeded(t, base, "acme/shop", "main")
		remote(t, base, "acme/lib", "main", nil)
		if linked {
			linkIn(t, t.TempDir(), filepath.Join(seed, ".qory"))
		} else {
			write(t, filepath.Join(seed, ".qory", "settings"), "the copy's\n")
		}
		dir := emptyFolder(t)
		write(t, filepath.Join(dir, ".qory", "harness", "kept.txt"), "kept\n")
		if _, err := prepareFrom(context.Background(), dir, seed, env("git.example.com", "acme/shop,acme/lib")); err != nil {
			t.Fatalf("linked %v: %v", linked, err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, ".qory", "harness", "kept.txt")); string(got) != "kept\n" {
			t.Errorf("linked %v: kept.txt holds %q", linked, got)
		}
		if fi, err := os.Lstat(filepath.Join(dir, ".qory")); err != nil || !fi.IsDir() {
			t.Errorf("linked %v: .qory is %v, %v", linked, fi, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".qory", "settings")); !os.IsNotExist(err) {
			t.Errorf("linked %v: .qory/settings of the copy was taken: %v", linked, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".qory", "repositories", "acme", "lib", "README.md")); err != nil {
			t.Errorf("linked %v: acme/lib: %v", linked, err)
		}
	}
}

// TestWritesNoExcludeThroughALink is a working folder whose exclude file, or the folder
// of it, is a link: a run with a further repository is refused in words that say so,
// and nothing is written where the link points.
func TestWritesNoExcludeThroughALink(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "main", nil)
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	for _, c := range []struct{ link, target, want string }{
		{".git/info/exclude", victim, "qory run prepare: the working folder's .git/info/exclude is a link; qory writes the exclude file of the repository only where no link leads"},
		{".git/info", outside, "qory run prepare: the working folder's .git/info is a link; qory writes the exclude file of the repository only where no link leads"},
	} {
		write(t, victim, "original\n")
		dir := emptyFolder(t)
		linkIn(t, c.target, filepath.Join(dir, filepath.FromSlash(c.link)))
		_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop,acme/lib"))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.link, err, c.want)
		}
		if got, _ := os.ReadFile(victim); string(got) != "original\n" {
			t.Errorf("%s: the file the link points at holds %q", c.link, got)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 1 {
			t.Errorf("%s: the folder outside holds %d entries", c.link, len(entries))
		}
	}
}

// TestRunsNoHookOfTheCopy starts from an image's copy whose hooks folder is a link to
// hooks elsewhere, and whose configuration names a file system monitor: a git of the
// copy runs the post-checkout hook and the monitor, and the preparation runs neither,
// nor any other hook, and leaves what the link pointed at as it was.
func TestRunsNoHookOfTheCopy(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, _ := seeded(t, base, "acme/shop", "main")
	marker := filepath.Join(t.TempDir(), "ran")
	elsewhere := t.TempDir()
	hostile(t, elsewhere, marker)
	hooks := filepath.Join(seed, ".git", "hooks")
	if err := os.RemoveAll(hooks); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "hooks"), hooks); err != nil {
		t.Fatal(err)
	}
	monitor := run(t, elsewhere, "config", "--file", filepath.Join(elsewhere, "config"), "core.fsmonitor")
	run(t, seed, "config", "core.fsmonitor", monitor)
	run(t, seed, "checkout", "--quiet", "-b", "built")
	ran, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(ran), "post-checkout") || !strings.Contains(string(ran), "monitor") {
		t.Fatalf("the hook and the monitor of the test do not run: %v\n%s", err, ran)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	dir := emptyFolder(t)
	if _, err := prepareFrom(context.Background(), dir, seed, env("git.example.com", "acme/shop")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("a hook or the monitor of the copy ran: %v\n%s", err, got)
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".git", "hooks")); err != nil || !fi.IsDir() {
		t.Errorf(".git/hooks is not a folder: %v, %v", fi, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(elsewhere, "hooks")); len(entries) == 0 {
		t.Error("the hooks the link pointed at were removed")
	}
}

// TestRefusesACopyThatIsNoTree refuses a copy that is a link or a file, one with no
// .git folder, and one that holds an entry that is not a file, a folder or a link, and
// starts from nothing.
func TestRefusesACopyThatIsNoTree(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, _ := seeded(t, base, "acme/shop", "main")
	link := filepath.Join(t.TempDir(), "seed")
	if err := os.Symlink(seed, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "seed")
	write(t, file, "a file\n")
	noGit := t.TempDir()
	write(t, filepath.Join(noGit, "README.md"), "no git\n")
	gitFile := t.TempDir()
	write(t, filepath.Join(gitFile, ".git"), "gitdir: "+filepath.Join(seed, ".git")+"\n")
	noConfig := t.TempDir()
	if err := os.Mkdir(filepath.Join(noConfig, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkedConfig := t.TempDir()
	if err := os.Mkdir(filepath.Join(linkedConfig, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(seed, ".git", "config"), filepath.Join(linkedConfig, ".git", "config")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, seed, want string }{
		{"a link", link, "qory run prepare: " + link + " is a file or a link; the image's copy of the run's repository is taken only from a folder"},
		{"a file", file, "qory run prepare: " + file + " is a file or a link; the image's copy of the run's repository is taken only from a folder"},
		{"no .git", noGit, "qory run prepare: " + noGit + " has no .git folder with a config file; the image's copy of the run's repository is a git working tree"},
		{"a .git file", gitFile, "qory run prepare: " + gitFile + " has no .git folder with a config file; the image's copy of the run's repository is a git working tree"},
		{"no config", noConfig, "qory run prepare: " + noConfig + " has no .git folder with a config file; the image's copy of the run's repository is a git working tree"},
		{"a config link", linkedConfig, "qory run prepare: " + linkedConfig + " has no .git folder with a config file; the image's copy of the run's repository is a git working tree"},
	} {
		dir := emptyFolder(t)
		_, err := prepareFrom(context.Background(), dir, c.seed, env("git.example.com", "acme/shop"))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if out, _ := exec.Command("git", "-C", dir, "remote").Output(); strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s: a remote was added: %s", c.name, out)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(seed, "deps", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := prepareFrom(context.Background(), emptyFolder(t), seed, env("git.example.com", "acme/shop"))
	if want := "qory run prepare: the image's copy in " + seed + " holds deps/pipe, which is not a file, a folder or a link; nothing is copied from it"; err == nil || err.Error() != want {
		t.Errorf("a pipe: %v, want %s", err, want)
	}
}

// TestCopiesNoLinkThrough is an image's copy with links that point out of it, to a
// folder and a file: each is copied as a link, and nothing is written where they point.
func TestCopiesNoLinkThrough(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	seed, _ := seeded(t, base, "acme/shop", "main")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "kept.txt"), "kept\n")
	if err := os.Symlink(outside, filepath.Join(seed, "deps", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "kept.txt"), filepath.Join(seed, "deps", "kept.txt")); err != nil {
		t.Fatal(err)
	}
	dir := emptyFolder(t)
	if _, err := prepareFrom(context.Background(), dir, seed, env("git.example.com", "acme/shop")); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "deps", "outside")); err != nil || target != outside {
		t.Errorf("deps/outside links to %q, %v", target, err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("the folder outside holds %d entries, want 1", len(entries))
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "kept.txt")); string(got) != "kept\n" {
		t.Errorf("kept.txt holds %q", got)
	}
}

// TestNoCopyIsTodaysFetch is a seed path where nothing is: the run's repository is
// fetched as with no image's copy, and no line says a copy was used.
func TestNoCopyIsTodaysFetch(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	lines, err := prepareFrom(context.Background(), emptyFolder(t), filepath.Join(t.TempDir(), "seed"), env("git.example.com", "acme/shop"))
	if err != nil || lines != "qory run prepare: fetched acme/shop in Xs" {
		t.Errorf("%q, %v", lines, err)
	}
}
