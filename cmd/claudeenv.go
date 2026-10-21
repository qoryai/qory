package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	modulepkg "github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/render/claude"
	"github.com/qoryai/qory/internal/ui"
)

// modelVariablesError is the refusal of the model and effort variables a compose for
// claude found: one line per variable and place, then why.
type modelVariablesError struct{ lines []string }

func (e *modelVariablesError) Error() string {
	return strings.Join(e.lines, "\n") + "\nClaude Code ranks these variables above the model and effort that qory writes into settings.json and the agent files."
}

// checkModelVariables refuses every place that sets one of [claude.ModelVariables] for a
// Claude Code session on this home, when the home holds claude after the compose: a
// module's settings fragment and its manifest's export, the configuration's env and
// harness.launch.claude.env, the settings.json of the CLAUDE_CONFIG_DIR the claude launch
// sets, and, for a home inside the checkout, the checkout's .claude/settings.json and
// .claude/settings.local.json and the user's Claude Code settings file. A settings file
// that is not JSON is not checked, and its row comes back; one qory cannot read stops
// the compose with the read error.
func checkModelVariables(res *compose.Result, conf config.Config, runtimes []render.Runtime, at places) ([][2]string, error) {
	if !slices.ContainsFunc(runtimes, func(rt render.Runtime) bool { return rt.Name() == claude.Runtime }) {
		return nil, nil
	}
	if err := checkConfigDir(res, conf, runtimes, at); err != nil {
		return nil, err
	}
	var lines []string
	add := func(name, place string) {
		lines = append(lines, "Remove env."+name+" from "+place+".")
	}
	fragment := "settings/" + claude.Runtime + "/settings.json"
	env, _ := res.Settings[claude.Runtime]["settings.json"]["env"].(map[string]any)
	for _, name := range modelNames(env) {
		add(name, fragment+" in module "+accesskey.ShownName(res.SetBy(fragment+"/env."+name), false))
	}
	for _, name := range modelNames(res.Exported) {
		add(name, modulepkg.ManifestName+" in module "+accesskey.ShownName(res.Exported[name], false))
	}
	// The configuration's env line has the variable alone; the other lines have the key
	// under env.
	for _, name := range modelNames(conf.Env) {
		lines = append(lines, "Remove "+name+" from env in "+ui.Short(conf.Origin("env."+name), at.root)+".")
	}
	if l, ok := conf.Launch[claude.Runtime]; ok {
		for _, name := range modelNames(l.Env) {
			add(name, "harness.launch."+claude.Runtime+".env in "+ui.Short(conf.Origin("harness.launch."+claude.Runtime), at.root))
		}
	}
	// Each file is read at path and shown as shown; place is the line's place, shown and
	// what follows it.
	type file struct{ path, shown, place string }
	var files []file
	if at.dir != "" {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			rel := ".claude/" + name
			files = append(files, file{filepath.Join(at.root, ".claude", name), rel, rel + " in the checkout"})
		}
		if user := claudeUserSettings(); user != "" {
			files = append(files, file{user, ui.Short(user, ""), ui.Short(user, "")})
		}
	}
	if path := launchSettings(res, conf, runtimes, at); path != "" && !slices.ContainsFunc(files, func(f file) bool { return f.path == path }) {
		shown := ui.Short(path, at.root)
		files = append(files, file{path, shown, shown})
	}
	// A settings file that is a link into a path the compose writes is refused before any
	// is read: the compose may change what it points at after the check. The checkout's
	// .claude/settings.json is skipped when it is the link qory writes to the home's.
	own := filepath.Join(at.home, claude.Runtime, "settings.json")
	under := written(res, runtimes, at)
	var replace []string
	var read []file
	for _, f := range files {
		steps := fileSteps(f.path, f.place != f.shown)
		if f.place == ".claude/settings.json in the checkout" && len(steps) > 0 && (filepath.Clean(steps[0]) == own || resolvedHome(steps[0]) == resolvedHome(own)) {
			continue
		}
		// The checkout's files and the user's; the CLAUDE_CONFIG_DIR file was checked with
		// its value.
		if f.place != f.shown || f.path == claudeUserSettings() {
			if u, inside := insideAny(steps, under); inside {
				replace = append(replace, "Replace "+modulepkg.Escape(exports.Value(f.shown, false))+", a link into "+modulepkg.Escape(exports.Value(ui.Short(u, at.root), false))+", with a file. qory checks that file for model variables before the compose writes where the link points.")
				continue
			}
		}
		read = append(read, f)
	}
	if len(replace) > 0 {
		return nil, errors.New(strings.Join(replace, "\n"))
	}
	var rows [][2]string
	for _, f := range read {
		env, err := settingsEnv(f.path)
		var bad *notJSON
		if errors.As(err, &bad) {
			rows = append(rows, [2]string{"skipped", exports.Value(f.shown, false) + "  (not JSON: " + bad.err.Error() + "; qory did not check it for model variables)"})
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, name := range modelNames(env) {
			add(name, f.place)
		}
	}
	if len(lines) > 0 {
		return rows, &modelVariablesError{lines: lines}
	}
	return rows, nil
}

// configDir is the variable that sets the directory of Claude Code's user settings.
const configDir = "CLAUDE_CONFIG_DIR"

// checkConfigDir refuses a CLAUDE_CONFIG_DIR whose settings the compose cannot check: one
// a module sets, by its export or a settings fragment's env, and one the configuration
// sets, in env or harness.launch.claude.env, that is relative or inside the home. A
// checkout path a runtime's links take, such as .claude/skills, and the checkout-root name
// a stack links a module as count as inside the home: the compose may replace what stands
// there with a link into it.
func checkConfigDir(res *compose.Result, conf config.Config, runtimes []render.Runtime, at places) error {
	const module = " A module does not choose where Claude Code reads its user settings, because qory checks those settings for model variables."
	var lines []string
	fragment := "settings/" + claude.Runtime + "/settings.json"
	env, _ := res.Settings[claude.Runtime]["settings.json"]["env"].(map[string]any)
	if _, ok := env[configDir]; ok {
		lines = append(lines, "Remove env."+configDir+" from "+fragment+" in module "+accesskey.ShownName(res.SetBy(fragment+"/env."+configDir), false)+"."+module)
	}
	if m, ok := res.Exported[configDir]; ok {
		lines = append(lines, "Remove env."+configDir+" from "+modulepkg.ManifestName+" in module "+accesskey.ShownName(m, false)+"."+module)
	}
	cannot := func(where, value string) {
		lines = append(lines, "Set "+configDir+" in "+where+" to an absolute path outside the harness home. qory checks the Claude Code settings there for model variables, and cannot check "+modulepkg.Escape(exports.Value(value, false))+".")
	}
	under := written(res, runtimes, at)
	if v, ok := conf.Env[configDir]; ok && !checkable(v, at.home, under) {
		cannot("env in "+ui.Short(conf.Origin("env."+configDir), at.root), v)
	}
	if l, ok := conf.Launch[claude.Runtime]; ok {
		if v, ok := l.Env[configDir]; ok && !checkable(v, at.home, under) {
			cannot("harness.launch."+claude.Runtime+".env in "+ui.Short(conf.Origin("harness.launch."+claude.Runtime), at.root), v)
		}
	}
	if len(lines) > 0 {
		return errors.New(strings.Join(lines, "\n"))
	}
	return nil
}

// written lists the paths the compose writes or may replace with a link: the home, the
// checkout path of each link a runtime of the compose takes, such as .claude/skills, and
// the checkout-root name a stack links each module as. A home outside the checkout is one
// directory per checkout under the --home or harness.home directory, which stands in for
// the home.
func written(res *compose.Result, runtimes []render.Runtime, at places) []string {
	under := []string{at.home}
	if at.dir == "" {
		under[0] = filepath.Dir(at.home)
	}
	for _, rt := range runtimes {
		for _, l := range rt.Links(res) {
			under = append(under, filepath.Join(at.root, l.Checkout))
		}
	}
	for _, m := range res.Modules {
		if m.Link != "" {
			under = append(under, filepath.Join(at.root, m.Link))
		}
	}
	return under
}

// checkable reports whether a CLAUDE_CONFIG_DIR value from the configuration is a path
// qory can check: once ${dir}, ${home} and $QORY_HARNESS_HOME are replaced with home's
// paths, it is absolute, filepath.EvalSymlinks resolves it whole, so every part exists
// and every link resolves, and it is outside each path in under, both as written and
// resolved. A path that does not exist yet could become a link into the home, so it is
// not checkable. Its settings.json, when it is a link, must lead outside each path in
// under too, at every step of the link and once resolved, since the compose may rewrite
// what the link points through.
func checkable(value, home string, under []string) bool {
	v := strings.NewReplacer("${dir}", filepath.Join(home, claude.Runtime), "${home}", home).Replace(value)
	v = compose.ForHome(v, home).(string)
	if !filepath.IsAbs(v) {
		return false
	}
	seen, _, ok := walk("/", v)
	if !ok {
		return false
	}
	file, _, _ := walk("/", filepath.Join(v, "settings.json"))
	_, inside := insideAny(append(append([]string{filepath.Clean(v)}, seen...), file...), under)
	return !inside
}

// insideAny reports whether a path in paths is at or under a path in under. Each path in
// under is compared cleaned, resolved whole, and with only its parent resolved: a link the
// compose wrote at it resolves into the home, and a directory that stands there now does
// not. Each path in paths is compared cleaned and with its existing part resolved. The
// path in under that one leads into comes back with true.
func insideAny(paths, under []string) (string, bool) {
	for _, u := range under {
		for _, dir := range []string{filepath.Clean(u), resolvedHome(u), filepath.Join(resolvedHome(filepath.Dir(u)), filepath.Base(u))} {
			for _, p := range paths {
				if within(dir, filepath.Clean(p)) || within(dir, resolvedHome(p)) {
					return u, true
				}
			}
		}
	}
	return "", false
}

// fileSteps are the paths a [walk] of a settings file records from its first link on;
// none when the walk meets no link. A file in the checkout is walked from its folder with
// the links resolved, since the checkout's .claude is where it belongs; the user's file
// is walked from /. A folder that does not resolve gives none; the read then skips a
// missing file and reports any other error.
func fileSteps(path string, inCheckout bool) []string {
	from, name := "/", path
	if inCheckout {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil
		}
		from, name = dir, filepath.Base(path)
	}
	seen, first, _ := walk(from, name)
	if first < 0 {
		return nil
	}
	return seen[first:]
}

// walk resolves path the way the kernel does, component by component from from, a real
// folder such as /. For each component it reads the path so far without following it:
// a folder or file becomes the real path so far; a link has its target recorded as
// written, joined to the real folder so far, and the walk goes on through the target's
// components, from / for an absolute target, then the components left. A .. leads to the
// parent of the real folder so far. It records every real path it reaches, every folder
// and the last file, and each link target. It returns the recorded paths, the index of
// the first link target among them, -1 when it met no link, and false when a component
// is missing or it met more than maxLinks links; the missing path is recorded as well.
func walk(from, path string) ([]string, int, bool) {
	var seen []string
	first, links := -1, 0
	cur := from
	comps := strings.Split(path, string(filepath.Separator))
	if filepath.IsAbs(path) {
		cur = string(filepath.Separator)
	}
	for len(comps) > 0 {
		c := comps[0]
		comps = comps[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil {
			return append(seen, next), first, false
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			seen = append(seen, cur)
			continue
		}
		if links++; links > maxLinks {
			return seen, first, false
		}
		target, err := os.Readlink(next)
		if err != nil {
			return seen, first, false
		}
		if filepath.IsAbs(target) {
			cur = string(filepath.Separator)
		}
		if first < 0 {
			first = len(seen)
		}
		seen = append(seen, filepath.Join(cur, target))
		comps = append(strings.Split(target, string(filepath.Separator)), comps...)
	}
	return seen, first, true
}

// resolvedHome is the home directory path with the links of its longest existing ancestor
// resolved and the rest joined as written, since a first compose has not made the home
// yet.
func resolvedHome(path string) string {
	path = filepath.Clean(path)
	for p := path; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			rel, _ := filepath.Rel(p, path)
			return filepath.Join(r, rel)
		}
		if filepath.Dir(p) == p {
			return path
		}
	}
}

// modelNames are the names of [claude.ModelVariables] that env sets, sorted.
func modelNames[V any](env map[string]V) []string {
	var names []string
	for name := range env {
		if slices.Contains(claude.ModelVariables, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// claudeUserSettings is the user's Claude Code settings file: settings.json under
// $CLAUDE_CONFIG_DIR, else under ~/.claude; "" when the home directory is unknown.
func claudeUserSettings() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// launchSettings is the settings.json of the CLAUDE_CONFIG_DIR value that render.LaunchFor
// gives the claude launch on the home; "" when the value is unset or empty, or when the
// launch variables fail to build. [checkConfigDir] has refused a value that is relative
// or inside the home.
func launchSettings(res *compose.Result, conf config.Config, runtimes []render.Runtime, at places) string {
	i := slices.IndexFunc(runtimes, func(rt render.Runtime) bool { return rt.Name() == claude.Runtime })
	override := launchOverride(conf.Launch, claude.Runtime)
	vars, err := render.LaunchEnv(res, runtimes[i], override)
	if err != nil {
		return ""
	}
	l, err := render.LaunchFor(runtimes[i], at.home, override, vars)
	if err != nil {
		return ""
	}
	var dir string
	for _, kv := range l.Env() {
		if v, ok := strings.CutPrefix(kv, configDir+"="); ok {
			dir = v
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "settings.json")
}

// notJSON is the JSON decoder's error for a settings file that is not JSON.
type notJSON struct{ err error }

func (e *notJSON) Error() string { return e.err.Error() }

// settingsEnv is the env object of the Claude Code settings file at path, nil when the
// file is missing or has none; a link whose target does not exist is not missing. The
// error is a [notJSON] for a file that is not JSON, and the read error for a file qory
// cannot read.
func settingsEnv(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// A link whose target is not written yet is not a missing file: the target may be
		// written later, and qory cannot check it now.
		if _, lerr := os.Lstat(path); errors.Is(lerr, os.ErrNotExist) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, &notJSON{err: err}
	}
	env, _ := settings["env"].(map[string]any)
	return env, nil
}
