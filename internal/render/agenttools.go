package render

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
)

// IsReviewAgent reports whether e is a member of a group role that the review_group of a
// workflow of the stack names: a reviewer of a review session, whose agent file runs no
// command, see [CommandGuard].
func IsReviewAgent(res *compose.Result, e compose.Entry) bool {
	if e.Kind != "agents" || res.Stack == nil {
		return false
	}
	for _, w := range res.Stack.Workflows {
		if w.ReviewGroup != "" && slices.Contains(res.Groups["agents/"+w.ReviewGroup], e.Name) {
			return true
		}
	}
	return false
}

// HasReviewSession reports whether a workflow of the stack of res has a review_group,
// so that a review session may start any agent of the harness.
func HasReviewSession(res *compose.Result) bool {
	if res.Stack == nil {
		return false
	}
	for _, w := range res.Stack.Workflows {
		if w.ReviewGroup != "" {
			return true
		}
	}
	return false
}

// CommandGuard is a runtime that can run a review session: it writes the agent file of a
// reviewer so that the agent cannot run a command, and it has the arguments and the
// settings that limit the session's main agent to the commands of a review. For a
// runtime that is not one, the agent file of a reviewer stops the build, and a review
// session stops before it starts. A run applies the settings only for a guard that is a
// [SettingsInliner] too, see [ReviewGuard]. A runtime adds its guard by implementing
// these methods in its package, with no change to shared code.
type CommandGuard interface {
	// CheckAgent returns an error, with the fix first, for a, an agent file as the module
	// wrote it, before qory edits it. It must stop every file that the runtime could read
	// in another way than qory does. It reads a.Source and parses the file no further.
	CheckAgent(a AgentCheck) error
	// CheckEntry returns an error, with the fix first, for d, a skill or a command of a
	// stack with a review session as the module wrote it, before qory edits it, when its
	// frontmatter may set a key that the check does not allow: with such a key, the
	// runtime can allow the review session a tool or a command past its settings. It
	// reads d.Source.
	CheckEntry(d EntryCheck) error
	// WithoutCommands returns data, the agent file of the reviewer name at path, written
	// so that the agent cannot run a command, or an error with the fix first. It must
	// fail closed: a file that it cannot write in that form stops the build.
	WithoutCommands(data []byte, path, name string) ([]byte, error)
	// ReviewArgs are the arguments that limit the tools of a review session's main
	// agent.
	ReviewArgs() []string
	// ReviewSettings returns settings, the settings of the harness as one JSON object,
	// as the JSON of the settings of a review session at p: its main agent may run only
	// the commands of a review, read only p.Read, edit only p.Write and never p.NoEdit,
	// and the runtime denies, with no prompt, every other command and edit. Every other
	// key of settings stays as it is. shown is the settings file as an error shows it,
	// "" for none. The error starts with the fix.
	ReviewSettings(settings map[string]any, shown string, p ReviewPlaces) ([]byte, error)
	// ProjectPaths are the paths, with forward slashes, that the runtime reads as a
	// project's configuration, such as its settings, skills, agents, commands and
	// servers, at the root of a folder that it reads and in any folder below it. A path
	// covers the file at it and everything under the folder at it, at any depth and in
	// any case: .claude covers sub/.claude/x and not .claude-notes. A review session
	// stops when the change that it reviews adds, changes or removes a file at one of
	// them, since the session could load the file.
	ProjectPaths() []string
}

// ReviewPlaces are the places of a review session that [CommandGuard.ReviewSettings]
// limits its main agent to.
type ReviewPlaces struct {
	// Home is the harness's home.
	Home string
	// Read are the folders that the session reads, in this order.
	Read []string
	// Write is the one folder that the session writes, one of Read.
	Write string
	// NoEdit are the folders that the session must never edit, such as the copy that it
	// reviews.
	NoEdit []string
}

// ReviewGuard returns rt as the [CommandGuard] of a review session, or false when rt is
// no CommandGuard or no [SettingsInliner]: a run passes the settings of
// [CommandGuard.ReviewSettings] with the flag of the SettingsInliner, so a guard without
// one cannot apply them.
func ReviewGuard(rt Runtime) (CommandGuard, bool) {
	guard, ok := rt.(CommandGuard)
	if _, inlines := rt.(SettingsInliner); !ok || !inlines {
		return nil, false
	}
	return guard, true
}

// EntryCheck is the file of a skill or a command that [CommandGuard.CheckEntry] checks.
type EntryCheck struct {
	// Data is the file as the module wrote it, and Source is Data split by [SplitAgent].
	Data   []byte
	Source AgentSource
	// Path is the file's path, and Kind is what an error calls the file: [SkillFile] or
	// [CommandFile].
	Path, Kind string
}

// The kinds of an [EntryCheck].
const (
	SkillFile   = "skill"
	CommandFile = "command"
)

// AgentCheck is an agent file that [CommandGuard.CheckAgent] checks.
type AgentCheck struct {
	// Data is the file as the module wrote it, and Source is Data split by [SplitAgent].
	Data   []byte
	Source AgentSource
	// Path is the file's path, and Name the name of its entry.
	Path, Name string
	// Review is whether the agent is a reviewer of a review session, see
	// [IsReviewAgent], and ReviewSession whether the stack has a review session, see
	// [HasReviewSession].
	Review, ReviewSession bool
}

// ReviewerAgent is what an error calls the file of a reviewer, and AgentFile any other.
const (
	ReviewerAgent = "reviewer agent"
	AgentFile     = "agent"
)

// AgentSource is an agent file split at its frontmatter.
type AgentSource struct {
	// Front is the frontmatter between the fences as written, "" with HasFront false.
	Front    string
	HasFront bool
	// Rest is everything after the closing fence's line, as written.
	Rest string
	// Keys is the frontmatter as [module.ParseDocument] decodes it.
	Keys map[string]any
}

// MaxAgentFront is the largest frontmatter of an agent file that qory reads, see
// [module.MaxAgentFront]. The compose stops a larger one first.
const MaxAgentFront = module.MaxAgentFront

// SplitAgent splits data, and returns the error of [module.ParseDocument], as the fix of
// the file of the kind kind, for data whose frontmatter does not parse. A frontmatter of
// more than [MaxAgentFront] bytes stops it before the parse. A byte order mark is
// dropped, as Claude Code does.
func SplitAgent(data []byte, path, kind string) (AgentSource, error) {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	text := string(data)
	src, closed := AgentSource{Rest: text}, true
	if first, _, _ := strings.Cut(text, "\n"); strings.TrimSuffix(first, "\r") == "---" {
		closed = false
		start := len(first) + 1
		for pos := start; pos < len(text) && !closed; {
			line, next := text[pos:], len(text)
			if i := strings.IndexByte(line, '\n'); i >= 0 {
				line, next = line[:i], pos+i+1
			}
			if strings.TrimRight(line, " \t\r") == "---" {
				src = AgentSource{Front: strings.TrimSuffix(text[start:pos], "\n"), HasFront: true, Rest: text[next:]}
				closed = true
			}
			pos = next
		}
	}
	if n := len(module.FrontText(data)); n > MaxAgentFront {
		return AgentSource{}, fmt.Errorf("Shorten the frontmatter of the %s %s to 64 KiB or less, then run again\n"+
			"  The frontmatter has %d bytes", kind, exports.HidePath(path), n)
	}
	doc, err := module.ParseDocument(data, path)
	if err != nil {
		return AgentSource{}, fmt.Errorf("Fix the frontmatter of the %s %s, then run again\n  %w", kind, exports.HidePath(path), err)
	}
	if !closed {
		return AgentSource{}, fmt.Errorf("Fix the frontmatter of the %s %s, then run again\n  the frontmatter is not closed", kind, exports.HidePath(path))
	}
	// [module.ParseDocument] drops a second byte order mark, and Claude Code does not.
	if src.HasFront {
		src.Keys = doc.Front
	}
	return src, nil
}

// CheckFence returns an error when the first line of data is the frontmatter fence with
// spaces or tabs after it: [module.ParseDocument] and [module.SetFront] read only a bare
// fence as the start of frontmatter, so they would treat the file as having none. kind is
// what the error calls the file.
func CheckFence(data []byte, path, kind string) error {
	first, _, _ := strings.Cut(strings.TrimPrefix(string(data), "\ufeff"), "\n")
	first = strings.TrimSuffix(first, "\r")
	if first != "---" && strings.TrimRight(first, " \t") == "---" {
		return fmt.Errorf("Remove the spaces after --- on line 1 of the %s %s, then run again\n"+
			"  qory reads the frontmatter only when line 1 is exactly ---", kind, exports.HidePath(path))
	}
	return nil
}
