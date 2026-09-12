package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// columns splits a printed row into its columns, which two or more spaces separate.
var columns = regexp.MustCompile(`\s{2,}`)

// publisherConfig is the qory.yaml of a harness repository that exports its stack and
// its modules by name, all under harness/, with one module more than the stack uses.
const publisherConfig = `apiVersion: qory.ai/v1alpha1
exports:
  dir: harness
  stacks: [nextjs-15]
  modules: [core, tools, extra]
`

// publisherTree writes the harness repository's tree into dir: the qory.yaml with its
// exports, the base stack under harness/stacks/nextjs-15, the modules under
// harness/modules, with a skill in core and one in extra.
func publisherTree(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "qory.yaml"), publisherConfig)
	writeFile(t, filepath.Join(dir, "harness", "stacks", "nextjs-15", "qory-stack.yaml"), baseStack)
	writeManifest(t, filepath.Join(dir, "harness", "modules", "core"), "core")
	writeFile(t, filepath.Join(dir, "harness", "modules", "core", "skills", "review", "SKILL.md"), "# review\n")
	writeFile(t, filepath.Join(dir, "harness", "modules", "core", "AGENTS.md"), "# Core rules\n")
	writeManifest(t, filepath.Join(dir, "harness", "modules", "tools"), "tools")
	writeFile(t, filepath.Join(dir, "harness", "modules", "tools", "scripts", "check.sh"), "#!/bin/sh\n")
	writeManifest(t, filepath.Join(dir, "harness", "modules", "extra"), "extra")
	writeFile(t, filepath.Join(dir, "harness", "modules", "extra", "skills", "extra", "SKILL.md"), "# extra\n")
}

// publisherRepo makes the harness repository as a git remote and returns its file URL.
func publisherRepo(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Operator")
	runGit(t, dir, "config", "user.email", "op@example.com")
	publisherTree(t, dir)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "harness")
	runGit(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	return "file://" + dir
}

// exportsCompose is the customer's qory.yaml: the base named as an export of the
// publisher's repository, at repo as a git URL or a path, the customer's own module and
// one more of the publisher's, named as an export too.
func exportsCompose(repo, stackName string) string {
	source := "{path: " + repo
	if strings.HasPrefix(repo, "file://") {
		source = "{git: " + repo + ", ref: main"
	}
	return "apiVersion: qory.ai/v1alpha1\nharness:\n  extends: " + source + ", stack: " + stackName + "}\n  modules:\n    - name: app\n    - name: extra\n      source: " + source + ", module: extra}\n"
}

// TestExtendsAnExportedStackByName is the customer's checkout naming the stack and a
// module by their export names: the base's modules resolve where the publisher's
// qory.yaml says, the report records the exports as written and the base's modules by
// the directory they resolved to, and the extra module's skill is composed.
func TestExtendsAnExportedStackByName(t *testing.T) {
	url := publisherRepo(t)
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), exportsCompose(url, "nextjs-15"))
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "claude opus")
	rep := readReport(t, root)
	if rep.Base == nil || rep.Base.Name != "nextjs-15" || rep.Base.Source != url+"#main stack nextjs-15" || len(rep.Base.Pin) != 12 {
		t.Fatalf("base: %+v", rep.Base)
	}
	if len(rep.Modules) != 4 {
		t.Fatalf("modules: %+v", rep.Modules)
	}
	for i, want := range []struct{ name, source string }{
		{"core", url + "#main:harness/modules/core"},
		{"tools", url + "#main:harness/modules/tools"},
		{"app", "modules/app"},
		{"extra", url + "#main module extra"},
	} {
		if m := rep.Modules[i]; m.Name != want.name || m.Source != want.source {
			t.Errorf("modules[%d] = %s from %s, want %s from %s", i, m.Name, m.Source, want.name, want.source)
		}
		if m := rep.Modules[i]; m.Base != (i < 2) {
			t.Errorf("modules[%d] base = %v", i, m.Base)
		}
	}
	if rep.Modules[3].Pin != rep.Base.Pin {
		t.Errorf("extra is pinned to %s, the base to %s", rep.Modules[3].Pin, rep.Base.Pin)
	}
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	entries := entryTable(out)
	if entries["skills/review"] != "core" || entries["skills/deploy"] != "app" || entries["skills/extra"] != "extra" {
		t.Errorf("entries: %v", entries)
	}
}

// TestExtendsAnExportedStackByPath is the same customer with the publisher's checkout
// on disk beside its own: the base and the extra module resolve through the exports of
// that repository, and the base's modules are recorded by a relative path.
func TestExtendsAnExportedStackByPath(t *testing.T) {
	root := newCheckout(t)
	harness := filepath.Join(filepath.Dir(root), "harness")
	publisherTree(t, harness)
	runGit(t, harness, "init", "-q")
	writeFile(t, filepath.Join(root, "qory.yaml"), exportsCompose("../harness", "nextjs-15"))
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, root)
	if rep.Base == nil || rep.Base.Source != "../harness stack nextjs-15" || rep.Base.Pin != "working-tree" {
		t.Fatalf("base: %+v", rep.Base)
	}
	if rep.Modules[0].Source != "../harness/harness/modules/core" || rep.Modules[3].Source != "../harness module extra" {
		t.Errorf("modules: %s, %s", rep.Modules[0].Source, rep.Modules[3].Source)
	}
}

// TestAnExportNotListedIsRefused names a stack the publisher does not export, and a
// module it keeps to itself: each message names the repository and what it exports.
func TestAnExportNotListedIsRefused(t *testing.T) {
	url := publisherRepo(t)
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), exportsCompose(url, "other"))
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	_, err := run(t, "harness", "compose")
	if want := "extends " + url + "#main stack other: " + url + " at main exports no stack named other; stacks: nextjs-15"; err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a stack not exported: err = %v, exit %d; want %q", err, cmd.ExitCode(err), want)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), strings.Replace(exportsCompose(url, "nextjs-15"), "module: extra", "module: secret", 1))
	_, err = run(t, "harness", "compose")
	if want := "module extra: " + url + " at main exports no module named secret; modules: core, tools, extra"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a module not exported: err = %v; want %q", err, want)
	}
}

// TestConfigPrintsThePublishersExports is qory config in the harness repository: the
// exports are rows with the file as their origin, and a listed module with no
// manifest behind it is refused naming the directory.
func TestConfigPrintsThePublishersExports(t *testing.T) {
	root := newCheckout(t)
	publisherTree(t, root)
	out, err := run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := columns.Split(strings.TrimSpace(line), -1); len(f) == 3 {
			rows[f[0]] = f[1] + " | " + f[2]
		}
	}
	for key, want := range map[string]string{
		"exports.dir.stacks":  "harness/stacks | qory.yaml",
		"exports.dir.modules": "harness/modules | qory.yaml",
		"exports.stacks":      "nextjs-15 | qory.yaml",
		"exports.modules":     "core, tools, extra | qory.yaml",
	} {
		if rows[key] != want {
			t.Errorf("%s = %q, want %q:\n%s", key, rows[key], want, out)
		}
	}
	if err := os.RemoveAll(filepath.Join(root, "harness", "modules", "extra")); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "config")
	if want := "qory.yaml: exports.modules names extra, and harness/modules/extra holds no qory-module.yaml"; err == nil || !strings.Contains(err.Error(), want) || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a stale list: err = %v, exit %d; want %q", err, cmd.ExitCode(err), want)
	}
}
