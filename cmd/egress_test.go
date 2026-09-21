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
// inspect shows them, and qory run hands the hosts to the runner, which records them
// as harness_hosts beside the policy's own list and narrows nothing by them.
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
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.example.com\", other.example.org]\n")
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, evs := events(t, root)
	applied := evs["ai.qory.run.policy_applied"]
	if len(applied) != 1 {
		t.Fatalf("policy_applied %v", applied)
	}
	if got := list(applied[0]["harness_hosts"]); got != "*.github.com api.anthropic.com api.example.com registry.npmjs.org" {
		t.Errorf("harness_hosts %q", got)
	}
	if got := list(applied[0]["allow"]); got != "api.anthropic.com *.example.com other.example.org" {
		t.Errorf("allow %q, want the policy's own list", got)
	}
	if _, old := applied[0]["declared"]; old {
		t.Error("policy_applied still carries declared")
	}
}

// TestAnEmptyDeclarationStillHasTheRuntime is a harness whose one module declares an
// empty list: the harness declares, so the runtime's own host joins under the runtime's
// name and is reported as the harness's hosts, while the run reaches what the policy
// allows, as it does for a harness that declares nothing.
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
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "runner.yaml"), "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.example.com\"]\n")
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, evs := events(t, root)
	applied := evs["ai.qory.run.policy_applied"]
	if len(applied) != 1 || list(applied[0]["allow"]) != "api.anthropic.com *.example.com" || list(applied[0]["harness_hosts"]) != "api.anthropic.com" {
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
