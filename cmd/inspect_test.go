package cmd_test

import (
	"maps"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// TestInspectAsksForAComposeFirst checks that inspect on a checkout with no report names
// the checkout and tells the person which command writes one.
func TestInspectAsksForAComposeFirst(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)

	out, err := run(t, "harness", "inspect")
	if err == nil {
		t.Fatalf("inspect without a compose: no error\n%s", out)
	}
	wants(t, err.Error(), "no composed harness for "+root, "run qory harness compose")
	if out != "" {
		t.Errorf("the failed inspect printed:\n%s", out)
	}
}

// TestInspectNamesAReportItCannotRead checks that a report that is not JSON any more fails
// with the path in the message, rather than being read as an empty harness.
func TestInspectNamesAReportItCannotRead(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	report := filepath.Join(root, ".qory", "harness-report.json")
	writeFile(t, report, "not a report\n")

	out, err := run(t, "harness", "inspect")
	if err == nil {
		t.Fatalf("inspect on a broken report: no error\n%s", out)
	}
	wants(t, err.Error(), report)
}

// TestInspectPrintsTheModulesAndEntries checks that inspect after a compose prints the
// report: the paths, every module with its source and pin, and every entry with its module.
func TestInspectPrintsTheModulesAndEntries(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	out, err := run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out,
		ui.Mark+" acme/app · claude opus",
		"qory-stack.yaml",
		root,
		".qory/harness",
		"Modules",
		"core",
		"modules/core",
		"nextjs",
		"modules/nextjs",
		"working-tree",
		"Entries",
	)
	if got := entryTable(out); !maps.Equal(got, twoModuleEntries) {
		t.Errorf("printed entries = %v, want %v", got, twoModuleEntries)
	}
}

// TestInspectRefusesAReportOfAnotherVersion is a report a later qory wrote: the version
// is named rather than the report read as this one's.
func TestInspectRefusesAReportOfAnotherVersion(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	report := filepath.Join(root, ".qory", "harness-report.json")
	writeFile(t, report, `{"version": 99, "stack": "x"}`+"\n")
	out, err := run(t, "harness", "inspect")
	if err == nil {
		t.Fatalf("inspect read a version 99 report:\n%s", out)
	}
	wants(t, err.Error(), "report version 99 is not one this qory reads; versions: 1")
}
