package cmd_test

import (
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/report"
)

// TestARunKeyComposesWithNoPreviousReport is a checkout with a harness report committed
// that lists a replaced path no compose wrote: a run given a run key composes outside the
// checkout as if there were no report, so the report it writes does not list the path.
func TestARunKeyComposesWithNoPreviousReport(t *testing.T) {
	root := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	writeManifest(t, filepath.Join(root, "modules", "core"), "core")
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file := filepath.Join(root, ".qory", "harness-report.json")
	planted := readReport(t, root)
	planted.Replaced = []string{"AGENTS.md"}
	if err := report.Write(file, planted); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-f", ".qory/harness-report.json")
	runGit(t, root, "commit", "-q", "-m", "report")

	out, _, n := inCheckoutRun(t, "claude")
	wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
	rep := runKeyReport(t, root)
	if len(rep.Replaced) != 0 {
		t.Errorf("the report lists the planted replaced paths %v", rep.Replaced)
	}
}
