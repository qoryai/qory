package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/runner/accesskey"

	"github.com/qoryai/qory/internal/config"
)

const enrolPinKey = "rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc"

var enrolment = config.Enrolment{URL: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef", Pin: accesskey.Pin{{Alg: "ed25519", PublicKey: enrolPinKey}}}

// writeEnrolment writes content to a runner file, mode m, applies the enrolment and
// returns what the file holds then.
func writeEnrolment(t *testing.T, content string, m os.FileMode, e config.Enrolment) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.yaml")
	if content != "-" {
		if err := os.WriteFile(path, []byte(content), m); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, m)
	}
	if err := config.WriteEnrolment(path, e); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if content == "-" {
		m = 0o600
	}
	if info.Mode().Perm() != m {
		t.Errorf("the file is mode %v, want %v", info.Mode().Perm(), m)
	}
	return string(b), nil
}

// TestWriteEnrolmentKeepsTheFile is the server section written into runner files of
// every shape: the comments, the order and every other key stay; a url there stays, an
// access_key_id there is replaced, a pin there stays; a file that does not exist is
// created mode 0600 with the apiVersion line; a file of comments alone keeps them; an
// existing file keeps its mode.
func TestWriteEnrolmentKeepsTheFile(t *testing.T) {
	for _, c := range []struct {
		name, in string
		mode     os.FileMode
		e        config.Enrolment
		want     string
	}{
		{"no file", "-", 0, enrolment,
			"apiVersion: qory.dev/v1alpha1\nserver:\n  url: https://apiary.example\n  access_key_id: ak_0123456789abcdef\n  apiary_public_key:\n    - {alg: ed25519, public_key: " + enrolPinKey + "}\n"},
		{"empty", "", 0o600, enrolment,
			"apiVersion: qory.dev/v1alpha1\nserver:\n  url: https://apiary.example\n"},
		{"comments alone", "# Mine.\n\n", 0o644, enrolment,
			"# Mine.\napiVersion: qory.dev/v1alpha1\nserver:\n"},
		{"sections and comments", "# Mine.\napiVersion: qory.dev/v1alpha1\negress:\n  mode: observe # for now\n# Shown.\ninstance:\n  name: build-01\n", 0o640, enrolment,
			"# Mine.\napiVersion: qory.dev/v1alpha1\negress:\n  mode: observe # for now\n# Shown.\ninstance:\n  name: build-01\nserver:\n  url: https://apiary.example\n  access_key_id: ak_0123456789abcdef\n  apiary_public_key:\n    - {alg: ed25519, public_key: " + enrolPinKey + "}\n"},
		{"a server section", "server: # the server\n  url: http://127.0.0.1:8080\n  access_key_id: ak_0000000000000000\n  apiary_public_key: [{alg: ed25519, public_key: kept}] # by hand\nrun:\n  timeout: 1h\n", 0o600, enrolment,
			"server: # the server\n  url: http://127.0.0.1:8080\n  access_key_id: ak_0123456789abcdef\n  apiary_public_key: [{alg: ed25519, public_key: kept}] # by hand\nrun:\n  timeout: 1h\n"},
		{"no pin given", "server:\n  url: https://apiary.example\n", 0o600, config.Enrolment{URL: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef"},
			"server:\n  url: https://apiary.example\n  access_key_id: ak_0123456789abcdef\n"},
		{"server null", "server:\n", 0o600, config.Enrolment{URL: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef"},
			"server:\n  url: https://apiary.example\n  access_key_id: ak_0123456789abcdef\n"},
	} {
		got, err := writeEnrolment(t, c.in, c.mode, c.e)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:\n%s\nwant it to contain:\n%s", c.name, got, c.want)
		}
		if strings.Count(got, "access_key_id:") != 1 {
			t.Errorf("%s: access_key_id twice:\n%s", c.name, got)
		}
	}
	for _, in := range []string{"server: https://apiary.example\n", "- a\n- b\n", "a: [\n"} {
		if _, err := writeEnrolment(t, in, 0o600, enrolment); err == nil {
			t.Errorf("%q: accepted", in)
		}
	}
}

// TestWriteEnrolmentFollowsALink is a runner file that is a link, kept in a dotfiles
// checkout say: the file it names is written, and the link stays.
func TestWriteEnrolmentFollowsALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "runner.yaml")
	os.MkdirAll(filepath.Dir(target), 0o700)
	if err := os.WriteFile(target, []byte("instance:\n  name: build-01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "runner.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteEnrolment(link, enrolment); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), "access_key_id: ak_0123456789abcdef") || !strings.Contains(string(b), "name: build-01") {
		t.Errorf("the file:\n%s", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("a temporary file is left: %v", entries)
	}
}
