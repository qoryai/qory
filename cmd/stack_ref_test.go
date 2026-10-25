package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/stack"
)

// refURL is the https URL of the harness repository the stack reference tests fetch
// without a run key: the test's global git configuration rewrites it to a repository on
// this machine.
const refURL = "https://git.example.com/example-org/harness"

// helloFiles are the files of examples/hello, by their path under it. Call it before
// the test changes the working directory, which is the package's directory until then.
func helloFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	base := filepath.Join("..", "examples", "hello")
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		files["examples/hello/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// refRepo makes the harness repository on this machine with the files given, the hello
// example under examples/hello among them, and an open stack under stacks/web with the
// module core, makes [refURL] lead to it through the test's global git configuration,
// and returns its commit. Call it after [emptyDir], which sets the configuration's path.
func refRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := tempDir(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Publisher")
	runGit(t, dir, "config", "user.email", "op@example.com")
	for name, body := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	writeFile(t, filepath.Join(dir, "stacks", "web", stack.FileName), "apiVersion: qory.dev/v1alpha1\nname: web\nmodules:\n  - name: core\nextending:\n  kinds: [skills]\n")
	writeManifest(t, filepath.Join(dir, "modules", "core"), "core")
	writeFile(t, filepath.Join(dir, "modules", "core", "skills", "review", "SKILL.md"), "# review\n")
	writeFile(t, filepath.Join(dir, "stacks", "empty", "README.md"), "no stack here\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "harness")
	writeFile(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[url \"file://"+dir+"\"]\n\tinsteadOf = "+refURL+"\n")
	return gitOut(t, dir, "rev-parse", "HEAD")
}

// TestComposeTakesAStackReference is -f set to the hello example's folder at a commit of
// the repository, in a checkout with no document: the compose fetches the folder and
// composes its stack alone, the hello and world modules from the fetched tree, each
// recorded as a git source of the repository pinned to that commit, and a --check with
// the same reference finds the home up to date.
func TestComposeTakesAStackReference(t *testing.T) {
	files := helloFiles(t)
	root := newCheckout(t)
	commit := refRepo(t, files)
	ref := refURL + "#" + commit + ":examples/hello"
	out, err := run(t, "harness", "compose", "-f", ref, "--runtime", "claude")
	if err != nil {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "hello · claude", "from 2 modules")
	rep := readReport(t, root)
	if len(rep.Modules) != 2 {
		t.Fatalf("modules: %+v", rep.Modules)
	}
	for _, m := range rep.Modules {
		if want := refURL + "#" + commit + ":examples/hello/modules/" + m.Name; m.Source != want || m.Pin != commit[:12] {
			t.Errorf("module %s: source %s at %s, want %s at %s", m.Name, m.Source, m.Pin, want, commit[:12])
		}
	}
	if rep.Base != nil {
		t.Errorf("the stack alone has a base: %+v", rep.Base)
	}
	greet := readText(t, filepath.Join(root, ".claude", "skills", "greet", "SKILL.md"))
	world := files["examples/hello/modules/world/skills/greet/SKILL.md"]
	if greet != world {
		t.Errorf("skills/greet is not the world module's:\n%s", greet)
	}
	if out, err := run(t, "harness", "compose", "-f", ref, "--runtime", "claude", "--check"); err != nil {
		t.Fatalf("check: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}

// TestAStackReferenceIsTheDocumentsBase is -f set to a reference in a checkout whose
// document lists a module of the checkout: the fetched stack is the document's base, the
// base row shows the reference, and the report records the base by the reference and the commit
// it resolved to.
func TestAStackReferenceIsTheDocumentsBase(t *testing.T) {
	root := fleetCheckout(t, "qory.yaml", consumerTarget)
	commit := refRepo(t, nil)
	ref := refURL + "#main:stacks/web"
	out, err := run(t, "harness", "compose", "-f", ref)
	if err != nil {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wantsRow(t, out, "base", ref+"  (set by -f)")
	rep := readReport(t, root)
	if rep.Base == nil || rep.Base.Name != "web" || rep.Base.Source != ref || rep.Base.Pin != commit[:12] {
		t.Fatalf("base: %+v, want %s at %s", rep.Base, ref, commit[:12])
	}
	if len(rep.Modules) != 2 || rep.Modules[0].Name != "core" || !rep.Modules[0].Base || rep.Modules[0].Source != refURL+"#main:modules/core" || rep.Modules[1].Name != "app" {
		t.Fatalf("modules: %+v", rep.Modules)
	}
}

// TestAStackReferenceStopsWithExit2 is each mistake in a reference, on qory harness
// compose and on qory run: a URL that is not https, no ref, no folder, an absolute
// folder, a folder with .., a folder the tree does not have, and a folder without a
// stack file. Each stops with exit 2 and an error that starts with its fix.
func TestAStackReferenceStopsWithExit2(t *testing.T) {
	newCheckout(t)
	refRepo(t, nil)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	for _, c := range []struct{ value, want string }{
		{"ssh://git@git.example.com/example-org/harness#main:stacks/web", "use an https URL in -f ssh://git@git.example.com/example-org/harness#main:stacks/web, as <https URL>#<ref>:<folder>: "},
		{refURL, "add the ref to -f " + refURL + ", as <https URL>#<ref>:<folder>: "},
		{refURL + "#main", "add the folder to -f " + refURL + "#main, as <https URL>#<ref>:<folder>: "},
		{refURL + "#main:/stacks/web", "write the folder in -f " + refURL + "#main:/stacks/web as a path from the repository's root: "},
		{refURL + "#main:stacks/../web", "write the folder in -f " + refURL + "#main:stacks/../web without ..: "},
		{refURL + "#main:stacks/missing", "check the folder in -f " + refURL + "#main:stacks/missing: " + refURL + " has no stacks/missing at main"},
		{refURL + "#main:stacks/empty", "check the folder in -f " + refURL + "#main:stacks/empty: " + refURL + " has no qory-stack.yaml in stacks/empty at main"},
	} {
		for _, args := range [][]string{{"harness", "compose", "--runtime", "claude"}, {"run", "claude"}} {
			out, err := run(t, append(args, "-f", c.value)...)
			if err != nil {
				// qory harness compose returns its error for main to print.
				out += err.Error()
			}
			if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(out, c.want) {
				t.Errorf("%s -f %s: %v (exit %d), want exit 2 and %q\n%s", args[0], c.value, err, cmd.ExitCode(err), c.want, out)
			}
		}
	}
}

// TestRunTakesAStackReference is qory run with -f set to the hello example's folder at a
// commit, with no run key: the run composes the fetched stack for the runtime given,
// prints its line, and session.started lists each module's git source with the commit.
// In a checkout whose document lists a module, the reference is the base, and
// session.started lists it first.
func TestRunTakesAStackReference(t *testing.T) {
	files := helloFiles(t)
	root := newCheckout(t)
	commit := refRepo(t, files)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "-f", refURL+"#"+commit+":examples/hello", "--", "-p", "/hello")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if !regexp.MustCompile(`qory run: composed [0-9]+ entries from 2 modules for claude\n`).MatchString(out) {
		t.Errorf("no compose line for 2 modules:\n%s", out)
	}
	wants(t, out, "hello from ", "-p /hello")
	if rep := readReport(t, root); len(rep.Modules) != 2 || rep.Modules[0].Pin != commit[:12] {
		t.Errorf("modules: %+v", rep.Modules)
	}
	harness, _ := dataOf(sessionRecord(t, root), "dev.qory.session.started")["harness"].([]any)
	want := []any{
		map[string]any{"source": refURL + "#" + commit + ":examples/hello/modules/hello", "commit": commit},
		map[string]any{"source": refURL + "#" + commit + ":examples/hello/modules/world", "commit": commit},
	}
	if b, w := mustJSON(t, harness), mustJSON(t, want); b != w {
		t.Errorf("session.started lists the harness %s, want %s", b, w)
	}

	root = fleetCheckout(t, "qory.yaml", consumerTarget)
	commit = refRepo(t, nil)
	fakeLaunch(t, fakeRuntime(t))
	out, err = run(t, "run", "claude", "-f", refURL+"#main:stacks/web")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("base: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	harness, _ = dataOf(sessionRecord(t, root), "dev.qory.session.started")["harness"].([]any)
	want = []any{
		map[string]any{"source": refURL + "#main:stacks/web", "commit": commit},
		map[string]any{"source": refURL + "#main:modules/core", "commit": commit},
	}
	if b, w := mustJSON(t, harness), mustJSON(t, want); b != w {
		t.Errorf("base: session.started lists the harness %s, want %s", b, w)
	}
}

// TestARunWithARunKeyFetchesAStackReferenceThroughTheStackProxy is qory run given a run
// key, with -f set to a reference: the folder is fetched through the run's stack proxy
// into the run's directory, and nothing through the host proxy, with one line, and the
// modules of the fetched stack come from the same fetch. A git source the fetched stack
// lists that is not https stops the run with exit 2 before git runs.
func TestARunWithARunKeyFetchesAStackReferenceThroughTheStackProxy(t *testing.T) {
	files := helloFiles(t)
	h := newHostFetch(t, "", "main")
	commit := h.server.Repository(t, "example-org/hello", "main", files)
	url := h.server.URL("example-org/hello")
	out, err, n := h.runRef(t, url+"#main:examples/hello")
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	got := fetchTimes.ReplaceAllString(out, " in X\n")
	want := regexp.MustCompile(`qory run: fetched ` + regexp.QuoteMeta(url) + `#main at ` + commit[:12] + ` in X\nqory run: composed [0-9]+ entries from 2 modules for claude\n$`)
	if !want.MatchString(got) || strings.Count(got, "qory run: fetched") != 1 {
		t.Errorf("the lines:\n%s\nwant one fetch line for the reference, then the compose line", got)
	}
	if h.server.StackTunnels() == 0 || h.server.Tunnels() != 0 {
		t.Errorf("%d fetches went through the stack proxy and %d through the host proxy, want some and none", h.server.StackTunnels(), h.server.Tunnels())
	}
	if entries, err := os.ReadDir(filepath.Join(h.runDir, "sources")); err != nil || len(entries) == 0 {
		t.Errorf("the run directory's sources: %v, %v", entries, err)
	}
	h.noPassword(t, out)

	h = newHostFetch(t, "", "main")
	h.server.Repository(t, "example-org/hello", "main", map[string]string{
		"stacks/web/" + stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: extra\n    source: {git: \"git@github.com:example-org/extra.git\", ref: v1}\n",
	})
	out, err, n = h.runRef(t, h.server.URL("example-org/hello")+"#main:stacks/web")
	if cmd.ExitCode(err) != cmd.ExitInput || !n.stoppedBeforeJoin() {
		t.Errorf("%v (exit %d), want exit 2 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: use an https URL for git@github.com:example-org/extra.git, such as https://github.com/example-org/extra. ")
}

// TestARunKeyStopsAStackReferenceWithAnAddressFault is qory run given a run key, with -f
// set to a reference whose URL has a user part, an @ before its path, or a host with a
// character outside ASCII: the run stops with exit 2 and an error that starts with the
// fix in -f, before anything is fetched, and no line shows the user part.
func TestARunKeyStopsAStackReferenceWithAnAddressFault(t *testing.T) {
	const user = "remove the user name and password from the URL in -f: the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none"
	files := helloFiles(t)
	for _, c := range []struct{ name, url, want string }{
		{"a user part", "https://reader:s3cret@" + proxygittest.Host + "/example-org/hello.git", user},
		{"an @ after a port", "https://" + proxygittest.Host + ":443@evil.example/example-org/hello.git", user},
		{"a fullwidth @ in the host", "https://u＠" + proxygittest.Host + "/example-org/hello.git",
			"write the host of the URL in -f in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHostFetch(t, "", "main")
			h.server.Repository(t, "example-org/hello", "main", files)
			out, err, n := h.runRef(t, c.url+"#main:examples/hello")
			if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput || !n.stoppedBeforeJoin() {
				t.Fatalf("%v (exit %d), want %q, exit 2 before the join\n%s", err, cmd.ExitCode(err), c.want, out)
			}
			if h.server.StackTunnels() != 0 || h.server.Tunnels() != 0 || strings.Contains(out, "fetched") {
				t.Errorf("the run fetched before it stopped: %d and %d tunnels\n%s", h.server.StackTunnels(), h.server.Tunnels(), out)
			}
			for _, secret := range []string{"reader", "s3cret"} {
				if strings.Contains(out, secret) || strings.Contains(err.Error(), secret) {
					t.Errorf("the output or the error shows %q:\n%s", secret, out)
				}
			}
			h.noPassword(t, out)
		})
	}
}

// runRef is [hostFetch.run] with no repositories and -f set to ref.
func (h *hostFetch) runRef(t *testing.T, ref string) (string, error, keyRun) {
	t.Helper()
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
	})
	defer restore()
	out, err := run(t, "run", "claude", "--run-key-fd", descriptor(t, key), "-f", ref)
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}
}
