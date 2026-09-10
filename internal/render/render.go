package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/layer"
)

// Link is one path in a checkout that points into the composed home.
//
// A link whose home path is a file is one symlink. A link whose home path is a directory,
// such as .claude, is written as a real directory in the checkout holding one symlink per
// entry of it, a file or a directory such as .claude/skills, so the checkout keeps its own
// files beside them: Claude Code writes .claude/settings.local.json when a person answers
// a permission prompt, and a repository may own .github/agents. [LinkInto] has the rules.
type Link struct {
	// Checkout is the path relative to the checkout root, such as ".claude".
	Checkout string
	// Home is the path inside the home the link points at, "" being the home itself.
	Home string
	// Soft marks a link that yields to whatever qory did not write at its path.
	Soft bool
}

// Linked is what [LinkInto] did besides writing links, for the caller to report.
type Linked struct {
	// Skipped are the checkout paths of soft links passed over because something qory did
	// not write stands there, such as "AGENTS.md" or ".github/agents/triage.agent.md".
	Skipped []string
	// Replaced are the checkout paths whose tracked, unmodified file or directory was
	// removed for a link under force. git checkout -- restores each of them.
	Replaced []string
}

// ForeignPathError is the refusal to remove or replace a path qory did not write. The
// command layer matches it with errors.As and exits with its own status for it.
type ForeignPathError struct {
	// Path is the absolute checkout path that stands in the way.
	Path string
	// Target is the link target when Path is a symlink, "" when it is a file or directory.
	Target string
	// Reason is set when force was asked for and refused, and says why.
	Reason string
}

// Error names the path and what qory found there.
func (e *ForeignPathError) Error() string {
	switch {
	case e.Reason != "":
		return fmt.Sprintf("%s %s; qory does not replace it", e.Path, e.Reason)
	case e.Target != "":
		return fmt.Sprintf("%s links to %s, which qory did not write; qory does not replace it", e.Path, e.Target)
	default:
		return fmt.Sprintf("%s is not a link qory wrote; qory does not replace it", e.Path)
	}
}

// Runtime renders for one program that runs the harness. A package under internal/render
// implements it for one program and registers the implementation from its init.
type Runtime interface {
	// Name is the target.runtime value.
	Name() string
	// Render writes the runtime's own files into dir, the runtime's directory inside a
	// staging copy of the home. Paths written into files name home, where the tree ends up.
	// The shared parts, AGENTS.md, skills/ and hooks/, are already at the home's root.
	Render(res *compose.Result, dir, home string) error
	// Links are the paths a checkout needs so the program reads the home. A nil res asks for
	// the full set, which is what [Unlink] removes.
	Links(res *compose.Result) []Link
	// Skips are the entry kinds the runtime has no place for.
	Skips() []string
}

// runtimes holds every registered runtime by its target.runtime value.
var runtimes = map[string]Runtime{}

// Register adds a runtime, and the runtime package's init is the only caller. A second
// runtime of the same name replaces the first.
func Register(p Runtime) { runtimes[p.Name()] = p }

// Lookup returns the runtime registered for a target.runtime value. The error for a name
// nothing registered lists the names that are.
func Lookup(name string) (Runtime, error) {
	p, ok := runtimes[name]
	if !ok {
		return nil, fmt.Errorf("runtime %q is not one this qory renders; runtimes: %s", name, strings.Join(Names(), ", "))
	}
	return p, nil
}

// Names lists the registered runtimes in alphabetical order.
func Names() []string {
	var names []string
	for n := range runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Composed reports the runtimes the home already holds, by the directories in it that are
// named after a registered runtime. It returns nothing when the home does not exist, which
// is the first compose of a checkout. A directory of any other name is ignored, so a home
// written by a later qory that knows more runtimes loses only what this build cannot render.
func Composed(home string) []Runtime {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var found []Runtime
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, ok := runtimes[e.Name()]; ok {
			found = append(found, p)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name() < found[j].Name() })
	return found
}

// Build writes the composed home for one or more runtimes. It stages the tree in home with
// ".tmp" appended, discarding whatever that path held, links the shared skills and hooks,
// links every layer's directory as layers/<name> and writes AGENTS.md at the staging root,
// calls each runtime's Render for the subdirectory named after it, then removes the old
// home and renames the staging directory over it. A failure before the rename leaves the
// previous home as it was.
//
// Every runtime the home is to hold must be passed in one call, because the rename replaces
// the whole tree. A caller composing for one runtime passes the runtimes already in the home
// alongside it, which [Composed] reports, so that the links of a checkout composed for
// several runtimes keep resolving and stay current. Build with no runtime is an error.
func Build(res *compose.Result, home string, runtimes ...Runtime) error {
	tmp := home + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := LinkEntries(res, tmp, "skills", "hooks"); err != nil {
		return err
	}
	if err := linkLayers(res, tmp); err != nil {
		return err
	}
	if res.Instructions != "" {
		if err := WriteFile(tmp, "AGENTS.md", []byte(res.Instructions)); err != nil {
			return err
		}
	}
	if len(runtimes) == 0 {
		return errors.New("build needs at least one runtime")
	}
	for _, p := range runtimes {
		dir := filepath.Join(tmp, p.Name())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := p.Render(res, dir, home); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	return os.Rename(tmp, home)
}

// LinkInto makes the checkout at root read the home through the runtime's links, and
// returns what it passed over and what it replaced. Every link is relative, so a checkout
// that moves keeps them valid, and every link and the qory directory go into the
// clone-local exclude file. home is the composed tree under that qory directory.
//
// A link to a file is one symlink. A link to a directory is a real directory in the checkout
// holding one symlink per entry of the home's directory, a file or a directory such as
// .claude/skills, so the checkout's own files there stay: .claude/settings.local.json
// beside qory's .claude/settings.json. A symlink qory wrote at the directory's path, the
// way a release before this one linked .claude whole, is removed and the directory made;
// a directory the home leaves empty gets no directory and no link.
//
// A path holding something qory did not write is a [*ForeignPathError] for a hard link and
// is passed over for a soft one. With force, a tracked and unmodified file or directory of
// the checkout is removed first and its path recorded, for either kind of link; anything
// untracked or modified is still refused, because git checkout -- could not bring it back.
//
// LinkInto also takes back what an earlier compose linked and this one does not: a link the
// runtime no longer asks for, such as AGENTS.override.md once the compose produces no
// instructions, and a file inside a linked directory the home no longer has. It removes
// only links that point into the qory directory. It stops at the first error, so the links
// before it are already written.
func LinkInto(p Runtime, res *compose.Result, root, home string, force bool) (Linked, error) {
	var out Linked
	if err := exclude(root, "/"+checkout.Dir); err != nil {
		return out, err
	}
	current := p.Links(res)
	for _, l := range current {
		src := filepath.Join(home, l.Home)
		info, err := os.Stat(src)
		if err != nil {
			return out, err
		}
		path := filepath.Join(root, l.Checkout)
		if !info.IsDir() {
			if err := link(root, path, src, l, force, &out); err != nil {
				return out, err
			}
			continue
		}
		skip, err := clearForDir(root, path, l, force, &out)
		if err != nil {
			return out, err
		}
		if skip {
			continue
		}
		children, err := os.ReadDir(src)
		if err != nil {
			return out, err
		}
		keep := map[string]bool{}
		for _, c := range children {
			keep[c.Name()] = true
			child := Link{Checkout: l.Checkout + "/" + c.Name(), Soft: l.Soft}
			if err := link(root, filepath.Join(path, c.Name()), filepath.Join(src, c.Name()), child, force, &out); err != nil {
				return out, err
			}
		}
		if err := prune(path, keep); err != nil {
			return out, err
		}
	}
	for _, l := range p.Links(nil) {
		if !asksFor(current, l.Checkout) {
			if err := prune(filepath.Join(root, l.Checkout), nil); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// link writes one symlink at path pointing at src, relative, and excludes it. What stands
// at path decides: a link qory wrote is replaced; a foreign path is replaced under force
// when git can restore it, else passed over for a soft link and an error for a hard one.
func link(root, path, src string, l Link, force bool, out *Linked) error {
	err := removeOwnLink(path)
	var foreign *ForeignPathError
	if errors.As(err, &foreign) {
		switch {
		case force:
			if reason := checkout.Restorable(root, path); reason != "" {
				return &ForeignPathError{Path: path, Reason: reason}
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			out.Replaced = append(out.Replaced, l.Checkout)
		case l.Soft:
			out.Skipped = append(out.Skipped, l.Checkout)
			return nil
		default:
			return err
		}
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	target, err := filepath.Rel(filepath.Dir(path), src)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, path); err != nil {
		return err
	}
	return exclude(root, "/"+l.Checkout)
}

// clearForDir makes room for a real directory at path. A directory already there, the
// checkout's own, is left for the links to go into. A symlink qory wrote there is removed.
// A symlink qory did not write, or a file, is foreign and follows the rules of [link]:
// replaced under force when git can restore it, passed over for a soft link, which
// clearForDir reports as skip, and a [*ForeignPathError] for a hard one.
func clearForDir(root, path string, l Link, force bool, out *Linked) (skip bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}
	err = removeOwnLink(path)
	if info.Mode()&os.ModeSymlink == 0 {
		err = &ForeignPathError{Path: path}
	}
	var foreign *ForeignPathError
	if !errors.As(err, &foreign) {
		return false, err
	}
	switch {
	case force:
		if reason := checkout.Restorable(root, path); reason != "" {
			return false, &ForeignPathError{Path: path, Reason: reason}
		}
		if err := os.RemoveAll(path); err != nil {
			return false, err
		}
		out.Replaced = append(out.Replaced, l.Checkout)
		return false, nil
	case l.Soft:
		out.Skipped = append(out.Skipped, l.Checkout)
		return true, nil
	default:
		return false, err
	}
}

// asksFor reports whether the links hold one at the checkout path.
func asksFor(links []Link, path string) bool {
	for _, l := range links {
		if l.Checkout == path {
			return true
		}
	}
	return false
}

// prune removes what qory wrote at path that the compose no longer asks for: a symlink
// into the qory directory, or, in a real directory, every such symlink whose name keep does
// not hold, and then the directory itself when nothing is left in it. Anything else at path
// or inside it is left alone, and a path that does not exist is nothing to prune.
func prune(path string, keep map[string]bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		own, err := ownLink(path)
		if err != nil || !own {
			return err
		}
		return os.Remove(path)
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, it := range items {
		if keep[it.Name()] {
			continue
		}
		child := filepath.Join(path, it.Name())
		own, err := ownLink(child)
		if err != nil {
			return err
		}
		if own {
			if err := os.Remove(child); err != nil {
				return err
			}
		}
	}
	_ = os.Remove(path)
	return nil
}

// Unlink removes the runtime's links that qory wrote and returns their checkout paths as
// the runtime declares them: ".claude" once for the links inside it. A link that one of
// the others also declares, such as .agents/skills or AGENTS.md, is left for it, which is
// how one runtime leaves a checkout composed for several. A parent directory the links
// left empty, such as .agents, is removed too.
//
// Anything else at a link's path is not qory's to remove: a hard link's path fails, and a
// soft link's path is left alone, which is how a checkout's own AGENTS.md survives. Inside a
// linked directory only the symlinks into the qory directory go, so
// .claude/settings.local.json stays and so does the directory holding it. Unlink leaves
// the home for the caller to remove, and it leaves the exclude lines, because worktrees of
// one repository share one exclude file and a line without its link is harmless.
func Unlink(p Runtime, root string, others ...Runtime) ([]string, error) {
	shared := map[string]bool{}
	for _, o := range others {
		for _, l := range o.Links(nil) {
			shared[l.Checkout] = true
		}
	}
	var removed []string
	for _, l := range p.Links(nil) {
		if shared[l.Checkout] {
			continue
		}
		path := filepath.Join(root, l.Checkout)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if info.IsDir() {
			n, err := removeOwnLinks(path)
			if err != nil {
				return removed, err
			}
			if n > 0 {
				removed = append(removed, l.Checkout)
			}
			_ = os.Remove(path)
		} else {
			if err := removeOwnLink(path); err != nil {
				if l.Soft {
					continue
				}
				return removed, err
			}
			removed = append(removed, l.Checkout)
		}
		if parent := filepath.Dir(path); parent != root {
			_ = os.Remove(parent)
		}
	}
	return removed, nil
}

// removeOwnLinks removes every symlink into the qory directory inside dir and returns how
// many it removed. Files, directories and other links stay.
func removeOwnLinks(dir string) (int, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		path := filepath.Join(dir, it.Name())
		own, err := ownLink(path)
		if err != nil {
			return n, err
		}
		if !own {
			continue
		}
		if err := os.Remove(path); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ownLink reports whether path is a link qory wrote: a relative symlink whose target runs
// through the qory directory. A path that is not there reads as not qory's.
func ownLink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	slash := filepath.ToSlash(target)
	return !filepath.IsAbs(target) && (strings.Contains(slash, "/"+checkout.Dir+"/") || strings.HasPrefix(slash, checkout.Dir+"/")), nil
}

// removeOwnLink removes path when it is a link qory wrote. A path that does not exist is
// nothing to remove and no error. A regular file, a directory, or a link pointing
// elsewhere is a [*ForeignPathError] naming what stands in the way, and it is the check
// that keeps a render from eating a repository's own files.
func removeOwnLink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return &ForeignPathError{Path: path}
	}
	own, err := ownLink(path)
	if err != nil {
		return err
	}
	if !own {
		target, _ := os.Readlink(path)
		return &ForeignPathError{Path: path, Target: target}
	}
	return os.Remove(path)
}

// exclude appends one line to the checkout's clone-local exclude file, once. A checkout
// outside git has no such file, and then there is nothing to exclude and no error.
func exclude(root, line string) error {
	file := checkout.ExcludeFile(root)
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(file, []byte(text+line+"\n"), 0o644)
}

// LinkEntries writes one symlink per composed entry of the kinds in keep, at
// dir/<kind>/<name>, pointing at the entry in its layer. Skills and hooks keep their
// name, the Markdown kinds get ".md" appended. Every kind directory named is created,
// with no entry of that kind as well, so a checkout link to it resolves. Entries of any
// other kind are left out, and it is the caller's job to pass every kind the runtime
// reads from a link.
func LinkEntries(res *compose.Result, dir string, keep ...string) error {
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
		link := filepath.Join(dir, e.Kind, e.Name)
		switch e.Kind {
		case "skills", "hooks":
		default:
			link += ".md"
		}
		if err := os.Symlink(e.Path, link); err != nil {
			return err
		}
	}
	return nil
}

// linkLayers writes one symlink per layer at dir/layers/<name>, pointing at the layer's
// directory, so a settings fragment or a hook reaches a layer's other files as
// $QORY_HARNESS_HOME/layers/<name>/<path>.
func linkLayers(res *compose.Result, dir string) error {
	layers := filepath.Join(dir, "layers")
	if err := os.MkdirAll(layers, 0o755); err != nil {
		return err
	}
	for _, l := range res.Layers {
		if err := os.Symlink(l.Dir, filepath.Join(layers, l.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Skipped lists the composed entries of the kinds a runtime has no place for, as
// "<kind>/<name>", in the order the compose produced them. The compose prints the list so
// an entry that went nowhere is announced rather than silently dropped.
func Skipped(p Runtime, res *compose.Result) []string {
	skip := map[string]bool{}
	for _, k := range p.Skips() {
		skip[k] = true
	}
	var out []string
	for _, e := range res.Entries {
		if skip[e.Kind] {
			out = append(out, e.Kind+"/"+e.Name)
		}
	}
	return out
}

// WriteSettings writes the runtime's merged settings files into dir, one per target file
// a layer contributed a fragment to, with every $QORY_HARNESS_HOME already replaced by
// home. A ".toml" name is encoded as TOML, every other name as indented JSON.
//
// patch, when it is not nil, is called with each file's name and its merged map before
// the encoding, and changing the map there is how a runtime writes the target model.
// Names in ensure are written even when no layer contributed to them, so a model reaches
// a file that would otherwise not exist.
func WriteSettings(res *compose.Result, runtime, dir, home string, ensure []string, patch func(file string, m map[string]any)) error {
	files := res.SettingsFiles(runtime)
	for _, e := range ensure {
		found := false
		for _, f := range files {
			if f == e {
				found = true
			}
		}
		if !found {
			files = append(files, e)
		}
	}
	for _, file := range files {
		m := res.SettingsFor(runtime, file, home)
		if patch != nil {
			patch(file, m)
		}
		if filepath.Ext(file) == ".toml" {
			data, err := EncodeTOML(m)
			if err != nil {
				return err
			}
			if err := WriteFile(dir, file, data); err != nil {
				return err
			}
			continue
		}
		if err := WriteJSON(dir, file, m); err != nil {
			return err
		}
	}
	return nil
}

// EncodeTOML encodes a settings map as TOML. A list whose every element is a map becomes
// an array of tables, which the encoder will not do for a list of the untyped maps a JSON
// or YAML decoder produces.
func EncodeTOML(m map[string]any) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(tables(m)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// tables retypes a value for the TOML encoder: a []any of maps becomes a
// []map[string]any, the shape the encoder emits as an array of tables. A list with one
// non-map element is left as it is, because a mixed list is no array of tables.
func tables(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = tables(e)
		}
		return out
	case []any:
		allMaps := len(x) > 0
		for _, e := range x {
			if _, ok := e.(map[string]any); !ok {
				allMaps = false
			}
		}
		if !allMaps {
			return x
		}
		out := make([]map[string]any, len(x))
		for i, e := range x {
			out[i] = tables(e).(map[string]any)
		}
		return out
	default:
		return v
	}
}

// WriteAgents writes one Markdown file per composed agent at dir/sub/<name><suffix>,
// where suffix carries the runtime's extension, such as ".agent.md". Each file is the
// agent's body under frontmatter holding only the keys in keep that the source agent has,
// so a key one runtime reads does not reach another. When keep names "name" and the
// source has none, the entry's name fills it. The keys come out in alphabetical order,
// not the order of keep.
func WriteAgents(res *compose.Result, dir, sub, suffix string, keep ...string) error {
	for _, e := range res.Entries {
		if e.Kind != "agents" {
			continue
		}
		doc, err := layer.ReadDocument(e.Path)
		if err != nil {
			return err
		}
		front := map[string]any{}
		for _, k := range keep {
			if v, ok := doc.Front[k]; ok {
				front[k] = v
			}
		}
		if _, ok := front["name"]; !ok && contains(keep, "name") {
			front["name"] = e.Name
		}
		var b bytes.Buffer
		b.WriteString("---\n")
		data, err := yaml.Marshal(front)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteString("---\n\n")
		b.WriteString(doc.Body)
		if err := WriteFile(dir, filepath.Join(sub, e.Name+suffix), b.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PutServers merges the MCP servers into m under key, on top of whatever a settings
// fragment put there, so a server declared as an entry wins over one written into a
// fragment by hand. It leaves m alone when servers is nil.
func PutServers(m map[string]any, key string, servers map[string]any) {
	if servers == nil {
		return
	}
	existing, _ := m[key].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}
	for name, v := range servers {
		existing[name] = v
	}
	m[key] = existing
}

// WriteJSON writes v as indented JSON with a trailing newline to dir/name, through
// [WriteFile].
func WriteJSON(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(dir, name, append(data, '\n'))
}

// WriteFile writes data to dir/name with mode 0644, creating the parent directories and
// overwriting a file already there. name may hold separators, such as
// "agents/reviewer.md".
func WriteFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
