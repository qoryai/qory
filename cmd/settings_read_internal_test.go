package cmd

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// permissionsOf is the permissions of the settings JSON data.
func permissionsOf(t *testing.T, data string) map[string]any {
	t.Helper()
	var s struct {
		Permissions map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	return s.Permissions
}

// The rules of a read run for the store's folder.
const (
	readStoreRule = "Read(//tmp/qory-artifacts/**)"
	editStoreRule = "Edit(//tmp/qory-artifacts/**)"
)

// TestReadRulesLeaveNoWriteUnderTheCheckout is the settings of a read run whose composed
// settings allow every edit: an Edit rule that denies the checkout's path and /workspace
// is present, and a deny wins over any allow in Claude Code. The only edit qory allows is
// in the store, which lies outside both. The rules have no bare Edit or Write, which would
// remove the tool from the agent, also in the store.
func TestReadRulesLeaveNoWriteUnderTheCheckout(t *testing.T) {
	data, err := readRules(map[string]any{"permissions": map[string]any{"allow": []any{"Edit", "Write"}}}, "", "/home/a/co")
	if err != nil {
		t.Fatal(err)
	}
	p := permissionsOf(t, string(data))
	deny, _ := p["deny"].([]any)
	for _, rule := range []string{"Edit(//home/a/co/**)", "Edit(//workspace/**)", "NotebookEdit"} {
		if !slices.Contains(deny, any(rule)) {
			t.Errorf("deny %v has no %s", deny, rule)
		}
	}
	if slices.Contains(deny, any("Edit")) || slices.Contains(deny, any("Write")) {
		t.Errorf("deny %v removes a file tool", deny)
	}
	allow, _ := p["allow"].([]any)
	if want := []any{"Edit", "Write", "Bash(/qory/qory *)", readStoreRule, editStoreRule}; !reflect.DeepEqual(allow, want) {
		t.Errorf("allow %v, want %v", allow, want)
	}
	for _, under := range []string{"/home/a/co", workspaceInWall} {
		if strings.HasPrefix(tempStoreDir+"/", under+"/") {
			t.Errorf("the store %s lies under %s", tempStoreDir, under)
		}
	}
}

// TestReadRulesOpenTheStoreToAComposedBlock is settings with
// blockReadsOutsideWorkingDirectories, as a composed module sets it: the key stays, the
// store joins the directories the file lists, once, and a value that is no array stops
// the run.
func TestReadRulesOpenTheStoreToAComposedBlock(t *testing.T) {
	data, err := readRules(map[string]any{"permissions": map[string]any{
		"blockReadsOutsideWorkingDirectories": true,
		"additionalDirectories":               []any{"/data", "/tmp/qory-artifacts"},
	}}, "", "/co")
	if err != nil {
		t.Fatal(err)
	}
	p := permissionsOf(t, string(data))
	if p["blockReadsOutsideWorkingDirectories"] != true {
		t.Errorf("the block is gone: %v", p)
	}
	if got, want := p["additionalDirectories"], []any{"/data", "/tmp/qory-artifacts"}; !reflect.DeepEqual(got, want) {
		t.Errorf("additionalDirectories %v, want %v", got, want)
	}
	_, err = readRules(map[string]any{"permissions": map[string]any{"additionalDirectories": "/data"}}, "", "/co")
	if want := "set permissions.additionalDirectories in the Claude Code settings to a JSON array: a run of a read workflow adds rules to it, so the run does not start"; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// TestReadRulesEscapeTheCheckoutsPath is a checkout whose path contains the characters
// of a gitignore pattern. The rule matches that path, as Claude Code 2.1.316 reads it: a
// [, ] and * escaped, a backslash written as four, and a ? left as a pattern, which also
// matches any other character there. The checkout at /workspace has one rule.
func TestReadRulesEscapeTheCheckoutsPath(t *testing.T) {
	if got, want := readDeny(`/a[1]/b*c?d\e`), []string{"NotebookEdit", `Edit(//a\[1\]/b\*c?d\\\\e/**)`, "Edit(//workspace/**)"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := readDeny("/workspace"), []string{"NotebookEdit", "Edit(//workspace/**)"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestInlineSettingsAddsTheReadRules is the settings of a read workflow's run: the
// file's permissions get NotebookEdit and an Edit rule for the checkout and for
// /workspace in deny, and Bash(/qory/qory *) and a Read and an Edit rule for the store in
// allow, each once, after the rules the file has, the store in additionalDirectories,
// and defaultMode dontAsk in place of the file's; every other key stays, < > & as they are. Settings the launch passes as
// JSON get the same, and a launch with no settings gets --settings with the rules alone.
func TestInlineSettingsAddsTheReadRules(t *testing.T) {
	content := `{"model": "a-model", "hooks": {"Stop": "a && b <c>"}, "permissions": {"allow": ["WebSearch", "Bash(/qory/qory *)"], "deny": ["Bash", "Write"], "defaultMode": "acceptEdits"}}`
	home, file := settingsHome(t, content)
	got, err := inlineSettings("--settings", []string{"--settings", file}, []string{"-p", "hi"}, home, "/co")
	if err != nil || len(got) != 4 || got[0] != "--settings" || !slices.Equal(got[2:], []string{"-p", "hi"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	p := permissionsOf(t, got[1])
	want := map[string]any{
		"allow":       []any{"WebSearch", "Bash(/qory/qory *)", readStoreRule, editStoreRule},
		"deny":        []any{"Bash", "Write", "NotebookEdit", "Edit(//co/**)", "Edit(//workspace/**)"},
		"defaultMode": "dontAsk",

		"additionalDirectories": []any{"/tmp/qory-artifacts"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("permissions %v, want %v", p, want)
	}
	if !strings.Contains(got[1], `"model": "a-model"`) || !strings.Contains(got[1], `"Stop": "a && b <c>"`) {
		t.Errorf("the other keys changed:\n%s", got[1])
	}

	got, err = inlineSettings("--settings", []string{"--settings=" + `{"model":"x"}`}, nil, home, "/co")
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0], "--settings={") {
		t.Fatalf("JSON already: got %q, %v", got, err)
	}
	want = map[string]any{
		"allow":       []any{"Bash(/qory/qory *)", readStoreRule, editStoreRule},
		"deny":        []any{"NotebookEdit", "Edit(//co/**)", "Edit(//workspace/**)"},
		"defaultMode": "dontAsk",

		"additionalDirectories": []any{"/tmp/qory-artifacts"},
	}
	if p := permissionsOf(t, strings.TrimPrefix(got[0], "--settings=")); !reflect.DeepEqual(p, want) {
		t.Errorf("JSON already: permissions %v, want %v", p, want)
	}

	got, err = inlineSettings("--settings", []string{"--verbose"}, []string{"-p", "hi"}, home, "/co")
	if err != nil || len(got) != 5 || !slices.Equal(got[:2], []string{"--verbose", "--settings"}) || !slices.Equal(got[3:], []string{"-p", "hi"}) {
		t.Fatalf("no settings: got %q, %v", got, err)
	}
	if p := permissionsOf(t, got[2]); !reflect.DeepEqual(p, want) {
		t.Errorf("no settings: permissions %v, want %v", p, want)
	}
}

// TestInlineSettingsLeavesAWriteRunsSettings is the settings of a run that is not a read
// workflow's: the file's bytes, unchanged, and no --settings for a launch with none.
func TestInlineSettingsLeavesAWriteRunsSettings(t *testing.T) {
	content := `{"permissions": {"deny": ["Bash"]}}`
	home, file := settingsHome(t, content)
	got, err := inlineSettings("--settings", []string{"--settings", file}, nil, home, "")
	if err != nil || !slices.Equal(got, []string{"--settings", content}) {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = inlineSettings("--settings", []string{"--verbose"}, nil, home, "")
	if err != nil || !slices.Equal(got, []string{"--verbose"}) {
		t.Errorf("no settings: got %q, %v", got, err)
	}
}

// TestInlineSettingsRefusesReadRulesItCannotAdd stops a read workflow's run whose
// settings have permissions that is no object, or an allow or a deny that is no array,
// and one whose settings of the launch are no JSON object: each returns an input error
// with its line.
func TestInlineSettingsRefusesReadRulesItCannotAdd(t *testing.T) {
	for content, want := range map[string]string{
		`{"permissions": []}`:               "set permissions in the Claude Code settings %s to a JSON object: a run of a read workflow adds rules to it, so the run does not start",
		`{"permissions": {"deny": "Edit"}}`: "set permissions.deny in the Claude Code settings %s to a JSON array: a run of a read workflow adds rules to it, so the run does not start",
		`{"permissions": {"allow": {}}}`:    "set permissions.allow in the Claude Code settings %s to a JSON array: a run of a read workflow adds rules to it, so the run does not start",
	} {
		home, file := settingsHome(t, content)
		_, err := inlineSettings("--settings", []string{"--settings", file}, nil, home, "/co")
		if err == nil || err.Error() != strings.Replace(want, "%s", file, 1) || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d)", content, err, ExitCode(err))
		}
	}
	home, _ := settingsHome(t, "{}")
	_, err := inlineSettings("--settings", []string{"--settings", `{"a": 1} {}`}, nil, home, "/co")
	if want := "pass one JSON object to --settings in harness.launch: a run of a read workflow adds rules to it, and the value is not one JSON object, so the run does not start"; err == nil || err.Error() != want {
		t.Errorf("JSON of the launch: %v, want %q", err, want)
	}
}
