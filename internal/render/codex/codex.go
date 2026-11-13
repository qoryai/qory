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
//	skills       qory links them there for a launch; ship it as skills/<name>
//	AGENTS.md    qory writes it for a launch
//	auth.json    Codex's login file, which no home of qory's has
//	hooks.json, .env, environments.toml, AGENTS.override.md, rules/read-access.rules
//	             qory writes them for every walled run
//
// Codex also takes its whole home from CODEX_HOME, and the runtime's directory is one:
// config.toml, the agents, the skills linked under skills/ and the instructions as
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
	"strings"

	"github.com/qoryai/qory/internal/compose"
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
const textAuthFile = "Remove `files/codex/auth.json` from module %s: a Codex run takes its model key from the run, not from a file."

// textWalledFile is the refusal of a module that ships a file of [walledFiles], with the
// file's path under files/codex for the first %s; the module's name is left as %%s.
const textWalledFile = "Remove `files/codex/%s` from module %%s: qory writes it for every walled Codex run."

// walledFiles are the paths under .codex that qory writes in the home of every walled
// Codex run, see [WriteWalledHome]: the hooks file, the variables Codex sets, the
// environments Codex starts, the instructions Codex reads ahead of AGENTS.md, and the
// read run's rules.
var walledFiles = []string{"hooks.json", ".env", "environments.toml", "AGENTS.override.md", "rules/" + ReadRulesName}

// Reserved are config.toml and the agents directory, which Render writes, the skills and
// AGENTS.md, which qory places for a launch, auth.json, Codex's login file, which no
// home of qory's has, and the [walledFiles].
func (codex) Reserved() []render.Reserved {
	out := []render.Reserved{
		{Path: "config.toml", Why: "qory writes it"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "skills", Why: "qory links them there for a launch; ship it as skills/<name>"},
		{Path: "AGENTS.md", Why: "qory writes it for a launch"},
		{Path: "auth.json", Refusal: textAuthFile},
	}
	for _, p := range walledFiles {
		out = append(out, render.Reserved{Path: p, Refusal: fmt.Sprintf(textWalledFile, p)})
	}
	return out
}

// Render writes config.toml, with the target model as model, the MCP servers as
// mcp_servers, one table per server containing the object as the module wrote it, and any
// other codex settings fragment without shell_environment_policy.set, whose variables are
// the launch's, see [EnvAt], then one agents/<name>.toml per agent containing the agent's
// name, description and its body as developer_instructions. The instructions written as
// AGENTS.md end with the names the session registers, see [render.Addressing]: a Codex
// home registers them as the modules wrote them, and the file states that. Codex reads a
// project .codex only in a project the user has marked trusted. A fragment that sets a
// key of [reservedKeys], or a hook number Codex cannot read, see [checkHookNumbers], is
// refused before anything is written.
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
	err := render.WriteSettings(res, Runtime, dir, home, []string{"config.toml"}, func(file string, m map[string]any) {
		if file != "config.toml" {
			return
		}
		if res.Stack.Target.Model != "" {
			m["model"] = res.Stack.Target.Model
		}
		render.PutServers(m, "mcp_servers", res.MCPFor(home))
		render.DropEnv(m, setKey)
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
		data, err := render.EncodeTOML(map[string]any{
			"name":                   name,
			"description":            doc.String("description"),
			"developer_instructions": strings.TrimRight(doc.Body, "\n"),
		})
		if err != nil {
			return err
		}
		if err := render.WriteFile(dir, filepath.Join("agents", e.Name+".toml"), data); err != nil {
			return err
		}
	}
	if err := render.PlaceEntries(res, dir, render.Bare, "skills"); err != nil {
		return err
	}
	if text := render.WithAddressing(res, render.Bare, codex{}.Skips()); text != "" {
		if err := render.WriteFile(dir, "AGENTS.md", []byte(text)); err != nil {
			return err
		}
	}
	return nil
}

// reservedKey is a key path of config.toml that a harness fragment may not set. Each
// segment is a pattern of [path.Match], so experimental_realtime_*_base_url covers every
// key of that form.
type reservedKey []string

// textReservedKey is the refusal of a fragment that sets a key of [reservedKeys], with
// the key as a dotted path for its %s.
const textReservedKey = "Remove `%s` from the harness's `settings/codex/config.toml`: a Codex run takes this key from qory and Forager, not from the harness."

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
// as hooks.Stop.
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
			return fmt.Errorf(textReservedKey, strings.Join(key, "."))
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
const textHookNumber = "Set `%s` of the `%s` hook in the harness's `settings/codex/%s` to a whole number of 0 or more: Codex ignores the whole file otherwise."

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

// Template starts Codex with the runtime's directory as its home.
func (codex) Template() render.Template {
	return render.Template{Command: "codex", Env: map[string]string{"CODEX_HOME": "${dir}"}}
}
