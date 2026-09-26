package module

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Referable are the kinds a reference may refer to: the entries a document dispatches by
// name, and the ones a runtime may register under another name than the module wrote.
var Referable = []string{"agents", "commands", "skills"}

// reference matches one reference in a document: ${qory:<kind>/<name>}, the kind plural
// as an exclude names it. The group is whatever stands between the colon and the brace,
// checked by [References].
var reference = regexp.MustCompile(`\$\{qory:([^}]*)\}`)

// HasReference reports whether data contains anything shaped like a reference, so a
// renderer copies the file with the references resolved instead of linking it.
func HasReference(data []byte) bool { return reference.Match(data) }

// References lists the entries text references, as sorted <kind>/<name> keys, each once.
// A reference is ${qory:<kind>/<name>}: the kind one of [Referable], the name one path
// segment. Anything else between ${qory: and } is an error that contains the reference,
// so a misspelt one is refused instead of reaching a session as it is.
func References(text string) ([]string, error) {
	seen := map[string]bool{}
	for _, m := range reference.FindAllStringSubmatch(text, -1) {
		key := m[1]
		kind, name, ok := strings.Cut(key, "/")
		if !ok || !isReferable(kind) {
			return nil, fmt.Errorf("%s is not a reference; after ${qory: comes one of %s, then /<name>", m[0], strings.Join(Referable, ", "))
		}
		if name == "" || strings.ContainsAny(name, `/\@ `) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("%s refers to %q, which is not an entry name; a name is one path segment", m[0], name)
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
				return err
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
// each once across the files. The error names the file, relative to root, and the
// reference.
func references(root string, e Entry) ([]string, error) {
	files, err := Documents(e)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		keys, err := References(string(data))
		if err != nil {
			rel, relErr := filepath.Rel(root, f)
			if relErr != nil {
				rel = f
			}
			return nil, fmt.Errorf("%s: %w", filepath.ToSlash(rel), err)
		}
		for _, k := range keys {
			seen[k] = true
		}
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
