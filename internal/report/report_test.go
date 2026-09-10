package report_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/source"
)

// result is the composed profile every test here reports on: two layers, one of them dirty
// with a variant, two entries from different layers, and one exclude. The paths are fixed so the golden file does not move with the test directory.
func result() *compose.Result {
	return &compose.Result{
		Profile: &profile.Profile{
			File:   "/work/app/harness-compose.yaml",
			Target: profile.Target{Runtimes: []string{"claude"}, Model: "opus"},
		},
		Layers: []compose.Layer{
			{
				Name:   "core",
				Dir:    "/work/layers/core",
				Source: "../layers/core",
				Pin:    source.WorkingTree,
			},
			{
				Name:    "review",
				Dir:     "/work/layers/review",
				Source:  "../layers/review",
				Pin:     source.WorkingTree,
				Dirty:   true,
				Variant: "claude",
			},
		},
		Entries: []compose.Entry{
			{Kind: "agents", Name: "reviewer", Layer: "review", Path: "/work/layers/review/agents/reviewer.md"},
			{Kind: "skills", Name: "ship", Layer: "core", Path: "/work/layers/core/skills/ship"},
		},
		Excludes: []compose.Exclude{
			{Layer: "core", Kind: "skills", Name: "old"},
		},
	}
}

// want is the report result() composes into.
func want() report.Report {
	return report.Report{
		Version:  report.Version,
		Profile:  "app",
		File:     "/work/app/harness-compose.yaml",
		Target:   report.Target{Runtimes: []string{"claude"}, Model: "opus"},
		Checkout: "/work/app",
		Home:     "/work/app/.qory/harness",
		Layers: []report.Layer{
			{Name: "core", Source: "../layers/core", Pin: "working-tree"},
			{Name: "review", Source: "../layers/review", Pin: "working-tree", Dirty: true, Variant: "claude"},
		},
		Entries: []report.Entry{
			{Kind: "agents", Name: "reviewer", Layer: "review"},
			{Kind: "skills", Name: "ship", Layer: "core"},
		},
		Excludes: []report.Exclude{
			{Layer: "core", Kind: "skills", Name: "old"},
		},
	}
}

// TestNewNamesEveryLayerEntryAndExclude checks that the report carries the whole result: the
// target, the paths, and every layer, entry and exclude with all of its fields.
func TestNewNamesEveryLayerEntryAndExclude(t *testing.T) {
	got := report.New(result(), "app", "/work/app", "/work/app/.qory/harness")
	if !reflect.DeepEqual(got, want()) {
		t.Errorf("got %+v, want %+v", got, want())
	}
	if got.Version != 1 {
		t.Errorf("Version is %d, want 1", got.Version)
	}
}

// TestNewOnAnEmptyResultKeepsTheListsEmptyNotNull matters for the stored JSON: a caller
// reading it finds [] and not null.
func TestNewOnAnEmptyResultKeepsTheListsEmptyNotNull(t *testing.T) {
	res := &compose.Result{Profile: &profile.Profile{Target: profile.Target{Runtimes: []string{"codex"}}}}
	r := report.New(res, "app", "/work/app", "/work/app/.qory/harness")
	if r.Layers == nil || r.Entries == nil || r.Excludes == nil {
		t.Fatalf("a list is nil: %+v", r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"layers":[]`, `"entries":[]`, `"excludes":[]`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("the JSON lacks %s:\n%s", key, data)
		}
	}
	if strings.Contains(string(data), `"model"`) {
		t.Errorf("an empty model is written:\n%s", data)
	}
}

// TestWriteAndReadRoundTripTheReport writes into a directory that does not exist yet, which
// Write creates, and reads the same report back.
func TestWriteAndReadRoundTripTheReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".qory", "harness-report.json")
	r := want()
	if err := report.Write(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Error("the file does not end with a newline")
	}
	if !bytes.Contains(data, []byte("\n  \"version\": 1,")) {
		t.Errorf("the JSON is not indented:\n%s", data)
	}
	got, err := report.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("got %+v, want %+v", got, r)
	}
}

// TestReadAMissingFileIsNotExist keeps the error a caller can match with errors.Is and
// os.ErrNotExist.
func TestReadAMissingFileIsNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness-report.json")
	got, err := report.Read(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want an os.ErrNotExist", err)
	}
	if !reflect.DeepEqual(got, report.Report{}) {
		t.Errorf("got %+v, want the zero report", got)
	}
}

// TestReadMalformedJSONNamesTheFile checks the error a person sees for a truncated report.
func TestReadMalformedJSONNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness-report.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "layers": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := report.Read(path)
	if err == nil {
		t.Fatal("read a truncated report without an error")
	}
	if !strings.HasPrefix(err.Error(), path+": ") {
		t.Errorf("the error does not name the file: %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("a malformed report reads as a missing one: %v", err)
	}
}

// TestPrintRendersTheReport compares the whole rendered report against the golden file.
func TestPrintRendersTheReport(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// A home directory no path in the report is under keeps Short from writing a tilde.
	t.Setenv("HOME", t.TempDir())
	var buf bytes.Buffer
	if err := want().Print(&buf); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "print.golden")
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(data) {
		t.Errorf("Print wrote\n%s\nwant\n%s", buf.String(), data)
	}
}

// TestPrintWithoutExcludesLeavesOutTheSection covers the one conditional section.
func TestPrintWithoutExcludesLeavesOutTheSection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", t.TempDir())
	r := want()
	r.Excludes = nil
	var buf bytes.Buffer
	if err := r.Print(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Excludes") {
		t.Errorf("a report with no excludes prints the section:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Entries") {
		t.Errorf("the Entries section is missing:\n%s", buf.String())
	}
}

// TestWriteReportsAPathItCannotCreate covers Write failing to make the directory, here
// because a file stands where the directory would go.
func TestWriteReportsAPathItCannotCreate(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("a file, not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := report.Write(filepath.Join(blocked, ".qory", "harness-report.json"), want()); err == nil {
		t.Error("wrote a report under a file")
	}
}
