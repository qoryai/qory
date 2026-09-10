package compose_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

const fixtures = "../../contracts/harness/v1/fixtures"

// TestFixtures runs every fixture: a stack composes to the expected entries, settings and
// instructions, or fails with the expected error text.
func TestFixtures(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(fixtures, "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixtures under %s: %v", fixtures, err)
	}
	for _, dir := range dirs {
		dir, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Chdir(dir)
			res, err := load()
			if want, readErr := os.ReadFile("expected/error.txt"); readErr == nil {
				if err == nil {
					t.Fatalf("composed without error; want:\n%s", want)
				}
				if got := err.Error(); got != strings.TrimRight(string(want), "\n") {
					t.Fatalf("error:\n%s\nwant:\n%s", got, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for _, e := range res.Entries {
				fmt.Fprintf(&b, "%s/%s %s\n", e.Kind, e.Name, e.Module)
			}
			expectText(t, "expected/entries.txt", b.String())
			// expected/settings/<runtime>/<file>.json holds the merged target file as JSON;
			// a TOML target is expected as <file>.toml.json.
			expected, _ := filepath.Glob("expected/settings/*/*.json")
			for _, file := range expected {
				runtime := filepath.Base(filepath.Dir(file))
				target := strings.TrimSuffix(filepath.Base(file), ".json")
				if strings.HasSuffix(target, ".toml") {
					target = strings.TrimSuffix(target, ".json")
				} else {
					target += ".json"
				}
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var want map[string]any
				if err := json.Unmarshal(data, &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(res.Settings[runtime][target], want) {
					got, _ := json.MarshalIndent(res.Settings[runtime][target], "", "  ")
					t.Fatalf("settings %s/%s:\n%s\nwant:\n%s", runtime, target, got, data)
				}
			}
			n := 0
			for _, files := range res.Settings {
				n += len(files)
			}
			if n != len(expected) {
				t.Fatalf("%d merged settings files, want %d", n, len(expected))
			}
			if _, err := os.Stat("expected/AGENTS.md"); err == nil {
				expectText(t, "expected/AGENTS.md", res.Instructions)
			}
		})
	}
}

func load() (*compose.Result, error) {
	p, err := stack.Load(stack.FileName)
	if err != nil {
		return nil, err
	}
	return compose.Compose(p)
}

func expectText(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is missing; got:\n%s", path, got)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s:\n%s\nwant:\n%s", path, got, want)
	}
}
