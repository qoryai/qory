package cmd_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
)

// extensionsVariable is the variable a run from a run key or a run code sets to the path
// of the home's harness-extensions.json.
const extensionsVariable = "QORY_HARNESS_EXTENSIONS"

// extensionsOf is the variable of fixed, the launch's fixed values, NAME=value, with the
// name [extensionsVariable]; "" for none.
func extensionsOf(fixed []string) string {
	for _, kv := range fixed {
		if v, ok := strings.CutPrefix(kv, extensionsVariable+"="); ok {
			return v
		}
	}
	return ""
}

// compactJSON is data with its insignificant spaces removed, or data as it is when it is
// no JSON.
func compactJSON(data []byte) string {
	var b bytes.Buffer
	if err := json.Compact(&b, data); err != nil {
		return string(data)
	}
	return b.String()
}

// TestARunCodeWritesTheHarnessExtensions is a run from a run code that may write: the
// home contains harness-extensions.json with the report's extensions, the base stack's
// merged with the harness document's by key, or an empty map for none, and the launch's
// fixed values set QORY_HARNESS_EXTENSIONS to the file's path.
func TestARunCodeWritesTheHarnessExtensions(t *testing.T) {
	for _, c := range []struct {
		name     string
		stack    string
		document map[string]any
		want     string
	}{
		{
			name:     "with extensions",
			stack:    "extensions:\n  checks:\n    allow: [one, two]\n  team:\n    name: base\n",
			document: map[string]any{"team": map[string]any{"name": "web"}},
			want:     `{"extensions":{"checks":{"allow":["one","two"]},"team":{"name":"web"}}}`,
		},
		{name: "with none", want: `{"extensions":{}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runFromCode(t, func(s string) string { return writesHello(s) + c.stack }, func(d map[string]any) {
				if c.document != nil {
					d["harness"].(map[string]any)["harness"].(map[string]any)["extensions"] = c.document
				}
			})
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			file := filepath.Join(r.spec.HarnessHome, "harness-extensions.json")
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := compactJSON(data); got != c.want {
				t.Errorf("harness-extensions.json is %s, want %s", got, c.want)
			}
			if got := extensionsOf(r.spec.LaunchFixed); got != file {
				t.Errorf("%s is %q, want %q: %q", extensionsVariable, got, file, r.spec.LaunchFixed)
			}
		})
	}
}

// TestAWalledRunCodeMountsTheHarnessExtensionsReadOnly is a walled run from a run code:
// QORY_HARNESS_EXTENSIONS is the path of harness-extensions.json in the home, and the
// home is mounted into the container read-only, at the same path.
func TestAWalledRunCodeMountsTheHarnessExtensionsReadOnly(t *testing.T) {
	r := runFromCodeWith(t, func(string) []string {
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if want := filepath.Join(r.spec.HarnessHome, "harness-extensions.json"); extensionsOf(r.spec.LaunchFixed) != want {
		t.Errorf("%s is not %q: %q", extensionsVariable, want, r.spec.LaunchFixed)
	}
	if !slices.Contains(r.spec.Mounts, wall.Mount{Path: r.spec.HarnessHome, ReadOnly: true}) {
		t.Errorf("the mounts %+v have no read-only %s", r.spec.Mounts, r.spec.HarnessHome)
	}
}

// TestAPersonsRunHasNoHarnessExtensions is a person's run, with no run key and no run code,
// without a wall and behind one: the home has no harness-extensions.json and the launch
// sets no QORY_HARNESS_EXTENSIONS.
func TestAPersonsRunHasNoHarnessExtensions(t *testing.T) {
	for _, walled := range []bool{false, true} {
		t.Run(map[bool]string{false: "without a wall", true: "behind a wall"}[walled], func(t *testing.T) {
			root := newCheckout(t)
			copyFixture(t, "two-modules", root)
			composedForFake(t, root, "claude")
			if walled {
				walledSection(t)
			}
			_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{})
			defer restore()
			// A walled run's home goes once the run is over, so it is read at the join.
			var spec session.SessionSpec
			fileErr := errors.New("no join")
			defer cmd.SetJoinSession(func(s session.SessionSpec) {
				spec = s
				_, fileErr = os.Stat(filepath.Join(s.HarnessHome, "harness-extensions.json"))
			})()
			if out, err := run(t, "run", "claude"); !errors.Is(err, cmd.ErrJoinedNowhere) {
				t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
			}
			if spec.HarnessHome == "" || !errors.Is(fileErr, os.ErrNotExist) {
				t.Errorf("the home %q has harness-extensions.json: %v", spec.HarnessHome, fileErr)
			}
			if got := extensionsOf(spec.LaunchFixed); got != "" {
				t.Errorf("%s is %q in a person's run", extensionsVariable, got)
			}
		})
	}
}
