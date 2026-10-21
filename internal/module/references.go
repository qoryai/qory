package module

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
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
var Tiers = []string{"light", "fast", "thinking"}

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
	for _, t := range Tiers {
		if t == name {
			return true
		}
	}
	return false
}

// TierList is the tiers for a message: "light, fast and thinking".
func TierList() string {
	return strings.Join(Tiers[:len(Tiers)-1], ", ") + " and " + Tiers[len(Tiers)-1]
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
func NotATier(where, name string) error {
	return fmt.Errorf("%s: use one of the tiers %s; %s is not a tier", Escape(where), TierList(), Escape(accesskey.ShownName(name, false)))
}

// misplacedTier is the refusal of a tier placeholder found where no tier goes; placeholder
// is the placeholder as written.
func misplacedTier(placeholder string) error {
	return fmt.Errorf("%s is a tier placeholder; a tier goes only in an agent's model, as model: %s", placeholder, placeholder)
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
			return nil, fmt.Errorf("%s is not a reference; after ${qory: comes one of %s, then /<name>", exports.Value(m[0], false), strings.Join(Referable, ", "))
		}
		if name == "" || strings.ContainsAny(name, `/\@ `) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("%s refers to %s, which is not an entry name; a name is one path segment", exports.Value(m[0], false), accesskey.ShownName(name, true))
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
		return "", nil, NotATier("model "+exports.Value(model, false), tier)
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

// references reads every document of e and returns what they reference, as sorted keys,
// each once across the files, and for an agent the tier its model sets, "" for none. The
// error contains the file, relative to root, as [exports.HidePath] prints it, and the
// reference.
func references(root string, e Entry) ([]string, string, error) {
	files, err := Documents(e)
	if err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	var tier string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, "", exports.HideError(err)
		}
		rel, relErr := filepath.Rel(root, f)
		if relErr != nil {
			rel = f
		}
		rel = filepath.ToSlash(rel)
		// shown is the path as the errors print it.
		shown := exports.HidePath(rel)
		if e.Kind == "agents" {
			if tier, data, err = agentTier(data, rel); err != nil {
				return nil, "", fmt.Errorf("%s: %w", shown, err)
			}
		}
		keys, err := References(string(data))
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", shown, err)
		}
		for _, k := range keys {
			seen[k] = true
		}
	}
	if len(seen) == 0 {
		return nil, tier, nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, tier, nil
}
