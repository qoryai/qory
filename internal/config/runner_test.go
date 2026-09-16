package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// runnerFile writes the machine's runner file under the configuration directory.
func runnerFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", config.RunnerFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunnerFileReadsBothSections reads egress and webhook, lists them with the file as
// origin, and carries them on the configuration a checkout loads.
func TestRunnerFileReadsBothSections(t *testing.T) {
	hermetic(t)
	path := runnerFile(t, "apiVersion: qory.dev/v1alpha1\negress:\n  mode: enforce\n  allow: [api.anthropic.com, \"*.github.com\"]\nwebhook:\n  url: https://example.com/qory/events\n  secret: sixteen-characters-at-least\n  events: [ai.qory.run.exited]\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runner
	if r == nil || r.File != path || r.Egress == nil || r.Egress.Mode != "enforce" || strings.Join(r.Egress.Allow, " ") != "api.anthropic.com *.github.com" {
		t.Fatalf("egress read as %+v", r)
	}
	if r.Webhook == nil || r.Webhook.URL != "https://example.com/qory/events" || r.Webhook.Secret != "sixteen-characters-at-least" || r.Webhook.FromEnv || strings.Join(r.Webhook.Events, " ") != "ai.qory.run.exited" {
		t.Fatalf("webhook read as %+v", r.Webhook)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"runner.egress.mode": "enforce", "runner.egress.allow": "api.anthropic.com, *.github.com", "runner.webhook.url": "https://example.com/qory/events", "runner.webhook.secret": "(set)", "runner.webhook.events": "ai.qory.run.exited"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
}

// TestRunnerFileDefaults is no file, an empty file and a file with one section: what is
// absent is observe everything and files only, listed as defaults, and the secret may
// come from the environment.
func TestRunnerFileDefaults(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil || c.Runner != nil {
		t.Fatalf("no file: %+v, %v", c.Runner, err)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	if rows["runner.egress.mode"].Value != "observe" || rows["runner.egress.mode"].Origin != config.Default || rows["runner.webhook.url"].Value != "(none)" {
		t.Errorf("default rows %+v", rows)
	}
	runnerFile(t, "apiVersion: qory.dev/v1alpha1\n")
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Runner == nil || c.Runner.Egress != nil || c.Runner.Webhook != nil {
		t.Errorf("empty file: %+v, %v", c.Runner, err)
	}
	t.Setenv(config.EnvWebhookSecret, "from-the-environment")
	runnerFile(t, "webhook:\n  url: http://127.0.0.1:8787/events\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Runner.Webhook == nil || c.Runner.Webhook.Secret != "from-the-environment" || !c.Runner.Webhook.FromEnv || c.Runner.Egress != nil {
		t.Fatalf("secret from the environment: %+v, %v", c.Runner, err)
	}
	for _, row := range c.Rows() {
		if row.Key == "runner.webhook.secret" && row.Value != "(from "+config.EnvWebhookSecret+")" {
			t.Errorf("secret row %+v", row)
		}
	}
}

// TestRunnerFileRefusesAMistake is every refusal, each naming the file and the key.
func TestRunnerFileRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"egres: {mode: observe}\n", `key "egres" is not one runner.yaml reads`},
		{"egress: {allow: [a.example]}\n", "egress.mode is required"},
		{"egress: {mode: log}\n", `egress.mode "log" is not observe or enforce`},
		{"egress: {mode: enforce, allow: [\"api.example.com:443\"]}\n", `egress.allow: "api.example.com:443" is not a lower-case host name or a *. suffix`},
		{"webhook: {secret: sixteen-characters-at-least}\n", "webhook.url is required"},
		{"webhook: {url: \"ftp://x\", secret: sixteen-characters-at-least}\n", `webhook.url "ftp://x" is not an https URL`},
		{"webhook: {url: \"http://example.com/e\", secret: sixteen-characters-at-least}\n", "is http to a host that is not this machine"},
		{"webhook: {url: \"https://example.com/e\"}\n", "webhook.secret is missing; set it there or in QORY_WEBHOOK_SECRET"},
		{"webhook: {url: \"https://example.com/e\", secret: short}\n", "webhook.secret is shorter than 16 characters"},
		{"webhook: {url: \"https://example.com/e\", secret: sixteen-characters-at-least, events: [\"\"]}\n", "webhook.events names an empty type"},
		{"apiVersion: qory.dev/v9\n", "qory.dev/v9"},
	} {
		path := runnerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}
