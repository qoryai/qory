package prebuild

import (
	"reflect"
	"testing"
)

// TestCarriesUser is a proxy URL with user information and one without, with a scheme
// or without.
func TestCarriesUser(t *testing.T) {
	for url, want := range map[string]bool{
		"http://proxy.example.com:3128":           false,
		"proxy.example.com:3128":                  false,
		"":                                        false,
		"http://user:password@proxy.example.com":  true,
		"http://user@proxy.example.com:3128":      true,
		"user:password@proxy.example.com:3128":    true,
		"socks5://user@proxy.example.com:1080/":   true,
		"http://:password@proxy.example.com:3128": true,
		"http:/user:pw@proxy.example.com:3128":    true,
		"http:\\/user:pw@proxy.example.com:3128":  true,
		"http://user:p/w@proxy.example.com:3128":  true,
		"http://proxy.example.com:3128/a@b":       true,
	} {
		if got := carriesUser(url); got != want {
			t.Errorf("carriesUser(%q) is %v, want %v", url, got, want)
		}
	}
}

// TestAllowedWithholdsAProxyWithAUser is the environment the programs get: the allowed
// variables, the proxies and authorities among them, and not a proxy with a user,
// which is named once.
func TestAllowedWithholdsAProxyWithAUser(t *testing.T) {
	env := []string{
		"PATH=/usr/bin", "GITHUB_TOKEN=x", "HTTPS_PROXY=http://proxy.example.com:3128",
		"http_proxy=http://user:pw@proxy.example.com", "NO_PROXY=localhost,.example.com",
		"http_proxy=http://user:pw2@proxy.example.com", "SSL_CERT_FILE=/etc/ssl/ca.pem", "BUILDX_BUILDER=remote", "BUILDX_CONFIG=/etc/buildx",
	}
	out, withheld := allowed(env)
	want := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy.example.com:3128", "NO_PROXY=localhost,.example.com", "SSL_CERT_FILE=/etc/ssl/ca.pem"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("allowed is %v, want %v", out, want)
	}
	if !reflect.DeepEqual(withheld, []string{"http_proxy"}) {
		t.Errorf("withheld is %v", withheld)
	}
}
