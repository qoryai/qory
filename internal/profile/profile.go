package profile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
)

// APIVersion is the one format version this qory reads. A profile that carries another one
// is refused, and the message names this one.
const APIVersion = "qory.ai/v1alpha1"

// Kind is the kind every profile document carries.
const Kind = "HarnessProfile"

// FileName is the profile's file name on disk, in the checkout root or in an ancestor
// directory covering several checkouts.
const FileName = "harness-compose.yaml"

// Kinds are the atomic entry kinds an exclude may name. An exclude that names anything else
// is refused.
var Kinds = []string{"skills", "agents", "commands", "output-styles", "hooks", "mcp"}

// Source is where a layer comes from: a directory, or a git repository at a ref.
//
//	source: {path: ../harness/core}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0}
//	source: {git: https://github.com/acme/harness, ref: v2.4.0, path: layers/nextjs}
//
// A path source is pinned by nothing and reads the directory as it stands. A git source is
// pinned by the commit the ref resolves to, and its path, when given, is the layer's
// directory inside the repository.
type Source struct {
	// Path is the layer directory, relative to the profile file unless it is absolute; with
	// Git set, the layer's directory inside the repository, relative to its root.
	Path string `yaml:"path,omitempty"`
	// Git is the repository URL, in any form git clones from.
	Git string `yaml:"git,omitempty"`
	// Ref is the tag, branch or commit to read from a git source, required with Git.
	Ref string `yaml:"ref,omitempty"`
}

// String returns the source as the report and the error messages show it: the path as the
// profile writes it for a path source, and <git>#<ref>, with :<path> appended when there
// is one, for a git source.
func (s Source) String() string {
	if s.Git == "" {
		return s.Path
	}
	out := s.Git + "#" + s.Ref
	if s.Path != "" {
		out += ":" + s.Path
	}
	return out
}

// Layer is one entry of the profile's ordered list of layers. An entry gives a name, a
// source, or both: a name alone reads layers/<name> at the root of the repository the
// profile is in; a source alone takes the layer's name from its manifest; both means the
// manifest must carry that name.
type Layer struct {
	// Name is the layer's name, the one its manifest declares. Alone, it is the address
	// too: layers/<name> under [Profile.Root].
	Name string `yaml:"name,omitempty"`
	// Source is where the layer is read from, when it is not at layers/<name>.
	Source Source `yaml:"source,omitempty"`
	// Exclude lists, per kind from [Kinds], the entry names this layer does not contribute.
	Exclude map[string][]string `yaml:"exclude,omitempty"`
	// Base marks a layer that came from the base profile an extending profile extends. It
	// is set by [Extend], not by the YAML, which also rewrites the layer's source so it
	// resolves from the extending profile.
	Base bool `yaml:"-"`
	// Variant forces one of the layer's variants instead of the one named like the runtime.
	Variant string `yaml:"variant,omitempty"`
	// Link is a name at the checkout root that links to the layer's directory, so a
	// permission rule or a script reaches the layer's files by a checkout-relative path.
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
// a profile for one runtime stays as short as it reads:
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
// called on a loaded profile, and again by the command layer after the --runtime flag has
// replaced what the profile said.
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

// Extending is what a base profile lets an extending profile's layers ship. A base
// without the block is closed: no profile may extend it.
type Extending struct {
	// Kinds are the entry kinds an appended layer may ship, from [Kinds]. Hooks and MCP
	// servers are never allowed, since the runner executes them without the agent.
	Kinds []string `yaml:"kinds,omitempty"`
	// Instructions allows an appended layer's AGENTS.md, appended after the base's.
	Instructions bool `yaml:"instructions,omitempty"`
	// Settings are the dotted key paths an appended layer's settings fragment may set,
	// such as permissions.allow; a fragment setting anything else fails the compose.
	Settings []string `yaml:"settings,omitempty"`
}

// Profile is one harness-compose.yaml, validated.
type Profile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// Name is the profile's name in the report. A profile that leaves it out is named after
	// the checkout by the caller.
	Name string `yaml:"name,omitempty"`
	// Extends names the base profile this one appends to: a directory holding a
	// harness-compose.yaml, as a path or inside a git repository at a ref. The base's
	// layers come first and cannot be changed; its target is this profile's target.
	Extends Source `yaml:"extends,omitempty"`
	// Target is the runtime and model the harness is rendered for. A profile that extends
	// a base leaves it out and takes the base's.
	Target Target `yaml:"target,omitempty"`
	// Layers are the layers to compose, in the order they merge.
	Layers []Layer `yaml:"layers"`
	// Extending, on a base profile, is what an extending profile's layers may ship. A base
	// that leaves it out cannot be extended.
	Extending *Extending `yaml:"extending,omitempty"`
	// Extensions are values qory carries into the report and does not read: one map per
	// namespace, for the scripts of a team that keep their own settings beside the
	// profile.
	Extensions map[string]map[string]any `yaml:"extensions,omitempty"`

	// File is the absolute path the profile was read from, set by [Load], not by the YAML.
	File string `yaml:"-"`
	// Root is the root of the repository the profile is in, set by [Load]: git's toplevel
	// for the profile's directory, else that directory. A layer named without a source is
	// read from layers/<name> under it.
	Root string `yaml:"-"`
}

// Dir returns the directory holding the profile file. A layer's relative path source
// resolves against it.
func (p *Profile) Dir() string { return filepath.Dir(p.File) }

// SourceOf is the source a layer entry reads from: its own, or layers/<name> for an entry
// with a name alone, which resolves against [Profile.Root]; [Profile.DirOf] says which.
// The relative form is what the report records, so it reads the same on every machine.
func (p *Profile) SourceOf(l Layer) Source {
	if l.Source.Path == "" && l.Source.Git == "" {
		return Source{Path: filepath.Join("layers", l.Name)}
	}
	return l.Source
}

// DirOf is the directory a layer entry's relative path resolves against: the repository
// root for an entry with a name alone, else the profile's directory.
func (p *Profile) DirOf(l Layer) string {
	if l.Source.Path == "" && l.Source.Git == "" {
		return p.Root
	}
	return p.Dir()
}

// Load reads and validates one profile file and records its absolute path in
// [Profile.File]. An unknown field is an error, so a misspelled key is reported rather than
// ignored, and so is a second YAML document in the file. The error from a missing or
// unreadable file is returned as it comes from the operating system, and a caller can
// match it with errors.Is and os.ErrNotExist; a decoding or validation error is prefixed
// with path.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Profile{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, decodeError(path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: holds more than one document; a profile is one", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if p.Root, err = checkout.Root(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// unknownKey is the decoder's report of a key the document has no field for. It names the
// Go type, which the message a person reads leaves out.
var unknownKey = regexp.MustCompile(`(line \d+: )?field (\S+) not found in type \S+`)

// decodeError prefixes a decode error with path. An unknown key is reported as one the
// profile does not read, with the decoder's line number when it gives one; any other
// error is returned as the decoder wrote it.
func decodeError(path string, err error) error {
	if m := unknownKey.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], FileName)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// validate checks the whole document before a caller sees it, so a profile that reaches the
// compose is known to name a runtime or a base to take it from, at least one layer, each
// with a name or a source, no name twice, excludes over known kinds only, links that are
// one path segment and named once, extensions that are maps, and an extending block over
// known kinds without hooks and servers. Whether a source holds a layer, and whether its
// manifest carries the name the entry gives, is the compose's check.
func (p *Profile) validate() error {
	if p.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %q is not one this qory reads; versions: %s", p.APIVersion, APIVersion)
	}
	if p.Kind != Kind {
		return fmt.Errorf("kind %q is not %s", p.Kind, Kind)
	}
	if p.Extends.Path != "" || p.Extends.Git != "" || p.Extends.Ref != "" {
		if err := p.Extends.validate(); err != nil {
			return fmt.Errorf("extends: %w", err)
		}
		if len(p.Target.Runtimes) > 0 || p.Target.Model != "" {
			return errors.New("target is the base profile's; a profile that extends one does not set it")
		}
		if p.Extending != nil {
			return errors.New("extending and extends together are not supported; a base profile does not extend another")
		}
	} else if err := p.Target.Runtimes.Validate(); err != nil {
		return err
	}
	if p.Extending != nil {
		for _, k := range p.Extending.Kinds {
			if !isKind(k) {
				return fmt.Errorf("extending.kinds names kind %q; kinds: %s", k, strings.Join(Kinds, ", "))
			}
			if k == "hooks" || k == "mcp" {
				return fmt.Errorf("extending.kinds names %s, which an extending layer may never ship; the runner executes those without the agent", k)
			}
		}
		for _, key := range p.Extending.Settings {
			if key == "" || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
				return fmt.Errorf("extending.settings names %q, which is not a dotted key path", key)
			}
		}
	}
	if len(p.Layers) == 0 {
		return errors.New("layers is empty; a profile names at least one layer")
	}
	seen := map[string]bool{}
	links := map[string]string{}
	for i, l := range p.Layers {
		who := fmt.Sprintf("layers[%d]", i)
		if l.Name != "" {
			who = "layer " + l.Name
			if !segment(l.Name) {
				return fmt.Errorf("layers[%d]: name %q is not one path segment; a layer name holds no slash, backslash, @ or leading dot", i, l.Name)
			}
			if seen[l.Name] {
				return fmt.Errorf("layer %s is named twice", l.Name)
			}
			seen[l.Name] = true
		}
		if l.Source.Path != "" || l.Source.Git != "" || l.Source.Ref != "" {
			if err := l.Source.validate(); err != nil {
				return fmt.Errorf("%s: %w", who, err)
			}
		} else if l.Name == "" {
			return fmt.Errorf("%s: a layer gives a name, a source, or both", who)
		}
		for kind := range l.Exclude {
			if !isKind(kind) {
				return fmt.Errorf("%s: exclude names kind %q; kinds: %s", who, kind, strings.Join(Kinds, ", "))
			}
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

// validate checks that a source is one directory or one git ref: a path source needs its
// path, a git source needs its ref, and a path inside a git source stays inside it.
func (s Source) validate() error {
	if s.Git == "" {
		if s.Ref != "" {
			return errors.New("source.ref needs source.git")
		}
		if s.Path == "" {
			return errors.New("source.path is required")
		}
		return nil
	}
	if s.Ref == "" {
		return errors.New("source.ref is required with source.git")
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
// leading dot, and no "@", which the compose uses to key an entry by its layer. A layer
// name becomes the directory name layers/<name> in the composed tree, so anything else
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

// Extend returns the profile p composes as an extending profile of base: the base's
// layers first, marked [Layer.Base], each with its source rewritten to resolve from p,
// then p's layers; the base's target; both files' extensions. It refuses a base that extends another, a base without an extending block,
// and an extension namespace both files declare. The result's File and Root are p's.
func Extend(base, p *Profile) (*Profile, error) {
	if base.Extends.Path != "" || base.Extends.Git != "" {
		return nil, fmt.Errorf("%s extends %s, and a base profile does not extend another", base.File, base.Extends.String())
	}
	if base.Extending == nil {
		return nil, fmt.Errorf("the profile at %s is closed: it declares no extending block, so nothing may extend it", base.File)
	}
	out := *p
	out.Target = base.Target
	out.Layers = nil
	for _, l := range base.Layers {
		l.Base = true
		if l.Source.Git == "" {
			// A base layer read by path: inside the base's repository, which is the
			// extends source's clone for a git base, so the layer becomes a git source
			// at the same ref and gets the commit as its pin; a path relative to the
			// extending profile for a base on disk, so the report reads on any machine.
			abs := base.SourceOf(l).Path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(base.DirOf(l), abs)
			}
			rel, err := filepath.Rel(base.Root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("%s: layer %s is outside the base profile's repository", base.File, l.Name)
			}
			if p.Extends.Git != "" {
				l.Source = Source{Git: p.Extends.Git, Ref: p.Extends.Ref, Path: filepath.ToSlash(rel)}
			} else if here, err := filepath.Rel(p.Dir(), abs); err == nil {
				l.Source = Source{Path: here}
			} else {
				l.Source = Source{Path: abs}
			}
		}
		out.Layers = append(out.Layers, l)
	}
	out.Layers = append(out.Layers, p.Layers...)
	out.Extensions = map[string]map[string]any{}
	for ns, v := range base.Extensions {
		out.Extensions[ns] = v
	}
	for ns, v := range p.Extensions {
		if _, ok := out.Extensions[ns]; ok {
			return nil, fmt.Errorf("extensions.%s is the base profile's; an extending profile declares another namespace", ns)
		}
		out.Extensions[ns] = v
	}
	if len(out.Extensions) == 0 {
		out.Extensions = nil
	}
	return &out, nil
}
