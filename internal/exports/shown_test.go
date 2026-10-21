package exports_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/exports"
)

// secretValue and runKeyValue are values that contain an access key secret and a run
// key; neither is a real key.
const (
	secretValue = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
	runKeyValue = "QRK_example"
)

// TestAValueWithASecretPrintsAsForagersPhrase is a value with an access key secret, one
// with a run key in another letter case, and a plain one, quoted and bare.
func TestAValueWithASecretPrintsAsForagersPhrase(t *testing.T) {
	for _, c := range []struct {
		v     string
		quote bool
		want  string
	}{
		{"token " + secretValue, false, "(a value that contains an access key secret)"},
		{"x" + runKeyValue, true, "(a value that contains a run key)"},
		{"token-123", true, `"token-123"`},
		{"token-123", false, "token-123"},
	} {
		if got := exports.Value(c.v, c.quote); got != c.want {
			t.Errorf("Value(%q, %v) = %q, want %q", c.v, c.quote, got, c.want)
		}
	}
	got := strings.Join(exports.Values([]string{"a", secretValue}), ", ")
	if want := "a, (a value that contains an access key secret)"; got != want {
		t.Errorf("Values = %q, want %q", got, want)
	}
}

// TestAGitURLPrintsWithoutItsPassword is a git URL with a password in its user part, one
// with a user alone, one with a secret, and one with neither.
func TestAGitURLPrintsWithoutItsPassword(t *testing.T) {
	for _, c := range []struct{ u, want string }{
		{"https://user:token-123@git.example/h.git", "https://user:(hidden)@git.example/h.git"},
		{"https://user:to@ken@git.example/h.git#main", "https://user:(hidden)@git.example/h.git#main"},
		{"ssh://git@git.example/h.git", "ssh://git@git.example/h.git"},
		{"https://git.example/h.git", "https://git.example/h.git"},
		{"https://u:" + secretValue + "@git.example/h.git", "(a value that contains an access key secret)"},
		{"/srv/h.git", "/srv/h.git"},
	} {
		if got := exports.URL(c.u); got != c.want {
			t.Errorf("URL(%q) = %q, want %q", c.u, got, c.want)
		}
	}
	text := "fetching https://u:token-123@git.example/h.git#main: fatal: 'https://u:token-123@git.example/h.git/' not found"
	if got, want := exports.HideURLs(text), "fetching https://u:(hidden)@git.example/h.git#main: fatal: 'https://u:(hidden)@git.example/h.git/' not found"; got != want {
		t.Errorf("HideURLs = %q, want %q", got, want)
	}
}

// TestAPercentEncodedSecretIsNotPrinted is a git URL whose user part, password, path,
// query or fragment contains an access key secret or a run key with a letter
// percent-encoded, as %71 for q: the URL prints Forager's value phrase in place of it,
// and a text and an error that quote it print the phrase, or (hidden) for the password.
func TestAPercentEncodedSecretIsNotPrinted(t *testing.T) {
	const phrase = "(a value that contains an access key secret)"
	for _, u := range []string{
		"https://%71ak_example@git.example/h.git",
		"https://u:%71ak_example@git.example/h.git",
		"https://git.example/%71%61k_example.git",
		"https://git.example/h.git?t=%71ak_example",
		"https://git.example/h.git#qa%6B_example",
		"https://git.example/h.git?t=%51%52%4B%5Fexample",
	} {
		want := phrase
		if strings.Contains(u, "%52") {
			want = "(a value that contains a run key)"
		}
		if got := exports.URL(u); got != want {
			t.Errorf("URL(%q) = %q, want %q", u, got, want)
		}
		quoted, inError := want, want
		if strings.HasPrefix(u, "https://u:") {
			quoted, inError = "https://u:(hidden)@git.example/h.git", "'https://u:(hidden)@git.example/h.git'"
		}
		if got := exports.HideValues(exports.HideURLs("fetching " + u + ": failed")); got != "fetching "+quoted+": failed" {
			t.Errorf("HideValues(HideURLs(%q)) = %q", u, got)
		}
		err := exports.HideURLError(errors.New("fatal: '" + u + "' not found"))
		if err == nil || err.Error() != "fatal: "+inError+" not found" {
			t.Errorf("HideURLError(%q) = %v", u, err)
		}
	}
	if got := exports.URL("https://git.example/a%20b.git"); got != "https://git.example/a%20b.git" {
		t.Errorf("URL with no secret = %q", got)
	}
}

// TestAnErrorWithASecretValueKeepsItsType is the error of a command whose text quotes a
// value with a secret: the text prints the phrase, and errors.As still finds the error
// under it.
func TestAnErrorWithASecretValueKeepsItsType(t *testing.T) {
	value := "./setup --token " + secretValue
	inner := &exec.ExitError{}
	err := exports.HideValueError(errorWith(value+": exit status 1", inner), value)
	if want := "(a value that contains an access key secret): exit status 1"; err.Error() != want {
		t.Errorf("HideValueError = %q, want %q", err, want)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Errorf("errors.As lost the *exec.ExitError under %v", err)
	}
	// A word with a secret that is not the value prints the phrase too.
	if got, want := exports.HideValue("ran ./a; then "+secretValue+"x: failed", "./a"), "ran ./a; then (a value that contains an access key secret): failed"; got != want {
		t.Errorf("HideValue = %q, want %q", got, want)
	}
	plain := errorWith("./setup: exit status 1", fs.ErrNotExist)
	if got := exports.HideValueError(plain, "./setup"); got != plain {
		t.Errorf("HideValueError changed an error with no secret: %v", got)
	}
	url := exports.HideURLError(errorWith("fetching https://u:p@git.example/h.git#main: exit status 128", fs.ErrNotExist))
	if want := "fetching https://u:(hidden)@git.example/h.git#main: exit status 128"; url.Error() != want || !errors.Is(url, fs.ErrNotExist) {
		t.Errorf("HideURLError = %q (errors.Is %v), want %q", url, errors.Is(url, fs.ErrNotExist), want)
	}
}

// wrapped is an error with a text and an error under it.
type wrapped struct {
	text string
	err  error
}

func (w wrapped) Error() string { return w.text }
func (w wrapped) Unwrap() error { return w.err }

// errorWith is an error with text that unwraps to err.
func errorWith(text string, err error) error { return wrapped{text, err} }

// TestAnExportsRefusalPrintsNoSecret is each refusal of an exports section that prints a
// name or a value with a secret, and an export looked up by such a name: each prints
// Forager's phrase in its place.
func TestAnExportsRefusalPrintsNoSecret(t *testing.T) {
	isolate(t)
	const name = "(a name that contains an access key secret)"
	const value = "(a value that contains an access key secret)"
	for _, c := range []struct{ body, want string }{
		{"exports:\n  modules: [qak_/m]\n", "exports.modules lists " + name + ", which is not one path segment"},
		{"exports:\n  modules: [qak_m, qak_m]\n", "exports.modules lists " + name + " twice"},
		{"exports:\n  dir: {stacks: ../qak_s, modules: m}\n  modules: [core]\n", "exports.dir stacks " + value + " is not a directory inside the repository"},
	} {
		_, err := exports.Read(write(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v\nwant one that contains %q", c.body, err, c.want)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, exports.FileName), []byte("apiVersion: qak_v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := exports.Read(root)
	if want := "apiVersion " + value + " is not one this qory reads"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("apiVersion: %v\nwant one that contains %q", err, want)
	}
	e, err := exports.Read(write(t, "exports:\n  modules: [qrk_m, core]\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Module("qak_x")
	if want := "exports no module named " + name + "; modules: (a name that contains a run key), core"; err == nil || err.Error() != want {
		t.Errorf("lookup: %v\nwant %q", err, want)
	}
	if err := e.Verify(); err == nil || strings.Contains(strings.ToLower(err.Error()), "qrk_") {
		t.Errorf("verify: %v, want a refusal without the run key", err)
	}
}
