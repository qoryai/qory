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

// TestInlineSettingsAddsTheReadRules is the settings of a read workflow's run: the
// file's permissions get Edit, Write and NotebookEdit in deny and Bash(/qory/qory *) in
// allow, each once, after the rules the file has, and defaultMode dontAsk in place of
// the file's; every other key stays, < > & as they are. Settings the launch passes as
// JSON get the same, and a launch with no settings gets --settings with the rules alone.
func TestInlineSettingsAddsTheReadRules(t *testing.T) {
	content := `{"model": "a-model", "hooks": {"Stop": "a && b <c>"}, "permissions": {"allow": ["WebSearch", "Bash(/qory/qory *)"], "deny": ["Bash", "Write"], "defaultMode": "acceptEdits"}}`
	home, file := settingsHome(t, content)
	got, err := inlineSettings("--settings", []string{"--settings", file}, []string{"-p", "hi"}, home, true)
	if err != nil || len(got) != 4 || got[0] != "--settings" || !slices.Equal(got[2:], []string{"-p", "hi"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	p := permissionsOf(t, got[1])
	want := map[string]any{
		"allow":       []any{"WebSearch", "Bash(/qory/qory *)"},
		"deny":        []any{"Bash", "Write", "Edit", "NotebookEdit"},
		"defaultMode": "dontAsk",
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("permissions %v, want %v", p, want)
	}
	if !strings.Contains(got[1], `"model": "a-model"`) || !strings.Contains(got[1], `"Stop": "a && b <c>"`) {
		t.Errorf("the other keys changed:\n%s", got[1])
	}

	got, err = inlineSettings("--settings", []string{"--settings=" + `{"model":"x"}`}, nil, home, true)
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0], "--settings={") {
		t.Fatalf("JSON already: got %q, %v", got, err)
	}
	want = map[string]any{"allow": []any{"Bash(/qory/qory *)"}, "deny": []any{"Edit", "Write", "NotebookEdit"}, "defaultMode": "dontAsk"}
	if p := permissionsOf(t, strings.TrimPrefix(got[0], "--settings=")); !reflect.DeepEqual(p, want) {
		t.Errorf("JSON already: permissions %v, want %v", p, want)
	}

	got, err = inlineSettings("--settings", []string{"--verbose"}, []string{"-p", "hi"}, home, true)
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
	got, err := inlineSettings("--settings", []string{"--settings", file}, nil, home, false)
	if err != nil || !slices.Equal(got, []string{"--settings", content}) {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = inlineSettings("--settings", []string{"--verbose"}, nil, home, false)
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
		_, err := inlineSettings("--settings", []string{"--settings", file}, nil, home, true)
		if err == nil || err.Error() != strings.Replace(want, "%s", file, 1) || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d)", content, err, ExitCode(err))
		}
	}
	home, _ := settingsHome(t, "{}")
	_, err := inlineSettings("--settings", []string{"--settings", `{"a": 1} {}`}, nil, home, true)
	if want := "pass one JSON object to --settings in harness.launch: a run of a read workflow adds rules to it, and the value is not one JSON object, so the run does not start"; err == nil || err.Error() != want {
		t.Errorf("JSON of the launch: %v, want %q", err, want)
	}
}
