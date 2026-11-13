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
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// HomeInWall is the folder in the wall where a walled run binds Codex's writable home
// and sets CODEX_HOME: one fixed path in every image, whatever HOME the image sets, so
// qory knows it before the join. Codex keys each hook's trust by this path, see
// [WalledConfig].
const HomeInWall = "/tmp/qory-codex-home"

// Bound is one file or folder of a walled run's Codex home that the run binds read-only
// at Target, its path in [HomeInWall].
type Bound struct {
	// Path is the path under the home, such as config.toml or rules.
	Path string
	// Source is the file or folder on this machine, in the folder [WriteWalledHome]
	// writes, which shares no path with any other mount of the run.
	Source string
	// Target is the path in the wall: Path under [HomeInWall].
	Target string
	// Dir marks a folder.
	Dir bool
}

// boundFiles are the files of a walled run's home that [WriteWalledHome] writes, and
// boundDirs the folders it binds. Codex reads hooks from config.toml and hooks.json,
// sets each variable of .env that is not named CODEX_*, starts the program an
// environments.toml lists, and reads the instructions, rules, skills and agents of its
// home; bound read-only, none of them is one the agent changes for a later Codex process.
// Codex reads AGENTS.override.md ahead of AGENTS.md in its home and passes over one that
// is empty or only white space (codex-rs/codex-home/src/instructions/mod.rs), so an
// empty one keeps AGENTS.md the instructions.
var (
	boundFiles = []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.override.md", "AGENTS.md"}
	boundDirs  = []string{"rules", "skills", "agents"}
)

// noHooks is the hooks.json of a walled run's home: a file with no hook.
const noHooks = "{\"hooks\": {}}\n"

// WalledRun is what [WriteWalledHome] needs to know of the run.
type WalledRun struct {
	// Projects are the paths where the run's checkout is in the wall.
	Projects []string
	// Checkouts are the folders on this machine that the wall shows at Projects. qory
	// reads the skills of each one's .codex/skills and .agents/skills.
	Checkouts []string
	// Skills are the skills the run may load, from [AllowedSkills]. With [AllSkills],
	// the run may load every skill of the home's skills folder.
	Skills []string
	// Access is what the run may do on its target, stack.AccessRead or
	// stack.AccessWrite.
	Access string
	// ReadRules are the harness's Codex execpolicy rules for a read run,
	// compose.Result.CodexReadRules; nil when it has none.
	ReadRules []byte
}

// ReadRulesName is the file in the rules folder of a walled read run's home that
// contains [WalledRun.ReadRules].
const ReadRulesName = "read-access.rules"

// WriteWalledHome makes the folder files, which must not exist, on this machine apart
// from the writable home and from every other mount of the run, such as
// <user cache>/qory/runs/<run id>/codex-fixed, and writes into it every part of a walled run's Codex home
// that the run binds read-only: the config.toml from dir, the runtime's directory of the
// composed home, with [WalledConfig]'s additions for run.Projects and with every skill
// that is not in run.Skills disabled, a hooks.json with no hook, an empty .env,
// environments.toml and AGENTS.override.md, dir's AGENTS.md, or an empty one, and a copy
// of each folder of [boundDirs] that dir has, or an empty folder. For a run whose access
// is read, rules/ also contains run.ReadRules as [ReadRulesName], in place of any file of
// that name in dir's rules; for any other access, rules/ is the copy alone. A mount with
// a target may share no path on this machine with another mount, so nothing is bound
// from dir itself, which the run mounts as the harness home. It writes no auth.json. It
// returns the binds, files first, in a fixed order.
//
// The skills disabled are every name, other than run.Skills, of a skill in the copy of
// dir's skills and in run.Checkouts, by [skillNames], and every skill of run.Checkouts
// by its path in the wall, by [checkoutSkillPaths], so a checkout's skill with the name
// of an allowed one is disabled too. When run.Skills contains
// [AllSkills], every name of a skill in the copy of dir's skills counts as in run.Skills.
// A skill that is not there when WriteWalledHome runs keeps its name enabled: one the
// agent writes during the run, and one in ~/.agents/skills or /etc/codex/skills of the
// image.
func WriteWalledHome(dir, files string, run WalledRun) ([]Bound, error) {
	// A folder that exists may have links planted in it; a new one has none.
	if err := os.Mkdir(files, 0o755); err != nil {
		return nil, err
	}
	var dirs []Bound
	for _, name := range boundDirs {
		dst := filepath.Join(files, name)
		if err := os.Mkdir(dst, 0o755); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			if err := copyTree(filepath.Join(dir, name), dst); err != nil {
				return nil, err
			}
		}
		dirs = append(dirs, Bound{Path: name, Source: dst, Target: path.Join(HomeInWall, name), Dir: true})
	}
	if run.Access == stack.AccessRead && len(run.ReadRules) > 0 {
		p := filepath.Join(files, "rules", ReadRulesName)
		// The copy may have a link of this name to the composed home; writing through
		// it would change the home.
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := writeNew(p, run.ReadRules, 0o644); err != nil {
			return nil, err
		}
	}
	type root struct{ path, bound string }
	roots := []root{{path: filepath.Join(files, "skills")}}
	for _, c := range run.Checkouts {
		for _, r := range skillRoots {
			roots = append(roots, root{path: filepath.Join(c, r), bound: c})
		}
	}
	allowed := slices.Clone(run.Skills)
	if slices.Contains(run.Skills, AllSkills) {
		allowed = append(allowed, skillNames(roots[0].path, "")...)
	}
	var disabled []string
	for _, r := range roots {
		for _, n := range skillNames(r.path, r.bound) {
			if !slices.Contains(allowed, n) {
				disabled = append(disabled, n)
			}
		}
	}
	slices.Sort(disabled)
	var disabledPaths []string
	for _, c := range run.Checkouts {
		disabledPaths = append(disabledPaths, checkoutSkillPaths(c, run.Projects)...)
	}
	slices.Sort(disabledPaths)
	config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		config = nil
	} else if err != nil {
		return nil, err
	}
	config, err = WalledConfig(config, HomeInWall, run.Projects, slices.Compact(disabled), slices.Compact(disabledPaths))
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
		if err := writeNew(p, content[name], 0o644); err != nil {
			return nil, err
		}
		out = append(out, Bound{Path: name, Source: p, Target: path.Join(HomeInWall, name)})
	}
	return append(out, dirs...), nil
}

// writeNew writes data to a new file at p, with mode perm. It fails when anything is at
// p already, a link included, so it never writes through a link to another file.
func writeNew(p string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// checkoutSkillPaths are the paths in the wall of each SKILL.md that [findSkills] finds
// in the skill folders of checkout, for each of projects, the paths where the wall shows
// checkout. Codex compares a skills.config path with a skill's SKILL.md path with every
// link resolved, as Codex sees it in the wall, and falls back to the path it reached
// when that fails (codex-rs/ext/skills/src/loader/host.rs). Both paths are here: the
// file's real path and the path qory reached, each relative to checkout and put under
// each project. A project is taken as Codex resolves it, as for the trust entry of
// [WalledConfig].
func checkoutSkillPaths(checkout string, projects []string) []string {
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range skillRoots {
		for _, f := range findSkills(filepath.Join(checkout, r), checkout) {
			for _, rel := range []string{relTo(realCheckout, f.real), relTo(checkout, f.path)} {
				if rel == "" {
					continue
				}
				for _, p := range projects {
					out = append(out, path.Join(p, rel))
				}
			}
		}
	}
	return out
}

// relTo is file relative to dir, with forward slashes; "" when file is not below dir.
func relTo(dir, file string) string {
	rel, err := filepath.Rel(dir, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// copyTree copies the folder src into dst, which exists: folders, regular files with
// their executable bits, and links, each written with an absolute target, so a relative
// link leads where it led from src. The harness home that such a target lies in is
// mounted at the same path in the wall. Any other kind of file is an error.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(p), target)
			}
			return os.Symlink(target, to)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return writeNew(to, data, 0o644|info.Mode().Perm()&0o111)
		default:
			return fmt.Errorf("copy %s for the walled Codex home: it is neither a folder, a regular file nor a link", p)
		}
	})
}

// WalledConfig is config, a config.toml the compose rendered, with what a walled run adds
// for a Codex whose home is home in the wall: projects."<path>".trust_level = "untrusted"
// for each of projects, the paths where the run's checkout is in the wall, so Codex reads
// no hook, rule or setting of the repository's .codex and writes no trust of its own; and
// hooks.state."<home>/config.toml:<event>:<group>:<handler>".trusted_hash for each
// command and MCP tool hook of config's hooks, so Codex runs the harness's hooks without
// --dangerously-bypass-hook-trust, which would trust an agent's hooks too. home is
// absolute and contains no link, as Codex resolves it.
//
// It also turns off the skills Codex ships and the skills of a cloud provider, with
// skills.bundled.enabled = false and cloud.skills.enabled = false, and adds a
// skills.config entry with enabled = false for each name of disabled, then one for each
// SKILL.md path of disabledPaths, after the entries config has, since a later entry
// decides (codex-rs/config/src/skills_config.rs). Codex reads skills.config from this
// file alone, never from a repository's .codex.
//
// It sets cli_auth_credentials_store = "ephemeral", so Codex keeps a login in memory
// only and reads no auth.json from the writable home, which the agent could write
// (codex-rs/login/src/auth/storage.rs, EphemeralAuthStorage). Without a login, Codex
// also reads no cloud-config-bundle-cache.json, whose managed hooks would run without
// trust (codex-rs/cloud-config/src/service.rs, load_startup_bundle). A model provider's
// env_key reads its variable and needs no stored login.
func WalledConfig(config []byte, home string, projects, disabled, disabledPaths []string) ([]byte, error) {
	m := map[string]any{}
	if _, err := toml.NewDecoder(bytes.NewReader(config)).Decode(&m); err != nil {
		return nil, err
	}
	skills, _ := m["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
	}
	skills["bundled"] = map[string]any{"enabled": false}
	entries, _ := skills["config"].([]map[string]any)
	for _, name := range disabled {
		entries = append(entries, map[string]any{"name": name, "enabled": false})
	}
	for _, p := range disabledPaths {
		entries = append(entries, map[string]any{"path": p, "enabled": false})
	}
	if len(entries) > 0 {
		skills["config"] = entries
	}
	m["skills"] = skills
	cloud, _ := m["cloud"].(map[string]any)
	if cloud == nil {
		cloud = map[string]any{}
	}
	cloud["skills"] = map[string]any{"enabled": false}
	m["cloud"] = cloud
	m["cli_auth_credentials_store"] = "ephemeral"
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

// hookHash is the trusted_hash Codex computes for one hook of event in group
// (codex-rs/hooks/src/engine/discovery.rs, hook_hash, and
// codex-rs/config/src/fingerprint.rs, version_for_toml): "sha256:" and the SHA-256 of
// the compact JSON, keys sorted, of the event's label, the matcher where the event takes
// one, and the one handler normalized, see [commandHandler] and [mcpToolHandler]. False
// for a handler of any other type, for one Codex skips, and for a hook with U+2028 or
// U+2029 in its matcher, see [lineSeparator]; none of them gets trust.
func hookHash(event string, group, handler map[string]any) (string, bool, error) {
	var normalized map[string]any
	switch handler["type"] {
	case "command":
		normalized = commandHandler(event, handler)
	case "mcp_tool":
		normalized = mcpToolHandler(event, handler)
	}
	if normalized == nil {
		return "", false, nil
	}
	identity := map[string]any{"event_name": eventLabels[event], "hooks": []any{normalized}}
	if matcher, ok := group["matcher"].(string); ok && matched[event] {
		if lineSeparator(matcher) {
			return "", false, nil
		}
		identity["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", false, fmt.Errorf("hash a %s hook: %w", event, err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), true, nil
}

// commandHandler is a command hook's handler as Codex normalizes it before the hash:
// type, command as written, timeout with Codex's default and limits, async, and
// statusMessage and additionalContextLimit when they count. nil for a command of only
// white space, which Codex skips, and for U+2028 or U+2029 in the command or
// statusMessage.
func commandHandler(event string, handler map[string]any) map[string]any {
	command, ok := handler["command"].(string)
	if !ok || strings.TrimSpace(command) == "" || lineSeparator(command) {
		return nil
	}
	normalized := map[string]any{"type": "command", "command": command, "timeout": hookTimeout(event, handler["timeout"]), "async": handler["async"] == true}
	if s, ok := handler["statusMessage"].(string); ok {
		if lineSeparator(s) {
			return nil
		}
		normalized["statusMessage"] = s
	}
	if n, ok := handler["additionalContextLimit"].(int64); ok && contextEvents[event] && n != defaultContextLimit {
		normalized["additionalContextLimit"] = n
	}
	return normalized
}

// mcpToolHandler is an MCP tool hook's handler as Codex normalizes it before the hash:
// type, server and tool as written, input, an empty table when it has none, timeout
// with Codex's default and limits, and statusMessage when it is set. nil for a
// SessionEnd hook and for an empty server or tool, which Codex skips, for U+2028 or
// U+2029 in a string, and for an input whose JSON qory cannot write as Codex does: one
// with a float or a date anywhere in it. Codex writes a float with Rust's shortest form,
// which differs from Go's for some values, and a TOML date as a table of its own.
func mcpToolHandler(event string, handler map[string]any) map[string]any {
	if event == "SessionEnd" {
		return nil
	}
	server, _ := handler["server"].(string)
	tool, _ := handler["tool"].(string)
	if strings.TrimSpace(server) == "" || strings.TrimSpace(tool) == "" || lineSeparator(server) || lineSeparator(tool) {
		return nil
	}
	input := map[string]any{}
	if v, ok := handler["input"]; ok {
		m, ok := v.(map[string]any)
		if !ok || !hashable(m) {
			return nil
		}
		input = m
	}
	normalized := map[string]any{"type": "mcp_tool", "server": server, "tool": tool, "input": input, "timeout": hookTimeout(event, handler["timeout"])}
	if s, ok := handler["statusMessage"].(string); ok {
		if lineSeparator(s) {
			return nil
		}
		normalized["statusMessage"] = s
	}
	return normalized
}

// hashable reports whether v, a value TOML decoded, is one whose JSON encoding/json writes
// as serde_json does: a string without U+2028 or U+2029, an integer, a boolean, or a table
// or array of such values.
func hashable(v any) bool {
	switch v := v.(type) {
	case string:
		return !lineSeparator(v)
	case int64, bool:
		return true
	case map[string]any:
		for k, e := range v {
			if lineSeparator(k) || !hashable(e) {
				return false
			}
		}
		return true
	case []any:
		for _, e := range v {
			if !hashable(e) {
				return false
			}
		}
		return true
	case []map[string]any:
		for _, e := range v {
			if !hashable(e) {
				return false
			}
		}
		return true
	}
	return false
}

// lineSeparator reports whether s contains U+2028 or U+2029. serde_json writes them as
// they are, and encoding/json escapes them, so a hook with either in a string it hashes
// gets no trust rather than a hash Codex does not compute.
func lineSeparator(s string) bool {
	return strings.ContainsAny(s, "\u2028\u2029")
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
