// Package exports reads the exports section of a repository's qory.yaml: the stacks and
// the modules the repository publishes for other repositories, each by name, and the
// directories they live in.
//
//	exports:
//	  dir: ./harness            # optional; where stacks/ and modules/ are
//	  stacks: [nextjs]          # each at <dir>/stacks/<name>/qory-stack.yaml
//	  modules: [core, nextjs]   # each at <dir>/modules/<name>/qory-module.yaml
//
// A consumer selects an export instead of a directory, {git: <url>, ref: <ref>, stack:
// nextjs}, so the publisher may move its directories and only the section changes. The
// section is the whole public surface: a stack or a module not listed is the publisher's
// own, whatever directory it is in.
//
// dir is where the exports are read from, relative to the repository root. Absent, the
// stacks are under stacks/ and the modules under modules/ at the root. As one string it
// is the directory that contains both, <dir>/stacks and <dir>/modules. As a map it sets
// each directory on its own, {stacks: ./stacks, modules: ./lib/modules}, and the names
// are read directly under them. [Read] reads the section of the qory.yaml in a directory,
// [Exports.Stack] and [Exports.Module] find one export, and [Exports.Verify] checks that
// every export is there, which is how the publisher's own compose keeps the section true.
package exports

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"
)

// FileName is the configuration's file name, the qory.yaml the section is read from.
const FileName = "qory.yaml"

// AltFileName is the configuration's second name, read exactly as [FileName]; a
// directory contains one of the two.
const AltFileName = "harness.yaml"

// File returns the configuration file dir contains under either name, "" for none, and
// an error for a directory that contains both.
func File(dir string) (string, error) {
	var found string
	for _, name := range []string{FileName, AltFileName} {
		path := filepath.Join(dir, name)
		if !isFile(path) {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("%s contains both %s and %s; a directory contains one of the two", dir, FileName, AltFileName)
		}
		found = path
	}
	return found, nil
}

// APIVersion is the format version this qory writes and the newest it reads, the same one
// every document declares.
const APIVersion = "qory.dev/v1alpha1"

// RetiredAPIVersions maps each retired spelling of a version of the format qory reads to
// the version a document that declares it is read as. Every reader keeps the declared
// version beside the result, so a compose can report that the document wants its line
// rewritten.
var RetiredAPIVersions = map[string]string{"qory.ai/v1alpha1": APIVersion}

// ResolveAPIVersion returns the version a document declaring version is read as:
// [APIVersion] for itself and for a retired spelling of it, and for any other version an
// error that contains it and the ones read. A reader stores the result in the document's
// place and, when it differs from what the document declared, keeps the declared spelling
// beside it, so the compose can report which files want rewriting. A reader prefixes the
// error with the file.
func ResolveAPIVersion(version string) (string, error) {
	if version == APIVersion {
		return version, nil
	}
	if current, ok := RetiredAPIVersions[version]; ok {
		return current, nil
	}
	return "", fmt.Errorf("apiVersion %s is not one this qory reads; versions: %s", Value(version, true), APIVersion)
}

// StackFileName and ModuleFileName are what an exported directory contains: a stack its
// qory-stack.yaml, a module its qory-module.yaml.
const (
	StackFileName  = "qory-stack.yaml"
	ModuleFileName = "qory-module.yaml"
)

// DefaultStacksDir and DefaultModulesDir are where the exports are read from when dir
// is absent, relative to the repository root.
const (
	DefaultStacksDir  = "stacks"
	DefaultModulesDir = "modules"
)

// Dirs is the dir key: where the stacks and where the modules are, each relative to the
// repository root. In the file it is one string, the directory that contains stacks/ and
// modules/, or a map that sets each directory on its own.
type Dirs struct {
	// Stacks is the directory the exported stacks are under, one directory per stack.
	Stacks string `yaml:"stacks,omitempty"`
	// Modules is the directory the exported modules are under, one directory per module.
	Modules string `yaml:"modules,omitempty"`
}

// UnmarshalYAML accepts the string form, which appends stacks and modules to the one
// directory, and the map form, which sets each directory as it is.
func (d *Dirs) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		if one == "" {
			return errors.New("dir is empty; it is a directory relative to the repository root")
		}
		*d = Dirs{Stacks: filepath.Join(one, DefaultStacksDir), Modules: filepath.Join(one, DefaultModulesDir)}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("dir is one directory that contains stacks/ and modules/, or a map that sets the stacks and modules directories")
	}
	type plain Dirs
	var m plain
	if err := n.Decode(&m); err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "stacks" && k != "modules" {
			return fmt.Errorf("dir has the key %s, which a dir map does not have; its keys are stacks and modules", accesskey.ShownName(k, true))
		}
	}
	*d = Dirs(m)
	return nil
}

// Section is the exports key as written. A configuration reader embeds it, so a
// misspelled key inside it is refused with the rest of the file; [Section.Validate]
// checks what the decoder cannot.
type Section struct {
	// Dir is where the exports are, nil for the defaults.
	Dir *Dirs `yaml:"dir,omitempty"`
	// Stacks are the names of the exported stacks.
	Stacks []string `yaml:"stacks,omitempty"`
	// Modules are the names of the exported modules.
	Modules []string `yaml:"modules,omitempty"`
}

// Validate refuses a section that lists nothing and sets no directory, a name that is
// not one path segment, a name listed twice, and a directory that is absolute, empty or
// outside the repository.
func (s *Section) Validate() error {
	if s.Dir == nil && len(s.Stacks) == 0 && len(s.Modules) == 0 {
		return errors.New("exports lists no stacks and no modules")
	}
	if s.Dir != nil {
		for _, d := range []struct{ key, dir string }{{"stacks", s.Dir.Stacks}, {"modules", s.Dir.Modules}} {
			if d.dir == "" {
				return fmt.Errorf("exports.dir sets no %s directory; the map form sets both", d.key)
			}
			clean := filepath.Clean(d.dir)
			if filepath.IsAbs(d.dir) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("exports.dir %s %s is not a directory inside the repository", d.key, Value(d.dir, true))
			}
		}
	}
	for _, list := range []struct {
		key   string
		names []string
	}{{"stacks", s.Stacks}, {"modules", s.Modules}} {
		seen := map[string]bool{}
		for _, name := range list.names {
			if !segment(name) {
				return fmt.Errorf("exports.%s lists %s, which is not one path segment; a name contains no slash, backslash, @ or leading dot", list.key, accesskey.ShownName(name, true))
			}
			if seen[name] {
				return fmt.Errorf("exports.%s lists %s twice", list.key, accesskey.ShownName(name, false))
			}
			seen[name] = true
		}
	}
	return nil
}

// At returns the section resolved for the repository rooted at root, whose qory.yaml is
// file: the directories defaulted, each relative to root.
func (s *Section) At(root, file string) *Exports {
	e := &Exports{Root: root, File: file, Stacks: s.Stacks, Modules: s.Modules, Dir: Dirs{Stacks: DefaultStacksDir, Modules: DefaultModulesDir}}
	if s.Dir != nil {
		e.Dir = Dirs{Stacks: filepath.Clean(s.Dir.Stacks), Modules: filepath.Clean(s.Dir.Modules)}
	}
	return e
}

// Exports is what one repository publishes, read from its qory.yaml and resolved.
type Exports struct {
	// Root is the repository root the directories are relative to.
	Root string
	// File is the qory.yaml the section was read from.
	File string
	// Stacks and Modules are the exported names, in the order the file lists them.
	Stacks  []string
	Modules []string
	// Dir is where the exports are, relative to Root, defaulted when the file sets none.
	Dir Dirs
}

// Stack returns the directory of the exported stack, relative to [Exports.Root], and an
// error that lists the stacks the repository exports when name is not one of them.
func (e *Exports) Stack(name string) (string, error) {
	if !contains(e.Stacks, name) {
		return "", fmt.Errorf("exports no stack named %s; %s", accesskey.ShownName(name, false), listText("stacks", e.Stacks))
	}
	return filepath.Join(e.Dir.Stacks, name), nil
}

// Module returns the directory of the exported module, relative to [Exports.Root], and an
// error that lists the modules the repository exports when name is not one of them.
func (e *Exports) Module(name string) (string, error) {
	if !contains(e.Modules, name) {
		return "", fmt.Errorf("exports no module named %s; %s", accesskey.ShownName(name, false), listText("modules", e.Modules))
	}
	return filepath.Join(e.Dir.Modules, name), nil
}

// Verify checks that every export is there: a qory-stack.yaml in each stack's directory,
// a qory-module.yaml in each module's. The error contains the file that is missing and
// the export that wants it.
func (e *Exports) Verify() error {
	for _, name := range e.Stacks {
		dir, _ := e.Stack(name)
		if !isFile(filepath.Join(e.Root, dir, StackFileName)) {
			return fmt.Errorf("%s: exports.stacks lists %s, and %s contains no %s", e.File, accesskey.ShownName(name, false), HidePath(dir), StackFileName)
		}
	}
	for _, name := range e.Modules {
		dir, _ := e.Module(name)
		if !isFile(filepath.Join(e.Root, dir, ModuleFileName)) {
			return fmt.Errorf("%s: exports.modules lists %s, and %s contains no %s", e.File, accesskey.ShownName(name, false), HidePath(dir), ModuleFileName)
		}
	}
	return nil
}

// document is what [Read] decodes: the apiVersion and the exports section, with every
// other key of the file collected and left unread, so the section is checked strictly
// and the rest of the file is the configuration reader's to check.
type document struct {
	APIVersion string         `yaml:"apiVersion"`
	Qory       string         `yaml:"qory,omitempty"`
	Exports    *Section       `yaml:"exports,omitempty"`
	Rest       map[string]any `yaml:",inline"`
}

// Read reads the exports section of the qory.yaml in root, under either of its names,
// and returns it resolved, or nil when root contains no such file or the file has no
// exports section. A file that cannot be decoded, one with another apiVersion, and a
// section that does not validate are errors that contain the file; a file that declares
// no apiVersion is read as [APIVersion], the newest format, as the configuration reads
// it.
func Read(root string) (*Exports, error) {
	doc, file, err := read(root)
	if err != nil || doc == nil || doc.Exports == nil {
		return nil, err
	}
	if err := doc.Exports.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc.Exports.At(root, file), nil
}

// Range returns the qory key of the qory.yaml in root as the file writes it, "" when
// root contains no such file or the file sets no range. It is the range every stack the
// repository delivers must satisfy, joined with the stack's own by the stack reader, so
// the repository states its floor once. Range does not parse the value; the stack reader
// parses it and reports the file.
func Range(root string) (value, file string, err error) {
	doc, file, err := read(root)
	if err != nil || doc == nil {
		return "", "", err
	}
	return doc.Qory, file, nil
}

// read decodes the qory.yaml in root, under either of its names, and returns it with
// its path, or nil for a root without one. The apiVersion is resolved as the
// configuration resolves it.
func read(root string) (*document, string, error) {
	file, err := File(root)
	if err != nil || file == "" {
		return nil, "", err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, "", err
	}
	if err := RefuseAliases(file, data); err != nil {
		return nil, "", err
	}
	var doc document
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, "", DecodeError(file, err)
	}
	if doc.APIVersion == "" {
		doc.APIVersion = APIVersion
	}
	if doc.APIVersion, err = ResolveAPIVersion(doc.APIVersion); err != nil {
		return nil, "", fmt.Errorf("%s: %w", file, err)
	}
	return &doc, file, nil
}

// ModulesDir is the directory a module listed without a source is read from, relative
// to the repository root: what the root's exports.dir sets, else modules. The error is a
// qory.yaml at root that cannot be read.
func ModulesDir(root string) (string, error) {
	e, err := Read(root)
	if err != nil {
		return "", err
	}
	if e == nil {
		return DefaultModulesDir, nil
	}
	return e.Dir.Modules, nil
}

// UnknownKey is the decoder's report of a key a document has no field for: the line,
// when the decoder gives one, and the key's name, which may contain a space. The report
// contains the Go type, which the message a person reads leaves out.
var UnknownKey = regexp.MustCompile(`(line \d+: )?field (.+) not found in type \S+`)

// definedTwice is the decoder's report of a mapping key written twice, which quotes the
// key as a Go string, and the line it was first written at.
var definedTwice = regexp.MustCompile(`mapping key "(?:[^"\\]|\\.)*" already defined at line (\d+)`)

// entryLine is the line at the start of one of the decoder's refusals of a value, and
// unknownKeyAt the same of its refusal of an unknown key; syntaxLine is the line of the
// decoder's refusal of the file's syntax.
var (
	entryLine    = regexp.MustCompile(`^line (\d+): `)
	unknownKeyAt = regexp.MustCompile(`^line (\d+): field `)
	syntaxLine   = regexp.MustCompile(`^yaml: line (\d+):`)
)

// DecodeError prefixes a decode error with path. An unknown key is reported by
// [KeyNotRead] as one the file at path does not read, with the decoder's line number
// when it gives one. A key written twice whose name contains an access key secret or a
// run key is reported in the decoder's words without the name. Any other error is
// returned as the decoder wrote it, unless its text still contains an access key secret
// or a run key, as a key whose name has a newline in it does: then [withoutSecrets]
// says it.
func DecodeError(path string, err error) error {
	text := err.Error()
	rest := " " + filepath.Base(path) + " reads"
	if m := UnknownKey.FindStringSubmatch(text); m != nil {
		return fmt.Errorf("%s: %s%s", path, m[1], KeyNotRead(m[2], rest))
	}
	hidden := hideDefinedTwice(text)
	if accesskey.ContainsSecret(hidden) {
		return withoutSecrets(path, rest, err)
	}
	if hidden != text {
		return fmt.Errorf("%s: %s", path, hidden)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// hideDefinedTwice is text with each of the decoder's reports of a key written twice
// whose name contains an access key secret or a run key said without the name.
func hideDefinedTwice(text string) string {
	return definedTwice.ReplaceAllStringFunc(text, func(m string) string {
		if !accesskey.ContainsSecret(m) {
			return m
		}
		return definedTwice.ReplaceAllString(m, "mapping key already defined at line $1")
	})
}

// cannotRead is what a refusal says in place of the decoder's message when that message
// contains an access key secret or a run key.
const cannotRead = "qory cannot read the value here. The YAML decoder's message contains an access key secret or a run key, so qory does not print it."

// withoutSecrets is err, a decode error of the file at path whose text contains an
// access key secret or a run key, said without them. Each of the decoder's refusals of
// a value that contains one is its line and [cannotRead], or, for an unknown key, its
// line and [KeyNotRead]'s words with rest; each that contains none keeps the decoder's
// words. When any keeps them, the error is the decoder's list of refusals with those
// entries; when none does, it is the first unknown key, or else the one entry, or else
// the decoder's list of the entries. A refusal of the file's syntax is its line and
// [cannotRead], and any other error the path and cannotRead. When err wraps the
// decoder's message in words of qory's own, as a list of runtimes does, the refusal keeps
// those words in front, unless they contain an access key secret or a run key too.
func withoutSecrets(path, rest string, err error) error {
	if inner := errors.Unwrap(err); inner != nil {
		if words, ok := strings.CutSuffix(err.Error(), inner.Error()); ok && words != "" && !accesskey.ContainsSecret(words) {
			path += ": " + strings.TrimSuffix(words, ": ")
		}
	}
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		if m := syntaxLine.FindStringSubmatch(err.Error()); m != nil {
			return fmt.Errorf("%s: line %s: %s", path, m[1], cannotRead)
		}
		return fmt.Errorf("%s: %s", path, cannotRead)
	}
	var entries, lines []string
	unknown, kept := "", false
	for _, e := range te.Errors {
		e = hideDefinedTwice(e)
		if !accesskey.ContainsSecret(e) {
			entries, kept = append(entries, e), true
			continue
		}
		if m := unknownKeyAt.FindStringSubmatch(e); m != nil {
			// The entry contains a secret, so KeyNotRead leaves it out.
			e = "line " + m[1] + ": " + KeyNotRead(e, rest)
			if unknown == "" {
				unknown = e
			}
		} else if m := entryLine.FindStringSubmatch(e); m != nil {
			e = "line " + m[1] + ": " + cannotRead
			lines = append(lines, e)
		} else {
			continue
		}
		entries = append(entries, e)
	}
	lines = slices.Compact(lines)
	switch {
	case kept:
		return fmt.Errorf("%s: yaml: unmarshal errors:\n  %s", path, strings.Join(entries, "\n  "))
	case unknown != "":
		return fmt.Errorf("%s: %s", path, unknown)
	case len(lines) == 1:
		return fmt.Errorf("%s: %s", path, lines[0])
	case len(lines) > 1:
		return fmt.Errorf("%s: yaml: unmarshal errors:\n  %s", path, strings.Join(lines, "\n  "))
	}
	return fmt.Errorf("%s: %s", path, cannotRead)
}

// KeyNotRead is the refusal of the key name where no key of that name is read: "key",
// the name quoted, "is not one" and rest, as in key "x" is not one qory.yaml reads.
// When the name contains an access key secret or a run key, as
// [accesskey.ContainsSecret] reports one, the refusal leaves the name out, so it does
// not contain the secret.
func KeyNotRead(name, rest string) string {
	if accesskey.ContainsSecret(name) {
		return "key is not one" + rest
	}
	return fmt.Sprintf("key %q is not one%s", name, rest)
}

// word is one word of a message for [HideName]: a string quoted as Go's %q quotes one,
// with its quotes, or what stands between spaces and quotes.
var word = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|[^\s"]+`)

// HideName is text with each word that contains name, or that contains an access key
// secret or a run key in any letter case, printed as [accesskey.ShownName] prints it
// unquoted, when name contains an access key secret or a run key; text as it is when
// name contains neither. A word is a program's name or path, a name another program's
// message quotes, or such a name cut short, and the colons, semicolons and commas at its
// end stay after the phrase.
func HideName(text, name string) string {
	if !accesskey.ContainsSecret(name) {
		return text
	}
	return word.ReplaceAllStringFunc(text, func(w string) string {
		if !strings.Contains(w, name) && !accesskey.ContainsSecret(w) {
			return w
		}
		end := strings.TrimRight(w, ":;,")
		return accesskey.ShownName(end, false) + w[len(end):]
	})
}

// HideError is err with its text printed as [HidePath] prints it, when the text contains
// an access key secret or a run key. The result unwraps to err, so a caller still matches
// a [*fs.PathError] or another error under it. An error with no secret, and nil, are
// returned as they are.
func HideError(err error) error {
	if err == nil || !accesskey.ContainsSecret(err.Error()) {
		return err
	}
	return hiddenError{HidePath(err.Error()), err}
}

// hiddenError is an error whose text contains a secret, with the text [HideError]
// prints.
type hiddenError struct {
	text string
	err  error
}

// Error is the text with the secret left out.
func (e hiddenError) Error() string { return e.text }

// Unwrap returns the error whose text contains the secret.
func (e hiddenError) Unwrap() error { return e.err }

// HidePath is text with each segment of a path, between slashes, that contains an access
// key secret or a run key printed as [accesskey.ShownName] prints it unquoted, as in
// /root/modules/(a name that contains an access key secret)/qory-module.yaml. A word
// without a slash is one segment. The quotes around a segment, and the colons,
// semicolons and commas after it, stay around the phrase.
func HidePath(text string) string {
	return word.ReplaceAllStringFunc(text, func(w string) string {
		if !accesskey.ContainsSecret(w) {
			return w
		}
		segments := strings.Split(w, "/")
		for i, s := range segments {
			if accesskey.ContainsSecret(s) {
				name := strings.TrimLeft(strings.TrimRight(s, `:;,"'`), `"'`)
				start := strings.Index(s, name)
				segments[i] = s[:start] + accesskey.ShownName(name, false) + s[start+len(name):]
			}
		}
		return strings.Join(segments, "/")
	})
}

// RefuseAliases fails on the first YAML alias in the file at path, a *name or a <<: *name
// merge; an anchor alone copies nothing and stands. The file's readers other than qory
// refuse an alias, since a few hundred bytes of them chained can expand to gigabytes, and
// read such a file as unreadable without a word to its author. qory refuses the alias
// too, so the file fails where it is written. When the anchor's name contains an access
// key secret or a run key, the refusal says "the value" in place of the alias, and its
// line places the alias. A file that does not parse is left to the decoder, which
// reports why.
func RefuseAliases(path string, data []byte) error {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	if a := firstAlias(&doc); a != nil {
		alias := "*" + a.Value
		if accesskey.ContainsSecret(a.Value) {
			// The line places the alias, and the anchor's name is left out.
			alias = "the value"
		}
		return fmt.Errorf("%s: line %d: %s is a YAML alias, which %s may not contain; write the value out in full where it is used", path, a.Line, alias, filepath.Base(path))
	}
	return nil
}

// firstAlias returns the first alias node under n in document order, nil for none. The
// node tree holds an alias as one node, so the walk is as long as the file however far
// the aliases would expand.
func firstAlias(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return n
	}
	for _, c := range n.Content {
		if a := firstAlias(c); a != nil {
			return a
		}
	}
	return nil
}

// segment reports whether name is one plain path segment: no separator of either kind, no
// leading dot, and no "@", the rule a module name follows.
func segment(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\@`) && !strings.HasPrefix(name, ".")
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// listText lists the exports of one kind for a message, "exports no stacks" for none.
func listText(kind string, names []string) string {
	if len(names) == 0 {
		return "it exports no " + kind
	}
	return kind + ": " + strings.Join(accesskey.ShownNames(names), ", ")
}

// isFile reports whether a regular file, or a link to one, is at path.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
