package cmd

import (
	"errors"
	"io/fs"
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

// TestTheInputsAreCheckedBeforeTheRunOpens is the check of a run code's inputs: a member
// qory at the top level stops the run with its line, and so do inputs that are not a JSON
// object, with theirs; a member qory deeper down, or in another case, does not. Each line
// has at most 100 characters.
func TestTheInputsAreCheckedBeforeTheRunOpens(t *testing.T) {
	haveQory, notAnObject := textInputsHaveQory+codeUsed, textInputsNotAnObject+codeUsed
	for inputs, want := range map[string]string{
		`{"version":1,"qory":{}}`: haveQory,
		`{"qory":null}`:           haveQory,
		`{"version":1}`:           "",
		`{"a":{"qory":1}}`:        "",
		`{"Qory":1}`:              "",
		`["qory"]`:                notAnObject,
		`null`:                    notAnObject,
		`"a"`:                     notAnObject,
		``:                        notAnObject,
	} {
		got := ""
		if err := checkInputs([]byte(inputs)); err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: %q, want %q", inputs, got, want)
		}
	}
	for _, line := range []string{haveQory, notAnObject} {
		if len(line) > 100 {
			t.Errorf("%q has %d characters, want at most 100", line, len(line))
		}
	}
}

// TestACopyOfTheInputsOverAFileInTheHomeStops is a home that has inputs.json when qory
// writes the copy of the run's inputs: the file stays as it was, and the error's first line,
// at most 100 characters, is the fix, with the home's path on the next line. Any other
// failure of the write has a first line of at most 100 characters too.
func TestACopyOfTheInputsOverAFileInTheHomeStops(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, inputsFile)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := copyInputs(home, []byte(`{"qory":{"harness_extensions":{}}}`+"\n"))
	if err == nil {
		t.Fatal("the copy over a file in the home went through")
	}
	if data, _ := os.ReadFile(path); string(data) != "{}\n" {
		t.Errorf("the file in the home is %q, want it as it was", data)
	}
	lines := strings.Split(inputsNotCopied(home, err).Error(), "\n")
	want := "Remove inputs.json from the home or unset --home and harness.home, then take the work item in again."
	if len(lines) != 2 || lines[0] != want || !strings.Contains(lines[1], home) || !strings.HasSuffix(lines[1], codeUsed) {
		t.Errorf("the error's lines are %q, want %q, then a line with %s", lines, want, home)
	}
	other := strings.Split(inputsNotCopied(home, os.ErrPermission).Error(), "\n")
	for _, first := range []string{lines[0], other[0]} {
		if len(first) > 100 {
			t.Errorf("the first line %q has %d characters, want at most 100", first, len(first))
		}
	}
	if len(other) != 2 || !strings.Contains(other[1], path) {
		t.Errorf("the lines of another failure are %q, want a second line with %s", other, path)
	}
}

// TestACopyOfTheInputsThatFailsAfterItsCreationLeavesNoFile is a copy whose mode or write
// fails once qory created inputs.json in the home: the file is gone, so the next run in a
// given home does not stop at it. A file that was in the home before stays as it was.
func TestACopyOfTheInputsThatFailsAfterItsCreationLeavesNoFile(t *testing.T) {
	data := []byte(`{"qory":{"harness_extensions":{}}}` + "\n")
	failing := errors.New("the disk is full")
	for name, fill := range map[string]func(*os.File, []byte) error{
		"the mode fails":  func(*os.File, []byte) error { return failing },
		"the write fails": func(f *os.File, d []byte) error { f.Write(d[:4]); return failing },
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, inputsFile)
			err := copyInputsVia(home, data, fill)
			if !errors.Is(err, failing) {
				t.Fatalf("the copy returned %v, want %v", err, failing)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the partial copy stays in the home: %v", err)
			}
		})
	}
	home := t.TempDir()
	path := filepath.Join(home, inputsFile)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err := copyInputsVia(home, data, func(*os.File, []byte) error { called = true; return failing })
	if !errors.Is(err, fs.ErrExist) || called {
		t.Errorf("the copy over a file returned %v (fill called: %v), want %v", err, called, fs.ErrExist)
	}
	if got, _ := os.ReadFile(path); string(got) != "{}\n" {
		t.Errorf("the file that was in the home is %q, want it as it was", got)
	}
}
