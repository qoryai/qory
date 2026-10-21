package source_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// TestAGitSourceErrorHidesTheURLsPasswordAndASecret is a fetch error and a gone ref of a
// URL with a password in its user part, and of a ref with a secret: the password prints
// as (hidden), the secret as Forager's phrase, and the fetch error still unwraps.
func TestAGitSourceErrorHidesTheURLsPasswordAndASecret(t *testing.T) {
	inner := &exec.ExitError{}
	fetch := &source.FetchError{Source: "https://u:token-123@git.example/h.git#main", Output: "fatal: unable to access 'https://u:token-123@git.example/h.git/': 403\n", Err: inner}
	if want := "fetching https://u:(hidden)@git.example/h.git#main: fatal: unable to access 'https://u:(hidden)@git.example/h.git/': 403"; fetch.Error() != want {
		t.Errorf("fetch error = %q\nwant %q", fetch.Error(), want)
	}
	var exit *exec.ExitError
	if !errors.As(error(fetch), &exit) {
		t.Error("the fetch error lost the git error under it")
	}
	fetch = &source.FetchError{Source: "https://git.example/h.git#" + secret, Output: "fatal: couldn't find remote ref " + secret, Err: inner}
	if want := "fetching (a value that contains an access key secret): fatal: couldn't find remote ref (a value that contains an access key secret)"; fetch.Error() != want {
		t.Errorf("fetch error = %q\nwant %q", fetch.Error(), want)
	}
	gone := &source.GoneError{URL: "https://u:token-123@git.example/h.git", Ref: "x" + secret}
	if want := "https://u:(hidden)@git.example/h.git has no branch or tag (a value that contains an access key secret); to keep the old harness, set ref to a commit id"; gone.Error() != want {
		t.Errorf("gone error = %q\nwant %q", gone.Error(), want)
	}
}

// TestAPathSourceWithASecretIsNotPrinted is a path source with a secret in a directory's
// name that is not there, and one that is a file: neither error contains the secret, and
// the first still matches fs.ErrNotExist.
func TestAPathSourceWithASecretIsNotPrinted(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	_, err := source.Resolve(root, stack.Source{Path: secret + "/m"}, source.Options{})
	if err == nil || strings.Contains(err.Error(), secret) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing directory: %v (errors.Is ErrNotExist %v)", err, errors.Is(err, fs.ErrNotExist))
	}
	write(t, filepath.Join(root, secret), "x")
	_, err = source.Resolve(root, stack.Source{Path: secret}, source.Options{})
	if want := "(a value that contains an access key secret) is not a directory"; err == nil || err.Error() != want {
		t.Errorf("file: %v, want %q", err, want)
	}
}

// TestACacheErrorOfAURLWithASecretIsNotPrinted is a git source whose URL contains a
// secret and whose cache directory qory cannot write: the error does not contain the
// secret, which the cache directory's name repeats, and still matches the os error.
func TestACacheErrorOfAURLWithASecretIsNotPrinted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a directory of mode 500")
	}
	hermetic(t)
	cache := t.TempDir()
	if err := os.Chmod(cache, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cache, 0o700) })
	_, err := source.Resolve("/nowhere", stack.Source{Git: "file:///nonexistent/" + secret + ".git", Ref: "main"}, source.Options{Cache: cache})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("err = %v, want one without the secret", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("err = %v, want a *fs.PathError under it", err)
	}
}
