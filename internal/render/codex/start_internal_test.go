package codex

import (
	"slices"
	"testing"
)

// TestMentionByPath is each form of a prompt: one that starts with $<name> of a skill
// mentions the skill by its path in the walled home, and a $<name> that is no skill, a
// plugin's name, a bare $, a name followed by no space and a prompt with no $ stay as
// given.
func TestMentionByPath(t *testing.T) {
	for _, c := range []struct{ prompt, want string }{
		{"$explore x", "[$explore](/var/lib/qory-codex-home/skills/explore/SKILL.md) x"},
		{"$workflow-explore", "[$workflow-explore](/var/lib/qory-codex-home/skills/workflow-explore/SKILL.md)"},
		{"$pack:review x", "$pack:review x"},
		{"$ x", "$ x"},
		{"$explore,x", "$explore,x"},
		{"$HOME is unset", "$HOME is unset"},
		{"explore x", "explore x"},
	} {
		isSkill := func(name string) bool { return name == "explore" || name == "workflow-explore" }
		if got := mentionByPath(c.prompt, isSkill); got != c.want {
			t.Errorf("mentionByPath(%q) = %q, want %q", c.prompt, got, c.want)
		}
	}
}

// TestStartDirs is each form of -C and --cd: every value before -- is returned in order,
// in the forms -C <dir>, -C<dir>, -C=<dir>, --cd <dir> and --cd=<dir>, before exec and
// after it; a value after --, an empty value and a flag with no argument after it add
// none.
func TestStartDirs(t *testing.T) {
	for _, c := range []struct {
		args, want []string
	}{
		{[]string{"exec", "-C", "a", "-Cb", "-C=c", "--cd", "d", "--cd=e", "x"}, []string{"a", "b", "c", "d", "e"}},
		{[]string{"-C", "/abs", "exec", "--cd", "../up", "x"}, []string{"/abs", "../up"}},
		{[]string{"exec", "-C=", "--cd=", "--", "-C", "after", "x"}, nil},
		{[]string{"exec", "x", "-C"}, nil},
		{[]string{"exec", "--cdx", "--add-dir", "y", "x"}, nil},
	} {
		if got := StartDirs(c.args); !slices.Equal(got, c.want) {
			t.Errorf("StartDirs(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}
