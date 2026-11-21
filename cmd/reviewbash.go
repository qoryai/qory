package cmd

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/render/claude"
)

// reviewChecksScript is the checks script of the skill running-a-workflow in the plugin
// of the harness home: the path that the skill's commands start with when the runtime
// states the skill's folder, and the path of the composed rule in the harness's core
// settings.
func reviewChecksScript(home string) string {
	return filepath.Join(home, claude.Runtime, claude.Plugin, "skills", "running-a-workflow", "checks.cjs")
}

// reviewBashAllow are the Bash rules that the main agent of a review session needs
// beyond [readAllow]: the verdict command of the qory in the wall, and the checks script,
// whose subcommand end the runtime's stop hook runs. Each rule is a prefix with a
// trailing " *", which Claude Code matches as the prefix and one command: a command
// joined to it with ;, &&, || or | needs a rule of its own. permissions.defaultMode
// dontAsk, set by [readRules], denies every command that no rule allows.
//
// shortcut: Claude Code runs its built-in read-only commands, such as cat, ls and grep,
// with no rule in every mode, so dontAsk does not deny those, see [readRules].
// shortcut: no rule for end beyond the checks script: the only end command of the
// skills is checks.cjs end, and qory end is a starter's verb, not a session's.
func reviewBashAllow(home string) []string {
	return []string{
		"Bash(" + wall.HelperPath + " run verdict *)",
		"Bash(" + reviewChecksScript(home) + " *)",
	}
}

// reviewRules is [readRules] for the settings of a review session: object, Claude Code's
// settings, gets [reviewBashAllow] of home in permissions.allow, each once, then the
// rules of a read run. A home with a * stops the run, because a rule reads the * as any
// text. A permissions or allow that is no object or no array is left for [readRules] to
// refuse.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func reviewRules(object map[string]any, shown, root, home string) ([]byte, error) {
	if strings.Contains(home, "*") {
		return nil, input(fmt.Errorf("remove the * from the harness's home %s: a Bash rule reads it as any text, so the run does not start", home))
	}
	permissions, ok := object["permissions"].(map[string]any)
	if !ok && object["permissions"] == nil {
		permissions, ok = map[string]any{}, true
		object["permissions"] = permissions
	}
	if ok {
		list, isList := permissions["allow"].([]any)
		if isList || permissions["allow"] == nil {
			for _, rule := range reviewBashAllow(home) {
				if !slices.Contains(list, any(rule)) {
					list = append(list, rule)
				}
			}
			permissions["allow"] = list
		}
	}
	return readRules(object, shown, root)
}
