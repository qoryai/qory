package agentenv_test

import (
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/agentenv"
)

// TestWidens is each prefix and name, matched exactly and case included, and the
// variables that stay allowed.
func TestWidens(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_", "CLAUDE_CONFIG_DIR", "QORY_RUN_KEY", "MCP_SERVERS", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_AUDIT", "BASH_ENV", "ENV"} {
		if !agentenv.Widens(name) {
			t.Errorf("%s does not widen", name)
		}
	}
	for _, name := range []string{"anthropic_api_key", "Claude_X", "XANTHROPIC_KEY", "ENVIRONMENT", "env", "NODE_OPTIONS_X", "LD_LIBRARY_PATH",
		"HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "PATH", "GOPATH", "CARGO_HOME", "ANTHROPIC"} {
		if agentenv.Widens(name) {
			t.Errorf("%s widens", name)
		}
	}
}

// TestWidening is the names that widen, in their order, each once.
func TestWidening(t *testing.T) {
	got := agentenv.Widening([]string{"PATH", "QORY_X", "ENV", "HOME", "QORY_X", "LD_PRELOAD"})
	if want := []string{"QORY_X", "ENV", "LD_PRELOAD"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Widening is %v, want %v", got, want)
	}
	if got := agentenv.Widening([]string{"PATH"}); got != nil {
		t.Errorf("Widening is %v", got)
	}
}
