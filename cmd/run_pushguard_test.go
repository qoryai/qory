package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pushGuardRuntime writes a program that stands in for a runtime: it sends the POST that
// ends a git push, the request to git-receive-pack, to git.example.com through the
// run's proxy with the curl at curl, and prints status=<code>. It prints whether
// HTTPS_PROXY is set, not its value.
func pushGuardRuntime(t *testing.T, curl string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "push-runtime")
	body := "#!/bin/sh\n" +
		"echo \"hello from $QORY_RUN_ID\"\n" +
		"echo \"proxy=${HTTPS_PROXY:+set}\"\n" +
		"status=$(" + shellQuote(curl) + " -sS --max-time 30 -o /dev/null -w '%{http_code}' -X POST" +
		" -H 'Content-Type: application/x-git-receive-pack-request' --data-binary ''" +
		" https://git.example.com/acme/site.git/git-receive-pack 2>/dev/null)\n" +
		"echo \"status=$status\"\n"
	writeFile(t, script, body)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestAPushThroughAForagerYamlCredentialIsForbidden is a run with no control plane whose
// forager.yaml credential for git.example.com has env, as machineCredentials builds it:
// a credential of that file has no push_ref. The POST of a git push through the run's
// proxy gets 403, and the run's events.jsonl records the rule wall:push-ref, the rule of
// Forager's branch guard.
func TestAPushThroughAForagerYamlCredentialIsForbidden(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed")
	}
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, pushGuardRuntime(t, curl))
	t.Setenv("GIT_TOKEN", "forge-token-value-0123456789")
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: forge}\n")
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  credentials:\n    forge:\n      env: GIT_TOKEN\n      hosts: [git.example.com]\n      auth: {scheme: bearer}\n")
	clearRuns(t, root)
	out, err := run(t, "run", "claude", "--wall", "none", "--policy", policy)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "proxy=set\n", "status=403\n")
	lacks(t, out, "forge-token-value-0123456789")
	ids := recorded(t, root)
	if len(ids) != 1 {
		t.Fatalf("%d runs recorded, want 1", len(ids))
	}
	record, err := os.ReadFile(filepath.Join(runsDir(t, root), ids[0], "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), "wall:push-ref") {
		t.Errorf("the run's events.jsonl has no request denied by the rule wall:push-ref")
	}
}
