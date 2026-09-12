package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// runnerTree is the operator's stack tree on the runner's disk: [operatorRepo] as a
// directory, and the path of the base stack file in it.
func runnerTree(t *testing.T) (dir, file string) {
	t.Helper()
	dir = strings.TrimPrefix(operatorRepo(t), "file://")
	return dir, filepath.Join(dir, "nextjs-15", "qory-stack.yaml")
}

// fleetCheckout is a customer's repository as the operator wants it committed: a document
// under harness holding only what is the repository's own, its module and its
// extensions, with the lines given before them, and no apiVersion, since the file names
// nothing of the tool that reads it.
func fleetCheckout(t *testing.T, name string, lines ...string) string {
	t.Helper()
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, name), "harness:\n"+strings.Join(lines, "")+"  modules:\n    - name: app\n      source: {path: ./modules/app}\n  extensions:\n    customer: {team: web}\n")
	writeManifest(t, filepath.Join(root, "modules", "app"), "app")
	writeFile(t, filepath.Join(root, "modules", "app", "skills", "deploy", "SKILL.md"), "# deploy\n")
	return root
}

// TestFileFlagNamesTheBaseOfTheCheckoutsDocument is the runner composing a customer's
// checkout from its own stack tree: -f names the base stack, the checkout's document
// composes on it with its module appended and its extensions beside the base's, the
// report records the base by the path -f named, relative to the document, and the rows
// say where the base came from and that the document's own extends was not read.
func TestFileFlagNamesTheBaseOfTheCheckoutsDocument(t *testing.T) {
	_, base := runnerTree(t)
	root := fleetCheckout(t, "qory.yaml", "  extends: {git: https://git.example.com/acme/harness, ref: v9, path: nextjs-15}\n")
	out, err := run(t, "harness", "compose", "-f", base)
	if err != nil {
		t.Fatalf("compose -f: %v\n%s", err, out)
	}
	wants(t, out, "claude opus", "composed")
	wantsRow(t, out, "base", base+"  (named by -f, in place of extends https://git.example.com/acme/harness#v9:nextjs-15)")
	wantsRow(t, out, "skipped", "qory.yaml  (its harness, git and env keys; the base stack decides under extends)")
	rep := readReport(t, root)
	rel, _ := filepath.Rel(root, filepath.Dir(base))
	if rep.Base == nil || rep.Base.Name != "nextjs-15" || rep.Base.Source != rel || rep.Base.Pin != "working-tree" {
		t.Fatalf("base: %+v, want source %q", rep.Base, rel)
	}
	if len(rep.Modules) != 3 || rep.Modules[0].Name != "core" || !rep.Modules[0].Base || rep.Modules[1].Name != "tools" || rep.Modules[2].Name != "app" || rep.Modules[2].Base {
		t.Fatalf("modules: %+v", rep.Modules)
	}
	if rep.Extensions["acme"] == nil || rep.Extensions["customer"] == nil {
		t.Errorf("extensions: %v", rep.Extensions)
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "skills", "deploy")); err != nil {
		t.Errorf("the checkout's own skill is not composed: %v", err)
	}
	// Composed again the same way, the tree is up to date.
	if out, err := run(t, "harness", "compose", "-f", base, "--check"); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
}

// TestDocumentLeavesExtendsOutForTheFlag is the committed file the operator wants: no
// apiVersion, no extends. It composes on the base -f names and is refused, saying what
// to do, when nothing supplies a base.
func TestDocumentLeavesExtendsOutForTheFlag(t *testing.T) {
	_, base := runnerTree(t)
	root := fleetCheckout(t, "qory.yaml")
	out, err := run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("without a base: err = %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, err.Error(), "qory.yaml: harness names no target.runtime and no stack to extend", "leaves extends out for the base that qory harness compose -f <stack> names")
	gone(t, root, ".qory", ".claude")
	out, err = run(t, "harness", "compose", "-f", base)
	if err != nil {
		t.Fatalf("compose -f: %v\n%s", err, out)
	}
	wantsRow(t, out, "base", base+"  (named by -f)")
	rep := readReport(t, root)
	if len(rep.Modules) != 3 || rep.Modules[2].Name != "app" || rep.Extensions["customer"] == nil {
		t.Fatalf("report: modules %+v, extensions %v", rep.Modules, rep.Extensions)
	}
	// A document that appends no module at all, its extensions alone, composes too.
	writeFile(t, filepath.Join(root, "qory.yaml"), "harness:\n  extensions:\n    customer: {team: web}\n")
	if out, err := run(t, "harness", "compose", "-f", base); err != nil {
		t.Fatalf("extensions alone: %v\n%s", err, out)
	}
	rep = readReport(t, root)
	if len(rep.Modules) != 2 || rep.Extensions["customer"] == nil || rep.Extensions["acme"] == nil {
		t.Fatalf("report: modules %+v, extensions %v", rep.Modules, rep.Extensions)
	}
}

// TestFileFlagRefusesADocumentWithItsOwnTarget is -f naming a stack in a checkout whose
// document holds its own stack: nothing is a base for that, and the message says so
// rather than composing either one and dropping the other.
func TestFileFlagRefusesADocumentWithItsOwnTarget(t *testing.T) {
	_, base := runnerTree(t)
	root := fleetCheckout(t, "qory.yaml", "  target: {runtime: claude}\n")
	out, err := run(t, "harness", "compose", "-f", base)
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, err.Error(), "qory.yaml: harness sets a target, this repository's own stack, and -f names a base for a document that extends one")
	gone(t, root, ".qory", ".claude")
}

// TestHarnessYamlIsTheDocumentsOtherName is the committed file under its other name:
// discovery reads it, -f names its base, the rows name it, and a directory holding both
// names is refused.
func TestHarnessYamlIsTheDocumentsOtherName(t *testing.T) {
	tree, base := runnerTree(t)
	root := fleetCheckout(t, "harness.yaml")
	out, err := run(t, "harness", "compose", "-f", base)
	if err != nil {
		t.Fatalf("compose -f: %v\n%s", err, out)
	}
	wantsRow(t, out, "skipped", "harness.yaml  (its harness, git and env keys; the base stack decides under extends)")
	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	rel, _ := filepath.Rel(root, filepath.Join(tree, "nextjs-15"))
	writeFile(t, filepath.Join(root, "harness.yaml"), "harness:\n  extends: {path: "+rel+"}\n  modules:\n    - name: app\n      source: {path: ./modules/app}\n")
	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("discovered: %v\n%s", err, out)
	}
	if rep := readReport(t, root); len(rep.Modules) != 3 || rep.Base == nil || rep.Base.Source != rel {
		t.Fatalf("report: %+v", rep)
	}
	writeFile(t, filepath.Join(root, "harness.yaml"), "harness:\n  nope: 1\n")
	_, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), `harness.yaml: line 2: key "nope" is not one harness.yaml reads`) {
		t.Fatalf("an unknown key: %v", err)
	}
	writeFile(t, filepath.Join(root, "harness.yaml"), "harness:\n  extends: {path: "+rel+"}\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "worktree: {base: main}\n")
	for _, args := range [][]string{{"harness", "compose"}, {"config"}} {
		_, err = run(t, args...)
		if err == nil || !strings.Contains(err.Error(), root+" holds both qory.yaml and harness.yaml; a directory holds one of the two") {
			t.Fatalf("%s with both names: %v", strings.Join(args, " "), err)
		}
	}
}
