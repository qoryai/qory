package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// maxSettings is the largest Claude Code settings file qory passes inline: 64 KiB.
const maxSettings = 64 << 10

// settingsFlag is Claude Code's flag for its settings: a file, or the JSON itself.
const settingsFlag = "--settings"

// errLinked is a settings file reached through a link under the home.
var errLinked = errors.New("linked")

// errNotRegular is a settings path that is not a regular file.
var errNotRegular = errors.New("not regular")

// settingsAt finds --settings in args, as one argument and its value or as
// --settings=value: the index of the value and the value, -1 for none.
func settingsAt(args []string) (int, string) {
	for i, a := range args {
		if a == settingsFlag && i+1 < len(args) {
			return i + 1, args[i+1]
		}
		if v, ok := strings.CutPrefix(a, settingsFlag+"="); ok {
			return i, v
		}
	}
	return -1, ""
}

// setsSettings reports whether args hold --settings anywhere: alone, before a value or
// as --settings=value. A --settings with no value after it is Claude Code's flag all the
// same, and would take whatever Forager puts after it as its value.
func setsSettings(args []string) bool {
	for _, a := range args {
		if a == settingsFlag || strings.HasPrefix(a, settingsFlag+"=") {
			return true
		}
	}
	return false
}

// inlineSettings is the arguments of a Claude Code launch, launch then extra, with the
// settings file of the harness's home in place as its JSON: qory reads the file itself,
// under home and through no link, so what Forager is given is what the home holds. The
// launch passes --settings once at most, with a value after it, and the agent's
// arguments not at all, since Forager takes the first and adds its hooks to it alone. A
// value that is JSON already, from harness.launch, stays as it is. The file is a regular
// file of at most 64 KiB that holds one JSON object.
func inlineSettings(launch, extra []string, home string) ([]string, error) {
	refuse := func(format string, a ...any) error {
		return input(fmt.Errorf(format+", so the run does not start", a...))
	}
	if setsSettings(extra) {
		return nil, refuse("the agent's arguments set %s, which the harness's launch sets", settingsFlag)
	}
	args := append([]string{}, launch...)
	at, value := settingsAt(args)
	if at < 0 {
		if setsSettings(args) {
			return nil, refuse("the launch sets %s with no file after it", settingsFlag)
		}
		return append(args, extra...), nil
	}
	// A value that is --settings, or --settings=..., is a second --settings: Claude Code
	// would read it as the file.
	if setsSettings([]string{value}) || setsSettings(args[at+1:]) {
		return nil, refuse("the launch sets %s more than once", settingsFlag)
	}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
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
	var object map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&object); err != nil || object == nil || dec.More() {
		return nil, refuse("the Claude Code settings %s are not one JSON object", exports.Value(value, false))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("the Claude Code settings %s are not one JSON object", exports.Value(value, false))
	}
	if strings.HasPrefix(args[at], settingsFlag+"=") {
		args[at] = settingsFlag + "=" + string(data)
	} else {
		args[at] = string(data)
	}
	return append(args, extra...), nil
}
