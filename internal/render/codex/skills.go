package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
)

// AllSkills in [AllowedSkills] allows every skill of the harness, as Skill(harness:*)
// does in Claude Code.
const AllSkills = "*"

// AllowedSkills are the skills a walled Codex run may load: the <name> of each
// Skill(harness:<name>) rule in permissions.allow of the harness's
// settings/claude/settings.json, and [AllSkills] for Skill(harness:*), sorted, with no
// name twice. The harness has no separate list for Codex, so the harness's Claude Code
// list is the Codex list too. Any other rule with a pattern, such as
// Skill(harness:review-*), allows no skill here. A harness with no such rule allows none.
func AllowedSkills(res *compose.Result) []string {
	perms, _ := res.Settings["claude"]["settings.json"]["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	var out []string
	for _, rule := range allow {
		s, _ := rule.(string)
		name, ok := strings.CutPrefix(s, "Skill("+render.PluginName+":")
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, ")")
		if !ok || name == "" || name != AllSkills && strings.ContainsAny(name, "*()") {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// placeSkills writes every composed skill into dir/skills, the skills folder of the Codex
// home dir, as folders and regular files only. Codex 0.167.2 passes over a SKILL.md that
// is a link, see [treeCopy], and [render.PlaceEntries] links a file of a skill that
// contains no reference. So placeSkills places the skills with [render.PlaceEntries] in
// a scratch folder in dir, copies each skill with a [treeCopy], and removes the scratch
// folder. The copy follows a link only into the composed home, the parent of dir, and the
// folders of [compose.Result.SourceDirs]; any other link fails the copy with
// [textOutsideLink]. Each skill is copied within the limits of a copy of one entry of
// its module, see [render.LimitFor].
func placeSkills(res *compose.Result, dir string) (err error) {
	scratch, err := os.MkdirTemp(dir, ".skills-")
	if err != nil {
		return failed("skills", err)
	}
	// The scratch folder contains links and files that qory wrote; RemoveAll follows no link.
	defer func() {
		if rerr := os.RemoveAll(scratch); err == nil && rerr != nil {
			err = failed("skills", rerr)
		}
	}()
	if err := render.PlaceEntries(codex{}, res, scratch, render.Bare, "skills"); err != nil {
		return err
	}
	dst := filepath.Join(dir, "skills")
	if err := os.Mkdir(dst, 0o755); err != nil {
		return failed("skills", err)
	}
	roots := append([]string{filepath.Dir(dir)}, res.SourceDirs()...)
	for _, e := range res.Entries {
		if e.Kind != "skills" {
			continue
		}
		src := filepath.Join(scratch, "skills", e.Name)
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		limit := render.LimitFor(e.Module)
		if err := newTreeCopy(roots, &limit).entry(src, filepath.Join(dst, e.Name), path.Join("skills", e.Name)); err != nil {
			return err
		}
	}
	return nil
}

// skillRoots are the folders of a checkout that Codex 0.167.2 reads skills from, trusted
// or not: a project's .codex/skills and .agents/skills
// (codex-rs/ext/skills/src/host_roots.rs).
var skillRoots = []string{".codex/skills", ".agents/skills"}

// pluginManifests are the files that make a folder a plugin, whose name Codex puts
// before the name of each skill below it, as <plugin>:<skill>
// (codex-rs/exec-server-protocol/src/protocol.rs, DISCOVERABLE_PLUGIN_MANIFEST_PATHS).
var pluginManifests = []string{".codex-plugin/plugin.json", ".claude-plugin/plugin.json", ".cursor-plugin/plugin.json"}

// maxScanDepth is how deep Codex 0.167.2 looks for skills below a root, which is at
// depth 0: it reads the folders down to depth 6 and enters none below
// (codex-rs/ext/skills/src/loader/mod.rs, MAX_SCAN_DEPTH).
const maxScanDepth = 6

// maxSkillSize is the largest file Codex 0.167.2 reads, a SKILL.md included
// (codex-rs/exec-server/src/local_file_system_read.rs, MAX_READ_FILE_BYTES); Codex
// loads no larger SKILL.md.
const maxSkillSize = 512 << 20

// maxFrontmatter is how much of a SKILL.md qory reads for its frontmatter's name.
const maxFrontmatter = 1 << 20

// skillFile is one SKILL.md below a root.
type skillFile struct {
	// path is the file as reached from the root.
	path string
	// real is path with every link resolved, as Codex names the skill.
	real string
}

// findSkills are the SKILL.md files below root, as Codex looks for them: regular files of
// at most [maxSkillSize] bytes, folders down to [maxScanDepth], links to folders followed
// and links to files passed over, and a folder reached twice read once. It
// also reads hidden folders, which Codex passes over, so the list is a superset. A root
// that does not exist or is no folder has none, and an entry that cannot be read is
// passed over, as Codex passes over both. When bound is not "", only folders whose real
// path lies inside bound are read, root included: a checkout's link that leads out of the
// checkout is not followed.
func findSkills(root, bound string) []skillFile {
	realBound := ""
	if bound != "" {
		r, err := filepath.EvalSymlinks(bound)
		if err != nil {
			return nil
		}
		realBound = r
	}
	var out []skillFile
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || seen[real] || realBound != "" && !within(real, realBound) {
			return
		}
		seen[real] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			info, err := os.Lstat(p)
			if err != nil {
				continue
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				// Codex follows a link to a folder and passes over a link to a file.
				if info, err = os.Stat(p); err != nil || !info.IsDir() {
					continue
				}
			}
			switch {
			case info.IsDir():
				if depth < maxScanDepth {
					walk(p, depth+1)
				}
			case e.Name() == "SKILL.md" && info.Mode().IsRegular() && info.Size() <= maxSkillSize:
				r, err := filepath.EvalSymlinks(p)
				if err != nil {
					continue
				}
				out = append(out, skillFile{path: p, real: r})
			}
		}
	}
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		walk(root, 0)
	}
	return out
}

// within reports whether path is dir or lies below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// skillNames are the names under which Codex may load each skill of [findSkills] below
// root and bound, sorted, with no name twice. Codex names a skill by the name of its
// frontmatter, or by its real folder, with every link resolved, when the frontmatter has
// none, and puts the name of the nearest plugin above it before that name. The names
// here are a superset: for each skill, the frontmatter's name, the name of the folder
// qory reached and the name of the real folder, each alone and after the name of every
// plugin above the skill, between it and root and between its real folder and bound, or
// the file system's root when bound is "".
func skillNames(root, bound string) []string {
	stop := string(filepath.Separator)
	if bound != "" {
		if r, err := filepath.EvalSymlinks(bound); err == nil {
			stop = r
		}
	}
	var names []string
	for _, f := range findSkills(root, bound) {
		names = append(names, namesOf(root, f, stop)...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// namesOf are the names of the skill f below root: see [skillNames]. The plugins above
// f's real folder are looked for up to stop.
func namesOf(root string, f skillFile, stop string) []string {
	// Codex names a skill without a frontmatter name after its real folder
	// (codex-rs/ext/skills/src/loader/host.rs, default_skill_name), which differs from
	// the folder qory reached when a link with another name leads there.
	base := []string{filepath.Base(filepath.Dir(f.path)), filepath.Base(filepath.Dir(f.real))}
	if n := frontName(f.real); n != "" {
		base = append(base, n)
	}
	var plugins []string
	addPlugins := func(dir, stop string) {
		for {
			if p := pluginName(dir); p != "" {
				plugins = append(plugins, p)
			}
			parent := filepath.Dir(dir)
			if dir == stop || parent == dir {
				return
			}
			dir = parent
		}
	}
	addPlugins(filepath.Dir(f.path), root)
	addPlugins(filepath.Dir(f.real), stop)
	out := slices.Clone(base)
	for _, p := range plugins {
		for _, b := range base {
			out = append(out, p+":"+b)
		}
	}
	return out
}

// frontName is the name in the frontmatter of the SKILL.md at file, with its white space
// folded as Codex folds it; "" when there is none, when file is no regular file, and when
// the frontmatter does not end within the first [maxFrontmatter] bytes. A frontmatter
// that YAML cannot read gives the value of its first top-level name: line, since Codex
// repairs some values before it reads them.
func frontName(file string) string {
	data, err := readHead(file, maxFrontmatter, true)
	if err != nil {
		return ""
	}
	fold := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if doc, err := module.ParseDocument(data, file); err == nil {
		return fold(doc.String("name"))
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			return fold(strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return ""
}

// readHead is up to n bytes from the start of the regular file at file. It reads no device
// or pipe, and with noFollow it opens no link, so a link swapped in after the scan reads
// nothing.
func readHead(file string, n int64, noFollow bool) ([]byte, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if noFollow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(file, flags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is no regular file", file)
	}
	return io.ReadAll(io.LimitReader(f, n))
}

// pluginName is the name of the plugin whose manifest is in dir, as Codex reads it: the
// manifest's name, or dir's name when that is empty; "" when dir has no manifest that
// JSON reads within its first [maxFrontmatter] bytes, or no regular file there.
func pluginName(dir string) string {
	for _, m := range pluginManifests {
		data, err := readHead(filepath.Join(dir, m), maxFrontmatter, false)
		if err != nil {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return ""
		}
		if strings.TrimSpace(manifest.Name) == "" {
			return filepath.Base(dir)
		}
		return manifest.Name
	}
	return ""
}
