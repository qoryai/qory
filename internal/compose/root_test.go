package compose_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	_ "github.com/qoryai/qory/internal/render/claude"
	"github.com/qoryai/qory/internal/stack"
)

// repointed composes the module app at ./harness, a link to harness-real, for a home
// outside the checkout, with the link pointed at a directory outside the checkout at the
// moment hook selects, and builds the home of the result with the module copied. It
// returns the module's root, harness-real, and the staging tree, which must not contain
// the outside directory's id_example.
func repointed(t *testing.T, before bool) (*compose.Result, string, string) {
	t.Helper()
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		stack.FileName:                        "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: app\n    source:\n      path: ./harness\n",
		"harness-real/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: app\n",
		"harness-real/skills/deploy/SKILL.md": "---\nname: deploy\n---\n\nSteps.\n",
	})
	harness := filepath.Join(dir, "harness")
	if err := os.Symlink("harness-real", harness); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	for name, body := range map[string]string{"qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: app\n", "id_example": "OUTSIDE\n"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repoint := func(string) {
		if err := os.Remove(harness); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, harness); err != nil {
			t.Fatal(err)
		}
	}
	if before {
		compose.SetManifestHooks(t, repoint, nil)
	} else {
		compose.SetManifestHooks(t, nil, repoint)
	}
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Copies: everything})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Close() })
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	err = render.BuildAt(res, stage, stage, rt)
	t.Logf("build: %v", err)
	return res, filepath.Join(dir, "harness-real"), stage
}

// everything is [compose.Options.Copies] for a home that holds a copy of every module.
func everything(string) bool { return true }

// noOutside fails the test when the staging tree contains the outside directory's file,
// and when the module's recorded directory is not harness-real.
func noOutside(t *testing.T, res *compose.Result, real, stage string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(stage, "modules", "app", "id_example")); err == nil {
		t.Error("modules/app holds id_example from outside the checkout")
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Modules[0].DirInfo; got == nil || !os.SameFile(got, info) {
		t.Errorf("the module's recorded directory is not harness-real: %v", got)
	}
}

// TestARepointAfterTheManifestCopiesNothingFromOutside is ./harness pointed outside the
// checkout after the compose read the module's manifest: the home's copy of the module
// takes nothing from outside.
func TestARepointAfterTheManifestCopiesNothingFromOutside(t *testing.T) {
	res, real, stage := repointed(t, false)
	noOutside(t, res, real, stage)
}

// TestARepointBeforeTheManifestCopiesNothingFromOutside is ./harness pointed outside the
// checkout after the compose opened the module's directory and before it read the
// manifest, a manifest the outside directory also holds: the home's copy of the module
// takes nothing from outside.
func TestARepointBeforeTheManifestCopiesNothingFromOutside(t *testing.T) {
	res, real, stage := repointed(t, true)
	noOutside(t, res, real, stage)
}

// TestPathReadsOfACopiedModuleGoThroughItsRoot is a copied module whose ./harness leads
// outside the checkout after the compose read the manifest and back before the build.
// The outside directory has an AGENTS.md, a settings fragment and an MCP server at the
// module's names, each holding a marker: no file of the home holds the marker, and the
// module's values, read through its root, reach the home, the fragment's statusLine in
// claude/settings.json among them.
func TestPathReadsOfACopiedModuleGoThroughItsRoot(t *testing.T) {
	isolateAll(t)
	const inside, outside = "INSIDE-VALUE", "OUTSIDE-MARKER"
	files := func(mark string) map[string]string {
		return map[string]string{
			"AGENTS.md":                     mark + "\n",
			"settings/claude/settings.json": `{"env":{"PROBE":"` + mark + `"},"statusLine":{"type":"command","command":"echo ` + mark + `"}}`,
			"mcp/probe.json":                `{"command":"probe","env":{"TOKEN":"` + mark + `"}}`,
		}
	}
	tree := map[string]string{
		stack.FileName:                        "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: app\n    source:\n      path: ./harness\n",
		"harness-real/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: app\n",
		"harness-real/skills/deploy/SKILL.md": "---\nname: deploy\n---\n\nSteps.\n",
	}
	for name, body := range files(inside) {
		tree["harness-real/"+name] = body
	}
	dir := writeTree(t, tree)
	harness := filepath.Join(dir, "harness")
	if err := os.Symlink("harness-real", harness); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	for name, body := range files(outside) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(out, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	point := func(target string) {
		if err := os.Remove(harness); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, harness); err != nil {
			t.Fatal(err)
		}
	}
	compose.SetManifestHooks(t, nil, func(string) { point(out) })
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Copies: everything})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	point("harness-real")
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err := render.BuildAt(res, stage, stage, rt); err != nil {
		t.Fatal(err)
	}
	holding := map[string][]string{}
	_ = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		data, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(stage, path)
		for _, mark := range []string{inside, outside} {
			if strings.Contains(string(data), mark) {
				holding[mark] = append(holding[mark], filepath.ToSlash(rel))
			}
		}
		return nil
	})
	for _, f := range holding[outside] {
		t.Errorf("%s holds the outside directory's marker", f)
	}
	for _, want := range []string{"AGENTS.md", "claude/CLAUDE.md", "claude/launch/CLAUDE.md", "claude/mcp.json", "claude/settings.json"} {
		if !slices.Contains(holding[inside], want) {
			t.Errorf("%s does not hold the module's value; files with it: %v", want, holding[inside])
		}
	}
}

// TestAListedFileTheRootLacksFailsTheCompose is a copied module whose ./harness leads,
// after the manifest read, to a directory with an MCP server the module does not ship:
// the module read lists the server by path, its root has no such file, and the compose
// fails.
func TestAListedFileTheRootLacksFailsTheCompose(t *testing.T) {
	isolateAll(t)
	dir := writeTree(t, map[string]string{
		stack.FileName:                  "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: app\n    source:\n      path: ./harness\n",
		"harness-real/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: app\n",
	})
	harness := filepath.Join(dir, "harness")
	if err := os.Symlink("harness-real", harness); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "mcp", "probe.json"), []byte(`{"command":"probe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	compose.SetManifestHooks(t, nil, func(string) {
		if err := os.Remove(harness); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, harness); err != nil {
			t.Fatal(err)
		}
	})
	p, err := loadFor(filepath.Join(dir, stack.FileName), "claude")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(p, compose.Options{Copies: everything})
	if err == nil {
		res.Close()
		t.Fatal("the compose took an MCP server the module's root does not hold")
	}
	t.Logf("compose: %v", err)
}
