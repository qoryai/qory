package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// envStack is a stack over module core, which exports HARNESS_HOME as its root and
// TOKEN_DIR as a directory inside it, and sets LOG_LEVEL in a Claude Code and a Codex
// fragment.
var envStack = map[string]string{
	"qory.yaml":                                  "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n",
	"modules/core/qory-module.yaml":              "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  HARNESS_HOME: .\n  TOKEN_DIR: tokens\n",
	"modules/core/tokens/.keep":                  "",
	"modules/core/settings/claude/settings.json": `{"env": {"LOG_LEVEL": "debug"}, "permissions": {"allow": ["Read"]}}`,
	"modules/core/settings/codex/config.toml":    "[shell_environment_policy.set]\nLOG_LEVEL = \"debug\"\n",
}

// withFile is envStack with one file replaced.
func withFile(name, content string) map[string]string {
	files := map[string]string{}
	for k, v := range envStack {
		files[k] = v
	}
	files[name] = content
	return files
}

// composeEnv composes files for the runtimes with the configuration's env, and returns the
// result and a home in a fresh checkout.
func composeEnv(t *testing.T, files map[string]string, env map[string]string, runtimes ...string) (*compose.Result, string) {
	t.Helper()
	hermetic(t)
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := config.Compose(filepath.Join(dir, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = runtimes
	res, err := compose.ComposeWith(p, compose.Options{Env: env})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return res, filepath.Join(root, ".qory", "harness")
}

// launchFor builds the home for the runtime and resolves its launch with the override,
// the way qory harness launch does from the report.
func launchFor(t *testing.T, res *compose.Result, home, runtime string, override *render.Template) render.Launch {
	t.Helper()
	rt := lookup(t, runtime)
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	vars, err := render.LaunchEnv(res, rt, override)
	if err != nil {
		t.Fatal(err)
	}
	l, err := render.LaunchFor(rt, home, override, vars)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestSettingsHoldNoHarnessEnv is a compose exporting variables, setting one in the
// configuration and one in each fragment: neither Claude Code's settings.json nor Codex's
// config.toml holds any of them or QORY_HARNESS_HOME, and the rest of each fragment is
// written as before. A Codex policy table left empty is not written; one with keys of its
// own stays, without set.
func TestSettingsHoldNoHarnessEnv(t *testing.T) {
	res, home := composeEnv(t, envStack, map[string]string{"PROFILE": "nextjs"}, "claude", "codex")
	if err := render.Build(res, home, lookup(t, "claude"), lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	settings := read(t, filepath.Join(home, "claude", "settings.json"))
	lacks(t, "claude settings.json", settings, `"env"`, "QORY_HARNESS_HOME", "HARNESS_HOME", "TOKEN_DIR", "LOG_LEVEL", "PROFILE")
	has(t, "claude settings.json", settings, `"Read"`)
	config := read(t, filepath.Join(home, "codex", "config.toml"))
	lacks(t, "codex config.toml", config, "shell_environment_policy", "QORY_HARNESS_HOME", "HARNESS_HOME", "TOKEN_DIR", "LOG_LEVEL", "PROFILE")

	res, home = composeEnv(t, withFile("modules/core/settings/codex/config.toml", "[shell_environment_policy]\nexclude = [\"AWS_*\"]\n[shell_environment_policy.set]\nLOG_LEVEL = \"debug\"\n"), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	config = read(t, filepath.Join(home, "codex", "config.toml"))
	has(t, "codex config.toml", config, "[shell_environment_policy]", `exclude = ["AWS_*"]`)
	lacks(t, "codex config.toml", config, "set", "LOG_LEVEL")
}

// TestLaunchSortsFixedAndDefaults is Codex's launch over a compose whose configuration
// sets PROFILE and HARNESS_HOME, the second over core's export: the template's
// CODEX_HOME alone is fixed, and the export TOKEN_DIR is a default, as are the
// configuration's HARNESS_HOME, PROFILE and the fragment's LOG_LEVEL. QORY_HARNESS_HOME
// is the home, apart from both, and first in the whole.
func TestLaunchSortsFixedAndDefaults(t *testing.T) {
	res, home := composeEnv(t, envStack, map[string]string{"PROFILE": "nextjs", "HARNESS_HOME": "/opt/core"}, "codex")
	l := launchFor(t, res, home, "codex", nil)
	wantFixed := []string{"CODEX_HOME=" + home + "/codex"}
	wantDefaults := []string{"HARNESS_HOME=/opt/core", "LOG_LEVEL=debug", "PROFILE=nextjs", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(l.Fixed, wantFixed) || !reflect.DeepEqual(l.Defaults, wantDefaults) || l.HarnessHome != home {
		t.Errorf("launch fixed %q defaults %q home %q\nwant fixed %q defaults %q home %q", l.Fixed, l.Defaults, l.HarnessHome, wantFixed, wantDefaults, home)
	}
	want := append(append([]string{"QORY_HARNESS_HOME=" + home}, wantFixed...), wantDefaults...)
	if got := l.Env(); !reflect.DeepEqual(got, want) {
		t.Errorf("env %q, want %q", got, want)
	}
}

// TestLaunchOverrideEnvIsADefault is harness.launch.codex.env replacing the template's
// variables with CODEX_HOME at ${dir} and an API key: both are defaults, the
// placeholder replaced; the template's own CODEX_HOME is gone, nothing is fixed, and
// the exports are defaults as ever. A variable naming a path the compose did not write is
// left out, as before.
func TestLaunchOverrideEnvIsADefault(t *testing.T) {
	res, home := composeEnv(t, envStack, nil, "codex")
	l := launchFor(t, res, home, "codex", &render.Template{Env: map[string]string{"CODEX_HOME": "${dir}", "OPENAI_API_KEY": "sk-test", "MISSING": "${dir}/missing.json"}})
	var wantFixed []string
	wantDefaults := []string{"CODEX_HOME=" + home + "/codex", "HARNESS_HOME=" + home + "/modules/core", "LOG_LEVEL=debug", "OPENAI_API_KEY=sk-test", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(l.Fixed, wantFixed) || !reflect.DeepEqual(l.Defaults, wantDefaults) {
		t.Errorf("launch fixed %q defaults %q\nwant fixed %q defaults %q", l.Fixed, l.Defaults, wantFixed, wantDefaults)
	}
}

// TestTheTemplatesOwnVariableStaysFixed is Codex's launch over a compose whose
// configuration sets CODEX_HOME, a fragment setting it too, and a module exporting
// OPENCODE_CONFIG_DIR, read by OpenCode: the runtime's own template keeps its value, fixed,
// and none of the three is a variable of the launch or of the report's launch_env. The
// same variables passed to the launch as an older report recorded them are left out too.
// The exports are defaults. With harness.launch.codex.env replacing the template's, its
// CODEX_HOME is a default and the configuration's value layers over it.
func TestTheTemplatesOwnVariableStaysFixed(t *testing.T) {
	files := withFile("modules/core/settings/codex/config.toml", "[shell_environment_policy.set]\nLOG_LEVEL = \"debug\"\nCODEX_HOME = \"/fragment\"\n")
	files["modules/core/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  HARNESS_HOME: .\n  TOKEN_DIR: tokens\n  OPENCODE_CONFIG_DIR: .\n"
	res, home := composeEnv(t, files, map[string]string{"CODEX_HOME": "/elsewhere"}, "codex", "opencode")
	rt := lookup(t, "codex")
	l := launchFor(t, res, home, "codex", nil)
	wantFixed := []string{"CODEX_HOME=" + home + "/codex"}
	defaults := []string{"HARNESS_HOME=" + home + "/modules/core", "LOG_LEVEL=debug", "OPENCODE_CONFIG_DIR=" + home + "/modules/core", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(l.Fixed, wantFixed) || !reflect.DeepEqual(l.Defaults, defaults) {
		t.Errorf("codex launch fixed %q defaults %q\nwant fixed %q defaults %q", l.Fixed, l.Defaults, wantFixed, defaults)
	}
	vars, err := render.LaunchEnv(res, rt, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.Name == "CODEX_HOME" {
			t.Errorf("codex launch_env holds %+v; the template's own CODEX_HOME is fixed", v)
		}
	}
	wantDefaults := []string{"LOG_LEVEL=debug"}
	all := []compose.Var{{Name: "CODEX_HOME", Value: "/elsewhere", From: "configuration"}, {Name: "LOG_LEVEL", Value: "debug", From: "module core, settings/codex/config.toml"}}
	l, err = render.LaunchFor(rt, home, nil, all)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CODEX_HOME=" + home + "/codex"}; !reflect.DeepEqual(l.Fixed, want) || !reflect.DeepEqual(l.Defaults, wantDefaults) {
		t.Errorf("codex launch from a recorded CODEX_HOME: fixed %q defaults %q, want fixed %q defaults %q", l.Fixed, l.Defaults, want, wantDefaults)
	}

	oc := launchFor(t, res, home, "opencode", nil)
	wantFixed = []string{"OPENCODE_CONFIG_DIR=" + home + "/opencode"}
	wantDefaults = []string{"CODEX_HOME=/elsewhere", "HARNESS_HOME=" + home + "/modules/core", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(oc.Fixed, wantFixed) || !reflect.DeepEqual(oc.Defaults, wantDefaults) {
		t.Errorf("opencode launch fixed %q defaults %q\nwant fixed %q defaults %q", oc.Fixed, oc.Defaults, wantFixed, wantDefaults)
	}

	l = launchFor(t, res, home, "codex", &render.Template{Env: map[string]string{"CODEX_HOME": "${dir}"}})
	wantFixed = nil
	wantDefaults = []string{"CODEX_HOME=/elsewhere", "HARNESS_HOME=" + home + "/modules/core", "LOG_LEVEL=debug", "OPENCODE_CONFIG_DIR=" + home + "/modules/core", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(l.Fixed, wantFixed) || !reflect.DeepEqual(l.Defaults, wantDefaults) {
		t.Errorf("codex launch under harness.launch.codex.env: fixed %q defaults %q\nwant fixed %q defaults %q", l.Fixed, l.Defaults, wantFixed, wantDefaults)
	}
}

// TestRuntimeWithoutAPlaceGetsTheLaunchEnv is OpenCode, whose settings have no place for
// variables: its launch carries the exports and the configuration's, defaults, beside its
// own OPENCODE_CONFIG_DIR, fixed, and no fragment's. Goose has no launch template and gets
// none.
func TestRuntimeWithoutAPlaceGetsTheLaunchEnv(t *testing.T) {
	res, home := composeEnv(t, envStack, map[string]string{"PROFILE": "nextjs"}, "opencode")
	l := launchFor(t, res, home, "opencode", nil)
	wantFixed := []string{"OPENCODE_CONFIG_DIR=" + home + "/opencode"}
	wantDefaults := []string{"HARNESS_HOME=" + home + "/modules/core", "PROFILE=nextjs", "TOKEN_DIR=" + home + "/modules/core/tokens"}
	if !reflect.DeepEqual(l.Fixed, wantFixed) || !reflect.DeepEqual(l.Defaults, wantDefaults) {
		t.Errorf("launch fixed %q defaults %q", l.Fixed, l.Defaults)
	}
	if vars, err := render.LaunchEnv(res, lookup(t, "goose"), nil); err != nil || vars != nil {
		t.Errorf("goose launch env %v %v, want none", vars, err)
	}
}

// codexPolicy is Codex's shell_environment_policy as config.toml holds it.
type codexPolicy struct {
	Inherit               *string           `toml:"inherit"`
	IgnoreDefaultExcludes *bool             `toml:"ignore_default_excludes"`
	Exclude               []string          `toml:"exclude"`
	Set                   map[string]string `toml:"set"`
	IncludeOnly           []string          `toml:"include_only"`
}

// codexShellEnv is the environment Codex gives a command it runs, from its own
// environment and the policy in config.toml, as Codex derives it (codex-rs
// protocol/src/shell_environment.rs populate_env, with the defaults of
// config/src/shell_environment_policy.rs): inherit "all" and ignore_default_excludes
// true when the file leaves them out; then, unless the default excludes are ignored,
// every name matching *KEY*, *SECRET* or *TOKEN*, case-insensitively, is dropped; then
// exclude, then set, then include_only. Core inheritance is not modelled.
func codexShellEnv(t *testing.T, configToml string, env []string) map[string]string {
	t.Helper()
	var doc struct {
		Policy codexPolicy `toml:"shell_environment_policy"`
	}
	if _, err := toml.Decode(configToml, &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.Policy
	out := map[string]string{}
	if p.Inherit == nil || *p.Inherit == "all" {
		for _, kv := range env {
			name, value, _ := strings.Cut(kv, "=")
			out[name] = value
		}
	}
	matches := func(name string, patterns []string) bool {
		for _, pattern := range patterns {
			if ok, _ := filepath.Match(strings.ToUpper(pattern), strings.ToUpper(name)); ok {
				return true
			}
		}
		return false
	}
	drop := func(keep func(string) bool) {
		for name := range out {
			if !keep(name) {
				delete(out, name)
			}
		}
	}
	if p.IgnoreDefaultExcludes != nil && !*p.IgnoreDefaultExcludes {
		drop(func(n string) bool { return !matches(n, []string{"*KEY*", "*SECRET*", "*TOKEN*"}) })
	}
	drop(func(n string) bool { return !matches(n, p.Exclude) })
	for name, value := range p.Set {
		out[name] = value
	}
	if len(p.IncludeOnly) > 0 {
		drop(func(n string) bool { return matches(n, p.IncludeOnly) })
	}
	return out
}

// TestCodexShellGetsTheLaunchEnv is the composed config.toml under Codex's own policy
// defaults: a command Codex runs gets every variable of the launch, the export TOKEN_DIR
// and the configuration's SERVICE_TOKEN among them, whose names match Codex's default
// excludes, since Codex ignores those unless a policy says otherwise. A fragment that
// sets ignore_default_excludes = false filters them, as it filters any other variable.
func TestCodexShellGetsTheLaunchEnv(t *testing.T) {
	res, home := composeEnv(t, envStack, map[string]string{"SERVICE_TOKEN": "t-1"}, "codex")
	l := launchFor(t, res, home, "codex", nil)
	shell := codexShellEnv(t, read(t, filepath.Join(home, "codex", "config.toml")), l.Env())
	for _, kv := range l.Env() {
		name, value, _ := strings.Cut(kv, "=")
		if shell[name] != value {
			t.Errorf("a command Codex runs gets %s=%q, want %q", name, shell[name], value)
		}
	}
	if shell["TOKEN_DIR"] == "" || shell["SERVICE_TOKEN"] != "t-1" {
		t.Errorf("shell env %v lacks TOKEN_DIR or SERVICE_TOKEN", shell)
	}

	res, home = composeEnv(t, withFile("modules/core/settings/codex/config.toml", "[shell_environment_policy]\nignore_default_excludes = false\n"), map[string]string{"SERVICE_TOKEN": "t-1"}, "codex")
	l = launchFor(t, res, home, "codex", nil)
	shell = codexShellEnv(t, read(t, filepath.Join(home, "codex", "config.toml")), l.Env())
	if _, ok := shell["SERVICE_TOKEN"]; ok {
		t.Errorf("SERVICE_TOKEN reached the shell under ignore_default_excludes = false: %v", shell)
	}
	if shell["HARNESS_HOME"] == "" {
		t.Errorf("HARNESS_HOME is filtered: %v", shell)
	}
}
