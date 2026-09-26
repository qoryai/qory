package render

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/module"
)

// Address is how a delivery path addresses a composed entry: the name the program
// registers an entry of kind and name under when it reads the entry from that path. A
// checkout link and most launch paths register the name as the module wrote it, see
// [Bare]; a plugin puts its own name before it, see [Prefixed]. A renderer passes the
// address of the path it writes for wherever it puts a document in front of the program,
// so a reference in the document resolves to the name the session there answers to, and
// the launch verb prints the same names for a launcher to build its prompt from.
type Address func(kind, name string) string

// Bare is the address of a path where every kind registers under the name the module
// wrote: a checkout link, a Codex home, an OpenCode configuration directory.
func Bare(kind, name string) string { return name }

// Prefixed is the address of a plugin loaded under prefix, where an agent, a skill and a
// command all register as <prefix>:<name>, the way Claude Code registers a plugin's
// entries.
func Prefixed(prefix string) Address {
	return func(kind, name string) string { return prefix + ":" + name }
}

// Addresser is a [Launcher] whose launch path registers entries under other names than
// the modules wrote. A launcher that does not implement it registers bare names on its
// launch path, and [AddressOf] answers [Bare] for it.
type Addresser interface {
	// Address is the address of the launch path, the one [Launcher.Template] starts the
	// program on.
	Address() Address
}

// AddressOf is the address of a runtime's launch path: the runtime's own when it is an
// [Addresser], else [Bare].
func AddressOf(p Runtime) Address {
	if a, ok := p.(Addresser); ok {
		return a.Address()
	}
	return Bare
}

// Addresses lists, per kind, the name each composed agent, skill and command registers
// under at addr, and each bound role beside them as the name of the entry it is bound to,
// leaving out the kinds in skips. entries are the composed entries as <kind>/<name>
// keys, bind the bindings, both as a result or a report contains them. It is what the
// launch verb prints and what [Addressing] states; a kind with nothing in it is absent.
func Addresses(entries []string, bind map[string]string, addr Address, skips []string) map[string]map[string]string {
	out := map[string]map[string]string{}
	put := func(kind, name, entry string) {
		if out[kind] == nil {
			out[kind] = map[string]string{}
		}
		out[kind][name] = addr(kind, entry)
	}
	for _, key := range entries {
		kind, name, _ := strings.Cut(key, "/")
		if !contains(module.Referable, kind) || contains(skips, kind) {
			continue
		}
		put(kind, name, name)
	}
	for role, to := range bind {
		kind, name, _ := strings.Cut(role, "/")
		if contains(skips, kind) {
			continue
		}
		put(kind, name, to)
	}
	return out
}

// Keys lists a result's entries as <kind>/<name>, the form [Addresses] takes.
func Keys(res *compose.Result) []string {
	keys := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		keys = append(keys, e.Kind+"/"+e.Name)
	}
	return keys
}

// Addressing is the section a launch path appends to the instructions it writes: the
// names the session registers the composed agents, skills and commands under on that
// path, and the entry each bound role is, so a session that meets a name a document
// wrote in prose finds the registered one. A reference in a document is already
// resolved; this covers the rest. It is "" when the compose contains none of those kinds
// the runtime places, so a harness of hooks and settings alone adds no section, and it is
// derived from the same address the substitution uses, so the two cannot disagree.
func Addressing(res *compose.Result, addr Address, skips []string) string {
	names := Addresses(Keys(res), res.Bind, addr, skips)
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Names in this session\n\n")
	if addr("skills", "name") == "name" {
		b.WriteString("qory composed this harness, and the session registers its agents, skills and commands under the names the modules wrote, listed below. A role is the entry it is bound to.\n\n")
	} else {
		b.WriteString("qory composed this harness, and the session registers its agents, skills and commands under the names listed below. A document that writes a name without the prefix means the registered one; a role is the entry it is bound to.\n\n")
	}
	kinds := make([]string, 0, len(names))
	for k := range names {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var roles []string
	for _, kind := range kinds {
		var items []string
		for _, name := range sortedNames(names[kind]) {
			if _, bound := res.Bind[kind+"/"+name]; bound {
				roles = append(roles, kind+"/"+name+" is "+names[kind][name])
				continue
			}
			items = append(items, names[kind][name])
		}
		if len(items) > 0 {
			b.WriteString("- " + kind + ": " + strings.Join(items, ", ") + "\n")
		}
	}
	if len(roles) > 0 {
		b.WriteString("- roles: " + strings.Join(roles, "; ") + "\n")
	}
	return b.String()
}

// WithAddressing is the instructions a launch path writes: the composed instructions with
// every reference resolved at addr, then [Addressing] as its own section after a blank
// line. Either part may be empty; the result is "" when both are.
func WithAddressing(res *compose.Result, addr Address, skips []string) string {
	text := res.Substitute(res.Instructions, addr)
	section := Addressing(res, addr, skips)
	switch {
	case text == "":
		return section
	case section == "":
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n" + section
}

// sortedNames lists a map's keys in order.
func sortedNames(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// PlaceEntries puts every composed entry of the kinds in keep into the tree at
// dir/<kind>/<name>, for the path addr addresses. Skills and hooks keep their name, the
// Markdown kinds get ".md" appended. An entry whose documents reference nothing is one
// symlink pointing at the entry in its module. An entry with a reference is placed with
// the references resolved: a single file is written, a skill becomes a real directory
// containing a link per file and directory that contains no reference and a written copy
// of each Markdown file that does, so the module's files stay live wherever nothing had
// to change. Every kind directory in keep is created, with no entry of that kind as well,
// so a checkout link to it resolves. Entries of any other kind are left out, and it is
// the caller's job to pass every kind the runtime reads from the directory.
func PlaceEntries(res *compose.Result, dir string, addr Address, keep ...string) error {
	wanted := map[string]bool{}
	for _, k := range keep {
		wanted[k] = true
		if err := os.MkdirAll(filepath.Join(dir, k), 0o755); err != nil {
			return err
		}
	}
	for _, e := range res.Entries {
		if !wanted[e.Kind] {
			continue
		}
		dst := filepath.Join(dir, e.Kind, e.Name)
		switch e.Kind {
		case "skills", "hooks":
		default:
			dst += ".md"
		}
		if len(e.References) == 0 {
			if err := os.Symlink(e.Path, dst); err != nil {
				return err
			}
			continue
		}
		if err := place(res, e.Path, dst, addr); err != nil {
			return err
		}
	}
	return nil
}

// place puts the file or directory at src at dst for the path addr names, see
// [PlaceEntries]: a link where nothing below src holds a reference, else a written copy
// of each Markdown file that does and a link for everything else.
func place(res *compose.Result, src, dst string, addr Address) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return placeFile(res, src, dst, addr)
	}
	has, err := hasReference(src)
	if err != nil {
		return err
	}
	if !has {
		return os.Symlink(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	children, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := place(res, filepath.Join(src, c.Name()), filepath.Join(dst, c.Name()), addr); err != nil {
			return err
		}
	}
	return nil
}

// placeFile writes the Markdown file at src to dst with its references resolved at addr,
// or links it when it holds none.
func placeFile(res *compose.Result, src, dst string, addr Address) error {
	if strings.HasSuffix(src, ".md") {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if module.HasReference(data) {
			return WriteFile(filepath.Dir(dst), filepath.Base(dst), []byte(res.Substitute(string(data), addr)))
		}
	}
	return os.Symlink(src, dst)
}

// hasReference reports whether any Markdown file under dir, at any depth, holds a
// reference.
func hasReference(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if module.HasReference(data) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

// ReadDocument reads the Markdown entry at path for the path addr addresses, with every
// reference resolved before the frontmatter and body are split, so a renderer that
// rewrites an agent or a command into the program's own shape writes the registered
// names into it.
func ReadDocument(res *compose.Result, addr Address, path string) (module.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return module.Document{}, err
	}
	if module.HasReference(data) {
		data = []byte(res.Substitute(string(data), addr))
	}
	return module.ParseDocument(data, path)
}
