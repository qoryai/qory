package compose_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
)

// manifest is a layer manifest exporting one variable at the given path.
func manifest(name, variable, path string) string {
	return "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: " + name + "\nenv:\n  " + variable + ": " + path + "\n"
}

// TestLayersExportVariablesAsPathsUnderTheHome is a layer exporting HARNESS_HOME as its
// root and TOOLS as a directory inside it: each becomes a path under layers/<name>, with
// $QORY_HARNESS_HOME in place until EnvFor renders it for a home.
func TestLayersExportVariablesAsPathsUnderTheHome(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"harness-compose.yaml":        twoLayers,
		"layers/a/harness-layer.yaml": manifest("a", "HARNESS_HOME", "."),
		"layers/b/harness-layer.yaml": "apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\nname: b\nenv:\n  TOOLS: scripts/tools\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/layers/a" || res.Env["TOOLS"] != "$QORY_HARNESS_HOME/layers/b/scripts/tools" {
		t.Errorf("env: %v", res.Env)
	}
	if got := res.EnvFor("/work/.qory/harness"); got["TOOLS"] != "/work/.qory/harness/layers/b/scripts/tools" {
		t.Errorf("EnvFor: %v", got)
	}
}

// TestTwoLayersExportingOneNameIsRefused is two layers each exporting HARNESS_HOME as
// their own root: neither is the one to keep, so the compose refuses and points at the
// configuration; the same value twice is fine, and the configuration's value wins.
func TestTwoLayersExportingOneNameIsRefused(t *testing.T) {
	_, err := composeTree(t, map[string]string{
		"harness-compose.yaml":        twoLayers,
		"layers/a/harness-layer.yaml": manifest("a", "HARNESS_HOME", "."),
		"layers/b/harness-layer.yaml": manifest("b", "HARNESS_HOME", "."),
	})
	if err == nil || err.Error() != "env HARNESS_HOME is exported by layers a and b; set it in qory.yaml to decide" {
		t.Fatalf("err = %v", err)
	}
	dir := writeTree(t, map[string]string{
		"harness-compose.yaml":        twoLayers,
		"layers/a/harness-layer.yaml": manifest("a", "HARNESS_HOME", "."),
		"layers/b/harness-layer.yaml": manifest("b", "HARNESS_HOME", "."),
	})
	p, err := profile.Load(dir + "/harness-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Env: map[string]string{"HARNESS_HOME": "$QORY_HARNESS_HOME/layers/b", "PROFILE": "nextjs"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Env["HARNESS_HOME"] != "$QORY_HARNESS_HOME/layers/b" || res.Env["PROFILE"] != "nextjs" {
		t.Errorf("env: %v", res.Env)
	}
}

// TestMCPForDropsTheDescription is a server whose file carries a description: the
// composed server keeps it for the report's readers and the rendered one leaves it out.
func TestMCPForDropsTheDescription(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		"harness-compose.yaml": twoLayers,
		"layers/a/mcp/db.json": `{"command": "python3", "args": ["$QORY_HARNESS_HOME/layers/a/db.py"], "description": "pinned to the app's version"}`,
		"layers/b/":            "",
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
	if args := server["args"].([]any); !strings.HasPrefix(args[0].(string), "/work/.qory/harness/layers/a/") {
		t.Errorf("args: %v", args)
	}
}
