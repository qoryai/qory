package compose_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// composeTreeWith is composeTree with options.
func composeTreeWith(t *testing.T, files map[string]string, opts compose.Options) (*compose.Result, error) {
	t.Helper()
	dir := writeTree(t, files)
	p, err := stack.Load(filepath.Join(dir, stack.FileName))
	if err != nil {
		return nil, err
	}
	return compose.ComposeWith(p, opts)
}

// TestSettingsSameValueTwiceMerges is two fragments setting model.name, a list and an env
// key to the same values: the merged file holds each once.
func TestSettingsSameValueTwiceMerges(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		stack.FileName: twoModules,
		"modules/a/settings/claude/settings.json": `{"model": {"name": "opus"}, "paths": ["x"], "env": {"A": "1"}}`,
		"modules/b/settings/claude/settings.json": `{"model": {"name": "opus"}, "paths": ["x"], "env": {"A": "1"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := jsonValue(t, `{"model": {"name": "opus"}, "paths": ["x"], "env": {"A": "1"}}`)
	if got := res.Settings["claude"]["settings.json"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings %v, want %v", got, want)
	}
}

// TestSettingsDifferentValuesCollide is a scalar, a list outside permissions and hooks, and
// a value whose shape changes, each set differently by two modules: the compose fails and
// names the target file, the dotted path and both modules in stack order.
func TestSettingsDifferentValuesCollide(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want string
	}{
		{"scalar", `{"model": {"name": "opus"}}`, `{"model": {"name": "sonnet"}}`, "model.name"},
		{"list", `{"paths": ["x"]}`, `{"paths": ["x", "y"]}`, "paths"},
		{"shape", `{"model": "opus"}`, `{"model": {"name": "opus"}}`, "model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, map[string]string{
				stack.FileName: twoModules,
				"modules/a/settings/claude/settings.json": c.a,
				"modules/b/settings/claude/settings.json": c.b,
			})
			want := "module b: settings/claude/settings.json: " + c.want + " is set by modules a and b with different values"
			if err == nil || err.Error() != want {
				t.Fatalf("error %v, want %q", err, want)
			}
		})
	}
}

// TestSettingsPermissionsAndHooksConcatenate is two modules each adding a permission, one
// of them twice, and a hook: permissions concatenate without the duplicate, hooks
// concatenate, and neither collides.
func TestSettingsPermissionsAndHooksConcatenate(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		stack.FileName: twoModules,
		"modules/a/settings/claude/settings.json": `{"permissions": {"allow": ["Read", "Write"]}, "hooks": {"PreToolUse": [{"matcher": "Bash"}]}}`,
		"modules/b/settings/claude/settings.json": `{"permissions": {"allow": ["Write", "Edit"]}, "hooks": {"PreToolUse": [{"matcher": "Bash"}]}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := jsonValue(t, `{"permissions": {"allow": ["Read", "Write", "Edit"]}, "hooks": {"PreToolUse": [{"matcher": "Bash"}, {"matcher": "Bash"}]}}`)
	if got := res.Settings["claude"]["settings.json"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings %v, want %v", got, want)
	}
}

// TestSettingsEnvDecidedByTheConfigurationDoesNotCollide is two fragments setting env.A
// differently while the configuration names A: the compose succeeds and the merged env
// holds the configuration's value.
func TestSettingsEnvDecidedByTheConfigurationDoesNotCollide(t *testing.T) {
	res, err := composeTreeWith(t, map[string]string{
		stack.FileName: twoModules,
		"modules/a/settings/claude/settings.json": `{"env": {"A": "a"}}`,
		"modules/b/settings/claude/settings.json": `{"env": {"A": "b"}}`,
	}, compose.Options{Env: map[string]string{"A": "decided"}})
	if err != nil {
		t.Fatal(err)
	}
	env := res.Settings["claude"]["settings.json"]["env"].(map[string]any)
	if env["A"] != "decided" {
		t.Fatalf("env %v, want A decided", env)
	}
}

// TestExportAgainstFragmentEnvCollides is module a setting env.TOOLS in its fragment and
// module b exporting TOOLS from its manifest with another value: the compose fails and names
// both, unless the configuration names TOOLS, and a fragment that repeats the exported
// value is fine.
func TestExportAgainstFragmentEnvCollides(t *testing.T) {
	files := func(value string) map[string]string {
		return map[string]string{
			stack.FileName: twoModules,
			"modules/a/settings/claude/settings.json": `{"env": {"TOOLS": "` + value + `"}}`,
			"modules/b/qory-module.yaml":              manifest("b", "TOOLS", "scripts/tools"),
		}
	}
	_, err := composeTree(t, files("/usr/local/bin"))
	want := "settings/claude/settings.json: env.TOOLS is set by module a and exported by module b with different values"
	if err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
	if _, err := composeTree(t, files("$QORY_HARNESS_HOME/modules/b/scripts/tools")); err != nil {
		t.Fatalf("the exported value repeated in a fragment: %v", err)
	}
	res, err := composeTreeWith(t, files("/usr/local/bin"), compose.Options{Env: map[string]string{"TOOLS": "/opt/tools"}})
	if err != nil {
		t.Fatal(err)
	}
	env := res.Settings["claude"]["settings.json"]["env"].(map[string]any)
	if env["TOOLS"] != "/opt/tools" || res.Env["TOOLS"] != "/opt/tools" {
		t.Fatalf("settings env %v, exported env %v, want TOOLS /opt/tools in both", env, res.Env)
	}
}

// TestSelectionRecordsEveryPartLeftOut is the report's view of an exclude and an only
// block: the instruction section, a settings fragment and every entry a block left out
// are recorded beside the excluded entries, so inspect says why a module contributed
// less than it ships.
func TestSelectionRecordsEveryPartLeftOut(t *testing.T) {
	for fixture, want := range map[string][]string{
		"exclude-drops-parts":  {"core instructions/AGENTS.md", "core settings/claude/settings.json"},
		"only-keeps-one-skill": {"core agents/reviewer", "core commands/ship", "core hooks/guard.sh", "core mcp/db", "core output-styles/terse", "core skills/test", "core instructions/AGENTS.md", "core settings/claude/settings.json"},
	} {
		p, err := stack.Load(filepath.Join(fixtures, fixture, stack.FileName))
		if err != nil {
			t.Fatal(err)
		}
		res, err := compose.Compose(p)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range res.Excludes {
			got = append(got, x.Module+" "+x.Kind+"/"+x.Name)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: excludes\n%s\nwant\n%s", fixture, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// TestOnlyBringsInWhatANamedEntryRequires is the pull: only names one skill, the module's
// manifest says what it needs, and the compose brings those in, transitively, each marked
// with the entry that required it; an exclude beside the only leaves one of them to
// another module.
func TestOnlyBringsInWhatANamedEntryRequires(t *testing.T) {
	for fixture, want := range map[string]map[string]string{
		"only-pulls-requirements":      {"agents/reviewer": "skills/deploy", "commands/ship": "skills/deploy", "hooks/guard.sh": "commands/ship", "skills/deploy": ""},
		"only-excludes-a-pulled-entry": {"agents/reviewer": "", "commands/ship": "skills/deploy", "hooks/guard.sh": "commands/ship", "skills/deploy": ""},
	} {
		p, err := stack.Load(filepath.Join(fixtures, fixture, stack.FileName))
		if err != nil {
			t.Fatal(err)
		}
		res, err := compose.Compose(p)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, e := range res.Entries {
			got[e.Kind+"/"+e.Name] = e.For
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: for = %v, want %v", fixture, got, want)
		}
	}
}
