package exports_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/exports"
)

// TestAPrefixedRefusalFillsItsPlaces is a refusal with a file and a module place: In and
// Prefixed fill them, and a refusal no caller filled says "the file" and "the module".
func TestAPrefixedRefusalFillsItsPlaces(t *testing.T) {
	r := exports.Refuse("Set x in "+exports.ModulePlace+".", "x is in "+exports.FilePlace+".")
	if got, want := r.Error(), "Set x in the module.\n  x is in the file."; got != want {
		t.Errorf("unfilled: %q, want %q", got, want)
	}
	got := exports.Prefixed("module core", exports.AtPath("modules/core/qory-module.yaml", r)).Error()
	if want := "Set x in module core.\n  x is in modules/core/qory-module.yaml."; got != want {
		t.Errorf("filled: %q, want %q", got, want)
	}
}

// TestAHiddenRefusalStaysHiddenThroughPrefixedAndAtPath is a refusal whose cause has an
// access key secret, which HideValueError hides: Prefixed and AtPath put their prefix in
// front of the hidden text, and do not rebuild the refusal under it with the secret.
func TestAHiddenRefusalStaysHiddenThroughPrefixedAndAtPath(t *testing.T) {
	hidden := exports.HideValueError(exports.Refuse("Set token in "+exports.ModulePlace+".", "The value is "+secretValue+"."), "")
	if strings.Contains(hidden.Error(), secretValue) {
		t.Fatalf("HideValueError left the secret in: %s", hidden)
	}
	for name, err := range map[string]error{
		"Prefixed": exports.Prefixed("module core", hidden),
		"AtPath":   exports.AtPath("modules/core/qory-module.yaml", hidden),
	} {
		got := err.Error()
		if strings.Contains(got, secretValue) {
			t.Errorf("%s printed the secret: %s", name, got)
		}
		if !strings.Contains(got, "(a value that contains an access key secret)") {
			t.Errorf("%s: %s, want the hidden text", name, got)
		}
	}
}
