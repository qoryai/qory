package cmd_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// versionRepository is a run from a control plane, as [newHostFetch] makes it, whose
// server serves acme/site as well, on its default branch main: a first commit whose
// qory.yaml lists the module hello, with the command hello, and declares the workflow
// hello write, then a head commit that removes the command and the workflow. A run from a
// run code that may write needs a wall, so forager.yaml has the fake docker's. It returns
// the run, both commits and the files of the first.
func versionRepository(t *testing.T) (h *hostFetch, first, head string, files map[string]string) {
	t.Helper()
	hello := helloFiles(t)
	h = newHostFetch(t, "", "main")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n"
	files = map[string]string{
		"qory.yaml":                       ownStack(stack + "workflows:\n  hello:\n    access: write\n"),
		"modules/hello/qory-module.yaml":  hello["examples/hello/modules/hello/qory-module.yaml"],
		"modules/hello/commands/hello.md": hello["examples/hello/modules/hello/commands/hello.md"],
	}
	first = h.server.Repository(t, "acme/site", "main", files)
	work := tempDir(t)
	proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(h.server.Root, "acme", "site.git"), ".")
	proxygittest.Git(t, work, "rm", "--quiet", "modules/hello/commands/hello.md")
	writeFile(t, filepath.Join(work, "qory.yaml"), ownStack(stack))
	proxygittest.Git(t, work, "add", "qory.yaml")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "head")
	proxygittest.Git(t, work, "push", "--quiet", "origin", "main")
	return h, first, proxygittest.Git(t, work, "rev-parse", "HEAD"), files
}

// runAtVersion runs qory run from a run code whose documents set the workflow hello, the
// target acme/site on git.test, target_version version and the read targets reads, a
// comma-separated list of paths on git.test, "" for none. The run is opened nowhere on
// git.test, through h's host proxy, and the run configuration lists [decoyRepositories],
// which the run does not read. It returns the output,
// the error, what the run came to and the arguments of the session that joined. The run
// has a home outside the checkout, as a walled run of [versionRepository] needs. An empty
// version leaves target_version out and adds a harness document, as the schema of the
// documents requires.
func runAtVersion(t *testing.T, h *hostFetch, reads, version string) (string, error, keyRun, []string) {
	t.Helper()
	return runAtVersionOn(t, h, proxygittest.Host, reads, version)
}

// runAtVersionOn is [runAtVersion] with the run opened on system, "" for none.
func runAtVersionOn(t *testing.T, h *hostFetch, system, reads, version string) (string, error, keyRun, []string) {
	t.Helper()
	target := map[string]any{"collection": "R_1001", "address": map[string]any{"system": proxygittest.Host, "path": "acme/site"}}
	return runAtTarget(t, h, system, reads, version, target)
}

// runAtTarget is [runAtVersionOn] with the documents' target, target.
func runAtTarget(t *testing.T, h *hostFetch, system, reads, version string, target map[string]any) (string, error, keyRun, []string) {
	t.Helper()
	return runAtTargetWith(t, h, system, reads, version, target, nil)
}

// decoyRepositories is the run configuration's QORY_RUN_REPOSITORIES of a run from a run
// code in these tests: a repository that the server does not have, so a fetch of it fails
// the test. The run fetches its documents' target and read targets.
const decoyRepositories = "acme/decoy"

// readTargets is the documents' read_targets of reads, a comma-separated list of paths on
// git.test, each with the collection R_2000, R_2001 and on: none for "".
func readTargets(reads string) []any {
	list := []any{}
	if reads == "" {
		return list
	}
	for i, path := range strings.Split(reads, ",") {
		list = append(list, map[string]any{"collection": fmt.Sprintf("R_2%03d", i), "address": map[string]any{"system": proxygittest.Host, "path": path}})
	}
	return list
}

// runAtTargetWith is [runAtTarget] with change applied to the documents last.
func runAtTargetWith(t *testing.T, h *hostFetch, system, reads, version string, target map[string]any, change func(map[string]any)) (string, error, keyRun, []string) {
	t.Helper()
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["read_targets"] = readTargets(reads)
		d["target_version"] = version
		if version == "" {
			delete(d, "target_version")
			d["harness"] = map[string]any{"harness": map[string]any{"extends": map[string]any{"git": "https://" + proxygittest.Host + "/acme/harness.git", "ref": "main", "path": "stack"}}}
		}
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
		if change != nil {
			change(d)
		}
	})
	var args []string
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { args = s.Args }))
	t.Setenv("QORY_RUN_KEY", "")
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: system, Repositories: decoyRepositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
	})
	defer restore()
	out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL, "--home", filepath.Join(tempDir(t), "home"))
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, args
}

// TestARunCodeWorksAtItsTargetVersion is a run from a run code whose documents set a
// commit of the run's repository that is not its default branch's head: qory run
// fetches the repository at that commit, prints it in the fetch line, and composes the
// qory.yaml of that commit, whose command hello the workflow starts. The head, which
// has no command hello, is not composed.
func TestARunCodeWorksAtItsTargetVersion(t *testing.T) {
	h, first, head, _ := versionRepository(t)
	out, err, n, args := runAtVersion(t, h, "", first)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: fetched acme/site at "+first[:12]+" in ", "qory run: composed ")
	lacks(t, out, head[:12])
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("the working folder is at %s, want %s", got, first)
	}
	if i := slices.Index(args, "-p"); i < 0 || i+1 >= len(args) || !strings.Contains(args[i+1], "hello ") {
		t.Errorf("the session does not start the workflow hello: %q", args)
	}
	h.noPassword(t, out)
}

// TestARunCodeFetchesIntoAnEmptyFolder is a run from a run code in a folder with no
// entries, as a pool and a machine start script give it: qory run runs git init there,
// with no template, before it adds the remote, fetches the target's repository at its
// version, and the session joins.
func TestARunCodeFetchesIntoAnEmptyFolder(t *testing.T) {
	h, first, _, _ := versionRepository(t)
	if err := os.RemoveAll(filepath.Join(h.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	out, err, n, _ := runAtVersion(t, h, "", first)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: fetched acme/site at "+first[:12]+" in ", "qory run: composed ")
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("the working folder is at %s, want %s", got, first)
	}
	data, readErr := os.ReadFile(h.argv)
	if readErr != nil {
		t.Fatal(readErr)
	}
	argv := string(data)
	init, remote := strings.Index(argv, "init --quiet --template= --initial-branch=main\n"), strings.Index(argv, "remote add -- origin ")
	if init < 0 || remote < init {
		t.Errorf("git init at %d, git remote add at %d, want init first:\n%s", init, remote, argv)
	}
	h.noPassword(t, out)
}

// TestARunCodeInAFolderWithAFileStops is a run from a run code in a folder that has a file
// and is not a git working tree: the run stops before the join with exit 1, and git init
// does not run.
func TestARunCodeInAFolderWithAFileStops(t *testing.T) {
	h, first, _, _ := versionRepository(t)
	if err := os.RemoveAll(filepath.Join(h.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(h.dir, "notes.txt"), "notes\n")
	out, err, n, _ := runAtVersion(t, h, "", first)
	if cmd.ExitCode(err) != 1 || n.joined {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out+err.Error(), "qory run: start the run in an empty folder outside any git repository, or at the top of a git working tree without commits: the working folder is neither, and qory fetches the run's repository only into one of these")
	if _, err := os.Lstat(filepath.Join(h.dir, ".git")); !os.IsNotExist(err) {
		t.Errorf("the folder has a .git after the stop: %v", err)
	}
}

// TestARunCodeAtItsBranchHeadComposesTheHead is the run of
// [TestARunCodeWorksAtItsTargetVersion] at the head commit: qory run composes the head's
// qory.yaml, which has no command hello, so the run stops before the join, exit 1, and
// closes failed with the reason workflow_not_in_harness. The line shows the checkout's
// harness in the target's repository.
func TestARunCodeAtItsBranchHeadComposesTheHead(t *testing.T) {
	h, _, head, _ := versionRepository(t)
	out, err, n, _ := runAtVersion(t, h, "", head)
	if cmd.ExitCode(err) != 1 || n.joined {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	if want := []session.Outcome{{State: "failed", Reason: "workflow_not_in_harness", ExitCode: -1}}; !slices.Equal(n.closes, want) {
		t.Errorf("the run closed with %v, want %v", n.closes, want)
	}
	wants(t, out, "qory run: fetched acme/site at "+head[:12]+" in ")
	wants(t, out+err.Error(), "add a skill workflow-hello to the checkout's harness in the repository acme/site, or set another workflow in Qory Apiary: the composed harness has no skill or command workflow-hello or hello (workflow_not_in_harness); the code is used")
}

// TestARunCodeAtACommitTheServerLacksStops is a run from a run code whose documents set
// a commit the run's repository does not have: the run stops before the compose, exit
// 1, with the line that starts with taking the work item in again, and no password.
func TestARunCodeAtACommitTheServerLacksStops(t *testing.T) {
	h, _, _, _ := versionRepository(t)
	out, err, n, _ := runAtVersion(t, h, "", "0123456789abcdef0123456789abcdef01234567")
	if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "\nqory run: take the work item in again in Qory Apiary: the commit 0123456789ab that the run's documents set is not on acme/site on git.test. The fetch through the run's gateway failed: ", "; the code is used\n")
	lacks(t, out, "qory run: composed ")
	h.noPassword(t, out)
}

// TestARunCodeWhoseFetchFailsSendsThePersonToANewTakeIn is a run from a run code whose
// further repository is not on the server, at a commit of the target and with no
// target_version: the run stops before the compose, exit 1, and the fix of the whole line is
// a new take-in, since the code is used.
func TestARunCodeWhoseFetchFailsSendsThePersonToANewTakeIn(t *testing.T) {
	const head = "\nqory run: add a credential for git.test that reads acme/missing to the run configuration, then take the work item in again in Qory Apiary: the fetch of acme/missing through the run's gateway failed: "
	for name, version := range map[string]func(first string) string{
		"at a commit": func(first string) string { return first },
		"no version":  func(string) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			h, first, _, _ := versionRepository(t)
			out, err, n, _ := runAtVersion(t, h, "acme/missing", version(first))
			if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out, head)
			line := out[strings.Index(out, head)+1:]
			line = line[:strings.Index(line, "\n")]
			if !strings.HasSuffix(line, "; the code is used") || strings.Contains(line, "run again") {
				t.Errorf("the line is %q", line)
			}
			lacks(t, out, "qory run: composed ")
			h.noPassword(t, out)
		})
	}
}

// TestARunCodeAtATargetVersionWithNoRepositoryRunsOn is a run from a run code whose
// documents set a target_version while the run configuration sets no QORY_RUN_SYSTEM and
// lists no repository, as for a target in another system: qory run fetches nothing,
// uses no version, and composes the qory.yaml in the working folder.
func TestARunCodeAtATargetVersionWithNoRepositoryRunsOn(t *testing.T) {
	h, first, _, files := versionRepository(t)
	for name, content := range files {
		writeFile(t, filepath.Join(h.dir, filepath.FromSlash(name)), content)
	}
	out, err, n, args := runAtVersionOn(t, h, "", "", first)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: composed ")
	lacks(t, out, "qory run: fetched ", first[:12])
	if i := slices.Index(args, "-p"); i < 0 || i+1 >= len(args) || !strings.Contains(args[i+1], "hello ") {
		t.Errorf("the session does not start the workflow hello: %q", args)
	}
}

// TestARunCodeWhoseTargetHasNoAddressUsesNoVersion is a run from a run code whose
// documents set a target with a collection and no address, and a target_version, the
// first commit of acme/site:
//   - with no QORY_RUN_SYSTEM and no repository, as for a place that is not on a host,
//     qory run fetches nothing and composes the qory.yaml in the working folder;
//   - with QORY_RUN_SYSTEM git.test and the run configuration's repository acme/site, whose
//     path is also the collection's ref, qory run fetches nothing too: a run from a run code
//     fetches its documents' target and read targets alone.
func TestARunCodeWhoseTargetHasNoAddressUsesNoVersion(t *testing.T) {
	t.Run("no repositories", func(t *testing.T) {
		h, first, _, files := versionRepository(t)
		for name, content := range files {
			writeFile(t, filepath.Join(h.dir, filepath.FromSlash(name)), content)
		}
		out, err, n, args := runAtTarget(t, h, "", "", first, map[string]any{"collection": "profile-7"})
		if !n.joined {
			t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
		}
		wants(t, out, "qory run: composed ")
		lacks(t, out, "qory run: fetched ", first[:12])
		if i := slices.Index(args, "-p"); i < 0 || i+1 >= len(args) || !strings.Contains(args[i+1], "hello ") {
			t.Errorf("the session does not start the workflow hello: %q", args)
		}
		data, _ := os.ReadFile(h.argv)
		for line := range strings.Lines(string(data)) {
			if slices.Contains(strings.Fields(line), "fetch") {
				t.Errorf("git ran a fetch: %s", line)
			}
		}
	})
	t.Run("the run configuration's repository is not fetched", func(t *testing.T) {
		h, first, _, files := versionRepository(t)
		for name, content := range files {
			writeFile(t, filepath.Join(h.dir, filepath.FromSlash(name)), content)
		}
		g := newCodeRelay(t)
		g.documents = codeDocuments(t, func(d map[string]any) {
			target := map[string]any{"collection": "acme/site"}
			d["target"], d["inputs"].(map[string]any)["target"] = target, target
			d["target_version"], d["harness_hidden"] = first, false
			d["run_settings"] = map[string]any{"runtime": "claude"}
		})
		t.Setenv("QORY_RUN_KEY", "")
		closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
			System: proxygittest.Host, Repositories: "acme/site",
			Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
			StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		})
		defer restore()
		out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL, "--home", filepath.Join(tempDir(t), "home"))
		closes()
		if !errors.Is(err, cmd.ErrJoinedNowhere) {
			t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
		}
		wants(t, out, "qory run: composed ")
		lacks(t, out, "qory run: fetched ", first[:12])
		if n := h.server.Tunnels(); n != 0 {
			t.Errorf("the host proxy led %d connections", n)
		}
	})
}

// TestARunCodeWithReadTargetsAndNoTargetFetchesThemReadOnly is a run from a run code whose
// documents set a target with no address, the read target acme/site and a harness
// document: qory run fetches acme/site under .qory/repositories, read-only, leaves the
// working folder an empty repository, and composes the harness document.
func TestARunCodeWithReadTargetsAndNoTargetFetchesThemReadOnly(t *testing.T) {
	files := harnessFiles(t, "write")
	h, first, _, _ := versionRepository(t)
	out, err, n, _ := runAtTargetWith(t, h, proxygittest.Host, "acme/site", first, map[string]any{"collection": "profile-7"}, func(d map[string]any) {
		d["harness"] = servedHarness(t, h, files)
	})
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: fetched 1 read-only repository in ", "qory run: composed ")
	lacks(t, out, "qory run: fetched acme/site")
	if got := gitOut(t, filepath.Join(h.dir, ".qory", "repositories", "acme", "site"), "rev-parse", "HEAD"); got == "" {
		t.Error("the read target acme/site is not fetched")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "qory.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the working folder has a qory.yaml: %v", err)
	}
}

// harnessFiles is the files of examples/hello, whose workflow hello declares access. It
// reads them from the repository, so a test calls it before it changes the working folder.
func harnessFiles(t *testing.T, access string) map[string]string {
	t.Helper()
	files := helloFiles(t)
	files["examples/hello/qory-stack.yaml"] = declares(access)(withExtendable(files["examples/hello/qory-stack.yaml"]))
	return files
}

// servedHarness serves acme/stack with files, from [harnessFiles], on h's server, and
// returns a harness document that extends its stack examples/hello, as the documents of a
// run whose workspace has a harness contain it.
func servedHarness(t *testing.T, h *hostFetch, files map[string]string) map[string]any {
	t.Helper()
	commit := h.server.Repository(t, "acme/stack", "main", files)
	return map[string]any{"harness": map[string]any{"extends": map[string]any{"git": h.server.URL("acme/stack"), "ref": commit, "path": "examples/hello"}}}
}

// siteTarget is the target acme/site on git.test, and siteDestinations the inputs'
// destinations with that target, as an Explore run's inputs list them.
var (
	siteTarget       = map[string]any{"collection": "R_1001", "address": map[string]any{"system": proxygittest.Host, "path": "acme/site"}}
	siteDestinations = []any{map[string]any{"id": "tg_s000000000000000", "target": siteTarget}}
)

// TestARunCodeWhoseInputsListDestinationsFetchesNothing is a read run from a run code on
// git.test at a commit of acme/site, its target, whose documents contain a harness
// document and whose inputs list destinations, as an Explore run's do: qory run fetches
// nothing through the host proxy and reads no branch before the session, composes the
// harness document's stack, fetched through the stack proxy, and joins, with a read
// target acme/shop or none. The agent clones what it needs with
// qory destination clone.
func TestARunCodeWhoseInputsListDestinationsFetchesNothing(t *testing.T) {
	for name, reads := range map[string]string{"a read target": "acme/shop", "no read target": ""} {
		t.Run(name, func(t *testing.T) {
			files := harnessFiles(t, "read")
			h, first, _, _ := versionRepository(t)
			harness := servedHarness(t, h, files)
			out, err, n, _ := runAtTargetWith(t, h, proxygittest.Host, reads, first, siteTarget, func(d map[string]any) {
				d["inputs"].(map[string]any)["destinations"] = siteDestinations
				d["access"] = "read"
				d["harness"] = harness
			})
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out, "qory run: composed ")
			lacks(t, out, "qory run: fetched acme/site", first[:12])
			if n := h.server.Tunnels(); n != 0 {
				t.Errorf("the host proxy led %d connections", n)
			}
			if h.server.StackTunnels() == 0 {
				t.Error("the stack proxy led no connection, want the harness's fetch")
			}
			if _, err := os.Lstat(filepath.Join(h.dir, "qory.yaml")); !os.IsNotExist(err) {
				t.Errorf("the working folder has a qory.yaml: %v", err)
			}
		})
	}
}

// TestARunCodeFetchesItsTargetUnlessItClonesDestinations is a run from a run code on
// git.test at a commit of acme/site, its target, whose documents list the read target
// acme/shop. Each fetches the target at the commit before the session and joins:
//   - a read run whose inputs list destinations and whose documents contain no harness
//     document composes the qory.yaml of the fetched commit, and fetches acme/site alone:
//     the agent clones any other destination it needs;
//   - a run that may write, whose inputs list destinations and whose documents contain a
//     harness document, fetches acme/shop too;
//   - a run whose inputs list no destinations, as a Build run's, fetches acme/shop too.
//
// In each, git reaches no remote repository other than these and the harness's.
func TestARunCodeFetchesItsTargetUnlessItClonesDestinations(t *testing.T) {
	for _, c := range []struct {
		name, access                 string
		destinations, harness, alone bool
	}{
		{"read, no harness document", "read", true, false, true},
		{"write, harness document", "write", true, true, false},
		{"no destinations", "write", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := harnessFiles(t, c.access)
			h, first, _, _ := versionRepository(t)
			out, err, n, _ := runAtTargetWith(t, h, proxygittest.Host, "acme/shop", first, siteTarget, func(d map[string]any) {
				if c.destinations {
					d["inputs"].(map[string]any)["destinations"] = siteDestinations
				}
				if c.harness {
					d["harness"] = servedHarness(t, h, files)
				}
				d["access"] = c.access
			})
			if !n.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			fetched := "qory run: fetched acme/site at " + first[:12] + " and 1 further repository in "
			if c.alone {
				fetched = "qory run: fetched acme/site at " + first[:12] + " in "
			}
			wants(t, out, fetched, "qory run: composed ")
			if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != first {
				t.Errorf("the working folder is at %s, want %s", got, first)
			}
			data, _ := os.ReadFile(h.argv)
			shop := false
			for line := range strings.Lines(string(data)) {
				if !strings.Contains(line, "://") {
					continue
				}
				shop = shop || strings.Contains(line, "/acme/shop")
				if !strings.Contains(line, "/acme/site") && !strings.Contains(line, "/acme/shop") && !strings.Contains(line, "/acme/stack") {
					t.Errorf("git reached another repository: %s", line)
				}
			}
			if shop == c.alone {
				t.Errorf("git reached acme/shop: %v, want %v\n%s", shop, !c.alone, out)
			}
			if c.alone && strings.Contains(out, "acme/shop") {
				t.Errorf("the run's lines show acme/shop:\n%s", out)
			}
		})
	}
}
