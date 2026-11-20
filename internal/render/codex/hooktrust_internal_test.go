package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// endCheck is a Stop hook command with a quote, a variable, HTML characters and a letter
// outside ASCII, which the hash takes as written.
const endCheck = `/srv/harness/modules/core/hooks/end-check.sh --run "$QORY_RUN_ID" <a&b> é`

// TestHookHashMatchesCodex checks hookHash against hashes that a Codex 0.167.2 run
// accepted, from the probe's trust-hook.py for the same command and timeout, with the
// timeout as TOML and as JSON decode it. A command, a statusMessage or a matcher with
// U+2028 or U+2029 gets no hash, since encoding/json escapes them and Codex hashes them
// as they are; the six characters \u2028 get a hash. The matcher of an event that takes
// none is not in the hash, so it counts for nothing.
func TestHookHashMatchesCodex(t *testing.T) {
	const (
		endCheckHash = "sha256:63c3f3b6340d10c653f0da3f6fb82f539ed270e6a4e24cfc4bab96de20d4ec3a" // endCheck, 30
		trueHash     = "sha256:fef68b1a8a8090019e0421bcd64cfac1f7b7c21980e2f35d49b46427395a6dda" // /bin/true, 600
	)
	// literal is the hash of a Stop hook whose command is echo and the six characters
	// \u2028, whose backslash the JSON escapes.
	sum := sha256.Sum256([]byte(`{"event_name":"stop","hooks":[{"async":false,"command":"echo \\u2028","timeout":600,"type":"command"}]}`))
	literal := "sha256:" + hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name    string
		event   string
		group   map[string]any
		handler map[string]any
		want    string
	}{
		{"a TOML timeout", "Stop", nil, map[string]any{"type": "command", "command": endCheck, "timeout": int64(30)}, endCheckHash},
		{"a JSON timeout", "Stop", nil, map[string]any{"type": "command", "command": endCheck, "timeout": float64(30)}, endCheckHash},
		{"no timeout", "Stop", nil, map[string]any{"type": "command", "command": "/bin/true"}, trueHash},
		{"the text \\u2028", "Stop", nil, map[string]any{"type": "command", "command": `echo \u2028`}, literal},
		{"U+2028 in the command", "Stop", nil, map[string]any{"type": "command", "command": "echo \u2028"}, ""},
		{"U+2029 in the command", "Stop", nil, map[string]any{"type": "command", "command": "echo \u2029"}, ""},
		{"U+2028 in the statusMessage", "Stop", nil, map[string]any{"type": "command", "command": "/bin/true", "statusMessage": "a\u2028b"}, ""},
		{"U+2029 in a matcher", "PreToolUse", map[string]any{"matcher": "a\u2029b"}, map[string]any{"type": "command", "command": "/bin/true"}, ""},
		{"U+2029 in a matcher of Stop", "Stop", map[string]any{"matcher": "a\u2029b"}, map[string]any{"type": "command", "command": "/bin/true"}, trueHash},
	} {
		got, ok := hookHash(c.event, c.group, c.handler)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}
