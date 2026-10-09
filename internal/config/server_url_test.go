package config_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// serverURLShape ends every refusal of gateway.server.url but the secret's.
const serverURLShape = "an https URL of a host and an optional port, or an http one to this machine, with nothing after"

// TestAGatewayServerURLRefusalNamesWhatIsWrong is each refusal of gateway.server.url,
// exactly: the file, the part that is wrong and the server's scheme, host and port,
// never more of the URL.
func TestAGatewayServerURLRefusalNamesWhatIsWrong(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ url, want string }{
		{"qory.example", "gateway.server.url is not a URL: " + serverURLShape},
		{"//qory.example", "gateway.server.url is not a URL: " + serverURLShape},
		{"https:///v1", "gateway.server.url is not a URL: " + serverURLShape},
		{"https://qory example", "gateway.server.url is not a URL: " + serverURLShape},
		{"mailto:ops@qory.example", "gateway.server.url is not a URL: " + serverURLShape},
		{"ftp://qory.example/x", "gateway.server.url for ftp://qory.example is not https or http: " + serverURLShape},
		{"http://qory.example:8080/x", "gateway.server.url for http://qory.example:8080 is http to a host that is not this machine: " + serverURLShape},
		{"https://user@qory.example:8443/x", "gateway.server.url for https://qory.example:8443 holds user information: " + serverURLShape},
		{"https://qory.example/v1/events", "gateway.server.url for https://qory.example has a path: " + serverURLShape},
		{"https://qory.example/", "gateway.server.url for https://qory.example has a path: " + serverURLShape},
		{"http://127.0.0.1:8080/v1", "gateway.server.url for http://127.0.0.1:8080 has a path: " + serverURLShape},
		{"https://qory.example?x=1", "gateway.server.url for https://qory.example has a query or a fragment: " + serverURLShape},
		{"https://qory.example?", "gateway.server.url for https://qory.example has a query or a fragment: " + serverURLShape},
		{"https://qory.example#top", "gateway.server.url for https://qory.example has a query or a fragment: " + serverURLShape},
		{"https://qory.example#", "gateway.server.url for https://qory.example has a query or a fragment: " + serverURLShape},
		{"https://qory.example/?qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "gateway.server.url: the document contains an access key secret, which belongs in access-key-secret or QORY_ACCESS_KEY_SECRET and nowhere else"},
	} {
		path := foragerFile(t, "gateway:\n  server:\n    url: \""+c.url+"\"\n")
		_, err := config.Load(t.TempDir(), true)
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.url, err, want)
		}
		if err := config.CheckServerURL(c.url); err == nil || err.Error() != c.want {
			t.Errorf("CheckServerURL(%q) = %v, want %q", c.url, err, c.want)
		}
	}
}

// TestAGatewayServerURLIsTaken is what gateway.server.url takes: https to any host,
// and http to this machine, each with an optional port and nothing after.
func TestAGatewayServerURLIsTaken(t *testing.T) {
	for _, u := range []string{"https://qory.example", "https://qory.example:8443", "HTTPS://qory.example", "http://localhost", "http://127.0.0.1:8080", "http://[::1]:8443"} {
		if err := config.CheckServerURL(u); err != nil {
			t.Errorf("CheckServerURL(%q) = %v, want nil", u, err)
		}
	}
}

// TestAGatewayServerURLRefusalPrintsNoSecret is a gateway.server.url that holds a
// credential in its user information, its query or its fragment, or an access key
// secret percent-encoded in an IPv6 zone, https or http: the refusal names the part that
// is wrong and the server's scheme, host and port, or the secret, and holds no part of
// the credential or the secret.
func TestAGatewayServerURLRefusalPrintsNoSecret(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ url, want string }{
		{"https://TOKEN@qory.example", "gateway.server.url for https://qory.example holds user information"},
		{"https://u:p@qory.example:8443", "gateway.server.url for https://qory.example:8443 holds user information"},
		{"http://u:p@qory.example", "gateway.server.url for http://qory.example is http to a host that is not this machine"},
		{"ftp://u:p@qory.example/x?token=x#TOKEN", "gateway.server.url for ftp://qory.example is not https or http"},
		{"https://qory.example?token=x", "gateway.server.url for https://qory.example has a query or a fragment"},
		{"https://qory.example#TOKEN", "gateway.server.url for https://qory.example has a query or a fragment"},
		{"https://[fe80::1%25%71ak_SECRETZONE]", "gateway.server.url: the document contains an access key secret"},
		{"http://[fe80::1%25%71ak_SECRETZONE]", "gateway.server.url: the document contains an access key secret"},
		{"https://[fe80::1%25%71ak_SECRETZONE]:8443/", "gateway.server.url: the document contains an access key secret"},
	} {
		foragerFile(t, "gateway:\n  server:\n    url: \""+c.url+"\"\n")
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.url, err, c.want)
			continue
		}
		for _, secret := range []string{"TOKEN", "u:p", ":p@", "token=x", "token", "SECRETZONE", "fe80", c.url} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the refusal holds %q: %v", c.url, secret, err)
			}
		}
	}
}

// TestAGatewayServerURLEncodedTooOftenIsRefusedForThat is a gateway.server.url still
// percent-encoded after the 8 rounds the reader undoes: with no secret in it, or with
// one only deeper, it is refused for what it is, with no part of the URL, by the file's
// reader and by CheckServerURL, which qory access-key enrol asks. A secret found within
// the 8 rounds is refused as one, and a URL encoded 8 times is read to its end.
func TestAGatewayServerURLEncodedTooOftenIsRefusedForThat(t *testing.T) {
	hermetic(t)
	const tooDeep = "gateway.server.url is percent-encoded more than 8 times: " + serverURLShape
	for _, c := range []struct{ url, want string }{
		{"https://qory.example/%" + strings.Repeat("25", 8) + "41", tooDeep},
		{"https://qory.example:8443?%" + strings.Repeat("25", 12) + "41", tooDeep},
		{"https://[fe80::1%25%" + strings.Repeat("25", 9) + "71ak_SECRETZONE]", tooDeep},
		{"https://[fe80::1%25%" + strings.Repeat("25", 7) + "71ak_SECRETZONE]", "gateway.server.url: the document contains an access key secret, which belongs in access-key-secret or QORY_ACCESS_KEY_SECRET and nowhere else"},
		{"https://qory.example/?qak_SECRETZONE%" + strings.Repeat("25", 9) + "41", "gateway.server.url: the document contains an access key secret, which belongs in access-key-secret or QORY_ACCESS_KEY_SECRET and nowhere else"},
		{"https://qory.example/%" + strings.Repeat("25", 7) + "41", "gateway.server.url for https://qory.example has a path: " + serverURLShape},
	} {
		path := foragerFile(t, "gateway:\n  server:\n    url: \""+c.url+"\"\n")
		_, err := config.Load(t.TempDir(), true)
		if want := path + ": " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.url, err, want)
		}
		if err := config.CheckServerURL(c.url); err == nil || err.Error() != c.want {
			t.Errorf("CheckServerURL(%q) = %v, want %q", c.url, err, c.want)
		}
		if c.want != tooDeep || err == nil {
			continue
		}
		for _, part := range []string{"qory.example", "8443", "%25", "%41", "SECRETZONE", "fe80", "?", "/"} {
			if strings.Contains(strings.TrimPrefix(err.Error(), path), part) {
				t.Errorf("%s: the refusal holds %q: %v", c.url, part, err)
			}
		}
	}
}
