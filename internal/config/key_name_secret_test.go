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
		{accessKeySecretName, foragerServer + "    " + accessKeySecretName + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is not one forager.yaml reads.\n  It is at line 4 of {path}."},
		{runKeyName, foragerServer + "    " + runKeyName + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is not one forager.yaml reads.\n  It is at line 4 of {path}."},
		{accessKeySecretName, pinEntry + "        " + accessKeySecretName + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is not one forager.yaml reads.\n  It is at line 7 of {path}."},
		{runKeyName, pinEntry + "        " + runKeyName + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is not one forager.yaml reads.\n  It is at line 7 of {path}."},
		{accessKeySecretName, foragerServer + "    " + accessKeySecretName + " x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is not one forager.yaml reads.\n  It is at line 4 of {path}."},
		{accessKeySecretName, "gateway:\n  server:\n    url: &" + accessKeySecretName + " https://qory.example\n    *" + accessKeySecretName + " : 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}."},
		{runKeyName, "gateway:\n  server:\n    url: &" + runKeyName + " https://qory.example\n    *" + runKeyName + " : 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}."},
	} {
		path := foragerFile(t, c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != strings.ReplaceAll(c.want, "{path}", path) {
			t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(c.want, "{path}", path))
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
		want := "Remove the key, or rename it to one harness.yaml reads.\n  Key is not one harness.yaml reads.\n  It is at line 2 of " + path + "."
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
	if want := "Remove the key, or rename it to one forager.yaml reads.\n  Key \"events2\" is not one forager.yaml reads.\n  It is at line 4 of " + path + "."; err == nil || err.Error() != want {
		t.Errorf("forager.yaml: %v, want %q", err, want)
	}
	root := t.TempDir()
	path = filepath.Join(root, "harness.yaml")
	writeRaw(t, path, "harness:\n  events2: 1\n")
	_, err = config.Document(root)
	if want := "Remove the key, or rename it to one harness.yaml reads.\n  Key \"events2\" is not one harness.yaml reads.\n  It is at line 2 of " + path + "."; err == nil || err.Error() != want {
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
		if err == nil || err.Error() != strings.ReplaceAll(c.want, "{path}", path) {
			t.Errorf("%q: %v, want %q", c.body, err, strings.ReplaceAll(c.want, "{path}", path))
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
			{credential + name + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.credentials.github: key is not one.\n  It is at line 5 of {path}."},
			{tool + name + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.tools.gh: key is not one.\n  It is at line 5 of {path}."},
			{integration + name + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.integrations.github: key is not one.\n  It is at line 4 of {path}."},
			{image + name + ": 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  wall.images.go: key is not one. An image has ref, runtime, docker.\n  It is at line 6 of {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{credential + "x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.credentials.github: key \"x\" is not one.\n  The file is {path}."},
		{tool + "x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.tools.gh: key \"x\" is not one.\n  The file is {path}."},
		{integration + "x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.integrations.github: key \"x\" is not one.\n  The file is {path}."},
		{image + "x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  wall.images.go: key \"x\" is not one. An image has ref, runtime, docker.\n  The file is {path}."},
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
			{"wall:\n  adapter: docker\n  images:\n    " + name + ":\n      ref: a\n", "Rename each image of wall.images to 1 to 64 of a-z, 0-9, underscore, dot, and dash.\n  the name at line 4 is not one. A name starts with a letter or a digit.\n  wall.images is in {path}."},
			{"gateway:\n  integrations:\n    " + name + ":\n      program: x\n", "Rename each integration of gateway.integrations to 1 to 64 of a-z, 0-9, underscore, and dash.\n  the name at line 3 is not one. A name starts with a letter or a digit.\n  gateway.integrations is in {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"wall:\n  adapter: docker\n  images:\n    Go:\n      ref: a\n", "Rename each image of wall.images to 1 to 64 of a-z, 0-9, underscore, dot, and dash.\n  \"Go\" is not one. A name starts with a letter or a digit.\n  wall.images is in {path}."},
		{"gateway:\n  integrations:\n    GitHub:\n      program: x\n", "Rename each integration of gateway.integrations to 1 to 64 of a-z, 0-9, underscore, and dash.\n  \"GitHub\" is not one. A name starts with a letter or a digit.\n  gateway.integrations is in {path}."},
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
			{"gateway:\n  credentials:\n    " + name + ":\n      env: GH\n      x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.credentials (line 3): key \"x\" is not one.\n  The file is {path}."},
			{"gateway:\n  credentials:\n    " + name + ":\n      file: relative\n", "Set gateway.credentials (line 3).file to an absolute path.\n  The value is not one.\n  gateway.credentials (line 3) is in {path}."},
			{"gateway:\n  tools:\n    " + name + ":\n      command: gh\n      x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.tools (line 3): key \"x\" is not one.\n  The file is {path}."},
			{"gateway:\n  tools:\n    " + name + ":\n      command: [gh]\n", "Set gateway.tools (line 3).command to a program's absolute path, then its arguments.\n  The value is not one.\n  gateway.tools (line 3) is in {path}."},
		})
		foragerRefusals(t, lower[i], []struct{ body, want string }{
			{"gateway:\n  integrations:\n    " + lower[i] + ":\n      x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.integrations (line 3): key \"x\" is not one.\n  The file is {path}."},
			{"gateway:\n  integrations:\n    " + lower[i] + ": 1\n", "Set gateway.integrations (line 3) to a mapping of program and settings.\n  It is not a mapping.\n  gateway.integrations (line 3) is in {path}."},
			{"wall:\n  adapter: docker\n  images:\n    " + lower[i] + ":\n      ref: a\n      x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  wall.images (line 4): key \"x\" is not one. An image has ref, runtime, docker.\n  The file is {path}."},
			{"wall:\n  adapter: docker\n  images:\n    " + lower[i] + ":\n      runtime: runc\n", "Set wall.images (line 4).ref to the image's reference.\n  wall.images (line 4).ref is required.\n  wall.images (line 4) is in {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"gateway:\n  credentials:\n    github:\n      env: GH\n      x: 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  gateway.credentials.github: key \"x\" is not one.\n  The file is {path}."},
		{"gateway:\n  credentials:\n    github:\n      file: relative\n", "Set gateway.credentials.github.file to an absolute path.\n  The value is not one.\n  gateway.credentials.github is in {path}."},
		{"gateway:\n  tools:\n    gh:\n      command: [gh]\n", "Set gateway.tools.gh.command to a program's absolute path, then its arguments.\n  The value is not one.\n  gateway.tools.gh is in {path}."},
		{"gateway:\n  integrations:\n    github: 1\n", "Set gateway.integrations.github to a mapping of program and settings.\n  It is not a mapping.\n  gateway.integrations.github is in {path}."},
		{"wall:\n  adapter: docker\n  images:\n    go:\n      runtime: runc\n", "Set wall.images.go.ref to the image's reference.\n  wall.images.go.ref is required.\n  wall.images.go is in {path}."},
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
		{accessKeySecretName + " x", "Key is not one harness.yaml reads."},
		{runKeyName + " x", "Key is not one harness.yaml reads."},
		{"a b", `Key "a b" is not one harness.yaml reads.`},
	} {
		writeRaw(t, path, "harness:\n  "+c.name+": 1\n")
		_, err := config.Document(root)
		if want := "Remove the key, or rename it to one harness.yaml reads.\n  " + c.want + "\n  It is at line 2 of " + path + "."; err == nil || err.Error() != want {
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
			{"session:\n  instance:\n    name: &" + name + " build\n  *" + name + " : 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{"session:\n  instance:\n    name: &a build\n  *a : 1\n", "Remove the key, or rename it to one forager.yaml reads.\n  Key *a is an alias of a key forager.yaml does not read.\n  It is at line 4 of {path}."},
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
			{foragerServer + "    " + name + ": 1\n    " + name + ": 2\n", "Remove the key that is written twice, at the line below.\n  a key of gateway.server is written twice; it was first written at line 4.\n  It is at line 5 of {path}."},
			{name + ": 1\n" + name + ": 2\n", "Remove the key that is written twice, at the line below.\n  a key is written twice; it was first written at line 1.\n  It is at line 2 of {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{foragerServer + "    x: 1\n    x: 2\n", "Remove one of the two keys gateway.server.x.\n  gateway.server.x is written twice; it was first written at line 4.\n  It is at line 5 of {path}."},
	})
	root := t.TempDir()
	path := filepath.Join(root, "harness.yaml")
	for _, c := range []struct{ name, want string }{
		{accessKeySecretName, "Line 3: mapping key already defined at line 2."},
		{runKeyName, "Line 3: mapping key already defined at line 2."},
		{"runtime", "Line 3: mapping key \"runtime\" already defined at line 2."},
	} {
		writeRaw(t, path, "harness:\n  "+c.name+": claude\n  "+c.name+": codex\n")
		_, err := config.Document(root)
		if want := "Remove the key that is written twice.\n  " + c.want + "\n  The file is " + path + "."; err == nil || err.Error() != want {
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
			{credential(name, "      adapter: [relative]\n"), "Set gateway.credentials (line 3).adapter to a program's absolute path, then its arguments.\n  The value is not one.\n  gateway.credentials (line 3) is in {path}."},
			{credential(name, "      hosts: x\n"), "Set gateway.credentials (line 3).hosts to a list of strings.\n  gateway.credentials (line 3).hosts is not a list of strings.\n  It is at line 4 of {path}."},
			{credential(name, "      auth:\n        <<: {scheme: basic}\n        [a]: 1\n"), "Give each key of gateway.credentials (line 3).auth a name.\n  gateway.credentials (line 3).auth has a key that is not a name.\n  It is at line 6 of {path}."},
			{credential(name, "      env: [x]\n"), "Set gateway.credentials (line 3).env to a string.\n  gateway.credentials (line 3).env is not a string.\n  It is at line 4 of {path}."},
			{tool(name, "      command: gh\n"), "Set gateway.tools (line 3).command to a list of strings.\n  gateway.tools (line 3).command is not a list of strings.\n  It is at line 4 of {path}."},
			{tool(name, "      command: [/bin/gh]\n      argument: [x]\n"), "Set gateway.tools (line 3).argument to a string.\n  gateway.tools (line 3).argument is not a string.\n  It is at line 5 of {path}."},
		})
		foragerRefusals(t, lower[i], []struct{ body, want string }{
			{"gateway:\n  integrations:\n    " + lower[i] + ": {}\n    " + lower[i] + ": {}\n", "Declare gateway.integrations (line 4) once.\n  gateway.integrations (line 4) is declared twice.\n  gateway.integrations is in {path}."},
			{integration(lower[i], "      program: [x]\n"), "Set gateway.integrations (line 3).program to a path or a name on the PATH.\n  The value is not one.\n  gateway.integrations (line 3) is in {path}."},
			{integration(lower[i], "      program: bin/x\n"), "Set gateway.integrations (line 3).program to an absolute path or a name on the PATH.\n  The value is a relative path.\n  gateway.integrations (line 3) is in {path}."},
			{integration(lower[i], "      settings: x\n"), "Set gateway.integrations (line 3).settings to a mapping, the settings document.\n  It is not a mapping.\n  gateway.integrations (line 3) is in {path}."},
			{integration(lower[i], "      settings: {1: x}\n"), "Make each key of gateway.integrations (line 3).settings a string.\n  The key at line 4 is not a string.\n  The file is {path}."},
			{image + lower[i] + ":\n      ref: a\n    " + lower[i] + ":\n      ref: b\n", "Define wall.images (line 6) once.\n  wall.images (line 6) is defined twice.\n  wall.images is in {path}."},
		})
		foragerRefusals(t, name, []struct{ body, want string }{
			{aliasedTool(name), "Give each key of gateway.tools (line 6) a name.\n  gateway.tools (line 6) has a key that is not a name.\n  It is at line 3 of {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{credential("github", "      adapter: [relative]\n"), "Set gateway.credentials.github.adapter to a program's absolute path, then its arguments.\n  The value is not one.\n  gateway.credentials.github is in {path}."},
		{credential("github", "      hosts: x\n"), "Set gateway.credentials.github.hosts to a list of strings.\n  gateway.credentials.github.hosts is not a list of strings.\n  It is at line 4 of {path}."},
		{credential("github", "      auth:\n        <<: {scheme: basic}\n        [a]: 1\n"), "Give each key of gateway.credentials.github.auth a name.\n  gateway.credentials.github.auth has a key that is not a name.\n  It is at line 6 of {path}."},
		{credential("github", "      env: [x]\n"), "Set gateway.credentials.github.env to a string.\n  gateway.credentials.github.env is not a string.\n  It is at line 4 of {path}."},
		{tool("gh", "      command: gh\n"), "Set gateway.tools.gh.command to a list of strings.\n  gateway.tools.gh.command is not a list of strings.\n  It is at line 4 of {path}."},
		{tool("gh", "      command: [/bin/gh]\n      argument: [x]\n"), "Set gateway.tools.gh.argument to a string.\n  gateway.tools.gh.argument is not a string.\n  It is at line 5 of {path}."},
		{"gateway:\n  integrations:\n    github: {}\n    github: {}\n", "Declare gateway.integrations.github once.\n  gateway.integrations.github is declared twice.\n  gateway.integrations is in {path}."},
		{integration("github", "      program: [x]\n"), "Set gateway.integrations.github.program to a path or a name on the PATH.\n  The value is not one.\n  gateway.integrations.github is in {path}."},
		{integration("github", "      program: bin/x\n"), "Set gateway.integrations.github.program to an absolute path or a name on the PATH.\n  The value is a relative path.\n  gateway.integrations.github is in {path}."},
		{integration("github", "      settings: x\n"), "Set gateway.integrations.github.settings to a mapping, the settings document.\n  It is not a mapping.\n  gateway.integrations.github is in {path}."},
		{integration("github", "      settings: {1: x}\n"), "Make each key of gateway.integrations.github.settings a string.\n  The key at line 4 is not a string.\n  The file is {path}."},
		{image + "go:\n      ref: a\n    go:\n      ref: b\n", "Define wall.images.go once.\n  wall.images.go is defined twice.\n  wall.images is in {path}."},
		{aliasedTool("gh"), "Give each key of gateway.tools.gh a name.\n  gateway.tools.gh has a key that is not a name.\n  It is at line 3 of {path}."},
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
	if want := "Remove the key, or rename it to one harness.yaml reads.\n  Line 2: key is not one harness.yaml reads.\n  The file is " + path + "."; err == nil || err.Error() != want {
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
			{settings + "        " + name + ": 1\n        " + name + ": 2\n", "Remove one of the two keys of gateway.integrations.github.settings that are written twice.\n  Line 6 writes a key again; it was first written at line 5.\n  The file is {path}."},
			{settings + "        " + name + ": .inf\n", "Set gateway.integrations.github.settings (line 5) to a number that JSON can represent.\n  The value is not one.\n  The file is {path}."},
			{settings + "        " + name + ":\n          a: .inf\n", "Set gateway.integrations.github.settings (line 5).a to a number that JSON can represent.\n  The value is not one.\n  The file is {path}."},
			{settings + "        a: &" + name + " 1\n        b: *" + name + "\n", "Write the value of gateway.integrations.github.settings.b out in full.\n  The value at line 6 is a YAML alias, which the settings may not contain.\n  The file is {path}."},
		})
	}
	foragerRefusals(t, "", []struct{ body, want string }{
		{settings + "        x: 1\n        x: 2\n", "Remove one of the two gateway.integrations.github.settings.x keys.\n  The key appears twice.\n  The file is {path}."},
		{settings + "        x: .inf\n", "Set gateway.integrations.github.settings.x to a number that JSON can represent.\n  The value is not one.\n  The file is {path}."},
		{settings + "        x:\n          a: .inf\n", "Set gateway.integrations.github.settings.x.a to a number that JSON can represent.\n  The value is not one.\n  The file is {path}."},
		{settings + "        a: &x 1\n        b: *x\n", "Write the value of gateway.integrations.github.settings.b out in full.\n  *x at line 6 is a YAML alias, which the settings may not contain.\n  The file is {path}."},
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
	if want := "Set each value in the file to the type its key takes.\n  Line 2: cannot unmarshal !!seq into string.\n  Line 3: qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key, a run code or an enrolment code, so qory does not print it.\n  The file is " + path + "."; err == nil || err.Error() != want {
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
	want := "Set each value in the file to the type its key takes.\n  Line 2: qory cannot read the value here. The YAML decoder's message contains an access key secret, a run key, a run code or an enrolment code, so qory does not print it.\n  The file is " + path + "."
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
		at := "gateway.integrations." + shown(c.key)
		path := foragerFile(t, "gateway:\n  integrations:\n    "+c.key+": {}\n")
		fixProgram, where := "Fix the program of "+at+".\n  ", "\n  "+at+" is in "+path+"."
		if _, err := expand(t); err == nil || err.Error() != fixProgram+shown("qory-"+c.key)+" is not on the PATH; install it there, or set program to its path."+where {
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
		if _, err := expand(t); err == nil || err.Error() != fixProgram+resolvedPath+" describe: exit status 3: the key file /k.pem is readable by others."+where {
			t.Errorf("%s describe fails: %v", c.key, err)
		}
		fakeIntegration(t, dir, "qory-"+c.key, `{"version": 1, "name": "queue", "title": "Queue", "program_version": "1", "settings": {"type": "object"}, "roles": {"example_role": {}}}`)
		if _, err := expand(t); err == nil || err.Error() != "Remove "+at+", or give it a program that defines a credential.\n  "+shown("qory-"+c.key)+" plays the role example_role, none of which qory expands, and defines nothing."+where {
			t.Errorf("%s plays no role: %v", c.key, err)
		}
		fakeIntegration(t, dir, "qory-"+c.key, strings.Replace(fixture(t, "acme-tracker.json"), `"argument": "[A-Z]+"`, `"argument": "("`, 1))
		if _, err := expand(t); err == nil || !strings.HasPrefix(err.Error(), "Fix the credential that "+at+" defines.\n  Credential "+shown(c.key)+": ") || !strings.HasSuffix(err.Error(), where) {
			t.Errorf("%s Forager refuses the credential: %v", c.key, err)
		}
	}
}

// TestTheForagerRowsLeaveOutANameWithASecret is the rows qory config prints for a
// forager.yaml whose credential, tool, integration, image and
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
		"gateway.credentials." + accessKeyPhrase:  "env MODEL_TOKEN",
		"gateway.credentials.plain":               "env PLAIN_TOKEN",
		"gateway.credentials.secret_env":          "env " + runKeyPhrase,
		"gateway.tools." + runKeyPhrase:           "/opt/tool, serves a.example",
		"gateway.integrations." + accessKeyPhrase: accessKeyPhrase + ", shadowed by gateway.credentials." + accessKeyPhrase,
		"gateway.integrations." + runKeyPhrase:    runKeyPhrase,
		"gateway.integrations.plain":              "qory-plain, shadowed by gateway.credentials.plain",
		"wall.image":                              accessKeyPhrase + " (wall.images." + accessKeyPhrase + ")",
		"wall.env":                                accessKeyPhrase + ", PLAIN_E",
		"wall.ca_env":                             runKeyPhrase,
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
