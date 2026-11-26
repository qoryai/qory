package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/render"
)

// reviewDirs are the folders of the review join in these tests, for Claude Code.
var reviewDirs = reviewFolders{inputs: "/runs/in-1/review-session", reports: "/runs/in-1/review-reports-2", guard: claudeGuard()}

// claudeGuard is the review step of Claude Code.
func claudeGuard() render.CommandGuard {
	rt, _ := render.Lookup("claude")
	return rt.(render.CommandGuard)
}

// ruleAllows is Claude Code's match of a Bash rule "Bash(<prefix> *)" with a command that
// has no shell operator: the prefix alone, or the prefix and a space.
func ruleAllows(rule, command string) bool {
	prefix, ok := strings.CutSuffix(strings.TrimPrefix(rule, "Bash("), " *)")
	return ok && (command == prefix || strings.HasPrefix(command, prefix+" "))
}

func TestReviewRulesAllowTheReviewCommandsAndDenyTheRest(t *testing.T) {
	const home = "/home/run/harness"
	const checks = home + "/claude/plugin/skills/running-a-workflow/checks.cjs"
	// The composed settings of the harness already allow the checks script.
	object := map[string]any{"permissions": map[string]any{
		"allow": []any{"Skill(harness:running-a-workflow)", "Bash(" + checks + " *)"},
		"deny":  []any{"Read(./.env)"},
	}}
	data, err := reviewRules(object, "", "/work", home, reviewDirs)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions struct {
			Allow       []string
			Deny        []string
			DefaultMode string
			Dirs        []string `json:"additionalDirectories"`
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
		wall.HelperPath + " work-item show",
		wall.HelperPath + " artifact pull build-report",
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
		wall.HelperPath + " destination clone x",
		wall.HelperPath + " artifact push x",
		wall.HelperPath + " work-item set x",
		wall.HelperPath + " work-item shown",
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
	// A write of the reports folder is allowed, and a write of the copy stays denied: a
	// deny wins over an allow.
	writes := func(path string) bool {
		match := func(rules []string) bool {
			return slices.ContainsFunc(rules, func(rule string) bool {
				dir, ok := strings.CutSuffix(strings.TrimPrefix(rule, "Edit("), "/**)")
				// A rule that starts with // is an absolute path.
				dir = strings.TrimPrefix(dir, "/")
				return ok && strings.HasPrefix(rule, "Edit(//") && strings.HasPrefix(path, dir+"/")
			})
		}
		return match(p.Allow) && !match(p.Deny)
	}
	if !writes("/runs/in-1/review-reports-2/x.json") {
		t.Errorf("a write of the reports folder is denied: allow %q, deny %q", p.Allow, p.Deny)
	}
	if writes("/work/x.json") || writes("/runs/in-1/review-reports-3/x.json") {
		t.Errorf("a write of the copy or of another folder is allowed: allow %q, deny %q", p.Allow, p.Deny)
	}
}

// TestReviewJoinSettingsDisableSkillShellLines: the settings of a review join set
// disableSkillShellExecution to true, also over a false of the harness and with no
// settings flag in the launch, so a reviewer that loads a skill runs none of its !`cmd`
// lines. The settings of a read run that is no review join stay without the key.
func TestReviewJoinSettingsDisableSkillShellLines(t *testing.T) {
	disabled := func(name string, data []byte) {
		t.Helper()
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		}
		if got["disableSkillShellExecution"] != true {
			t.Errorf("%s: disableSkillShellExecution is %#v, want true", name, got["disableSkillShellExecution"])
		}
	}
	data, err := reviewRules(map[string]any{"disableSkillShellExecution": false, "model": "m"}, "", "/work", "/h", reviewDirs)
	if err != nil {
		t.Fatal(err)
	}
	disabled("a false of the harness", data)
	if !strings.Contains(string(data), `"model": "m"`) {
		t.Errorf("another key of the harness is gone:\n%s", data)
	}
	args, err := inlineSettings("--settings", []string{"--verbose"}, nil, "/h", "/work", &reviewDirs)
	if err != nil || len(args) != 3 || args[1] != "--settings" {
		t.Fatalf("no settings flag: %q, %v", args, err)
	}
	disabled("no settings flag", []byte(args[2]))
	read, err := readRules(map[string]any{}, "", "/co")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(read), "disableSkillShellExecution") {
		t.Errorf("a read run that is no review join has the key:\n%s", read)
	}
}

// TestReviewJoinSettingsTurnOffBundledSkillsAndTheOtherModes: the settings of a review
// join turn off the skills that Claude Code ships, auto mode and bypassPermissions mode,
// also over the values of the harness.
func TestReviewJoinSettingsTurnOffBundledSkillsAndTheOtherModes(t *testing.T) {
	object := map[string]any{
		"disableBundledSkills": false, "disableAutoMode": "allow",
		"permissions": map[string]any{"disableBypassPermissionsMode": "allow", "defaultMode": "bypassPermissions"},
	}
	data, err := reviewRules(object, "", "/work", "/h", reviewDirs)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		DisableBundledSkills any `json:"disableBundledSkills"`
		DisableAutoMode      any `json:"disableAutoMode"`
		Permissions          struct {
			DisableBypassPermissionsMode any    `json:"disableBypassPermissionsMode"`
			DefaultMode                  string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.DisableBundledSkills != true || got.DisableAutoMode != "disable" ||
		got.Permissions.DisableBypassPermissionsMode != "disable" || got.Permissions.DefaultMode != "dontAsk" {
		t.Errorf("the settings do not turn the skills and the modes off:\n%s", data)
	}
}

// TestReviewSettingsStopAnEmptyFolder: an empty folder to write, or an empty folder to
// read, stops the settings, because its rule, Edit(//**) or Read(//**), covers every file.
func TestReviewSettingsStopAnEmptyFolder(t *testing.T) {
	const want = "give each review folder a path: an empty one would cover every file, so the run does not start"
	for name, p := range map[string]render.ReviewPlaces{
		"no folder to write":      {Home: "/h", Read: []string{"/r"}, NoEdit: []string{"/work"}},
		"an empty folder to read": {Home: "/h", Read: []string{"/r", ""}, Write: "/r", NoEdit: []string{"/work"}},
	} {
		if data, err := claudeGuard().ReviewSettings(map[string]any{}, "", p); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q\n%s", name, err, want, data)
		}
	}
	if _, err := claudeGuard().ReviewSettings(map[string]any{}, "", render.ReviewPlaces{Home: "/h", Read: []string{"/r"}, Write: "/r"}); err != nil {
		t.Errorf("folders with paths: %v", err)
	}
}

// TestAReviewSessionGetsItsRulesWithNoReadRoot: the settings of a review session get the
// guard's rules also when the read root is empty, for a launch with no settings flag, with
// JSON after the flag and with a file of the home. The empty root then stops the guard,
// so no review session starts with the settings of the harness alone.
func TestAReviewSessionGetsItsRulesWithNoReadRoot(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "settings.json")
	if err := os.WriteFile(file, []byte(`{"model": "m"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "give each review folder a path"
	for name, launch := range map[string][]string{
		"no settings flag": {"--verbose"},
		"JSON":             {"--settings", `{"model": "m"}`},
		"a file":           {"--settings", file},
	} {
		args, err := inlineSettings("--settings", launch, nil, home, "", &reviewDirs)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %q, %v, want an error with %q", name, args, err, want)
		}
	}
}

func TestReviewRulesWithNoSettingsAndBadInput(t *testing.T) {
	data, err := reviewRules(map[string]any{}, "", "/work", "/h", reviewDirs)
	if err != nil || !strings.Contains(string(data), "/h/claude/plugin/skills/running-a-workflow/checks.cjs *") {
		t.Errorf("empty settings: %s, %v", data, err)
	}
	if _, err := reviewRules(map[string]any{}, "", "/work", "/h*", reviewDirs); err == nil || !strings.HasPrefix(err.Error(), "remove the *") {
		t.Errorf("a home with a *: %v", err)
	}
	if _, err := reviewRules(map[string]any{"permissions": "x"}, "", "/work", "/h", reviewDirs); err == nil {
		t.Error("a permissions that is no object has no error")
	}
	if _, err := reviewRules(map[string]any{"permissions": map[string]any{"allow": "x"}}, "", "/work", "/h", reviewDirs); err == nil {
		t.Error("an allow that is no array has no error")
	}
}

// TestReviewJoinSettingsHaveExactlyTheReviewAllowList: with no settings of the harness,
// the JSON of a review join has the allow list of the review guard of Claude Code, the
// folders that the session reads in additionalDirectories, the deny of a read run and
// defaultMode dontAsk.
// The list has no rule for destination, for artifact push or for the other commands of
// work-item, and no Edit rule outside the reports folder.
func TestReviewJoinSettingsHaveExactlyTheReviewAllowList(t *testing.T) {
	const home = "/h"
	data, err := reviewRules(map[string]any{}, "", "/runs/in-1/review-copy-2/commit", home, reviewDirs)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions struct {
			Allow       []string
			Deny        []string
			DefaultMode string
			Dirs        []string `json:"additionalDirectories"`
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	p := got.Permissions
	wantAllow := []string{
		"Bash(" + wall.HelperPath + " run verdict *)",
		"Bash(" + wall.HelperPath + " work-item show *)",
		"Bash(" + wall.HelperPath + " artifact pull *)",
		"Bash(/h/claude/plugin/skills/running-a-workflow/checks.cjs *)",
		"Read(//tmp/qory-artifacts/**)",
		"Read(//runs/in-1/review-copy-2/commit/**)",
		"Read(//runs/in-1/review-session/**)",
		"Read(//runs/in-1/review-reports-2/**)",
		"Edit(//runs/in-1/review-reports-2/**)",
	}
	if !slices.Equal(p.Allow, wantAllow) {
		t.Errorf("allow %q, want %q", p.Allow, wantAllow)
	}
	wantDirs := []string{tempStoreDir, "/runs/in-1/review-copy-2/commit", "/runs/in-1/review-session", "/runs/in-1/review-reports-2"}
	if !slices.Equal(p.Dirs, wantDirs) {
		t.Errorf("additionalDirectories %q, want %q", p.Dirs, wantDirs)
	}
	for _, rule := range p.Allow {
		for _, bad := range []string{"destination", "artifact *", "artifact push", "work-item *", "Edit(//tmp/qory-artifacts"} {
			if strings.Contains(rule, bad) {
				t.Errorf("allow has %q, which has %q", rule, bad)
			}
		}
	}
	if p.DefaultMode != "dontAsk" || !slices.Equal(p.Deny, readDeny("/runs/in-1/review-copy-2/commit")) {
		t.Errorf("defaultMode %q, deny %q", p.DefaultMode, p.Deny)
	}
	if slices.Contains(p.Dirs, destinationsDir) {
		t.Errorf("additionalDirectories has %q, which only a read run clones into", destinationsDir)
	}
}

// TestAReadRunThatIsNoReviewJoinKeepsReadAllow: [readRules] adds [readAllow] and both
// folders of a read run.
func TestAReadRunThatIsNoReviewJoinKeepsReadAllow(t *testing.T) {
	data, err := readRules(map[string]any{}, "", "/co")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions struct {
			Allow []string
			Dirs  []string `json:"additionalDirectories"`
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Permissions.Allow, readAllow()) || !slices.Equal(got.Permissions.Dirs, []string{tempStoreDir, destinationsDir}) {
		t.Errorf("allow %q, dirs %q, want readAllow and both folders", got.Permissions.Allow, got.Permissions.Dirs)
	}
}
