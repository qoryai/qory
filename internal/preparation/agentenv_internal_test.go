package preparation

import (
	"slices"
	"testing"

	"github.com/qoryai/forager/link"
)

// TestDroppedNames is each group of names an image's value is dropped for, and names
// beside them that stay.
func TestDroppedNames(t *testing.T) {
	for _, n := range []string{
		"ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR", "QORY_RUN_SYSTEM", "MCP_TIMEOUT", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_AUDIT", "BASH_ENV", "ENV",
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	} {
		if !Dropped(n) {
			t.Errorf("%s is not dropped", n)
		}
	}
	for _, n := range []string{"PATH", "HOME", "LANG", "GOPATH", "ENVIRONMENT", "NODE_ENV", "anthropic_base_url", "GIT_SSL_CAINFO"} {
		if Dropped(n) {
			t.Errorf("%s is dropped", n)
		}
	}
}

// TestAgentEnvKeepsWhatForagerSets is an image's environment with the names Forager
// sets for the run kept: the image's NODE_OPTIONS, ANTHROPIC_BASE_URL and ALL_PROXY go;
// the wall's proxy, CLAUDE_CONFIG_DIR, the server's QORY_RUN_SYSTEM, the declared model
// key and every name outside the lists stay, in order.
func TestAgentEnvKeepsWhatForagerSets(t *testing.T) {
	env := []string{
		"PATH=/usr/bin", "NODE_OPTIONS=--require /x.js", "ANTHROPIC_BASE_URL=https://elsewhere.example",
		"ALL_PROXY=socks5://elsewhere.example", "all_proxy=socks5://elsewhere.example",
		"HTTP_PROXY=http://qory-proxy:3128", "CLAUDE_CONFIG_DIR=/tmp/qory-agent-home",
		"QORY_RUN_SYSTEM=github.com", "ANTHROPIC_API_KEY=placeholder", "HOME=/home/agent",
	}
	keep := map[string]bool{"HTTP_PROXY": true, "CLAUDE_CONFIG_DIR": true, "QORY_RUN_SYSTEM": true, "ANTHROPIC_API_KEY": true}
	want := []string{
		"PATH=/usr/bin", "HTTP_PROXY=http://qory-proxy:3128", "CLAUDE_CONFIG_DIR=/tmp/qory-agent-home",
		"QORY_RUN_SYSTEM=github.com", "ANTHROPIC_API_KEY=placeholder", "HOME=/home/agent",
	}
	if got := AgentEnv(env, keep); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestKeptNamesRoundTrip is the file of kept names written and read back: each name
// once, the authority's apart, and a line that is no name refused by its number alone.
func TestKeptNamesRoundTrip(t *testing.T) {
	data := KeptNames([]string{"QORY_RUN_ID", "HTTP_PROXY", "QORY_RUN_ID", ""}, []string{"SSL_CERT_FILE"})
	if want := "HTTP_PROXY\nQORY_RUN_ID\nca SSL_CERT_FILE\n"; string(data) != want {
		t.Errorf("wrote %q, want %q", data, want)
	}
	always, ca, err := ReadKept(data)
	if err != nil || len(always) != 2 || !always["HTTP_PROXY"] || !always["QORY_RUN_ID"] || len(ca) != 1 || !ca["SSL_CERT_FILE"] {
		t.Errorf("read %v %v %v", always, ca, err)
	}
	if _, _, err := ReadKept([]byte("HTTP_PROXY\nA=b\n")); err == nil || err.Error() != "line 2 is not a variable name" {
		t.Errorf("%v", err)
	}
}

// TestKeepForCountsTheAuthorityWhenItIsMounted is the authority's names kept only when
// the bundle's path is a mount, as the wall binds it for a run with an authority, and
// with no /proc, none of them.
func TestKeepForCountsTheAuthorityWhenItIsMounted(t *testing.T) {
	data := KeptNames([]string{"HTTP_PROXY"}, []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"})
	const bundle = "/qory/ca-bundle.pem"
	before := readMountinfo
	t.Cleanup(func() { readMountinfo = before })
	for _, c := range []struct {
		info string
		ca   bool
	}{
		{"36 35 98:0 / / rw - overlay overlay rw\n37 36 0:5 /ca /qory/ca-bundle.pem ro - ext4 /dev/sda1 rw\n", true},
		{"36 35 98:0 / / rw - overlay overlay rw\n37 36 0:5 /x /qory/qory ro - ext4 /dev/sda1 rw\n", false},
		{"", false},
	} {
		readMountinfo = func() ([]byte, error) { return []byte(c.info), nil }
		keep, err := KeepFor(data, bundle)
		if err != nil {
			t.Fatal(err)
		}
		if !keep["HTTP_PROXY"] || keep["SSL_CERT_FILE"] != c.ca || keep["NODE_EXTRA_CA_CERTS"] != c.ca {
			t.Errorf("mountinfo %q: kept %v", c.info, keep)
		}
	}
	if !Mounted([]byte(`1 2 0:5 / /a\040b ro - x y rw`), "/a b") {
		t.Error("a mount point with a space is not read")
	}
}

// TestAgentEnvKeepsThePlaceholders is the gateway's placeholders, whose names qory does
// not know before the run, under names the list drops: each with the placeholder value
// is kept, and an image's value of such a name is dropped.
func TestAgentEnvKeepsThePlaceholders(t *testing.T) {
	env := []string{
		"MCP_SERVICE_TOKEN=" + link.Placeholder, "CLAUDE_EXTRA_TOKEN=" + link.Placeholder,
		"ANTHROPIC_API_KEY=" + link.Placeholder, "MCP_X=other",
	}
	want := env[:3]
	if got := AgentEnv(env, map[string]bool{}); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestAgentEnvDropsAPlaceholderOutsideThePrefixes is the image setting the placeholder,
// a value without a slash that a shell sources, Node resolves and the loader loads as a
// relative name, under each name outside the prefixes: every one is dropped, and a
// prefixed name with it is kept.
func TestAgentEnvDropsAPlaceholderOutsideThePrefixes(t *testing.T) {
	var env []string
	for _, n := range []string{"BASH_ENV", "ENV", "LD_PRELOAD", "LD_AUDIT", "NODE_PATH", "NODE_OPTIONS", "SSL_CERT_FILE", "HTTPS_PROXY", "MCP_SERVICE_TOKEN"} {
		env = append(env, n+"="+link.Placeholder)
	}
	want := []string{"MCP_SERVICE_TOKEN=" + link.Placeholder}
	if got := AgentEnv(env, map[string]bool{}); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
