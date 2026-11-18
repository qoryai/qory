package module

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// Referable are the kinds a reference may refer to: the entries a document dispatches by
// name, and the ones a runtime may register under another name than the module wrote.
var Referable = []string{"agents", "commands", "skills"}

// reference matches one reference in a document: ${qory:<kind>/<name>}, the kind plural
// as an exclude names it. The group is whatever stands between the colon and the brace,
// checked by [References].
var reference = regexp.MustCompile(`\$\{qory:([^}]*)\}`)

// Tiers are the names an agent's model may set as a tier, which the compose replaces with
// the model id the tier maps to.
var Tiers = stack.Tiers

// TierOf returns the name a value sets as a tier when the whole value is a tier
// placeholder, ${qory:<name>}: the name not empty and with no slash, which tells it apart
// from a reference. The name may be one that is not in [Tiers]; the caller checks it with
// [IsTier]. This is the one function that parses the placeholder.
func TierOf(value string) (string, bool) {
	m := reference.FindStringSubmatch(value)
	if m == nil || m[0] != value || m[1] == "" || strings.Contains(m[1], "/") {
		return "", false
	}
	return m[1], true
}

// IsTier reports whether name is one of [Tiers].
func IsTier(name string) bool {
	return stack.IsTier(name)
}

// TierList is the tiers for a message: "light, fast, and thinking".
func TierList() string {
	return stack.TierList()
}

// HasControl reports whether a tier's model id or effort contains a line end or another
// control character, which would end the value's line in an agent's frontmatter.
func HasControl(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	})
}

// Escape writes each control character of value as a Go escape, such as \n, so a message
// shows the value on one line and passes nothing to the terminal. A value without one
// comes back as it is.
func Escape(value string) string {
	if !HasControl(value) {
		return value
	}
	q := strconv.Quote(value)
	return strings.ReplaceAll(q[1:len(q)-1], `\"`, `"`)
}

// NotATier is the refusal of a name that is not one of [Tiers], in where, such as
// "model ${qory:heavy}" or "harness.tiers.heavy", with control characters escaped. The
// name prints as [accesskey.ShownName] prints it; the caller passes where with a name or
// a value in it printed as a message prints one.
func NotATier(where, name string) error { return notATier(where, name) }

// notATier is [NotATier] with the lines after the two that it always has.
func notATier(where, name string, more ...string) error {
	return exports.Refuse("Use one of the tiers "+TierList()+".",
		append([]string{Escape(accesskey.ShownName(name, false)) + " is not a tier.", "The name is in " + Escape(where) + "."}, more...)...)
}

// misplacedTier is the refusal of a tier placeholder found where no tier goes; placeholder
// is the placeholder as written. It has [exports.FilePlace] and [exports.ModulePlace] for
// the file and the module that the caller fills in.
func misplacedTier(placeholder string) error {
	return exports.Refuse("Move "+placeholder+" into the model of an agent, or remove it.",
		"A tier placeholder goes only in an agent's model, as model: "+placeholder+".",
		"The placeholder is in "+exports.FilePlace+" of "+exports.ModulePlace+".")
}

// NoTier refuses the first tier placeholder in text, for a document that may contain no
// tier and whose references are not checked here, such as AGENTS.md.
func NoTier(text string) error {
	for _, m := range reference.FindAllString(text, -1) {
		if name, ok := TierOf(m); ok && IsTier(name) {
			return misplacedTier(m)
		}
	}
	return nil
}

// HasReference reports whether data contains anything shaped like a reference, so a
// renderer copies the file with the references resolved instead of linking it.
func HasReference(data []byte) bool { return reference.Match(data) }

// References lists the entries text references, as sorted <kind>/<name> keys, each once.
// A reference is ${qory:<kind>/<name>}: the kind one of [Referable], the name one path
// segment. Anything else between ${qory: and } is an error that contains the reference,
// so a misspelt one is refused instead of reaching a session as it is. The refusal
// prints the reference as [exports.Value] prints it.
func References(text string) ([]string, error) {
	seen := map[string]bool{}
	for _, m := range reference.FindAllStringSubmatch(text, -1) {
		key := m[1]
		if tier, ok := TierOf(m[0]); ok && IsTier(tier) {
			return nil, misplacedTier(m[0])
		}
		kind, name, ok := strings.Cut(key, "/")
		if !ok || !isReferable(kind) {
			ref := exports.Value(m[0], false)
			return nil, exports.Refuse(fmt.Sprintf("Change %s to a reference.", ref),
				fmt.Sprintf("%s is not a reference: after ${qory: comes one of %s, then /<name>.", ref, strings.Join(Referable, ", ")),
				fmt.Sprintf("The reference is in %s of %s.", exports.FilePlace, exports.ModulePlace))
		}
		if name == "" || strings.ContainsAny(name, `/\@ `) || strings.HasPrefix(name, ".") {
			ref := exports.Value(m[0], false)
			return nil, exports.Refuse(fmt.Sprintf("Change %s to a reference.", ref),
				fmt.Sprintf("%s refers to %s, which is not an entry name: a name is one path segment.", ref, accesskey.ShownName(name, true)),
				fmt.Sprintf("The reference is in %s of %s.", exports.FilePlace, exports.ModulePlace))
		}
		seen[key] = true
	}
	if len(seen) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// agentTier returns the tier an agent's frontmatter model sets, "" for none, and the
// agent's text with that model line left out, so the reference check reads everything
// else. A model that is a placeholder of a name that is not a tier is an error. A file
// with no placeholder is not parsed, so an agent without a tier reads as before.
func agentTier(data []byte, name string) (string, []byte, error) {
	if !bytes.Contains(data, []byte("${qory:")) {
		return "", data, nil
	}
	doc, err := ParseDocument(data, name)
	if err != nil {
		return "", data, nil
	}
	model := doc.String("model")
	tier, ok := TierOf(model)
	if !ok {
		return "", data, nil
	}
	if !IsTier(tier) {
		return "", nil, notATier("model "+exports.Value(model, false), tier, "The model is in "+exports.FilePlace+" of "+exports.ModulePlace+".")
	}
	return tier, SetFront(data, "model", ""), nil
}

// Substitute replaces every reference in text with what resolve returns for its kind and
// name. A reference [References] refuses is left as written; the compose refuses the
// document before a renderer sees it.
func Substitute(text string, resolve func(kind, name string) string) string {
	return reference.ReplaceAllStringFunc(text, func(m string) string {
		key := m[len("${qory:") : len(m)-1]
		kind, name, ok := strings.Cut(key, "/")
		if !ok || !isReferable(kind) || name == "" {
			return m
		}
		return resolve(kind, name)
	})
}

// isReferable reports whether kind is one a reference may name.
func isReferable(kind string) bool {
	for _, k := range Referable {
		if k == kind {
			return true
		}
	}
	return false
}

// Documents lists the Markdown files a renderer reads for an entry and a reference may
// stand in: the file itself for an agent, a command or an output style, and every .md
// file under a skill's directory, at any depth, a linked one included. Any other kind
// has none.
func Documents(e Entry) ([]string, error) {
	switch e.Kind {
	case "agents", "commands", "output-styles":
		return []string{e.Path}, nil
	case "skills":
		var files []string
		err := filepath.WalkDir(e.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return exports.HideError(err)
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".md") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		return files, nil
	}
	return nil, nil
}

// documentRefs is what [references] reads from the documents of one entry.
type documentRefs struct {
	// keys are the entries referenced, sorted, each once across the files.
	keys []string
	// tier is the tier an agent's model sets, "" for none.
	tier string
	// front is, per key a frontmatter references, the first file that does.
	front map[string]string
	// in is, per key, the first file that references it, relative to the root.
	in map[string]string
	// paths are an agent's frontmatter paths, nil for none.
	paths []string
	// note is a skill's note, nil for a skill without notes_for.
	note *Note
}

// references reads every document of e and returns what they reference, as sorted keys,
// each once across the files, with the first file of each and the keys a frontmatter
// references, and for an agent the tier its model sets and the globs its paths set. The
// error contains the file, relative to root, as [exports.HidePath] prints it, and the
// reference or the key.
func references(root string, e Entry) (documentRefs, error) {
	var out documentRefs
	files, err := Documents(e)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return out, exports.HideError(err)
		}
		rel, relErr := filepath.Rel(root, f)
		if relErr != nil {
			rel = f
		}
		rel = filepath.ToSlash(rel)
		// shown is the path as the errors print it.
		shown := exports.HidePath(rel)
		if e.Kind == "agents" {
			if out.paths, err = agentPaths(data, rel); err != nil {
				return out, exports.AtPath(shown, err)
			}
			if out.tier, data, err = agentTier(data, rel); err != nil {
				return out, exports.AtPath(shown, err)
			}
		}
		if e.Kind == "skills" && filepath.Base(f) == "SKILL.md" && filepath.Dir(f) == e.Path {
			out.note = skillNote(data, rel, e.Name)
			if out.note != nil && out.note.Err != nil {
				out.note.Err = exports.AtPath(shown, out.note.Err)
			}
		}
		keys, err := References(string(data))
		if err != nil {
			return out, exports.AtPath(shown, err)
		}
		for _, k := range keys {
			if !seen[k] {
				if out.in == nil {
					out.in = map[string]string{}
				}
				out.in[k] = rel
			}
			seen[k] = true
		}
		if text := frontText(data); text != "" {
			keys, _ := References(text)
			for _, k := range keys {
				if _, ok := out.front[k]; ok {
					continue
				}
				if out.front == nil {
					out.front = map[string]string{}
				}
				out.front[k] = rel
			}
		}
	}
	out.keys = sortedSet(seen)
	return out, nil
}

// sortedSet is the keys of set, sorted; nil for none.
func sortedSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// agentPaths returns the globs an agent's frontmatter paths sets: one string, or a list
// of strings, none empty. Nil for an agent without the key, and for a file whose
// frontmatter does not parse, which the renderer refuses. Any other value is an error.
func agentPaths(data []byte, name string) ([]string, error) {
	if !bytes.Contains(data, []byte("paths:")) {
		return nil, nil
	}
	doc, err := ParseDocument(data, name)
	if err != nil {
		return nil, nil
	}
	v, ok := doc.Front["paths"]
	if !ok {
		return nil, nil
	}
	notGlobs := exports.Refuse("Set paths to globs, such as [\"**/*.py\"], or remove it.", "The value is not a glob or a list of globs.",
		"The agent is in "+exports.FilePlace+" of "+exports.ModulePlace+".")
	globs, ok := stringList(v)
	if !ok {
		return nil, notGlobs
	}
	return globs, nil
}

// skillNote returns the note a skill's SKILL.md is: nil for a file whose frontmatter has no
// notes_for key, and for one that does not parse, which the renderer refuses. A note whose
// notes_for is no role or list of roles, or whose paths are missing, empty or no globs, has
// [Note.Err], a refusal with [exports.ModulePlace] and [exports.FilePlace] for the caller
// to fill in.
func skillNote(data []byte, name, skill string) *Note {
	if !bytes.Contains(data, []byte("notes_for:")) {
		return nil
	}
	doc, err := ParseDocument(data, name)
	if err != nil {
		return nil
	}
	v, ok := doc.Front["notes_for"]
	if !ok {
		return nil
	}
	shown := accesskey.ShownName(skill, false)
	where := "The skill is in " + exports.FilePlace + " of " + exports.ModulePlace + "."
	roles, ok := stringList(v)
	if !ok {
		return &Note{Err: exports.Refuse("Set notes_for in "+shown+" of "+exports.ModulePlace+" to a role or roles.",
			"The value is not a role or a list of roles.", where)}
	}
	noPaths := func(cause string) *Note {
		return &Note{Err: exports.Refuse("Add paths to "+shown+" of "+exports.ModulePlace+".", cause, where)}
	}
	raw, ok := doc.Front["paths"]
	if !ok {
		return noPaths("A note needs one path or more.")
	}
	globs, ok := stringList(raw)
	if !ok {
		return noPaths("A note needs one path or more, and the value is not a glob or a list of globs.")
	}
	return &Note{Roles: roles, Paths: globs}
}

// stringList reads v, the value of a frontmatter key paths, as one non-empty string or a
// non-empty list of non-empty strings; the result is false for any other value. An agent's
// paths and a note's paths are read by this function, so they have one grammar.
func stringList(v any) ([]string, bool) {
	var out []string
	switch v := v.(type) {
	case string:
		out = []string{v}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
	}
	if len(out) == 0 || slices.Contains(out, "") {
		return nil, false
	}
	return out, true
}
