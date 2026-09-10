package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscover finds the stack in an ancestor directory when the checkout has none.
func TestDiscover(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(checkout); err == nil {
		t.Fatal("found a stack where there is none")
	}
	want := filepath.Join(base, "code", FileName)
	if err := os.WriteFile(want, []byte("apiVersion: qory.ai/v1alpha1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverTakesTheComposeFileAndRefusesBoth finds qory-compose.yaml where there is
// no qory-stack.yaml, and refuses a directory holding both, naming them.
func TestDiscoverTakesTheComposeFileAndRefusesBoth(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, ComposeFileName)
	if err := os.WriteFile(compose, []byte("apiVersion: qory.ai/v1alpha1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(dir)
	if err != nil || got != compose {
		t.Fatalf("got %s, %v; want %s", got, err, compose)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("apiVersion: qory.ai/v1alpha1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Discover(dir)
	want := dir + " holds both " + FileName + " and " + ComposeFileName + "; a directory holds one of the two"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestLoadReadsTheDocumentItsFileNameSays refuses a stack with extends, a compose file
// without extends, and one with a target or an extending block, and reads a compose file
// that names its base.
func TestLoadReadsTheDocumentItsFileNameSays(t *testing.T) {
	dir := t.TempDir()
	load := func(name, body string) error {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		return err
	}
	head := "apiVersion: qory.ai/v1alpha1\n"
	cases := []struct{ name, body, want string }{
		{FileName, head + "extends: {path: ../base}\nmodules: [{name: app}]\n", "extends is not a stack's; a compose file, qory-compose.yaml, extends a stack"},
		{ComposeFileName, head + "modules: [{name: app}]\n", "extends is empty; a compose file names the stack it extends"},
		{ComposeFileName, head + "extends: {path: ../base}\ntarget: {runtime: claude}\nmodules: [{name: app}]\n", "target is the base stack's; a compose file does not set it"},
		{ComposeFileName, head + "extends: {path: ../base}\nextending: {kinds: [skills]}\nmodules: [{name: app}]\n", "extending is the base stack's; a compose file does not set it"},
		{ComposeFileName, head + "extends: {path: ../base}\nmodules: [{name: app}]\n", ""},
	}
	for _, c := range cases {
		err := load(c.name, c.body)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.body, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", c.body, err, c.want)
		}
	}
}
