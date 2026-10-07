package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/ui"
)

// TestComposeReadsAModuleFromGit is a product repository that pins its core module to a tag
// of the harness repository: the compose fetches it, pins it by commit, and reads the
// cache from then on.
func TestComposeReadsAModuleFromGit(t *testing.T) {
	root := newCheckout(t)
	// The module repository, with its own identity, tagged v1.
	remote := tempDir(t)
	runGit(t, remote, "init", "--quiet", "--initial-branch=main")
	runGit(t, remote, "config", "user.name", "Tester")
	runGit(t, remote, "config", "user.email", "tester@example.com")
	writeManifest(t, filepath.Join(remote, "modules", "core"), "core")
	writeFile(t, filepath.Join(remote, "modules", "core", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review.\n---\n\nReview it.\n")
	writeFile(t, filepath.Join(remote, "modules", "core", "AGENTS.md"), "# Core\n")
	runGit(t, remote, "add", "-A")
	runGit(t, remote, "commit", "-q", "-m", "first")
	runGit(t, remote, "tag", "v1")
	url := "file://" + remote

	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: "+url+", ref: v1, path: modules/core}\n")
	out, err := run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude", "composed 1 entry from 1 module")
	rep := readReport(t, root)
	if len(rep.Modules) != 1 || rep.Modules[0].Source != url+"#v1:modules/core" {
		t.Fatalf("report modules = %+v", rep.Modules)
	}
	pin := rep.Modules[0].Pin
	if len(pin) != 12 || rep.Modules[0].Dirty {
		t.Errorf("pin %q dirty %v, want a twelve-character commit, clean", pin, rep.Modules[0].Dirty)
	}
	// The home reaches the module through its cache clone.
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "modules", "core", "AGENTS.md")); err != nil {
		t.Errorf("modules/core does not resolve: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(root, ".qory", "harness", "modules", "core")); err != nil || strings.HasPrefix(target, remote) {
		t.Errorf("modules/core links to %q (%v), want the cache clone, not the remote", target, err)
	}

	// inspect prints the pin, and a second compose reads the cache: the remote may go.
	out, err = run(t, "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, url+"#v1:modules/core", pin)
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("second compose without the remote: %v\n%s", err, out)
	}
	if out, err := run(t, "hc", "--update"); err == nil || !strings.Contains(err.Error(), "fetching "+url+"#v1") {
		t.Errorf("--update without the remote: %v\n%s", err, out)
	}
}

// TestComposeFollowsAnEditedRef is the stack moving from one tag to another: the pin of
// the last compose was for the old source, so the new ref resolves on its own.
func TestComposeFollowsAnEditedRef(t *testing.T) {
	root := newCheckout(t)
	remote := tempDir(t)
	runGit(t, remote, "init", "--quiet", "--initial-branch=main")
	runGit(t, remote, "config", "user.name", "Tester")
	runGit(t, remote, "config", "user.email", "tester@example.com")
	writeManifest(t, remote, "core")
	writeFile(t, filepath.Join(remote, "AGENTS.md"), "# v1\n")
	runGit(t, remote, "add", "-A")
	runGit(t, remote, "commit", "-q", "-m", "first")
	runGit(t, remote, "tag", "v1")
	writeFile(t, filepath.Join(remote, "AGENTS.md"), "# v2\n")
	runGit(t, remote, "commit", "-q", "-am", "second")
	runGit(t, remote, "tag", "v2")
	url := "file://" + remote
	stackFor := func(ref string) string {
		return "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: any\nmodules:\n  - name: core\n    source: {git: " + url + ", ref: " + ref + "}\n"
	}
	writeOwnStack(t, root, stackFor("v1"))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := readReport(t, root).Modules[0].Pin
	writeOwnStack(t, root, stackFor("v2"))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep := readReport(t, root); rep.Modules[0].Pin == first || rep.Modules[0].Source != url+"#v2" {
		t.Errorf("after editing the ref: %+v, still pinned to %s", rep.Modules[0], first)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(data) != "# v2\n" {
		t.Errorf("AGENTS.md reads %q after moving to v2", data)
	}
}

// branchRemote is a harness repository on branch main with the core module at its root,
// and returns its directory and file URL.
func branchRemote(t *testing.T) (dir, url string) {
	t.Helper()
	dir = tempDir(t)
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	runGit(t, dir, "config", "user.name", "Tester")
	runGit(t, dir, "config", "user.email", "tester@example.com")
	writeManifest(t, dir, "core")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# v1\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "first")
	return dir, "file://" + dir
}

// onMain is a stack with the core module from url at main.
func onMain(url string) string {
	return "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: any\nmodules:\n  - name: core\n    source: {git: " + url + ", ref: main}\n"
}

// TestComposeFollowsABranch is a module at main: a compose after a push takes the new
// commit without --update and prints the move on the module's row, a compose with nothing
// pushed prints no row, and a branch deleted on the remote fails with the gone error.
func TestComposeFollowsABranch(t *testing.T) {
	root := newCheckout(t)
	remote, url := branchRemote(t)
	writeOwnStack(t, root, onMain(url))
	out, err := run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsNoRow(t, out, "module")
	first := readReport(t, root).Modules[0].Pin
	writeFile(t, filepath.Join(remote, "AGENTS.md"), "# v2\n")
	runGit(t, remote, "commit", "-q", "-am", "second")
	out, err = run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	second := readReport(t, root).Modules[0].Pin
	if second == first {
		t.Fatalf("the compose after a push stayed on %s", first)
	}
	wantsRow(t, out, "module", "core  main "+first+" → "+second)
	if data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(data) != "# v2\n" {
		t.Errorf("AGENTS.md reads %q after the push", data)
	}
	out, err = run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsNoRow(t, out, "module")
	runGit(t, remote, "checkout", "-q", "-b", "other")
	runGit(t, remote, "branch", "-q", "-D", "main")
	_, err = run(t, "hc")
	if want := url + " has no branch or tag main; to keep the old harness, set ref to a commit id"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("err = %v, exit %d\nwant %s", err, cmd.ExitCode(err), want)
	}
}

// TestComposeWarnsOffline is a module at main whose remote is gone: compose, --dry-run and
// --check keep the cached commit and print the warning.
func TestComposeWarnsOffline(t *testing.T) {
	root := newCheckout(t)
	remote, url := branchRemote(t)
	writeOwnStack(t, root, onMain(url))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pin := readReport(t, root).Modules[0].Pin
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	warning := "⚠ " + url + "#main: could not reach the remote; kept " + pin + "\n"
	for _, args := range [][]string{{"hc"}, {"hc", "--dry-run"}, {"hc", "--check"}} {
		out, err := run(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if strings.Count(out, warning) != 1 {
			t.Errorf("%v prints the warning %d times, want once:\n%s", args, strings.Count(out, warning), out)
		}
	}
	if rep := readReport(t, root); rep.Modules[0].Pin != pin {
		t.Errorf("pin %s offline, want the cached %s", rep.Modules[0].Pin, pin)
	}
}

// TestComposeChecksAMovedBranch is --check on a module at main: up to date while the
// branch stays, and stale once a push changes the module, without --update.
func TestComposeChecksAMovedBranch(t *testing.T) {
	root := newCheckout(t)
	remote, url := branchRemote(t)
	writeOwnStack(t, root, onMain(url))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := readReport(t, root).Modules[0].Pin
	out, err := run(t, "hc", "--check")
	if err != nil {
		t.Fatalf("the branch as composed: %v\n%s", err, out)
	}
	wants(t, out, "up to date")
	wantsNoRow(t, out, "module")
	writeFile(t, filepath.Join(remote, "AGENTS.md"), "# v2\n")
	runGit(t, remote, "commit", "-q", "-am", "second")
	out, err = run(t, "hc", "--check")
	if cmd.ExitCode(err) != cmd.ExitStale {
		t.Errorf("a push to the module: exit %d (%v), want %d\n%s", cmd.ExitCode(err), err, cmd.ExitStale, out)
	}
	wants(t, out, "core  main "+first+" → ")
}
