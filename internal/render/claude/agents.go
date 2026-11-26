package claude

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
)

// bashTool is Claude Code's shell tool, and agentTool the tool that starts an agent.
const (
	bashTool  = "Bash"
	agentTool = "Agent"
)

// CheckAgent checks the agent file of every agent for a spaced fence, see
// [checkSpacedFence]; the agent file of a reviewer as the module wrote it, see
// [readReviewerKeys]; every other agent with [checkFences]; and, last, every agent with
// [checkNameLines]; then, in a stack with a review session, every agent with
// [checkPermissionMode]. It reads a.Source, which [render.AgentFront] split once.
func (claude) CheckAgent(a render.AgentCheck) error {
	kind := render.AgentFile
	if a.Review {
		kind = render.ReviewerAgent
	}
	if err := checkSpacedFence(a.Data, a.Path, kind); err != nil {
		return err
	}
	if !a.Review {
		if err := checkFences(a.Data, a.Source, a.Path, kind, a.Name); err != nil {
			return err
		}
	} else {
		fix := reviewerFix(a.Path)
		if err := checkReviewerText(a.Data, fix); err != nil {
			return err
		}
		if _, err := readReviewerKeys(a.Source, a.Path, sourceKeys); err != nil {
			return err
		}
	}
	// The checks of the lines below split the frontmatter at line feeds, so in a stack with
	// a review session no other line break may hide a key from them.
	if a.ReviewSession {
		if err := checkFrontText(a.Source.Front, kind+" "+exports.HidePath(a.Path)); err != nil {
			return err
		}
	}
	if err := checkNameLines(a.Source, a.Path, a.Name, kind); err != nil {
		return err
	}
	if a.ReviewSession {
		return checkPermissionMode(a.Source, a.Path, kind)
	}
	return nil
}

// entryKeys are the keys that the frontmatter of a skill or a command of a stack with a
// review session may set, beside [allowedToolsKey]: Claude Code's name, description and
// argument-hint, with which Claude Code allows no tool, and qory's paths and notes_for of
// a note. A later Claude Code may allow a tool for a key that it adds, so the check
// allows the keys of this list and stops every other key.
var entryKeys = []string{"name", "description", "argument-hint", "paths", "notes_for"}

// listEntryKeys are the [entryKeys] whose value may be a list of strings, beside a
// string; each other key's value must be a string. A value of another type, such as a
// map, could contain a key that the check does not read.
var listEntryKeys = []string{"argument-hint", "paths", "notes_for"}

// entryValueKind returns "" when v, the value of k, one of [entryKeys], is a string, or a
// list of strings for a key of [listEntryKeys], and otherwise the type that k must have,
// for an error.
func entryValueKind(k string, v any) string {
	want := "a string"
	if slices.Contains(listEntryKeys, k) {
		want = "a string or a list of strings"
	}
	switch v := v.(type) {
	case string:
		return ""
	case []any:
		if want == "a string" {
			return want
		}
		for _, item := range v {
			if _, ok := item.(string); !ok {
				return want
			}
		}
		return ""
	}
	return want
}

// allowedToolsKey is the key of the tools that Claude Code allows once a skill or a
// command runs, and readTools are the tools it may list in a stack with a review session.
const allowedToolsKey = "allowed-tools"

var readTools = []string{"Read", "Grep", "Glob"}

// grantLines are the expressions of the keys with which Claude Code allows a tool or runs
// a command: the tools that a skill allows, the agent that a skill with context: fork
// starts, and its hooks.
var grantLines = map[string]*regexp.Regexp{
	allowedToolsKey: keyLine(allowedToolsKey),
	"context":       keyLine("context"),
	"agent":         keyLine("agent"),
	"hooks":         keyLine("hooks"),
}

// CheckEntry checks d, a skill or a command of a stack with a review session: its
// frontmatter sets only [entryKeys], each with a value of the type [entryValueKind]
// allows, and [allowedToolsKey], and allowed-tools lists only [readTools]. Claude Code
// adds the tools of allowed-tools to the session's allow rules once the skill runs, runs
// the hooks of a skill, and starts the skill with context: fork in an agent with those
// tools. Since a YAML reader other than qory's may read a key from any line, a line at any
// indent with one of [grantLines] stops it too, but for the one allowed-tools line at the
// start of a line when the parse has the key, whose value the parse checks, and whose text
// [checkToolsText] checks. When the parse has allowed-tools and no such line was checked,
// the build stops, since the key was then written in a form that the line checks do not
// read, such as a key with an escape in quotes. A spaced fence on line 1 stops it, see
// [checkSpacedFence], a --- in the frontmatter, see [checkFences], and an explicit key or
// a flow form, see [checkKeyForms].
func (claude) CheckEntry(d render.EntryCheck) error {
	if err := checkSpacedFence(d.Data, d.Path, d.Kind); err != nil {
		return err
	}
	if err := checkFences(d.Data, d.Source, d.Path, d.Kind, ""); err != nil {
		return err
	}
	file := d.Kind + " " + exports.HidePath(d.Path)
	if err := checkFrontText(d.Source.Front, file); err != nil {
		return err
	}
	if err := checkKeyForms(d.Source.Front, file); err != nil {
		return err
	}
	keys := make([]string, 0, len(d.Source.Keys))
	for k := range d.Source.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case k == allowedToolsKey:
			if !onlyReadTools(d.Source.Keys[k]) {
				return fmt.Errorf("Set allowed-tools to Read, Grep or Glob in the %s, or remove it, then run again\n"+
					"  The stack has a review session, and a %s of it may allow only those tools", file, d.Kind)
			}
		case !slices.Contains(entryKeys, k):
			return fmt.Errorf("Remove the key %s from the %s, then run again\n"+
				"  The stack has a review session, and a %s of it may set only the keys %s and %s",
				exports.Value(k, false), file, d.Kind, strings.Join(entryKeys, ", "), allowedToolsKey)
		default:
			if want := entryValueKind(k, d.Source.Keys[k]); want != "" {
				return fmt.Errorf("Set %s to %s in the %s, then run again\n"+
					"  The stack has a review session, and a value of another type, such as a map, could contain a key that qory does not check",
					k, want, file)
			}
		}
	}
	names := make([]string, 0, len(grantLines))
	for k := range grantLines {
		names = append(names, k)
	}
	sort.Strings(names)
	_, parsed := d.Source.Keys[allowedToolsKey]
	checked := false
	for _, k := range names {
		lines := keyedLines(d.Source.Front, grantLines[k])
		// The one allowed-tools line at the start of a line is the key that the parse read
		// when the parse has the key; a second such line is in a value.
		tops := 0
		for _, l := range lines {
			if l.top {
				tops++
			}
		}
		for _, l := range lines {
			if k == allowedToolsKey && l.top && parsed && tops == 1 {
				if err := checkToolsText(d.Source.Front, l, file); err != nil {
					return err
				}
				checked = true
				continue
			}
			return fmt.Errorf("Remove the key %s from line %d of the %s, then run again\n"+
				"  A YAML reader may read the key from any line, and with it Claude Code can allow the review session a tool", k, l.line, file)
		}
	}
	if parsed && !checked {
		return fmt.Errorf("Write allowed-tools: once, plain, at the start of a line in the %s, then run again\n"+
			"  The stack has a review session, and qory checks the text of that line, which no other form of the key has", file)
	}
	return nil
}

// checkFrontText returns an error, with the fix first, for a character in front, a
// frontmatter between its fences, that a review stack does not take: front must be UTF-8,
// with only printable characters (unicode.IsPrint), tabs, line feeds and a carriage
// return before a line feed. This stops a line break other than a line feed, where a YAML
// reader such as yaml.v3 starts a new line, so a ... after it ends the document; a byte
// order mark, which makes yaml.v3 skip the first character of each line; and every other
// format or control character, where readers may differ. The line checks of [CheckEntry]
// and of CheckAgent split the frontmatter at line feeds alone. file is what the error
// calls the file.
func checkFrontText(front, file string) error {
	line := 2
	for i, r := range front {
		switch {
		case r == '\n':
			line++
			continue
		case r == utf8.RuneError && !strings.HasPrefix(front[i:], "\uFFFD"):
			return fmt.Errorf("Write the frontmatter of the %s in UTF-8, then run again\n"+
				"  Line %d has the byte 0x%02X, which is not UTF-8, and qory checks the frontmatter as UTF-8 text", file, line, front[i])
		// front ends before the line feed of its last line, so a carriage return at its
		// end is the end of a CRLF line.
		case r == '\r' && (i+1 == len(front) || front[i+1] == '\n'), r == '\t', unicode.IsPrint(r):
			continue
		}
		return fmt.Errorf("Remove the character U+%04X from line %d of the %s, then run again\n"+
			"  A review stack takes only printable characters, tabs and line ends in a frontmatter, so every YAML reader reads its lines as qory does", r, line, file)
	}
	return nil
}

// checkKeyForms returns an error, with the fix first, for a form of a key in front, a
// frontmatter between its fences, that the line checks of [CheckEntry] do not read: a
// line that starts with ?, at any indent and after any - of a list, which a YAML reader
// reads as an explicit key whose : may be on the next line; and a frontmatter whose
// first line with text, but for a comment, starts with { or [, a flow form whose keys
// may be anywhere on a line. It also stops a line that starts with ..., as
// [readReviewerKeys] does: the line ends a YAML document, qory's parse reads no key after
// it, and another reader may read the next document. A --- in the frontmatter, the
// other start of a second document, stops at [checkFences]. file is what the error calls
// the file.
func checkKeyForms(front, file string) error {
	first := true
	for i, line := range strings.Split(front, "\n") {
		if endsDocument(line) {
			return fmt.Errorf("Remove the ... from line %d of the %s, then run again\n"+
				"  A line that starts with ... ends a YAML document, and qory reads no key after it", i+2, file)
		}
		// isJSSpace matches "\r" too: a blank CRLF line trims to "", and the "\r" after a
		// bare ? counts as the space after it.
		text := strings.TrimLeftFunc(line, isJSSpace)
		for len(text) >= 2 && text[0] == '-' && isJSSpace(rune(text[1])) {
			text = strings.TrimLeftFunc(text[1:], isJSSpace)
		}
		if text == "" || text[0] == '#' {
			continue
		}
		if first && (text[0] == '{' || text[0] == '[') {
			return fmt.Errorf("Write the frontmatter of the %s with one key on each line, not in { } or [ ], then run again\n"+
				"  The stack has a review session, and qory checks each key on its line", file)
		}
		first = false
		if text == "?" || text[0] == '?' && isJSSpace(rune(text[1])) {
			return fmt.Errorf("Remove the ? from line %d of the %s, then run again\n"+
				"  A YAML reader reads ? as the start of a key, and qory checks a key only on the line of its colon", i+2, file)
		}
	}
	return nil
}

// toolsText is the text after the colon of an allowed-tools line that lists only
// [readTools], separated by commas and spaces, or nothing; toolItem is a line of a
// block list with one of them.
var (
	toolsText = regexp.MustCompile(`^[ \t,]*(?:(?:` + strings.Join(readTools, "|") + `)(?:[ \t,]+|$))*$`)
	toolItem  = regexp.MustCompile(`^[ \t]*-[ \t]+(?:` + strings.Join(readTools, "|") + `)[ \t]*$`)
)

// checkToolsText returns an error, with the fix first, when the text of l, the
// allowed-tools line of front, or of a line of its value is not [toolsText]. A line of
// the value is each line after l up to the next line that starts with a character other
// than a space, a tab, - or #. Each such line that is not blank stops when l has text
// after the colon, and is otherwise a [toolItem]. So a #, a quote, a bracket or another
// character that one YAML reader reads and another does not stops the build, and the
// check does not depend on the parse. file is what the error calls the file.
func checkToolsText(front string, l keyed, file string) error {
	stop := func(line int) error {
		return fmt.Errorf("Write allowed-tools on line %d of the %s as Read, Grep or Glob, then run again\n"+
			"  The stack has a review session, and the value may have only those tools, commas and spaces, "+
			"or lines of - and one tool", line, file)
	}
	if !toolsText.MatchString(l.text) {
		return stop(l.line)
	}
	lines := strings.Split(front, "\n")
	for i := l.line - 1; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if line != "" && !strings.ContainsRune(" \t-#", rune(line[0])) {
			break
		}
		if strings.TrimFunc(line, isJSSpace) == "" {
			continue
		}
		if strings.TrimFunc(l.text, isJSSpace) != "" || !toolItem.MatchString(line) {
			return stop(i + 2)
		}
	}
	return nil
}

// onlyReadTools reports whether v, the value of allowed-tools, lists only [readTools]: a
// string or a list of strings, each split at commas and spaces, so that a tool in any
// form of the list counts.
func onlyReadTools(v any) bool {
	var items []string
	switch v := v.(type) {
	case nil:
	case string:
		items = []string{v}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return false
			}
			items = append(items, s)
		}
	default:
		return false
	}
	for _, item := range items {
		for _, tool := range strings.FieldsFunc(item, func(r rune) bool { return r == ',' || isJSSpace(r) }) {
			if !slices.Contains(readTools, tool) {
				return false
			}
		}
	}
	return true
}

// keyLine returns the expression of a frontmatter line whose key is key, bare or in
// quotes, once the spaces and the indicators - ? : before it are removed; group 1 is the
// text after the colon.
func keyLine(key string) *regexp.Regexp {
	k := regexp.QuoteMeta(key)
	return regexp.MustCompile(`^(?:"` + k + `"|'` + k + `'|` + k + `)[ \t]*:(.*)$`)
}

// nameLine is a line with the key name, and modeLine one with the key permissionMode.
var (
	nameLine = keyLine("name")
	modeLine = keyLine(permissionModeKey)
)

// keyed is a line of a frontmatter that has a key: its line number in the file, its
// value as [nameValue] reads it, the text after the colon, and whether the key starts
// the line.
type keyed struct {
	line        int
	value, text string
	top         bool
}

// keyedLines returns each line of front, a frontmatter between its fences, that key, an
// expression of [keyLine], matches, at any indent.
func keyedLines(front string, key *regexp.Regexp) []keyed {
	var out []keyed
	for i, line := range strings.Split(front, "\n") {
		line = strings.TrimSuffix(line, "\r")
		text := line
		for {
			text = strings.TrimLeftFunc(text, isJSSpace)
			if len(text) < 2 || !strings.ContainsRune("-?:", rune(text[0])) || !isJSSpace(rune(text[1])) {
				break
			}
			text = text[1:]
		}
		if m := key.FindStringSubmatch(text); m != nil {
			out = append(out, keyed{line: i + 2, value: nameValue(m[1]), text: m[1], top: text == line})
		}
	}
	return out
}

// checkNameLines returns an error, with the fix first, when a YAML reader other than
// qory's could read a name other than the entry name from src, the agent file of the
// entry name at path; kind is what the error calls the file. Claude Code registers an
// agent by that name. The check reads the text of the frontmatter, which the checks
// before it end at the first ---, line by line, so it does not depend on a YAML reader,
// nor on Claude Code's retry that quotes each value. It stops when the frontmatter has a
// << anywhere, which a reader may read as a merge key; when a line, at any indent, has
// the key name and a value that is not the entry name once a comment, the spaces and one
// pair of quotes are removed; and when the frontmatter has a name for qory and not
// exactly one such line at the start of a line.
func checkNameLines(src render.AgentSource, path, name, kind string) error {
	file := kind + " " + exports.HidePath(path)
	if strings.Contains(src.Front, "<<") {
		return fmt.Errorf("Remove the << from the frontmatter of the %s, then run again\n"+
			"  A YAML reader may read << as a merge key and take another name from it", file)
	}
	top := 0
	for _, l := range keyedLines(src.Front, nameLine) {
		if l.top {
			top++
		}
		if l.value != name {
			return fmt.Errorf("Set line %d to name: %s, or remove it, in the %s, then run again\n"+
				"  A YAML reader may read the name of the agent from any line with the key name", l.line, exports.Value(name, false), file)
		}
	}
	if src.Keys["name"] != nil && top != 1 {
		return fmt.Errorf("Write name: %s once, not indented, in the %s, then run again\n"+
			"  The frontmatter has %d lines that start with name:, so a YAML reader may read another name", exports.Value(name, false), file, top)
	}
	return nil
}

// permissionModeKey is the key of an agent's permission mode, and permissionModes the
// modes that an agent may have in a stack with a review session, which can start any
// agent of the harness.
const permissionModeKey = "permissionMode"

var permissionModes = []string{"default", "dontAsk", "plan"}

// checkPermissionMode returns an error, with the fix first, when src, the agent file at
// path, has a permissionMode that is not one of [permissionModes]: as qory reads the
// frontmatter, where a null or a value that is no string stops too, and on any line with
// the key, at any indent, as [checkNameLines] reads a name line. kind is what the error
// calls the file.
func checkPermissionMode(src render.AgentSource, path, kind string) error {
	file := kind + " " + exports.HidePath(path)
	if v, ok := src.Keys[permissionModeKey]; ok {
		if s, isString := v.(string); !isString || !slices.Contains(permissionModes, s) {
			return fmt.Errorf("Set permissionMode to default, dontAsk or plan in the %s, or remove it, then run again\n"+
				"  The stack has a review session, which can start this agent, and qory allows only these modes there", file)
		}
	}
	for _, l := range keyedLines(src.Front, modeLine) {
		if !slices.Contains(permissionModes, l.value) {
			return fmt.Errorf("Set line %d to permissionMode: default, dontAsk or plan in the %s, then run again\n"+
				"  A YAML reader may read the mode of the agent from any line with the key permissionMode", l.line, file)
		}
	}
	return nil
}

// nameValue is the value of a name line: v without a comment, which starts at a # after
// a space or a tab, without the spaces around it and without one pair of quotes.
func nameValue(v string) string {
	for i := range len(v) {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	v = jsTrim(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return v
}

// WithoutCommands is [WithoutBash].
func (claude) WithoutCommands(data []byte, path, name string) ([]byte, error) {
	return WithoutBash(data, path, name)
}

// WithoutBash returns the agent file of Claude Code for the reviewer name with no Bash,
// with a frontmatter that qory writes and does not edit in place. Claude Code ends the
// frontmatter at the first --- anywhere in the text and reads it with a YAML reader that
// differs from qory's, and an agent whose frontmatter it cannot read gets every tool. So
// the function parses the frontmatter of data with [module.ParseDocument], and writes
// these keys in this order, each value as a plain or double-quoted scalar or a block list:
//
//   - name, set to name;
//   - description, then color, effort, maxTurns and model, in this order, each with its
//     value as data has it;
//   - tools, the entries of data without each Bash entry, a Bash rule such as
//     Bash(git) included, left out when data has no tools key;
//   - disallowedTools, with the bare tools Bash and Agent first and then the entries of
//     data.
//
// The body after the closing fence is kept byte for byte. The frontmatter is a narrow
// form that every reader reads the same way, and qory escapes or normalises nothing. It
// stops with an error that names the file at path, as [exports.HidePath] prints it, when
// line 1 is not exactly --- and a line break; when the frontmatter has a byte that is
// not printable ASCII or a line break, see [checkReviewerText]; when it does not parse;
// when it has a key that [reviewerKeyNames] does not list;
// when it is not a block mapping; when it has a "..." line, a merge key, or a ---
// anywhere between its fences, which Claude Code would read as the end of the
// frontmatter; when a tools or disallowedTools entry is not a tool name, see [toolName],
// or the value has a *; when a key or value cannot be written as above (an alias, an
// anchor, a tag, a mapping, a null, a float, a time, an int or a bool written in another
// form than qory writes it, a character outside printable ASCII); or when a tools key
// has no entry left once Bash is removed. A first line that is the fence followed by
// spaces stops it too, which Claude Code may read as frontmatter and this package reads
// as none.
//
// Last, it reads the text it wrote the way Claude Code does, see [claudeFrontOf],
// [claudeTools] and [denyList], and stops unless that text is the frontmatter it wrote,
// no tool is Bash or *, the deny list has Bash and Agent, and each other key has the
// value it has in data.
func WithoutBash(data []byte, path, name string) ([]byte, error) {
	if err := checkSpacedFence(data, path, render.ReviewerAgent); err != nil {
		return nil, err
	}
	fix := reviewerFix(path)
	if err := checkReviewerText(data, fix); err != nil {
		return nil, err
	}
	src, err := render.SplitAgent(data, path, render.ReviewerAgent)
	if err != nil {
		return nil, err
	}
	keys, err := readReviewerKeys(src, path, reviewerKeyNames)
	if err != nil {
		return nil, err
	}
	front, err := writeReviewerFront(keys, name, fix)
	if err != nil {
		return nil, err
	}
	out := []byte("---\n" + front + "\n---\n" + src.Rest)
	if err := checkWritten(out, front, name, src.Keys, fix); err != nil {
		return nil, err
	}
	return out, nil
}

// checkSpacedFence returns the error of [render.CheckFence] for every character that
// JavaScript's \s matches, not only a space or a tab: when line 1 of data, after one byte
// order mark, is the fence followed by such characters alone. Claude Code reads that line
// as the start of the frontmatter, and qory reads none. One "\r" at the end of the line is
// a CRLF line end, not a space. kind is what the error calls the file.
func checkSpacedFence(data []byte, path, kind string) error {
	if err := render.CheckFence(data, path, kind); err != nil {
		return err
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(string(data), "\ufeff"), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSuffix(first, "\r"), "---")
	if !ok || rest == "" || strings.TrimFunc(rest, isJSSpace) != "" {
		return nil
	}
	return fmt.Errorf("Remove the spaces after --- on line 1 of the %s %s, then run again\n"+
		"  Claude Code reads the frontmatter after that line, and qory reads none", kind, exports.HidePath(path))
}

// checkFences returns an error, with the fix first, when Claude Code could read another
// frontmatter than qory in the file data, split as src, at path: when line 1, after one
// byte order mark, starts with --- and is not --- alone, with an "\r" at the end or none;
// and when the frontmatter between its fences, its name or name has a ---, where Claude
// Code ends the frontmatter. A file whose line 1 does not start with --- has no
// frontmatter for either reader. kind is what the error calls the file, and name is the
// name that qory writes into the frontmatter of an agent, "" for a skill or a command.
func checkFences(data []byte, src render.AgentSource, path, kind, name string) error {
	text := strings.TrimPrefix(string(data), "\ufeff")
	if first, _, _ := strings.Cut(text, "\n"); strings.HasPrefix(first, "---") && strings.TrimSuffix(first, "\r") != "---" {
		return fmt.Errorf("Write --- alone on line 1 of the %s %s, then run again\n"+
			"  Claude Code and qory read the frontmatter only after a line 1 that is exactly ---", kind, exports.HidePath(path))
	}
	front, _ := src.Keys["name"].(string)
	if strings.Contains(src.Front, "---") || strings.Contains(front, "---") {
		return fmt.Errorf("Remove the --- from the frontmatter of the %s %s, then run again\n"+
			"  Claude Code ends the frontmatter at the first ---, so it would read another one than qory", kind, exports.HidePath(path))
	}
	if strings.Contains(name, "---") {
		return fmt.Errorf("Rename the agent %s to a name with no ---, then run again\n"+
			"  qory writes the name into the frontmatter, and Claude Code ends the frontmatter at the first ---", exports.HidePath(path))
	}
	return nil
}

// reviewerFix returns the function that makes the error of a reviewer file at path, with
// the cause on line 2.
func reviewerFix(path string) func(string) error {
	return func(cause string) error {
		return fmt.Errorf("Fix the frontmatter of the %s %s, then run again\n  %s", render.ReviewerAgent, exports.HidePath(path), cause)
	}
}

// checkReviewerText returns the error fix makes when line 1 of data is not exactly ---
// and a line break, or when the frontmatter, from line 1 to the line of its closing
// fence, has a byte that is neither printable ASCII (0x20 to 0x7E) nor a line break: a
// tab, a "\r", a byte order mark and every character beyond ASCII stop the build.
func checkReviewerText(data []byte, fix func(string) error) error {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return fix("line 1 is not exactly ---; write --- alone on line 1, with nothing before or after it")
	}
	end := len(text)
	for pos := len("---\n"); pos < len(text); {
		next := len(text)
		if i := strings.IndexByte(text[pos:], '\n'); i >= 0 {
			next = pos + i + 1
		}
		if strings.TrimRight(strings.TrimSuffix(text[pos:next], "\n"), " \t\r") == "---" {
			end = next
			break
		}
		pos = next
	}
	line := 1
	for i := 0; i < end; i++ {
		switch b := text[i]; {
		case b == '\n':
			line++
		case b < 0x20 || b > 0x7E:
			return fix(fmt.Sprintf("line %d of the frontmatter has the byte 0x%02X; write the frontmatter in printable ASCII", line, b))
		}
	}
	return nil
}

// endsDocument reports whether line, with a carriage return at its end or none, is a YAML
// document end marker: ... alone, or followed by a space or a tab.
func endsDocument(line string) bool {
	l := strings.TrimRight(line, "\r")
	return l == "..." || strings.HasPrefix(l, "... ") || strings.HasPrefix(l, "...\t")
}

// reviewerKeys are the keys that qory writes into the frontmatter of a reviewer: the
// text of each key's value as one line or a block list, "" for none.
type reviewerKeys struct {
	other                  map[string]string
	tools, disallowedTools []string
	hasTools               bool
}

// keyName is a frontmatter key that qory writes.
var keyName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// reviewerKeyNames are the only keys that the frontmatter of a reviewer may have, as
// Claude Code gets it. qory writes no other key, such as permissionMode, hooks,
// mcpServers or skills, into the file of a reviewer.
var reviewerKeyNames = []string{"name", "description", "model", "effort", "maxTurns", "color", "tools", "disallowedTools"}

// sourceKeys are [reviewerKeyNames] and paths, which qory reads and removes before it
// writes the file: the keys of a reviewer as the module wrote it.
var sourceKeys = append(slices.Clone(reviewerKeyNames), "paths")

// readReviewerKeys reads the frontmatter of src, the file of the reviewer at path, as a
// YAML tree and returns its keys, or an error for the first form that qory does not
// write: a key that allowed does not list stops with the key in the fix on line 1, and
// every other form with [reviewerFix].
func readReviewerKeys(src render.AgentSource, path string, allowed []string) (reviewerKeys, error) {
	fix := reviewerFix(path)
	keys := reviewerKeys{other: map[string]string{}}
	if !src.HasFront {
		return keys, nil
	}
	if strings.Contains(src.Front, "---") {
		return keys, fix("the frontmatter contains ---, where Claude Code ends it; remove the ---")
	}
	for _, line := range strings.Split(src.Front, "\n") {
		if endsDocument(line) {
			return keys, fix("the frontmatter has a line that starts with ..., which ends a YAML document; remove it")
		}
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src.Front), &root); err != nil {
		return keys, fix(exports.HideValueError(err, "").Error())
	}
	if len(root.Content) == 0 {
		return keys, nil
	}
	m := root.Content[0]
	if m.Kind != yaml.MappingNode || m.Style&(yaml.FlowStyle|yaml.TaggedStyle) != 0 || m.Anchor != "" {
		return keys, fix("the frontmatter is not a block mapping; write one key per line as key: value")
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Tag == "!!merge" || k.Value == "<<" {
			return keys, fix("the frontmatter has a merge key <<; write the keys out")
		}
		if k.Style&yaml.TaggedStyle != 0 {
			return keys, fix("the key " + exports.Value(k.Value, true) + " has a tag; remove the tag")
		}
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Anchor != "" || !keyName.MatchString(k.Value) {
			return keys, fix("the key " + exports.Value(k.Value, true) + " is not a plain word of letters, digits, _ and -")
		}
		if !slices.Contains(allowed, k.Value) {
			return keys, fmt.Errorf("Remove the key %s from the %s %s, then run again\n"+
				"  A reviewer takes only the keys %s", k.Value, render.ReviewerAgent, exports.HidePath(path), keyList(allowed))
		}
		switch k.Value {
		case "name":
		case "tools", "disallowedTools":
			list, err := toolEntries(k.Value, v)
			if err != nil {
				return keys, fix(err.Error())
			}
			if k.Value == "tools" {
				keys.tools, keys.hasTools = list, true
			} else {
				keys.disallowedTools = list
			}
		default:
			text, err := valueText(v)
			if err != nil {
				return keys, fix("the value of " + k.Value + " is not " + err.Error())
			}
			keys.other[k.Value] = text
		}
	}
	return keys, nil
}

// keyList is keys as a text: a, b and c.
func keyList(keys []string) string {
	if len(keys) < 2 {
		return strings.Join(keys, "")
	}
	return strings.Join(keys[:len(keys)-1], ", ") + " and " + keys[len(keys)-1]
}

// toolName is the one form of a tools or disallowedTools entry that qory writes for a
// reviewer: a word, or a word with one (...) whose content has no comma, space,
// parenthesis or *.
var toolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\([!-'+\-.-~]*\))?$`)

// toolEntries is the entries of a tools value: a string or a list of strings, each read
// as Claude Code splits it by [claudeTools]. The error is the cause of a value that is
// not one of them, that has a *, or that has an entry that is not a [toolName].
func toolEntries(key string, v *yaml.Node) ([]string, error) {
	notList := errors.New("the value of " + key + " is not a string or a list of strings")
	if v.Anchor != "" || v.Style&yaml.TaggedStyle != 0 {
		return nil, notList
	}
	var values []string
	switch {
	case v.Kind == yaml.ScalarNode && v.Tag == "!!str":
		values = []string{v.Value}
	case v.Kind == yaml.SequenceNode:
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode || c.Tag != "!!str" || c.Anchor != "" || c.Style&yaml.TaggedStyle != 0 {
				return nil, notList
			}
			values = append(values, c.Value)
		}
	default:
		return nil, notList
	}
	for _, s := range values {
		if strings.Contains(s, "*") {
			return nil, errors.New("the value of " + key + " has a *, which Claude Code reads as every tool; list each tool by name")
		}
	}
	out := claudeTools(values...)
	for _, t := range out {
		if !toolName.MatchString(t) {
			return nil, errors.New("the " + key + " entry " + exports.Value(t, true) + " is not a tool name, or a name and one (rule) with no comma, space or parenthesis in it")
		}
	}
	return out, nil
}

// valueText writes the value of a key as text for a frontmatter: a scalar on the line of
// its key, a list of scalars as a block under it, where an empty list is []. The error
// says what the value is not.
func valueText(v *yaml.Node) (string, error) {
	if v.Kind == yaml.SequenceNode && v.Anchor == "" && v.Style&yaml.TaggedStyle == 0 {
		if len(v.Content) == 0 {
			return " []", nil
		}
		var b strings.Builder
		for _, c := range v.Content {
			s, err := scalarText(c)
			if err != nil {
				return "", err
			}
			b.WriteString("\n  - " + s)
		}
		return b.String(), nil
	}
	s, err := scalarText(v)
	if err != nil {
		return "", err
	}
	return " " + s, nil
}

// scalarText writes a string, a bool or an int. A bool or an int is written as qory
// writes it, and its text in the file must be that text: 007, 0x1F and True stop the
// build. So does a plain yes, no, on, off, y or n in any case, which a YAML 1.1 reader
// reads as a bool and qory as a string.
func scalarText(v *yaml.Node) (string, error) {
	const notScalar = "a string, a bool, an int or a list of them"
	if v.Kind != yaml.ScalarNode || v.Anchor != "" || v.Style&yaml.TaggedStyle != 0 {
		return "", errors.New(notScalar)
	}
	var text string
	switch v.Tag {
	case "!!str":
		if v.Style == 0 && slices.Contains(yamlWords, strings.ToLower(v.Value)) {
			return "", errors.New("a string written in quotes, or a bool written as true or false")
		}
		return quoted(v.Value)
	case "!!bool":
		var b bool
		if err := v.Decode(&b); err != nil {
			return "", errors.New(notScalar)
		}
		text = strconv.FormatBool(b)
	case "!!int":
		var n int64
		if err := v.Decode(&n); err != nil {
			return "", errors.New(notScalar)
		}
		text = strconv.FormatInt(n, 10)
	default:
		return "", errors.New(notScalar)
	}
	if v.Value != text {
		return "", errors.New("written as " + text + ", the form qory writes")
	}
	return text, nil
}

// plainWord is a string that qory writes without quotes.
var plainWord = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// yamlWords are plain words that a YAML reader may read as another type than a string.
var yamlWords = []string{"true", "false", "null", "yes", "no", "on", "off", "y", "n"}

// quoted writes s as a plain word when it is one, else in double quotes with the escapes
// \\ \" \n \t and \r only. A string with any other character outside printable ASCII
// cannot be written.
func quoted(s string) (string, error) {
	if plainWord.MatchString(s) && !slices.Contains(yamlWords, strings.ToLower(s)) {
		return s, nil
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r >= 0x20 && r <= 0x7E:
			b.WriteRune(r)
		default:
			return "", errors.New("text of printable ASCII characters")
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// writeReviewerFront writes the frontmatter of a reviewer, without its fences.
func writeReviewerFront(keys reviewerKeys, name string, fix func(string) error) (string, error) {
	n, err := quoted(name)
	if err != nil {
		return "", fix("the entry name is not " + err.Error())
	}
	lines := []string{"name: " + n}
	if d, ok := keys.other["description"]; ok {
		lines = append(lines, "description:"+d)
	}
	var others []string
	for k := range keys.other {
		if k != "description" {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	for _, k := range others {
		lines = append(lines, k+":"+keys.other[k])
	}
	if keys.hasTools {
		kept := slices.DeleteFunc(slices.Clone(keys.tools), isBash)
		if len(kept) == 0 {
			return "", fix("tools has no entry left once Bash is removed; list a tool the reviewer needs, or remove tools")
		}
		list, err := blockList("tools", kept)
		if err != nil {
			return "", fix("a tools entry is not " + err.Error())
		}
		lines = append(lines, list)
	}
	denied := []string{bashTool, agentTool}
	for _, t := range keys.disallowedTools {
		if t != bashTool && t != agentTool {
			denied = append(denied, t)
		}
	}
	list, err := blockList("disallowedTools", denied)
	if err != nil {
		return "", fix("a disallowedTools entry is not " + err.Error())
	}
	lines = append(lines, list)
	front := strings.Join(lines, "\n")
	if strings.Contains(front, "---") {
		return "", fix("the written frontmatter would contain ---, where Claude Code ends it; remove the ---")
	}
	return front, nil
}

// blockList writes a key with its items as a YAML block list.
func blockList(key string, items []string) (string, error) {
	lines := []string{key + ":"}
	for _, item := range items {
		s, err := quoted(item)
		if err != nil {
			return "", err
		}
		lines = append(lines, "  - "+s)
	}
	return strings.Join(lines, "\n"), nil
}

// jsSpace is the characters that JavaScript's \s matches, which RE2's \s does not.
const jsSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

// claudeFront is the expression that Claude Code 2.1.332 takes the frontmatter of an
// agent file with, after it drops one byte order mark.
var claudeFront = regexp.MustCompile(`(?s)^---` + jsSpace + `*\n(.*?)---` + jsSpace + `*\n?`)

// claudeFrontOf is the text that Claude Code takes as the frontmatter of text: the text
// from the line after the first fence to the first --- anywhere after it.
func claudeFrontOf(text string) (string, bool) {
	m := claudeFront.FindStringSubmatch(strings.TrimPrefix(text, "\ufeff"))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// claudeTools splits tool lists the way the function Zf of Claude Code 2.1.332 does: at
// each comma and each U+0020 outside parentheses, with each item trimmed as JavaScript's
// trim() trims, and empty items dropped. A ( starts the inside and a ) ends it, with no
// count of how many are open, so an unclosed ( swallows everything after it. A
// character that trim() removes is not added at the start of an item.
func claudeTools(lists ...string) []string {
	var out []string
	for _, s := range lists {
		inside := false
		var cur strings.Builder
		push := func() {
			if t := jsTrim(cur.String()); t != "" {
				out = append(out, t)
			}
		}
		for _, r := range s {
			switch r {
			case '(':
				inside = true
				cur.WriteRune(r)
			case ')':
				inside = false
				cur.WriteRune(r)
			case ',':
				if inside {
					cur.WriteRune(r)
					continue
				}
				push()
				cur.Reset()
			case ' ':
				if inside {
					cur.WriteRune(r)
				} else if jsTrim(cur.String()) != "" {
					push()
					cur.Reset()
				}
			default:
				if cur.Len() > 0 || !isJSSpace(r) {
					cur.WriteRune(r)
				}
			}
		}
		push()
	}
	return out
}

// isJSSpace reports whether JavaScript's \s matches r, the characters its trim()
// removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsTrim is s as JavaScript's trim() returns it.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// toolList is a decoded tools value as the function sgn of Claude Code 2.1.332 reads it:
// false for a missing or null value; no entry for an empty string or a value that is no
// string and no list; else the strings of the value split by [claudeTools], and ["*"]
// when one entry is *.
func toolList(v any) ([]string, bool) {
	var items []string
	switch x := v.(type) {
	case nil:
		return nil, false
	case string:
		items = []string{x}
	case []any:
		for _, i := range x {
			if s, ok := i.(string); ok {
				items = append(items, s)
			}
		}
	}
	list := claudeTools(items...)
	if slices.Contains(list, "*") {
		return []string{"*"}, true
	}
	return list, true
}

// denyList is the deny list that the function Bvt of Claude Code 2.1.332 makes of a
// decoded disallowedTools value: nil, no deny list at all, for a missing value and for a
// list with a *.
func denyList(v any, present bool) []string {
	list, ok := toolList(v)
	switch {
	case !ok && !present:
		return nil
	case !ok:
		return []string{}
	case slices.Contains(list, "*"):
		return nil
	}
	return list
}

// checkWritten reads out the way Claude Code does and returns the error fix makes of the
// first difference from what qory wrote: the text Claude Code takes as frontmatter is not
// front, a tools entry is Bash or *, the deny list has no Bash or no Agent, or a key
// other than the three has another value than in source. It is the backstop of the
// checks before it.
func checkWritten(out []byte, front, name string, source map[string]any, fix func(string) error) error {
	taken, ok := claudeFrontOf(string(out))
	if !ok || taken != front+"\n" {
		return fix("Claude Code would read another frontmatter than the one qory wrote")
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(taken), &got); err != nil {
		return fix("the frontmatter that qory wrote does not parse: " + exports.HideValueError(err, "").Error())
	}
	if got["name"] != name {
		return fix("the written name is not " + exports.Value(name, true))
	}
	if tools, _ := toolList(got["tools"]); slices.ContainsFunc(tools, func(t string) bool { return t == "*" || isBash(t) }) {
		return fix("Claude Code would read a Bash entry in tools")
	}
	denied, present := got["disallowedTools"]
	if deny := denyList(denied, present); !slices.Contains(deny, bashTool) || !slices.Contains(deny, agentTool) {
		return fix("Claude Code would not read Bash and Agent in the deny list of disallowedTools")
	}
	for k, v := range source {
		if k == "name" || k == "tools" || k == "disallowedTools" {
			continue
		}
		if !reflect.DeepEqual(got[k], v) {
			return fix("the written value of " + k + " is not the value in the file")
		}
	}
	return nil
}

// isBash reports whether tool is the Bash tool, with or without a rule in parentheses.
func isBash(tool string) bool {
	return tool == bashTool || strings.HasPrefix(tool, bashTool+"(")
}
