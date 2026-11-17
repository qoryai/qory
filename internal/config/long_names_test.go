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
	// 24 characters is the longest name that is printed whole, 64 the longest a name has.
	for _, n := range []int{24, 64} {
		name := "n" + strings.Repeat("x", n-1)
		host := strings.Repeat("h", n-8) + ".example"
		if n == 64 {
			host = strings.Repeat("h", 60) + ".example"
		}
		longNameCases(t, name, host)
	}
}

// longNameCases checks each refusal that prints name, an entry's name, or host, a host of
// gateway.egress.paths, in its first line.
func longNameCases(t *testing.T, name, host string) {
	t.Helper()
	hermetic(t)
	for label, c := range map[string]struct{ body, long string }{
		"an image defined twice":        {"wall:\n  adapter: docker\n  images:\n    " + name + ": {ref: a}\n    " + name + ": {ref: b}\n", name},
		"an image with no ref":          {"wall:\n  adapter: docker\n  images:\n    " + name + ": {runtime: runc}\n", name},
		"an image that is no mapping":   {"wall:\n  adapter: docker\n  images:\n    " + name + ": x\n", name},
		"an image that needs a runtime": {"wall:\n  adapter: docker\n  images:\n    " + name + ": {ref: a, docker: true}\n", name},
		"a prebuild in no docker image": {"wall:\n  adapter: docker\n  images:\n    " + name + ": {ref: a}\n  prebuild_image: " + name + "\n", name},
		"an integration twice":          {"gateway:\n  integrations:\n    " + name + ": {}\n    " + name + ": {}\n", name},
		"an integration no mapping":     {"gateway:\n  integrations:\n    " + name + ": 1\n", name},
		"an integration program":        {"gateway:\n  integrations:\n    " + name + ": {program: [x]}\n", name},
		"a credential file":             {"gateway:\n  credentials:\n    " + name + ":\n      file: relative\n", name},
		"a credential adapter":          {"gateway:\n  credentials:\n    " + name + ":\n      adapter: [relative]\n", name},
		"a tool command":                {"gateway:\n  tools:\n    " + name + ":\n      command: [gh]\n", name},
		"a path host twice":             {"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + host + ": [/v1]\n      " + host + ": [/v2]\n", host},
		"a path of a host":              {"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + host + ": [v1]\n", host},
		"a list of paths":               {"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + host + ": x\n", host},
		"a path listed twice":           {"gateway:\n  egress:\n    mode: observe\n    paths:\n      " + host + ": [/v1, /v1]\n", host},
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
		"a launch variable": {"harness: {launch: {" + name + ": {env: {1A: x}}}}\n", name},
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
