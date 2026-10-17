// Package integration runs the program of an integration as the integration contract
// has it, contracts/integration/v1 of github.com/qoryai/integrations: <program>
// describe prints the integration's description, one JSON document, and every role is
// started as <program> <role> --settings <json> -- [the role's own arguments].
// [Describe] runs the program and reads its answer, [Description.CheckSettings] checks
// a settings document against it, and [CredentialAdapter] is the credential role's
// adapter, a gateway credential definition's adapter word for word. The package contains no table of
// integrations: what one takes and does is defined by its description, so Qory's own
// qory-<name> programs and a machine's own are read the same way.
//
// The contract's repository is not a module this one requires, so the description's
// schema is a copy, description.schema.json beside this file.
package integration

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// descriptionSchema is a copy of contracts/integration/v1/description.schema.json of
// github.com/qoryai/integrations, byte for byte: the contract as of commit 952eec3. A
// new revision of the contract is copied over it.
//
//go:embed description.schema.json
var descriptionSchema []byte

// descriptionURL is the schema's $id, which it is compiled under.
const descriptionURL = "https://qory.dev/contracts/integration/v1/description.schema.json"

// settingsURL is what a description's settings schema is compiled under: a name no
// loader resolves, since the schema refers to nothing outside itself.
const settingsURL = "https://qory.dev/contracts/integration/v1/settings"

// DescribeWait is how long describe may take before the program is stopped and the
// declaration refused. Describe reaches no network, so it answers at once.
const DescribeWait = 10 * time.Second

// describeWait is [DescribeWait], which a test shortens.
var describeWait = DescribeWait

// maxDescription is the most of describe's standard output that is kept, the most raw
// bytes the integration contract takes for a description, 256 KiB; a description is a
// few kilobytes.
const maxDescription = 256 << 10

// maxStandardError is the most of describe's standard error that is kept.
const maxStandardError = 1 << 20

// RoleCredential is the credential role, the one this package expands.
const RoleCredential = "credential"

// Description is what describe printed, read and checked against the contract.
type Description struct {
	// Name is the integration's own name, which a machine may declare it under or not.
	Name string
	// ProgramVersion is the program's version, as it was built.
	ProgramVersion string
	// Roles lists every role the program plays, known here or not, sorted.
	Roles []string
	// Credential is the credential role, nil when the program plays none.
	Credential *Credential

	settings *jsonschema.Schema
	secrets  []string // the settings' writeOnly properties, sorted
}

// Credential is the credential role of a description: what Forager definition it
// expands to contains beside its adapter.
type Credential struct {
	// Argument is a regular expression, RE2, the policy's argument matches whole.
	Argument string
	// Hosts are the hosts the adapter answers for, the most an answer may claim.
	Hosts []string
}

// contract is the description's schema, compiled once.
var contract = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(descriptionSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(descriptionURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(descriptionURL)
})

// Describe runs program describe, as Forager starts an adapter: with this process's
// environment, no standard input, and / as its working directory. program is a path.
// The program runs in a process group of its own, and the whole group is stopped when
// describe returns, so no process of its group outlives it. Describe refuses a program
// that does not exit 0 within [DescribeWait], with the one line it wrote on standard
// error, and an answer that is not one JSON document within the contract's limits that
// the contract's schema accepts. The answer is checked in this order, and the first
// rule broken is the one refusal: the raw size; UTF-8; the JSON; a lone surrogate
// escaped in a string or a member name repeated in one object, the first in the
// document, as [scan] has them; the values the document holds, how deep it nests, and
// the first string or member name too long, as [beyond] has them; then the schema.
func Describe(ctx context.Context, program string) (*Description, error) {
	ctx, cancel := context.WithTimeout(ctx, describeWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "describe")
	cmd.Dir = "/"
	// The access key's variables and the run key are Forager's alone: describe never
	// receives them.
	cmd.Env = accesskey.WithoutVariables(os.Environ())
	stdout, stderr := &capped{limit: maxDescription}, &capped{limit: maxStandardError}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// A process left holding the output is not waited for past the limit.
	cmd.WaitDelay = time.Second
	group(cmd)
	err := cmd.Run()
	stopGroup(cmd)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s describe did not answer within %s", program, describeWait)
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return nil, fmt.Errorf("%s describe exited and left a process that keeps its output open", program)
		}
		// The line is the program's own to word, and never a secret: the contract says so.
		if line := firstLine(stderr.String()); line != "" {
			return nil, fmt.Errorf("%s describe: %w: %s", program, err, line)
		}
		return nil, fmt.Errorf("%s describe: %w", program, err)
	}
	if stdout.over {
		return nil, fmt.Errorf("%s describe printed more than %d bytes; a description is a few kilobytes", program, maxDescription)
	}
	raw := stdout.buf.Bytes()
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf(refusedEncoding, program)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s describe did not print one JSON document: %w", program, err)
	}
	if err := scan(program, raw); err != nil {
		return nil, err
	}
	if err := beyond(program, doc); err != nil {
		return nil, err
	}
	schema, err := contract()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%s describe printed a description the integration contract refuses: %s", program, failures(err, true))
	}
	d, err := read(doc.(map[string]any))
	if err != nil {
		return nil, fmt.Errorf("%s describe: %w", program, err)
	}
	return d, nil
}

// The limits of a description the integration contract states in words and its schema
// cannot express: a string or a member name of at most maxText bytes of UTF-8, where the
// schema counts characters; at most maxValues JSON values in the decoded document, the
// document itself, each array entry and each member's value counting one, a container
// or not, and member names not counting; and arrays and objects nested at most maxDepth
// levels deep, the top-level object being level 1.
const (
	maxText   = 16384
	maxValues = 4096
	maxDepth  = 64
)

// The refusals of a description past a limit of [beyond]. Each names where the limit
// is broken, never what is there.
const (
	refusedString = "%s describe printed a description the integration contract refuses: the string at %s is more than 16384 bytes"
	refusedName   = "%s describe printed a description the integration contract refuses: the member name at %s is more than 16384 bytes"
	refusedValues = "%s describe printed a description the integration contract refuses: it holds more than 4096 values"
	refusedDepth  = "%s describe printed a description the integration contract refuses: it nests arrays and objects more than 64 deep"
)

// The refusals of a description Qory Apiary's reader takes otherwise than Go's: Go
// reads invalid UTF-8 and an escaped lone surrogate as U+FFFD, which Qory Apiary
// refuses, and keeps the last of a repeated member name, where Qory Apiary keeps the
// first. qory refuses all three, so no description reads one way here and another
// there.
const (
	refusedEncoding        = "%s describe did not print one JSON document: it is not valid UTF-8"
	refusedSurrogateString = `%s describe did not print one JSON document: the string at %s holds a \u escape of a lone surrogate`
	refusedSurrogateName   = `%s describe did not print one JSON document: the member name at %s holds a \u escape of a lone surrogate`
	refusedRepeated        = "%s describe printed a description the integration contract refuses: the object at %s names %s twice"
)

// scan refuses a description raw, as program printed it, one JSON document of valid
// UTF-8, that holds a \u escape of a lone surrogate in a string or a member name, or an
// object that names a member twice, the names compared as they read; or returns nil.
// The first of these in the document is the one refused. It reads raw once, token by
// token: the raw text of each string token is what lies between the decoder's offset
// before the token and after it, a colon, a comma and space at most before the string,
// and that text is scanned once for its escapes. Each open object holds the set of the
// names it has read, dropped when it closes.
func scan(program string, raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var open []*container
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s describe did not print one JSON document: %w", program, err)
		}
		var top *container
		if len(open) > 0 {
			top = open[len(open)-1]
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			open = open[:len(open)-1]
			if len(open) > 0 && open[len(open)-1].object {
				open[len(open)-1].named = false
			}
			continue
		}
		name := top != nil && top.object && !top.named
		if top != nil && !top.object {
			top.index++
		}
		switch t := tok.(type) {
		case json.Delim:
			c := &container{object: t == '{', index: -1}
			if c.object {
				c.names = map[string]struct{}{}
			}
			open = append(open, c)
			continue
		case string:
			lone := loneSurrogate(raw[start:dec.InputOffset()])
			switch {
			case name && lone:
				return fmt.Errorf(refusedSurrogateName, program, Printable(cmp.Or(pointer(open, false)+"/"+pointerToken(t), "/")))
			case lone:
				return fmt.Errorf(refusedSurrogateString, program, Printable(cmp.Or(pointer(open, true), "/")))
			case name:
				if _, twice := top.names[t]; twice {
					return fmt.Errorf(refusedRepeated, program, Printable(cmp.Or(pointer(open, false), "/")), Printable(t))
				}
				top.names[t] = struct{}{}
				top.name, top.named = t, true
				continue
			}
		}
		// A value other than an array or an object ends here.
		if top != nil && top.object {
			top.named = false
		}
	}
}

// container is an array or an object [scan] is reading: an array's entry being read,
// or an object's member, whether its name is read, and the names read before it.
type container struct {
	object bool
	index  int
	name   string
	named  bool
	names  map[string]struct{}
}

// pointer is the JSON pointer of the value [scan] is reading inside open, or, without
// last, of the innermost container itself.
func pointer(open []*container, last bool) string {
	if !last {
		open = open[:len(open)-1]
	}
	var at strings.Builder
	for _, c := range open {
		if c.object {
			at.WriteString("/" + pointerToken(c.name))
		} else {
			at.WriteString("/" + strconv.Itoa(c.index))
		}
	}
	return at.String()
}

// loneSurrogate reports whether text, a JSON string token as it was printed, with at
// most a colon, a comma and space before it, holds a \u escape of a surrogate that is
// not a pair: a high surrogate, D800 to DBFF, not followed at once by an escaped low
// one, DC00 to DFFF, or a low one standing alone. Every other escape is two bytes, so
// a backslash read as an escape's first byte is never mistaken for one.
func loneSurrogate(text []byte) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		r, ok := escaped(text[i:])
		if !ok {
			i++ // the escaped character
			continue
		}
		switch {
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		case r >= 0xD800 && r <= 0xDBFF:
			low, ok := escaped(text[i+6:])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 11
		default:
			i += 5
		}
	}
	return false
}

// escaped is the code unit of the \u escape text starts with, and false when it starts
// with none.
func escaped(text []byte) (rune, bool) {
	if len(text) < 6 || text[0] != '\\' || text[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(string(text[2:6]), 16, 16)
	return rune(n), err == nil
}

// beyond refuses a description doc, as program printed it, past a limit of the
// integration contract its schema cannot express, or returns nil. One limit is refused,
// the first broken of: the values, then the depth, then the strings and member names,
// the first too long in the order [walker.walk] visits them.
func beyond(program string, doc any) error {
	var w walker
	if !w.walk(doc, 0) {
		return fmt.Errorf(refusedValues, program)
	}
	if w.deepest > maxDepth {
		return fmt.Errorf(refusedDepth, program)
	}
	if w.long != "" {
		return fmt.Errorf(w.long, program, Printable(cmp.Or(w.at, "/")))
	}
	return nil
}

// walker counts the values of a description, notes how deep it nests, and keeps the
// first string or member name longer than [maxText] bytes: its refusal and its JSON
// pointer. path is the pointer's tokens of the value walked, joined only for the one
// kept, so a long name is not copied into the pointer of every value under it.
type walker struct {
	values, deepest int
	path            []string
	long, at        string
}

// walk visits v, inside a container nested level deep, and every value under it, depth
// first: an array's entries in order, an object's members in the order of their names,
// each name before its value. It stops, returning false, at the value past [maxValues],
// so it visits at most one more than that.
func (w *walker) walk(v any, level int) bool {
	w.values++
	if w.values > maxValues {
		return false
	}
	switch v := v.(type) {
	case string:
		w.note(len(v), refusedString)
	case []any:
		w.deepest = max(w.deepest, level+1)
		for i, e := range v {
			if !w.member(strconv.Itoa(i), e, level+1, false) {
				return false
			}
		}
	case map[string]any:
		w.deepest = max(w.deepest, level+1)
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if !w.member(name, v[name], level+1, true) {
				return false
			}
		}
	}
	return true
}

// member walks v under token, an array's index or, when named, an object's member name,
// which is checked before v.
func (w *walker) member(token string, v any, level int, named bool) bool {
	w.path = append(w.path, token)
	defer func() { w.path = w.path[:len(w.path)-1] }()
	if named {
		w.note(len(token), refusedName)
	}
	return w.walk(v, level)
}

// note keeps refusal and the pointer of the value walked when n bytes are past
// [maxText] and nothing is kept yet.
func (w *walker) note(n int, refusal string) {
	if n <= maxText || w.long != "" {
		return
	}
	var at strings.Builder
	for _, token := range w.path {
		at.WriteString("/" + pointerToken(token))
	}
	w.long, w.at = refusal, at.String()
}

// maxLine is the most runes of a program's text an error or a line quotes.
const maxLine = 200

// firstLine is the first line of what a program wrote on standard error, trimmed, as
// [Printable] has it.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return Printable(strings.TrimSpace(line))
}

// Printable is text a program wrote, a line of its standard error or a string of its
// description, as it is printed to a terminal: at most [maxLine] runes, with ... after
// what is cut, and each character that does not print as ?, so it stays one line and
// moves no cursor.
func Printable(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxLine {
			b.WriteString("...")
			break
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// read takes a description the contract's schema accepted, so every field it reads is
// there and of its type, and compiles its settings schema.
func read(doc map[string]any) (*Description, error) {
	d := &Description{Name: doc["name"].(string), ProgramVersion: doc["program_version"].(string)}
	roles := doc["roles"].(map[string]any)
	for name := range roles {
		d.Roles = append(d.Roles, name)
	}
	sort.Strings(d.Roles)
	if role, ok := roles[RoleCredential].(map[string]any); ok {
		d.Credential = &Credential{Argument: role["argument"].(string)}
		for _, h := range role["hosts"].([]any) {
			d.Credential.Hosts = append(d.Credential.Hosts, h.(string))
		}
	}
	settings := doc["settings"].(map[string]any)
	if v, ok := settings["$schema"]; ok && v != draft2020 && v != draft2020+"#" {
		return nil, fmt.Errorf("the settings schema is written in %s; the integration contract takes draft 2020-12, %s", Printable(fmt.Sprint(v)), draft2020)
	}
	// A secret is a property of the settings themselves and never a nested one, so the
	// settings' own properties are where writeOnly marks one, and anywhere else it marks
	// what no check here could keep off a command line.
	if at := misplacedWriteOnly(settings, "", true, false); at != "" {
		return nil, fmt.Errorf("the settings schema marks %s writeOnly; a secret is a property of the settings themselves, /properties/<name>", at)
	}
	props, _ := settings["properties"].(map[string]any)
	for name, p := range props {
		if s, ok := p.(map[string]any); ok && s["writeOnly"] == true {
			d.secrets = append(d.secrets, name)
		}
	}
	sort.Strings(d.secrets)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(settingsURL, settings); err != nil {
		return nil, fmt.Errorf("the settings schema: %w", err)
	}
	s, err := c.Compile(settingsURL)
	if err != nil {
		return nil, fmt.Errorf("the settings schema does not compile: %w", err)
	}
	d.settings = s
	return d, nil
}

// CheckSettings checks a settings document, one JSON object, against the description's
// settings schema. A value for a writeOnly property is refused before anything else,
// since every role is started with the settings on its command line; the error contains
// the setting and the <name>_file that takes its place. No error contains a value of the
// document: each contains a setting and the rule it breaks.
func (d *Description) CheckSettings(settings []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(settings))
	if err != nil {
		return err
	}
	if m, ok := doc.(map[string]any); ok {
		for _, name := range d.secrets {
			if _, set := m[name]; set {
				return fmt.Errorf("settings.%s is a secret, and the settings go on a command line; set %s_file, the path of a file that contains it, in its place", name, name)
			}
		}
	}
	if err := d.settings.Validate(doc); err != nil {
		return fmt.Errorf("the settings are not what %s takes: %s", d.Name, failures(err, false))
	}
	return nil
}

// CredentialAdapter is the adapter of Forager definition the credential role
// expands to: <program> credential --settings <json> -- ${argument}, with every $ of
// the settings written as [DollarEscape], JSON's escape for it, since Forager
// replaces ${argument} wherever it stands in an argument and a setting reaches the
// program as it was written.
func CredentialAdapter(program string, settings []byte) []string {
	return []string{program, RoleCredential, "--settings", strings.ReplaceAll(string(settings), "$", DollarEscape), "--", "${argument}"}
}

// DollarEscape is JSON's escape for $, the six characters backslash, u, 0, 0, 2 and 4,
// which a JSON reader reads as $ and Forager's ${argument} never matches.
const DollarEscape = `\` + "u0024"

// draft2020 is the one JSON Schema draft a settings schema is written in, the one its
// $schema names when it names one, with or without an empty fragment, #.
const draft2020 = "https://json-schema.org/draft/2020-12/schema"

// subschema, subschemas and schemaMaps are the keywords of JSON Schema whose value is a
// schema, a list of schemas, and a mapping from a name to a schema.
var (
	subschema  = []string{"additionalProperties", "propertyNames", "items", "contains", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema", "additionalItems"}
	subschemas = []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"}
	schemaMaps = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"}
)

// misplacedWriteOnly is where under the schema s, at a JSON pointer, writeOnly marks
// anything other than a property of the settings themselves, or "" when it marks
// nothing else. root is true for the settings schema, and secret for a schema writeOnly
// may mark: a property of the settings schema's own properties.
func misplacedWriteOnly(s any, at string, root, secret bool) string {
	m, ok := s.(map[string]any)
	if !ok {
		return ""
	}
	if m["writeOnly"] == true && !secret {
		return cmp.Or(at, "/")
	}
	for _, k := range subschema {
		if found := misplacedWriteOnly(m[k], at+"/"+k, false, false); found != "" {
			return found
		}
	}
	for _, k := range subschemas {
		list, _ := m[k].([]any)
		for i, v := range list {
			if found := misplacedWriteOnly(v, fmt.Sprintf("%s/%s/%d", at, k, i), false, false); found != "" {
				return found
			}
		}
	}
	for _, k := range schemaMaps {
		named, _ := m[k].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(named)) {
			if found := misplacedWriteOnly(named[name], at+"/"+k+"/"+pointerToken(name), false, root && k == "properties"); found != "" {
				return found
			}
		}
	}
	return ""
}

// pointerToken is a name as one token of a JSON pointer, ~ and / escaped.
func pointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// failures is a validation error on one line: each rule the document breaks, where.
// With values false a rule is named by its keyword alone, since a settings document
// may hold what is not to be printed; a description holds nothing of the kind, and the
// library's own words, which quote it, are kept.
func failures(err error, values bool) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var out []string
	if values {
		// The first line names the schema; each after it is one rule, indented by depth.
		lines := strings.Split(ve.Error(), "\n")
		for _, l := range lines[1:] {
			out = append(out, strings.TrimPrefix(strings.TrimSpace(l), "- "))
		}
		return strings.Join(out, "; ")
	}
	for _, l := range broken(ve) {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}

// broken names the rules under e a settings document breaks, each by the setting and
// the keyword, never the value, every name as [Printable] has it. The alternatives of a oneOf or an anyOf are one rule,
// "or" between them; a property's name the schema does not take is named, since it is
// the document's key and not its value.
func broken(e *jsonschema.ValidationError) []string {
	at := located(e.InstanceLocation)
	switch k := e.ErrorKind.(type) {
	case *kind.PropertyNames:
		loc := e.InstanceLocation
		if p, ok := propertiesPath(e.SchemaURL); ok && len(p) == len(loc) {
			loc = p
		}
		return []string{located(loc) + "." + Printable(k.Property) + " is not a setting it takes"}
	case *kind.OneOf, *kind.AnyOf:
		if len(e.Causes) > 0 {
			var alternatives []string
			for _, c := range e.Causes {
				alternatives = append(alternatives, strings.Join(broken(c), " and "))
			}
			return []string{strings.Join(alternatives, ", or ")}
		}
	}
	if len(e.Causes) > 0 {
		var out []string
		for _, c := range e.Causes {
			out = append(out, broken(c)...)
		}
		return out
	}
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		var out []string
		for _, m := range k.Missing {
			out = append(out, at+"."+Printable(m)+" is required")
		}
		return out
	case *kind.DependentRequired:
		var out []string
		for _, m := range k.Missing {
			out = append(out, at+"."+Printable(m)+" is required with "+at+"."+Printable(k.Prop))
		}
		return out
	case *kind.AdditionalProperties:
		var out []string
		for _, p := range k.Properties {
			out = append(out, at+"."+Printable(p)+" is not a setting it takes")
		}
		return out
	case *kind.Type:
		return []string{fmt.Sprintf("%s is %s, where %s is wanted", at, k.Got, strings.Join(k.Want, " or "))}
	}
	var keyword []string
	for _, k := range e.ErrorKind.KeywordPath() {
		keyword = append(keyword, Printable(k))
	}
	return []string{at + " breaks the schema's " + strings.Join(keyword, "/")}
}

// located is a place in a settings document, settings.a.b, each name as [Printable]
// has it: a name is the document's or the description's, and both are printed.
func located(path []string) string {
	out := []string{"settings"}
	for _, p := range path {
		out = append(out, Printable(p))
	}
	return strings.Join(out, ".")
}

// propertiesPath is the object a propertyNames keyword applies to, read from the
// keyword's own location when that is a chain of properties: #/properties/a/propertyNames
// is the object at a. The library's instance location of a propertyNames error shares
// its memory with the locations of the properties validated after it, which may
// overwrite it, and the keyword's location holds the same path.
func propertiesPath(schemaURL string) ([]string, bool) {
	_, frag, ok := strings.Cut(schemaURL, "#")
	if !ok {
		return nil, false
	}
	frag, ok = strings.CutSuffix(frag, "/propertyNames")
	if !ok {
		return nil, false
	}
	var path []string
	tokens := strings.Split(strings.TrimPrefix(frag, "/"), "/")
	if frag == "" {
		tokens = nil
	}
	if len(tokens)%2 != 0 {
		return nil, false
	}
	for i := 0; i < len(tokens); i += 2 {
		if tokens[i] != "properties" {
			return nil, false
		}
		path = append(path, strings.ReplaceAll(strings.ReplaceAll(tokens[i+1], "~1", "/"), "~0", "~"))
	}
	return path, true
}

// capped keeps the first limit bytes written to it and drops the rest, noting that it
// did, so a program that prints without end holds no more than that. The buffer is a
// field and not embedded, so a copy into capped goes through Write and never through
// the buffer's ReadFrom.
type capped struct {
	limit int
	buf   bytes.Buffer
	over  bool
}

// Write keeps what fits and reports every byte written, so the program is not stopped
// by a short write before its status is known.
func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); len(p) > room {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.buf.Write(p)
}

// String is what was kept.
func (c *capped) String() string { return c.buf.String() }
