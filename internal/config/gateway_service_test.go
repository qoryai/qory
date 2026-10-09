package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// issuerSection is one issuer of gateway.run_credentials, in flow style.
const issuerSection = `{issuer: "https://issuer.example", audience: qory-gateway, algorithms: [RS256], keys: [{kid: k1, alg: RS256, public_key_file: issuer-k1.pem}], labels: {forge: {value: example-forge}, repository: {claims: [namespace, project], join: /}, run_key: {claim: sub}}, introspection: {url: "https://issuer.example/introspect", client_id: example-gateway, client_secret_file: issuer-secret, cache: 30s}}`

// TestTheForagerSchemaTakesTheGatewayService holds forager.schema.json to what the
// reader takes of gateway.listen, gateway.tls and gateway.run_credentials, Forager's
// run-credentials.schema.json: an HMAC algorithm, an issuer without an audience, a
// run_key that is not sub and a TLS without its key fail it.
func TestTheForagerSchemaTakesTheGatewayService(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {listen: \"0.0.0.0:8443\", tls: {certificate: gateway.pem, key: gateway-key.pem}, run_credentials: [" + issuerSection + "]}\n", true},
		{"gateway: {listen: \"127.0.0.1:8443\"}\n", true},
		{"gateway: {listen: \"[::1]:8443\"}\n", true},
		{"gateway: {listen: gateway.example}\n", false},
		{"gateway: {tls: {certificate: gateway.pem}}\n", false},
		{"gateway: {tls: {certificate: gateway.pem, key: gateway-key.pem, ca: ca.pem}}\n", false},
		{"gateway: {run_credentials: []}\n", false},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "[RS256]", "[HS256]", 1) + "]}\n", false},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "audience: qory-gateway, ", "", 1) + "]}\n", false},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "{claim: sub}", "{claim: jti}", 1) + "]}\n", false},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "client_secret_file: issuer-secret, ", "client_secret: x, ", 1) + "]}\n", false},
		{"session: {listen: \"0.0.0.0:8443\"}\n", false},
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

// TestTheGatewayServiceReads is gateway.listen, gateway.tls and gateway.run_credentials
// read: each as written, a relative path resolved under forager.yaml's directory, and
// what Forager's schema or the reader refuses an error that names the file and the key.
func TestTheGatewayServiceReads(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 0.0.0.0:8443\n  tls: {certificate: gateway.pem, key: /etc/qory/gateway-key.pem}\n  run_credentials: ["+issuerSection+"]\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Gateway || r.Listen != "0.0.0.0:8443" || r.TLS == nil || r.TLS.Certificate != "gateway.pem" || r.TLS.Key != "/etc/qory/gateway-key.pem" {
		t.Errorf("read %+v", r)
	}
	if got, want := r.Path(r.TLS.Certificate), filepath.Join(filepath.Dir(path), "gateway.pem"); got != want {
		t.Errorf("the certificate resolves to %s, want %s", got, want)
	}
	if got := r.Path(r.TLS.Key); got != "/etc/qory/gateway-key.pem" {
		t.Errorf("the key resolves to %s, want it as written", got)
	}
	if len(r.RunCredentials) != 1 || r.RunCredentials[0].Issuer != "https://issuer.example" || r.RunCredentials[0].Introspection == nil || r.RunCredentials[0].Introspection.ClientSecretFile != "issuer-secret" {
		t.Errorf("run credentials %+v", r.RunCredentials)
	}
	for _, c := range []struct{ body, want string }{
		{"gateway: {listen: gateway.example}\n", `gateway.listen "gateway.example" is not host:port, such as 0.0.0.0:8443`},
		{"gateway: {listen: \"0.0.0.0:99999\"}\n", `gateway.listen "0.0.0.0:99999" is not host:port, such as 0.0.0.0:8443`},
		{"gateway: {tls: {certificate: gateway.pem}}\n", "gateway.tls needs both certificate and key"},
		{"gateway: {run_credentials: [" + strings.Replace(issuerSection, "[RS256]", "[HS256]", 1) + "]}\n", "gateway.run_credentials: "},
	} {
		path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\n"+c.body)
		_, err := config.LoadForager()
		if err == nil || !strings.HasPrefix(err.Error(), path+": "+c.want) {
			t.Errorf("%q: error %v, want %q after the file", c.body, err, c.want)
		}
	}
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\nsession: {instance: {name: build-01}}\n")
	if r, err := config.LoadForager(); err != nil || r.Gateway {
		t.Errorf("a file without a gateway section reads as one with it: %+v, %v", r, err)
	}
}
