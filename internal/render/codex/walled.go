package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/render"
)

// HomeInWall is the folder in the agent image where a walled run binds Codex's
// writable home and sets CODEX_HOME: ~/.codex of the agent image's HOME, /home/agent.
// Codex keys each hook's trust by this path, see [WalledConfig].
const HomeInWall = "/home/agent/.codex"

// Bound is one file or folder of a walled run's Codex home that the run binds read-only
// over its path in [HomeInWall].
type Bound struct {
	// Path is the path under the home, such as config.toml or rules.
	Path string
	// Source is the file or folder on this machine.
	Source string
	// Dir marks a folder.
	Dir bool
}

// boundFiles are the files of a walled run's home that [WriteWalledHome] writes, and
// boundDirs the folders it binds. Codex reads hooks from config.toml and hooks.json,
// sets each variable of .env that is not named CODEX_*, starts the program an
// environments.toml lists, and reads the instructions, rules, skills and agents of its
// home; bound read-only, none of them is one the agent changes for a later Codex process.
var (
	boundFiles = []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.md"}
	boundDirs  = []string{"rules", "skills", "agents"}
)

// noHooks is the hooks.json of a walled run's home: a file with no hook.
const noHooks = "{\"hooks\": {}}\n"

// WriteWalledHome writes into files, a new folder on this machine that no mount of the
// run writes, the parts of a walled run's Codex home that the run binds read-only: the
// config.toml from dir, the runtime's directory of the composed home, with
// [WalledConfig]'s additions for projects, a hooks.json with no hook, an empty .env and
// environments.toml, and dir's AGENTS.md, or an empty one. Each folder of [boundDirs] is
// dir's when the compose wrote it, and an empty folder in files when it did not. It
// writes no auth.json. It returns the binds, files first, in a fixed order.
func WriteWalledHome(dir, files string, projects []string) ([]Bound, error) {
	config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		config = nil
	} else if err != nil {
		return nil, err
	}
	config, err = WalledConfig(config, HomeInWall, projects)
	if err != nil {
		return nil, err
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	content := map[string][]byte{"config.toml": config, "hooks.json": []byte(noHooks), "AGENTS.md": agents}
	var out []Bound
	for _, name := range boundFiles {
		p := filepath.Join(files, name)
		// A bind pins the file's inode, so the file is written once, in place, and never
		// replaced by a rename.
		if err := os.WriteFile(p, content[name], 0o644); err != nil {
			return nil, err
		}
		out = append(out, Bound{Path: name, Source: p})
	}
	for _, name := range boundDirs {
		src := filepath.Join(dir, name)
		if info, err := os.Lstat(src); err != nil || !info.IsDir() {
			src = filepath.Join(files, name)
			if err := os.Mkdir(src, 0o755); err != nil {
				return nil, err
			}
		}
		out = append(out, Bound{Path: name, Source: src, Dir: true})
	}
	return out, nil
}

// WalledConfig is config, a config.toml the compose rendered, with what a walled run adds
// for a Codex whose home is home in the wall: projects."<path>".trust_level =
// "untrusted" for each of projects, the paths where the run's checkout is in the wall,
// so Codex reads no hook, rule or setting of the repository's .codex and writes no
// trust of its own; and hooks.state."<home>/config.toml:<event>:<group>:<handler>".trusted_hash
// for each command hook of config's hooks, so Codex runs the harness's hooks without
// --dangerously-bypass-hook-trust, which would trust an agent's hooks too. home is
// absolute and contains no link, as Codex resolves it.
func WalledConfig(config []byte, home string, projects []string) ([]byte, error) {
	m := map[string]any{}
	if _, err := toml.NewDecoder(bytes.NewReader(config)).Decode(&m); err != nil {
		return nil, err
	}
	if len(projects) > 0 {
		trust := map[string]any{}
		for _, p := range projects {
			trust[p] = map[string]any{"trust_level": "untrusted"}
		}
		m["projects"] = trust
	}
	if hooks, ok := m["hooks"].(map[string]any); ok {
		state := map[string]any{}
		key := path.Join(home, "config.toml")
		for event, groups := range hooks {
			label, ok := eventLabels[event]
			if !ok {
				continue
			}
			list, ok := groups.([]map[string]any)
			if !ok {
				continue
			}
			for g, group := range list {
				handlers, _ := group["hooks"].([]map[string]any)
				for h, handler := range handlers {
					hash, ok, err := hookHash(event, group, handler)
					if err != nil {
						return nil, err
					}
					if ok {
						state[key+":"+label+":"+strconv.Itoa(g)+":"+strconv.Itoa(h)] = map[string]any{"trusted_hash": hash}
					}
				}
			}
		}
		if len(state) > 0 {
			hooks["state"] = state
		}
	}
	return render.EncodeTOML(m)
}

// eventLabels are the hook events of config.toml's hooks table, each with the name Codex
// uses for it in a hook's key and hash (codex-rs/hooks/src/lib.rs, hook_event_key_label).
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
// other event out of the hash (codex-rs/hooks/src/events/common.rs).
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
func hookHash(event string, group, handler map[string]any) (string, bool, error) {
	if handler["type"] != "command" {
		return "", false, nil
	}
	command, ok := handler["command"].(string)
	if !ok {
		return "", false, nil
	}
	normalized := map[string]any{"type": "command", "command": command, "timeout": hookTimeout(event, handler["timeout"]), "async": handler["async"] == true}
	if s, ok := handler["statusMessage"].(string); ok {
		normalized["statusMessage"] = s
	}
	if n, ok := handler["additionalContextLimit"].(int64); ok && contextEvents[event] && n != defaultContextLimit {
		normalized["additionalContextLimit"] = n
	}
	identity := map[string]any{"event_name": eventLabels[event], "hooks": []any{normalized}}
	if matcher, ok := group["matcher"].(string); ok && matched[event] {
		identity["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", false, fmt.Errorf("hash the %s hook %q: %w", event, command, err)
	}
	data := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	// serde_json writes U+2028 and U+2029 as they are, and encoding/json escapes them,
	// so a hook with either gets no trust rather than a hash Codex does not compute.
	if bytes.Contains(data, []byte(`\u2028`)) || bytes.Contains(data, []byte(`\u2029`)) {
		return "", false, nil
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), true, nil
}

// hookTimeout is a hook's timeout in seconds as Codex normalizes it: SessionEnd and
// Interrupt default to 1 and are held between 1 and 3; every other event defaults to 600
// and is at least 1.
func hookTimeout(event string, v any) int64 {
	n, set := v.(int64)
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
