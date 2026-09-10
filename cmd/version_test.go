package cmd_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// TestVersionPrintsTheMarkAndTheHarnessFormat runs version for a build stamped with a
// release version and for one that carries none, and checks the lines a person reads.
func TestVersionPrintsTheMarkAndTheHarnessFormat(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "a release version", version: "v1.2.3", want: "v1.2.3"},
		// A build without one falls back to what Go recorded, which is why the test asks
		// only that the title carries something.
		{name: "no release version", version: "dev", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			was := cmd.Version
			t.Cleanup(func() { cmd.Version = was })
			cmd.Version = tc.version

			out, err := run(t, "version")
			if err != nil {
				t.Fatal(err)
			}
			prefix := ui.Mark + " qory · "
			title, _, _ := strings.Cut(out, "\n")
			if !strings.HasPrefix(title, prefix) {
				t.Fatalf("title = %q, want it to start with %q", title, prefix)
			}
			if rest := strings.TrimSpace(strings.TrimPrefix(title, prefix)); rest == "" {
				t.Error("the title names no version")
			} else if !strings.Contains(rest, tc.want) {
				t.Errorf("the title names %q, want %q in it", rest, tc.want)
			}
			wants(t, out, "harness format  "+stack.APIVersion)
		})
	}
}
