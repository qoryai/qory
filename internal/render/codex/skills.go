package codex

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
// name twice. Codex has no list of its own, so the harness's Claude Code list is the
// Codex list too. Any other rule with a pattern, such as Skill(harness:review-*), allows
// no skill here. A harness with no such rule allows none.
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

// skillRoots are the folders of a checkout that Codex 0.167.2 reads skills from, trusted
// or not: a project's .codex/skills and .agents/skills
// (codex-rs/ext/skills/src/host_roots.rs).
var skillRoots = []string{".codex/skills", ".agents/skills"}

// pluginManifests are the files that make a folder a plugin, whose name Codex puts
// before the name of each skill below it, as <plugin>:<skill>
// (codex-rs/exec-server-protocol/src/protocol.rs, DISCOVERABLE_PLUGIN_MANIFEST_PATHS).
var pluginManifests = []string{".codex-plugin/plugin.json", ".claude-plugin/plugin.json", ".cursor-plugin/plugin.json"}

// skillNames are the names under which Codex may load each SKILL.md below root, sorted,
// with no name twice; none when root does not exist. Codex names a skill by the name of
// its frontmatter, or by its folder when the frontmatter has none, and puts the name of
// the nearest plugin above it before that name. The names here are a superset: for each
// skill, both the frontmatter's name and the folder's, each alone and after the name of
// every plugin above the skill, in the folder qory reads and where its links lead.
// Links to folders are followed, every folder is read, a hidden one included, and a
// folder reached twice is read once.
func skillNames(root string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	var walk func(dir string) error
	walk = func(dir string) error {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			info, err := os.Stat(p)
			if err != nil {
				// A link that leads nowhere is no skill Codex loads.
				continue
			}
			if info.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if e.Name() == "SKILL.md" {
				names = append(names, namesOf(root, p)...)
			}
		}
		return nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// namesOf are the names of the skill whose SKILL.md is at file, below root: see
// [skillNames].
func namesOf(root, file string) []string {
	base := []string{filepath.Base(filepath.Dir(file))}
	if n := frontName(file); n != "" {
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
	addPlugins(filepath.Dir(file), root)
	if real, err := filepath.EvalSymlinks(filepath.Dir(file)); err == nil {
		addPlugins(real, string(filepath.Separator))
	}
	out := slices.Clone(base)
	for _, p := range plugins {
		for _, b := range base {
			out = append(out, p+":"+b)
		}
	}
	return out
}

// frontName is the name in the frontmatter of the SKILL.md at file, with its white space
// folded as Codex folds it; "" when there is none. A frontmatter that YAML cannot read
// gives the value of its first top-level name: line, since Codex repairs some values
// before it reads them.
func frontName(file string) string {
	data, err := os.ReadFile(file)
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

// pluginName is the name of the plugin whose manifest is in dir, as Codex reads it: the
// manifest's name, or dir's name when that is empty; "" when dir has no manifest that
// JSON reads.
func pluginName(dir string) string {
	for _, m := range pluginManifests {
		data, err := os.ReadFile(filepath.Join(dir, m))
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
