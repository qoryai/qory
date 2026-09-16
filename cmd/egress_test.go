package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/report"
)

// TestDeclaredEgressReachesTheRun is decision 0053 end to end: the modules' declarations
// are unioned in the report with the runtime's own host under the runtime's name, the
// inspect shows them, and qory run hands the hosts to the runner, which keeps the ones
// the policy covers and records both lists.
func TestDeclaredEgressReachesTheRun(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "egress-declared", root)
	script := fakeRuntime(t)
	composedForFake(t, root, script)
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(rep.Hosts(), " "), "*.github.com api.anthropic.com api.example.com registry.npmjs.org"; got != want {
		t.Errorf("hosts %q, want %q", got, want)
	}
	for _, e := range rep.Egress {
		if e.Host == "api.anthropic.com" && strings.Join(e.Modules, " ") != "claude" {
			t.Errorf("api.anthropic.com is declared by %v", e.Modules)
		}
	}
	out, err := run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "Egress", "api.example.com", "core, web", "api.anthropic.com", "claude")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "policy.yaml"), "version: 1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.example.com\", other.example.org]\n")
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, evs := events(t, root)
	applied := evs["ai.qory.run.policy_applied"]
	if len(applied) != 1 {
		t.Fatalf("policy_applied %v", applied)
	}
	if got := list(applied[0]["declared"]); got != "*.github.com api.anthropic.com api.example.com registry.npmjs.org" {
		t.Errorf("declared %q", got)
	}
	if got := list(applied[0]["allow"]); got != "api.anthropic.com api.example.com" {
		t.Errorf("effective allow %q", got)
	}
}

// TestAnEmptyDeclarationStillHasTheRuntime is a harness whose one module declares an
// empty list: the harness declares, so the runtime's own host joins under the runtime's
// name and is all the run reaches, unlike a harness that declares nothing, whose run
// keeps the policy's list, and unlike a runtime with no host of its own, which then
// reaches nothing.
func TestAnEmptyDeclarationStillHasTheRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "egress-nothing", root)
	composedForFake(t, root, fakeRuntime(t))
	data, err := os.ReadFile(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), `"egress": [`, `"host": "api.anthropic.com"`, `"modules": [
        "claude"
      ]`)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "policy.yaml"), "version: 1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.example.com\"]\n")
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, evs := events(t, root)
	applied := evs["ai.qory.run.policy_applied"]
	if len(applied) != 1 || list(applied[0]["allow"]) != "api.anthropic.com" || list(applied[0]["declared"]) != "api.anthropic.com" {
		t.Errorf("policy_applied %v", applied)
	}
}

// list joins a JSON array of strings with spaces.
func list(v any) string {
	items, _ := v.([]any)
	var words []string
	for _, item := range items {
		words = append(words, item.(string))
	}
	return strings.Join(words, " ")
}
