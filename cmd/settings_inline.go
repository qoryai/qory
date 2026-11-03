package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/exports"
)

// maxSettings is the largest Claude Code settings file qory passes inline: 64 KiB.
const maxSettings = 64 << 10

// errLinked is a settings file reached through a link under the home.
var errLinked = errors.New("linked")

// errNotRegular is a settings path that is not a regular file.
var errNotRegular = errors.New("not regular")

// settingsAt finds flag, the launch's flag for its settings, such as --settings, in
// args, as one argument and its value or as --settings=value: the index of the value
// and the value, -1 for none.
func settingsAt(flag string, args []string) (int, string) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return i + 1, args[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return i, v
		}
	}
	return -1, ""
}

// setsSettings reports whether args contain flag, such as --settings, anywhere: alone,
// before a value or as --settings=value. A --settings with no value after it is the
// flag all the same, and would take whatever Forager puts after it as its value.
func setsSettings(flag string, args []string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// inlineSettings is the arguments of a launch of a [render.SettingsInliner], Claude
// Code's, launch then extra, with the settings file that flag, its settings flag, passes
// from the harness's home in place as its JSON: qory reads the file itself,
// under home and through no link, so what Forager is given is what the home holds. The
// launch passes the flag once at most, with a value after it, and the agent's
// arguments not at all, since Forager takes the first and adds its hooks to it alone. A
// value that is JSON already, from harness.launch, stays as it is. The file is a regular
// file of at most 64 KiB that contains one JSON object. With readRoot, the checkout of a
// run that only reads it, the settings get [readRules], and a launch with no settings
// flag gets the flag with those rules alone.
func inlineSettings(flag string, launch, extra []string, home, readRoot string) ([]string, error) {
	refuse := func(format string, a ...any) error {
		return input(fmt.Errorf(format+", so the run does not start", a...))
	}
	if setsSettings(flag, extra) {
		return nil, refuse("the agent's arguments set %s, which the harness's launch sets", flag)
	}
	args := append([]string{}, launch...)
	at, value := settingsAt(flag, args)
	if at < 0 {
		if setsSettings(flag, args) {
			return nil, refuse("the launch sets %s with no file after it", flag)
		}
		if readRoot != "" {
			data, err := readRules(map[string]any{}, "", readRoot)
			if err != nil {
				return nil, err
			}
			args = append(args, flag, string(data))
		}
		return append(args, extra...), nil
	}
	// A value that is --settings, or --settings=..., is a second --settings: Claude Code
	// would read it as the file.
	if setsSettings(flag, []string{value}) || setsSettings(flag, args[at+1:]) {
		return nil, refuse("the launch sets %s more than once", flag)
	}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		if readRoot == "" {
			return append(args, extra...), nil
		}
		object, ok := oneObject([]byte(value))
		if !ok {
			return nil, refuse("pass one JSON object to %s in harness.launch: a run of a read workflow adds rules to it, and the value is not one JSON object", flag)
		}
		data, err := readRules(object, "of the launch", readRoot)
		if err != nil {
			return nil, err
		}
		args[at] = setValue(args[at], flag, data)
		return append(args, extra...), nil
	}
	path := filepath.Clean(value)
	rel, err := filepath.Rel(home, path)
	if !filepath.IsAbs(path) || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, refuse("the Claude Code settings %s lie outside the harness's home %s", exports.Value(value, false), exports.Value(home, false))
	}
	f, err := openUnder(home, rel)
	switch {
	case errors.Is(err, errLinked):
		return nil, refuse("the Claude Code settings %s are reached through a link under the harness's home", exports.Value(value, false))
	case errors.Is(err, errNotRegular):
		return nil, refuse("the Claude Code settings %s are not a regular file", exports.Value(value, false))
	case err != nil:
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSettings+1))
	f.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > maxSettings {
		return nil, refuse("the Claude Code settings %s are larger than 64 KiB", exports.Value(value, false))
	}
	object, ok := oneObject(data)
	if !ok {
		return nil, refuse("write one JSON object in the Claude Code settings %s: the file is not one JSON object", exports.Value(value, false))
	}
	if readRoot != "" {
		if data, err = readRules(object, exports.Value(value, false), readRoot); err != nil {
			return nil, err
		}
	}
	args[at] = setValue(args[at], flag, data)
	return append(args, extra...), nil
}

// setValue is arg, the settings flag's value or flag=value, with the value data.
func setValue(arg, flag string, data []byte) string {
	if strings.HasPrefix(arg, flag+"=") {
		return flag + "=" + string(data)
	}
	return string(data)
}

// oneObject is data read as one JSON object, and whether it is one.
func oneObject(data []byte) (map[string]any, bool) {
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&object); err != nil || object == nil || dec.More() {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return object, true
}

// workspaceInWall is where a run whose policy names its image by its reference sees the
// checkout, in place of the checkout's path on the machine.
const workspaceInWall = "/workspace"

// readDeny and readAllow are the rules a run that only reads the checkout at root adds to
// Claude Code's settings. Claude Code denies every edit under the checkout and
// NotebookEdit, allows the qory inside the wall in Bash, which is mounted at
// [wall.HelperPath] and is not on PATH, allows reads and edits in [tempStoreDir], where
// the agent reads what it pulled and makes the files it pushes, and denies every other
// tool that no rule allows, without a prompt. Edit rules cover the Write tool too, and a
// bare Edit or Write would remove the tool from the agent, also in that folder. A deny
// wins over any allow that the composed settings contain. The read-only mount stays the
// boundary.
func readDeny(root string) []string {
	rules := []string{"NotebookEdit"}
	for _, path := range []string{root, workspaceInWall} {
		if rule := "Edit(/" + ruleGlob.Replace(path) + "/**)"; !slices.Contains(rules, rule) {
			rules = append(rules, rule)
		}
	}
	return rules
}

// readAllow is described at [readDeny].
func readAllow() []string {
	return []string{
		"Bash(" + wall.HelperPath + " *)",
		"Read(/" + ruleGlob.Replace(tempStoreDir) + "/**)",
		"Edit(/" + ruleGlob.Replace(tempStoreDir) + "/**)",
	}
}

// ruleGlob escapes the characters of a path that a rule reads as a gitignore pattern. On
// Claude Code 2.1.316 a rule matches a backslash only when it is written as four, and an
// escaped ? matches nothing, so ? stays a pattern: a deny then also covers a path with
// any other character there.
var ruleGlob = strings.NewReplacer(`\`, `\\\\`, `*`, `\*`, `[`, `\[`, `]`, `\]`)

// readDefaultMode is the permissions.defaultMode of a read workflow's run.
const readDefaultMode = "dontAsk"

// readRules is object, Claude Code's settings, as JSON with [readDeny] of root added to
// permissions.deny, [readAllow] to permissions.allow, [tempStoreDir] to
// permissions.additionalDirectories, each once, and permissions.defaultMode set to
// [readDefaultMode]; every other key stays as it is, and < > & stay unescaped. The
// directory opens the folder under permissions.blockReadsOutsideWorkingDirectories, which
// a composed module can set and which stops a Read rule there. shown is the settings file
// as a text shows it, or "of the launch" for settings that the launch passes as JSON. A
// permissions that is no object, and an allow, a deny or an additionalDirectories that is
// no array, stop the run.
func readRules(object map[string]any, shown, root string) ([]byte, error) {
	at := "the Claude Code settings " + shown
	if shown == "" {
		at = "the Claude Code settings"
	}
	permissions := map[string]any{}
	if p, ok := object["permissions"]; ok {
		if permissions, ok = p.(map[string]any); !ok {
			return nil, input(fmt.Errorf("set permissions in %s to a JSON object: a run of a read workflow adds rules to it, so the run does not start", at))
		}
	}
	for _, add := range []struct {
		key   string
		rules []string
	}{{"deny", readDeny(root)}, {"allow", readAllow()}, {"additionalDirectories", []string{tempStoreDir}}} {
		var list []any
		if v, ok := permissions[add.key]; ok {
			if list, ok = v.([]any); !ok {
				return nil, input(fmt.Errorf("set permissions.%s in %s to a JSON array: a run of a read workflow adds rules to it, so the run does not start", add.key, at))
			}
		}
		for _, rule := range add.rules {
			if !slices.Contains(list, any(rule)) {
				list = append(list, rule)
			}
		}
		permissions[add.key] = list
	}
	permissions["defaultMode"] = readDefaultMode
	object["permissions"] = permissions
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(object); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
