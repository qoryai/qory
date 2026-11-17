package cmd_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoTextSaysToContactSupport reads every text file of the repository, code, tests, docs and the
// CHANGELOG, and finds no text that sends the reader to the vendor's support desk, in any case and across a
// line break (the phrase is split here, so this file does not match it): what to do is Qory Apiary's text for each reader, and qory prints the cause alone.
func TestNoTextSendsTheReaderToSupport(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	phrase := regexp.MustCompile(`(?i)contact\s+` + `support`)
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(body, 0) >= 0 {
			return nil
		}
		checked++
		if at := phrase.FindIndex(body); at != nil {
			rel, _ := filepath.Rel(root, path)
			line := 1 + strings.Count(string(body[:at[0]]), "\n")
			t.Errorf("%s:%d sends the reader to support; say the cause alone", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Errorf("the walk read %d files of %s, want the whole repository", checked, root)
	}
}
