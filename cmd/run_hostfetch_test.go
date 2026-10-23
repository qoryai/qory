package cmd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// hostFetch is a run from a control plane whose repositories qory run fetches on this
// machine, through a stand-in for the run's host proxy: the server it leads to, the run
// directory, the log of every git command line, and the working folder, an empty git
// repository.
type hostFetch struct {
	server *proxygittest.Server
	runDir string
	argv   string
	dir    string
}

// newHostFetch serves acme/shop, whose qory.yaml lists the module core from the git
// source src at ref, and acme/harness, which has the module core under modules/core. It
// puts a git first on the PATH that writes its command line to a log and runs git.
func newHostFetch(t *testing.T, src, ref string) *hostFetch {
	t.Helper()
	h := &hostFetch{dir: emptyDir(t)}
	h.server = proxygittest.Start(t)
	runGit(t, h.dir, "init", "--quiet", "-b", "main")
	h.server.Repository(t, "acme/harness", "main", map[string]string{
		"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\n",
	})
	if src == "" {
		src = h.server.URL("acme/harness")
	}
	h.server.Repository(t, "acme/shop", "main", map[string]string{
		"README.md": "acme/shop\n",
		"qory.yaml": ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: \"" + src + "\", ref: " + ref + ", path: modules/core}\n"),
	})
	fakeLaunch(t, fakeRuntime(t))
	h.runDir = tempDir(t)
	h.argv = filepath.Join(tempDir(t), "argv")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bin := tempDir(t)
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+h.argv+"\nexec "+real+" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return h
}

// run runs qory run claude with a run key, its run opened nowhere with the server's
// system git.test, the repositories given and the stand-in host proxy, and returns the
// output, the error, what the run came to and the calls the run got.
func (h *hostFetch) run(t *testing.T, repositories string) (string, error, keyRun, []string) {
	t.Helper()
	return h.runWith(t, repositories, nil)
}

// runWith is [hostFetch.run] with proxyErr as the error of the run's HostProxy.
func (h *hostFetch) runWith(t *testing.T, repositories string, proxyErr error) (string, error, keyRun, []string) {
	t.Helper()
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: repositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		ProxyErr: proxyErr,
	})
	defer restore()
	out, err := run(t, "run", "claude", "--run-key-fd", descriptor(t, key))
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, calls()
}

// noPassword fails the test when the output, a git command line or a file under the
// working folder, the run directory or the home holds the proxy's password.
func (h *hostFetch) noPassword(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, proxygittest.Password) {
		t.Errorf("the output holds the proxy's password:\n%s", out)
	}
	for _, dir := range []string{h.dir, h.runDir, os.Getenv("HOME"), filepath.Dir(h.argv)} {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if data, _ := os.ReadFile(path); strings.Contains(string(data), proxygittest.Password) {
				t.Errorf("%s holds the proxy's password", path)
			}
			return nil
		})
	}
}

// gitFetches matches the command line of a git that reaches a remote.
var gitFetches = regexp.MustCompile(` (fetch|ls-remote|clone) `)

// fetchTimes are the times in the fetch lines of qory run.
var fetchTimes = regexp.MustCompile(` in [0-9]+\.[0-9]s\n`)

// TestARunFetchesThroughTheHostProxy is a run from a control plane that lists acme/shop,
// on a machine whose git configuration sends git.test elsewhere and whose environment
// names another proxy: qory run fetches acme/shop into the working folder at depth one
// and the module's git source into the run directory, both through the host proxy,
// prints a line for each in that order, closes the host proxy before the join, and
// leaves the proxy's password in no line, git command line or file.
func TestARunFetchesThroughTheHostProxy(t *testing.T) {
	h := newHostFetch(t, "", "main")
	writeFile(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[url \"https://elsewhere.invalid/\"]\n\tinsteadOf = https://git.test/\n")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	shop := proxygittest.Git(t, filepath.Join(h.server.Root, "acme", "shop.git"), "rev-parse", "HEAD")
	harness := proxygittest.Git(t, filepath.Join(h.server.Root, "acme", "harness.git"), "rev-parse", "HEAD")
	out, err, n, calls := h.run(t, "acme/shop")
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	want := "qory run: fetched acme/shop at " + shop[:12] + " in X\n" +
		"qory run: fetched " + h.server.URL("acme/harness") + "#main at " + harness[:12] + " in X\n" +
		"qory run: composed 0 entries from 1 module for claude\n"
	if got := fetchTimes.ReplaceAllString(out, " in X\n"); !strings.HasSuffix(got, want) || strings.Count(got, "\n") != 4 {
		t.Errorf("the lines:\n%s\nwant the gateway's line, then:\n%s", got, want)
	}
	if want := []string{"host proxy", "host proxy closed", "join"}; !slices.Equal(calls, want) {
		t.Errorf("the calls: %v, want %v", calls, want)
	}
	if h.server.Tunnels() == 0 {
		t.Error("no fetch went through the host proxy")
	}
	if got := gitOut(t, h.dir, "rev-parse", "HEAD"); got != shop {
		t.Errorf("the working folder is at %s, want %s", got, shop)
	}
	if got := gitOut(t, h.dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the working folder is shallow: %s, want true", got)
	}
	if entries, err := os.ReadDir(filepath.Join(h.runDir, "sources")); err != nil || len(entries) == 0 {
		t.Errorf("the run directory's sources: %v, %v", entries, err)
	}
	argv, err := os.ReadFile(h.argv)
	if err != nil || !gitFetches.Match(argv) {
		t.Fatalf("the git command lines: %v\n%s", err, argv)
	}
	for _, opt := range []string{"core.hooksPath=" + os.DevNull, "credential.helper=", "protocol.allow=never", "protocol.https.allow=always"} {
		for _, line := range strings.Split(strings.TrimSpace(string(argv)), "\n") {
			if gitFetches.MatchString(line) && !strings.Contains(line, opt) {
				t.Errorf("git %s lacks -c %s", line, opt)
			}
		}
	}
	h.noPassword(t, out)
}

// TestARunStopsOnAFailedHostFetch is a run from a control plane whose repository, git
// source or ref the server lacks, or whose git source is not https: qory run prints the
// fix as one line, closes the run as failed with no session, and exits 1, or 2 for a ref
// the remote lacks and a source that is not https. A failed fetch of the repository
// stops the run before the compose.
func TestARunStopsOnAFailedHostFetch(t *testing.T) {
	for _, c := range []struct {
		name, repositories, src, ref string
		want                         func(h *hostFetch) string
		exit                         int
		plant                        func(dir string)
	}{
		{"repository", "acme/missing", "", "main", func(*hostFetch) string {
			return "qory run: add a credential for git.test that reads acme/missing to the run configuration, then run again. The fetch of acme/missing through the run's gateway failed: "
		}, 1, nil},
		{"git source", "acme/shop", "https://git.test/acme/missing.git", "main", func(*hostFetch) string {
			return "qory run: check that https://git.test/acme/missing.git has the branch, tag or commit main, that the run's policy allows git.test and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: "
		}, 1, nil},
		{"ref", "acme/shop", "", "gone", func(h *hostFetch) string {
			return "qory run: check that " + h.server.URL("acme/harness") + " has the branch, tag or commit gone, that the run's policy allows git.test and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: " + h.server.URL("acme/harness") + " has no branch or tag gone\n"
		}, 2, nil},
		{"scheme", "acme/shop", "git@github.com:example-org/harness.git", "main", func(*hostFetch) string {
			return "qory run: use an https URL for git@github.com:example-org/harness.git in qory.yaml, such as https://github.com/example-org/harness. A run from a control plane fetches git sources only over https, through the run's gateway; ssh would use this machine's keys.\n"
		}, 2, nil},
		{"proxy in the working folder", "acme/shop", "", "main", func(*hostFetch) string {
			return "qory run: start the run in a git working tree with no remote: the working folder's repository has a remote already, and the run's repository is fetched only into an empty git working tree\n"
		}, 1, func(dir string) { runGit(t, dir, "config", "remote.origin.proxy", "http://127.0.0.1:9") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHostFetch(t, c.src, c.ref)
			if c.plant != nil {
				c.plant(h.dir)
			}
			out, err, n, calls := h.run(t, c.repositories)
			if cmd.ExitCode(err) != c.exit || !n.stoppedBeforeJoin() {
				t.Errorf("%v (exit %d, closes %v, joined %v), want exit %d before the join\n%s", err, cmd.ExitCode(err), n.closes, n.joined, c.exit, out)
			}
			if want := c.want(h); !strings.Contains(out, "\n"+want) {
				t.Errorf("the output:\n%s\nwant the line %s", out, want)
			}
			if want := []string{"host proxy", "host proxy closed"}; !slices.Equal(calls, want) {
				t.Errorf("the calls: %v, want %v", calls, want)
			}
			if c.name == "repository" || c.plant != nil {
				lacks(t, out, "fetched https://", "composed")
			}
			h.noPassword(t, out)
		})
	}
}

// TestARunStopsWhenItsHostProxyDoesNotOpen is a run from a control plane whose
// HostProxy fails: qory run prints the fix first and Forager's error after it, fetches
// nothing, closes the run as failed with no session, and exits 1.
func TestARunStopsWhenItsHostProxyDoesNotOpen(t *testing.T) {
	h := newHostFetch(t, "", "main")
	out, err, n, calls := h.runWith(t, "acme/shop", errors.New("the run's session is joining"))
	want := "\nqory run: run again: the run's host proxy, through which qory fetches the run's code, did not open: the run's session is joining\n"
	if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() || !strings.Contains(out, want) {
		t.Errorf("%v (exit %d, closes %v, joined %v)\n%s\nwant the line%s", err, cmd.ExitCode(err), n.closes, n.joined, out, want)
	}
	if want := []string{"host proxy"}; !slices.Equal(calls, want) {
		t.Errorf("the calls: %v, want %v", calls, want)
	}
	lacks(t, out, "qory run: fetched")
}

// TestARunWithNoControlPlaneComposesAsBefore is a person's run, with no run key, whose
// qory.yaml lists a module from a git source of this machine: qory run composes it with
// the person's git, opens no host proxy, prints no fetch line and goes on to the join.
func TestARunWithNoControlPlaneComposesAsBefore(t *testing.T) {
	root := newCheckout(t)
	harness := tempDir(t)
	runGit(t, harness, "init", "--quiet", "-b", "main")
	runGit(t, harness, "config", "user.name", "Tester")
	runGit(t, harness, "config", "user.email", "tester@example.com")
	writeManifest(t, filepath.Join(harness, "modules", "core"), "core")
	runGit(t, harness, "add", "-A")
	runGit(t, harness, "commit", "--quiet", "-m", "first")
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: \"file://"+harness+"\", ref: main, path: modules/core}\n")
	t.Setenv("QORY_RUN_KEY", "")
	_, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{Proxy: "http://qory:nowhere@127.0.0.1:9"})
	defer restore()
	out, err := run(t, "run", "claude")
	if !errors.Is(err, cmd.ErrJoinedNowhere) || !strings.Contains(out, "qory run: composed 0 entries from 1 module for claude\n") {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "qory run: fetched")
	if slices.Contains(calls(), "host proxy") {
		t.Errorf("the calls: %v, want no host proxy", calls())
	}
}
