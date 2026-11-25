package hostgit_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// plant is a repository whose HEAD names an object that a read fetches from a promisor remote
// with a program of the configuration, which makes the file that the function returns.
type plant struct {
	name string
	// config is the configuration after the keys of the promisor remote evil, with %[1]s for
	// the marker's path and %[2]s for the repository's .git.
	config string
	// corrupt is a HEAD at a blob whose packed entry is corrupt, so the fetch checks that the
	// objects it has are connected before it checks the protocol.
	corrupt bool
	// bundle is a HEAD at a commit that the file b.bundle of .git has.
	bundle bool
}

// plants are the ways to a program of the repository's configuration from the fetch of a
// missing object.
var plants = []plant{
	{name: "uploadpack", config: "[remote \"evil\"]\n\turl = %[2]s\n\tuploadpack = touch %[1]s #\n"},
	{name: "sshCommand", config: "[remote \"evil\"]\n\turl = ssh://example.invalid/x\n[core]\n\tsshCommand = touch %[1]s #\n"},
	{name: "a protocol key", config: "[protocol]\n\tallow = always\n[protocol \"file\"]\n\tallow = always\n[remote \"evil\"]\n\turl = %[2]s\n\tuploadpack = touch %[1]s #\n"},
	{name: "ext", config: "[protocol \"ext\"]\n\tallow = always\n[remote \"evil\"]\n\turl = \"ext::touch %[1]s\"\n"},
	{name: "vcs none", config: "[remote \"evil\"]\n\turl = x\n\tvcs = none\n[alias]\n\tremote-none = \"!touch %[1]s #\"\n"},
	{name: "url none", config: "[remote \"evil\"]\n\turl = none::x\n[alias]\n\tremote-none = \"!touch %[1]s #\"\n"},
	{name: "a vcs with no name", config: "[remote \"evil\"]\n\turl = x\n\tvcs = \"\"\n[alias]\n\tremote- = \"!touch %[1]s #\"\n"},
	// git takes the object from the bundle and runs no program: the read must still fail.
	{name: "bundleURI", config: "[remote \"evil\"]\n\turl = %[2]s\n[fetch]\n\tbundleURI = %[2]s/b.bundle\n", bundle: true},
	{name: "alternateRefsCommand", config: "[remote \"evil\"]\n\turl = %[2]s\n[core]\n\talternateRefsCommand = \"touch %[1]s #\"\n", corrupt: true},
}

// make makes the repository of p, and returns its folder and the path of the marker.
func (p plant) make(t *testing.T) (dir, marker string) {
	t.Helper()
	dir, marker = t.TempDir(), filepath.Join(t.TempDir(), "ran")
	git(t, dir, "init", "--quiet", "-b", "main")
	id := strings.Repeat("12", 20)
	if p.corrupt {
		id = corruptBlob(t, dir)
	}
	if p.bundle {
		from := newRepo(t)
		git(t, from, "bundle", "create", "--quiet", filepath.Join(dir, ".git", "b.bundle"), "HEAD", "main")
		id = git(t, from, "rev-parse", "HEAD")
	}
	dotGit := filepath.Join(dir, ".git")
	config := "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tpartialClone = evil\n[remote \"evil\"]\n\tpromisor = true\n"
	config += fmt.Sprintf(strings.ReplaceAll(p.config, "%[2]s", dotGit), marker)
	f, err := os.OpenFile(filepath.Join(dotGit, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(config)
	if err := cmpErr(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotGit, "refs", "heads", "main"), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, marker
}

// cmpErr is the first error of a and b.
func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// corruptBlob packs a blob in the repository at dir, with an alternate, and breaks the data of
// its entry, and returns its id.
func corruptBlob(t *testing.T, dir string) string {
	t.Helper()
	objects := filepath.Join(dir, ".git", "objects")
	content := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(content, []byte(strings.Repeat("content of the blob ", 4)), 0o644); err != nil {
		t.Fatal(err)
	}
	id := git(t, dir, "hash-object", "-w", content)
	cmd := exec.Command("git", "pack-objects", "-q", filepath.Join(objects, "pack", "pack"))
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(id+"\n")
	name, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(objects, id[:2], id[2:])); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(objects, "pack", "pack-"+strings.TrimSpace(string(name))+".pack")
	data, err := os.ReadFile(pack)
	if err != nil {
		t.Fatal(err)
	}
	// The header of 12 bytes, the entry's type and size, and the start of its zlib stream stay.
	copy(data[18:], "\xff\xff\xff\xff\xff\xff")
	if err := os.Chmod(pack, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pack, data, 0o644); err != nil {
		t.Fatal(err)
	}
	alternate := t.TempDir()
	git(t, alternate, "init", "--quiet", "--bare")
	if err := os.WriteFile(filepath.Join(objects, "info", "alternates"), []byte(filepath.Join(alternate, "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

// ran reports whether the program of a [plant] ran.
func ran(marker string) bool {
	_, err := os.Lstat(marker)
	return err == nil
}

// TestNoProgramRunsForAMissingObject: no program of the repository's configuration runs for the
// read of an object that is missing, whether or not git reads GIT_NO_LAZY_FETCH.
func TestNoProgramRunsForAMissingObject(t *testing.T) {
	for _, p := range plants {
		t.Run(p.name, func(t *testing.T) {
			hermetic(t)
			for _, lazy := range []bool{false, true} {
				for name, command := range map[string]func(dir string, args ...string) (*exec.Cmd, error){
					"Command": func(dir string, args ...string) (*exec.Cmd, error) {
						return hostgit.Command(context.Background(), dir, args...)
					},
					"Unchecked": func(dir string, args ...string) (*exec.Cmd, error) {
						return hostgit.Unchecked(context.Background(), dir, os.Environ(), args...)
					},
				} {
					dir, marker := p.make(t)
					c, err := command(dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
					if err != nil {
						t.Fatal(err)
					}
					if lazy {
						// As a git that does not read it.
						c.Env = slices.DeleteFunc(c.Env, func(kv string) bool { return kv == "GIT_NO_LAZY_FETCH=1" })
					}
					if out, err := c.CombinedOutput(); err == nil {
						t.Errorf("%s: git printed %q, want an error", name, out)
					}
					if ran(marker) {
						t.Errorf("%s: the program ran", name)
					}
				}
			}
		})
	}
}

// TestAPlantRunsItsProgram: the plants of [TestNoProgramRunsForAMissingObject] run their program
// in a git with none of the guards, but for the ones that git itself stops.
func TestAPlantRunsItsProgram(t *testing.T) {
	for _, p := range plants {
		t.Run(p.name, func(t *testing.T) {
			hermetic(t)
			dir, marker := p.make(t)
			c := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
			c.Dir = dir
			if strings.HasSuffix(p.name, " none") {
				c.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=none")
			}
			_ = c.Run()
			if ran(marker) {
				return
			}
			switch p.name {
			case "a vcs with no name", "bundleURI":
				// git reads an empty vcs as none, and runs no program for a bundle.
			case "alternateRefsCommand":
				t.Skip("this git checks the protocol before the objects that it has")
			default:
				t.Error("the program did not run")
			}
		})
	}
}

// TestRemoteAloneHasAProtocol: a fetch from a repository by its path and a push to one are
// stopped in a git of Command and of Unchecked, also when the environment allows every
// protocol, and work in a git of Remote, which fetches no missing object.
func TestRemoteAloneHasAProtocol(t *testing.T) {
	hermetic(t)
	from, to := newRepo(t), t.TempDir()
	git(t, to, "init", "--quiet", "--bare")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file:ssh:https:http:ext")
	ctx := context.Background()
	for _, args := range [][]string{
		{"fetch", "--quiet", from, "HEAD"},
		{"-c", "protocol.allow=always", "push", "--quiet", from, "HEAD:refs/heads/x"},
		{"-c", "protocol.file.allow=always", "ls-remote", from},
	} {
		dir := to
		if slices.Contains(args, "push") {
			dir = newRepo(t)
		}
		command, err := hostgit.Command(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		unchecked, err := hostgit.Unchecked(ctx, dir, os.Environ(), args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []*exec.Cmd{command, unchecked} {
			c.Env = append(c.Env, "GIT_TRACE=1")
			out, err := c.CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("transport 'file' not allowed")) {
				t.Errorf("git %s ran (%v)", args, err)
			}
			for _, kv := range c.Env {
				if hostgit.NoProtocol(kv) && bytes.Contains(out, []byte(strings.TrimPrefix(kv, "GIT_ALLOW_PROTOCOL="))) {
					t.Error("git's output has the name of the one protocol")
				}
			}
		}
		remote, err := hostgit.Remote(ctx, dir, os.Environ(), args...)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := remote.CombinedOutput(); err != nil {
			t.Errorf("git %s: %v\n%s", args, err, out)
		}
		if !slices.Contains(remote.Env, "GIT_NO_LAZY_FETCH=1") {
			t.Error("a git of Remote fetches a missing object")
		}
	}
	// The plain variables stay the person's in a git of Remote.
	t.Setenv("GIT_ALLOW_PROTOCOL", "https")
	remote, err := hostgit.Remote(ctx, to, os.Environ(), "fetch", "--quiet", from, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.CombinedOutput(); err == nil {
		t.Error("a git of Remote fetched with another protocol than the person allows")
	}
}

// TestLazyFetchIsTheGitOfThePersonsWorktreeCommandsAlone: a git of LazyFetch has the flags and
// the environment of a git of Command without what stops a fetch, and no code outside
// internal/worktree makes one.
func TestLazyFetchIsTheGitOfThePersonsWorktreeCommandsAlone(t *testing.T) {
	hermetic(t)
	ctx, dir := context.Background(), newRepo(t)
	command, err := hostgit.Command(ctx, dir, "status")
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := hostgit.LazyFetch(ctx, dir, os.Environ(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lazy.Env, hostgit.Env()) || lazy.Path != command.Path || lazy.Dir != dir {
		t.Errorf("git %s in %s with %q, want the environment %q", lazy.Path, lazy.Dir, lazy.Env, hostgit.Env())
	}
	n := len(lazy.Args) - 1
	stops := []string{"-c", "fetch.bundleURI=", "-c", "core.alternateRefsCommand=:", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "status"}
	if lazy.Args[n] != "status" || !slices.Equal(command.Args, slices.Concat(lazy.Args[:n], stops)) {
		t.Errorf("git runs with %q, and git of Command with %q", lazy.Args, command.Args)
	}
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if bytes.Contains(data, []byte("hostgit.LazyFetch")) && filepath.Base(filepath.Dir(path)) != "worktree" {
				t.Errorf("%s makes a git of LazyFetch", path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestNoProtocolIsOneNameOfTheProcess: each git has the same variable, whose protocol has a
// name that no configuration can have, and no other value of the variable is it.
func TestNoProtocolIsOneNameOfTheProcess(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	var values []string
	for range 2 {
		c, err := hostgit.Command(context.Background(), dir, "version")
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") {
				values = append(values, kv)
			}
		}
	}
	if len(values) != 2 || values[0] != values[1] || !hostgit.NoProtocol(values[0]) {
		t.Fatal("the gits of a process do not have the one variable")
	}
	if name := strings.TrimPrefix(values[0], "GIT_ALLOW_PROTOCOL="); len(name) < 26 || strings.Contains(name, ":") {
		t.Error("the protocol's name is short, or names two")
	}
	for _, kv := range []string{"GIT_ALLOW_PROTOCOL=", "GIT_ALLOW_PROTOCOL=none", "GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=1"} {
		if hostgit.NoProtocol(kv) {
			t.Errorf("%s is the variable", kv)
		}
	}
}
