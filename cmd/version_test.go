package cmd_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// release stamps the build with a release version for one test and restores what the
// binary carried after it.
func release(t *testing.T, version string) {
	t.Helper()
	was := cmd.Version
	t.Cleanup(func() { cmd.Version = was })
	cmd.Version = version
}

// TestVersionPrintsTheMarkAndTheHarnessFormat runs version for a build stamped with a
// release version, with and without the v, and for one that carries none, and checks the
// lines a person reads: the number without the v, the source and the formats.
func TestVersionPrintsTheMarkAndTheHarnessFormat(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		source  string
	}{
		{name: "a release version", version: "0.3.0", want: "0.3.0", source: "release"},
		{name: "a release version with a v", version: "v1.2.3", want: "1.2.3", source: "release"},
		// A build without one falls back to what Go recorded, which is why the test asks
		// only that the title carries something.
		{name: "no release version", version: "dev", want: "", source: "source"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			release(t, tc.version)

			out, err := run(t, "version")
			if err != nil {
				t.Fatal(err)
			}
			prefix := ui.Mark + " qory · "
			title, _, _ := strings.Cut(out, "\n")
			if !strings.HasPrefix(title, prefix) {
				t.Fatalf("title = %q, want it to start with %q", title, prefix)
			}
			rest := strings.TrimSpace(strings.TrimPrefix(title, prefix))
			switch {
			case rest == "":
				t.Error("the title names no version")
			case tc.want != "" && rest != tc.want:
				t.Errorf("the title names %q, want %q", rest, tc.want)
			case strings.HasPrefix(rest, "v"):
				t.Errorf("the title names %q with a v; the number stands alone", rest)
			}
			wantsRow(t, out, "source", tc.source)
			wantsRow(t, out, "harness format", stack.APIVersion)
			wantsRow(t, out, "report version", "1")
		})
	}
}

// TestVersionJSONCarriesEveryField runs version --json for a release build and checks
// that the output is one object with every field a script reads, and nothing else.
func TestVersionJSONCarriesEveryField(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	release(t, "0.3.0")

	out, err := run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("output does not end in the object and a newline:\n%s", out)
	}
	want := map[string]any{
		"version":       "0.3.0",
		"source":        "release",
		"harnessFormat": stack.APIVersion,
		"reportVersion": float64(report.Version),
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	if _, ok := got["commit"].(string); !ok {
		t.Errorf("commit = %v, want a string", got["commit"])
	}
	if _, ok := got["dirty"].(bool); !ok {
		t.Errorf("dirty = %v, want a bool", got["dirty"])
	}
}

// TestVersionJSONOfASourceBuildSaysSo runs version --json for a build without a release
// version and checks that source says so, which is what an installer reads to leave a
// developer's own build alone.
func TestVersionJSONOfASourceBuildSaysSo(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	release(t, "dev")

	out, err := run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version string `json:"version"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	if got.Source != "source" {
		t.Errorf("source = %q, want %q", got.Source, "source")
	}
	if strings.HasPrefix(got.Version, "v") {
		t.Errorf("version = %q with a v; the number stands alone", got.Version)
	}
}
