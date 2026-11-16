package cmd_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
)

// qoryMember is the member qory of inputs.json as a run wrote it, "" when the file has no
// member qory or is no JSON object.
func qoryMember(t *testing.T, inputs string) string {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inputs), &members); err != nil {
		t.Errorf("inputs.json does not read: %v\n%s", err, inputs)
		return ""
	}
	return string(members["qory"])
}

// TestARunCodesInputsHaveTheHarnessExtensions is a run from a run code: inputs.json has
// the member qory, whose harness_extensions is the report's extensions, the base stack's
// merged with the harness document's by key, or {} for none, and every member of the
// documents' inputs as well. Behind a wall, the folder of inputs.json is mounted
// read-only.
func TestARunCodesInputsHaveTheHarnessExtensions(t *testing.T) {
	for _, c := range []struct {
		name     string
		stack    string
		document map[string]any
		want     string
	}{
		{
			name:     "with extensions",
			stack:    "extensions:\n  software-factory:\n    command_limits_minutes: {test: 10}\n  team:\n    name: base\n",
			document: map[string]any{"team": map[string]any{"name": "web"}},
			want:     `{"harness_extensions":{"software-factory":{"command_limits_minutes":{"test":10}},"team":{"name":"web"}}}`,
		},
		{name: "with none", want: `{"harness_extensions":{}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runFromCodeWith(t, func(string) []string {
				docker, _ := fakeDocker(t)
				walledBy(t, docker)
				return nil
			}, []string{"--home", filepath.Join(tempDir(t), "home")}, func(s string) string { return writesHello(s) + c.stack }, func(d map[string]any) {
				if c.document != nil {
					d["harness"].(map[string]any)["harness"].(map[string]any)["extensions"] = c.document
				}
			})
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if got := qoryMember(t, r.inputs); got != c.want {
				t.Errorf("the member qory is %s, want %s", got, c.want)
			}
			if !strings.Contains(r.inputs, `"version":1`) {
				t.Errorf("inputs.json lacks the documents' inputs: %s", r.inputs)
			}
			if !slices.Contains(r.spec.Mounts, wall.Mount{Path: r.inputDir, ReadOnly: true}) {
				t.Errorf("the mounts %+v have no read-only %s", r.spec.Mounts, r.inputDir)
			}
		})
	}
}

// TestARunCodeWhoseInputsHaveQoryStops is a run from a run code whose inputs have the
// member qory, which qory adds: the run stops before it opens, exit 1, with a line that
// starts with its fix and names the member.
func TestARunCodeWhoseInputsHaveQoryStops(t *testing.T) {
	r := runFromCode(t, writesHello, func(d map[string]any) {
		d["inputs"].(map[string]any)["qory"] = map[string]any{"harness_extensions": map[string]any{}}
	})
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if len(r.closes) != 0 {
		t.Errorf("the run opened and closed with %v", r.closes)
	}
	wants(t, r.err.Error(), "remove the input qory: qory adds that member to inputs.json; the code is used")
}
