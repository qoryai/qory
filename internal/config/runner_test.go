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

// TestRunnerFileReadsTheWall reads the wall section, lists it, and lists no wall as the
// default when the file names none.
func TestRunnerFileReadsTheWall(t *testing.T) {
	hermetic(t)
	path := runnerFile(t, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: podman\n  helper: /opt/qory/qory-linux\n  env: [ANTHROPIC_API_KEY, GH_TOKEN]\n  user: \"1000:1000\"\n  mounts: [/srv/odoo:ro, /srv/cache]\n  cpus: \"3.5\"\n  memory: 14g\n  pids_limit: 4096\n  shm_size: 2g\n  ca_env: [SSL_CERT_FILE, MY_TOOLS_CA]\nrun:\n  timeout: 5h30m\n  stop_signal: SIGINT\n  stop_grace: 30s\ncredentials:\n  product:\n    adapter: [/opt/adapters/git-host, --repo, \"${argument}\"]\n    argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n    hosts: [\"*.example.com\"]\n    placeholders: [GIT_HOST_TOKEN]\n  model:\n    env: MODEL_TOKEN\n    hosts: [api.model.example]\n    auth: {scheme: header, header: X-Api-Key}\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Runner.Wall
	if w == nil || w.Adapter != config.WallDocker || w.Image != "example.com/agent:1" || w.Command != "podman" || w.Helper != "/opt/qory/qory-linux" || w.User != "1000:1000" || strings.Join(w.Env, " ") != "ANTHROPIC_API_KEY GH_TOKEN" {
		t.Fatalf("wall read as %+v", w)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"runner.wall.adapter": "docker", "runner.wall.image": "example.com/agent:1", "runner.wall.env": "ANTHROPIC_API_KEY, GH_TOKEN", "runner.wall.command": "podman", "runner.wall.helper": "/opt/qory/qory-linux",
		"runner.wall.mounts": "/srv/odoo:ro, /srv/cache", "runner.wall.cpus": "3.5", "runner.wall.memory": "14g", "runner.wall.pids_limit": "4096", "runner.wall.shm_size": "2g",
		"runner.run.timeout": "5h30m0s", "runner.run.stop_grace": "30s", "runner.run.stop_signal": "SIGINT", "runner.wall.ca_env": "SSL_CERT_FILE, MY_TOOLS_CA",
		"runner.credentials.product": "adapter /opt/adapters/git-host", "runner.credentials.model": "env MODEL_TOKEN"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
	runnerFile(t, "egress: {mode: observe}\n")
	c, err = config.Load(t.TempDir(), true)
	if err != nil || c.Runner.Wall != nil {
		t.Fatalf("no wall section: %+v, %v", c.Runner, err)
	}
	for _, row := range c.Rows() {
		if row.Key == "runner.wall.adapter" && (row.Value != "(none)" || row.Origin != config.Default) {
			t.Errorf("no wall listed as %+v", row)
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
		{"wall: {image: i}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: bubblewrap}\n", "wall.adapter is required, and docker is the one there is"},
		{"wall: {adapter: docker, helper: qory-linux}\n", `wall.helper "qory-linux" is not an absolute path`},
		{"wall: {adapter: docker, env: [\"KEY=value\"]}\n", `wall.env: "KEY=value" is not a variable's name`},
		{"wall: {adapter: docker, network: host}\n", `key "network" is not one`},
		{"wall: {adapter: docker, mounts: [srv]}\n", `wall.mounts: the mount "srv" is not an absolute path`},
		{"wall: {adapter: docker, pids_limit: 0}\n", `wall.pids_limit is 0`},
		{"wall: {adapter: docker, env: [QORY_WEBHOOK_SECRET]}\n", `the runner's own`},
		{"credentials: {product: {adapter: [git-host]}}\n", `credentials.product.adapter is a program by its absolute path`},
		{"credentials: {product: {command: [/x]}}\n", `credentials.product: key "command" is not one`},
		{"credentials: [product]\n", `credentials is a mapping`},
		{"run: {timeout: soon}\n", `run.timeout "soon" is not a duration above zero`},
		{"run: {stop_grace: 0s}\n", `run.stop_grace "0s" is not a duration above zero`},
		{"run: {stop_signal: SIGKILL}\n", `run.stop_signal: the stop signal "SIGKILL" is not one of`},
		{"apiVersion: qory.dev/v9\n", "qory.dev/v9"},
	} {
		path := runnerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}
