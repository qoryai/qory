package cmd

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"
)

// ruleAllows is Claude Code's match of a Bash rule "Bash(<prefix> *)" with a command that
// has no shell operator: the prefix alone, or the prefix and a space.
func ruleAllows(rule, command string) bool {
	prefix, ok := strings.CutSuffix(strings.TrimPrefix(rule, "Bash("), " *)")
	return ok && (command == prefix || strings.HasPrefix(command, prefix+" "))
}

func TestReviewRulesAllowThreeCommandsAndDenyTheRest(t *testing.T) {
	const home = "/home/run/harness"
	const checks = home + "/claude/plugin/skills/running-a-workflow/checks.cjs"
	// The composed settings of the harness already allow the checks script.
	object := map[string]any{"permissions": map[string]any{
		"allow": []any{"Skill(harness:running-a-workflow)", "Bash(" + checks + " *)"},
		"deny":  []any{"Read(./.env)"},
	}}
	data, err := reviewRules(object, "", "/work", home)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions struct {
			Allow       []string
			Deny        []string
			DefaultMode string
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	p := got.Permissions
	if p.DefaultMode != "dontAsk" {
		t.Errorf("defaultMode %q, want dontAsk: a command that no rule allows is denied", p.DefaultMode)
	}
	for _, rule := range p.Deny {
		if rule == "Bash" || strings.HasPrefix(rule, "Bash(") {
			t.Errorf("deny has %q: a deny of Bash wins over every allow rule", rule)
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(p.Allow), func(r string) bool { return r != "Bash("+checks+" *)" })); n != 1 {
		t.Errorf("the checks rule is in allow %d times, want 1", n)
	}
	allowed := func(command string) bool {
		return slices.ContainsFunc(p.Allow, func(rule string) bool { return ruleAllows(rule, command) })
	}
	for _, command := range []string{
		wall.HelperPath + " run verdict passed",
		wall.HelperPath + " run verdict changes-requested --points /tmp/qory-artifacts/points.json",
		checks + " end /tmp/qory-artifacts",
		checks + " findings round /tmp/r --reviewers a,b --suite /tmp/s",
	} {
		if !allowed(command) {
			t.Errorf("%q is not allowed", command)
		}
	}
	for _, command := range []string{
		"rm -rf /work",
		"sh -c id",
		wall.HelperPath + " run exec -- sh",
		wall.HelperPath + " run nest -- sh",
		wall.HelperPath + " run verdictx",
		checks + "x end",
		home + "/claude/plugin/skills/running-a-workflow/versions.cjs copy a 1",
	} {
		if allowed(command) {
			t.Errorf("%q is allowed", command)
		}
	}
	if len(p.Allow) != len(slices.Compact(slices.Sorted(slices.Values(p.Allow)))) {
		t.Errorf("allow has a rule twice: %q", p.Allow)
	}
}

func TestReviewRulesWithNoSettingsAndBadInput(t *testing.T) {
	data, err := reviewRules(map[string]any{}, "", "/work", "/h")
	if err != nil || !strings.Contains(string(data), "/h/claude/plugin/skills/running-a-workflow/checks.cjs *") {
		t.Errorf("empty settings: %s, %v", data, err)
	}
	if _, err := reviewRules(map[string]any{}, "", "/work", "/h*"); err == nil || !strings.HasPrefix(err.Error(), "remove the *") {
		t.Errorf("a home with a *: %v", err)
	}
	if _, err := reviewRules(map[string]any{"permissions": "x"}, "", "/work", "/h"); err == nil {
		t.Error("a permissions that is no object has no error")
	}
	if _, err := reviewRules(map[string]any{"permissions": map[string]any{"allow": "x"}}, "", "/work", "/h"); err == nil {
		t.Error("an allow that is no array has no error")
	}
}
