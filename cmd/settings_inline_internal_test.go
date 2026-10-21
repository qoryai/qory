package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// settingsHome makes a home whose claude/settings.json holds content, and returns the
// home and the file.
func settingsHome(t *testing.T, content string) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "harness")
	file := filepath.Join(home, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, file
}

// TestInlineSettingsPassesTheFileAsItIs replaces the settings file's path with its bytes,
// unchanged, in either form of the flag, and keeps every other argument where it was.
func TestInlineSettingsPassesTheFileAsItIs(t *testing.T) {
	content := "{\n  \"model\": \"a-model\",\n  \"hooks\": {}\n}\n"
	home, file := settingsHome(t, content)
	got, err := inlineSettings([]string{"--plugin-dir", "/p", "--settings", file, "--setting-sources", "user"}, []string{"-p", "hi"}, home)
	want := []string{"--plugin-dir", "/p", "--settings", content, "--setting-sources", "user", "-p", "hi"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, %v\nwant %q", got, err, want)
	}
	got, err = inlineSettings([]string{"--settings=" + file}, nil, home)
	if err != nil || !reflect.DeepEqual(got, []string{"--settings=" + content}) {
		t.Errorf("--settings=: got %q, %v", got, err)
	}
	got, err = inlineSettings([]string{"--settings", `{"model":"x"}`}, nil, home)
	if err != nil || !reflect.DeepEqual(got, []string{"--settings", `{"model":"x"}`}) {
		t.Errorf("JSON already: got %q, %v", got, err)
	}
	got, err = inlineSettings([]string{"--verbose"}, []string{"-p", "hi"}, home)
	if err != nil || !reflect.DeepEqual(got, []string{"--verbose", "-p", "hi"}) {
		t.Errorf("no settings: got %q, %v", got, err)
	}
}

// TestInlineSettingsRefuses refuses each settings file qory does not pass, each with its
// line, and a second --settings.
func TestInlineSettingsRefuses(t *testing.T) {
	refused := func(name string, launch, extra []string, home, want string) {
		t.Helper()
		_, err := inlineSettings(launch, extra, home)
		if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	tail := ", so the run does not start"

	home, file := settingsHome(t, "{}")
	elsewhere := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(elsewhere, []byte(`{"model":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	refused("outside the home", []string{"--settings", elsewhere}, nil, home, "the Claude Code settings "+elsewhere+" lie outside the harness's home "+home+tail)
	refused("up and out of the home", []string{"--settings", home + "/../x.json"}, nil, home, "the Claude Code settings "+home+"/../x.json lie outside the harness's home "+home+tail)

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, file); err != nil {
		t.Fatal(err)
	}
	refused("a link at the file", []string{"--settings", file}, nil, home, "the Claude Code settings "+file+" are reached through a link under the harness's home"+tail)

	home, file = settingsHome(t, "{}")
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "settings.json"), []byte(`{"model":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	refused("a link at a folder under the home", []string{"--settings", file}, nil, home, "the Claude Code settings "+file+" are reached through a link under the harness's home"+tail)

	home, file = settingsHome(t, "{}")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(file, 0o644); err != nil {
		t.Fatal(err)
	}
	refused("a pipe", []string{"--settings", file}, nil, home, "the Claude Code settings "+file+" are not a regular file"+tail)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	refused("a folder", []string{"--settings", file}, nil, home, "the Claude Code settings "+file+" are not a regular file"+tail)

	home, file = settingsHome(t, `{"model":"`+strings.Repeat("x", maxSettings)+`"}`)
	refused("too large", []string{"--settings", file}, nil, home, "the Claude Code settings "+file+" are larger than 64 KiB"+tail)
	if _, err := inlineSettings([]string{"--settings", file}, nil, home); err == nil {
		t.Error("too large passed")
	}
	_, file2 := settingsHome(t, "")
	for _, content := range []string{"[1]", "null", "{} {}", `{"a":`, "\xef\xbb\xbf{}"} {
		if err := os.WriteFile(file2, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		refused("not one object: "+content, []string{"--settings", file2}, nil, filepath.Dir(filepath.Dir(file2)), "the Claude Code settings "+file2+" are not one JSON object"+tail)
	}

	home, file = settingsHome(t, "{}")
	refused("a second in the launch", []string{"--settings", file, "--settings=" + file}, nil, home, "the launch sets --settings more than once"+tail)
	refused("one in the agent's arguments", []string{"--settings", file}, []string{"--settings", `{"hooks":{}}`}, home, "the agent's arguments set --settings, which the harness's launch sets"+tail)
	refused("one in the agent's arguments alone", nil, []string{"--settings=" + file}, home, "the agent's arguments set --settings, which the harness's launch sets"+tail)
	refused("a second in the launch with no value", []string{"--settings", file, "--settings"}, nil, home, "the launch sets --settings more than once"+tail)
	refused("a second in the launch with no value, with the agent's arguments", []string{"--settings", file, "--settings"}, []string{"-p", "hi"}, home, "the launch sets --settings more than once"+tail)
	refused("a second in the launch as the first's value", []string{"--settings", "--settings", file}, nil, home, "the launch sets --settings more than once"+tail)
	refused("a second in the launch as the first's value, and last", []string{"--settings", "--settings"}, nil, home, "the launch sets --settings more than once"+tail)
	refused("a second in the launch as the = form's value", []string{"--settings=--settings", file}, nil, home, "the launch sets --settings more than once"+tail)
	refused("a second = form in the launch as the first's value", []string{"--settings", "--settings=" + file}, nil, home, "the launch sets --settings more than once"+tail)
	for _, extra := range [][]string{nil, {"-p", "hi"}} {
		refused(fmt.Sprintf("the launch's last, with no value, agent's arguments %q", extra), []string{"--verbose", "--settings"}, extra, home, "the launch sets --settings with no file after it"+tail)
		refused(fmt.Sprintf("the launch's only, with no value, agent's arguments %q", extra), []string{"--settings"}, extra, home, "the launch sets --settings with no file after it"+tail)
	}
}

// TestInlineSettingsRefusesABareSettingsInTheAgentsArguments refuses --settings with no
// value after it as the agent's last argument, with the launch's --settings and without
// it: Forager, which appends --settings and its copy to a launch with none, would give
// Claude Code --settings --settings and the copy. The form with a value and the = form
// stay refused, and the scan, as before, does not stop at -- or skip a flag's value.
func TestInlineSettingsRefusesABareSettingsInTheAgentsArguments(t *testing.T) {
	home, file := settingsHome(t, "{}")
	want := "the agent's arguments set --settings, which the harness's launch sets, so the run does not start"
	for _, launch := range [][]string{nil, {"--verbose"}, {"--settings", file}} {
		for _, extra := range [][]string{
			{"--settings"},
			{"-p", "hi", "--settings"},
			{"-p", "hi", "--settings", file},
			{"-p", "hi", "--settings=" + file},
			{"--settings="},
			{"--", "--settings"},
			{"--append-system-prompt", "--settings"},
		} {
			got, err := inlineSettings(launch, extra, home)
			if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
				t.Errorf("launch %q, agent's arguments %q: got %q, %v, want %q", launch, extra, got, err, want)
			}
		}
	}
	got, err := inlineSettings(nil, []string{"-p", "--settingsx", "--setting-sources", "user"}, home)
	if err != nil || !reflect.DeepEqual(got, []string{"-p", "--settingsx", "--setting-sources", "user"}) {
		t.Errorf("no --settings: got %q, %v", got, err)
	}
}
