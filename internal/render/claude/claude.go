// Package claude renders for Claude Code, which reads a project's .claude directory: the
// skills, agents, commands, output styles and hooks under it, its settings.json, and the
// rules under .claude/rules, and .mcp.json at the checkout root for the MCP servers. The
// checkout gets .claude, a real directory containing one link per file of the runtime's
// directory in the home, so Claude Code's settings.local.json stays beside them;
// .claude/rules, a real directory in the same way, so a repository's rules stay beside
// qory's; and a soft .mcp.json when the compose contains a server. The target model is
// written into settings.json as model. Claude Code has a place for every entry kind, so
// nothing is skipped, and a files entry named claude/<path> lands at .claude/<path>,
// which is how a module ships .claude/rules/nextjs-15.md.
//
// qory writes no file named CLAUDE.md. The instructions reach a session in the checkout
// as the rule rules/qory-harness.md, a link to the home's AGENTS.md, which every runtime
// reads. Claude Code loads a rule without paths frontmatter at launch, beside a
// repository's CLAUDE.md, and a rule does not hide a repository's AGENTS.md the way a
// .claude/CLAUDE.md does.
//
// Claude Code also takes a harness from outside the checkout, and the runtime's directory
// contains the launch spec for that: plugin/ is a plugin in Claude Code's own layout, the
// skills, agents, commands and output styles under a .claude-plugin/plugin.json, which
// --plugin-dir loads for one session; settings.json goes to --settings, mcp.json to
// --mcp-config and launch/AGENTS.md to --append-system-prompt-file. [Template] contains
// those arguments, and neither the plugin nor the launch directory is linked into the
// checkout. The plugin's agents are copies where everything else in the home is a link,
// or a copy for a module that lies in the checkout, see [render.WritePlugin]: Claude
// Code passes over a link in a plugin's agents directory, and registers a file there as
// harness:<name>. The launch line adds to the session the program was started in and
// takes nothing over: whatever configuration directory, login and memory the launcher
// has stay its own.
//
// The two paths register the same entries under different names, and that is why the
// instructions are written twice. A session reading the checkout's .claude finds its
// agents in .claude/agents and its skills in .claude/skills, under the names the
// modules wrote; a session started on the plugin finds every one of them as
// harness:<name>. The home's AGENTS.md, linked into the checkout as a rule, resolves a
// reference to the bare name; launch/AGENTS.md, read at launch alone, resolves it to the
// plugin's and ends with the names the session registers, see [render.Addressing]. The
// documents in the plugin resolve to the plugin's names as well, so a skill that
// dispatches an agent refers to the one the session has.
//
// The paths under .claude a files entry may not take, see [render.Reserved]:
//
//	CLAUDE.md              qory adds no CLAUDE.md to a checkout; ship the instructions as AGENTS.md at the module root
//	AGENTS.md              qory adds no AGENTS.md under .claude; ship the instructions as AGENTS.md at the module root
//	rules/qory-harness.md  qory links the instructions there
//	rules                  the path alone: rules is a directory qory links one file at a time; ship each rule as rules/<name>
//	settings.json          qory writes it
//	mcp.json               qory writes it
//	plugin                 qory writes the plugin there
//	launch                 qory writes the launch instructions there
//	settings.local.json    Claude Code reads it as settings; a module sets those through settings/claude/settings.json
//	skills                 skills are linked there; ship it as skills/<name>
//	agents                 agents are linked there; ship it as agents/<name>
//	commands               commands are linked there; ship it as commands/<name>
//	hooks                  hooks are linked there; ship it as hooks/<name>
//	output-styles          output-styles are linked there; ship it as output-styles/<name>
package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/modelplace"
	"github.com/qoryai/qory/internal/render"
)

// Runtime is the target.runtime value for Claude Code, and the name of its directory in
// the home.
const Runtime = "claude"

// Plugin is the plugin's directory under the runtime's directory in the home, the value
// of --plugin-dir.
const Plugin = "plugin"

// Launch is the directory under the runtime's directory that contains what a launch reads
// and a checkout must not: AGENTS.md with the plugin's names, the file
// --append-system-prompt-file reads.
const Launch = "launch"

// Rules is the directory under the runtime's directory that the checkout links as
// .claude/rules, one link per file, beside the rules a repository keeps there.
const Rules = "rules"

// HarnessRule is the rule under [Rules] that links to the home's AGENTS.md, so a session
// in the checkout loads the instructions as a rule.
const HarnessRule = "qory-harness.md"

// claude implements [render.Runtime] for Claude Code.
type claude struct{}

func init() { render.Register(claude{}) }

// Name is the target.runtime value.
func (claude) Name() string { return Runtime }

// Links are the hard link .claude, a directory link so the checkout's files in it stay,
// without the plugin and the launch directory, which a checkout has no use for and whose
// names are wrong for a session in it, and without the rules; the hard link .claude/rules,
// a second directory link so the rules a repository keeps there stay, left out when the
// runtime writes no rule; and a soft .mcp.json at the checkout root, left out when the
// runtime writes no mcp.json, see [Render].
func (claude) Links(res *compose.Result) []render.Link {
	links := []render.Link{{Checkout: ".claude", Home: Runtime, Except: []string{Plugin, Launch, Rules}}}
	if res == nil || writesRules(res) {
		links = append(links, render.Link{Checkout: ".claude/" + Rules, Home: Runtime + "/" + Rules})
	}
	if res == nil || writesMCP(res) {
		links = append(links, render.Link{Checkout: ".mcp.json", Home: Runtime + "/mcp.json", Soft: true})
	}
	return links
}

// writesMCP reports whether the runtime writes mcp.json for this compose: when the compose
// holds a server, or a module ships a settings/claude/mcp.json fragment.
func writesMCP(res *compose.Result) bool {
	return len(res.MCP) > 0 || res.Settings[Runtime]["mcp.json"] != nil
}

// writesRules reports whether the home has a rules directory for this compose: when the
// compose produced instructions, which [Render] links as [HarnessRule], or a module ships
// a files entry under claude/rules/.
func writesRules(res *compose.Result) bool {
	if res.Instructions != "" {
		return true
	}
	return slices.ContainsFunc(res.Entries, func(e compose.Entry) bool {
		path, ok := render.FileFor(e, Runtime)
		return ok && strings.HasPrefix(path, Rules+"/")
	})
}

// Skips is empty: Claude Code has a place for every kind.
func (claude) Skips() []string { return nil }

// Reserved are CLAUDE.md and AGENTS.md, which qory does not write here and a module may
// not add, the rule and the two files Render writes, the rules directory as a file, the plugin and launch directories,
// the settings file Claude Code writes and reads beside them, and the five kind
// directories Render links.
func (claude) Reserved() []render.Reserved {
	return []render.Reserved{
		{Path: "CLAUDE.md", Why: "qory adds no CLAUDE.md to a checkout; ship the instructions as AGENTS.md at the module root"},
		{Path: "AGENTS.md", Why: "qory adds no AGENTS.md under .claude; ship the instructions as AGENTS.md at the module root"},
		{Path: Rules + "/" + HarnessRule, Why: "qory links the instructions there"},
		{Path: Rules, Why: "rules is a directory qory links one file at a time; ship each rule as rules/<name>", Exact: true},
		{Path: "settings.json", Why: "qory writes it"},
		{Path: "mcp.json", Why: "qory writes it"},
		{Path: Plugin, Why: "qory writes the plugin there"},
		{Path: Launch, Why: "qory writes the launch instructions there"},
		{Path: "settings.local.json", Why: "Claude Code reads it as settings; a module sets those through settings/claude/settings.json"},
		{Path: "skills", Why: "skills are linked there; ship it as skills/<name>"},
		{Path: "agents", Why: "agents are linked there; ship it as agents/<name>"},
		{Path: "commands", Why: "commands are linked there; ship it as commands/<name>"},
		{Path: "hooks", Why: "hooks are linked there; ship it as hooks/<name>"},
		{Path: "output-styles", Why: "output-styles are linked there; ship it as output-styles/<name>"},
	}
}

// Render places every atomic kind for the checkout's bare names, writes settings.json
// with the model and without env, whose variables are the launch's, see [EnvAt],
// mcp.json with the MCP servers under mcpServers on top of any settings/claude/mcp.json
// fragment, and the rule [HarnessRule], a relative link to the home's AGENTS.md, whose
// references [render.BuildAt] resolved to the bare names, see [writeRule]. The rule is a
// link and has no @AGENTS.md import: Claude Code resolves a link to its real path and
// treats an import found through it as external, which it shows an approval prompt for
// on every start. Then it writes the plugin, see [Plugin], and the launch instructions, see [Launch].
func (claude) Render(res *compose.Result, dir, home string) error {
	if err := render.PlaceEntries(res, dir, render.Bare, "skills", "agents", "commands", "output-styles", "hooks"); err != nil {
		return err
	}
	ensure := []string{"settings.json"}
	if writesMCP(res) {
		ensure = append(ensure, "mcp.json")
	}
	err := render.WriteSettings(res, Runtime, dir, home, ensure, func(file string, m map[string]any) {
		if file == "mcp.json" {
			render.PutServers(m, "mcpServers", res.MCPFor(home))
		}
		if file != "settings.json" {
			return
		}
		render.DropEnv(m, []string{"env"})
		if res.Stack.Target.Model != "" {
			m["model"] = res.Stack.Target.Model
		}
	})
	if err != nil {
		return err
	}
	if err := writeRule(res, dir); err != nil {
		return err
	}
	if text := render.WithAddressing(res, render.PluginAddress, nil); text != "" {
		if err := render.WriteFile(dir, filepath.Join(Launch, "AGENTS.md"), []byte(text)); err != nil {
			return err
		}
	}
	return renderPlugin(res, filepath.Join(dir, Plugin))
}

// writeRule writes [HarnessRule] when the compose produced instructions: a relative link
// to the home's AGENTS.md, or, in the home of a hidden harness, which contains no link, a
// copy of the same text.
func writeRule(res *compose.Result, dir string) error {
	if res.Instructions == "" {
		return nil
	}
	name := filepath.Join(Rules, HarnessRule)
	if slices.ContainsFunc(res.Modules, func(m compose.Module) bool { return m.Hidden }) {
		return render.WriteFile(dir, name, []byte(res.Substitute(res.Instructions, render.Bare)))
	}
	if err := os.MkdirAll(filepath.Join(dir, Rules), 0o755); err != nil {
		return err
	}
	return os.Symlink(filepath.Join("..", "..", "AGENTS.md"), filepath.Join(dir, name))
}

// EnvAt is settings.json's env: what a fragment sets there is the launch's, a default
// each, and the file holds no variable.
func (claude) EnvAt() (string, []string) { return "settings.json", []string{"env"} }

// Address is the plugin's: every kind registers as harness:<name> on the launch path.
func (claude) Address() render.Address { return render.PluginAddress }

// renderPlugin writes the plugin into dir: .claude-plugin/plugin.json naming it, and the
// skills, agents and commands under the directories Claude Code reads in a plugin, with
// the output styles under output-styles and the manifest pointing there when the compose
// holds one. The manifest names no agents key: Claude Code discovers agents/ on its own,
// and a manifest that names the directory turns the discovery off. The hooks, the servers, the settings and the instructions are not
// in the plugin: they reach the session through the settings and files [Template] names,
// the same ones the checkout's links point at, so nothing is rendered twice. A plugin of
// a home that holds a copy of a module is refused past 10,000 entries, see
// [render.CheckPluginSize], so a walled launch does not refuse it later.
func renderPlugin(res *compose.Result, dir string) error {
	extra := map[string]any{}
	for _, e := range res.Entries {
		if e.Kind == "output-styles" {
			extra["outputStyles"] = "./output-styles"
		}
	}
	if extra["outputStyles"] != nil {
		if err := render.PlaceEntries(res, dir, render.PluginAddress, "output-styles"); err != nil {
			return err
		}
	}
	if err := render.WritePlugin(res, dir, ".claude-plugin", extra); err != nil {
		return err
	}
	if render.Copies(res) {
		return render.CheckPluginSize(dir, slices.ContainsFunc(res.Modules, func(m compose.Module) bool { return m.Walled || m.Hidden }))
	}
	return nil
}

// ModelVariables are the environment variables Claude Code ranks above the model and
// effort qory writes: the main model, the aliases' models, the agents' model, the
// workflow agents' model, the agents' effort and the effort. The exception is
// CLAUDE_CODE_SUBAGENT_EFFORT: it replaces only the effort an agent inherits, and an
// agent file's effort ranks above it. A compose for claude refuses each place that sets
// one for the session.
var ModelVariables = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE", "CLAUDE_CODE_EFFORT_LEVEL",
	"CLAUDE_CODE_SUBAGENT_EFFORT", "CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL",
}

// AgentEfforts are the effort values a Claude Code agent file takes in its effort
// frontmatter, as the agent place of the effort in Forager's descriptor for the runtime
// lists them; nil when the descriptor has none. The effortLevel setting does not take
// max; an agent file does.
func (claude) AgentEfforts() []string {
	p, ok := modelplace.AgentEffort(Runtime)
	if !ok {
		return nil
	}
	return p.Values
}

// Egress is the host Claude Code's program reaches for the model: the API. The
// telemetry and the MCP proxy hosts it also tries are not declared; a policy that does
// not cover them denies them, and a session is unaffected.
func (claude) Egress() []string { return []string{"api.anthropic.com"} }

// Template starts Claude Code with the harness in a home: the plugin for the skills,
// agents, commands and output styles, settings.json for the permissions, the hooks and
// the model, mcp.json for the servers when the compose wrote one,
// launch/AGENTS.md appended to the system prompt when the compose produced instructions
// or a roster to name, and the setting sources cut to the user's, so no .claude of the
// checkout or a directory above it is read.
func (claude) Template() render.Template {
	return render.Template{
		Command: "claude",
		Args: [][]string{
			{"--plugin-dir", "${dir}/" + Plugin},
			{"--settings", "${dir}/settings.json"},
			{"--mcp-config", "${dir}/mcp.json"},
			{"--append-system-prompt-file", "${dir}/" + Launch + "/AGENTS.md"},
			{"--setting-sources", "user"},
		},
	}
}
