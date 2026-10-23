package compose_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// gitRepo commits files into a new git repository on branch main and returns its file
// URL. A file of the hello example is copied in under the folder copyTo, when set.
func gitRepo(t *testing.T, copyTo string, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if copyTo != "" {
		to := filepath.Join(dir, filepath.FromSlash(copyTo))
		if err := os.MkdirAll(to, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("cp", "-R", "../../examples/hello/.", to).CombinedOutput(); err != nil {
			t.Fatalf("copy: %v\n%s", err, out)
		}
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "first"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return "file://" + dir
}

// TestExampleStackComposes is the hello example's qory-stack.yaml: read where it stands
// in a repository, and fetched by reference from one, it composes the hello and world
// modules with the greet skill from world. Fetched, each module is a git source of the
// reference's repository on the commit the reference resolved to.
func TestExampleStackComposes(t *testing.T) {
	isolateAll(t)
	url := gitRepo(t, "examples/hello", nil)
	dir := strings.TrimPrefix(url, "file://")
	p, err := stack.Load(filepath.Join(dir, "examples", "hello", stack.FileName))
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = stack.Runtimes{"claude"}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	wantHelloWorld(t, res)

	opts := compose.Options{Cache: t.TempDir(), Memo: source.NewMemo()}
	ref := stack.Source{Git: url, Ref: "main", Path: "examples/hello"}
	p, fetched, err := compose.LoadReference(ref, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Commit) != 40 {
		t.Errorf("the reference resolved to %+v, want a commit", fetched)
	}
	for _, l := range p.Modules {
		if want := (stack.Source{Git: url, Ref: "main", Path: "examples/hello/modules/" + l.Name}); l.Source != want {
			t.Errorf("module %s reads %+v, want %+v", l.Name, l.Source, want)
		}
	}
	p.Target.Runtimes = stack.Runtimes{"claude"}
	res, err = compose.ComposeWith(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	wantHelloWorld(t, res)
	for _, m := range res.Modules {
		if m.Commit != fetched.Commit {
			t.Errorf("module %s is on %s, want the reference's %s", m.Name, m.Commit, fetched.Commit)
		}
	}
}

// wantHelloWorld fails the test unless res has the modules hello and world, in that
// order, and the greet skill from world.
func wantHelloWorld(t *testing.T, res *compose.Result) {
	t.Helper()
	if len(res.Modules) != 2 || res.Modules[0].Name != "hello" || res.Modules[1].Name != "world" {
		t.Fatalf("modules: %+v", res.Modules)
	}
	for _, e := range res.Entries {
		if e.Kind == "skills" && e.Name == "greet" && e.Module != "world" {
			t.Errorf("skills/greet comes from %s, want world", e.Module)
		}
	}
}

// TestLoadReference is a stack fetched by reference whose modules are listed by name, by
// a path, by a git source, and by a path to a repository named ..lib with an export
// selected: all but the git source become git sources inside the fetched tree, the
// last at the export's directory, and the git source stays as it is.
func TestLoadReference(t *testing.T) {
	isolateAll(t)
	url := gitRepo(t, "", map[string]string{
		"stacks/web/" + stack.FileName:                "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n  - name: web\n    source: {path: ../../lib/web}\n  - name: extra\n    source: {git: https://git.example.com/example-org/extra, ref: v1}\n  - name: shop\n    source: {path: ../../..lib, module: shop}\n",
		"modules/core/qory-module.yaml":               "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"lib/web/qory-module.yaml":                    "apiVersion: qory.dev/v1alpha1\nname: web\n",
		"..lib/qory.yaml":                             "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: ./harness\n  modules: [shop]\n",
		"..lib/harness/modules/shop/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: shop\n",
	})
	p, _, err := compose.LoadReference(stack.Source{Git: url, Ref: "main", Path: "stacks/web"}, compose.Options{Cache: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := []stack.Source{
		{Git: url, Ref: "main", Path: "modules/core"},
		{Git: url, Ref: "main", Path: "lib/web"},
		{Git: "https://git.example.com/example-org/extra", Ref: "v1"},
		{Git: url, Ref: "main", Path: "..lib/harness/modules/shop"},
	}
	if len(p.Modules) != len(want) {
		t.Fatalf("modules: %+v", p.Modules)
	}
	for i, l := range p.Modules {
		if l.Source != want[i] {
			t.Errorf("module %s reads %+v, want %+v", l.Name, l.Source, want[i])
		}
	}
}

// TestLoadReferenceErrors is a reference whose folder is not in the tree, a folder
// without a stack file, a file in place of the folder, a folder that is a link out of the
// tree, a stack file that is a link out of the tree, and a stack with a module outside
// the tree, by a path and by a link, and a module exported from a folder whose qory.yaml
// is a link out of the tree: each error starts with its fix, and no stack file or
// qory.yaml outside the tree is read.
func TestLoadReferenceErrors(t *testing.T) {
	isolateAll(t)
	outside := t.TempDir()
	url := gitRepo(t, "", map[string]string{
		"stacks/web/" + stack.FileName:               "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: web\n    source: {path: ../../../outside}\n",
		"stacks/abs/" + stack.FileName:               "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: web\n    source: {path: " + outside + "}\n",
		"stacks/empty/README.md":                     "no stack here\n",
		"stacks/file":                                "a file\n",
		"stacks/linked/" + stack.FileName:            "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: web\n    source: {path: web}\n",
		"stacks/shop/" + stack.FileName:              "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: shop\n    source: {path: ../../shop, module: shop}\n",
		"shop/harness/modules/shop/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: shop\n",
	})
	dir := strings.TrimPrefix(url, "file://")
	// A stack file outside the tree, which no reference reads.
	if err := os.WriteFile(filepath.Join(outside, stack.FileName), []byte("apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A qory.yaml outside the tree that exports module shop from a folder outside it.
	if err := os.MkdirAll(filepath.Join(outside, "elsewhere", "modules", "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "qory.yaml"), []byte("apiVersion: qory.dev/v1alpha1\nexports:\n  dir: ./elsewhere\n  modules: [shop]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "stacks", "filelink"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{
		filepath.Join(dir, "stacks", "linked", "web"):            outside,
		filepath.Join(dir, "stacks", "dirlink"):                  outside,
		filepath.Join(dir, "stacks", "filelink", stack.FileName): filepath.Join(outside, stack.FileName),
		filepath.Join(dir, "shop", "qory.yaml"):                  filepath.Join(outside, "qory.yaml"),
	} {
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "link"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v\n%s", err, out)
		}
	}
	for _, c := range []struct{ folder, want string }{
		{"stacks/missing", "check the folder in -f " + url + "#main:stacks/missing: " + url + " has no stacks/missing at main"},
		{"stacks/empty", "check the folder in -f " + url + "#main:stacks/empty: " + url + " has no qory-stack.yaml in stacks/empty at main"},
		{"stacks/file", "check the folder in -f " + url + "#main:stacks/file: stacks/file is not a directory in " + url + " at main"},
		{"stacks/web", "give module web a path inside the repository, or a git source, in the stack of -f " + url + "#main:stacks/web: qory reads the modules of a stack fetched by reference from the fetched tree only, and ../../../outside is outside it"},
		{"stacks/abs", "give module web a path inside the repository, or a git source, in the stack of -f " + url + "#main:stacks/abs: "},
		{"stacks/linked", "give module web a path inside the repository, or a git source, in the stack of -f " + url + "#main:stacks/linked: "},
		{"stacks/dirlink", "check the folder in -f " + url + "#main:stacks/dirlink: stacks/dirlink in " + url + " at main links outside the repository"},
		{"stacks/filelink", "check the folder in -f " + url + "#main:stacks/filelink: stacks/filelink/qory-stack.yaml in " + url + " at main links outside the repository"},
		{"stacks/shop", "check the export of module shop in the stack of -f " + url + "#main:stacks/shop: shop/qory.yaml in " + url + " at main links outside the repository"},
	} {
		_, _, err := compose.LoadReference(stack.Source{Git: url, Ref: "main", Path: c.folder}, compose.Options{Cache: t.TempDir()})
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant the prefix %q", c.folder, err, c.want)
		}
	}
}

// TestLoadReferenceRootConfigLinksOut is a reference to a repository whose qory.yaml at
// the root is a link to a file outside the tree that sets exports.dir: the error starts
// with its fix, and the stack is not read with the modules directory that file sets.
func TestLoadReferenceRootConfigLinksOut(t *testing.T) {
	isolateAll(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "q.yaml"), []byte("apiVersion: qory.dev/v1alpha1\nexports:\n  dir: ./elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := gitRepo(t, "", map[string]string{
		"stacks/web/" + stack.FileName:  "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n",
		"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\n",
	})
	dir := strings.TrimPrefix(url, "file://")
	if err := os.Symlink(filepath.Join(outside, "q.yaml"), filepath.Join(dir, "qory.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "link"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v\n%s", err, out)
		}
	}
	p, _, err := compose.LoadReference(stack.Source{Git: url, Ref: "main", Path: "stacks/web"}, compose.Options{Cache: t.TempDir()})
	want := "check the qory.yaml in -f " + url + "#main:stacks/web: qory.yaml in " + url + " at main links outside the repository"
	if err == nil || err.Error() != want {
		t.Fatalf("got %+v, %v\nwant the error %q", p, err, want)
	}
}
