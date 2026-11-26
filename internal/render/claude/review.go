package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/render"
)

// reviewTools are the tools of a review session's main agent: Read, Grep and Glob read
// the checkout, Skill loads the workflow's skill, Write writes the report, Agent starts
// the reviewer agents of the skill, and Bash runs the commands that [reviewCommands]
// allows. It has no edit tool. The agent files of the reviewers have no Bash in tools,
// and Bash and Agent first in disallowedTools, see [WithoutBash]. Agent can also start an
// agent that is no reviewer, such as a built-in agent of Claude Code, and this list does
// not limit the tools of that agent.
//
// shortcut: cmd/reviewwall_docker_test.go shows the agent files and the locked copy in a
// real wall with a shell stand-in, not Claude Code, so no test shows that a reviewer
// agent cannot run a command; the first run on the cloud shows it.
var reviewTools = []string{"Read", "Grep", "Glob", "Skill", "Write", "Agent", "Bash"}

// ProjectPaths are .claude, the folder of a project's settings, skills, agents, commands,
// output styles, hooks and rules; .mcp.json, the file of its MCP servers; and the
// instruction files CLAUDE.md, CLAUDE.local.md and AGENTS.md, which Claude Code 2.1.332
// loads as project instructions, AGENTS.md by default where the project has no CLAUDE.md.
func (claude) ProjectPaths() []string {
	return []string{".claude", ".mcp.json", "CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}
}

// ReviewArgs are --tools with [reviewTools].
func (claude) ReviewArgs() []string {
	return []string{"--tools", strings.Join(reviewTools, ",")}
}

// reviewChecksScript is the checks script of the skill running-a-workflow in the plugin
// of the harness home: the path that the skill's commands start with when the runtime
// states the skill's folder, and the path of the composed rule in the harness's core
// settings.
func reviewChecksScript(home string) string {
	return filepath.Join(home, Runtime, Plugin, "skills", "running-a-workflow", "checks.cjs")
}

// reviewCommands are the Bash rules that the main agent of a review session has: the
// verdict command, the work item's show command and the artifact pull command of the qory
// in the wall, and the checks script, whose subcommand end the runtime's stop hook runs.
// It has no rule for the other commands of work-item and artifact, nor for destination.
// Each rule is a prefix with a trailing " *", which Claude Code matches as the prefix and
// one command: a command joined to it with ;, &&, || or | needs another rule.
// permissions.defaultMode dontAsk, which [ReviewSettings] sets, denies every command that
// no rule allows.
//
// shortcut: Claude Code runs its built-in read-only commands, such as cat, ls and grep,
// with no rule in every mode, so dontAsk does not deny those, see readRules in cmd.
// shortcut: no rule for end beyond the checks script: the only end command of the
// skills is checks.cjs end, and qory end is a starter's verb, not a session's.
func reviewCommands(home string) []string {
	return []string{
		"Bash(" + wall.HelperPath + " run verdict *)",
		"Bash(" + wall.HelperPath + " work-item show *)",
		"Bash(" + wall.HelperPath + " artifact pull *)",
		"Bash(" + reviewChecksScript(home) + " *)",
	}
}

// ruleGlob escapes the characters of a path that a rule reads as a gitignore pattern. On
// Claude Code 2.1.316 a rule matches a backslash only when it is written as four, and an
// escaped ? matches nothing, so ? stays a pattern: a deny then also covers a path with
// any other character there.
var ruleGlob = strings.NewReplacer(`\`, `\\\\`, `*`, `\*`, `[`, `\[`, `]`, `\]`)

// pathRule is the rule of tool for every file under dir: an absolute path starts with //.
func pathRule(tool, dir string) string {
	return tool + "(/" + ruleGlob.Replace(dir) + "/**)"
}

// ReviewSettings is settings, Claude Code's settings, as JSON with these rules added,
// each once, and every other key as it is, with < > & unescaped:
//
//   - permissions.deny: NotebookEdit, and an Edit rule for each folder of p.NoEdit. A
//     deny wins over any allow that the composed settings contain.
//   - permissions.allow: [reviewCommands] of p.Home, a Read rule for each folder of
//     p.Read, and an Edit rule for p.Write alone. An Edit rule covers the Write tool too.
//   - permissions.additionalDirectories: p.Read, so that the session can read them under
//     permissions.blockReadsOutsideWorkingDirectories, which a composed module can set.
//   - permissions.defaultMode: dontAsk, which denies a command or an edit that no rule
//     allows, without a prompt.
//   - disableSkillShellExecution: true, also over a false in settings. Claude Code
//     2.1.332 runs the !`command` lines of a skill through the Bash rules above, also for
//     an agent with no Bash, such as a reviewer that loads a skill: disallowedTools only
//     removes tools and adds no deny rule.
//   - disableBundledSkills: true, so the session has no skill that Claude Code ships,
//     only the harness's skills, which the build checks.
//   - disableAutoMode and permissions.disableBypassPermissionsMode: "disable", so no
//     mode of the session runs a tool that no rule allows.
//
// Each key is set over the value that settings has. A home with a * stops it, because a
// rule reads the * as any text. An empty p.Write or folder of p.Read stops it, because its
// rule would cover every file. A permissions that is no object, and an allow, a deny or an
// additionalDirectories that is no array, stop it.
func (claude) ReviewSettings(settings map[string]any, shown string, p render.ReviewPlaces) ([]byte, error) {
	if strings.Contains(p.Home, "*") {
		return nil, fmt.Errorf("remove the * from the harness's home %s: a Bash rule reads it as any text, so the run does not start", p.Home)
	}
	if p.Write == "" || slices.Contains(p.Read, "") {
		return nil, errors.New("give each review folder a path: an empty one would cover every file, so the run does not start")
	}
	deny := []string{"NotebookEdit"}
	for _, dir := range p.NoEdit {
		if rule := pathRule("Edit", dir); !slices.Contains(deny, rule) {
			deny = append(deny, rule)
		}
	}
	allow := reviewCommands(p.Home)
	for _, dir := range p.Read {
		allow = append(allow, pathRule("Read", dir))
	}
	allow = append(allow, pathRule("Edit", p.Write))
	settings["disableSkillShellExecution"] = true
	settings["disableBundledSkills"] = true
	settings["disableAutoMode"] = "disable"
	return withRules(settings, shown, deny, allow, p.Read)
}

// VerifySettings is settings, Claude Code's settings, as JSON with a Read rule and an Edit
// rule for reports added to permissions.allow, and reports to
// permissions.additionalDirectories, each once, with < > & unescaped. An Edit rule covers
// the Write tool too. Nothing else changes: the session writes its checkout, so it sets no
// defaultMode and no deny. An empty reports, a permissions that is no object, and an
// allow or an additionalDirectories that is no array, stop it.
func (claude) VerifySettings(settings map[string]any, shown, reports string) ([]byte, error) {
	if reports == "" {
		return nil, errors.New("set the Verify reports path: an empty one covers all files, so the run does not start")
	}
	if _, err := addRules(settings, shown, "a Verify session", []ruleList{
		{"allow", []string{pathRule("Read", reports), pathRule("Edit", reports)}},
		{"additionalDirectories", []string{reports}},
	}); err != nil {
		return nil, err
	}
	return encode(settings)
}

// withRules is object, Claude Code's settings, as JSON with deny, allow and dirs added to
// permissions.deny, permissions.allow and permissions.additionalDirectories, each rule
// once, permissions.defaultMode set to dontAsk and
// permissions.disableBypassPermissionsMode to disable. shown is the settings file as an
// error shows it, "" for none.
func withRules(object map[string]any, shown string, deny, allow, dirs []string) ([]byte, error) {
	permissions, err := addRules(object, shown, "a run of a read workflow",
		[]ruleList{{"deny", deny}, {"allow", allow}, {"additionalDirectories", dirs}})
	if err != nil {
		return nil, err
	}
	permissions["defaultMode"] = "dontAsk"
	permissions["disableBypassPermissionsMode"] = "disable"
	return encode(object)
}

// ruleList is the rules that a session adds to the list permissions.<key> of the settings.
type ruleList struct {
	key   string
	rules []string
}

// addRules adds each list of adds to the permissions of object, Claude Code's settings,
// each rule once, creating the permissions or the list when it is missing, and returns
// the permissions. Every other key stays as it is. who is the session that adds the rules,
// as an error shows it, and shown is the settings file as an error shows it, "" for none.
// A permissions that is no object, and a list that is no array, stop it.
func addRules(object map[string]any, shown, who string, adds []ruleList) (map[string]any, error) {
	at := "the Claude Code settings " + shown
	if shown == "" {
		at = "the Claude Code settings"
	}
	permissions := map[string]any{}
	if v, ok := object["permissions"]; ok {
		if permissions, ok = v.(map[string]any); !ok {
			return nil, fmt.Errorf("set permissions in %s to a JSON object: %s adds rules to it, so the run does not start", at, who)
		}
	}
	for _, add := range adds {
		var list []any
		if v, ok := permissions[add.key]; ok {
			if list, ok = v.([]any); !ok {
				return nil, fmt.Errorf("set permissions.%s in %s to a JSON array: %s adds rules to it, so the run does not start", add.key, at, who)
			}
		}
		for _, rule := range add.rules {
			if !slices.Contains(list, any(rule)) {
				list = append(list, rule)
			}
		}
		permissions[add.key] = list
	}
	object["permissions"] = permissions
	return permissions, nil
}

// encode is object as indented JSON, with < > & unescaped.
func encode(object map[string]any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(object); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
