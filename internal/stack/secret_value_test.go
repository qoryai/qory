package stack

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// nameWithSecret and valueWithSecret are the phrases Forager prints in place of a
	// name and a value that contain an access key secret.
	nameWithSecret  = "(a name that contains an access key secret)"
	valueWithSecret = "(a value that contains an access key secret)"
	// nameWithRunKey is the phrase in place of a name that contains a run key.
	nameWithRunKey = "(a name that contains a run key)"
)

// TestAStackRefusalPrintsNoSecret is each refusal of a stack file that prints a name or
// a value a person wrote, with qak_ or qrk_ in it: the refusal prints Forager's phrase in
// its place and no secret.
func TestAStackRefusalPrintsNoSecret(t *testing.T) {
	isolate(t)
	const head = "apiVersion: qory.dev/v1alpha1\n"
	const core = "modules:\n  - name: core\n"
	for _, c := range []struct{ body, want string }{
		{head + "extending:\n  kinds: [qak_k]\n" + core, "extending.kinds lists kind " + nameWithSecret + "; kinds: "},
		{head + "extending:\n  settings: [qak_k.]\n" + core, "extending.settings lists " + nameWithSecret + ", which is not a dotted key path"},
		{head + "extending:\n  files: [../qak_x]\n" + core, "extending.files lists " + valueWithSecret + ", which is not a <runtime>/<path> prefix"},
		{head + "extending:\n  target:\n    runtime: [qrk_r, qrk_r]\n" + core, "extending.target.runtime lists " + nameWithRunKey + " twice"},
		{head + "extending:\n  target:\n    model: [qak_m, qak_m]\n" + core, "extending.target.model lists " + valueWithSecret + " twice"},
		{head + "bind:\n  qak_x: y\n" + core, "bind " + nameWithSecret + ": a role is <kind>/<name>"},
		{head + "bind:\n  skills/a: qak_/y\n" + core, "bind skills/a: " + nameWithSecret + " is not an entry name"},
		{head + "modules:\n  - name: qak_/m\n", "modules[0]: name " + nameWithSecret + " is not one path segment"},
		{head + "modules:\n  - name: qak_m\n  - name: qak_m\n", "module " + nameWithSecret + " is listed twice"},
		{head + "modules:\n  - name: core\n    source: {path: x, stack: qak_s}\n", "module core: source selects stack " + nameWithSecret + ";"},
		{head + "modules:\n  - name: core\n    link: qak_/l\n", "module core: link " + nameWithSecret + " is not one path segment"},
		{head + "modules:\n  - name: a\n    link: qak_l\n  - name: b\n    link: qak_l\n", "module a and module b both link " + nameWithSecret},
		{head + "modules:\n  - name: core\n    only:\n      skills: [qak_s]\n    exclude:\n      skills: [qak_s]\n", "skills/" + nameWithSecret + " is listed in only and in exclude"},
		{head + "modules:\n  - name: core\n    source: {path: x, module: qak_/m}\n", "source.module " + nameWithSecret + " is not one path segment"},
		{head + "modules:\n  - name: core\n    source: {git: https://git.example/h.git, ref: main, path: ../qak_x}\n", "source.path " + valueWithSecret + " is not a directory inside the repository"},
		{head + "qory: \">=qak_x\"\n" + core, "qory " + valueWithSecret + ": " + valueWithSecret + " is not a version"},
	} {
		_, err := Load(write(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v\nwant one that contains %q", c.body, err, c.want)
		}
		noSecret(t, err)
	}
}

// TestATargetOutsideThePolicyPrintsNoSecret is a runtime, a model and a base name with a
// secret outside a base stack's target policy: the refusal prints the phrases.
func TestATargetOutsideThePolicyPrintsNoSecret(t *testing.T) {
	policy := &TargetPolicy{Runtime: Names{"claude", "qrk_r"}, Model: Names{"qak_m"}}
	err := policy.Check(Target{Runtimes: Runtimes{"qak_r"}}, "qak_base@abc")
	if want := "runtime " + nameWithSecret + " is not one the base stack " + nameWithSecret + " is written for; runtimes: claude, " + nameWithRunKey; err == nil || err.Error() != want {
		t.Errorf("runtime: %v\nwant %q", err, want)
	}
	err = policy.Check(Target{Runtimes: Runtimes{"claude"}, Model: "qrk_other"}, "base")
	if want := "model (a value that contains a run key) is not one the base stack base is written for; models: " + valueWithSecret; err == nil || err.Error() != want {
		t.Errorf("model: %v\nwant %q", err, want)
	}
}

// noSecret fails the test when err contains qak_ or qrk_ in any case.
func noSecret(t *testing.T, err error) {
	t.Helper()
	if text := strings.ToLower(errText(err)); strings.Contains(text, "qak_") || strings.Contains(text, "qrk_") {
		t.Errorf("the refusal contains a secret: %v", err)
	}
}

// errText is err's text, "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestAStackFileUnderASecretPathPrintsNoSecret is a stack file in a directory whose name
// contains an access key secret, missing and with an unknown key: each refusal prints
// Forager's phrase in place of the directory, and a missing file still matches
// fs.ErrNotExist.
func TestAStackFileUnderASecretPathPrintsNoSecret(t *testing.T) {
	isolate(t)
	dir := filepath.Join(t.TempDir(), "qak_base")
	_, err := Load(filepath.Join(dir, FileName))
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(errText(err), "/"+nameWithSecret+"/"+FileName) {
		t.Errorf("missing: %v, want fs.ErrNotExist and the name phrase", err)
	}
	noSecret(t, err)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, FileName)
	if err := os.WriteFile(file, []byte("apiVersion: qory.dev/v1alpha1\nbogus: 1\nmodules:\n  - name: core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(file)
	if !strings.Contains(errText(err), "/"+nameWithSecret+"/"+FileName+": line 2: ") {
		t.Errorf("unknown key: %v, want the name phrase", err)
	}
	noSecret(t, err)
}
