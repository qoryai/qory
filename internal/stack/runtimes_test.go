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

// TestLoadRefusesABadRuntimePin refuses a pin that is not an exact version, latest and a
// version with + among them, a version that YAML reads as a number, and a runtime that
// qory does not install, each with the fix first, the cause, and the file last. The error
// is compared whole.
func TestLoadRefusesABadRuntimePin(t *testing.T) {
	isolate(t)
	version := func(name, value string) string {
		return "Set runtimes." + name + ".version to an exact version, or remove runtimes." + name + ".\n" +
			"  An exact version starts with a digit, followed by up to 63 letters, digits, . or -. A runtime with no pin installs its latest version.\n" +
			"  The value is \"" + value + "\".\n  runtimes." + name + " is in "
	}
	number := func(name, value string) string {
		return "Put runtimes." + name + ".version in quotes, such as version: \"2.1.320\".\n" +
			"  Without quotes, YAML reads the version as a number or a date, and an exact version is a string.\n" +
			"  The value is " + value + ".\n  runtimes." + name + " is in "
	}
	for _, c := range []struct{ body, want string }{
		{"  claude:\n    version: latest\n", version("claude", "latest")},
		{"  codex:\n    version: v0.167.2\n", version("codex", "v0.167.2")},
		{"  codex:\n    version: \"0.167 2\"\n", version("codex", "0.167 2")},
		{"  claude: {}\n", version("claude", "")},
		{"  claude:\n    version: 1.0.0+build.1\n", version("claude", "1.0.0+build.1")},
		{"  claude:\n    version: 2.1\n", number("claude", "2.1")},
		{"  codex:\n    version: 2\n", number("codex", "2")},
		{"  claude:\n    version: 2024-01-01\n", number("claude", "2024-01-01")},
		{"  gemini:\n    version: 1.0.0\n", "Remove gemini from runtimes.\n  qory installs and pins only claude and codex.\n  runtimes is in "},
	} {
		path := write(t, withRuntimes(c.body))
		_, err := Load(path)
		if want := c.want + path + "."; err == nil || err.Error() != want {
			t.Errorf("%q:\n%v\nwant:\n%s", c.body, err, want)
		}
	}
}

// TestLoadReportsAPinsKeysAndTypeAsTheDecoderDoes reads a pin with a key a pin does not
// have, a pin that is no mapping, and a version that is no scalar: each is the error of
// the strict decoder, with the line and the type stack.RuntimePin.
func TestLoadReportsAPinsKeysAndTypeAsTheDecoderDoes(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ body, want string }{
		{"  claude:\n    version: 2.1.300\n    channel: stable\n", "Key \"channel\" is not one qory-stack.yaml reads.\n  It is at line 16 of "},
		{"  claude: 2.1.300\n", "Line 14: cannot unmarshal !!str `2.1.300` into stack.RuntimePin.\n"},
		{"  claude:\n    version: [1]\n", "Line 15: cannot unmarshal !!seq into string.\n"},
	} {
		_, err := Load(write(t, withRuntimes(c.body)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q:\n%v\nwant it to contain:\n%s", c.body, err, c.want)
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
// with a checkout that pins one of them, claude and then codex: the checkout's pin of
// that runtime wins, from its file, and the base's pin of the other stays, from the
// base's file. When neither file pins a runtime, the result has no pins.
func TestExtendTakesTheNearerPinOfEachRuntime(t *testing.T) {
	const baseFile, checkoutFile = "/repo/qory-stack.yaml", "/app/qory.yaml"
	basePins := map[string]RuntimePin{"claude": {Version: "2.1.300", From: baseFile}, "codex": {Version: "0.167.2", From: baseFile}}
	nearer := map[string]RuntimePin{"claude": {Version: "2.1.320", From: checkoutFile}, "codex": {Version: "0.170.0", From: checkoutFile}}
	newBase := func() *Stack {
		return &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{}, Root: "/repo", File: baseFile}
	}
	for _, c := range []struct{ pinned, other string }{{"claude", "codex"}, {"codex", "claude"}} {
		base := newBase()
		base.RuntimePins = map[string]RuntimePin{"claude": basePins["claude"], "codex": basePins["codex"]}
		p := &Stack{Extends: Source{Path: "../repo"}, File: checkoutFile, RuntimePins: map[string]RuntimePin{c.pinned: nearer[c.pinned]}}
		out, err := Extend(base, p)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.RuntimePins) != 2 || out.RuntimePins[c.pinned] != nearer[c.pinned] || out.RuntimePins[c.other] != basePins[c.other] {
			t.Fatalf("the checkout pins %s: the pins are %+v, want %s %+v and %s %+v", c.pinned, out.RuntimePins, c.pinned, nearer[c.pinned], c.other, basePins[c.other])
		}
		if base.RuntimePins[c.pinned] != basePins[c.pinned] {
			t.Fatalf("Extend changed the base's pins: %+v", base.RuntimePins)
		}
	}
	if out, err := Extend(newBase(), &Stack{Extends: Source{Path: "../repo"}, File: checkoutFile}); err != nil || out.RuntimePins != nil {
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
		{"  claude:\n    version: \"2.1\"\n", true},
		{"  claude:\n    version: 2.1\n", false},
		{"  claude:\n    version: 2024-01-01\n", false},
		{"  claude:\n    version: 1.0.0+build.1\n", false},
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
