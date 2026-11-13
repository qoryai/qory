package codex

import "testing"

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
