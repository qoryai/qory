package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
)

// selectsModel defines model, a credential that takes no argument, whose values no
// refusal may print, indented to continue the gateway section.
const selectsModel = "  credentials:\n    model:\n      env: MODEL_TOKEN_VALUE\n      hosts: [api.model.example]\n      auth: {scheme: bearer}\n"

// selectsGitHub declares the github integration, whose credential takes an argument,
// indented to continue the gateway section.
const selectsGitHub = "  integrations:\n    github: {}\n"

// githubIntegration puts qory-github on the PATH: a program that describes the github
// integration, whose credential takes a repository as its argument.
func githubIntegration(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, filepath.Join(bin, "qory-github"), "#!/bin/sh\ntest \"$1\" = describe || exit 64\necho '{\"version\": 1, \"name\": \"github\", \"title\": \"GitHub\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Za-z0-9-]+/[A-Za-z0-9_.-]+\", \"hosts\": [\"api.github.com\"]}}}'\n")
	if err := os.Chmod(filepath.Join(bin, "qory-github"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// selectsFile writes forager.yaml for qory gateway: the gateway's server, address and
// TLS, model, the github integration, and gateway.selects with entries, each a flow
// mapping.
func selectsFile(t *testing.T, entries ...string) {
	t.Helper()
	selects := "  selects:\n    credentials:\n"
	for _, e := range entries {
		selects += "      - " + e + "\n"
	}
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+selectsModel+selectsGitHub+selects)
	writeCertificate(t)
}

// TestRunRefusesGatewaySelects is gateway.selects in forager.yaml, with entries or
// without, refused by qory run as input, with --local too, before anything starts: a
// run qory run starts takes its credentials from its policy.
func TestRunRefusesGatewaySelects(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error { return errors.New("the gateway started") }))
	const want = "forager.yaml: gateway.selects is read by qory gateway alone; a run qory run starts takes its credentials from its policy: remove gateway.selects"
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	for _, c := range []struct {
		selects string
		args    []string
	}{
		{"  selects:\n    credentials:\n      - {name: model}\n", []string{"run"}},
		{"  selects:\n    credentials:\n      - {name: model}\n", []string{"run", "--local"}},
		{"  selects: {}\n", []string{"run"}},
	} {
		writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsModel+c.selects)
		out, err := run(t, c.args...)
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v with %q: %v (exit %d), want %q\n%s", c.args, c.selects, err, cmd.ExitCode(err), want, out)
		}
		lacks(t, out, "MODEL_TOKEN_VALUE", "claude exited")
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}
}

// TestGatewayHandsForagerTheSelects is qory gateway handing the gateway it starts
// gateway.selects: github, a credential the github integration defines, by the run's
// repository label on github.com with the pattern of one repository there, and model, a
// credential gateway.credentials defines, by its name alone.
func TestGatewayHandsForagerTheSelects(t *testing.T) {
	emptyDir(t)
	githubIntegration(t)
	selectsFile(t, "{name: github, argument_label: repository, forge: github.com}", "{name: model}")
	var got []gateway.Selection
	var credentials []string
	stopped := errors.New("the test stopped the gateway before it started")
	t.Cleanup(cmd.SetStartGateway(func(cfg gateway.Config) error {
		got = cfg.Selects
		for _, c := range cfg.Credentials {
			credentials = append(credentials, c.Name)
		}
		return stopped
	}))
	out, err := run(t, "gateway")
	if err == nil || !strings.Contains(err.Error(), stopped.Error()) {
		t.Fatalf("qory gateway did not reach the gateway's start: %v\n%s", err, out)
	}
	want := []gateway.Selection{{Name: "github", Label: "repository", Forge: "github.com", Pattern: config.GitHubRepository}, {Name: "model"}}
	if !slices.Equal(got, want) {
		t.Errorf("the gateway's selection %+v, want %+v", got, want)
	}
	if !slices.Equal(credentials, []string{"model", "github"}) {
		t.Errorf("the gateway's credentials %v, want model and github", credentials)
	}
}

// TestGatewayRefusesSelectsItCannotHand is each refusal of gateway.selects by qory
// gateway, once the integrations are described and before the gateway starts, as input:
// each names the entry's name, and the forge alone of its values. A name given twice is
// Forager's gateway's to refuse, in its words.
func TestGatewayRefusesSelectsItCannotHand(t *testing.T) {
	emptyDir(t)
	githubIntegration(t)
	t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error { return errors.New("the gateway started") }))
	for _, c := range []struct{ entry, want string }{
		{"{name: tracker}", "forager.yaml: gateway.selects.credentials: tracker is no credential that gateway.credentials or gateway.integrations defines"},
		{"{name: github, argument_label: secret-label-value}", "forager.yaml: gateway.selects.credentials: github needs argument_label and forge"},
		{"{name: model, argument_label: secret-label-value, forge: github.com}", "forager.yaml: gateway.selects.credentials: model takes no argument, so argument_label and forge do not apply"},
		{"{name: github, argument_label: secret-label-value, forge: gitlab.example}", "forager.yaml: gateway.selects.credentials: github: forge gitlab.example is not one qory knows the repository form of; use github.com"},
	} {
		selectsFile(t, "{name: model}", c.entry)
		out, err := run(t, "gateway")
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.entry, err, cmd.ExitCode(err), c.want, out)
		}
		text := out
		if err != nil {
			text += err.Error()
		}
		lacks(t, text, "secret-label-value", "MODEL_TOKEN_VALUE", "api.model.example", "listening on")
	}
}

// TestGatewayStopsOnANameSelectedTwice is gateway.selects naming one credential twice:
// qory passes it on, and Forager's gateway refuses it at its start, in its words.
func TestGatewayStopsOnANameSelectedTwice(t *testing.T) {
	emptyDir(t)
	githubIntegration(t)
	selectsFile(t, "{name: model}", "{name: model}")
	out, err := run(t, "gateway")
	if want := "gateway: Selects names model twice"; err == nil || err.Error() != want {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if code := cmd.ExitCode(err); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	lacks(t, out, "listening on")
}

// TestConfigListsTheSelects is qory config listing each entry of gateway.selects by
// the credential's name: its label and its forge, or that it takes no argument.
func TestConfigListsTheSelects(t *testing.T) {
	emptyDir(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsModel+"    github:\n      adapter: [/opt/adapters/github, \"${argument}\"]\n      argument: \"[a-z]+/[a-z]+\"\n      hosts: [api.github.com]\n  selects:\n    credentials:\n      - {name: github, argument_label: repository, forge: github.com}\n      - {name: model}\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rows := configRows(out)
	file := "~/.config/qory/forager.yaml"
	wantsConfigRow(t, rows, "gateway.selects.credentials.github", "argument_label repository, forge github.com", file)
	wantsConfigRow(t, rows, "gateway.selects.credentials.model", "(no argument)", file)
	if t.Failed() {
		t.Log(out)
	}
}
