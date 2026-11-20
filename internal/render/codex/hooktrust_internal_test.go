package codex

import "testing"

// endCheck is a Stop hook command with a quote, a variable, HTML characters and a letter
// outside ASCII, which the hash takes as written.
const endCheck = `/srv/harness/modules/core/hooks/end-check.sh --run "$QORY_RUN_ID" <a&b> é`

// TestHookHashMatchesCodex checks hookHash against hashes that a Codex 0.167.2 run
// accepted, from the probe's trust-hook.py for the same command and timeout, with the
// timeout as TOML and as JSON decode it. A command with U+2028 or U+2029 gets no hash,
// since encoding/json escapes them and Codex hashes them as they are.
func TestHookHashMatchesCodex(t *testing.T) {
	const (
		endCheckHash = "sha256:63c3f3b6340d10c653f0da3f6fb82f539ed270e6a4e24cfc4bab96de20d4ec3a" // endCheck, 30
		trueHash     = "sha256:fef68b1a8a8090019e0421bcd64cfac1f7b7c21980e2f35d49b46427395a6dda" // /bin/true, 600
	)
	for _, c := range []struct {
		name    string
		handler map[string]any
		want    string
	}{
		{"a TOML timeout", map[string]any{"type": "command", "command": endCheck, "timeout": int64(30)}, endCheckHash},
		{"a JSON timeout", map[string]any{"type": "command", "command": endCheck, "timeout": float64(30)}, endCheckHash},
		{"no timeout", map[string]any{"type": "command", "command": "/bin/true"}, trueHash},
		{"U+2028", map[string]any{"type": "command", "command": "echo  "}, ""},
		{"U+2029", map[string]any{"type": "command", "command": "echo  "}, ""},
	} {
		got, ok := hookHash("Stop", map[string]any{}, c.handler)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}
