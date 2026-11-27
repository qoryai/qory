package claude_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// verifyGuard is the Verify guard of Claude Code.
func verifyGuard(t *testing.T) render.VerifyGuard {
	t.Helper()
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := render.VerifyGuardOf(rt)
	if !ok {
		t.Fatal("claude has no Verify guard")
	}
	return guard
}

// TestVerifySettingsAddsTheRulesOfTheReportsFolder: the settings get a Read and an Edit
// rule for the reports folder in permissions.allow and the folder in
// permissions.additionalDirectories, after what they contain. A path with glob characters
// has them escaped in the rules and kept as they are in additionalDirectories.
func TestVerifySettingsAddsTheRulesOfTheReportsFolder(t *testing.T) {
	for name, c := range map[string]struct {
		reports string
		allow   []any
	}{
		"plain path": {"/tmp/run/review-reports-1",
			[]any{"Read(./x)", "Read(//tmp/run/review-reports-1/**)", "Edit(//tmp/run/review-reports-1/**)"}},
		"glob characters": {`/tmp/run/r*[1]`,
			[]any{"Read(./x)", `Read(//tmp/run/r\*\[1\]/**)`, `Edit(//tmp/run/r\*\[1\]/**)`}},
	} {
		settings := map[string]any{"model": "m", "permissions": map[string]any{"allow": []any{"Read(./x)"}}}
		data, err := verifyGuard(t).VerifySettings(settings, "settings.json", c.reports)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, data)
		}
		want := map[string]any{"model": "m", "permissions": map[string]any{
			"allow":                 c.allow,
			"additionalDirectories": []any{c.reports},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: settings %s, want %v", name, data, want)
		}
	}
}

// TestVerifySettingsStopsAnEmptyReportsPath: an empty reports path would give rules for
// every file, so the settings stop with the fix first.
func TestVerifySettingsStopsAnEmptyReportsPath(t *testing.T) {
	const want = "set the Verify reports path: an empty one covers all files, so the run does not start"
	data, err := verifyGuard(t).VerifySettings(map[string]any{}, "settings.json", "")
	if err == nil || err.Error() != want || data != nil {
		t.Errorf("settings %s and %v, want the stop %q", data, err, want)
	}
	if len("qory run: "+want) > 100 {
		t.Errorf("line 1 has %d characters with qory run:, want at most 100", len("qory run: "+want))
	}
}
