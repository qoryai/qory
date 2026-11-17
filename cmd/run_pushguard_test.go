package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pushGuardRuntime writes a program that stands in for a runtime: it sends the POST that
// ends a git push, the request to git-receive-pack, to git.example.com through the
// run's proxy with the curl at curl, and prints the proxy's reply and then
// status=<code>. It prints whether HTTPS_PROXY is set, not its value.
func pushGuardRuntime(t *testing.T, curl string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "push-runtime")
	body := "#!/bin/sh\n" +
		"echo \"hello from $QORY_RUN_ID\"\n" +
		"echo \"proxy=${HTTPS_PROXY:+set}\"\n" +
		"reply=$(" + shellQuote(curl) + " -sS --max-time 30 -w '\\nstatus=%{http_code}' -X POST" +
		" -H 'Content-Type: application/x-git-receive-pack-request' --data-binary ''" +
		" https://git.example.com/acme/site.git/git-receive-pack 2>/dev/null)\n" +
		"echo \"$reply\"\n"
	writeFile(t, script, body)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestAPushThroughAForagerYamlCredentialIsForbidden is a run with no control plane whose
// forager.yaml credential for git.example.com has env, as machineCredentials builds it:
// a credential of that file has no push_ref. The POST of a git push through the run's
// proxy gets a 403 from Forager's branch guard, and the request does not reach the host.
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
	wants(t, out, "proxy=set\n", "The run's credential for git.example.com allows no push, so the push was not sent.\n", "status=403\n")
	lacks(t, out, "forge-token-value-0123456789")
}
