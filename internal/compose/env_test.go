package compose_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
)

// manifest is a module manifest exporting one variable at the given path.
func manifest(name, variable, path string) string {
	return "apiVersion: qory.dev/v1alpha1\nname: " + name + "\nenv:\n  " + variable + ": " + path + "\n"
}

// TestModulesExportVariablesAsPathsUnderTheHome is a module exporting HARNESS_HOME as its
// root and TOOLS as a directory inside it: each becomes a path under modules/<name>, with
// $QORY_HARNESS_HOME in place until EnvFor renders it for a home.
func TestModulesExportVariablesAsPathsUnderTheHome(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"qory-stack.yaml":            twoModules,
		"modules/a/qory-module.yaml": manifest("a", "HARNESS_HOME", "."),
		"modules/b/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: b\nenv:\n  TOOLS: scripts/tools\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/modules/a" || res.Env["TOOLS"] != "$QORY_HARNESS_HOME/modules/b/scripts/tools" {
		t.Errorf("env: %v", res.Env)
	}
	if got := res.EnvFor("/work/.qory/harness"); got["TOOLS"] != "/work/.qory/harness/modules/b/scripts/tools" {
		t.Errorf("EnvFor: %v", got)
	}
}

// TestTwoModulesExportingOneNameIsRefused is two modules each exporting HARNESS_HOME as
// their own root: neither is the one to keep, so the compose refuses and points at the
// configuration; the same value twice is fine, and the configuration's value wins.
func TestTwoModulesExportingOneNameIsRefused(t *testing.T) {
	_, err := composeTree(t, map[string]string{
		"qory-stack.yaml":            twoModules,
		"modules/a/qory-module.yaml": manifest("a", "HARNESS_HOME", "."),
		"modules/b/qory-module.yaml": manifest("b", "HARNESS_HOME", "."),
	})
	if err == nil || err.Error() != "env HARNESS_HOME is exported by modules a and b; set it in qory.yaml to decide" {
		t.Fatalf("err = %v", err)
	}
	dir := writeTree(t, map[string]string{
		"qory-stack.yaml":            twoModules,
		"modules/a/qory-module.yaml": manifest("a", "HARNESS_HOME", "."),
		"modules/b/qory-module.yaml": manifest("b", "HARNESS_HOME", "."),
	})
	p, err := loadFor(dir+"/qory-stack.yaml", "claude")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Env: map[string]string{"HARNESS_HOME": "$QORY_HARNESS_HOME/modules/b", "FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/modules/b" || res.Env["FOO"] != "bar" {
		t.Errorf("env: %v", res.Env)
	}
}

// TestMCPForDropsTheDescription is a server whose file carries a description: the
// composed server keeps it for the report's readers and the rendered one leaves it out.
func TestMCPForDropsTheDescription(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"qory-stack.yaml":       twoModules,
		"modules/a/mcp/db.json": `{"command": "python3", "args": ["$QORY_HARNESS_HOME/modules/a/db.py"], "description": "pinned to the app's version"}`,
		"modules/b/":            "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.MCP["db"]["description"] != "pinned to the app's version" {
		t.Errorf("composed server lost its description: %v", res.MCP["db"])
	}
	server := res.MCPFor("/work/.qory/harness")["db"].(map[string]any)
	if _, ok := server["description"]; ok {
		t.Errorf("rendered server carries the description: %v", server)
	}
	if args := server["args"].([]any); !strings.HasPrefix(args[0].(string), "/work/.qory/harness/modules/a/") {
		t.Errorf("args: %v", args)
	}
}

// TestDescriptionsAreRead is a stack and a manifest carrying a description: both come
// through the compose as written.
func TestDescriptionsAreRead(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"qory-stack.yaml":            strings.Replace(twoModules, "apiVersion: qory.dev/v1alpha1\n", "apiVersion: qory.dev/v1alpha1\ndescription: Two modules\n", 1),
		"modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\ndescription: the first\n",
		"modules/b/":                 "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stack.Description != "Two modules" || res.Modules[0].Description != "the first" || res.Modules[1].Description != "" {
		t.Errorf("descriptions: stack %q, modules %+v", res.Stack.Description, res.Modules)
	}
}

// TestLaunchEnvSortsByWhereTheValueCameFrom is module a exporting HARNESS_HOME and TOOLS,
// module b's fragment setting LOG_LEVEL, a number, QORY_HARNESS_HOME and the exported
// HARNESS_HOME again, and the configuration setting TOOLS and PROFILE. The layering is the
// environment's: the fragment's, the exports over them, the configuration's over all. An
// export's value is fixed; the configuration's TOOLS over the export, the fragment's own
// variables and PROFILE are defaults; QORY_HARNESS_HOME is none of them.
func TestLaunchEnvSortsByWhereTheValueCameFrom(t *testing.T) {
	res, err := composeTreeWith(t, map[string]string{
		"qory-stack.yaml":                         twoModules,
		"modules/a/qory-module.yaml":              "apiVersion: qory.dev/v1alpha1\nname: a\nenv:\n  HARNESS_HOME: .\n  TOOLS: scripts/tools\n",
		"modules/b/settings/claude/settings.json": `{"env": {"LOG_LEVEL": "debug", "PORT": 8080, "QORY_HARNESS_HOME": "/elsewhere", "HARNESS_HOME": "$QORY_HARNESS_HOME/modules/a"}}`,
	}, compose.Options{Env: map[string]string{"TOOLS": "/opt/tools", "PROFILE": "nextjs"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := res.LaunchEnv("claude", "settings.json", "env")
	if err != nil {
		t.Fatal(err)
	}
	want := []compose.Var{
		{Name: "HARNESS_HOME", Value: "$QORY_HARNESS_HOME/modules/a", From: "module a", Fixed: true},
		{Name: "LOG_LEVEL", Value: "debug", From: "module b, settings/claude/settings.json"},
		{Name: "PORT", Value: "8080", From: "module b, settings/claude/settings.json"},
		{Name: "PROFILE", Value: "nextjs", From: "configuration"},
		{Name: "TOOLS", Value: "/opt/tools", From: "configuration"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("launch env\n%+v\nwant\n%+v", got, want)
	}
	// A runtime whose settings have no place for variables gets the exports and the
	// configuration's alone.
	got, err = res.LaunchEnv("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "HARNESS_HOME" || got[1].Name != "PROFILE" || got[2].Name != "TOOLS" {
		t.Errorf("launch env without a settings place: %+v", got)
	}
}

// TestLaunchEnvReadsCodexsSet is a codex fragment setting a variable under
// shell_environment_policy.set beside an exported one of the same name: the export is
// over the fragment, as it always was in the file, and the fragment's other variable is
// a default. A table as a value is refused.
func TestLaunchEnvReadsCodexsSet(t *testing.T) {
	files := func(fragment string) map[string]string {
		return map[string]string{
			"qory-stack.yaml":                      twoModules,
			"modules/a/qory-module.yaml":           manifest("a", "TOOLS", "scripts/tools"),
			"modules/b/settings/codex/config.toml": fragment,
		}
	}
	res, err := composeTreeFor(t, files("[shell_environment_policy]\ninherit = \"all\"\n[shell_environment_policy.set]\nTOOLS = \"/usr/local/bin\"\nCI = \"1\"\n"), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got, err := res.LaunchEnv("codex", "config.toml", "shell_environment_policy", "set")
	if err != nil {
		t.Fatal(err)
	}
	want := []compose.Var{
		{Name: "CI", Value: "1", From: "module b, settings/codex/config.toml"},
		{Name: "TOOLS", Value: "$QORY_HARNESS_HOME/modules/a/scripts/tools", From: "module a", Fixed: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("launch env\n%+v\nwant\n%+v", got, want)
	}
	res, err = composeTreeFor(t, files("[shell_environment_policy.set.CI]\nvalue = \"1\"\n"), "codex")
	if err != nil {
		t.Fatal(err)
	}
	_, err = res.LaunchEnv("codex", "config.toml", "shell_environment_policy", "set")
	if want := "settings/codex/config.toml: shell_environment_policy.set.CI is not a string, a number or a boolean; a variable's value is text"; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}
