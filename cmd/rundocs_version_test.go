package cmd_test

import (
	"errors"
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
// qory.yaml lists the module hello, with the command hello, then a head commit that
// removes the command. It returns the run, both commits and the files of the first.
func versionRepository(t *testing.T) (h *hostFetch, first, head string, files map[string]string) {
	t.Helper()
	hello := helloFiles(t)
	h = newHostFetch(t, "", "main")
	files = map[string]string{
		"qory.yaml":                       ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n"),
		"modules/hello/qory-module.yaml":  hello["examples/hello/modules/hello/qory-module.yaml"],
		"modules/hello/commands/hello.md": hello["examples/hello/modules/hello/commands/hello.md"],
	}
	first = h.server.Repository(t, "acme/site", "main", files)
	work := tempDir(t)
	proxygittest.Git(t, work, "clone", "--quiet", filepath.Join(h.server.Root, "acme", "site.git"), ".")
	proxygittest.Git(t, work, "rm", "--quiet", "modules/hello/commands/hello.md")
	proxygittest.Git(t, work, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "-m", "head")
	proxygittest.Git(t, work, "push", "--quiet", "origin", "main")
	return h, first, proxygittest.Git(t, work, "rev-parse", "HEAD"), files
}

// runAtVersion runs qory run from a run code whose documents set the workflow hello, the
// target acme/site on git.test and target_version version, its run opened nowhere on
// git.test with the repositories given, through h's host proxy, and returns the output,
// the error, what the run came to and the arguments of the session that joined.
func runAtVersion(t *testing.T, h *hostFetch, repositories, version string) (string, error, keyRun, []string) {
	t.Helper()
	return runAtVersionOn(t, h, proxygittest.Host, repositories, version)
}

// runAtVersionOn is [runAtVersion] with the run opened on system, "" for none.
func runAtVersionOn(t *testing.T, h *hostFetch, system, repositories, version string) (string, error, keyRun, []string) {
	t.Helper()
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		target := map[string]any{"system": proxygittest.Host, "path": "acme/site"}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = version
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
	})
	var args []string
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { args = s.Args }))
	t.Setenv("QORY_RUN_KEY", "")
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: system, Repositories: repositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
	})
	defer restore()
	out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, args
}

// TestARunCodeWorksAtItsTargetVersion is a run from a run code whose documents set a
// commit of the run's repository that is not its default branch's head: qory run
// fetches the repository at that commit, prints it in the fetch line, and composes the
// qory.yaml of that commit, whose command hello the workflow starts. The head, which
// has no command hello, is not composed.
func TestARunCodeWorksAtItsTargetVersion(t *testing.T) {
	h, first, head, _ := versionRepository(t)
	out, err, n, args := runAtVersion(t, h, "acme/site", first)
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

// TestARunCodeAtItsBranchHeadComposesTheHead is the run of
// [TestARunCodeWorksAtItsTargetVersion] at the head commit: qory run composes the head's
// qory.yaml, which has no command hello, so the run stops before the join.
func TestARunCodeAtItsBranchHeadComposesTheHead(t *testing.T) {
	h, _, head, _ := versionRepository(t)
	out, err, n, _ := runAtVersion(t, h, "acme/site", head)
	if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: fetched acme/site at "+head[:12]+" in ")
	wants(t, out+err.Error(), "the composed harness has no skill or command hello; the code is used")
}

// TestARunCodeAtACommitTheServerLacksStops is a run from a run code whose documents set
// a commit the run's repository does not have: the run stops before the compose, exit
// 1, with the line that starts with taking the work item in again, and no password.
func TestARunCodeAtACommitTheServerLacksStops(t *testing.T) {
	h, _, _, _ := versionRepository(t)
	out, err, n, _ := runAtVersion(t, h, "acme/site", "0123456789abcdef0123456789abcdef01234567")
	if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "\nqory run: take the work item in again in Qory Apiary: the commit 0123456789ab that the run's documents set is not on acme/site on git.test. The fetch through the run's gateway failed: ", "; the code is used\n")
	lacks(t, out, "qory run: composed ")
	h.noPassword(t, out)
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

// TestARunCodeWhoseTargetIsNotTheFirstRepositoryStops is a run from a run code whose
// documents set the target acme/site on the run configuration's system, at a commit,
// while the run configuration lists acme/site after another repository, lists only
// another, or lists no repository: the run stops before any fetch, exit 1, with the
// line for each.
func TestARunCodeWhoseTargetIsNotTheFirstRepositoryStops(t *testing.T) {
	for _, c := range []struct{ repositories, want string }{
		{"acme/shop,acme/site", "the run's documents set the target acme/site, but the run configuration lists it after another repository; the target must be the first repository of the run configuration; the code is used\n"},
		{"acme/shop", "the run's documents set the target acme/site, but the run configuration does not list it; the target must be the first repository of the run configuration; the code is used\n"},
		{"", "the run's documents set the target acme/site, but the run configuration does not list it; the target must be the first repository of the run configuration; the code is used\n"},
	} {
		t.Run(c.repositories, func(t *testing.T) {
			h, first, _, _ := versionRepository(t)
			out, err, n, _ := runAtVersion(t, h, c.repositories, first)
			if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out, "\nqory run: report this run to the instance admin of Qory Apiary: "+c.want)
			lacks(t, out, "qory run: fetched ", "qory run: composed ")
			if h.server.Tunnels() != 0 {
				t.Errorf("%d fetches went through the host proxy, want none", h.server.Tunnels())
			}
		})
	}
}
