package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/report"
)

// TestNewCarriesLinksEnvAndExtensions is a result with a linked module, an exported
// variable and an extension: the report carries all three, and Print shows them.
func TestNewCarriesLinksEnvAndExtensions(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", t.TempDir())
	res := result()
	res.Modules[0].Link = "harness"
	res.Env = map[string]string{"HARNESS_HOME": "$QORY_HARNESS_HOME/modules/core"}
	res.Stack.Extensions = map[string]map[string]any{"acme": {"sweep_floor": 160, "roots": []any{"scripts"}}}
	r := report.New(res, "app", "/work/app", "/work/app/.qory/harness")
	if r.Modules[0].Link != "harness" || r.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/modules/core" || r.Extensions["acme"]["sweep_floor"] != 160 {
		t.Errorf("report: %+v", r)
	}
	var buf bytes.Buffer
	if err := r.Print(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"linked as harness", "Env", "HARNESS_HOME  $QORY_HARNESS_HOME/modules/core", "Extensions", "acme.roots        [\"scripts\"]", "acme.sweep_floor  160"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("print lacks %q:\n%s", want, buf.String())
		}
	}
	plain := want()
	buf.Reset()
	if err := plain.Print(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Env") || strings.Contains(buf.String(), "Extensions") {
		t.Errorf("a report without them prints the sections:\n%s", buf.String())
	}
}
