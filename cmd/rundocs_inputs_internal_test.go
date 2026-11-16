package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/link"
	"github.com/qoryai/forager/wall"
)

// recordSocketDirs is the pattern of the folders of Forager's record sockets, which no
// walled run mounts. Forager keeps the pattern internal: see dirPrefix and Dirs in
// session/internal/socket/socket.go of the Forager module.
var recordSocketDirs = filepath.Join(os.TempDir(), "qory-run-*")

// TestARunCodesInputsFolderIsNoneOfForagersFolders checks the temporary folder that the
// inputs of a run from a run code are written to: its path, as made and with links
// resolved, matches none of the patterns of the temporary folders Forager reserves,
// regardless of case, as Forager compares them, so the wall mounts the inputs.
func TestARunCodesInputsFolderIsNoneOfForagersFolders(t *testing.T) {
	in, remove, err := (&codeRun{}).inputsDir()
	t.Cleanup(remove)
	if err != nil {
		t.Fatal(err)
	}
	top := filepath.Dir(in)
	paths := []string{filepath.Join(os.TempDir(), filepath.Base(top)), top}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reserved := []string{
		wall.TempDirs(),
		filepath.Join(os.TempDir(), link.ToolDirPrefix+"*"),
		filepath.Join(os.TempDir(), link.LinkDirPrefix+"*"),
		recordSocketDirs,
	}
	for _, pattern := range reserved {
		patterns := []string{pattern, filepath.Join(tmp, strings.TrimPrefix(pattern, os.TempDir()))}
		for _, p := range patterns {
			for _, path := range paths {
				matched, err := filepath.Match(strings.ToLower(p), strings.ToLower(path))
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					t.Errorf("the inputs folder %s matches %s, a pattern of Forager's folders", path, p)
				}
			}
		}
	}
}

// TestTheInputsGetTheMemberQory is inputs.json as qory writes it: the inputs as they came,
// then the member qory with harness_extensions, the report's extensions, or {} for none;
// inputs that are no JSON object are an error.
func TestTheInputsGetTheMemberQory(t *testing.T) {
	ext := map[string]any{"software-factory": map[string]any{"command_limits_minutes": map[string]any{"test": 10}}}
	for _, c := range []struct {
		name, inputs string
		ext          map[string]any
		want         string
	}{
		{"members and extensions", "{\"version\": 1,\n \"b\": [2]}\n", ext, "{\"version\": 1,\n \"b\": [2]," + `"qory":{"harness_extensions":{"software-factory":{"command_limits_minutes":{"test":10}}}}}` + "\n"},
		{"no extensions", `{"version":1}`, nil, `{"version":1,"qory":{"harness_extensions":{}}}` + "\n"},
		{"no member", " { } ", map[string]any{}, `{ "qory":{"harness_extensions":{}}}` + "\n"},
	} {
		got, err := withQoryMember([]byte(c.inputs), qoryInputs{HarnessExtensions: c.ext})
		if err != nil || string(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, inputs := range []string{`[1]`, `"a"`, `{`, ``} {
		if got, err := withQoryMember([]byte(inputs), qoryInputs{}); err == nil {
			t.Errorf("%q: %q, want an error", inputs, got)
		}
	}
}

// TestAMemberQoryInTheInputsIsFound is the check that stops a run whose inputs have the
// member qory: only a member qory at the top level counts.
func TestAMemberQoryInTheInputsIsFound(t *testing.T) {
	for inputs, want := range map[string]bool{
		`{"version":1,"qory":{}}`: true,
		`{"qory":null}`:           true,
		`{"version":1}`:           false,
		`{"a":{"qory":1}}`:        false,
		`{"Qory":1}`:              false,
		`["qory"]`:                false,
	} {
		if got := hasQoryMember([]byte(inputs)); got != want {
			t.Errorf("%s: %v, want %v", inputs, got, want)
		}
	}
	if n := len(textInputsHaveQory + codeUsed); n > 100 {
		t.Errorf("the line has %d characters, want at most 100", n)
	}
}
