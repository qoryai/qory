package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"
)

// caRuntime writes a program that stands in for a runtime and prints, one line each,
// NAME=value for each variable that points a program at authorities, Forager's defaults
// and MY_TOOLS_CA, ANTHROPIC_API_KEY=value for the model key's placeholder,
// ANTHROPIC_BASE_URL=value for its endpoint, and MODEL_TOKEN=value for the variable the
// key's value is read from.
func caRuntime(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "ca-runtime")
	body := "#!/bin/sh\necho \"hello from $QORY_RUN_ID\"\n"
	for _, n := range slices.Concat(wall.DefaultCAEnv, []string{"MY_TOOLS_CA", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "MODEL_TOKEN"}) {
		body += "echo \"" + n + "=${" + n + "-unset}\"\n"
	}
	writeFile(t, script, body)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestAnUnwalledRunWithCredentialsGetsTheCAVariables is a run without a wall whose policy
// selects a credential: it starts, and each variable of wall.ca_env, or of Forager's
// defaults without one, points at the bundle in the run's directory, a file with a
// certificate in it. A name the list leaves out stays unset. The credential is a model key
// in forager.yaml's form, with format, endpoint and auth: the agent gets its placeholder
// in ANTHROPIC_API_KEY, the variable Claude Code reads a key set in the header x-api-key
// from, and its endpoint in ANTHROPIC_BASE_URL, never its value: MODEL_TOKEN is unset.
func TestAnUnwalledRunWithCredentialsGetsTheCAVariables(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, caRuntime(t))
	for _, n := range slices.Concat(wall.DefaultCAEnv, []string{"MY_TOOLS_CA"}) {
		if v, ok := os.LookupEnv(n); ok {
			t.Cleanup(func() { _ = os.Setenv(n, v) })
			_ = os.Unsetenv(n)
		}
	}
	const value = "model-token-value-0123456789"
	t.Setenv("MODEL_TOKEN", value)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	credentials := "gateway:\n  credentials:\n    model:\n      env: MODEL_TOKEN\n      format: anthropic-messages\n      endpoint: https://api.anthropic.com\n      auth: {scheme: header, header: x-api-key}\n      key_prefixes: [model-token-]\n"
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: model}\n")
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  ca_env: [MY_TOOLS_CA, SSL_CERT_FILE]\n"
	for _, c := range []struct {
		name, yaml string
		args       []string
		set        []string
	}{
		{"no wall section", credentials, []string{"run", "claude", "--policy", policy}, wall.DefaultCAEnv},
		{"wall.ca_env, --wall none", wallSection + credentials, []string{"run", "claude", "--wall", "none", "--policy", policy}, []string{"MY_TOOLS_CA", "SSL_CERT_FILE"}},
	} {
		writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+c.yaml)
		clearRuns(t, root)
		out, err := run(t, c.args...)
		if err != nil || !strings.Contains(out, "hello from") {
			t.Errorf("%s: %v\n%s", c.name, err, out)
			continue
		}
		ids := recorded(t, root)
		if len(ids) != 1 {
			t.Errorf("%s: %d runs recorded, want 1", c.name, len(ids))
			continue
		}
		bundle := filepath.Join(runsDir(t, root), ids[0], "ca-bundle.pem")
		for _, n := range slices.Concat(wall.DefaultCAEnv, []string{"MY_TOOLS_CA"}) {
			want := n + "=unset"
			if slices.Contains(c.set, n) {
				want = n + "=" + bundle
			}
			wants(t, out, want+"\n")
		}
		if b, err := os.ReadFile(bundle); err != nil || !strings.Contains(string(b), "BEGIN CERTIFICATE") {
			t.Errorf("%s: the bundle %s: %v", c.name, bundle, err)
		}
		wants(t, out, "MODEL_TOKEN=unset\n", "ANTHROPIC_API_KEY=qory-model-sets-the-credential-outside-the-enclosure\n", "ANTHROPIC_BASE_URL=https://api.anthropic.com\n")
		lacks(t, out, value)
	}
}
