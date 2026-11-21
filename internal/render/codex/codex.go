// Package codex renders for Codex CLI, which reads a project's .codex directory, skills
// from .agents/skills, and AGENTS.override.md ahead of AGENTS.md. The checkout gets a
// link for each of those three, and .codex contains config.toml with the target model as
// model and the MCP servers as mcp_servers, plus one TOML file per agent. The harness's
// variables, a fragment's shell_environment_policy.set among them, are the launch's.
// Codex reads prompt files from the user's home only and has no output styles, so
// commands and output styles are skipped. A files entry named codex/<path> lands at
// .codex/<path>.
//
// The paths under .codex a files entry may not take, see [render.Reserved]:
//
//	config.toml  qory writes it
//	agents       agents are linked there; ship it as agents/<name>
//	skills       qory copies them there for a launch; ship it as skills/<name>
//	AGENTS.md    qory writes it for a launch
//	auth.json    Codex's login file, which no home of qory's has
//	hooks.json, .env, environments.toml, AGENTS.override.md, rules/read-access.rules
//	             qory writes them for every walled run
//
// Codex also takes its whole home from CODEX_HOME, and the runtime's directory is one:
// config.toml, the agents, the skills copied under skills/ and the instructions as
// AGENTS.md, the global instructions file Codex reads there. [Template] sets the
// variable. Codex reads its login from auth.json in the same home; qory writes none there
// and refuses a module that ships one, and Codex reads its model key from OPENAI_API_KEY.
// The checkout's .codex never links the skills or the instructions.
package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/modelplace"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Codex CLI, and the name of its directory in the
// home.
const Runtime = "codex"

// codex implements [render.Runtime] for Codex CLI.
type codex struct{}

func init() { render.Register(codex{}) }

// Name is the target.runtime value.
func (codex) Name() string { return Runtime }

// Links are the hard links .codex, for config.toml and the agents, and .agents/skills,
// plus a soft AGENTS.override.md for the instructions. Codex reads the override file
// ahead of AGENTS.md, which leaves a repository's own AGENTS.md in place beside it. The
// instructions link is left out when the compose produced none.
func (codex) Links(res *compose.Result) []render.Link {
	links := []render.Link{
		{Checkout: ".codex", Home: Runtime, Except: []string{"skills", "AGENTS.md"}},
		{Checkout: ".agents/skills", Home: "skills"},
	}
	if res == nil || res.Instructions != "" {
		links = append(links, render.Link{Checkout: "AGENTS.override.md", Home: "AGENTS.md", Soft: true})
	}
	return links
}

// Skips are commands and output styles, since Codex reads prompt files from the user's
// home only and has no output styles.
func (codex) Skips() []string { return []string{"commands", "output-styles"} }

// textAuthFile is the refusal of a module that ships Codex's login file, with the module's
// name.
const textAuthFile = "Remove `files/codex/auth.json` from module %s.\n  A Codex run takes its model key from the run, not from a file."

// textWalledFile is the refusal of a module that ships a file of [walledFiles], with the
// file's path under files/codex for the first %s; the module's name is left as %%s.
const textWalledFile = "Remove `files/codex/%s` from module %%s.\n  qory writes it for every walled Codex run."

// textReadRulesFile is the refusal of a module that ships files/codex/rules/
// read-access.rules, with the module's name: qory writes that file only for a walled run
// that only reads, from the harness's read rules, see [WriteWalledHome].
const textReadRulesFile = "Remove `files/codex/rules/" + ReadRulesName + "` from module %s.\n  qory writes it for a walled Codex run that only reads, from the harness's `settings/codex/" + ReadRulesName + "`."

// walledFiles are the paths under .codex that qory writes in the home of every walled
// Codex run, see [WriteWalledHome]: the hooks file, the variables Codex sets, the
// environments Codex starts, and the instructions Codex reads ahead of AGENTS.md.
var walledFiles = []string{"hooks.json", ".env", "environments.toml", "AGENTS.override.md"}

// Reserved are config.toml and the agents directory, which Render writes, the skills and
// AGENTS.md, which qory places for a launch, auth.json, Codex's login file, which no
// home of qory's has, the [walledFiles], and rules/read-access.rules, which qory writes
// for a walled read run.
func (codex) Reserved() []render.Reserved {
	out := []render.Reserved{
		{Path: "config.toml", Why: "qory writes it"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "skills", Why: "qory copies them there for a launch; ship it as skills/<name>"},
		{Path: "AGENTS.md", Why: "qory writes it for a launch"},
		{Path: "auth.json", Refusal: textAuthFile},
	}
	for _, p := range walledFiles {
		out = append(out, render.Reserved{Path: p, Refusal: fmt.Sprintf(textWalledFile, p)})
	}
	return append(out, render.Reserved{Path: "rules/" + ReadRulesName, Refusal: textReadRulesFile})
}

// Render writes config.toml, with the target model as model, the MCP servers as
// mcp_servers, one table per server containing the object as the module wrote it, and any
// other codex settings fragment without shell_environment_policy.set, whose variables are
// the launch's, see [EnvAt], then one agents/<name>.toml per agent containing the agent's
// name, description and its body as developer_instructions, the model its tier maps to as
// model and the tier's effort, when the entry sets one, as model_reasoning_effort, and a
// [features] table with shell_tool = false for an agent that declares no shell tool, see
// [keepsShell]. A home composed for Codex and another runtime gets no tier, see
// [render.AloneModel], and an agent whose tier has no map gets neither key.
//
// config.toml also gets features.unified_exec_tty = false, so the agent cannot start a
// shell with a terminal and send it commands with write_stdin, for which Codex 0.170.0
// runs no PreToolUse hook; web_search = "disabled", since without the key Codex 0.169.0
// turns web search on: cached, or live in a run with --sandbox danger-full-access, such
// as a walled run (codex-rs/core/src/config/mod.rs, resolve_web_search_mode and
// resolve_web_search_mode_for_turn); and a
// hooks.state table with a trusted_hash for each command hook of hooks.json and
// config.toml, see [trustedHooks]. The instructions written as AGENTS.md end with the
// names the session registers, see [render.Addressing]: a Codex home registers them as
// the modules wrote them, and the file states that. Codex reads a project .codex only in
// a project the user has marked trusted. A fragment that sets a key of [reservedKeys], a
// hook number Codex cannot read, see [checkHookNumbers], or a hook group or hook Codex
// cannot read, see [checkHookShapes], fails the render before anything is written.
func (codex) Render(res *compose.Result, dir, home string) error {
	if err := checkReserved(res.SettingsFor(Runtime, "config.toml", home)); err != nil {
		return err
	}
	for _, file := range []string{"config.toml", "hooks.json"} {
		if err := checkHookNumbers(file, res.SettingsFor(Runtime, file, home)); err != nil {
			return err
		}
	}
	for _, data := range res.CodexHooksJSON {
		if err := checkHookNumbers("hooks.json", exactJSON(data)); err != nil {
			return err
		}
	}
	// Each module's hooks before the merged ones, so a fault is found at its place in the
	// module's file.
	for _, f := range res.CodexHooks {
		hooks, _ := f.Hooks.(map[string]any)
		if err := checkHookShapes(f.File, f.Module, hooks); err != nil {
			return err
		}
	}
	for _, file := range []string{"config.toml", "hooks.json"} {
		hooks, _ := res.SettingsFor(Runtime, file, home)["hooks"].(map[string]any)
		if err := checkHookShapes(file, "", hooks); err != nil {
			return err
		}
	}
	err := render.WriteSettings(res, Runtime, dir, home, []string{"config.toml"}, func(file string, m map[string]any) {
		if file != "config.toml" {
			return
		}
		if res.Stack.Target.Model != "" {
			m["model"] = res.Stack.Target.Model
		}
		render.PutServers(m, "mcp_servers", res.MCPFor(home))
		render.DropEnv(m, setKey)
		// A fragment may set neither features, web_search nor hooks.state, see [reservedKeys].
		m["features"] = map[string]any{"unified_exec_tty": false}
		m["web_search"] = "disabled"
		hooks, _ := m["hooks"].(map[string]any)
		fromJSON, _ := res.SettingsFor(Runtime, "hooks.json", home)["hooks"].(map[string]any)
		state := trustedHooks(filepath.Join(home, Runtime), map[string]map[string]any{"hooks.json": fromJSON, "config.toml": hooks})
		if len(state) > 0 {
			if hooks == nil {
				hooks = map[string]any{}
				m["hooks"] = hooks
			}
			hooks["state"] = state
		}
	})
	if err != nil {
		return err
	}
	for _, e := range res.Entries {
		if e.Kind != "agents" {
			continue
		}
		doc, err := render.ReadDocument(res, render.Bare, e.Path)
		if err != nil {
			return err
		}
		name := doc.String("name")
		if name == "" {
			name = e.Name
		}
		role := map[string]any{
			"name":                   name,
			"description":            doc.String("description"),
			"developer_instructions": strings.TrimRight(doc.Body, "\n"),
		}
		if t, ok := res.Tiers[e.Tier]; ok {
			role["model"] = t.Model
			if t.Effort != "" {
				role[effortKey] = t.Effort
			}
		}
		if !keepsShell(doc.Front) {
			role["features"] = map[string]any{"shell_tool": false}
		}
		data, err := render.EncodeTOML(role)
		if err != nil {
			return err
		}
		if err := render.WriteFile(dir, filepath.Join("agents", e.Name+".toml"), data); err != nil {
			return err
		}
	}
	if err := placeSkills(res, dir); err != nil {
		return err
	}
	if text := render.WithAddressing(res, render.Bare, codex{}.Skips()); text != "" {
		if err := render.WriteFile(dir, "AGENTS.md", []byte(text)); err != nil {
			return err
		}
	}
	return nil
}

// effortKey is the key of an agent file that Codex reads the agent's effort from, the
// agent place of the effort in Forager's descriptor for the runtime.
const effortKey = "model_reasoning_effort"

// AgentEfforts are the effort values a Codex agent file takes in [effortKey], as the
// agent place of the effort in Forager's descriptor for the runtime lists them; nil when
// the descriptor has none.
func (codex) AgentEfforts() []string {
	p, ok := modelplace.AgentEffort(Runtime)
	if !ok {
		return nil
	}
	return p.Values
}

// TiersAlone marks Codex as a [render.AloneModel]: a home composed for Codex and another
// runtime writes no tier into the Codex agents, so a model id of another provider does
// not reach them.
func (codex) TiersAlone() {}

// keepsShell reports whether a role keeps Codex's shell tool. A role with no tools key
// keeps it, since Claude Code gives an agent with no tools key every tool. A role whose
// tools key lists tools keeps it only when a listed tool is a shell: Bash or PowerShell,
// alone or with a rule such as Bash(git:*). Codex has no setting for any other tool of a
// role, and a role's file cannot set its sandbox.
func keepsShell(front map[string]any) bool {
	if v, ok := front["tools"]; !ok || v == nil {
		return true
	}
	for _, t := range module.ToolsOf(front) {
		name, _, _ := strings.Cut(t, "(")
		if name = strings.TrimSpace(name); name == "Bash" || name == "PowerShell" {
			return true
		}
	}
	return false
}

// reservedKey is a key path of config.toml that a harness fragment may not set. Each
// segment is a pattern of [path.Match], so experimental_realtime_*_base_url covers every
// key of that form.
type reservedKey []string

// textReservedKey is the refusal of a fragment that sets a key of [reservedKeys], with
// the key as a dotted path, as [shownArg] prints it, for its %s. The key is on the cause
// line, so line 1 has a fixed length.
const textReservedKey = "Remove the reserved key from `settings/codex/config.toml`.\n  qory reserves `%s`; a Codex run takes no value for it from the harness."

// reservedKeys are the key paths of config.toml that a harness fragment may not set,
// because Forager's launch of Codex sets them as --config flags, which win over every
// config.toml, or because qory writes them for a run, or because they send the model's
// requests, the login or Codex's files elsewhere: the model provider and its settings,
// project trust, hook trust, features, analytics, OpenTelemetry, the notify program, web
// search, feedback, the update check, the OpenAI and ChatGPT addresses, the realtime
// addresses, the metadata of each request, profiles, where the login is kept, the login
// shell, the files of instructions, compaction prompt and model list, the Node program,
// and Codex's database and log folders. Each name is one Codex 0.167.2 reads
// (codex-rs/config/src/config_toml.rs). A fragment may set the other keys of hooks, such
// as hooks.Stop. The Codex arguments of a walled run may not set the keys of
// [reservedKeys] either, and the arguments of every run may not set a key of
// [trustKeys], such as hooks.Stop; see [checkArgs].
var reservedKeys = []reservedKey{
	{"model_provider"},
	{"model_providers"},
	{"projects"},
	{"hooks", "state"},
	{"features"},
	{"analytics"},
	{"otel"},
	{"notify"},
	{"web_search"},
	{"feedback", "enabled"},
	{"check_for_update_on_startup"},
	{"openai_base_url"},
	{"chatgpt_base_url"},
	{"experimental_realtime_*_base_url"},
	{"responses_api_metadata"},
	{"profile"},
	{"profiles"},
	{"cli_auth_credentials_store"},
	{"allow_login_shell"},
	{"model_instructions_file"},
	{"experimental_compact_prompt_file"},
	{"model_catalog_json"},
	{"js_repl_node_path"},
	{"sqlite_home"},
	{"log_dir"},
}

// checkReserved refuses m, the harness's config.toml fragment, when it sets a key of
// [reservedKeys], naming the first in that list's order, and the first key of m in
// sorted order that a pattern covers.
func checkReserved(m map[string]any) error {
	for _, k := range reservedKeys {
		if key, ok := findKey(m, k); ok {
			return fmt.Errorf(textReservedKey, shownArg(strings.Join(key, ".")))
		}
	}
	return nil
}

// findKey is the first key path of m, in sorted order, that pattern covers.
func findKey(m map[string]any, pattern []string) ([]string, bool) {
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if ok, _ := path.Match(pattern[0], name); !ok {
			continue
		}
		if len(pattern) == 1 {
			return []string{name}, true
		}
		if sub, ok := m[name].(map[string]any); ok {
			if rest, ok := findKey(sub, pattern[1:]); ok {
				return append([]string{name}, rest...), true
			}
		}
	}
	return nil, false
}

// textHookNumber is the refusal of a hook number that Codex cannot read, with the key,
// the hook's event and the settings file it is in.
const textHookNumber = "Set `%s` of the `%s` hook to a whole number of 0 or more.\n  The hook is in the harness's `settings/codex/%s`, and Codex ignores the whole file otherwise."

// hookNumbers are the keys of a hook that Codex 0.167.2 reads as an unsigned whole
// number, by the hook's type (codex-rs/protocol/src/hooks.rs, HookHandlerConfig). Codex
// reads a timeout of 0 as 1 second, and an additionalContextLimit of 0 turns off the
// spilling of the hook's context to a file. Codex ignores these keys on a hook of any
// other type.
var hookNumbers = map[string][]string{
	"command":  {"timeout", "additionalContextLimit"},
	"mcp_tool": {"timeout"},
}

// checkHookNumbers refuses m, the harness's fragment of config.toml or hooks.json, when
// a hook in it has a number of [hookNumbers] that is negative or fractional, naming the
// first event in sorted order. Codex 0.167.2 then reads none of config.toml and starts
// with its defaults, or drops every hook of hooks.json
// (codex-rs/hooks/src/engine/discovery.rs, load_hooks_json). Codex refuses a float such
// as 2.0 as well: config.toml decodes it as a float, which is refused, and a hooks.json
// fragment read by [exactJSON] has it as a number with a ".", which is refused too.
func checkHookNumbers(file string, m map[string]any) error {
	events, _ := m["hooks"].(map[string]any)
	for _, event := range slices.Sorted(maps.Keys(events)) {
		groups, _ := events[event].([]any)
		for _, g := range groups {
			group, _ := g.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, h := range handlers {
				handler, _ := h.(map[string]any)
				kind, _ := handler["type"].(string)
				for _, key := range hookNumbers[kind] {
					v, ok := handler[key]
					if _, float := v.(float64); ok && (!whole(v) || float && file == "config.toml") {
						return fmt.Errorf(textHookNumber, key, event, file)
					}
				}
			}
		}
	}
	return nil
}

// hookEvents are the hook events that Codex 0.170.0 reads from a hooks table
// (codex-rs/protocol/src/hooks.rs, HookEventsToml). It ignores a key of any other name.
var hookEvents = []string{
	"PreModelRequest", "PostModelRequest", "PreToolUse", "PermissionRequest", "PostToolUse",
	"PreCompact", "PostCompact", "SessionStart", "SessionEnd", "UserPromptSubmit",
	"SubagentStart", "SubagentStop", "Stop", "Interrupt",
}

// The causes of a hook group and of a hook that Codex cannot read, see [checkHookShapes],
// after the fault.
const (
	textGroupShape = "%s, and Codex reads no hook of a file with such a group."
	textHookShape  = "%s, and Codex reads no hook of a file with such a hook."
)

// runLineMax is the most characters that the first line of an error has after
// "qory run: ", which qory run prints before it: 100 in all.
const runLineMax = 90

// hookError is the error with the fix line that fix makes of the place of file, a
// settings file of the harness's Codex settings, and cause as its second line. With
// module, the place is file in that module, by name: in the fix line when the line has
// at most [runLineMax] characters, and otherwise at the start of the second line.
func hookError(file, module, cause string, fix func(at string) string) error {
	at := "`settings/codex/" + file + "`"
	if module == "" {
		return fmt.Errorf("%s\n  %s", fix(at), cause)
	}
	name := accesskey.ShownName(module, false)
	if line := fix(at + " of module " + name); utf8.RuneCountInString(line) <= runLineMax {
		return fmt.Errorf("%s\n  %s", line, cause)
	}
	return fmt.Errorf("%s\n  The file is in module %s. %s", fix(at), name, cause)
}

// checkHookShapes fails on hooks, the hooks table of the harness's config.toml or
// hooks.json, file, when a group or a hook of one of [hookEvents] does not decode as Codex
// 0.170.0 decodes it (codex-rs/protocol/src/hooks.rs, MatcherGroup and
// HookHandlerConfig): Codex then reads no hook of the file
// (codex-rs/hooks/src/engine/discovery.rs, load_hooks_json and
// load_toml_hooks_from_layer). An event's value is a list of groups; a group is a table
// whose matcher, when set, is text and whose hooks, when set, is a list; a hook is a table
// whose type is command, with command as text, mcp_tool, with server and tool as text, prompt
// or agent, and whose other keys of that type have the type Codex reads. Codex ignores a key
// it does not know, and so does this check. It names the first fault, by event in the order
// of [hookEvents], then by place: its place in a module's file with the module's name,
// and its place in the merged file for module "".
func checkHookShapes(file, module string, hooks map[string]any) error {
	groupFault := func(event string, i int, fault string) error {
		return hookError(file, module, fmt.Sprintf(textGroupShape, fault), func(at string) string {
			return fmt.Sprintf("Fix `hooks.%s[%d]` in %s.", event, i, at)
		})
	}
	for _, event := range hookEvents {
		v, ok := hooks[event]
		if !ok {
			continue
		}
		groups, ok := listOf(v)
		if !ok {
			return hookGroupsError(file, module, event)
		}
		for i, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				return groupFault(event, i, "It is no table of keys")
			}
			if m, ok := group["matcher"]; ok && m != nil {
				if _, text := m.(string); !text {
					return groupFault(event, i, "Its `matcher` is no text")
				}
			}
			h, ok := group["hooks"]
			if !ok {
				continue
			}
			handlers, ok := listOf(h)
			if !ok {
				return groupFault(event, i, "Its `hooks` is no list")
			}
			for j, h := range handlers {
				if fault := hookFault(h); fault != "" {
					return hookError(file, module, fmt.Sprintf(textHookShape, fault), func(at string) string {
						return fmt.Sprintf("Fix `hooks.%s[%d].hooks[%d]` in %s.", event, i, j, at)
					})
				}
			}
		}
	}
	return nil
}

// listOf is v as a list, as JSON and TOML decode one: a list of values, or a list of
// tables for TOML's arrays of tables.
func listOf(v any) ([]any, bool) {
	switch l := v.(type) {
	case []any:
		return l, true
	case []map[string]any:
		out := make([]any, len(l))
		for i, m := range l {
			out[i] = m
		}
		return out, true
	}
	return nil, false
}

// hookFault is why Codex 0.170.0 cannot decode h, one hook of a group, see
// [checkHookShapes]; "" when it can. A key that Codex reads as optional may be null.
func hookFault(h any) string {
	hook, ok := h.(map[string]any)
	if !ok {
		return "It is no table of keys"
	}
	text := func(key string, required bool) string {
		v, ok := hook[key]
		if !ok || v == nil {
			if required {
				return "It has no `" + key + "` text"
			}
			return ""
		}
		if _, ok := v.(string); !ok {
			return "Its `" + key + "` is no text"
		}
		return ""
	}
	number := func(key string) string {
		if v, ok := hook[key]; ok && v != nil && !unsigned(v) {
			return "Its `" + key + "` is no whole number of 0 or more"
		}
		return ""
	}
	kind, _ := hook["type"].(string)
	var faults []string
	switch kind {
	case "command":
		faults = append(faults, text("command", true), text("commandWindows", false), text("command_windows", false))
		if _, a := hook["commandWindows"]; a {
			if _, b := hook["command_windows"]; b {
				faults = append(faults, "It sets both `commandWindows` and `command_windows`")
			}
		}
		faults = append(faults, number("timeout"), number("additionalContextLimit"), text("statusMessage", false))
		if v, ok := hook["async"]; ok {
			if _, ok := v.(bool); !ok {
				faults = append(faults, "Its `async` is neither true nor false")
			}
		}
	case "mcp_tool":
		faults = append(faults, text("server", true), text("tool", true), number("timeout"), text("statusMessage", false))
		if v, ok := hook["input"]; ok {
			if input, ok := v.(map[string]any); !ok {
				faults = append(faults, "Its `input` is no table of keys")
			} else if !inTOML(input) {
				faults = append(faults, "Its `input` contains a null or a whole number above 2^63-1")
			}
		}
	case "prompt", "agent":
	default:
		return "Its `type` is none of command, mcp_tool, prompt and agent"
	}
	for _, f := range faults {
		if f != "" {
			return f
		}
	}
	return ""
}

// unsigned reports whether v, a decoded settings value, is a whole number from 0 to
// 2^64-1, which Codex reads as an unsigned number.
func unsigned(v any) bool {
	switch n := v.(type) {
	case json.Number:
		_, err := strconv.ParseUint(string(n), 10, 64)
		return err == nil
	case int64:
		return n >= 0
	case int:
		return n >= 0
	case float64:
		return n >= 0 && n == math.Trunc(n) && n < math.Exp2(64)
	}
	return false
}

// inTOML reports whether v, the input of an mcp_tool hook, has no null and no whole
// number above 2^63-1 at any depth: Codex 0.170.0 fails to decode such an input, since
// TOML has neither (codex-rs/protocol/src/hooks.rs, deserialize_mcp_tool_input).
func inTOML(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case json.Number:
		// A number with a fraction or an exponent, or out of range of both, is a float.
		if _, err := strconv.ParseInt(string(x), 10, 64); err == nil || strings.ContainsAny(string(x), ".eE") {
			return true
		}
		_, err := strconv.ParseUint(string(x), 10, 64)
		return err != nil
	case float64:
		return x != math.Trunc(x) || x < math.Exp2(63)
	case uint64:
		return x <= math.MaxInt64
	case map[string]any:
		for _, e := range x {
			if !inTOML(e) {
				return false
			}
		}
	case []any:
		for _, e := range x {
			if !inTOML(e) {
				return false
			}
		}
	case []map[string]any:
		for _, e := range x {
			if !inTOML(e) {
				return false
			}
		}
	}
	return true
}

// exactJSON decodes a hooks.json fragment with every number as written, a json.Number,
// so a number with a "." or an exponent stays apart from a whole one. A fragment that
// does not decode is an empty map: compose decoded it before.
func exactJSON(data []byte) map[string]any {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	m := map[string]any{}
	if err := d.Decode(&m); err != nil {
		return map[string]any{}
	}
	return m
}

// whole reports whether v, a decoded settings value, is a whole number of 0 or more. A
// value that is no number passes: Codex refuses it too, and the refusal of a number
// would name the wrong fault.
func whole(v any) bool {
	switch n := v.(type) {
	case json.Number:
		return !strings.ContainsAny(string(n), ".eE-")
	case int64:
		return n >= 0
	case int:
		return n >= 0
	case float64:
		return n == math.Trunc(n) && n >= 0
	}
	return true
}

// setKey is where a codex fragment sets variables, which Codex sets in every command it
// runs.
var setKey = []string{"shell_environment_policy", "set"}

// EnvAt is config.toml's shell_environment_policy.set: what a fragment sets there is the
// launch's, a default each, and the file holds no variable. Codex passes its own
// environment to every command it runs: shell_environment_policy inherits all of it by
// default and leaves out no name by default, ignore_default_excludes being true, so a
// variable named like *KEY*, *SECRET* or *TOKEN* reaches the command too. A fragment that
// sets inherit, exclude, include_only or ignore_default_excludes = false filters the
// launch's variables the way it filters any other.
func (codex) EnvAt() (string, []string) { return "config.toml", setKey }

// HomeVariable is the variable Codex reads its home from.
const HomeVariable = "CODEX_HOME"

// Template starts Codex with the runtime's directory as its home. A walled run sets
// HomeVariable to [HomeInWall] in its place.
func (codex) Template() render.Template {
	return render.Template{Command: "codex", Env: map[string]string{HomeVariable: "${dir}"}}
}
