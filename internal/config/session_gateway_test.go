package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// examplePin is a certificate pin in its shape: the SHA-256 of a public key, in standard
// base64 with padding.
const examplePin = "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="

// TestTheSessionGatewayReads is session.gateway read: each setting as written, a relative
// path resolved under forager.yaml's directory, and qory config's rows, with the file as
// origin and the defaults for what the file leaves out.
func TestTheSessionGatewayReads(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\nsession:\n  gateway:\n    url: https://gateway.example:8443\n    ca_file: gateway-ca.pem\n    certificate_sha256: "+examplePin+"\n    run_credential_file: /run/issuer/run-credential\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Forager
	g := r.SessionGateway
	if g == nil || g.URL != "https://gateway.example:8443" || g.CAFile != "gateway-ca.pem" || g.CertificateSHA256 != examplePin || g.RunCredentialFile != "/run/issuer/run-credential" || r.Gateway {
		t.Fatalf("read %+v", g)
	}
	if got, want := r.Path(g.CAFile), filepath.Join(filepath.Dir(path), "gateway-ca.pem"); got != want {
		t.Errorf("the CA file resolves to %s, want %s", got, want)
	}
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{
		"session.gateway.url":                 "https://gateway.example:8443",
		"session.gateway.ca_file":             "gateway-ca.pem",
		"session.gateway.certificate_sha256":  examplePin,
		"session.gateway.run_credential_file": "/run/issuer/run-credential",
	} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s = %+v, want %q from the file", key, rows[key], want)
		}
	}

	for _, u := range []string{"https://gateway.example", "https://gateway.example/", "https://GATEWAY.example:443", "https://[::1]:8443", "HTTPS://gateway.example"} {
		foragerFile(t, "apiVersion: qory.dev/v1alpha1\nsession:\n  gateway:\n    url: "+u+"\n")
		if c, err := config.Load(t.TempDir(), true); err != nil || c.Forager.SessionGateway.URL != u {
			t.Errorf("%s: %v", u, err)
		}
	}
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\nsession:\n  gateway:\n    url: https://gateway.example\n")
	if c, err = config.Load(t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	rows = map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
	}
	for key, want := range map[string]string{
		"session.gateway.ca_file":             "(none: the system's roots)",
		"session.gateway.certificate_sha256":  "(none)",
		"session.gateway.run_credential_file": "(none)",
	} {
		if rows[key].Value != want || rows[key].Origin != config.Default {
			t.Errorf("%s = %+v, want %q by default", key, rows[key], want)
		}
	}
}

// TestNoSessionGatewayIsAGatewayOfEachRun is qory config's row with no gateway named:
// qory run starts a gateway for each run, with no file and with a file without
// session.gateway.
func TestNoSessionGatewayIsAGatewayOfEachRun(t *testing.T) {
	hermetic(t)
	for _, body := range []string{"", "apiVersion: qory.dev/v1alpha1\nsession:\n  instance:\n    name: build-01\n"} {
		if body != "" {
			foragerFile(t, body)
		}
		c, err := config.Load(t.TempDir(), true)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, row := range c.Rows() {
			switch row.Key {
			case "session.gateway.url":
				found = true
				if row.Value != "(none: qory run starts a gateway for each run)" || row.Origin != config.Default {
					t.Errorf("%q: session.gateway.url = %+v", body, row)
				}
			case "session.gateway.ca_file", "session.gateway.certificate_sha256", "session.gateway.run_credential_file":
				t.Errorf("%q: %s is listed with no gateway named", body, row.Key)
			}
		}
		if !found {
			t.Errorf("%q: no session.gateway.url row", body)
		}
	}
}

// TestASessionGatewayRefusesAGatewaySection is a forager.yaml that names a gateway for
// its runs and holds a gateway section too, any part of one; a session.gateway with no
// URL, or one Forager's session does not take; a pin that is empty or not one; and an
// empty ca_file or run_credential_file: each refused, naming the file.
func TestASessionGatewayRefusesAGatewaySection(t *testing.T) {
	hermetic(t)
	for _, c := range []struct {
		body, want string
		exact      bool
	}{
		{"session: {gateway: {url: \"https://gateway.example\"}}\ngateway: {egress: {mode: observe}}\n", ": gateway: this machine's runs go through the gateway session.gateway.url names, so it runs no gateway, and its file holds none: Qory Apiary's access key and the credentials' secrets belong on the gateway's machine. Remove the gateway section, or remove session.gateway to run the gateway here", true},
		{"session: {gateway: {url: \"https://gateway.example\"}}\ngateway: {server: {url: \"https://qory.example\"}}\n", ": gateway: this machine's runs go through the gateway session.gateway.url names, so it runs no gateway, and its file holds none: Qory Apiary's access key and the credentials' secrets belong on the gateway's machine. Remove the gateway section, or remove session.gateway to run the gateway here", true},
		{"session: {gateway: {ca_file: ca.pem}}\n", ": session.gateway.url is required", true},
		{"session: {gateway: {url: \"http://127.0.0.1:8443\"}}\n", `: session.gateway.url "http://127.0.0.1:8443" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://gateway.example/v1\"}}\n", `: session.gateway.url "https://gateway.example/v1" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://user@gateway.example\"}}\n", `: session.gateway.url "https://user@gateway.example" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://gateway.example?x=1\"}}\n", `: session.gateway.url "https://gateway.example?x=1" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://gateway.example#top\"}}\n", `: session.gateway.url "https://gateway.example#top" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://:8443\"}}\n", `: session.gateway.url "https://:8443" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"gateway.example:8443\"}}\n", `: session.gateway.url "gateway.example:8443" is not an https URL of a host and an optional port, with nothing after`, true},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: \"\"}}\n", ": session.gateway.certificate_sha256 is empty", true},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: abc}}\n", `: session.gateway.certificate_sha256 "abc" is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in =`, true},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: \"47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFV=\"}}\n", `: session.gateway.certificate_sha256 "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFV=" is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in =`, true},
		{"session: {gateway: {url: \"https://gateway.example\", ca_file: \"\"}}\n", ": session.gateway.ca_file is empty", true},
		{"session: {gateway: {url: \"https://gateway.example\", run_credential_file: \"\"}}\n", ": session.gateway.run_credential_file is empty", true},
		{"session: {gateway: {url: \"https://gateway.example\", access_key_id: ak_0123456789abcdef}}\n", `key "access_key_id" is not one`, false},
	} {
		path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\n"+c.body)
		_, err := config.Load(t.TempDir(), true)
		switch {
		case err == nil:
			t.Errorf("%q: read, want %q", c.body, c.want)
		case c.exact && err.Error() != path+c.want:
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		case !c.exact && (!strings.HasPrefix(err.Error(), path) || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q: %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestTheForagerSchemaTakesTheSessionGateway holds forager.schema.json to what the reader
// takes of session.gateway: a URL is required, the pin has its shape, and a file that
// names a gateway for its runs holds no gateway section.
func TestTheForagerSchemaTakesTheSessionGateway(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"session: {gateway: {url: \"https://gateway.example:8443\", ca_file: gateway-ca.pem, certificate_sha256: \"" + examplePin + "\", run_credential_file: /run/issuer/run-credential}}\n", true},
		{"session: {gateway: {url: \"https://gateway.example\"}, instance: {name: build-01}}\nwall: {adapter: docker}\n", true},
		{"session: {gateway: {ca_file: gateway-ca.pem}}\n", false},
		{"session: {gateway: {url: \"https://gateway.example/v1\"}}\n", false},
		{"session: {gateway: {url: \"https://:8443\"}}\n", false},
		{"session: {gateway: {url: \"https://[::1]:8443/\"}}\n", true},
		{"session: {gateway: {url: \"http://127.0.0.1:8443\"}}\n", false},
		{"session: {gateway: {url: \"https://gateway.example\", certificate_sha256: abc}}\n", false},
		{"session: {gateway: {url: \"https://gateway.example\", run_credential: x}}\n", false},
		{"session: {gateway: {url: \"https://gateway.example\"}}\ngateway: {egress: {mode: observe}}\n", false},
		{"gateway: {egress: {mode: observe}}\nsession: {instance: {name: build-01}}\n", true},
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
