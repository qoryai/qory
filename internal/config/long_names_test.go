package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/config"
)

// TestRefusalsKeepTheFixLineShortForLongNames is each refusal of forager.yaml and of
// qory.yaml that prints an entry's name in its first line, with a name of 64 characters:
// the first line has at most 100 characters, and some line has the whole name.
func TestRefusalsKeepTheFixLineShortForLongNames(t *testing.T) {
	hermetic(t)
	name := "n" + strings.Repeat("x", 63)
	host := strings.Repeat("h", 60) + ".example"
	for label, c := range map[string]struct{ body, long string }{
		"an image defined twice":      {"wall:\n  adapter: docker\n  images:\n    " + name + ": {ref: a}\n    " + name + ": {ref: b}\n", name},
		"an image with no ref":        {"wall:\n  adapter: docker\n  images:\n    " + name + ": {runtime: runc}\n", name},
		"an image that is no mapping": {"wall:\n  adapter: docker\n  images:\n    " + name + ": x\n", name},
		"an integration twice":        {"gateway:\n  integrations:\n    " + name + ": {}\n    " + name + ": {}\n", name},
		"an integration no mapping":   {"gateway:\n  integrations:\n    " + name + ": 1\n", name},
		"a credential file":           {"gateway:\n  credentials:\n    " + name + ":\n      file: relative\n", name},
		"a tool command":              {"gateway:\n  tools:\n    " + name + ":\n      command: [gh]\n", name},
		"a path host twice":           {"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + host + ": [/v1]\n      " + host + ": [/v2]\n", host},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil {
			t.Errorf("%s: no refusal", label)
			continue
		}
		checkFixLine(t, label, err, c.long, path)
	}
	for label, c := range map[string]struct{ body, long string }{
		"a launch command":  {"harness: {launch: {" + name + ": {command: \" \"}}}\n", name},
		"a launch argument": {"harness: {launch: {" + name + ": {args: [[]]}}}\n", name},
	} {
		hermetic(t)
		root := t.TempDir()
		path := filepath.Join(root, "qory.yaml")
		write(t, path, c.body)
		_, err := config.Load(root, true)
		if err == nil {
			t.Errorf("%s: no refusal", label)
			continue
		}
		checkFixLine(t, label, err, c.long, path)
	}
}

// checkFixLine fails the test when the first line of err has more than 100 characters,
// when no line has the whole name long, or when err does not name the file at path.
func checkFixLine(t *testing.T, label string, err error, long, path string) {
	t.Helper()
	lines := strings.Split(err.Error(), "\n")
	if n := utf8.RuneCountInString(lines[0]); n > 100 {
		t.Errorf("%s: the first line has %d characters: %s", label, n, lines[0])
	}
	if !strings.Contains(err.Error(), long) {
		t.Errorf("%s: no line has the whole name: %v", label, err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("%s: the refusal does not name the file %s: %v", label, path, err)
	}
}
