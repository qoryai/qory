package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// fixtures is the contract fixture directory, resolved from the working directory the test
// binary starts in, so a test can copy a fixture after it has changed directory.
var fixtures = fixtureDir()

func fixtureDir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(dir, "..", "contracts", "harness", "v1", "fixtures")
}

// twoLayerEntries are the entries the two-layers fixture composes, kind/name to the layer
// that provides each one.
var twoLayerEntries = map[string]string{
	"agents/reviewer":     "core",
	"commands/ship":       "core",
	"hooks/guard.sh":      "core",
	"output-styles/terse": "core",
	"skills/e2e":          "nextjs",
	"skills/review":       "core",
	"skills/test":         "core",
}

// emptyDir makes an empty directory the working directory, in an environment that reads
// nothing of the machine's own: HOME, git's global config and gh's config directory all
// point at temporary paths, git's system config is off, and colour is off. Nothing there
// names the person, so a command that greets one prints no name.
func emptyDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "gh"))
	t.Setenv("NO_COLOR", "1")
	dir := tempDir(t)
	t.Chdir(dir)
	return dir
}

// newCheckout makes a git checkout in a temporary directory and makes it the working
// directory, in the environment [emptyDir] sets. The checkout carries its own identity and
// an origin remote, so no command reads the machine's git identity and the title of every
// command is the same in every run.
func newCheckout(t *testing.T) string {
	t.Helper()
	root := emptyDir(t)
	runGit(t, root, "init", "--quiet", "--initial-branch=main")
	runGit(t, root, "config", "user.name", "Tester")
	runGit(t, root, "config", "user.email", "tester@example.com")
	runGit(t, root, "remote", "add", "origin", "https://git.example.com/acme/app.git")
	return root
}

// tempDir is a temporary directory with its symlinks resolved, so it reads the way git
// reports a checkout root.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// copyFixture copies one contract fixture into dir, without the expected output that the
// contract tests compare against.
func copyFixture(t *testing.T, name, dir string) {
	t.Helper()
	src := filepath.Join(fixtures, name)
	if out, err := exec.Command("cp", "-R", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", name, err, out)
	}
	if err := os.RemoveAll(filepath.Join(dir, "expected")); err != nil {
		t.Fatal(err)
	}
}

// run executes one qory command line and returns everything it printed. Output and errors
// go to one writer, the way a person reads them.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

// wants fails the test for every string the output does not carry.
func wants(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

// lacks fails the test for every string the output carries.
func lacks(t *testing.T, out string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(out, u) {
			t.Errorf("output carries %q and should not:\n%s", u, out)
		}
	}
}

// entryTable reads the entry rows of a printed table, kind/name to layer. The rows of two
// columns whose first column names a kind and a name are the entries; a field row such as
// "home  .qory/harness" is not one.
func entryTable(out string) map[string]string {
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.Contains(f[0], "/") {
			rows[f[0]] = f[1]
		}
	}
	return rows
}

// fieldRows reads the printed rows, key to the values printed under it, in order. A field
// row and a table row read alike: two spaces of indent, the key, two or more spaces, the
// value. A value that holds several items keeps the two spaces between them.
func fieldRows(out string) map[string][]string {
	rows := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		body, ok := strings.CutPrefix(line, "  ")
		if !ok || strings.HasPrefix(body, " ") {
			continue
		}
		key, rest, ok := strings.Cut(body, "  ")
		if !ok {
			continue
		}
		rows[key] = append(rows[key], strings.TrimSpace(rest))
	}
	return rows
}

// wantsRow fails the test when the key did not carry the value exactly once.
func wantsRow(t *testing.T, out, key, value string) {
	t.Helper()
	got := fieldRows(out)[key]
	if len(got) != 1 || got[0] != value {
		t.Errorf("row %q = %q, want [%q]:\n%s", key, got, value, out)
	}
}

// snapshot records the checkout tree: every path to its kind, a link to its target and a
// file to the digest of its content. The git directory is left out, because git writes
// there on its own. Links are read and not followed, so a snapshot never leaves the tree.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".git" {
			return fs.SkipDir
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			tree[rel] = "link -> " + target
		case d.IsDir():
			tree[rel] = "dir"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			tree[rel] = "file " + hex.EncodeToString(sum[:8])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// linkTarget is the target of a relative link in the checkout. It fails the test when the
// path is not a link, when the link is absolute, or when it does not resolve.
func linkTarget(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("%s is not a link", name)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(target) {
		t.Errorf("%s links to %s, which is absolute", name, target)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("%s links to %s, which does not resolve: %v", name, target, err)
	}
	return target
}

// gone fails the test when any of the paths still exists in the checkout.
func gone(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s is still there", name)
		}
	}
}
