package source_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/source"
)

// TestFetchErrorsAreMasked is a fetch that failed and a ref the remote does not have,
// for a URL with a signature and a value of the mask list in its query: each error
// prints *** for both, and for the value where git's output quotes it elsewhere.
func TestFetchErrorsAreMasked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const value = "source-mask-value-42"
	secretmask.Default().Add(value)
	const src = "https://git.example/h.git?Signature=abc123&k=" + value
	fetch := &source.FetchError{Source: src + "#main", URL: src, Ref: "main", Output: "fatal: denied for " + value, Err: errors.New("exit status 128")}
	gone := &source.GoneError{URL: src, Ref: "main"}
	for _, err := range []error{fetch, gone} {
		text := err.Error()
		if strings.Contains(text, value) || strings.Contains(text, "abc123") || !strings.Contains(text, "?Signature=***&k=***") {
			t.Errorf("%T: %s", err, text)
		}
	}
}
