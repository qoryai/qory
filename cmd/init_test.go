package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
)

// TestInit writes the example from the repository's own examples directory into an empty
// directory, composes it, and refuses to write twice.
func TestInit(t *testing.T) {
	cmd.Example = exampleFromDisk(t)
	dir := emptyDir(t)
	root := cmd.Root()
	root.SetArgs([]string{"harness", "init"})
	root.SetOut(&strings.Builder{})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(filepath.Join(dir, profile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compose.Compose(p); err != nil {
		t.Fatal(err)
	}
	root = cmd.Root()
	root.SetArgs([]string{"harness", "init"})
	root.SetOut(&strings.Builder{})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "does not overwrite") {
		t.Fatalf("second init: %v", err)
	}
}

// TestInitLeavesEveryExistingFile is a directory with its own README.md: init refuses
// rather than writing the example's over it.
func TestInitLeavesEveryExistingFile(t *testing.T) {
	example := exampleFromDisk(t)
	dir := emptyDir(t)
	setExample(t, example)
	writeFile(t, filepath.Join(dir, "README.md"), "# my project\n")
	out, err := run(t, "harness", "init")
	if err == nil || !strings.Contains(err.Error(), "already has a README.md; qory does not overwrite it") {
		t.Fatalf("init over a README: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(data) != "# my project\n" {
		t.Errorf("README.md was replaced: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, profile.FileName)); err == nil {
		t.Error("the profile was written although init refused")
	}
}

// TestInitWithoutAName writes the example where nothing on the machine names the person:
// no git identity and no gh account. The output counts the files instead of greeting.
func TestInitWithoutAName(t *testing.T) {
	example := exampleFromDisk(t)
	dir := emptyDir(t)
	setExample(t, example)

	out, err := run(t, "harness", "init")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "wrote 9 files", profile.FileName, "layers/hello/skills/greet/SKILL.md", "next", "qory harness compose")
	if strings.Contains(out, "Hello,") {
		t.Errorf("nothing names the person and the output greets one:\n%s", out)
	}
	if _, err := profile.Load(filepath.Join(dir, profile.FileName)); err != nil {
		t.Error(err)
	}
}

// TestInitWithoutAnExample checks the error of a build that carries no example.
func TestInitWithoutAnExample(t *testing.T) {
	emptyDir(t)
	setExample(t, nil)

	out, err := run(t, "harness", "init")
	if err == nil {
		t.Fatalf("init without an example: no error\n%s", out)
	}
	wants(t, err.Error(), "this build carries no example")
}

// setExample puts a file tree in place as the example init writes, and puts back whatever
// was there when the test ends.
func setExample(t *testing.T, tree fs.FS) {
	t.Helper()
	was := cmd.Example
	t.Cleanup(func() { cmd.Example = was })
	cmd.Example = tree
}

func exampleFromDisk(t *testing.T) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	base := filepath.Join("..", cmd.ExampleRoot)
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("..", "examples", "hello"), path)
		m[filepath.ToSlash(filepath.Join(cmd.ExampleRoot, rel))] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
