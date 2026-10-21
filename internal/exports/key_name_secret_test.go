package exports_test

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/exports"
)

// TestAnUnknownExportsKeyWithASecretInItsNameIsReportedByItsLine is an unknown key of
// the exports section whose name contains an access key secret or a run key, with a
// space in it or not: the error contains the key's line and not its name. A plain name
// is quoted as before.
func TestAnUnknownExportsKeyWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ name, secret, want string }{
		{"qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 4: key is not one qory.yaml reads"},
		{"qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA x", "qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 4: key is not one qory.yaml reads"},
		{"stack", "", `line 4: key "stack" is not one qory.yaml reads`},
	} {
		root := write(t, "exports:\n  modules: [core]\n  "+c.name+": 1\n")
		_, err := exports.Read(root)
		if want := filepath.Join(root, exports.FileName) + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		if c.secret != "" && err != nil && strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: the error contains the secret: %v", c.name, err)
		}
	}
}

// TestAnUnknownExportsKeyWithANewlineInItsNameIsReportedByItsLine is an unknown key of
// the exports section whose name contains an access key secret and a newline: the error
// is the key's line alone.
func TestAnUnknownExportsKeyWithANewlineInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	root := write(t, "exports:\n  modules: [core]\n  \"qak_SECRET\\nb\": 1\n")
	_, err := exports.Read(root)
	if want := filepath.Join(root, exports.FileName) + ": line 4: key is not one qory.yaml reads"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "qak_SECRET") {
		t.Errorf("the error contains the secret: %v", err)
	}
}

// isolate sets HOME and the XDG directories qory reads to directories of the test, so a
// test reads no file of the machine it runs on.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
}

// TestANameWithBothSecretsIsSaidToContainAnAccessKeySecret is a name that contains a
// run key and an access key secret, in either order and any letter case, in a message
// that quotes it once bare, once quoted and once in a path: each copy is the access key
// secret's phrase. A name with a run key alone is the run key's phrase, and a name with
// neither leaves the message as it is.
func TestANameWithBothSecretsIsSaidToContainAnAccessKeySecret(t *testing.T) {
	isolate(t)
	const access, run = "(a name that contains an access key secret)", "(a name that contains a run key)"
	for _, c := range []struct{ name, want string }{
		{"qrk_ab-QAK_x", "credential " + access + ": " + access + " is " + access + ", here"},
		{"QAK_x-qrk_ab", "credential " + access + ": " + access + " is " + access + ", here"},
		{"x-Qak_y-QRK_z", "credential " + access + ": " + access + " is " + access + ", here"},
		{"QRK_ab", "credential " + run + ": " + run + " is " + run + ", here"},
		{"deploy", `credential deploy: "deploy" is /opt/qory-deploy, here`},
	} {
		text := "credential " + c.name + `: "` + c.name + `" is /opt/qory-` + c.name + ", here"
		if got := exports.HideName(text, c.name); got != c.want {
			t.Errorf("HideName(%q) = %q, want %q", text, got, c.want)
		}
	}
}

// TestAnAliasOfASecretAnchorIsRefusedWithoutItsName is a qory.yaml whose first alias
// is of an anchor whose name contains an access key secret or a run key: the refusal
// gives the alias's line and says the value is a YAML alias, without the name. A plain
// anchor is named as before.
func TestAnAliasOfASecretAnchorIsRefusedWithoutItsName(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), exports.FileName)
	for _, c := range []struct{ name, want string }{
		{"qak_x", ": line 2: the value is a YAML alias, which qory.yaml may not contain; write the value out in full where it is used"},
		{"QRK_ab", ": line 2: the value is a YAML alias, which qory.yaml may not contain; write the value out in full where it is used"},
		{"m", ": line 2: *m is a YAML alias, which qory.yaml may not contain; write the value out in full where it is used"},
	} {
		err := exports.RefuseAliases(path, []byte("a: &"+c.name+" 1\nb: *"+c.name+"\n"))
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%s: %v, want %q", c.name, err, path+c.want)
		}
	}
}

// TestHideNameHidesEveryWordWithASecret is a message that quotes a name the way Go's %q
// quotes it, with a space, a quote, a backslash, a tab or a character outside ASCII in
// it, that writes the name in another letter case, or that cuts it short with "...":
// each word that contains an access key secret or a run key is the phrase, whether it
// contains the name as given or not.
func TestHideNameHidesEveryWordWithASecret(t *testing.T) {
	isolate(t)
	const access, run = "(a name that contains an access key secret)", "(a name that contains a run key)"
	for _, c := range []struct{ name, text, want string }{
		{"qak_a b", `the tool name "qak_a b" is not 1 to 64 of a-z`, "the tool name " + access + " is not 1 to 64 of a-z"},
		{`qak_a"b`, `the tool name "qak_a\"b" is not 1 to 64 of a-z`, "the tool name " + access + " is not 1 to 64 of a-z"},
		{`qrk_a\b`, `the tool name "qrk_a\\b" is not 1 to 64 of a-z`, "the tool name " + run + " is not 1 to 64 of a-z"},
		{"qak_a\tb", `the tool name "qak_a\tb" is not 1 to 64 of a-z`, "the tool name " + access + " is not 1 to 64 of a-z"},
		{"qak_é b", `the tool name "qak_é b" is not 1 to 64 of a-z`, "the tool name " + access + " is not 1 to 64 of a-z"},
		{"qak_x", "credential QAK_X: argument: x", "credential " + access + ": argument: x"},
		{"qak_abcdefgh", "qory-qak_ab... describe: exit status 3", access + " describe: exit status 3"},
	} {
		if got := exports.HideName(c.text, c.name); got != c.want {
			t.Errorf("%q: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestADecoderPrefixWithASecretIsLeftOut is a decoder error that qory's words wrap,
// when those words contain an access key secret: the refusal leaves the words out, as
// it leaves out the decoder's message.
func TestADecoderPrefixWithASecretIsLeftOut(t *testing.T) {
	isolate(t)
	const cannotRead = "qory cannot read the value here. The YAML decoder's message contains an access key secret or a run key, so qory does not print it."
	inner := errors.New("yaml: cannot decode !!str `qak_y` as a !!int")
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("qak_x is one name: %w", inner), "f.yaml: " + cannotRead},
		{fmt.Errorf("one name: %w", inner), "f.yaml: one name: " + cannotRead},
	} {
		if err := exports.DecodeError("f.yaml", c.err); err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
}

// TestHidePathLeavesOutEachWordWithASecret is error texts with a secret in a path, in a
// quoted path, and in a word without a slash: each word or path segment with the secret
// is the phrase, and the punctuation and quotes around it stay.
func TestHidePathLeavesOutEachWordWithASecret(t *testing.T) {
	const access = "(a name that contains an access key secret)"
	const run = "(a name that contains a run key)"
	for _, c := range []struct{ text, want string }{
		{"/r/modules/qak_m/qory-module.yaml: x", "/r/modules/" + access + "/qory-module.yaml: x"},
		{`open "/r/modules/qak_m": x`, `open "/r/modules/` + access + `": x`},
		{"stat /r/modules/a qak_m: no such file or directory", "stat /r/modules/a " + access + ": no such file or directory"},
		{"/r/modules/a qak_m has no qory-module.yaml", "/r/modules/a " + access + " has no qory-module.yaml"},
		{"exports no module named qak_x; modules: core", "exports no module named " + access + "; modules: core"},
		{"modules QRK_ab, core", "modules " + run + ", core"},
		{`module "qak_x": x`, `module "` + access + `": x`},
		{"/r/modules/core: x", "/r/modules/core: x"},
	} {
		if got := exports.HidePath(c.text); got != c.want {
			t.Errorf("HidePath(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

// TestHideErrorLeavesOutASecretAndKeepsTheError is an error whose text contains an
// access key secret in a path, and one with no secret: the first prints the phrase and
// unwraps to the error, the second is returned as it is.
func TestHideErrorLeavesOutASecretAndKeepsTheError(t *testing.T) {
	inner := &fs.PathError{Op: "open", Path: "/r/modules/qak_m/qory-module.yaml", Err: fs.ErrPermission}
	err := exports.HideError(inner)
	var pathErr *fs.PathError
	if want := "open /r/modules/(a name that contains an access key secret)/qory-module.yaml: permission denied"; err.Error() != want || !errors.As(err, &pathErr) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%v, want %q and the *fs.PathError under it", err, want)
	}
	plain := errors.New("open /r/modules/core: permission denied")
	if err := exports.HideError(plain); err != plain {
		t.Errorf("a plain error: %v, want it as it is", err)
	}
	if err := exports.HideError(nil); err != nil {
		t.Errorf("nil: %v", err)
	}
}
