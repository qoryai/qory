package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// viaPin is a certificate_sha256 of the right shape.
const viaPin = "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="

// viaFile is a forager.yaml whose server is reached through via, written as a flow
// mapping's members.
func viaFile(via string) string {
	return "gateway:\n  server:\n    url: https://apiary.example\n    via: " + via + "\n"
}

// rowsOf is the rows of r by key.
func rowsOf(r *config.Forager) map[string]config.Row {
	rows := map[string]config.Row{}
	for _, row := range r.Rows() {
		rows[row.Key] = row
	}
	return rows
}

// TestGatewayServerViaReads is gateway.server.via read: its url, ca_file and
// certificate_sha256 as written, ca_file resolved under forager.yaml's directory, and
// the rows qory config lists for each, a default where the file writes none. A server
// without a via is reached directly, and lists no via's ca_file or pin.
func TestGatewayServerViaReads(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, viaFile("{url: \"https://gateway.example:8443\", ca_file: gateway-ca.pem, certificate_sha256: \""+viaPin+"\"}"))
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if v := r.Server.Via; v == nil || v.URL != "https://gateway.example:8443" || v.CAFile != "gateway-ca.pem" || v.CertificateSHA256 != viaPin {
		t.Fatalf("read %+v", r.Server.Via)
	}
	if got, want := r.Path(r.Server.Via.CAFile), filepath.Join(filepath.Dir(path), "gateway-ca.pem"); got != want {
		t.Errorf("ca_file resolves to %s, want %s", got, want)
	}
	rows := rowsOf(r)
	for key, value := range map[string]string{
		"gateway.server.via.url":                "https://gateway.example:8443",
		"gateway.server.via.ca_file":            "gateway-ca.pem",
		"gateway.server.via.certificate_sha256": viaPin,
	} {
		if got := rows[key]; got.Value != value || got.Origin != path {
			t.Errorf("%s = %+v, want %q from %s", key, got, value, path)
		}
	}

	foragerFile(t, viaFile("{url: \"https://gateway.example\"}"))
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	rows = rowsOf(r)
	for key, value := range map[string]string{
		"gateway.server.via.ca_file":            "(none: the system's roots)",
		"gateway.server.via.certificate_sha256": "(none)",
	} {
		if got := rows[key]; got.Value != value || got.Origin != config.Default {
			t.Errorf("%s = %+v, want %q by default", key, got, value)
		}
	}

	foragerFile(t, "gateway:\n  server:\n    url: https://apiary.example\n")
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	rows = rowsOf(r)
	if got := rows["gateway.server.via.url"]; got.Value != "(none: the server is reached directly)" || got.Origin != config.Default {
		t.Errorf("gateway.server.via.url = %+v without a via", got)
	}
	for _, key := range []string{"gateway.server.via.ca_file", "gateway.server.via.certificate_sha256"} {
		if _, ok := rows[key]; ok {
			t.Errorf("%s is listed without a via", key)
		}
	}
}

// TestGatewayServerViaRefuses is each refusal of gateway.server.via, in its exact
// words after the file, none of which holds what a URL holds past its origin, nor a
// secret: a run key in the URL is named as one, which belongs in QORY_RUN_KEY.
func TestGatewayServerViaRefuses(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ via, want string }{
		{"", ": gateway.server.via is empty"},
		{"{}", ": gateway.server.via.url is required"},
		{"{ca_file: ca.pem}", ": gateway.server.via.url is required"},
		{"{url: \"\"}", ": gateway.server.via.url is required"},
		{"{url: gateway.example}", ": gateway.server.via.url is not a URL: an https URL of a host and an optional port, with nothing after"},
		{"{url: \"http://gateway.example\"}", ": gateway.server.via.url for http://gateway.example is not https: an https URL of a host and an optional port, with nothing after"},
		{"{url: \"https://" + marker + "@gateway.example\"}", ": gateway.server.via.url for https://gateway.example holds user information: an https URL of a host and an optional port, with nothing after"},
		{"{url: \"https://gateway.example/" + marker + "\"}", ": gateway.server.via.url for https://gateway.example has a path: an https URL of a host and an optional port, with nothing after"},
		{"{url: \"https://gateway.example?k=" + marker + "\"}", ": gateway.server.via.url for https://gateway.example has a query or a fragment: an https URL of a host and an optional port, with nothing after"},
		{"{url: \"https://gateway.example?k=qrk_" + marker + "." + marker + "\"}", ": gateway.server.via.url: the document contains a run key, which belongs in QORY_RUN_KEY and nowhere else"},
		{"{url: \"https://gateway.example?k=%71rk_" + marker + "\"}", ": gateway.server.via.url: the document contains a run key, which belongs in QORY_RUN_KEY and nowhere else"},
		{"{url: \"https://gateway.example\", ca_file: \"\"}", ": gateway.server.via.ca_file is empty"},
		{"{url: \"https://gateway.example\", certificate_sha256: \"\"}", ": gateway.server.via.certificate_sha256 is empty"},
		{"{url: \"https://gateway.example\", certificate_sha256: null}", ": gateway.server.via.certificate_sha256 is empty"},
		{"{url: \"https://gateway.example\", certificate_sha256: " + marker + "}", ": gateway.server.via.certificate_sha256 is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in ="},
	} {
		path := foragerFile(t, viaFile(c.via))
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("via %s: %v, want %q", c.via, err, path+c.want)
		}
		if err != nil && (strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "qrk_")) {
			t.Errorf("via %s: the refusal holds the value: %v", c.via, err)
		}
	}
}

// TestCheckViaReadsTheCAFile is gateway.server.via.ca_file read when a run starts: a
// file that is not there, and one that holds no PEM certificate, are refused naming
// the file and never what it holds; one that holds a certificate passes, as does a via
// without a ca_file and a file without a via.
func TestCheckViaReadsTheCAFile(t *testing.T) {
	hermetic(t)
	path := foragerFile(t, viaFile("{url: \"https://gateway.example\", ca_file: gateway-ca.pem}"))
	ca := filepath.Join(filepath.Dir(path), "gateway-ca.pem")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckVia(); err == nil || err.Error() != path+": gateway.server.via.ca_file "+ca+": no such file or directory" {
		t.Errorf("a missing ca_file: %v", err)
	}
	if err := os.WriteFile(ca, []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckVia(); err == nil || err.Error() != path+": gateway.server.via.ca_file "+ca+" holds no PEM certificate" {
		t.Errorf("a ca_file without a certificate: %v", err)
	}
	if err := os.WriteFile(ca, certificatePEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckVia(); err != nil {
		t.Errorf("a ca_file with a certificate: %v", err)
	}
	for _, body := range []string{viaFile("{url: \"https://gateway.example\"}"), "gateway:\n  server:\n    url: https://apiary.example\n"} {
		foragerFile(t, body)
		if r, err := config.LoadForager(); err != nil || r.CheckVia() != nil {
			t.Errorf("%q: %v, %v", body, err, r.CheckVia())
		}
	}
}

// certificatePEM is a new self-signed certificate, in PEM.
func certificatePEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gateway.example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestTheForagerSchemaTakesAVia holds forager.schema.json to what the reader takes of
// gateway.server.via: an https URL of a host and an optional port, a ca_file and a
// certificate_sha256 of 44 characters ending in =; a via without a url, an http one,
// one with a path, a pin of another shape and a key it does not have fail it.
func TestTheForagerSchemaTakesAVia(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		via   string
		valid bool
	}{
		{"{url: \"https://gateway.example:8443\", ca_file: gateway-ca.pem, certificate_sha256: \"" + viaPin + "\"}", true},
		{"{url: \"https://gateway.example/\"}", true},
		{"{ca_file: gateway-ca.pem}", false},
		{"{url: \"http://gateway.example\"}", false},
		{"{url: \"https://gateway.example/path\"}", false},
		{"{url: \"https://user@gateway.example\"}", false},
		{"{url: \"https://gateway.example\", certificate_sha256: abc}", false},
		{"{url: \"https://gateway.example\", ca_file: \"\"}", false},
		{"{url: \"https://gateway.example\", run_credential_file: f}", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+viaFile(c.via)), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("via %s: %v, want valid %v", c.via, err, c.valid)
		}
	}
}

// TestGatewayKeysListsWhatAGatewayOfItsOwnReads is GatewayKeys: the keys of the gateway
// section beside server, sorted, a key written null among them; none without a gateway
// section, or with server alone.
func TestGatewayKeysListsWhatAGatewayOfItsOwnReads(t *testing.T) {
	hermetic(t)
	for _, c := range []struct {
		name, file string
		want       string
	}{
		{"no gateway section", "session:\n  instance:\n    name: build-01\n", ""},
		{"server alone", viaFile("{url: \"https://gateway.example:8443\"}"), ""},
		{"out of order", "gateway:\n  tls:\n    certificate: c.pem\n    key: k.pem\n  listen: 127.0.0.1:8443\n  server:\n    url: https://apiary.example\n  egress:\n    mode: observe\n", "egress listen tls"},
		{"a key written null", "gateway:\n  egress:\n  server:\n    url: https://apiary.example\n", "egress"},
	} {
		foragerFile(t, c.file)
		r, err := config.LoadForager()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := strings.Join(r.GatewayKeys, " "); got != c.want {
			t.Errorf("%s: GatewayKeys %q, want %q", c.name, got, c.want)
		}
	}
}
