package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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

// Layer is one entry of the profile's ordered list of layers.
type Layer struct {
	// Name is unique within the profile and names the layer in the report and in messages.
	Name string `yaml:"name"`
	// Source is where the layer is read from.
	Source Source `yaml:"source"`
	// Exclude lists, per kind from [Kinds], the entry names this layer does not contribute.
	Exclude map[string][]string `yaml:"exclude,omitempty"`
	// Variant forces one of the layer's variants instead of the one named like the runtime.
	Variant string `yaml:"variant,omitempty"`
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

// Profile is one harness-compose.yaml, validated.
type Profile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// Name is the profile's name in the report. A profile that leaves it out is named after
	// the checkout by the caller.
	Name string `yaml:"name,omitempty"`
	// Target is the runtime and model the harness is rendered for.
	Target Target `yaml:"target"`
	// Layers are the layers to compose, in the order they merge.
	Layers []Layer `yaml:"layers"`

	// File is the absolute path the profile was read from, set by [Load], not by the YAML.
	File string `yaml:"-"`
}

// Dir returns the directory holding the profile file. A layer's relative path source
// resolves against it.
func (p *Profile) Dir() string { return filepath.Dir(p.File) }

// Load reads and validates one profile file and records its absolute path in
// [Profile.File]. An unknown field is an error, so a misspelled key is reported rather than
// ignored. The error from a missing or unreadable file is returned as it comes from the
// operating system, and a caller can match it with errors.Is and os.ErrNotExist; a decoding
// or validation error is prefixed with path.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Profile{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p.File = abs
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// validate checks the whole document before a caller sees it, so a profile that reaches the
// compose is known to name a runtime, at least one layer, uniquely named layers with a
// source each, and excludes over known kinds only.
func (p *Profile) validate() error {
	if p.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion %q is not one this qory reads; versions: %s", p.APIVersion, APIVersion)
	}
	if p.Kind != Kind {
		return fmt.Errorf("kind %q is not %s", p.Kind, Kind)
	}
	if err := p.Target.Runtimes.Validate(); err != nil {
		return err
	}
	if len(p.Layers) == 0 {
		return errors.New("layers is empty; a profile names at least one layer")
	}
	seen := map[string]bool{}
	for i, l := range p.Layers {
		if l.Name == "" {
			return fmt.Errorf("layers[%d]: name is required", i)
		}
		if seen[l.Name] {
			return fmt.Errorf("layer %s is named twice", l.Name)
		}
		seen[l.Name] = true
		if err := l.Source.validate(); err != nil {
			return fmt.Errorf("layer %s: %w", l.Name, err)
		}
		for kind := range l.Exclude {
			if !isKind(kind) {
				return fmt.Errorf("layer %s: exclude names kind %q; kinds: %s", l.Name, kind, strings.Join(Kinds, ", "))
			}
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
		if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("source.path %q is not a directory inside the repository", s.Path)
		}
	}
	return nil
}

func isKind(k string) bool {
	for _, kind := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}
