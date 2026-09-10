package layer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/profile"
)

// Kind is the kind every layer manifest carries.
const Kind = "HarnessLayer"

// ManifestName is the manifest's file name at the layer root. A layer without it is still a
// layer.
const ManifestName = "harness.yaml"

// InstructionsName is the layer's instruction file, read by every runtime.
const InstructionsName = "AGENTS.md"

// Variant maps an entry kind to the directory that variant reads it from, relative to the
// layer root. A kind the variant does not name is read from the directory named like the
// kind.
type Variant map[string]string

// Manifest is one harness.yaml, validated.
type Manifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// Name is the layer's own name, which the report shows beside the profile's name for it.
	Name string `yaml:"name"`
	// Variants are the layer's variants by name, without the default key.
	Variants map[string]Variant
	// Default is the variant a runtime without its own gets, or "fail" to refuse the compose.
	// It comes from the default key among the variants.
	Default string
}

// rawManifest decodes harness.yaml as it is written, where the variants map holds both the
// variants and the default, a string among the maps. [ReadManifest] splits the two apart.
type rawManifest struct {
	APIVersion string               `yaml:"apiVersion"`
	Kind       string               `yaml:"kind"`
	Name       string               `yaml:"name"`
	Variants   map[string]yaml.Node `yaml:"variants,omitempty"`
}

// Entry is one atomic entry: a skill, an agent, a command, an output style or a hook script.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles and hooks.
	Kind string
	// Name identifies the entry within its kind: the skill directory name, the Markdown file
	// name without its extension, or the hook file name with its extension.
	Name string
	// Path is absolute: the skill directory, the Markdown file or the hook script.
	Path string
}

// Layer is a layer read from disk.
type Layer struct {
	// Name is the profile's name for the layer, not the manifest's own name.
	Name string
	// Dir is the absolute layer root.
	Dir string
	// Manifest is the layer's manifest, nil when it ships none.
	Manifest *Manifest
	// Variant is the variant [Read] scanned, "" for a layer without variants.
	Variant string
	// Entries are the layer's entries, sorted by kind, then by name.
	Entries []Entry
	// Settings are the settings/<runtime>/<file> fragments: per runtime, per target file, the
	// absolute path of the one fragment this layer contributes to it.
	Settings map[string]map[string]string
	// Instructions is the absolute AGENTS.md path, or "" when the layer ships none.
	Instructions string
}

// ReadManifest reads and validates harness.yaml at dir. A layer without one returns nil,
// nil, and a caller passes that nil on to [SelectVariant] and [Read].
//
// An unknown field is an error, so is an apiVersion other than [profile.APIVersion], a kind
// other than [Kind], a missing name, and a default that names something other than a
// declared variant or "fail". Every error names the manifest path.
func ReadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := rawManifest{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if raw.APIVersion != profile.APIVersion {
		return nil, fmt.Errorf("%s: apiVersion %q is not one this qory reads; versions: %s", path, raw.APIVersion, profile.APIVersion)
	}
	if raw.Kind != Kind {
		return nil, fmt.Errorf("%s: kind %q is not %s", path, raw.Kind, Kind)
	}
	if raw.Name == "" {
		return nil, fmt.Errorf("%s: name is required", path)
	}
	m := &Manifest{APIVersion: raw.APIVersion, Kind: raw.Kind, Name: raw.Name}
	for name, node := range raw.Variants {
		if name == "default" {
			if err := node.Decode(&m.Default); err != nil {
				return nil, fmt.Errorf("%s: variants.default: %w", path, err)
			}
			continue
		}
		v := Variant{}
		if err := node.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: variants.%s: %w", path, name, err)
		}
		if m.Variants == nil {
			m.Variants = map[string]Variant{}
		}
		m.Variants[name] = v
	}
	if m.Default != "" && m.Default != "fail" {
		if _, ok := m.Variants[m.Default]; !ok {
			return nil, fmt.Errorf("%s: variants.default names %q, which is not a variant", path, m.Default)
		}
	}
	return m, nil
}

// SelectVariant picks the variant for a runtime: the forced one from the profile, else the
// one named like the runtime, else the manifest's default. A layer with no manifest and a
// manifest that declares no variant serve every runtime from the layer root, and return "".
//
// It returns an error when the profile forces a variant the layer does not have, or forces
// one on a layer that declares none, and when the layer has no variant for the runtime and
// its default is missing or "fail". Every message lists the variant names the layer has.
func SelectVariant(m *Manifest, forced, runtime string) (string, error) {
	if m == nil || len(m.Variants) == 0 {
		if forced != "" {
			return "", fmt.Errorf("variant %q is forced, and the layer declares no variants", forced)
		}
		return "", nil
	}
	if forced != "" {
		if _, ok := m.Variants[forced]; !ok {
			return "", fmt.Errorf("variant %q is forced, and the layer has no such variant; variants: %s", forced, names(m.Variants))
		}
		return forced, nil
	}
	if _, ok := m.Variants[runtime]; ok {
		return runtime, nil
	}
	if m.Default == "fail" {
		return "", fmt.Errorf("the layer has no variant for runtime %s and its default is fail; variants: %s", runtime, names(m.Variants))
	}
	if m.Default == "" {
		return "", fmt.Errorf("the layer has no variant for runtime %s and declares no default; variants: %s", runtime, names(m.Variants))
	}
	return m.Default, nil
}

// names lists the variant names, sorted, for the error messages of [SelectVariant].
func names(v map[string]Variant) string {
	ns := make([]string, 0, len(v))
	for n := range v {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}

// Read scans the layer at dir for one variant and returns what it ships. The name is the
// profile's name for the layer and appears in every error. The variant is the one
// [SelectVariant] returned for m; "" reads every kind from the layer root.
//
// A kind the variant redirects is read from the directory it names, which must be a
// relative directory inside the layer: an empty value, ".", an absolute path and a path
// leaving the layer are all refused. A variant that names a kind outside the five known
// kinds is refused too.
//
// A kind directory that is not there leaves the layer without entries of that kind. A
// skills subdirectory without SKILL.md is an error. In the Markdown kinds only .md files
// count, and under hooks only regular files; a name starting with a dot is skipped
// everywhere. Settings are read from settings/ at the layer root, which no variant
// redirects, and AGENTS.md from the root as well.
func Read(name, dir string, m *Manifest, variant string) (*Layer, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	l := &Layer{Name: name, Dir: abs, Manifest: m, Variant: variant}
	dirs := map[string]string{"skills": "skills", "agents": "agents", "commands": "commands", "output-styles": "output-styles", "hooks": "hooks"}
	if m != nil && variant != "" {
		for kind, sub := range m.Variants[variant] {
			if _, ok := dirs[kind]; !ok {
				return nil, fmt.Errorf("layer %s: variant %s names kind %q", name, variant, kind)
			}
			clean := filepath.Clean(sub)
			if sub == "" || clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
				return nil, fmt.Errorf("layer %s: variant %s reads %s from %q, which is not a directory inside the layer", name, variant, kind, sub)
			}
			dirs[kind] = clean
		}
	}
	skills, err := readSkills(filepath.Join(abs, dirs["skills"]))
	if err != nil {
		return nil, fmt.Errorf("layer %s: %w", name, err)
	}
	l.Entries = append(l.Entries, skills...)
	for _, kind := range []string{"agents", "commands", "output-styles"} {
		es, err := readMarkdown(kind, filepath.Join(abs, dirs[kind]))
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", name, err)
		}
		l.Entries = append(l.Entries, es...)
	}
	hooks, err := readFiles("hooks", filepath.Join(abs, dirs["hooks"]))
	if err != nil {
		return nil, fmt.Errorf("layer %s: %w", name, err)
	}
	l.Entries = append(l.Entries, hooks...)
	sort.Slice(l.Entries, func(i, j int) bool {
		if l.Entries[i].Kind != l.Entries[j].Kind {
			return l.Entries[i].Kind < l.Entries[j].Kind
		}
		return l.Entries[i].Name < l.Entries[j].Name
	})
	l.Settings, err = readSettings(filepath.Join(abs, "settings"))
	if err != nil {
		return nil, fmt.Errorf("layer %s: %w", name, err)
	}
	if _, err := os.Stat(filepath.Join(abs, InstructionsName)); err == nil {
		l.Instructions = filepath.Join(abs, InstructionsName)
	}
	return l, nil
}

// readSkills lists the skill directories of dir. A skill is a directory holding SKILL.md,
// so a directory without one is a mistake worth an error rather than a silent skip.
func readSkills(dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
			return nil, fmt.Errorf("skills/%s has no SKILL.md", it.Name())
		}
		es = append(es, Entry{Kind: "skills", Name: it.Name(), Path: path})
	}
	return es, nil
}

// readMarkdown lists the .md files of dir as entries of kind, named without the extension.
// A directory or any other extension in there is not an entry of this kind.
func readMarkdown(kind, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if it.IsDir() || strings.HasPrefix(it.Name(), ".") || !strings.HasSuffix(it.Name(), ".md") {
			continue
		}
		es = append(es, Entry{Kind: kind, Name: strings.TrimSuffix(it.Name(), ".md"), Path: filepath.Join(dir, it.Name())})
	}
	return es, nil
}

// readSettings lists settings/<runtime>/<file>: the directory name is the runtime, the file
// name is the target file that runtime reads, and a layer contributes at most one fragment
// per target file. A runtime directory holding no file at all is left out of the map.
func readSettings(dir string) (map[string]map[string]string, error) {
	runtimes, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, p := range runtimes {
		if !p.IsDir() || strings.HasPrefix(p.Name(), ".") {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			if out[p.Name()] == nil {
				out[p.Name()] = map[string]string{}
			}
			out[p.Name()][f.Name()] = filepath.Join(dir, p.Name(), f.Name())
		}
	}
	return out, nil
}

// readFiles lists the files of a directory as entries of kind, named by their file name
// with its extension, because a settings fragment names a hook by the file name it has.
func readFiles(kind, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if it.IsDir() || strings.HasPrefix(it.Name(), ".") {
			continue
		}
		es = append(es, Entry{Kind: kind, Name: it.Name(), Path: filepath.Join(dir, it.Name())})
	}
	return es, nil
}
