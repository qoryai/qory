package compose_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// manifest is a module manifest exporting one variable at the given path.
func manifest(name, variable, path string) string {
	return "apiVersion: qory.ai/v1alpha1\nname: " + name + "\nenv:\n  " + variable + ": " + path + "\n"
}

// TestModulesExportVariablesAsPathsUnderTheHome is a module exporting HARNESS_HOME as its
// root and TOOLS as a directory inside it: each becomes a path under modules/<name>, with
// $QORY_HARNESS_HOME in place until EnvFor renders it for a home.
func TestModulesExportVariablesAsPathsUnderTheHome(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"qory-stack.yaml":            twoModules,
		"modules/a/qory-module.yaml": manifest("a", "HARNESS_HOME", "."),
		"modules/b/qory-module.yaml": "apiVersion: qory.ai/v1alpha1\nname: b\nenv:\n  TOOLS: scripts/tools\n",
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
	p, err := stack.Load(dir + "/qory-stack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Env: map[string]string{"HARNESS_HOME": "$QORY_HARNESS_HOME/modules/b", "APP_ENV": "staging"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/modules/b" || res.Env["APP_ENV"] != "staging" {
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
		"qory-stack.yaml":            strings.Replace(twoModules, "apiVersion: qory.ai/v1alpha1\n", "apiVersion: qory.ai/v1alpha1\ndescription: Two modules\n", 1),
		"modules/a/qory-module.yaml": "apiVersion: qory.ai/v1alpha1\nname: a\ndescription: the first\n",
		"modules/b/":                 "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stack.Description != "Two modules" || res.Modules[0].Description != "the first" || res.Modules[1].Description != "" {
		t.Errorf("descriptions: stack %q, modules %+v", res.Stack.Description, res.Modules)
	}
}
