package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestTheEmbeddedExampleHasNoStackFile is the hello example as main embeds it: every
// file of examples/hello but its qory-stack.yaml, since a folder with that file and
// a qory.yaml that lists modules does not compose.
func TestTheEmbeddedExampleHasNoStackFile(t *testing.T) {
	base := filepath.Join("examples", "hello")
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		_, statErr := fs.Stat(example, filepath.ToSlash(path))
		if filepath.Base(path) == "qory-stack.yaml" {
			if statErr == nil {
				t.Errorf("the binary contains %s", path)
			}
			return nil
		}
		if statErr != nil {
			t.Errorf("the binary lacks %s: %v", path, statErr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "qory-stack.yaml")); err != nil {
		t.Errorf("examples/hello has no qory-stack.yaml: %v", err)
	}
}
