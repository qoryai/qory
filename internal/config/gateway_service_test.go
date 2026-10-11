package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestTheForagerSchemaTakesTheGatewayService holds forager.schema.json to what the
// reader takes of gateway.listen and gateway.tls: a TLS without its key fails it, and so
// does gateway.run_credentials, which is no key of the file.
func TestTheForagerSchemaTakesTheGatewayService(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {listen: \"0.0.0.0:8443\", tls: {certificate: gateway.pem, key: gateway-key.pem}}\n", true},
		{"gateway: {listen: \"127.0.0.1:8443\"}\n", true},
		{"gateway: {listen: \"[::1]:8443\"}\n", true},
		{"gateway: {listen: gateway.example}\n", false},
		{"gateway: {tls: {certificate: gateway.pem}}\n", false},
		{"gateway: {tls: {certificate: gateway.pem, key: gateway-key.pem, ca: ca.pem}}\n", false},
		{"gateway: {run_credentials: []}\n", false},
		{"gateway: {run_credentials: [{issuer: \"https://issuer.example\"}]}\n", false},
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

// TestTheGatewayServiceReads is gateway.listen and gateway.tls read: each as written, a
// relative path resolved under forager.yaml's directory, and what the reader refuses an
// error that names the file and the key. gateway.run_credentials is no key of the file.
func TestTheGatewayServiceReads(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n  listen: 0.0.0.0:8443\n  tls: {certificate: gateway.pem, key: /etc/qory/gateway-key.pem}\n")
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
	for _, c := range []struct{ body, want string }{
		{"gateway: {listen: gateway.example}\n", `gateway.listen is not host:port, such as 0.0.0.0:8443`},
		{"gateway: {listen: \"0.0.0.0:99999\"}\n", `gateway.listen is not host:port, such as 0.0.0.0:8443`},
		{"gateway: {tls: {certificate: gateway.pem}}\n", "gateway.tls needs both certificate and key"},
		{"gateway: {run_credentials: [{issuer: \"https://issuer.example\"}]}\n", `line 2: key "run_credentials" is not one forager.yaml reads`},
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
