package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/report"
)

// TestDescriptionsReachTheReport is a stack and a module with a description: the report
// carries both and Print shows them, the stack's as a field and the module's in its row.
func TestDescriptionsReachTheReport(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", t.TempDir())
	res := result()
	res.Stack.Description = "The web app's harness"
	res.Modules[0].Description = "shared rules"
	r := report.New(res, "app", "/work/app", "/work/app/.qory/harness")
	if r.Description != "The web app's harness" || r.Modules[0].Description != "shared rules" || r.Modules[1].Description != "" {
		t.Fatalf("report: %+v", r)
	}
	var buf bytes.Buffer
	if err := r.Print(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"about     The web app's harness", "shared rules"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("print lacks %q:\n%s", want, buf.String())
		}
	}
}
