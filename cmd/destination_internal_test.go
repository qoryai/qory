package cmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
)

// TestCloneFailedNamesEachCause is each way a clone fails, by the output and the end of
// git: the line of the proxy for its 403 and 407, the line of the integration's access
// for the host's 401, 403 and 404, and the line to run Explore again with each other
// cause, in words that contain no part of git's output.
func TestCloneFailedNamesEachCause(t *testing.T) {
	const id = "tg_web0000000000000"
	const (
		proxy  = "Ask the instance admin of Qory Apiary to let the proxy reach the host of " + id + "."
		access = "Give the integration access to " + id + " in Qory Apiary, then click Run Explore again.\n  The host returned "
		again  = "Run Explore again later: the clone of " + id + " failed: "
	)
	for _, c := range []struct {
		err  error
		want string
	}{
		{&preparation.CloneError{Exit: 128, Output: "fatal: unable to access 'https://git.test/a.git/': CONNECT tunnel failed, response 403"}, proxy},
		{&preparation.CloneError{Exit: 128, Output: "fatal: unable to access 'https://git.test/a.git/': CONNECT tunnel failed, response 407"}, proxy},
		{&preparation.CloneError{Exit: 128, Output: "fatal: unable to access 'https://git.test/a.git/': CONNECT tunnel failed, response 502"}, again + "the proxy returned 502."},
		{&preparation.CloneError{Exit: 128, Output: "fatal: could not read Username for 'https://git.test': terminal prompts disabled"}, access + "401 to the clone."},
		{&preparation.CloneError{Exit: 128, Output: "fatal: unable to access 'https://git.test/a.git/': The requested URL returned error: 403"}, access + "403 to the clone."},
		{&preparation.CloneError{Exit: 128, Output: "remote: Not found\nfatal: repository 'https://git.test/a.git/' not found"}, access + "404 to the clone."},
		{&preparation.CloneError{Exit: 128, Output: "fatal: unable to access 'https://git.test/a.git/': The requested URL returned error: 503"}, again + "the host returned 503."},
		{&preparation.CloneError{Exit: -1, Stopped: true}, again + "git was stopped."},
		{&preparation.CloneError{Exit: 128, Output: "fatal: the remote end hung up unexpectedly"}, again + "git exited with 128."},
		{&preparation.CloneError{Exit: -1}, again + "git did not start."},
		{&preparation.CloneError{Exit: -1, Output: "", NoGit: true}, "Ask the instance admin of Qory Apiary to check the machine: it cannot store " + id + ".\n  git is not on PATH"},
		{errors.New("exec: \"git\": executable file not found in $PATH"), again + "git did not start."},
	} {
		if got := cloneFailed(id, c.err).Error(); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// TestNestIsAPathInsideAnother is a path inside another at a /, either way round: the
// same path, and a path that only starts with another's letters, do not nest.
func TestNestIsAPathInsideAnother(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"acme/web", "acme/web/sub", true},
		{"acme/web/sub", "acme/web", true},
		{"acme/web", "acme/web", false},
		{"acme/web", "acme/web2", false},
		{"acme/web2", "acme/web", false},
		{"acme/web", "acme/back", false},
	} {
		if got := nest(c.a, c.b); got != c.want {
			t.Errorf("nest(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestClonesDestinationsOnlyForAReadRunWithAHarnessDocument is the rule of the fetch
// before the session: only a run whose documents have access read and a harness
// document, and whose inputs list destinations, clones its destinations in the session
// instead. Every other run, and a run with no run code, fetches.
func TestClonesDestinationsOnlyForAReadRunWithAHarnessDocument(t *testing.T) {
	const listed, none = `{"destinations": [{"id": "tg_web0000000000000", "target": {"collection": "c"}}]}`, `{"destinations": []}`
	harness := []byte(`{"harness": {}}`)
	for _, c := range []struct {
		access  string
		harness []byte
		inputs  string
		want    bool
	}{
		{"read", harness, listed, true},
		{"read", nil, listed, false},
		{"read", harness, none, false},
		{"read", harness, `{}`, false},
		{"write", harness, listed, false},
		{"write-no-push", harness, listed, false},
	} {
		r := &codeRun{access: c.access, harness: c.harness, inputs: []byte(c.inputs)}
		if got := r.clonesDestinations(); got != c.want {
			t.Errorf("access %s, harness document %t, inputs %s: %v, want %v", c.access, c.harness != nil, c.inputs, got, c.want)
		}
	}
	if (*codeRun)(nil).clonesDestinations() {
		t.Error("a run with no run code clones its destinations")
	}
}

// placeRoot is a folder of clones, opened as an [os.Root], with the folders above the
// place git.test/acme/web, and the clone of that place.
func placeRoot(t *testing.T) (*os.Root, clone) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "git.test", "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, clone{id: "tg_web0000000000000", rel: filepath.Join("git.test", "acme", "web")}
}

// TestClearPlaceKeepsAFileThatAppeared is a file put at the clone's place after the check
// before the clone: clearPlace stops with a link or a file at the place, and the file stays.
func TestClearPlaceKeepsAFileThatAppeared(t *testing.T) {
	r, d := placeRoot(t)
	if err := r.WriteFile(d.rel, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := clearPlace(r, d)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Path != d.rel || !errors.Is(err, errLinkOrFile) {
		t.Errorf("clearPlace: %v, want a link or a file at %s", err, d.rel)
	}
	if got, err := r.ReadFile(d.rel); err != nil || string(got) != "notes\n" {
		t.Errorf("the file is %q, %v after clearPlace", got, err)
	}
}

// TestClearPlaceKeepsALinkThatAppeared is a symbolic link put at the clone's place after
// the check before the clone: clearPlace stops with a link or a file at the place, and the
// link stays.
func TestClearPlaceKeepsALinkThatAppeared(t *testing.T) {
	r, d := placeRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(r.Name(), d.rel)); err != nil {
		t.Fatal(err)
	}
	err := clearPlace(r, d)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Path != d.rel || !errors.Is(err, errLinkOrFile) {
		t.Errorf("clearPlace: %v, want a link or a file at %s", err, d.rel)
	}
	if got, err := r.Readlink(d.rel); err != nil || got != outside {
		t.Errorf("the link leads to %q, %v after clearPlace", got, err)
	}
}

// TestClearPlaceRemovesAnEmptyFolderOnly is the other places clearPlace finds: nothing is
// free, an empty folder goes, and a folder with a file stops it and keeps the file.
func TestClearPlaceRemovesAnEmptyFolderOnly(t *testing.T) {
	r, d := placeRoot(t)
	if err := clearPlace(r, d); err != nil {
		t.Errorf("nothing at the place: %v", err)
	}
	if err := r.Mkdir(d.rel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := clearPlace(r, d); err != nil {
		t.Errorf("an empty folder: %v", err)
	}
	if _, err := r.Lstat(d.rel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the empty folder stayed: %v", err)
	}
	if err := r.Mkdir(d.rel, 0o700); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(d.rel, "notes.txt")
	if err := r.WriteFile(notes, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearPlace(r, d); err == nil {
		t.Error("a folder with a file: no error")
	}
	if _, err := r.Lstat(notes); err != nil {
		t.Errorf("the folder's file is gone: %v", err)
	}
}
