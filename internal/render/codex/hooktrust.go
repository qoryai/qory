package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"

	"github.com/qoryai/qory/internal/compose"
)

// hookFiles are the files of a Codex home that Codex 0.170.0 reads hooks from, in the
// order in which it loads them from one folder (codex-rs/hooks/src/engine/discovery.rs,
// discover_handlers).
var hookFiles = []string{"hooks.json", "config.toml"}

// trustedHooks is the hooks.state table that Render writes into config.toml, so Codex
// runs the harness's command hooks from CODEX_HOME without
// --dangerously-bypass-hook-trust, which would trust an agent's hooks too. Codex runs a
// hook of CODEX_HOME only when hooks.state."<file>:<event>:<group>:<hook>".trusted_hash
// is the hash it computes for the hook, and otherwise skips the hook with no error
// (codex-rs/hooks/src/engine/discovery.rs, hook_trust_status). <file> is hooks.json or
// config.toml in dir, the runtime's directory of the home, with every link resolved, as
// Codex resolves CODEX_HOME (codex-rs/utils/home-dir/src/lib.rs); <event> is the event's
// label, see [eventLabels]; <group> and <hook> count from 0. byFile has the hooks table
// of each of [hookFiles], as Render writes it. A walled run takes no state from this
// table: it binds the hooks at [SystemInWall], see [WalledConfig].
func trustedHooks(dir string, byFile map[string]map[string]any) map[string]any {
	home := resolvedPath(dir)
	state := map[string]any{}
	for _, file := range hookFiles {
		forEachHook(byFile[file], func(event string, g, h int, group, handler map[string]any) {
			if hash, ok := hookHash(event, group, handler); ok {
				key := filepath.Join(home, file) + ":" + eventLabels[event] + ":" + strconv.Itoa(g) + ":" + strconv.Itoa(h)
				state[key] = map[string]any{"trusted_hash": hash}
			}
		})
	}
	return state
}

// forEachHook calls fn with each hook of hooks, a hooks table of a Codex settings file,
// for each event of [eventLabels], with its group's place and its place in the group, the
// group and the hook. A group or a hook that is no table keeps its place and is passed
// over: Codex counts it too.
func forEachHook(hooks map[string]any, fn func(event string, g, h int, group, handler map[string]any)) {
	for _, event := range hookEvents {
		groups, _ := listOf(hooks[event])
		for g, v := range groups {
			group, _ := v.(map[string]any)
			handlers, _ := listOf(group["hooks"])
			for h, v := range handlers {
				if handler, ok := v.(map[string]any); ok {
					fn(event, g, h, group, handler)
				}
			}
		}
	}
}

// UntrustedHooks lists each hook of the Codex settings of res, for a home at home, that
// Render writes no trusted hash for, as "hooks.<event>[<group>].hooks[<hook>] in <file>":
// an mcp_tool hook. Codex skips such a hook in a run without a wall, with no error; a
// walled run binds it at [SystemInWall], where Codex runs it.
//
// shortcut: qory hashes command hooks only, as Codex 0.170.0 does for them; hash an
// mcp_tool hook, with its server, tool and input, when a harness ships one.
func UntrustedHooks(res *compose.Result, home string) []string {
	var out []string
	for _, file := range hookFiles {
		hooks, _ := res.SettingsFor(Runtime, file, home)["hooks"].(map[string]any)
		forEachHook(hooks, func(event string, g, h int, _, handler map[string]any) {
			if handler["type"] == "mcp_tool" {
				out = append(out, fmt.Sprintf("hooks.%s[%d].hooks[%d] in %s", event, g, h, file))
			}
		})
	}
	return out
}

// resolvedPath is p, made absolute, with every link resolved: the nearest folder of p
// that exists, resolved, and the rest of p after it. A compose with --check renders for
// a home that may not exist yet.
func resolvedPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	rest := ""
	for dir := p; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// eventLabels are the hook events of a hooks table, each with the label Codex uses for it
// in a hook's key and hash (codex-rs/hooks/src/lib.rs, hook_event_key_label).
var eventLabels = map[string]string{
	"PreModelRequest":   "pre_model_request",
	"PostModelRequest":  "post_model_request",
	"PreToolUse":        "pre_tool_use",
	"PermissionRequest": "permission_request",
	"PostToolUse":       "post_tool_use",
	"PreCompact":        "pre_compact",
	"PostCompact":       "post_compact",
	"SessionStart":      "session_start",
	"SessionEnd":        "session_end",
	"UserPromptSubmit":  "user_prompt_submit",
	"SubagentStart":     "subagent_start",
	"SubagentStop":      "subagent_stop",
	"Stop":              "stop",
	"Interrupt":         "interrupt",
}

// matched are the events whose hooks take a matcher; Codex leaves the matcher of any
// other event out of the hash (codex-rs/hooks/src/events/common.rs,
// matcher_pattern_for_event).
var matched = map[string]bool{
	"PreToolUse": true, "PermissionRequest": true, "PostToolUse": true, "SessionStart": true,
	"SessionEnd": true, "SubagentStart": true, "SubagentStop": true, "PreCompact": true,
	"PostCompact": true,
}

// contextEvents are the events whose hooks keep additionalContextLimit.
var contextEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "SessionStart": true, "UserPromptSubmit": true,
	"SubagentStart": true,
}

// defaultContextLimit is the additionalContextLimit Codex leaves out of the hash.
const defaultContextLimit = 2500

// hookHash is the trusted_hash Codex computes for one command hook of event in group
// (codex-rs/hooks/src/engine/discovery.rs, hook_hash, and
// codex-rs/config/src/fingerprint.rs, version_for_toml): "sha256:" and the SHA-256 of
// the compact JSON, keys sorted, of the event's label, the matcher where the event takes
// one, and the one handler normalized: type, command as written, timeout with Codex's
// default and limits, async, and statusMessage and additionalContextLimit when they
// count. False for a handler that is no command, and for a command with U+2028 or
// U+2029, which get no trust.
func hookHash(event string, group, handler map[string]any) (string, bool) {
	if handler["type"] != "command" {
		return "", false
	}
	command, ok := handler["command"].(string)
	if !ok {
		return "", false
	}
	normalized := map[string]any{"type": "command", "command": command, "timeout": hookTimeout(event, handler["timeout"]), "async": handler["async"] == true}
	if s, ok := handler["statusMessage"].(string); ok {
		normalized["statusMessage"] = s
	}
	if n, ok := wholeNumber(handler["additionalContextLimit"]); ok && contextEvents[event] && n != defaultContextLimit {
		normalized["additionalContextLimit"] = n
	}
	identity := map[string]any{"event_name": eventLabels[event], "hooks": []any{normalized}}
	if matcher, ok := group["matcher"].(string); ok && matched[event] {
		identity["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// The identity has text, whole numbers and booleans only, which always encode.
	_ = enc.Encode(identity)
	data := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	// serde_json writes U+2028 and U+2029 as they are, and encoding/json escapes them,
	// so a hook with either gets no trust rather than a hash Codex does not compute.
	if bytes.Contains(data, []byte(` `)) || bytes.Contains(data, []byte(` `)) {
		return "", false
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), true
}

// hookTimeout is a hook's timeout in seconds as Codex normalizes it: SessionEnd and
// Interrupt default to 1 and are held between 1 and 3; every other event defaults to 600
// and is at least 1.
func hookTimeout(event string, v any) int64 {
	n, set := wholeNumber(v)
	if event == "SessionEnd" || event == "Interrupt" {
		if !set {
			return 1
		}
		return min(max(n, 1), 3)
	}
	if !set {
		return 600
	}
	return max(n, 1)
}

// wholeNumber is v, a number of a settings file as the compose decoded it, as a whole
// number: an int64 from TOML, or a whole float64 from JSON. Render refuses a negative or
// fractional hook number before it hashes, see [checkHookNumbers].
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < math.Exp2(63) {
			return int64(n), true
		}
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
