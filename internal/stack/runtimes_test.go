package stack

import (
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// withRuntimes is a valid stack with runtimes set to body.
func withRuntimes(body string) string {
	return valid + "runtimes:\n" + body
}

// TestLoadReadsTheRuntimePins reads each runtime's pinned version, with the stack file
// as the file that sets it, and a stack with no runtimes as no pins.
func TestLoadReadsTheRuntimePins(t *testing.T) {
	isolate(t)
	path := write(t, withRuntimes("  claude:\n    version: 2.1.300\n  codex:\n    version: 0.167.2\n"))
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]RuntimePin{"claude": {Version: "2.1.300", From: path}, "codex": {Version: "0.167.2", From: path}}
	if len(p.RuntimePins) != len(want) || p.RuntimePins["claude"] != want["claude"] || p.RuntimePins["codex"] != want["codex"] {
		t.Fatalf("the pins are %+v, want %+v", p.RuntimePins, want)
	}
	if p, err = Load(write(t, valid)); err != nil || p.RuntimePins != nil {
		t.Fatalf("no runtimes: %+v, %v", p.RuntimePins, err)
	}
}

// TestLoadRefusesABadRuntimePin refuses a pin that is not an exact version, latest among
// them, and a runtime that qory does not install, each with the fix first, the cause,
// and the file last. The error is compared whole.
func TestLoadRefusesABadRuntimePin(t *testing.T) {
	isolate(t)
	version := func(name, value string) string {
		return "Set runtimes." + name + ".version to an exact version, or remove runtimes." + name + ".\n" +
			"  An exact version starts with a digit, followed by up to 63 letters, digits, ., + or -. A runtime with no pin installs its latest version.\n" +
			"  The value is \"" + value + "\".\n  runtimes." + name + " is in "
	}
	for _, c := range []struct{ body, want string }{
		{"  claude:\n    version: latest\n", version("claude", "latest")},
		{"  codex:\n    version: v0.167.2\n", version("codex", "v0.167.2")},
		{"  codex:\n    version: \"0.167 2\"\n", version("codex", "0.167 2")},
		{"  claude: {}\n", version("claude", "")},
		{"  gemini:\n    version: 1.0.0\n", "Remove gemini from runtimes.\n  qory installs and pins only claude and codex.\n  runtimes is in "},
	} {
		path := write(t, withRuntimes(c.body))
		_, err := Load(path)
		if want := c.want + path + "."; err == nil || err.Error() != want {
			t.Errorf("%q:\n%v\nwant:\n%s", c.body, err, want)
		}
	}
}

// TestNewComposeReadsTheRuntimePinsOfACheckout reads the pins of the harness section of a
// checkout's qory.yaml, beside extends, with that file as the one that sets them, and
// refuses a pin of latest there as in a stack file.
func TestNewComposeReadsTheRuntimePinsOfACheckout(t *testing.T) {
	p, err := compose(t, &Stack{Extends: Source{Path: "../base"}, RuntimePins: map[string]RuntimePin{"codex": {Version: "0.167.2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.RuntimePins["codex"]; got.Version != "0.167.2" || got.From != p.File {
		t.Fatalf("the pin is %+v, want 0.167.2 from %s", got, p.File)
	}
	_, err = compose(t, &Stack{Extends: Source{Path: "../base"}, RuntimePins: map[string]RuntimePin{"codex": {Version: "latest"}}})
	if err == nil || !strings.HasPrefix(err.Error(), "Set runtimes.codex.version ") {
		t.Fatalf("a pin of latest: %v", err)
	}
}

// TestExtendTakesTheNearerPinOfEachRuntime extends a base that pins claude and codex
// with a checkout that pins codex: the checkout's pin of codex wins, from its file, and
// the base's pin of claude stays, from the base's file. Neither file pinning gives none.
func TestExtendTakesTheNearerPinOfEachRuntime(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{}, Root: "/repo", File: "/repo/qory-stack.yaml",
		RuntimePins: map[string]RuntimePin{"claude": {Version: "2.1.300", From: "/repo/qory-stack.yaml"}, "codex": {Version: "0.167.2", From: "/repo/qory-stack.yaml"}}}
	p := &Stack{Extends: Source{Path: "../repo"}, File: "/app/qory.yaml",
		RuntimePins: map[string]RuntimePin{"codex": {Version: "0.170.0", From: "/app/qory.yaml"}}}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]RuntimePin{"claude": {Version: "2.1.300", From: "/repo/qory-stack.yaml"}, "codex": {Version: "0.170.0", From: "/app/qory.yaml"}}
	if len(out.RuntimePins) != 2 || out.RuntimePins["claude"] != want["claude"] || out.RuntimePins["codex"] != want["codex"] {
		t.Fatalf("the pins are %+v, want %+v", out.RuntimePins, want)
	}
	if base.RuntimePins["codex"].Version != "0.167.2" {
		t.Fatalf("Extend changed the base's pins: %+v", base.RuntimePins)
	}
	base.RuntimePins, p.RuntimePins = nil, nil
	if out, err = Extend(base, p); err != nil || out.RuntimePins != nil {
		t.Fatalf("no pins: %+v, %v", out.RuntimePins, err)
	}
}

// TestTheSchemaTakesTheRuntimePins validates stacks with runtimes against
// stack.schema.json: an exact version of claude or codex passes, and a pin that Load
// refuses fails.
func TestTheSchemaTakesTheRuntimePins(t *testing.T) {
	s, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"  claude:\n    version: 2.1.300\n", true},
		{"  codex:\n    version: 0.99.0-alpha.20\n", true},
		{"  claude:\n    version: latest\n", false},
		{"  claude:\n    version: \"\"\n", false},
		{"  claude: {}\n", false},
		{"  claude:\n    version: 2.1.300\n    channel: stable\n", false},
		{"  gemini:\n    version: 1.0.0\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(withRuntimes(c.body)), &doc); err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %t", c.body, err, c.valid)
		}
	}
}
