package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestForagerReadsAModelKeyAndTheGatewayLimits reads a model key of
// gateway.credentials, with format, endpoint, auth with oauth, and key_prefixes, and
// gateway.lock_memory and gateway.run_limits, and lists the two with the file as
// origin. An empty key_prefixes is read as an empty list, which fails Forager's check.
func TestForagerReadsAModelKeyAndTheGatewayLimits(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, `gateway:
  lock_memory: true
  run_limits:
    connections: 12
    requests_per_second: 34
    burst: 56
  credentials:
    model:
      env: MODEL_API_KEY
      format: anthropic-messages
      endpoint: https://api.model.example/anthropic
      auth: {scheme: bearer, oauth: true}
      key_prefixes: [sk-ant-, sk-other-]
    plain:
      file: /run/keys/model
      format: openai-chat
      endpoint: https://models.example.com/v1
      auth: {scheme: header, header: api-key}
      key_prefixes: []
`)
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if !r.LockMemory || r.RunLimits != (config.ForagerRunLimits{Connections: 12, RequestsPerSecond: 34, Burst: 56}) {
		t.Errorf("lock_memory %v, run_limits %+v", r.LockMemory, r.RunLimits)
	}
	if len(r.Credentials) != 2 {
		t.Fatalf("credentials %+v", r.Credentials)
	}
	k := r.Credentials[0]
	if k.Name != "model" || k.Env != "MODEL_API_KEY" || k.Format != "anthropic-messages" || k.Endpoint != "https://api.model.example/anthropic" || k.Scheme != "bearer" || !k.OAuth || !slices.Equal(k.KeyPrefixes, []string{"sk-ant-", "sk-other-"}) {
		t.Errorf("the model key read as %+v", k)
	}
	p := r.Credentials[1]
	if p.File != "/run/keys/model" || p.Format != "openai-chat" || p.Endpoint != "https://models.example.com/v1" || p.Scheme != "header" || p.Header != "api-key" || p.OAuth || p.KeyPrefixes == nil || len(p.KeyPrefixes) != 0 {
		t.Errorf("the second model key read as %+v", p)
	}
	rows := map[string]config.Row{}
	for _, row := range r.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{"gateway.lock_memory": "true", "gateway.run_limits.connections": "12", "gateway.run_limits.requests_per_second": "34", "gateway.run_limits.burst": "56"} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
	foragerFile(t, "gateway:\n  run_limits: {burst: 1}\n")
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	if r.LockMemory || r.RunLimits != (config.ForagerRunLimits{Burst: 1}) {
		t.Errorf("one limit: lock_memory %v, run_limits %+v", r.LockMemory, r.RunLimits)
	}
	rows = map[string]config.Row{}
	for _, row := range r.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]config.Row{
		"gateway.lock_memory":                    {Key: "gateway.lock_memory", Value: "false", Origin: config.Default},
		"gateway.run_limits.connections":         {Key: "gateway.run_limits.connections", Value: "256", Origin: config.Default},
		"gateway.run_limits.requests_per_second": {Key: "gateway.run_limits.requests_per_second", Value: "50", Origin: config.Default},
		"gateway.run_limits.burst":               {Key: "gateway.run_limits.burst", Value: "1", Origin: path},
	} {
		if rows[key] != want {
			t.Errorf("%s: %+v, want %+v", key, rows[key], want)
		}
	}
}

// TestForagerRefusesARunLimitOutsideItsRange is each field of gateway.run_limits below 1
// or above its maximum, which fails to load with an error that names its range and
// default, and never the value.
func TestForagerRefusesARunLimitOutsideItsRange(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"connections: 0", "set gateway.run_limits.connections to a number from 1 to 65536, or remove it for the default of 256"},
		{"connections: 65537", "set gateway.run_limits.connections to a number from 1 to 65536, or remove it for the default of 256"},
		{"requests_per_second: -3", "set gateway.run_limits.requests_per_second to a number from 1 to 10000, or remove it for the default of 50"},
		{"requests_per_second: 10001", "set gateway.run_limits.requests_per_second to a number from 1 to 10000, or remove it for the default of 50"},
		{"burst: 0", "set gateway.run_limits.burst to a number from 1 to 100000, or remove it for the default of 200"},
		{"burst: 100001", "set gateway.run_limits.burst to a number from 1 to 100000, or remove it for the default of 200"},
	} {
		path := foragerFile(t, "gateway:\n  run_limits: {"+c.body+"}\n")
		_, err := config.LoadForager()
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.body, err, want)
		}
	}
	for _, edge := range []string{"connections: 65536", "requests_per_second: 10000", "burst: 100000", "connections: 1"} {
		foragerFile(t, "gateway:\n  run_limits: {"+edge+"}\n")
		if _, err := config.LoadForager(); err != nil {
			t.Errorf("%s: %v", edge, err)
		}
	}
	path := foragerFile(t, "gateway:\n  run_limits: {connection: 3}\n")
	if _, err := config.LoadForager(); err == nil || !strings.HasPrefix(err.Error(), path+": ") || !strings.Contains(err.Error(), "connection") {
		t.Errorf("a key that is not one: %v", err)
	}
}

// TestTheForagerSchemaTakesAModelKeyAndTheGatewayLimits holds forager.schema.json to
// what the reader takes of a model key, gateway.lock_memory and gateway.run_limits.
func TestTheForagerSchemaTakesAModelKeyAndTheGatewayLimits(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "{env: MODEL_API_KEY, format: anthropic-messages, endpoint: \"https://api.model.example/anthropic\", auth: {scheme: bearer, oauth: true}, key_prefixes: [sk-ant-]}"
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {credentials: {model: " + key + "}}\n", true},
		{"gateway: {credentials: {model: {file: /run/keys/model, format: gemini, endpoint: \"https://models.example.com:8443\", auth: {scheme: header, header: x-goog-api-key}}}}\n", true},
		{"gateway: {credentials: {model: {env: K, format: anthropic, endpoint: \"https://api.model.example\", auth: {scheme: bearer}}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"http://api.model.example\", auth: {scheme: bearer}}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://api.model.example/v1?x=1\", auth: {scheme: bearer}}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://u@api.model.example\", auth: {scheme: bearer}}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://api.model.example\", auth: {scheme: bearer}, key_prefixes: []}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://api.model.example\", auth: {scheme: bearer}, key_prefixes: [a, b, c, d, e]}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://api.model.example\", auth: {scheme: bearer}, key_prefixes: [\"sk ant\"]}}}\n", false},
		{"gateway: {credentials: {model: {env: K, format: openai-chat, endpoint: \"https://api.model.example\", auth: {scheme: bearer}, key_prefixes: [a, a]}}}\n", false},
		{"gateway: {lock_memory: true, run_limits: {connections: 65536, requests_per_second: 10000, burst: 100000}}\n", true},
		{"gateway: {lock_memory: false, run_limits: {}}\n", true},
		{"gateway: {lock_memory: \"yes\"}\n", false},
		{"gateway: {run_limits: {connections: 0}}\n", false},
		{"gateway: {run_limits: {requests_per_second: 10001}}\n", false},
		{"gateway: {run_limits: {burst: 1.5}}\n", false},
		{"gateway: {run_limits: {connection: 3}}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}

// gatewayBlock is the lines of the section key under gateway in a forager.yaml block of
// the docs: "  key:" and every line below it indented further. Empty when the block has
// none.
func gatewayBlock(block, key string) string {
	var section []string
	gateway := false
	for _, line := range strings.SplitAfter(block, "\n") {
		if len(section) > 0 && !strings.HasPrefix(line, "   ") {
			break
		}
		switch {
		case !strings.HasPrefix(line, " "):
			gateway = strings.HasPrefix(line, "gateway:")
		case gateway && (strings.HasPrefix(line, "  "+key+":") || len(section) > 0):
			section = append(section, line)
		}
	}
	return strings.Join(section, "")
}

// TestTheDocsModelKeysAndRunLimitsRead is every forager.yaml of docs/run.md and
// docs/gateway.md: its gateway.credentials reads, each credential passes Forager's
// check of a definition, and none but a model key sets a variable Claude Code reads its
// key from; at least one is a model key. Its gateway.run_limits reads, with
// the defaults the docs give: 256, 50 and 200.
func TestTheDocsModelKeysAndRunLimitsRead(t *testing.T) {
	hermetic(t)
	var keys, limits int
	for _, name := range []string{"run.md", "gateway.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		blocks := strings.Split(string(data), "```yaml\n")[1:]
		for _, block := range blocks {
			block, _, _ = strings.Cut(block, "```")
			if credentials := gatewayBlock(block, "credentials"); credentials != "" {
				foragerFile(t, "gateway:\n"+credentials)
				r, err := config.LoadForager()
				if err != nil {
					t.Errorf("%s: %v\n%s", name, err, credentials)
					continue
				}
				for _, c := range r.Credentials {
					def := gateway.Credential{Name: c.Name, Env: c.Env, File: c.File, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts, Scheme: c.Scheme, Username: c.Username, Header: c.Header, Paths: c.Paths, Placeholders: c.Placeholders,
						Format: c.Format, Endpoint: c.Endpoint, OAuth: c.OAuth, KeyPrefixes: c.KeyPrefixes}
					if err := def.Check(); err != nil {
						t.Errorf("%s: %s: %v", name, c.Name, err)
					}
					if c.Format != "" {
						keys++
					}
					// The variables Claude Code reads a model key from: only a model key sets
					// one, and Forager stops a run whose other credential lists one.
					for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
						if slices.Contains(c.Placeholders, v) {
							t.Errorf("%s: %s lists %s among its placeholders", name, c.Name, v)
						}
					}
				}
			}
			if section := gatewayBlock(block, "run_limits"); section != "" {
				foragerFile(t, "gateway:\n"+section)
				r, err := config.LoadForager()
				if err != nil || r.RunLimits != (config.ForagerRunLimits{Connections: 256, RequestsPerSecond: 50, Burst: 200}) {
					t.Errorf("%s: run_limits %+v, %v\n%s", name, r, err, section)
				}
				limits++
			}
		}
	}
	if keys == 0 || limits == 0 {
		t.Errorf("the docs show %d model keys and %d run_limits", keys, limits)
	}
}

// TestTheForagerSchemaTakesTheEndpointsForagerTakes is forager.schema.json taking a
// model key's endpoint exactly when Forager's check of the definition does, for ports
// from 1 to 65535, an empty port, an IPv6 literal, and the ports Forager does not take:
// 0, above 65535, and one that is not a number.
func TestTheForagerSchemaTakesTheEndpointsForagerTakes(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		endpoint string
		valid    bool
	}{
		{"https://api.model.example", true},
		{"https://api.model.example:1/v1", true},
		{"https://api.model.example:443", true},
		{"https://api.model.example:0443/v1", true},
		{"https://api.model.example:65535", true},
		{"https://api.model.example:/v1", true},
		{"https://[2001:db8::1]:8443/v1", true},
		{"https://api.model.example:0", false},
		{"https://api.model.example:000/v1", false},
		{"https://api.model.example:65536", false},
		{"https://api.model.example:99999/v1", false},
		{"https://api.model.example:84x3", false},
		{"https://api.model.example:443:443", false},
	} {
		forager := gateway.Credential{Name: "model", Env: "K", Format: "openai-chat", Endpoint: c.endpoint, Scheme: "bearer"}.Check()
		if (forager == nil) != c.valid {
			t.Errorf("%s: Forager's check %v, want valid %v", c.endpoint, forager, c.valid)
		}
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "gateway": map[string]any{"credentials": map[string]any{"model": map[string]any{
			"env": "K", "format": "openai-chat", "endpoint": c.endpoint, "auth": map[string]any{"scheme": "bearer"},
		}}}}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: the schema %v, want valid %v", c.endpoint, err, c.valid)
		}
	}
}
