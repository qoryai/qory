package compose_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
)

const fixtures = "../../contracts/harness/v1/fixtures"

// TestFixtures runs every fixture: the document in its qory.yaml, on the stack it
// extends when it extends one, composes to the expected entries, settings, instructions
// and workflow access levels, or fails with the expected error text.
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
				// A file of a stack the document extends is in the error by its absolute
				// path; the expected text has it relative to the fixture.
				got := strings.ReplaceAll(err.Error(), dir+string(filepath.Separator), "")
				if got != strings.TrimRight(string(want), "\n") {
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
			// expected/references.txt lists the entries that reference others, and
			// expected/bind.txt the bindings; each when the fixture has any.
			b.Reset()
			for _, e := range res.Entries {
				if len(e.References) > 0 {
					fmt.Fprintf(&b, "%s/%s: %s\n", e.Kind, e.Name, strings.Join(e.References, " "))
				}
			}
			if _, err := os.Stat("expected/references.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/references.txt", b.String())
			}
			b.Reset()
			for _, role := range sortedKeys(res.Bind) {
				fmt.Fprintf(&b, "%s %s\n", role, res.Bind[role])
			}
			if _, err := os.Stat("expected/bind.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/bind.txt", b.String())
			}
			// expected/egress.txt is the union of the declared hosts, each with the
			// modules that declared it, when any module has the key; an empty file
			// is a harness that declares and names nothing.
			b.Reset()
			declared := false
			byHost := map[string][]string{}
			for _, m := range res.Modules {
				declared = declared || m.Egress != nil
				for _, host := range m.Egress {
					byHost[host] = append(byHost[host], m.Name)
				}
			}
			hosts := make([]string, 0, len(byHost))
			for host := range byHost {
				hosts = append(hosts, host)
			}
			sort.Strings(hosts)
			for _, host := range hosts {
				fmt.Fprintf(&b, "%s %s\n", host, strings.Join(byHost[host], " "))
			}
			if _, err := os.Stat("expected/egress.txt"); err == nil || declared {
				expectText(t, "expected/egress.txt", b.String())
			}
			// expected/access.txt is the access level of each workflow of the stack, as
			// a dispatched run reads it, when the stack has any workflows.
			b.Reset()
			for _, name := range slices.Sorted(maps.Keys(res.Stack.Workflows)) {
				fmt.Fprintf(&b, "%s %s\n", name, res.Stack.AccessOf(name))
			}
			if _, err := os.Stat("expected/access.txt"); err == nil || b.Len() > 0 {
				expectText(t, "expected/access.txt", b.String())
			}
		})
	}
}

func load() (*compose.Result, error) {
	p, err := config.Compose(config.FileName)
	if err != nil {
		return nil, err
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		return nil, err
	}
	return compose.ComposeWith(merged, compose.Options{Base: base})
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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
