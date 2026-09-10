package cmd_test

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/ui"
)

// TestComposeWritesTheHomeAndLinksIt composes a two-layer profile in a git checkout and
// checks what the person sees and what is left on disk: the title, the count, the home, the
// report, and one relative link that resolves.
func TestComposeWritesTheHomeAndLinksIt(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)

	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out,
		ui.Mark+" acme/app · claude opus",
		"composed 7 entries from 2 layers",
	)
	// The links are printed under the runtime that needs them.
	wantsRow(t, out, "home", ".qory/harness")
	wantsRow(t, out, "claude", ".claude")
	// Without --verbose the entries stay out of the output.
	lacks(t, out, "skills/e2e", "output-styles/terse")

	home := filepath.Join(root, ".qory", "harness")
	for _, name := range []string{"AGENTS.md", "claude/CLAUDE.md", "claude/settings.json", "claude/skills/test", "skills/test"} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			t.Errorf("home lacks %s: %v", name, err)
		}
	}
	if target := linkTarget(t, root, ".claude"); target != filepath.Join(".qory", "harness", "claude") {
		t.Errorf(".claude links to %q", target)
	}

	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Version != report.Version {
		t.Errorf("report version = %d, want %d", rep.Version, report.Version)
	}
	if rep.Profile != "acme/app" || !slices.Equal(rep.Target.Runtimes, []string{"claude"}) || rep.Target.Model != "opus" {
		t.Errorf("report profile %q, target %+v", rep.Profile, rep.Target)
	}
	if rep.Checkout != root || rep.Home != home {
		t.Errorf("report checkout %q, home %q", rep.Checkout, rep.Home)
	}
	if len(rep.Layers) != 2 || rep.Layers[0].Name != "core" || rep.Layers[1].Name != "nextjs" {
		t.Errorf("report layers = %+v", rep.Layers)
	}
	entries := map[string]string{}
	for _, e := range rep.Entries {
		entries[e.Kind+"/"+e.Name] = e.Layer
	}
	if !maps.Equal(entries, twoLayerEntries) {
		t.Errorf("report entries = %v, want %v", entries, twoLayerEntries)
	}
}

// TestComposeDryRunWritesNothing checks that --dry-run prints the report and leaves the
// checkout exactly as it found it: no qory directory and no link.
func TestComposeDryRunWritesNothing(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	before := snapshot(t, root)

	out, err := run(t, "harness", "compose", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out,
		ui.Mark+" acme/app · claude opus",
		"Layers",
		"Entries",
		"working-tree",
		"dry run: nothing written",
	)
	if got := entryTable(out); !maps.Equal(got, twoLayerEntries) {
		t.Errorf("printed entries = %v, want %v", got, twoLayerEntries)
	}
	lacks(t, out, "composed 7 entries")

	if _, err := os.Lstat(filepath.Join(root, ".qory")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".qory: %v, want it absent", err)
	}
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the dry run changed the checkout")
	}
}

// TestComposeFlags covers the flags one at a time: a profile outside the discovery path,
// another runtime than the profile's target, another model, and one line per entry.
func TestComposeFlags(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) []string
		want  []string
		check func(t *testing.T, root, out string)
	}{
		{
			name: "-f reads a profile outside the discovery path",
			setup: func(t *testing.T, _ string) []string {
				return []string{"harness", "compose", "-f", writeSoloProfile(t)}
			},
			want: []string{ui.Mark + " acme/app · claude", "composed 2 entries from 1 layer"},
			check: func(t *testing.T, root, out string) {
				wantsRow(t, out, "claude", ".claude")
				rep := readReport(t, root)
				if len(rep.Layers) != 1 || rep.Layers[0].Name != "solo" {
					t.Errorf("report layers = %+v", rep.Layers)
				}
				if rep.Layers[0].Pin != "working-tree" || rep.Layers[0].Dirty {
					t.Errorf("layer pin %q, dirty %v", rep.Layers[0].Pin, rep.Layers[0].Dirty)
				}
				if filepath.Dir(rep.File) == root {
					t.Errorf("report file %q is inside the checkout", rep.File)
				}
				linkTarget(t, root, ".claude")
			},
		},
		{
			name: "--runtime renders for another runtime than the target",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "two-layers", root)
				return []string{"harness", "compose", "--runtime", "codex"}
			},
			want: []string{
				ui.Mark + " acme/app · codex opus",
				"composed 7 entries from 2 layers",
			},
			check: func(t *testing.T, root, out string) {
				wantsRow(t, out, "codex", ".codex  .agents/skills  AGENTS.override.md")
				if got := fieldRows(out)["skipped"]; !slices.Equal(got, []string{"commands/ship  (no place in codex)", "output-styles/terse  (no place in codex)"}) {
					t.Errorf("skipped = %q", got)
				}
				if rep := readReport(t, root); !slices.Equal(rep.Target.Runtimes, []string{"codex"}) {
					t.Errorf("report runtimes = %q", rep.Target.Runtimes)
				}
				if target := linkTarget(t, root, ".codex"); target != filepath.Join(".qory", "harness", "codex") {
					t.Errorf(".codex links to %q", target)
				}
				if target := linkTarget(t, root, filepath.Join(".agents", "skills")); target != filepath.Join("..", ".qory", "harness", "skills") {
					t.Errorf(".agents/skills links to %q", target)
				}
				if target := linkTarget(t, root, "AGENTS.override.md"); target != filepath.Join(".qory", "harness", "AGENTS.md") {
					t.Errorf("AGENTS.override.md links to %q", target)
				}
				if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "codex", "config.toml")); err != nil {
					t.Errorf("home lacks codex/config.toml: %v", err)
				}
				gone(t, root, ".claude")
			},
		},
		{
			name: "--model writes another model than the target",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "two-layers", root)
				return []string{"harness", "compose", "--model", "sonnet-9"}
			},
			want: []string{ui.Mark + " acme/app · claude sonnet-9"},
			check: func(t *testing.T, root, _ string) {
				if rep := readReport(t, root); rep.Target.Model != "sonnet-9" {
					t.Errorf("report model = %q", rep.Target.Model)
				}
				var settings struct {
					Model string `json:"model"`
					Env   struct {
						Home string `json:"QORY_HARNESS_HOME"`
					} `json:"env"`
				}
				data, err := os.ReadFile(filepath.Join(root, ".qory", "harness", "claude", "settings.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &settings); err != nil {
					t.Fatal(err)
				}
				if settings.Model != "sonnet-9" {
					t.Errorf("settings model = %q", settings.Model)
				}
				if settings.Env.Home != filepath.Join(root, ".qory", "harness") {
					t.Errorf("settings QORY_HARNESS_HOME = %q", settings.Env.Home)
				}
			},
		},
		{
			name: "-v prints one line per entry",
			setup: func(t *testing.T, root string) []string {
				copyFixture(t, "two-layers", root)
				return []string{"harness", "compose", "-v"}
			},
			want: []string{"composed 7 entries from 2 layers"},
			check: func(t *testing.T, _, out string) {
				if got := entryTable(out); !maps.Equal(got, twoLayerEntries) {
					t.Errorf("printed entries = %v, want %v", got, twoLayerEntries)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newCheckout(t)
			args := tc.setup(t, root)
			out, err := run(t, args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wants(t, out, tc.want...)
			tc.check(t, root, out)
		})
	}
}

// TestComposeRefuses covers what a compose says no to before it writes anything: a
// checkout with no profile to discover, a profile of a format this qory does not read, and
// an exclude that names nothing.
func TestComposeRefuses(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		wantErr []string
	}{
		{
			name:    "no profile in the checkout or above it",
			wantErr: []string{"no " + profile.FileName + " in ", "ancestor directory you own"},
		},
		{
			name:    "a profile of another format",
			fixture: "unknown-api-version",
			wantErr: []string{profile.FileName + ":", `apiVersion "qory.ai/v2" is not one this qory reads`},
		},
		{
			name:    "an exclude that names nothing the layer ships",
			fixture: "exclude-names-nothing",
			wantErr: []string{"layer core: exclude skills/nope names nothing the layer ships"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newCheckout(t)
			if tc.fixture != "" {
				copyFixture(t, tc.fixture, root)
			}
			out, err := run(t, "harness", "compose")
			if err == nil {
				t.Fatalf("compose: no error\n%s", out)
			}
			wants(t, err.Error(), tc.wantErr...)
			lacks(t, out, "composed")
			gone(t, root, ".qory", ".claude")
		})
	}
}

// TestComposeUnknownRuntimeNamesTheRuntimes checks that a runtime nothing renders fails
// before anything is written, with an error that names the runtimes there are.
func TestComposeUnknownRuntimeNamesTheRuntimes(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)

	out, err := run(t, "harness", "compose", "--runtime", "nope")
	if err == nil {
		t.Fatalf("compose --runtime nope: no error\n%s", out)
	}
	wants(t, err.Error(), `runtime "nope" is not one this qory renders`)
	for _, name := range render.Names() {
		wants(t, err.Error(), name)
	}
	if out != "" {
		t.Errorf("the failed compose printed:\n%s", out)
	}
	gone(t, root, ".qory", ".claude")
}

// TestComposeTwiceLeavesTheTreeAsItWas checks that a second compose over the first is clean
// and ends with the same tree and the same output.
func TestComposeTwiceLeavesTheTreeAsItWas(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)

	first, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)

	second, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("second compose: %v\n%s", err, second)
	}
	if second != first {
		t.Errorf("second compose printed\n%s\nfirst printed\n%s", second, first)
	}
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the second compose changed the tree")
	}
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness.tmp")); err == nil {
		t.Error("the staging directory is still there")
	}
}

// writeSoloProfile writes a profile and its one small layer into a directory of their own,
// outside any checkout, and returns the profile's path. Discovery never reaches it: a
// checkout in another temporary directory is not below it.
func writeSoloProfile(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "layers", "solo", "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: Greet.\n---\n\nSay hello.\n")
	writeFile(t, filepath.Join(dir, "layers", "solo", "commands", "ship.md"), "# ship\n\nFrom the solo layer.\n")
	writeFile(t, filepath.Join(dir, profile.FileName), `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: solo
    source: {path: layers/solo}
`)
	return filepath.Join(dir, profile.FileName)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readReport(t *testing.T, root string) report.Report {
	t.Helper()
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	return rep
}
