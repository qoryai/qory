package cmd

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// verifyFolders are the folders of a Verify session on Claude Code whose reports folder is
// reports.
func verifyFolders(t *testing.T, reports string) *reviewFolders {
	t.Helper()
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := render.VerifyGuardOf(rt)
	if !ok {
		t.Fatal("claude has no Verify guard")
	}
	return &reviewFolders{reports: reports, verify: guard}
}

// settingsObject is the settings JSON data as an object.
func settingsObject(t *testing.T, data string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(data), &object); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	return object
}

// TestInlineSettingsOfAVerifySessionAddsTheReportsRulesOnly is the settings of a Verify
// session: the file's permissions get a Read and an Edit rule for the reports folder in
// allow and the folder in additionalDirectories, each once, after what the file has.
// Every other key stays as it is, defaultMode and deny included, and a path with a glob
// character is escaped as in a review join. Settings that the launch passes as JSON get
// the same, a launch with no settings gets --settings with these three entries alone, and
// a run with no folders of a Verify session gets the file's bytes.
func TestInlineSettingsOfAVerifySessionAddsTheReportsRulesOnly(t *testing.T) {
	const reports = "/tmp/run/review-reports-1"
	content := `{"model": "a-model", "hooks": {"Stop": "a && b <c>"}, "permissions": {"allow": ["WebSearch"], "deny": ["Bash"], "defaultMode": "acceptEdits", "additionalDirectories": ["/data"]}}`
	home, file := settingsHome(t, content)
	added := func(t *testing.T, input string, reports string) map[string]any {
		t.Helper()
		object := settingsObject(t, input)
		permissions, _ := object["permissions"].(map[string]any)
		if permissions == nil {
			permissions = map[string]any{}
			object["permissions"] = permissions
		}
		allow, _ := permissions["allow"].([]any)
		dirs, _ := permissions["additionalDirectories"].([]any)
		permissions["allow"] = append(allow, "Read(/"+ruleGlob.Replace(reports)+"/**)", "Edit(/"+ruleGlob.Replace(reports)+"/**)")
		permissions["additionalDirectories"] = append(dirs, reports)
		return object
	}

	got, err := inlineSettings("--settings", []string{"--settings", file}, []string{"-p", "hi"}, home, "", verifyFolders(t, reports))
	if err != nil || len(got) != 4 || got[0] != "--settings" || !slices.Equal(got[2:], []string{"-p", "hi"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if want := added(t, content, reports); !reflect.DeepEqual(settingsObject(t, got[1]), want) {
		t.Errorf("settings %s, want the file's with the three entries added: %v", got[1], want)
	}
	if !strings.Contains(got[1], `"Stop": "a && b <c>"`) {
		t.Errorf("< > & changed:\n%s", got[1])
	}
	p := permissionsOf(t, got[1])
	if p["defaultMode"] != "acceptEdits" || !reflect.DeepEqual(p["deny"], []any{"Bash"}) {
		t.Errorf("permissions %v, want the file's defaultMode and deny", p)
	}

	// The rules are added once: settings that have them are returned as they are.
	twice, err := inlineSettings("--settings", []string{"--settings", got[1]}, nil, home, "", verifyFolders(t, reports))
	if err != nil || !reflect.DeepEqual(settingsObject(t, twice[1]), settingsObject(t, got[1])) {
		t.Errorf("a second pass changed the settings: %q, %v", twice, err)
	}

	odd := "/tmp/run/a*b[1]/review-reports-1"
	got, err = inlineSettings("--settings", []string{"--settings", file}, nil, home, "", verifyFolders(t, odd))
	if err != nil || !slices.Contains(permissionsOf(t, got[1])["allow"].([]any), any(`Read(//tmp/run/a\*b\[1\]/review-reports-1/**)`)) {
		t.Errorf("a path with glob characters: %q, %v", got, err)
	}

	got, err = inlineSettings("--settings", []string{"--settings=" + `{"model":"x"}`}, nil, home, "", verifyFolders(t, reports))
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0], "--settings={") {
		t.Fatalf("JSON already: got %q, %v", got, err)
	}
	if want := added(t, `{"model":"x"}`, reports); !reflect.DeepEqual(settingsObject(t, strings.TrimPrefix(got[0], "--settings=")), want) {
		t.Errorf("JSON already: settings %s, want %v", got[0], want)
	}

	got, err = inlineSettings("--settings", []string{"--verbose"}, []string{"-p", "hi"}, home, "", verifyFolders(t, reports))
	if err != nil || len(got) != 5 || !slices.Equal(got[:2], []string{"--verbose", "--settings"}) || !slices.Equal(got[3:], []string{"-p", "hi"}) {
		t.Fatalf("no settings: got %q, %v", got, err)
	}
	want := map[string]any{"permissions": map[string]any{
		"allow":                 []any{"Read(//tmp/run/review-reports-1/**)", "Edit(//tmp/run/review-reports-1/**)"},
		"additionalDirectories": []any{reports},
	}}
	if !reflect.DeepEqual(settingsObject(t, got[2]), want) {
		t.Errorf("no settings: %s, want %v", got[2], want)
	}

	// A write session that is no Verify session gets the file's bytes, and no flag.
	got, err = inlineSettings("--settings", []string{"--settings", file}, nil, home, "", nil)
	if err != nil || !slices.Equal(got, []string{"--settings", content}) {
		t.Errorf("a write session: got %q, %v", got, err)
	}
	got, err = inlineSettings("--settings", []string{"--verbose"}, nil, home, "", nil)
	if err != nil || !slices.Equal(got, []string{"--verbose"}) {
		t.Errorf("a write session with no settings: got %q, %v", got, err)
	}
}

// TestInlineSettingsRefusesVerifyRulesItCannotAdd stops a Verify session whose settings
// have permissions that is no object, or an allow or an additionalDirectories that is no
// array, or whose settings of the launch are no JSON object: each returns an input error
// with its line.
func TestInlineSettingsRefusesVerifyRulesItCannotAdd(t *testing.T) {
	for content, want := range map[string]string{
		`{"permissions": []}`:                           "set permissions in the Claude Code settings %s to a JSON object: a Verify session adds rules to it, so the run does not start",
		`{"permissions": {"allow": {}}}`:                "set permissions.allow in the Claude Code settings %s to a JSON array: a Verify session adds rules to it, so the run does not start",
		`{"permissions": {"additionalDirectories": 1}}`: "set permissions.additionalDirectories in the Claude Code settings %s to a JSON array: a Verify session adds rules to it, so the run does not start",
	} {
		home, file := settingsHome(t, content)
		_, err := inlineSettings("--settings", []string{"--settings", file}, nil, home, "", verifyFolders(t, "/tmp/run/review-reports-1"))
		if err == nil || err.Error() != strings.Replace(want, "%s", file, 1) || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d)", content, err, ExitCode(err))
		}
	}
	home, _ := settingsHome(t, "{}")
	_, err := inlineSettings("--settings", []string{"--settings", `{"a": 1} {}`}, nil, home, "", verifyFolders(t, "/tmp/run/review-reports-1"))
	if want := "pass one JSON object to --settings in harness.launch: a Verify session adds rules to it, and the value is not one JSON object, so the run does not start"; err == nil || err.Error() != want {
		t.Errorf("JSON of the launch: %v, want %q", err, want)
	}
}

// inlinerOnly is a review guard that passes settings with a flag and has no Verify guard.
type inlinerOnly struct{ guardOnly }

func (inlinerOnly) SettingsFlag() string { return "--settings" }

// inlinerOnly must stay a SettingsInliner, or the test below passes for another reason.
var _ render.SettingsInliner = inlinerOnly{}

// TestAVerifySessionStopsOnAnInlinerWithNoVerifyGuard: a runtime that passes settings with
// a flag and has no VerifySettings stops the Verify session before it starts, with the fix
// first on line 1. A runtime with no settings flag gets no folders and no stop, so its
// session runs with no rules. Claude Code gets the folders with its guard.
func TestAVerifySessionStopsOnAnInlinerWithNoVerifyGuard(t *testing.T) {
	const reports = "/tmp/run/review-reports-1"
	const want = "Run this workflow on another runtime: qory has no Verify settings for fake, so the review did not start; the code is used"
	folders, err := verifyFoldersOf(inlinerOnly{}, "fake", reports)
	if folders != nil || flatStop(err) != want {
		t.Errorf("inlinerOnly: folders %v and %v, want the stop %q", folders, err, want)
	} else if first, _, _ := strings.Cut(err.Error(), "\n"); len("qory run: ")+len(first) > 100 {
		t.Errorf("inlinerOnly: line 1 has %d characters with qory run: %q", len("qory run: ")+len(first), first)
	}
	if _, inlines := any(guardOnly{}).(render.SettingsInliner); inlines {
		t.Fatal("guardOnly passes settings with a flag, so the case below tests nothing")
	}
	if folders, err := verifyFoldersOf(guardOnly{}, "fake", reports); folders != nil || err != nil {
		t.Errorf("guardOnly: folders %v and %v, want none and no stop", folders, err)
	}
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	folders, err = verifyFoldersOf(rt, "claude", reports)
	if err != nil || folders == nil || folders.verify == nil || folders.reports != reports {
		t.Errorf("claude: folders %+v, %v", folders, err)
	}
}
