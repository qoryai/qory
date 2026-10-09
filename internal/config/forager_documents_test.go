package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// moreThanOne is the refusal of a forager.yaml with a second YAML document, after the
// file.
const moreThanOne = ": holds more than one YAML document; forager.yaml is one document, and qory would read only the first"

// TestAForagerFileIsOneDocument is forager.yaml with --- at its start, at its end and in
// its middle. A --- that starts the one document, and a ... that ends it, read; a second
// document is refused wherever it stands, an empty one after a trailing --- among them,
// by every reader of the file, so a session.gateway in the second is never ignored.
func TestAForagerFileIsOneDocument(t *testing.T) {
	hermetic(t)
	const instance = "session:\n  instance:\n    name: build-01\n"
	const gateway = "session:\n  gateway:\n    url: https://gateway.example\n"
	for _, body := range []string{
		"---\n" + instance,
		"--- # the one document\n" + instance,
		"# a comment\n---\n" + instance,
		instance + "...\n",
		instance + "...\n# a comment\n",
	} {
		foragerFile(t, body)
		if r, err := config.LoadForager(); err != nil || r.InstanceName != "build-01" {
			t.Errorf("%q: %+v, %v; want it read", body, r, err)
		}
		if r, err := config.LoadForagerInstance(); err != nil || r.InstanceName != "build-01" {
			t.Errorf("%q: the instance alone: %+v, %v; want it read", body, r, err)
		}
	}
	for _, body := range []string{"", "---\n", "# a comment\n"} {
		foragerFile(t, body)
		if r, err := config.LoadForager(); err != nil || r.SessionGateway != nil {
			t.Errorf("%q: %+v, %v; want it read as no settings", body, r, err)
		}
	}
	for _, body := range []string{
		instance + "---\n",
		instance + "---",
		instance + "--- \n",
		instance + "---\n# a comment\n",
		instance + "--- ~\n",
		instance + "...\n---\n",
		"---\n---\n",
		"---\n" + instance + "---\n",
		instance + "---\n" + gateway,
		"---\n" + instance + "---\n" + gateway,
		instance + "...\n" + "---\n" + gateway,
		gateway + "---\n" + instance,
		instance + "---\n" + gateway + "---\n" + instance,
		instance + "---\n: [\n",
	} {
		path := foragerFile(t, body)
		if _, err := config.LoadForager(); err == nil || err.Error() != path+moreThanOne {
			t.Errorf("%q: %v, want %q", body, err, path+moreThanOne)
		}
		if _, err := config.LoadForagerInstance(); err == nil || err.Error() != path+moreThanOne {
			t.Errorf("%q: the instance alone: %v, want %q", body, err, path+moreThanOne)
		}
		if _, err := config.Load(t.TempDir(), true); err == nil || err.Error() != path+moreThanOne {
			t.Errorf("%q: the configuration: %v, want %q", body, err, path+moreThanOne)
		}
	}
}

// TestWriteEnrolmentRefusesASecondDocument is an enrolment written into a forager.yaml
// with a second document: it is refused, and the file is left as it was, since the
// encoder would write the first document alone.
func TestWriteEnrolmentRefusesASecondDocument(t *testing.T) {
	const body = "session:\n  instance:\n    name: build-01\n---\nsession:\n  gateway:\n    url: https://gateway.example\n"
	path := filepath.Join(t.TempDir(), "forager.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteEnrolment(path, enrolment); err == nil || err.Error() != path+moreThanOne {
		t.Errorf("%v, want %q", err, path+moreThanOne)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != body {
		t.Errorf("the file holds %q, %v; want it as it was", b, err)
	}
}
