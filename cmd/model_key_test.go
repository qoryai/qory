package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
)

// modelKey is a model key in forager.yaml's form, indented to continue the gateway
// section: its value from MODEL_TOKEN, its API format and endpoint, the header it is set
// in, and the prefix its value must start with.
const modelKey = "  credentials:\n    model:\n      env: MODEL_TOKEN\n      format: anthropic-messages\n      endpoint: https://api.model.example/anthropic\n      auth: {scheme: bearer, oauth: true}\n      key_prefixes: [model-token-]\n"

// TestGatewayHandsForagerTheModelKeyAndTheLimits is qory gateway handing the gateway it
// starts a model key of forager.yaml, with its format, endpoint, auth and key prefixes,
// and gateway.lock_memory, gateway.run_limits and gateway.stack_hosts as the file sets
// them.
func TestGatewayHandsForagerTheModelKeyAndTheLimits(t *testing.T) {
	emptyDir(t)
	var got gateway.Config
	stopped := errors.New("the test stopped the gateway before it started")
	t.Cleanup(cmd.SetStartGateway(func(cfg gateway.Config) error {
		got = cfg
		return stopped
	}))
	for _, c := range []struct {
		name, yaml string
		lock       bool
		limits     gateway.RunLimits
		hosts      []string
	}{
		{"set", "  lock_memory: true\n  run_limits: {connections: 12, requests_per_second: 34, burst: 56}\n  stack_hosts: [git.example.com, \"*.example.org\", 192.0.2.10]\n", true, gateway.RunLimits{Connections: 12, RequestsPerSecond: 34, Burst: 56}, []string{"git.example.com", "*.example.org", "192.0.2.10"}},
		{"one limit", "  run_limits: {burst: 1}\n", false, gateway.RunLimits{Burst: 1}, nil},
		{"left out", "", false, gateway.RunLimits{}, nil},
	} {
		got = gateway.Config{}
		serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+modelKey+c.yaml)
		writeCertificate(t)
		out, err := run(t, "gateway")
		if err == nil || !strings.Contains(err.Error(), stopped.Error()) {
			t.Fatalf("%s: qory gateway did not reach the gateway's start: %v\n%s", c.name, err, out)
		}
		if got.LockMemory != c.lock || got.RunLimits != c.limits {
			t.Errorf("%s: lock_memory %v and run_limits %+v, want %v and %+v", c.name, got.LockMemory, got.RunLimits, c.lock, c.limits)
		}
		if !slices.Equal(got.StackHosts, c.hosts) {
			t.Errorf("%s: stack_hosts %q, want %q", c.name, got.StackHosts, c.hosts)
		}
		if len(got.Credentials) != 1 {
			t.Fatalf("%s: credentials %+v, want the model key", c.name, got.Credentials)
		}
		k := got.Credentials[0]
		if k.Name != "model" || k.Env != "MODEL_TOKEN" || k.Format != "anthropic-messages" || k.Endpoint != "https://api.model.example/anthropic" || k.Scheme != "bearer" || !k.OAuth || !slices.Equal(k.KeyPrefixes, []string{"model-token-"}) || k.Hosts != nil || k.Placeholders != nil {
			t.Errorf("%s: the model key %+v", c.name, k)
		}
	}
}

// TestGatewayStopsOnAModelKeyForagerRefuses is qory gateway stopping, before the gateway
// starts, on a model key that fails Forager's check, with an input error that is
// Forager's message after forager.yaml: a format that is none, an endpoint over http, a
// model key with hosts, and oauth beside the scheme header.
func TestGatewayStopsOnAModelKeyForagerRefuses(t *testing.T) {
	emptyDir(t)
	t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error { return errors.New("the gateway started") }))
	for _, c := range []struct{ entry, want string }{
		{"{env: MODEL_TOKEN, format: anthropic, endpoint: \"https://api.model.example\", auth: {scheme: bearer}}",
			`forager.yaml: credential model: set format to anthropic-messages, openai-responses, openai-chat or gemini: "anthropic" is no API format Forager knows`},
		{"{env: MODEL_TOKEN, format: anthropic-messages, endpoint: \"http://api.model.example\", auth: {scheme: bearer}}",
			"forager.yaml: credential model: set endpoint to an https URL with a host and no user, query or fragment: Forager reads no host from it"},
		{"{env: MODEL_TOKEN, format: anthropic-messages, endpoint: \"https://api.model.example\", hosts: [api.model.example], auth: {scheme: bearer}}",
			"forager.yaml: credential model: remove hosts, paths and placeholders from the model key: the key is set on its endpoint's host, and the session gives the runtime the key's variable"},
		{"{env: MODEL_TOKEN, format: anthropic-messages, endpoint: \"https://api.model.example\", auth: {scheme: header, header: x-api-key, oauth: true}}",
			"forager.yaml: credential model: remove oauth, or set the scheme to bearer: oauth marks a bearer alone"},
		{"{env: MODEL_TOKEN, endpoint: \"https://api.model.example\", hosts: [api.model.example], auth: {scheme: bearer}}",
			"forager.yaml: credential model: add format to the credential, or remove endpoint, oauth and key_prefixes: only a model key has them"},
		{"{env: MODEL_TOKEN, format: anthropic-messages, endpoint: \"https://api.model.example\", auth: {scheme: bearer}, key_prefixes: []}",
			"forager.yaml: credential model: list 1 to 4 key prefixes, or none: the model key lists 0"},
	} {
		serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+"  credentials:\n    model: "+c.entry+"\n")
		writeCertificate(t)
		out, err := run(t, "gateway")
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.entry, err, cmd.ExitCode(err), c.want, out)
		}
	}
}

// TestARunPassesTheModelKeysPrefixes is qory run with a model key whose value starts
// with none of its key_prefixes: the gateway's check of the key fails before the runtime
// starts, and no output contains the value.
func TestARunPassesTheModelKeysPrefixes(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, caRuntime(t))
	const value = "other-token-value-0123456789"
	t.Setenv("MODEL_TOKEN", value)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: observe\ncredentials:\n  - {name: model}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n"+modelKey)
	out, err := run(t, "run", "claude", "--policy", policy)
	text := out
	if err != nil {
		text += err.Error()
	}
	if err == nil || !strings.Contains(text, `give the model key model a token that starts with "model-token-", its key_prefixes: the token starts with none of them`) {
		t.Errorf("%v, want the key's refusal\n%s", err, out)
	}
	lacks(t, text, value, "hello from")
}

// TestARunPassesTheMemoryLock is qory run with gateway.lock_memory true on a system
// other than Linux: Forager's gateway stops before the runtime starts, in its words. On
// Linux the lock would apply to the test's process, so the test does not run there.
func TestARunPassesTheMemoryLock(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the lock would apply to the test's process")
	}
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  lock_memory: true\n")
	out, err := run(t, "run")
	want := "set gateway.lock_memory to false: Forager locks memory only on Linux, and this system is " + runtime.GOOS
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v, want %q\n%s", err, want, out)
	}
	lacks(t, out, "claude exited")
}

// TestARunHandsForagerTheLimits is qory run with no separate gateway handing the gateway
// it starts gateway.lock_memory, gateway.run_limits and gateway.stack_hosts as
// forager.yaml sets them, and zero values when the file leaves them out. The test stops
// the run where the gateway would start, so no memory is locked, on Linux either, and
// the runtime does not start.
func TestARunHandsForagerTheLimits(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	var got gateway.Config
	stopped := errors.New("the test stopped the gateway before it started")
	t.Cleanup(cmd.SetStartGateway(func(cfg gateway.Config) error {
		got = cfg
		return stopped
	}))
	for _, c := range []struct {
		name, yaml string
		lock       bool
		limits     gateway.RunLimits
		hosts      []string
	}{
		{"set", "  lock_memory: true\n  run_limits: {connections: 12, requests_per_second: 34, burst: 56}\n  stack_hosts: [git.example.com, \"*.example.org\"]\n", true, gateway.RunLimits{Connections: 12, RequestsPerSecond: 34, Burst: 56}, []string{"git.example.com", "*.example.org"}},
		{"one limit", "  run_limits: {requests_per_second: 7}\n", false, gateway.RunLimits{RequestsPerSecond: 7}, nil},
		{"left out, stack_hosts empty", "  egress: {mode: observe}\n  stack_hosts: []\n", false, gateway.RunLimits{}, nil},
	} {
		got = gateway.Config{}
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n"+c.yaml)
		out, err := run(t, "run")
		if err == nil || !strings.Contains(err.Error(), stopped.Error()) {
			t.Fatalf("%s: qory run did not reach the gateway's start: %v\n%s", c.name, err, out)
		}
		if got.LockMemory != c.lock || got.RunLimits != c.limits {
			t.Errorf("%s: lock_memory %v and run_limits %+v, want %v and %+v", c.name, got.LockMemory, got.RunLimits, c.lock, c.limits)
		}
		if !slices.Equal(got.StackHosts, c.hosts) {
			t.Errorf("%s: stack_hosts %q, want %q", c.name, got.StackHosts, c.hosts)
		}
		lacks(t, out, "claude exited")
	}
}
