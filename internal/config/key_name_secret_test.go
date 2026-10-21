package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// Key names that contain a secret: an access key secret and a run key.
const (
	accessKeySecretName = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
	runKeyName          = "qrk_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
)

// foragerServer is the start of a forager.yaml whose gateway.server is open on line 2,
// and pinEntry the start of one whose first pin entry is open on line 5.
const (
	foragerServer = "gateway:\n  server:\n    url: https://qory.example\n"
	pinEntry      = foragerServer + "    apiary_public_key:\n      - alg: ed25519\n        public_key: rcFAEfgtHFbZVqpPnXPYhYNhpgYEhSXg0Ixjjcdd2Mc\n"
)

// TestAnUnknownForagerKeyWithASecretInItsNameIsReportedByItsLine is an unknown key of
// forager.yaml whose name contains an access key secret or a run key, under
// gateway.server, inside a pin entry, written with a space, and written as an alias of
// an anchor whose name contains one: the error contains the key's line and not its name.
func TestAnUnknownForagerKeyWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ name, body, want string }{
		{accessKeySecretName, foragerServer + "    " + accessKeySecretName + ": 1\n", ": line 4: key is not one forager.yaml reads"},
		{runKeyName, foragerServer + "    " + runKeyName + ": 1\n", ": line 4: key is not one forager.yaml reads"},
		{accessKeySecretName, pinEntry + "        " + accessKeySecretName + ": 1\n", ": line 7: key is not one forager.yaml reads"},
		{runKeyName, pinEntry + "        " + runKeyName + ": 1\n", ": line 7: key is not one forager.yaml reads"},
		{accessKeySecretName, foragerServer + "    " + accessKeySecretName + " x: 1\n", ": line 4: key is not one forager.yaml reads"},
		{accessKeySecretName, "gateway:\n  server:\n    url: &" + accessKeySecretName + " https://qory.example\n    *" + accessKeySecretName + " : 1\n", ": line 4: key is an alias of a key forager.yaml does not read"},
		{runKeyName, "gateway:\n  server:\n    url: &" + runKeyName + " https://qory.example\n    *" + runKeyName + " : 1\n", ": line 4: key is an alias of a key forager.yaml does not read"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && strings.Contains(err.Error(), c.name) {
			t.Errorf("%q: the error contains the secret: %v", c.body, err)
		}
	}
}

// TestAnUnknownKeyWithASecretInItsNameIsReportedByItsLine is an unknown key of
// harness.yaml, which decodeError reports, whose name contains an access key secret or
// a run key: the error contains the key's line and not its name.
func TestAnUnknownKeyWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	for _, name := range []string{accessKeySecretName, runKeyName} {
		writeRaw(t, path, "harness:\n  "+name+": 1\n")
		_, err := config.Document(root)
		want := path + ": line 2: key is not one harness.yaml reads"
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
		if err != nil && strings.Contains(err.Error(), name) {
			t.Errorf("%s: the error contains the secret: %v", name, err)
		}
	}
}

// TestAnUnknownKeyWithNoSecretIsNamed is an unknown key whose name contains no secret,
// in forager.yaml under gateway.server and in harness.yaml: the error quotes its name.
func TestAnUnknownKeyWithNoSecretIsNamed(t *testing.T) {
	isolate(t)
	path := foragerFile(t, foragerServer+"    events2: 1\n")
	_, err := config.LoadForager()
	if want := path + `: line 4: key "events2" is not one forager.yaml reads`; err == nil || err.Error() != want {
		t.Errorf("forager.yaml: %v, want %q", err, want)
	}
	root := t.TempDir()
	path = filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness:\n  events2: 1\n")
	_, err = config.Document(root)
	if want := path + `: line 2: key "events2" is not one harness.yaml reads`; err == nil || err.Error() != want {
		t.Errorf("harness.yaml: %v, want %q", err, want)
	}
}

// foragerRefusals checks that each body of forager.yaml is refused with the whole
// message want after the file's path, and that the refusal does not contain secret.
func foragerRefusals(t *testing.T, secret string, cases []struct{ body, want string }) {
	t.Helper()
	for _, c := range cases {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if secret != "" && err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%q: the error contains the secret: %v", c.body, err)
		}
	}
}

// TestAnUnknownKeyOfAnEntryWithASecretInItsNameIsReportedByItsLine is an unknown key of
// a credential, a tool, an integration and an image whose name contains an access key
// secret or a run key: the error contains the key's line and not its name, and a plain
// name is quoted as before.
func TestAnUnknownKeyOfAnEntryWithASecretInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	credential := "gateway:\n  credentials:\n    github:\n      env: GH\n      "
	tool := "gateway:\n  tools:\n    gh:\n      command: gh\n      "
	integration := "gateway:\n  integrations:\n    github:\n      "
	image := "wall:\n  adapter: docker\n  images:\n    go:\n      ref: a\n      "
	for _, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{credential + name + ": 1\n", ": line 5: gateway.credentials.github: key is not one"},
			{tool + name + ": 1\n", ": line 5: gateway.tools.gh: key is not one"},
			{integration + name + ": 1\n", ": line 4: gateway.integrations.github: key is not one"},
			{image + name + ": 1\n", ": line 6: wall.images.go: key is not one; an image has ref, runtime, docker"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{credential + "x: 1\n", `: gateway.credentials.github: key "x" is not one`},
		{tool + "x: 1\n", `: gateway.tools.gh: key "x" is not one`},
		{integration + "x: 1\n", `: gateway.integrations.github: key "x" is not one`},
		{image + "x: 1\n", `: wall.images.go: key "x" is not one; an image has ref, runtime, docker`},
	})
}

// TestAnEntryNameWithASecretIsReportedByItsLine is an image and an integration whose
// name contains an access key secret or a run key and is not a name the section takes:
// the error contains the name's line and not the name, and a plain name is quoted as
// before.
func TestAnEntryNameWithASecretIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{"wall:\n  adapter: docker\n  images:\n    " + name + ":\n      ref: a\n", ": wall.images: the name at line 4 is not 1 to 64 of a-z, 0-9, underscore, dot and dash, starting with a letter or a digit"},
			{"gateway:\n  integrations:\n    " + name + ":\n      program: x\n", ": gateway.integrations: the name at line 3 is not 1 to 64 of a-z, 0-9, underscore and dash, starting with a letter or a digit"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"wall:\n  adapter: docker\n  images:\n    Go:\n      ref: a\n", `: wall.images: "Go" is not 1 to 64 of a-z, 0-9, underscore, dot and dash, starting with a letter or a digit`},
		{"gateway:\n  integrations:\n    GitHub:\n      program: x\n", `: gateway.integrations: "GitHub" is not 1 to 64 of a-z, 0-9, underscore and dash, starting with a letter or a digit`},
	})
}

// TestAnEntryWithASecretInItsNameIsPlacedByItsLine is a credential, a tool, an
// integration and an image whose name contains an access key secret or a run key, and
// that the reader refuses: the error places the entry by the section and the name's
// line, and does not contain the name. The integration and the image are written in
// lower case, as their names are. A plain name is placed by the name as before.
func TestAnEntryWithASecretInItsNameIsPlacedByItsLine(t *testing.T) {
	isolate(t)
	lower := []string{"qak_abc", "qrk_abc"}
	for i, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{"gateway:\n  credentials:\n    " + name + ":\n      env: GH\n      x: 1\n", `: gateway.credentials (line 3): key "x" is not one`},
			{"gateway:\n  credentials:\n    " + name + ":\n      file: relative\n", ": gateway.credentials (line 3).file is not an absolute path"},
			{"gateway:\n  tools:\n    " + name + ":\n      command: gh\n      x: 1\n", `: gateway.tools (line 3): key "x" is not one`},
			{"gateway:\n  tools:\n    " + name + ":\n      command: [gh]\n", ": gateway.tools (line 3).command is a program by its absolute path, and its arguments"},
		})
		foragerRefusals(t, lower[i], []struct{ body, want string }{
			{"gateway:\n  integrations:\n    " + lower[i] + ":\n      x: 1\n", `: gateway.integrations (line 3): key "x" is not one`},
			{"gateway:\n  integrations:\n    " + lower[i] + ": 1\n", ": gateway.integrations (line 3) is a mapping: program and settings"},
			{"wall:\n  adapter: docker\n  images:\n    " + lower[i] + ":\n      ref: a\n      x: 1\n", `: wall.images (line 4): key "x" is not one; an image has ref, runtime, docker`},
			{"wall:\n  adapter: docker\n  images:\n    " + lower[i] + ":\n      runtime: runc\n", ": wall.images (line 4).ref is required: the image's reference"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"gateway:\n  credentials:\n    github:\n      env: GH\n      x: 1\n", `: gateway.credentials.github: key "x" is not one`},
		{"gateway:\n  credentials:\n    github:\n      file: relative\n", ": gateway.credentials.github.file is not an absolute path"},
		{"gateway:\n  tools:\n    gh:\n      command: [gh]\n", ": gateway.tools.gh.command is a program by its absolute path, and its arguments"},
		{"gateway:\n  integrations:\n    github: 1\n", ": gateway.integrations.github is a mapping: program and settings"},
		{"wall:\n  adapter: docker\n  images:\n    go:\n      runtime: runc\n", ": wall.images.go.ref is required: the image's reference"},
	})
}

// TestAnUnknownKeyWithASpaceInItsNameIsReportedAsAKey is an unknown key of harness.yaml
// whose name has a space in it: the error is the one of any unknown key, by its line
// and without the decoder's words or a Go type, and without the name when the name
// contains an access key secret or a run key.
func TestAnUnknownKeyWithASpaceInItsNameIsReportedAsAKey(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	for _, c := range []struct{ name, want string }{
		{accessKeySecretName + " x", "line 2: key is not one harness.yaml reads"},
		{runKeyName + " x", "line 2: key is not one harness.yaml reads"},
		{"a b", `line 2: key "a b" is not one harness.yaml reads`},
	} {
		writeRaw(t, path, "harness:\n  "+c.name+": 1\n")
		_, err := config.Document(root)
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		if err != nil && c.name != "a b" && strings.Contains(err.Error(), strings.TrimSuffix(c.name, " x")) {
			t.Errorf("%s: the error contains the secret: %v", c.name, err)
		}
	}
}

// TestAKeyWrittenAsAnAliasOfASecretIsReportedByItsLine is a key written as an alias of
// an anchor whose name contains an access key secret or a run key, where the decoder
// reports it: the error contains the key's line and not the anchor's name. A plain
// anchor's alias is printed as before.
func TestAKeyWrittenAsAnAliasOfASecretIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{"session:\n  instance:\n    name: &" + name + " build\n  *" + name + " : 1\n", ": line 4: key is an alias of a key forager.yaml does not read"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"session:\n  instance:\n    name: &a build\n  *a : 1\n", ": line 4: key *a is an alias of a key forager.yaml does not read"},
	})
}

// TestAKeyWithASecretInItsNameWrittenTwiceIsReportedByItsLine is a key whose name
// contains an access key secret or a run key, written twice in forager.yaml and in
// harness.yaml: the error contains the lines and not the name. A plain name is printed
// as before.
func TestAKeyWithASecretInItsNameWrittenTwiceIsReportedByItsLine(t *testing.T) {
	isolate(t)
	for _, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{foragerServer + "    " + name + ": 1\n    " + name + ": 2\n", ": line 5: a key of gateway.server is written twice; it was first written at line 4"},
			{name + ": 1\n" + name + ": 2\n", ": line 2: a key is written twice; it was first written at line 1"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{foragerServer + "    x: 1\n    x: 2\n", ": line 5: gateway.server.x is written twice; it was first written at line 4"},
	})
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	for _, c := range []struct{ name, want string }{
		{accessKeySecretName, "yaml: unmarshal errors:\n  line 3: mapping key already defined at line 2"},
		{runKeyName, "yaml: unmarshal errors:\n  line 3: mapping key already defined at line 2"},
		{"runtime", "yaml: unmarshal errors:\n  line 3: mapping key \"runtime\" already defined at line 2"},
	} {
		writeRaw(t, path, "harness:\n  "+c.name+": claude\n  "+c.name+": codex\n")
		_, err := config.Document(root)
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		if c.name != "runtime" && err != nil && strings.Contains(err.Error(), c.name) {
			t.Errorf("%s: the error contains the secret: %v", c.name, err)
		}
	}
}

// TestEveryPlaceOfAnEntryWithASecretInItsNameIsItsLine is each refusal of a credential,
// a tool, an integration and an image whose name contains an access key secret or a run
// key, other than an unknown key: the error places the entry by the section and the
// name's line, and does not contain the name. A plain name is placed by the name as
// before.
func TestEveryPlaceOfAnEntryWithASecretInItsNameIsItsLine(t *testing.T) {
	isolate(t)
	lower := []string{"qak_abc", "qrk_abc"}
	credential := func(name, body string) string { return "gateway:\n  credentials:\n    " + name + ":\n" + body }
	tool := func(name, body string) string { return "gateway:\n  tools:\n    " + name + ":\n" + body }
	integration := func(name, body string) string { return "gateway:\n  integrations:\n    " + name + ":\n" + body }
	image := "wall:\n  adapter: docker\n  images:\n    "
	for i, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{credential(name, "      adapter: [relative]\n"), ": gateway.credentials (line 3).adapter is a program by its absolute path, and its arguments"},
			{credential(name, "      hosts: x\n"), ": line 4: gateway.credentials (line 3).hosts is not a list of strings"},
			{credential(name, "      auth:\n        <<: {scheme: basic}\n        [a]: 1\n"), ": line 6: gateway.credentials (line 3).auth has a key that is not a name"},
			{credential(name, "      env: [x]\n"), ": line 4: gateway.credentials (line 3).env is not a string"},
			{tool(name, "      command: gh\n"), ": line 4: gateway.tools (line 3).command is not a list of strings"},
			{tool(name, "      command: [/bin/gh]\n      argument: [x]\n"), ": line 5: gateway.tools (line 3).argument is not a string"},
		})
		foragerRefusals(t, lower[i], []struct{ body, want string }{
			{"gateway:\n  integrations:\n    " + lower[i] + ": {}\n    " + lower[i] + ": {}\n", ": gateway.integrations (line 4) is declared twice"},
			{integration(lower[i], "      program: [x]\n"), ": gateway.integrations (line 3).program is a path or a name on the PATH"},
			{integration(lower[i], "      program: bin/x\n"), ": gateway.integrations (line 3).program is not an absolute path; set program to an absolute path or a name on the PATH"},
			{integration(lower[i], "      settings: x\n"), ": gateway.integrations (line 3).settings is a mapping, the settings document"},
			{integration(lower[i], "      settings: {1: x}\n"), ": gateway.integrations (line 3).settings: line 4: a key that is not a string"},
			{image + lower[i] + ":\n      ref: a\n    " + lower[i] + ":\n      ref: b\n", ": wall.images (line 6) is defined twice"},
		})
		foragerRefusals(t, name, []struct{ body, want string }{
			{aliasedTool(name), ": line 3: gateway.tools (line 6) has a key that is not a name"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{credential("github", "      adapter: [relative]\n"), ": gateway.credentials.github.adapter is a program by its absolute path, and its arguments"},
		{credential("github", "      hosts: x\n"), ": line 4: gateway.credentials.github.hosts is not a list of strings"},
		{credential("github", "      auth:\n        <<: {scheme: basic}\n        [a]: 1\n"), ": line 6: gateway.credentials.github.auth has a key that is not a name"},
		{credential("github", "      env: [x]\n"), ": line 4: gateway.credentials.github.env is not a string"},
		{tool("gh", "      command: gh\n"), ": line 4: gateway.tools.gh.command is not a list of strings"},
		{tool("gh", "      command: [/bin/gh]\n      argument: [x]\n"), ": line 5: gateway.tools.gh.argument is not a string"},
		{"gateway:\n  integrations:\n    github: {}\n    github: {}\n", ": gateway.integrations.github is declared twice"},
		{integration("github", "      program: [x]\n"), ": gateway.integrations.github.program is a path or a name on the PATH"},
		{integration("github", "      program: bin/x\n"), ": gateway.integrations.github.program is not an absolute path; set program to an absolute path or a name on the PATH"},
		{integration("github", "      settings: x\n"), ": gateway.integrations.github.settings is a mapping, the settings document"},
		{integration("github", "      settings: {1: x}\n"), ": gateway.integrations.github.settings: line 4: a key that is not a string"},
		{image + "go:\n      ref: a\n    go:\n      ref: b\n", ": wall.images.go is defined twice"},
		{aliasedTool("gh"), ": line 3: gateway.tools.gh has a key that is not a name"},
	})
}

// TestAnUnknownKeyWithANewlineInItsNameIsReportedByItsLine is an unknown key of
// harness.yaml whose name contains an access key secret and a newline: the error is
// the key's line alone, as for any unknown key whose name contains a secret.
func TestAnUnknownKeyWithANewlineInItsNameIsReportedByItsLine(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness:\n  \"qak_SECRET\\nb\": 1\n")
	_, err := config.Document(root)
	if want := path + ": line 2: key is not one harness.yaml reads"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	if err != nil && strings.Contains(err.Error(), "qak_SECRET") {
		t.Errorf("the error contains the secret: %v", err)
	}
}

// aliasedTool is a forager.yaml whose tool name is written as an alias of a mapping
// that merges and has a key that is not a name.
func aliasedTool(name string) string {
	return "wall:\n  adapter: docker\n  images: {go: &t {<<: {}, [x]: 1}}\ngateway:\n  tools:\n    " + name + ": *t\n"
}

// TestASettingsKeyWithASecretInItsNameIsPlacedByItsLine is a key of an integration's
// settings whose name contains an access key secret or a run key, written twice, with a
// value JSON cannot represent, with such a value under it, and an anchor of such a name
// used as an alias: the error places the key by its line and does not contain the
// name. A plain name is printed as before.
func TestASettingsKeyWithASecretInItsNameIsPlacedByItsLine(t *testing.T) {
	isolate(t)
	settings := "gateway:\n  integrations:\n    github:\n      settings:\n"
	for _, name := range []string{accessKeySecretName, runKeyName} {
		foragerRefusals(t, name, []struct{ body, want string }{
			{settings + "        " + name + ": 1\n        " + name + ": 2\n", ": gateway.integrations.github.settings: line 6: a key is written twice; it was first written at line 5"},
			{settings + "        " + name + ": .inf\n", ": gateway.integrations.github.settings (line 5) is not a number that JSON can represent"},
			{settings + "        " + name + ":\n          a: .inf\n", ": gateway.integrations.github.settings (line 5).a is not a number that JSON can represent"},
			{settings + "        a: &" + name + " 1\n        b: *" + name + "\n", ": gateway.integrations.github.settings.b: line 6: the value is a YAML alias, which the settings may not contain; write the value out in full"},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{settings + "        x: 1\n        x: 2\n", ": gateway.integrations.github.settings.x appears twice"},
		{settings + "        x: .inf\n", ": gateway.integrations.github.settings.x is not a number that JSON can represent"},
		{settings + "        x:\n          a: .inf\n", ": gateway.integrations.github.settings.x.a is not a number that JSON can represent"},
		{settings + "        a: &x 1\n        b: *x\n", ": gateway.integrations.github.settings.b: line 6: *x is a YAML alias, which the settings may not contain; write the value out in full"},
	})
}

// TestADecoderRefusalWithASecretKeepsTheOtherReasons is a harness.yaml that the decoder
// refuses at two lines, one of them for a value that contains an access key secret: the
// line with the secret is given alone, and the other line keeps the decoder's reason.
func TestADecoderRefusalWithASecretKeepsTheOtherReasons(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness:\n  model: [x]\n  force: qak_x\n")
	_, err := config.Document(root)
	if want := path + ": yaml: unmarshal errors:\n  line 2: cannot unmarshal !!seq into string\n  line 3: qory cannot read the value here. The YAML decoder's message contains an access key secret or a run key, so qory does not print it."; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// isolate is [hermetic], with the XDG cache and state directories under the test's home
// too, so a test reads no file of the machine it runs on.
func isolate(t *testing.T) {
	t.Helper()
	home := hermetic(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
}

// TestARefusalIsPlacedByTheLineAtItsStart is a harness.yaml value whose tag contains a
// line number, the words of an unknown key and an access key secret: the refusal gives
// the line at the start of the decoder's refusal, not the one in the tag.
func TestARefusalIsPlacedByTheLineAtItsStart(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness:\n  force: !line%2099:%20field%20qak_x 1\n")
	_, err := config.Document(root)
	want := path + ": line 2: qory cannot read the value here. The YAML decoder's message contains an access key secret or a run key, so qory does not print it."
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}

// The phrases a message prints in place of a name that contains an access key secret
// or a run key, where qory has no line to place the name by.
const (
	accessKeyPhrase = "(a name that contains an access key secret)"
	runKeyPhrase    = "(a name that contains a run key)"
)

// TestAnIntegrationKeyWithASecretIsLeftOutOfExpand is an integration whose key
// contains an access key secret or a run key, refused once its program runs: its
// program is not on the PATH, its describe fails, it plays no role qory expands, and
// the credential it defines is one Forager's gateway refuses. Each copy of the key, in
// the entry, the program's name and path, and Forager's words, is the phrase. A plain
// key prints as before.
func TestAnIntegrationKeyWithASecretIsLeftOutOfExpand(t *testing.T) {
	isolate(t)
	dir := onPath(t)
	failing := "#!/bin/sh\necho 'the key file /k.pem is readable by others' >&2\nexit 3\n"
	for _, c := range []struct{ key, phrase string }{{"qak_x", accessKeyPhrase}, {"qrk_ab", runKeyPhrase}, {"plain", ""}} {
		shown := func(v string) string {
			if c.phrase != "" {
				return c.phrase
			}
			return v
		}
		at := "gateway.integrations." + shown(c.key) + ": "
		path := foragerFile(t, "gateway:\n  integrations:\n    "+c.key+": {}\n")
		if _, err := expand(t); err == nil || err.Error() != path+": "+at+shown("qory-"+c.key)+" is not on the PATH; install it there, or set program to its path" {
			t.Errorf("%s not on the PATH: %v", c.key, err)
		}
		program := filepath.Join(dir, "qory-"+c.key)
		if err := os.WriteFile(program, []byte(failing), 0o755); err != nil {
			t.Fatal(err)
		}
		// The resolved path is a value, so it prints as Forager's value phrase.
		resolvedPath := resolved(t, program)
		if c.phrase != "" {
			resolvedPath = strings.Replace(c.phrase, "name", "value", 1)
		}
		if _, err := expand(t); err == nil || err.Error() != path+": "+at+resolvedPath+" describe: exit status 3: the key file /k.pem is readable by others" {
			t.Errorf("%s describe fails: %v", c.key, err)
		}
		fakeIntegration(t, dir, "qory-"+c.key, `{"version": 1, "name": "queue", "title": "Queue", "program_version": "1", "settings": {"type": "object"}, "roles": {"example_role": {}}}`)
		if _, err := expand(t); err == nil || err.Error() != path+": "+at+shown("qory-"+c.key)+" plays the role example_role, none of which qory expands, and defines nothing" {
			t.Errorf("%s plays no role: %v", c.key, err)
		}
		fakeIntegration(t, dir, "qory-"+c.key, strings.Replace(fixture(t, "acme-tracker.json"), `"argument": "[A-Z]+"`, `"argument": "("`, 1))
		if _, err := expand(t); err == nil || !strings.HasPrefix(err.Error(), path+": "+at+"credential "+shown(c.key)+": ") {
			t.Errorf("%s Forager refuses the credential: %v", c.key, err)
		}
	}
}

// TestASelectedNameWithASecretIsLeftOutOfItsRefusal is an entry of gateway.selects
// whose name contains an access key secret or a run key, refused for each reason: no
// credential has the name, the credential takes no argument and the entry gives one,
// the credential takes an argument and the entry gives no forge, and the forge is not
// one qory knows. Each refusal prints the phrase in place of the name.
func TestASelectedNameWithASecretIsLeftOutOfItsRefusal(t *testing.T) {
	isolate(t)
	const credentials = "  credentials:\n    qak_env:\n      env: MODEL_TOKEN\n      hosts: [api.model.example]\n      auth: {scheme: bearer}\n    qrk_adapter:\n      adapter: [/opt/adapters/a, \"${argument}\"]\n      argument: \"[a-z]+\"\n      hosts: [api.github.com]\n"
	const at = "forager.yaml: gateway.selects.credentials: "
	for _, c := range []struct{ entry, want string }{
		{"{name: qak_x}", at + accessKeyPhrase + " is no credential that gateway.credentials or gateway.integrations defines"},
		{"{name: QRK_ab}", at + runKeyPhrase + " is no credential that gateway.credentials or gateway.integrations defines"},
		{"{name: qak_env, forge: github.com}", at + accessKeyPhrase + " takes no argument, so argument_label and forge do not apply"},
		{"{name: qrk_adapter, argument_label: repository}", at + runKeyPhrase + " needs argument_label and forge"},
		{"{name: qrk_adapter, argument_label: repository, forge: gitlab.example}", at + runKeyPhrase + ": forge gitlab.example is not one qory knows the repository form of; use github.com"},
	} {
		foragerFile(t, "gateway:\n"+credentials+"  selects:\n    credentials:\n      - "+c.entry+"\n")
		r, err := config.LoadForager()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Selections(); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.entry, err, c.want)
		}
	}
}

// TestTheForagerRowsLeaveOutANameWithASecret is the rows qory config prints for a
// forager.yaml whose credential, tool, integration, selected credential, image and
// wall variables have names that contain an access key secret or a run key: each name
// is the phrase, in the row's key and in its value. Plain names print as before.
func TestTheForagerRowsLeaveOutANameWithASecret(t *testing.T) {
	isolate(t)
	foragerFile(t, `gateway:
  credentials:
    qak_c: {env: MODEL_TOKEN, hosts: [api.model.example], auth: {scheme: bearer}}
    plain: {env: PLAIN_TOKEN, hosts: [api.model.example], auth: {scheme: bearer}}
    secret_env: {env: QRK_TOKEN, hosts: [api.model.example], auth: {scheme: bearer}}
  tools:
    qrk_t: {command: [/opt/tool], serves: [a.example]}
  integrations:
    qak_c: {}
    qrk_i: {}
    plain: {}
  selects:
    credentials:
      - {name: qak_c}
wall:
  adapter: docker
  image: qak_img
  images: {qak_img: {ref: "example/img:1"}}
  env: [QAK_E, PLAIN_E]
  ca_env: [QRK_CA]
`)
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range r.Rows() {
		got[row.Key] = row.Value
	}
	for key, value := range map[string]string{
		"gateway.credentials." + accessKeyPhrase:         "env MODEL_TOKEN",
		"gateway.credentials.plain":                      "env PLAIN_TOKEN",
		"gateway.credentials.secret_env":                 "env " + runKeyPhrase,
		"gateway.tools." + runKeyPhrase:                  "/opt/tool, serves a.example",
		"gateway.integrations." + accessKeyPhrase:        accessKeyPhrase + ", shadowed by gateway.credentials." + accessKeyPhrase,
		"gateway.integrations." + runKeyPhrase:           runKeyPhrase,
		"gateway.integrations.plain":                     "qory-plain, shadowed by gateway.credentials.plain",
		"gateway.selects.credentials." + accessKeyPhrase: "(no argument)",
		"wall.image":  accessKeyPhrase + " (wall.images." + accessKeyPhrase + ")",
		"wall.env":    accessKeyPhrase + ", PLAIN_E",
		"wall.ca_env": runKeyPhrase,
	} {
		if got[key] != value {
			t.Errorf("row %s = %q, want %q", key, got[key], value)
		}
	}
	if _, ok := got["wall.images."+accessKeyPhrase]; !ok {
		t.Errorf("no row wall.images.%s", accessKeyPhrase)
	}
	for key, value := range got {
		if strings.Contains(strings.ToLower(key+value), "qak_") || strings.Contains(strings.ToLower(key+value), "qrk_") {
			t.Errorf("row %s %s contains a secret", key, value)
		}
	}
}

// TestAnIntegrationCredentialRowLeavesOutItsKey is the row of a credential that an
// integration whose key contains an access key secret defines: the row's key and the
// integration's key in its value are the phrase.
func TestAnIntegrationCredentialRowLeavesOutItsKey(t *testing.T) {
	isolate(t)
	dir := onPath(t)
	fakeIntegration(t, dir, "qory-qak_x", fixture(t, "acme-tracker.json"))
	foragerFile(t, "gateway:\n  integrations:\n    qak_x: {}\n")
	r, err := expand(t)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range r.Rows() {
		got[row.Key] = row.Value
	}
	if v := got["gateway.credentials."+accessKeyPhrase]; v != "integration "+accessKeyPhrase {
		t.Errorf("credential row %q, want %q", v, "integration "+accessKeyPhrase)
	}
	if v := got["gateway.integrations."+accessKeyPhrase]; v != accessKeyPhrase+" 0.1.0" {
		t.Errorf("integration row %q, want %q", v, accessKeyPhrase+" 0.1.0")
	}
}
