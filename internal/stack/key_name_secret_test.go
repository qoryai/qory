package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAnUnknownStackKeyWithASecretInItsNameIsReportedByItsLine is an unknown key of
// qory-stack.yaml whose name contains an access key secret or a run key, with a space
// in it or not: the error contains the key's line and not its name. A plain name is
// quoted as before.
func TestAnUnknownStackKeyWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ name, secret, want string }{
		{"qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 2: key is not one qory-stack.yaml reads"},
		{"qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA x", "qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "line 2: key is not one qory-stack.yaml reads"},
		{"layrs", "", `line 2: key "layrs" is not one qory-stack.yaml reads`},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		body := "apiVersion: qory.dev/v1alpha1\n" + c.name + ": 1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		if c.secret != "" && err != nil && strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: the error contains the secret: %v", c.name, err)
		}
	}
}

// TestAnUnknownStackKeyWithANewlineInItsNameIsReportedByItsLine is an unknown key of
// qory-stack.yaml whose name contains an access key secret and a newline: the error is
// the key's line alone.
func TestAnUnknownStackKeyWithANewlineInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), FileName)
	body := "apiVersion: qory.dev/v1alpha1\n\"qak_SECRET\\nb\": 1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if want := path + ": line 2: key is not one qory-stack.yaml reads"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "qak_SECRET") {
		t.Errorf("the error contains the secret: %v", err)
	}
}

// TestADecoderRefusalWithASecretIsGivenByItsLines is a qory-stack.yaml the decoder
// refuses with an access key secret or a run key in its text: each refusal whose text
// contains one is given by its line alone, a refusal without one keeps the decoder's
// words, a number in a value is not taken for a line, and a refusal with no line is the
// path alone.
func TestADecoderRefusalWithASecretIsGivenByItsLines(t *testing.T) {
	isolate(t)
	head := "apiVersion: qory.dev/v1alpha1\n"
	const cannotRead = "qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key or a run code, so qory does not print it."
	for _, c := range []struct{ body, want string }{
		{head + "modules: qak_x\n", ": line 2: " + cannotRead},
		{head + "extends: qak_x\nmodules: qrk_x\n", ": yaml: unmarshal errors:\n  line 2: " + cannotRead + "\n  line 3: " + cannotRead},
		{head + "extends: line 99\nmodules: qak_x\n", ": yaml: unmarshal errors:\n  line 2: cannot unmarshal !!str `line 99` into stack.Source\n  line 3: " + cannotRead},
		{head + "extends: \"line 99: field y\"\n\"qak_S\\nb\": 1\n", ": yaml: unmarshal errors:\n  line 2: cannot unmarshal !!str `line 99...` into stack.Source\n  line 3: key is not one qory-stack.yaml reads"},
		{head + "modules: *qak_x\n", ": " + cannotRead},
		{head + "modules: !!int qak_x\n", ": " + cannotRead},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if want := path + c.want; err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "qak_") || strings.Contains(err.Error(), "qrk_")) {
			t.Errorf("%q: the error contains the secret: %v", c.body, err)
		}
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

// TestARefusalWithoutALineIsThePath is a qory-stack.yaml value the decoder refuses
// without a line, whose text contains the words of a refusal of the file's syntax and
// an access key secret: the refusal is the path, not a line from the value.
func TestARefusalWithoutALineIsThePath(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), FileName)
	body := "apiVersion: qory.dev/v1alpha1\nextends: !!int \"x yaml: line 9: qak_x\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	want := path + ": qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key or a run code, so qory does not print it."
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestARefusalOfARuntimeListWithASecretKeepsQorysWords is a target.runtime and an
// extending.target.runtime list whose entry the decoder refuses with an access key
// secret or a run key in its message: the refusal keeps the words qory puts before the
// decoder's message, and says qory does not print that message.
func TestARefusalOfARuntimeListWithASecretKeepsQorysWords(t *testing.T) {
	isolate(t)
	head := "apiVersion: qory.dev/v1alpha1\n"
	const cannotRead = "qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key or a run code, so qory does not print it."
	for _, c := range []struct{ body, want string }{
		{head + "target:\n  runtime: [!!int qak_x]\n", ": target.runtime is one runtime name or a list of them: " + cannotRead},
		{head + "target:\n  runtime: [!!int QRK_ab]\n", ": target.runtime is one runtime name or a list of them: " + cannotRead},
		{head + "extending:\n  target:\n    runtime: [!!int qak_x]\n", ": one name or a list of them: " + cannotRead},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if want := path + c.want; err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
	}
}

// TestASelectedKindWithASecretIsPlacedByItsLine is a key of an exclude or an only block
// whose name contains an access key secret or a run key: the refusal gives the key's
// line in place of the quoted name. A plain key is quoted as before.
func TestASelectedKindWithASecretIsPlacedByItsLine(t *testing.T) {
	isolate(t)
	const rest = "; kinds: skills, agents, commands, output-styles, hooks, mcp, files; parts: instructions, settings, env"
	for _, c := range []struct{ block, want string }{
		{"exclude:\n  qak_x: [greet]", "module core: exclude selects kind the name at line 5" + rest},
		{"only:\n  skills: [a]\n  QRK_ab: [greet]", "module core: only selects kind the name at line 6" + rest},
		{"exclude:\n  prompts: [greet]", `module core: exclude selects kind "prompts"` + rest},
	} {
		_, err := Load(write(t, selectionStack(c.block)))
		if err == nil || !strings.HasSuffix(err.Error(), c.want) {
			t.Errorf("%q: %v, want suffix %q", c.block, err, c.want)
		}
		if err != nil && (strings.Contains(err.Error(), "qak_") || strings.Contains(err.Error(), "QRK_")) {
			t.Errorf("%q: the error contains the secret: %v", c.block, err)
		}
	}
}

// TestAModuleNameWithASecretIsLeftOutOfAStackRefusal is a refusal of a module's exclude
// block, when the module's name contains an access key secret: the refusal prints the
// phrase in place of the name.
func TestAModuleNameWithASecretIsLeftOutOfAStackRefusal(t *testing.T) {
	isolate(t)
	body := "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: qak_m\n    exclude:\n      prompts: [greet]\n"
	_, err := Load(write(t, body))
	want := `module (a name that contains an access key secret): exclude selects kind "prompts"`
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "qak_") {
		t.Errorf("%v, want one that contains %q", err, want)
	}
}
