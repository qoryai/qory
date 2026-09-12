package cmd_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestExitCodesTellTheFailuresApart is a caller branching on the status: a mistake in the
// input, a collision and a path qory left alone each get their own, and the rest get 1.
func TestExitCodesTellTheFailuresApart(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string) []string
		want  int
	}{
		{
			name:  "success",
			setup: func(t *testing.T, root string) []string { copyFixture(t, "two-modules", root); return []string{"hc"} },
			want:  0,
		},
		{
			name:  "no stack",
			setup: func(*testing.T, string) []string { return []string{"hc"} },
			want:  cmd.ExitInput,
		},
		{
			name: "a stack of another format",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "unknown-api-version", root)
				return []string{"hc"}
			},
			want: cmd.ExitInput,
		},
		{
			name: "a broken module",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "hooks-directory-fails", root)
				return []string{"hc"}
			},
			want: cmd.ExitInput,
		},
		{
			name: "an unknown runtime",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "two-modules", root)
				return []string{"hc", "--runtime", "nope"}
			},
			want: cmd.ExitInput,
		},
		{
			name:  "an unknown flag",
			setup: func(*testing.T, string) []string { return []string{"hc", "--nope"} },
			want:  cmd.ExitInput,
		},
		{
			name:  "a stray argument",
			setup: func(*testing.T, string) []string { return []string{"hc", "extra"} },
			want:  cmd.ExitInput,
		},
		{
			name:  "an unknown command",
			setup: func(*testing.T, string) []string { return []string{"nope"} },
			want:  cmd.ExitInput,
		},
		{
			name:  "an unknown harness verb",
			setup: func(*testing.T, string) []string { return []string{"harness", "compsoe"} },
			want:  cmd.ExitInput,
		},
		{
			name:  "a stray argument to version",
			setup: func(*testing.T, string) []string { return []string{"version", "extra"} },
			want:  cmd.ExitInput,
		},
		{
			name: "a collision",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "collision-fails", root)
				return []string{"hc"}
			},
			want: cmd.ExitCollision,
		},
		{
			name: "a path qory will not replace",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "two-modules", root)
				writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
				return []string{"hc"}
			},
			want: cmd.ExitForeign,
		},
		{
			name: "a git source that cannot be fetched",
			setup: func(t *testing.T, root string) []string {
				writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: file:///nowhere/at/all, ref: v1}\n")
				return []string{"hc"}
			},
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := newCheckout(t)
			args := c.setup(t, root)
			_, err := run(t, args...)
			if got := cmd.ExitCode(err); got != c.want {
				t.Errorf("exit %d for %v, want %d", got, err, c.want)
			}
			if c.want == cmd.ExitCollision && !errors.Is(err, cmd.ErrReported) {
				t.Errorf("a collision is not marked reported: %v", err)
			}
		})
	}
}
