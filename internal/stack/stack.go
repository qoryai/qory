package stack

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
)

// APIVersion is the one format version this qory reads. A stack that carries another one
// is refused, and the message names this one.
const APIVersion = "qory.ai/v1alpha1"

// FileName is the stack's file name on disk, in a directory of the harness repository
// that delivers it, or in an ancestor directory covering several checkouts. The file name
// is what says which document a file holds; the document carries no kind. A checkout's
// own stack, and the stack it extends, are in the harness section of its qory.yaml, which
// [github.com/qoryai/qory/internal/config] reads and turns into a Stack with [NewCompose].
const FileName = exports.StackFileName

// Kinds are the atomic entry kinds an exclude may name. An exclude that names anything else
// is refused.
var Kinds = []string{"skills", "agents", "commands", "output-styles", "hooks", "mcp", "files"}

// Source is where a module comes from: a directory, a git repository at a ref, or an
// export of a repository, one it names in the exports section of its qory.yaml.
//
//	source: {path: ../harness/core}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0, path: modules/nextjs}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0, module: nextjs}
//	source: {path: ../harness, module: nextjs}
//
// A path source is pinned by nothing and reads the directory as it stands. A git source is
// pinned by the commit the ref resolves to, and its path, when given, is the module's
// directory inside the repository. With a module or a stack named, the source is the
// repository, the clone or the path, and the export's directory is what the repository's
// qory.yaml says it is, so the publisher may move it. The same shape names the base a
// checkout extends, with stack in place of module.
type Source struct {
	// Path is the module directory, relative to the stack file unless it is absolute; with
	// Git set, the module's directory inside the repository, relative to its root. With
	// Module or Stack set and no Git, the repository whose export is read.
	Path string `yaml:"path,omitempty"`
	// Git is the repository URL, in any form git clones from.
	Git string `yaml:"git,omitempty"`
	// Ref is the tag, branch or commit to read from a git source, required with Git.
	Ref string `yaml:"ref,omitempty"`
	// Module is the name of an exported module of the repository, in a module's source.
	Module string `yaml:"module,omitempty"`
	// Stack is the name of an exported stack of the repository, in extends.
	Stack string `yaml:"stack,omitempty"`
}

// String returns the source as the report and the error messages show it: the path as the
// stack writes it for a path source, and <git>#<ref>, with :<path> appended when there
// is one, for a git source; an export follows as " module <name>" or " stack <name>".
func (s Source) String() string {
	out := s.Path
	if s.Git != "" {
		out = s.Git + "#" + s.Ref
		if s.Path != "" {
			out += ":" + s.Path
		}
	}
	switch {
	case s.Stack != "":
		out += " stack " + s.Stack
	case s.Module != "":
		out += " module " + s.Module
	}
	return out
}

// exported reports whether the source names an export of a repository.
func (s Source) exported() bool { return s.Module != "" || s.Stack != "" }

// Module is one entry of the stack's ordered list of modules. An entry gives a name, a
// source, or both: a name alone reads <name> under the modules directory of the
// repository the stack is in, modules/ at its root unless the root's qory.yaml says
// otherwise under exports.dir; a source alone takes the module's name from its manifest;
// both means the manifest must carry that name.
type Module struct {
	// Name is the module's name, the one its manifest declares. Alone, it is the address
	// too: <name> under [Stack.ModulesDir] under [Stack.Root].
	Name string `yaml:"name,omitempty"`
	// Source is where the module is read from, when it is not in the repository's
	// modules directory.
	Source Source `yaml:"source,omitempty"`
	// Exclude names what of the module the compose leaves out: entries by kind, the
	// instruction section, settings fragments, exported variables. Everything else is
	// composed.
	Exclude Selection `yaml:"exclude,omitempty"`
	// Only names the only things of the module the compose takes, with the same keys as
	// Exclude, and brings in what the named entries require from the module; everything
	// else is left out. Exclude beside it names entries brought in that way to leave out.
	Only Selection `yaml:"only,omitempty"`
	// Base marks a module that came from the base stack a checkout's qory.yaml extends. It
	// is set by [Extend], not by the YAML, which also rewrites the module's source so it
	// resolves from the checkout's qory.yaml.
	Base bool `yaml:"-"`
	// Variant forces one of the module's variants instead of the one named like the runtime.
	Variant string `yaml:"variant,omitempty"`
	// Link is a name at the checkout root that links to the module's directory, so a
	// permission rule or a script reaches the module's files by a checkout-relative path.
	Link string `yaml:"link,omitempty"`
}

// Target is the set of runtimes the harness is rendered for.
type Target struct {
	// Runtimes are the programs that run the harness, such as claude or codex. In the
	// file the key is runtime, and it holds either one name or a list of them.
	Runtimes Runtimes `yaml:"runtime"`
	// Model is written into the settings of every targeted runtime that has a place for
	// it. A target naming several runtimes usually leaves it out, because one model name
	// rarely means anything to two of them.
	Model string `yaml:"model,omitempty"`
}

// Runtimes is the list of runtime names a target names. Both spellings decode into it, so
// a stack for one runtime stays as short as it reads:
//
//	target:
//	  runtime: claude
//
//	target:
//	  runtime: [claude, codex]
//
// The order is the order of the file, and it is the order the runtimes are rendered and
// reported in. The first name is the one a message names when it can only name one.
type Runtimes []string

// UnmarshalYAML accepts a single name or a sequence of names.
func (r *Runtimes) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*r = Runtimes{one}
		return nil
	}
	var many []string
	if err := n.Decode(&many); err != nil {
		return fmt.Errorf("target.runtime is one runtime name or a list of them: %w", err)
	}
	*r = many
	return nil
}

// String joins the runtimes for a message or a title: "claude" for one, "claude, codex"
// for several.
func (r Runtimes) String() string { return strings.Join(r, ", ") }

// Validate refuses an empty target, an empty name in it, and the same runtime twice. It is
// called on a loaded stack, and again by the command module after the --runtime flag has
// replaced what the stack said.
func (r Runtimes) Validate() error {
	if len(r) == 0 {
		return errors.New("target.runtime is required")
	}
	seen := map[string]bool{}
	for _, name := range r {
		if name == "" {
			return errors.New("target.runtime names an empty runtime")
		}
		if seen[name] {
			return fmt.Errorf("target.runtime names %s twice", name)
		}
		seen[name] = true
	}
	return nil
}

// First is the runtime a single-runtime caller means, and "" for an empty target.
func (r Runtimes) First() string {
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// Extending is what a stack lets a checkout's own modules ship. A stack
// without the block is closed: no stack may extend it.
type Extending struct {
	// Kinds are the entry kinds an appended module may ship, from [Kinds] without files,
	// which Files governs by path. Hooks and MCP servers are never allowed, since the
	// runner executes them without the agent.
	Kinds []string `yaml:"kinds,omitempty"`
	// Instructions allows an appended module's AGENTS.md, appended after the base's.
	Instructions bool `yaml:"instructions,omitempty"`
	// Settings are the dotted key paths an appended module's settings fragment may set,
	// such as permissions.allow; a fragment setting anything else fails the compose.
	Settings []string `yaml:"settings,omitempty"`
	// Files are the <runtime>/<path> prefixes an appended module's files may sit under,
	// such as claude/rules, matched by path segment; a file elsewhere fails the compose.
	Files []string `yaml:"files,omitempty"`
}

// Stack is one qory-stack.yaml, validated, or the document a checkout's qory.yaml holds
// under harness: the checkout's own stack, with a Target and Modules, or a Stack whose
// Extends names the base and whose Target is empty; [Extend] merges that one onto its base
// and returns the stack that composes.
type Stack struct {
	APIVersion string `yaml:"apiVersion"`
	// Qory is the range of qory versions the stack is written for, from the qory key;
	// empty when the stack names none. The compose refuses a qory outside it.
	Qory Constraint `yaml:"qory,omitempty"`
	// Name is the stack's name in the report. A stack that leaves it out is named after
	// the checkout by the caller.
	Name string `yaml:"name,omitempty"`
	// Description says what the stack is for, carried into the report.
	Description string `yaml:"description,omitempty"`
	// Extends, in a checkout's qory.yaml, names the base stack it appends to: a directory holding
	// a qory-stack.yaml, as a path or inside a git repository at a ref. The base's modules come
	// first and cannot be changed; its target is the checkout's target.
	Extends Source `yaml:"extends,omitempty"`
	// Target is the runtime and model the harness is rendered for. A document that extends
	// a stack leaves it out and takes the base's.
	Target Target `yaml:"target,omitempty"`
	// Modules are the modules to compose, in the order they merge.
	Modules []Module `yaml:"modules"`
	// Extending, on a stack, is what a checkout's own modules may ship. A stack that
	// leaves it out cannot be extended.
	Extending *Extending `yaml:"extending,omitempty"`
	// Extensions are values qory carries into the report and does not read: one map per
	// namespace, for the scripts of a team that keep their own settings beside the
	// stack.
	Extensions map[string]map[string]any `yaml:"extensions,omitempty"`

	// File is the absolute path the stack was read from, set by [Load], not by the YAML.
	File string `yaml:"-"`
	// Root is the root of the repository the stack is in, set by [Load]: git's toplevel
	// for the stack's directory, else that directory. A module named without a source is
	// read from ModulesDir under it.
	Root string `yaml:"-"`
	// ModulesDir is the directory a module named without a source is read from, relative
	// to Root, set by [Load]: what the root's qory.yaml says under exports.dir, else
	// modules. A stack built without [Load] reads modules.
	ModulesDir string `yaml:"-"`
}

// Dir returns the directory holding the stack file. A module's relative path source
// resolves against it.
func (p *Stack) Dir() string { return filepath.Dir(p.File) }

// SourceOf is the source a module entry reads from: its own, or <name> under
// [Stack.ModulesDir] for an entry with a name alone, which resolves against [Stack.Root];
// [Stack.DirOf] says which. The relative form is what the report records, so it reads
// the same on every machine.
func (p *Stack) SourceOf(l Module) Source {
	if l.Source.Path == "" && l.Source.Git == "" {
		dir := p.ModulesDir
		if dir == "" {
			dir = exports.DefaultModulesDir
		}
		return Source{Path: filepath.Join(dir, l.Name)}
	}
	return l.Source
}

// DirOf is the directory a module entry's relative path resolves against: the repository
// root for an entry with a name alone, else the stack's directory.
func (p *Stack) DirOf(l Module) string {
	if l.Source.Path == "" && l.Source.Git == "" {
		return p.Root
	}
	return p.Dir()
}

// Load reads and validates one stack file and records its absolute path in
// [Stack.File]. An unknown field is an error, so a misspelled key is reported rather than
// ignored, and so is a second YAML document in the file. The error from a missing or
// unreadable file is returned as it comes from the operating system, and a caller can
// match it with errors.Is and os.ErrNotExist; a decoding or validation error is prefixed
// with path.
func Load(path string) (*Stack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Stack{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, decodeError(path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: holds more than one document; a stack is one", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if err := p.locate(); err != nil {
		return nil, err
	}
	if err := p.validate(false); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// locate sets [Stack.Root] and [Stack.ModulesDir] for the file the stack was read from:
// the repository root, and the modules directory its qory.yaml names, else modules.
func (p *Stack) locate() error {
	var err error
	if p.Root, err = checkout.Root(filepath.Dir(p.File)); err != nil {
		return err
	}
	p.ModulesDir, err = exports.ModulesDir(p.Root)
	return err
}

// NewCompose validates p as the document a checkout's qory.yaml holds under harness, read
// at path: the checkout's own stack when it sets a target, or, when it names a stack under
// extends, the document that appends to that base.
func NewCompose(path string, p *Stack) (*Stack, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if err := p.locate(); err != nil {
		return nil, err
	}
	if err := p.validate(p.extends()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// extends reports whether the document names a stack to extend.
func (p *Stack) extends() bool {
	return p.Extends.Path != "" || p.Extends.Git != "" || p.Extends.Ref != "" || p.Extends.exported()
}

// unknownKey is the decoder's report of a key the document has no field for. It names the
// Go type, which the message a person reads leaves out.
var unknownKey = regexp.MustCompile(`(line \d+: )?field (\S+) not found in type \S+`)

// decodeError prefixes a decode error with path. An unknown key is reported as one the
// stack does not read, with the decoder's line number when it gives one; any other
// error is returned as the decoder wrote it.
func decodeError(path string, err error) error {
	if m := unknownKey.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], filepath.Base(path))
	}
	return fmt.Errorf("%s: %w", path, err)
}

// validate checks the whole document before a caller sees it, so a document that reaches
// the compose is known to carry what it is asked for, a runtime for a stack or a base for
// a document that extends one, at least one module, each with a name or a source, no name
// twice, excludes and onlys over known kinds and parts, an exclude beside an only naming
// entries the only does not, links
// that are one path segment and named once,
// extensions that are maps, and an extending block over known kinds without hooks and
// servers. Whether a source holds a module, and whether its manifest carries the name the
// entry gives, is the compose's check.
func (p *Stack) validate(compose bool) error {
	if p.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %q is not one this qory reads; versions: %s", p.APIVersion, APIVersion)
	}
	if compose {
		if err := p.Extends.validate(); err != nil {
			return fmt.Errorf("extends: %w", err)
		}
		if p.Extends.Module != "" {
			return fmt.Errorf("extends names module %s; a checkout extends a stack, and a module goes under modules", p.Extends.Module)
		}
		if len(p.Target.Runtimes) > 0 || p.Target.Model != "" {
			return errors.New("target is the base stack's; a harness section that extends a stack does not set it")
		}
		if p.Extending != nil {
			return errors.New("extending is the base stack's; a harness section that extends a stack does not set it")
		}
	} else {
		if p.extends() {
			return errors.New("extends is not a stack's; the harness section of a checkout's qory.yaml extends a stack")
		}
		if err := p.Target.Runtimes.Validate(); err != nil {
			return err
		}
	}
	if p.Extending != nil {
		for _, k := range p.Extending.Kinds {
			if !isKind(k) {
				return fmt.Errorf("extending.kinds names kind %q; kinds: %s", k, strings.Join(Kinds, ", "))
			}
			if k == "hooks" || k == "mcp" {
				return fmt.Errorf("extending.kinds names %s, which an extending module may never ship; the runner executes those without the agent", k)
			}
			if k == "files" {
				return errors.New("extending.kinds names files; the paths an extending module's files may sit under go in extending.files")
			}
		}
		for _, key := range p.Extending.Settings {
			if key == "" || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
				return fmt.Errorf("extending.settings names %q, which is not a dotted key path", key)
			}
		}
		for _, prefix := range p.Extending.Files {
			if !filePrefix(prefix) {
				return fmt.Errorf("extending.files names %q, which is not a <runtime>/<path> prefix; a prefix is relative, holds no dot segment and starts with the runtime", prefix)
			}
		}
	}
	if len(p.Modules) == 0 {
		return errors.New("modules is empty; a stack names at least one module")
	}
	seen := map[string]bool{}
	links := map[string]string{}
	for i, l := range p.Modules {
		who := fmt.Sprintf("modules[%d]", i)
		if l.Name != "" {
			who = "module " + l.Name
			if !segment(l.Name) {
				return fmt.Errorf("modules[%d]: name %q is not one path segment; a module name holds no slash, backslash, @ or leading dot", i, l.Name)
			}
			if seen[l.Name] {
				return fmt.Errorf("module %s is named twice", l.Name)
			}
			seen[l.Name] = true
		}
		if l.Source.Path != "" || l.Source.Git != "" || l.Source.Ref != "" || l.Source.exported() {
			if err := l.Source.validate(); err != nil {
				return fmt.Errorf("%s: %w", who, err)
			}
			if l.Source.Stack != "" {
				return fmt.Errorf("%s: source names stack %s; a module's source names a module, and a stack goes under extends", who, l.Source.Stack)
			}
		} else if l.Name == "" {
			return fmt.Errorf("%s: a module gives a name, a source, or both", who)
		}
		// The blocks are parsed on the stack's own entry, not on the loop's copy.
		if err := p.Modules[i].Exclude.parse(); err != nil {
			return fmt.Errorf("%s: exclude %w", who, err)
		}
		if err := p.Modules[i].Only.parse(); err != nil {
			return fmt.Errorf("%s: only %w", who, err)
		}
		if err := p.Modules[i].besideOnly(); err != nil {
			return fmt.Errorf("%s: %w", who, err)
		}
		if l.Link != "" {
			if !segment(l.Link) || l.Link == ".qory" {
				return fmt.Errorf("%s: link %q is not one path segment; a link holds no slash, backslash, @ or leading dot", who, l.Link)
			}
			if links[l.Link] != "" {
				return fmt.Errorf("%s and %s both link %s", links[l.Link], who, l.Link)
			}
			links[l.Link] = who
		}
	}
	for ns, values := range p.Extensions {
		if values == nil {
			return fmt.Errorf("extensions.%s is empty; an extension is a map of values", ns)
		}
	}
	return nil
}

// besideOnly checks an exclude block that stands beside an only block: it names entries
// only, since only has already left every part it does not name out, and none that only
// names, since taking and leaving out one entry contradict. What it names has to be an
// entry only brings in, which the compose checks when it knows what the module ships.
func (l Module) besideOnly() error {
	if l.Only.Empty() || l.Exclude.Empty() {
		return nil
	}
	if l.Exclude.Instructions || l.Exclude.Settings.Set() || l.Exclude.Env.Set() {
		return errors.New("exclude beside only names a part; only leaves every part it does not name out, so exclude names entries only brings in")
	}
	for _, kind := range l.Exclude.KindNames() {
		for _, name := range l.Exclude.Kinds[kind] {
			if slices.Contains(l.Only.Kinds[kind], name) {
				return fmt.Errorf("%s/%s is named in only and in exclude; name it in one", kind, name)
			}
		}
	}
	return nil
}

// validate checks that a source is one directory, one git ref or one export: a path
// source needs its path, a git source needs its ref, a path inside a git source stays
// inside it, an export is one name that is one path segment, and a git source names an
// export or a path, not both.
func (s Source) validate() error {
	if s.Module != "" && s.Stack != "" {
		return errors.New("source names both a module and a stack; it names one export")
	}
	for _, e := range []struct{ key, name string }{{"module", s.Module}, {"stack", s.Stack}} {
		if e.name != "" && !segment(e.name) {
			return fmt.Errorf("source.%s %q is not one path segment; an export's name holds no slash, backslash, @ or leading dot", e.key, e.name)
		}
	}
	if s.Git == "" {
		if s.Ref != "" {
			return errors.New("source.ref needs source.git")
		}
		if s.Path == "" {
			if s.exported() {
				return errors.New("source.path is required; with an export named, it is the repository exporting it")
			}
			return errors.New("source.path is required")
		}
		return nil
	}
	if s.Ref == "" {
		return errors.New("source.ref is required with source.git")
	}
	if s.Path != "" && s.exported() {
		return errors.New("source.path and an export both name the directory; an export's directory is what the repository's qory.yaml says")
	}
	if s.Path != "" {
		clean := filepath.Clean(s.Path)
		if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("source.path %q is not a directory inside the repository", s.Path)
		}
	}
	return nil
}

// segment reports whether name is one plain path segment: no separator of either kind, no
// leading dot, and no "@", which the compose uses to key an entry by its module. A module
// name becomes the directory name modules/<name> in the composed tree, so anything else
// would write outside it.
func segment(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\@`) && !strings.HasPrefix(name, ".")
}

func isKind(k string) bool {
	for _, kind := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// filePrefix reports whether s is a prefix an extending.files entry may hold: forward
// slashes, a trailing one allowed, no leading one, and at least the runtime segment, with
// no segment empty, "." or "..". [FileAllowed] does the matching.
func filePrefix(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(s, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// FileAllowed reports whether a files entry name, <runtime>/<path>, is one of the prefixes
// or under one, by path segment: claude/rules and claude/rules/ both cover
// claude/rules/a/b.md and neither covers claude/rules-private/x.md, and a prefix naming a
// file covers that file only.
func FileAllowed(name string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// Extend returns the stack the checkout's qory.yaml p composes on base: the base's modules first,
// marked [Module.Base], each with its source rewritten to resolve from p, then p's
// modules; the base's target; both files' extensions. The result's Qory is p's; the
// base's range is the caller's to carry and check, as [Base.Qory] does. It refuses a base that extends
// another, a base without an extending block, and an extension namespace both files
// declare. The result's File and Root are p's.
func Extend(base, p *Stack) (*Stack, error) {
	if base.Extends.Path != "" || base.Extends.Git != "" {
		return nil, fmt.Errorf("%s extends %s, and a stack does not extend another", base.File, base.Extends.String())
	}
	if base.Extending == nil {
		return nil, fmt.Errorf("the stack at %s is closed: it declares no extending block, so nothing may extend it", base.File)
	}
	out := *p
	out.Target = base.Target
	out.Modules = nil
	for _, l := range base.Modules {
		l.Base = true
		if l.Source.Git == "" {
			// A base module read by path: inside the base's repository, which is the
			// extends source's clone for a git base, so the module becomes a git source
			// at the same ref and gets the commit as its pin; a path relative to the
			// checkout's qory.yaml for a base on disk, so the report reads on any machine.
			abs := base.SourceOf(l).Path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(base.DirOf(l), abs)
			}
			rel, err := filepath.Rel(base.Root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("%s: module %s is outside the base stack's repository", base.File, l.Name)
			}
			if p.Extends.Git != "" {
				l.Source = Source{Git: p.Extends.Git, Ref: p.Extends.Ref, Path: filepath.ToSlash(rel)}
			} else if here, err := filepath.Rel(p.Dir(), abs); err == nil {
				l.Source = Source{Path: here}
			} else {
				l.Source = Source{Path: abs}
			}
		}
		out.Modules = append(out.Modules, l)
	}
	inBase := map[string]bool{}
	for _, l := range out.Modules {
		inBase[l.Name] = true
	}
	for _, l := range p.Modules {
		if inBase[l.Name] {
			return nil, fmt.Errorf("module %s belongs to the base stack; a checkout's qory.yaml cannot name a base module, exclude from it or replace it", l.Name)
		}
	}
	out.Modules = append(out.Modules, p.Modules...)
	out.Extensions = map[string]map[string]any{}
	for ns, v := range base.Extensions {
		out.Extensions[ns] = v
	}
	for ns, v := range p.Extensions {
		if _, ok := out.Extensions[ns]; ok {
			return nil, fmt.Errorf("extensions.%s is the base stack's; a checkout's qory.yaml declares another namespace", ns)
		}
		out.Extensions[ns] = v
	}
	if len(out.Extensions) == 0 {
		out.Extensions = nil
	}
	return &out, nil
}
