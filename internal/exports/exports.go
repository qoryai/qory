// Package exports reads the exports section of a repository's qory.yaml: the stacks and
// the modules the repository publishes for other repositories, each by name, and the
// directories they live in.
//
//	exports:
//	  dir: ./harness            # optional; where stacks/ and modules/ are
//	  stacks: [nextjs]          # each at <dir>/stacks/<name>/qory-stack.yaml
//	  modules: [core, nextjs]   # each at <dir>/modules/<name>/qory-module.yaml
//
// A consumer names an export instead of a directory, {git: <url>, ref: <ref>, stack:
// nextjs}, so the publisher may move its directories and only the section changes. The
// section is the whole public surface: a stack or a module not listed is the publisher's
// own, whatever directory it is in.
//
// dir is where the exports are read from, relative to the repository root. Absent, the
// stacks are under stacks/ and the modules under modules/ at the root. As one string it
// is the directory holding both, <dir>/stacks and <dir>/modules. As a map it names each
// directory on its own, {stacks: ./stacks, modules: ./lib/modules}, and the names are
// read directly under them. [Read] reads the section of the qory.yaml in a directory,
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
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the configuration's file name, the qory.yaml the section is read from.
const FileName = "qory.yaml"

// AltFileName is the configuration's second name, read exactly as [FileName]; a
// directory holds one of the two.
const AltFileName = "harness.yaml"

// File returns the configuration file dir holds under either name, "" for none, and an
// error for a directory holding both.
func File(dir string) (string, error) {
	var found string
	for _, name := range []string{FileName, AltFileName} {
		path := filepath.Join(dir, name)
		if !isFile(path) {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("%s holds both %s and %s; a directory holds one of the two", dir, FileName, AltFileName)
		}
		found = path
	}
	return found, nil
}

// APIVersion is the one format version this qory reads, the same one every document
// carries.
const APIVersion = "qory.ai/v1alpha1"

// StackFileName and ModuleFileName are what an exported directory holds: a stack its
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
// repository root. In the file it is one string, the directory holding stacks/ and
// modules/, or a map naming each directory on its own.
type Dirs struct {
	// Stacks is the directory the exported stacks are under, one directory per stack.
	Stacks string `yaml:"stacks,omitempty"`
	// Modules is the directory the exported modules are under, one directory per module.
	Modules string `yaml:"modules,omitempty"`
}

// UnmarshalYAML accepts the string form, which appends stacks and modules to the one
// directory, and the map form, which names each directory as it is.
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
		return errors.New("dir is one directory holding stacks/ and modules/, or a map naming stacks and modules directories")
	}
	type plain Dirs
	var m plain
	if err := n.Decode(&m); err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "stacks" && k != "modules" {
			return fmt.Errorf("dir names %q; a dir map names stacks and modules", k)
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

// Validate refuses a section naming nothing and setting no directory, a name that is
// not one path segment, a name listed twice, and a directory that is absolute, empty or
// outside the repository.
func (s *Section) Validate() error {
	if s.Dir == nil && len(s.Stacks) == 0 && len(s.Modules) == 0 {
		return errors.New("exports names no stacks and no modules")
	}
	if s.Dir != nil {
		for _, d := range []struct{ key, dir string }{{"stacks", s.Dir.Stacks}, {"modules", s.Dir.Modules}} {
			if d.dir == "" {
				return fmt.Errorf("exports.dir names no %s directory; the map form names both", d.key)
			}
			clean := filepath.Clean(d.dir)
			if filepath.IsAbs(d.dir) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("exports.dir %s %q is not a directory inside the repository", d.key, d.dir)
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
				return fmt.Errorf("exports.%s names %q, which is not one path segment; a name holds no slash, backslash, @ or leading dot", list.key, name)
			}
			if seen[name] {
				return fmt.Errorf("exports.%s names %s twice", list.key, name)
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
	// Dir is where the exports are, relative to Root, defaulted when the file names none.
	Dir Dirs
}

// Stack returns the directory of the exported stack, relative to [Exports.Root], and an
// error naming the stacks the repository exports when name is not one of them.
func (e *Exports) Stack(name string) (string, error) {
	if !contains(e.Stacks, name) {
		return "", fmt.Errorf("exports no stack named %s; %s", name, listText("stacks", e.Stacks))
	}
	return filepath.Join(e.Dir.Stacks, name), nil
}

// Module returns the directory of the exported module, relative to [Exports.Root], and an
// error naming the modules the repository exports when name is not one of them.
func (e *Exports) Module(name string) (string, error) {
	if !contains(e.Modules, name) {
		return "", fmt.Errorf("exports no module named %s; %s", name, listText("modules", e.Modules))
	}
	return filepath.Join(e.Dir.Modules, name), nil
}

// Verify checks that every export is there: a qory-stack.yaml in each stack's directory,
// a qory-module.yaml in each module's. The error names the file that is missing and the
// export that wants it.
func (e *Exports) Verify() error {
	for _, name := range e.Stacks {
		dir, _ := e.Stack(name)
		if !isFile(filepath.Join(e.Root, dir, StackFileName)) {
			return fmt.Errorf("%s: exports.stacks names %s, and %s holds no %s", e.File, name, dir, StackFileName)
		}
	}
	for _, name := range e.Modules {
		dir, _ := e.Module(name)
		if !isFile(filepath.Join(e.Root, dir, ModuleFileName)) {
			return fmt.Errorf("%s: exports.modules names %s, and %s holds no %s", e.File, name, dir, ModuleFileName)
		}
	}
	return nil
}

// document is what [Read] decodes: the apiVersion and the exports section, with every
// other key of the file collected and left unread, so the section is checked strictly
// and the rest of the file is the configuration reader's to check.
type document struct {
	APIVersion string         `yaml:"apiVersion"`
	Exports    *Section       `yaml:"exports,omitempty"`
	Rest       map[string]any `yaml:",inline"`
}

// Read reads the exports section of the qory.yaml in root, under either of its names,
// and returns it resolved, or nil when root holds no such file or the file has no
// exports section. A file that cannot be decoded, one with another apiVersion, and a
// section that does not validate are errors naming the file; a file naming no
// apiVersion is read as [APIVersion], the newest format, as the configuration reads it.
func Read(root string) (*Exports, error) {
	file, err := File(root)
	if err != nil || file == "" {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc document
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, decodeError(file, err)
	}
	if doc.APIVersion == "" {
		doc.APIVersion = APIVersion
	}
	if doc.APIVersion != APIVersion {
		return nil, fmt.Errorf("%s: apiVersion %q is not one this qory reads; versions: %s", file, doc.APIVersion, APIVersion)
	}
	if doc.Exports == nil {
		return nil, nil
	}
	if err := doc.Exports.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc.Exports.At(root, file), nil
}

// ModulesDir is the directory a module named without a source is read from, relative
// to the repository root: what the root's exports.dir says, else modules. The error is a
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

// unknownKey is the decoder's report of a key the document has no field for. It names the
// Go type, which the message a person reads leaves out.
var unknownKey = regexp.MustCompile(`(line \d+: )?field (\S+) not found in type \S+`)

// decodeError prefixes a decode error with path. An unknown key is reported as one the
// configuration does not read, with the decoder's line number when it gives one.
func decodeError(path string, err error) error {
	if m := unknownKey.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], filepath.Base(path))
	}
	return fmt.Errorf("%s: %w", path, err)
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
	return kind + ": " + strings.Join(names, ", ")
}

// isFile reports whether a regular file, or a link to one, is at path.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
