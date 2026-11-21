package cmd

import (
	"slices"
	"strings"
	"testing"
)

func TestClaudeReviewArgs(t *testing.T) {
	l := claudeReviewLaunch{Plugin: "/h/claude/plugin", Settings: "/h/claude/settings.json", Reviewer: "code-reviewer"}
	args, err := claudeReviewArgs(l)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--plugin-dir", "/h/claude/plugin", "--settings", "/h/claude/settings.json",
		"--agent", "harness:code-reviewer", "--tools", "Read,Grep,Glob,Skill,Write",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "user",
	}
	if !slices.Equal(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
	l.Model = "model-b"
	args, err = claudeReviewArgs(l)
	if err != nil || !slices.Equal(args, append(want, "--model", "model-b")) {
		t.Errorf("with a model: args = %q, error %v", args, err)
	}
}

func TestClaudeReviewToolsHaveNoShellNoNetworkNoAgent(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "WebFetch", "WebSearch", "Task", "Agent", "NotebookEdit"} {
		if slices.Contains(reviewerTools, tool) {
			t.Errorf("a reviewer has the tool %s", tool)
		}
	}
}

func TestClaudeReviewArgsRefuses(t *testing.T) {
	ok := claudeReviewLaunch{Plugin: "/h/claude/plugin", Settings: "/h/claude/settings.json", Reviewer: "code-reviewer"}
	for _, c := range []struct {
		name   string
		change func(*claudeReviewLaunch)
		want   string
	}{
		{"name", func(l *claudeReviewLaunch) { l.Reviewer = "../x" }, "Name each reviewer"},
		{"name with a colon", func(l *claudeReviewLaunch) { l.Reviewer = "a:b" }, "Name each reviewer"},
		{"relative plugin", func(l *claudeReviewLaunch) { l.Plugin = "plugin" }, "clean plugin path"},
		{"unclean settings", func(l *claudeReviewLaunch) { l.Settings = "/h/../settings.json" }, "clean settings path"},
	} {
		l := ok
		c.change(&l)
		args, err := claudeReviewArgs(l)
		if err == nil || args != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: args %q, error %v, want %q", c.name, args, err, c.want)
		}
	}
}
