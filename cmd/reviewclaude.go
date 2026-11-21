package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/qoryai/qory/internal/render"
)

// reviewerTools are the only tools a Claude Code reviewer has: Read, Grep and Glob read
// its copy, Skill loads the skills of the review, and Write writes its report. It has no
// shell, no network tool and no tool that starts an agent.
var reviewerTools = []string{"Read", "Grep", "Glob", "Skill", "Write"}

// noMCP is the MCP list of a reviewer: no server.
const noMCP = `{"mcpServers":{}}`

// claudeReviewLaunch is what [claudeReviewArgs] builds the launch of a reviewer from.
type claudeReviewLaunch struct {
	// Plugin is the absolute path of the plugin directory of the composed harness, the
	// claude/plugin folder of the home, which has the reviewer as an agent.
	Plugin string
	// Settings is the absolute path of the home's claude/settings.json, which has the
	// hooks of the tool guard.
	Settings string
	// Reviewer is the agent's name in the plugin, such as code-reviewer.
	Reviewer string
	// Model is the model of the reviewer's tier, "" to leave the agent's model.
	Model string
}

// claudeReviewArgs are the arguments that start Claude Code on the reviewer in l as the
// main agent of the session, with the tools of [reviewerTools] alone: the plugin of the
// harness for this session, the agent as <plugin>:<reviewer>, the tools, no MCP server and
// the settings of the user and of l.Settings. They replace the arguments of the runtime's
// launch template, which loads the harness's MCP list. The prompt and the folders
// that Claude Code reads are the caller's, see render.Headless. It returns an error that
// starts with the fix for a path that is not absolute or a reviewer name of another form.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func claudeReviewArgs(l claudeReviewLaunch) ([]string, error) {
	if !reviewerNameForm.MatchString(l.Reviewer) {
		return nil, fmt.Errorf("Name each reviewer with lower-case letters, digits and hyphens.\nThe reviewer is %s.", strconv.Quote(l.Reviewer))
	}
	for _, p := range []struct{ what, path string }{{"plugin", l.Plugin}, {"settings", l.Settings}} {
		if !cleanAbs(p.path) {
			return nil, fmt.Errorf("Give the reviewer launch an absolute, clean %s path.\nThe path is %s.", p.what, strconv.Quote(p.path))
		}
	}
	args := []string{
		"--plugin-dir", l.Plugin,
		"--settings", l.Settings,
		"--agent", render.PluginName + ":" + l.Reviewer,
		"--tools", strings.Join(reviewerTools, ","),
		"--strict-mcp-config", "--mcp-config", noMCP,
		"--setting-sources", "user",
	}
	if l.Model != "" {
		args = append(args, "--model", l.Model)
	}
	return args, nil
}
