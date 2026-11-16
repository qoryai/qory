package cmd_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/report"
)

// branchStack is the stack of the qory.yaml of [branchRepository], with the command
// limit of the extension software-factory given and the lines of more modules after the
// module hello.
func branchStack(limit, modules string) string {
	return ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n" + modules +
		"extensions:\n  software-factory:\n    command_limits_minutes: {build: " + limit + "}\nworkflows:\n  hello:\n    access: write\n")
}

// branchMarker is a line that a commit on the work item's branch writes into a file of
// the harness, and that no file of the target version contains.
const branchMarker = "This line is from the work item's branch."

// branchRepository is a run from a control plane, as [newHostFetch] makes it, whose
// server serves acme/site: a first commit on main, the target version, whose qory.yaml
// lists the module hello by its path modules/hello, sets the command limit 10 of the
// extension software-factory and declares the workflow hello write, then a commit that
// [change] makes on top of it, pushed to the work item's branch qory/<workItem> alone.
// hello is [helloFiles], read before the first run moves the working directory. merge
// pushes that commit to main, as a merge of the branch into the target's default branch
// does.
func branchRepository(t *testing.T, hello map[string]string, change func(work string)) (h *hostFetch, first, changed string, merge func()) {
	t.Helper()
	h = newHostFetch(t, "", "main")
	first = h.server.Repository(t, "acme/site", "main", map[string]string{
		"qory.yaml":                       branchStack("10", ""),
		"modules/hello/qory-module.yaml":  hello["examples/hello/modules/hello/qory-module.yaml"],
		"modules/hello/commands/hello.md": hello["examples/hello/modules/hello/commands/hello.md"],
	})
	work := tempDir(t)
	proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(h.server.Root, "acme", "site.git"), ".")
	change(work)
	proxygittest.Git(t, work, "add", "--all")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "work on the branch")
	proxygittest.Git(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	changed = proxygittest.Git(t, work, "rev-parse", "HEAD")
	return h, first, changed, func() { proxygittest.Git(t, work, "push", "--quiet", "origin", "HEAD:main") }
}

// branchChanges are the changes of a commit on the work item's branch to files that a
// compose reads: the modules and the extension's command limit in qory.yaml, and a file
// of a module that qory.yaml lists by a path.
var branchChanges = map[string]func(t *testing.T, work string){
	"the modules in qory.yaml": func(t *testing.T, work string) {
		writeFile(t, filepath.Join(work, "qory.yaml"), branchStack("90", "  - name: extra\n    source: {path: modules/extra}\n"))
		writeFile(t, filepath.Join(work, "modules", "extra", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: extra\n")
		writeFile(t, filepath.Join(work, "modules", "extra", "commands", "extra.md"), branchMarker+"\n")
	},
	"a file of a path module": func(t *testing.T, work string) {
		writeFile(t, filepath.Join(work, "modules", "hello", "commands", "hello.md"), branchMarker+"\n")
	},
}

// composed is the compose of the run in the checkout at root, as a run from a run code
// writes it under the user's cache directory: the report, and every file of the home, which
// the test keeps with [cmd.KeepRunHomes], with <home> and <checkout> in place of the paths
// of the two folders.
func composed(t *testing.T, root string) (report.Report, map[string]string) {
	t.Helper()
	home := runKeyRunHome(t, root)
	tree := folderTree(t, home)
	for path, content := range tree {
		tree[path] = strings.NewReplacer(home, "<home>", root, "<checkout>").Replace(content)
	}
	return runKeyReport(t, root), tree
}

// sameCompose fails the test when the report or the home of got is not the one of want:
// the modules with their sources, the entries, the extensions and every file of the
// home.
func sameCompose(t *testing.T, gotReport, wantReport report.Report, got, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(gotReport.Modules, wantReport.Modules) {
		t.Errorf("modules %+v, want %+v", gotReport.Modules, wantReport.Modules)
	}
	if !reflect.DeepEqual(gotReport.Entries, wantReport.Entries) {
		t.Errorf("entries %+v, want %+v", gotReport.Entries, wantReport.Entries)
	}
	if !reflect.DeepEqual(gotReport.Extensions, wantReport.Extensions) {
		t.Errorf("extensions %v, want %v", gotReport.Extensions, wantReport.Extensions)
	}
	sameTree(t, "the home at the target version", want, got)
}

// commandLimit is the command limit build of the extension software-factory in rep.
func commandLimit(rep report.Report) any {
	factory, _ := rep.Extensions["software-factory"].(map[string]any)
	limits, _ := factory["command_limits_minutes"].(map[string]any)
	return limits["build"]
}

// TestABranchCommitDoesNotChangeTheHarnessOfTheNextRun is a run of a work item whose
// branch version has a commit that changes what the compose reads: the run composes the
// harness at target_version, so the report and the home are the ones of a run at
// target_version whose branch has no version yet, and the working folder is on the
// branch's commit.
func TestABranchCommitDoesNotChangeTheHarnessOfTheNextRun(t *testing.T) {
	// The test reads the run's home once the run is over.
	t.Cleanup(cmd.KeepRunHomes())
	hello := helloFiles(t)
	for name, change := range branchChanges {
		t.Run(name, func(t *testing.T) {
			h, first, _, _ := branchRepository(t, hello, func(work string) { change(t, work) })
			out, err, n, _ := runOnBranch(t, h, first, branchAPI(t, ""))
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			wantReport, want := composed(t, h.dir)
			if got := commandLimit(wantReport); got != float64(10) {
				t.Fatalf("the target version's command limit is %v, want 10", got)
			}
			if !slices.ContainsFunc(wantReport.Entries, func(e report.Entry) bool { return e.Kind == "commands" && e.Name == "hello" }) {
				t.Fatalf("the target version composes no command hello: %+v", wantReport.Entries)
			}

			h, first, changed, _ := branchRepository(t, hello, func(work string) { change(t, work) })
			out, err, n, _ = runOnBranch(t, h, first, branchAPI(t, branchDocument(t, changed)))
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out, "qory run: fetched acme/site at "+first[:12]+" in ", "qory run: on branch qory/"+workItem+" at "+changed[:12])
			if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != changed {
				t.Errorf("the working folder is at %s, want %s", got, changed)
			}
			gotReport, got := composed(t, h.dir)
			sameCompose(t, gotReport, wantReport, got, want)
			for path, content := range got {
				if strings.Contains(content, branchMarker) {
					t.Errorf("the home has the branch's file %s", path)
				}
			}
		})
	}
}

// TestABranchCommitReachesTheHarnessOnceMerged is the next run of the work item once its
// branch is merged into the target's default branch: Qory Apiary sends a target_version that
// contains the branch's commit, and the run composes it.
func TestABranchCommitReachesTheHarnessOnceMerged(t *testing.T) {
	// The test reads the run's home once the run is over.
	t.Cleanup(cmd.KeepRunHomes())
	hello := helloFiles(t)
	for name, change := range branchChanges {
		t.Run(name, func(t *testing.T) {
			h, _, changed, merge := branchRepository(t, hello, func(work string) { change(t, work) })
			merge()
			out, err, n, _ := runOnBranch(t, h, changed, branchAPI(t, branchDocument(t, changed)))
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out, "qory run: fetched acme/site at "+changed[:12]+" in ")
			rep, tree := composed(t, h.dir)
			marked := false
			for _, content := range tree {
				marked = marked || strings.Contains(content, branchMarker)
			}
			if !marked {
				t.Errorf("no file of the home has the branch's line: %v", rep.Entries)
			}
			if name == "the modules in qory.yaml" {
				if len(rep.Modules) != 2 || rep.Modules[1].Name != "extra" {
					t.Errorf("modules %+v, want hello and extra", rep.Modules)
				}
				if got := commandLimit(rep); got != float64(90) {
					t.Errorf("the command limit is %v, want 90", got)
				}
			}
		})
	}
}

// TestABranchCommitThatChangesQoryStopsAfterTheCompose is a run of a work item whose
// branch version has a commit that adds a file under .qory, where qory keeps the files
// of a run: the compose reads the target version, as it does for any branch, and the
// checkout of the branch then stops the run before the agent starts.
func TestABranchCommitThatChangesQoryStopsAfterTheCompose(t *testing.T) {
	// The test reads the run's home once the run is over.
	t.Cleanup(cmd.KeepRunHomes())
	hello := helloFiles(t)
	h, first, _, _ := branchRepository(t, hello, func(work string) {
		writeFile(t, filepath.Join(work, ".qory", "notes.md"), branchMarker+"\n")
	})
	out, err, n, _ := runOnBranch(t, h, first, branchAPI(t, ""))
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	wantReport, want := composed(t, h.dir)

	h, first, changed, _ := branchRepository(t, hello, func(work string) {
		writeFile(t, filepath.Join(work, ".qory", "notes.md"), branchMarker+"\n")
	})
	out, err, n, _ = runOnBranch(t, h, first, branchAPI(t, branchDocument(t, changed)))
	if err == nil || n.joined {
		t.Fatalf("%v (exit %d), want a stop before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out+err.Error(), "qory run: composed ", "commit "+changed[:12]+" of qory/"+workItem+" has another .qory")
	gotReport, got := composed(t, h.dir)
	sameCompose(t, gotReport, wantReport, got, want)
	if _, err := os.Stat(filepath.Join(h.dir, ".qory", "notes.md")); !os.IsNotExist(err) {
		t.Errorf("the branch's .qory/notes.md is checked out: %v", err)
	}
}
