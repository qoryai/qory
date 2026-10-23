package cmd_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// inCheckoutRun runs qory run with a run key, its run opened nowhere ([cmd.OpenNowhere]),
// and returns its output and error, and the outcomes the run's close got.
func inCheckoutRun(t *testing.T, args ...string) (string, error, keyRun) {
	t.Helper()
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	closes, restore := cmd.OpenNowhere()
	defer restore()
	out, err := run(t, append(append([]string{"run"}, args...), "--run-key-fd", descriptor(t, key))...)
	if _, n := gw.received(); n != 0 {
		t.Errorf("%d connections reached the gateway of a run opened nowhere", n)
	}
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}
}

// keyRun is what a run given a run key and opened nowhere came to: the outcomes its
// close got, and whether its session went on to the join.
type keyRun struct {
	closes []session.Outcome
	joined bool
}

// stoppedBeforeJoin reports whether the run closed once, failed with no reason and -1,
// which Forager records as failed with no_session, and no session joined.
func (k keyRun) stoppedBeforeJoin() bool {
	return !k.joined && len(k.closes) == 1 && k.closes[0] == session.Outcome{State: "failed", ExitCode: -1}
}

// moduleRefusal is the line a run given a run key stops with for module name, listed in
// the file at root by a path that resolves to dir, outside root.
func moduleRefusal(name, root, file, dir string) string {
	return "move module " + name + " into the checkout " + root + ", or give it a git source in " + file + ". A run given a run key reads a module that a file of the checkout lists only from inside the checkout, and " + dir + " is outside it"
}

// TestARunKeyReadsModulesOnlyInsideTheCheckout is a checkout whose qory.yaml lists a
// module by a path outside the checkout, and then by a link in the checkout that resolves
// outside: a person's run composes it, and a run given a run key opens its run and stops
// before the join, with exit 2.
func TestARunKeyReadsModulesOnlyInsideTheCheckout(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, filepath.Join(root, "modules", "core"), "core")
	outside := filepath.Join(tempDir(t), "extra")
	writeManifest(t, outside, "extra")
	fakeLaunch(t, fakeRuntime(t))
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: modules/core}\n  - name: extra\n    source: {path: "
	writeOwnStack(t, root, stack+outside+"}\n")
	t.Setenv("QORY_RUN_KEY", "")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a person's run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	clearRuns(t, root)

	out, err, n := inCheckoutRun(t, "claude")
	if want := moduleRefusal("extra", realRoot, "qory.yaml", outside); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a path outside: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("a path outside: the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}

	if err := os.Symlink(outside, filepath.Join(root, "modules", "extra")); err != nil {
		t.Fatal(err)
	}
	writeOwnStack(t, root, stack+"modules/extra}\n")
	out, err, n = inCheckoutRun(t, "claude")
	if want := moduleRefusal("extra", realRoot, "qory.yaml", outside); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a link that resolves outside: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("a link that resolves outside: the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}
}

// TestARunKeyReadsAnExtendedStackOnlyInsideTheCheckout is a checkout whose qory.yaml
// extends a stack by a path outside the checkout: a run given a run key stops before it
// reads the stack.
func TestARunKeyReadsAnExtendedStackOnlyInsideTheCheckout(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	base := tempDir(t)
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\nextends: {path: "+base+"}\ntarget:\n  runtime: claude\n")
	out, err, n := inCheckoutRun(t, "claude")
	want := "move the stack that qory.yaml extends into the checkout " + realRoot + ", or extend a git source. A run given a run key reads a stack that a file of the checkout extends only from inside the checkout, and " + base + " is outside it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}
}

// TestARunKeyReadsTheModulesOfAStackOutsideTheCheckout is a run given a run key with -f
// naming a stack outside a checkout that has none, the way a starter hands one over: the
// stack is no file of the checkout, so its module outside the checkout is composed, and
// the run goes on to the join.
func TestARunKeyReadsTheModulesOfAStackOutsideTheCheckout(t *testing.T) {
	newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	stacks := tempDir(t)
	outside := filepath.Join(tempDir(t), "extra")
	writeManifest(t, outside, "extra")
	file := filepath.Join(stacks, "qory-stack.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: extra\n    source: {path: "+outside+"}\n")
	homes := tempDir(t)
	out, _, n := inCheckoutRun(t, "claude", "-f", file, "--home", homes)
	wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
}

// TestARunKeyReadsAnExportOnlyInsideTheCheckout is a checkout whose qory.yaml exports its
// modules from a link that resolves outside the checkout, and lists a module by that
// export: a person's run composes it, and a run given a run key stops before it reads the
// module.
func TestARunKeyReadsAnExportOnlyInsideTheCheckout(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := tempDir(t)
	writeManifest(t, filepath.Join(outside, "x"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: x\n    source: {path: ., module: x}\n")
	configure(t, root, nil, []string{"exports:\n  dir: {stacks: stacks, modules: link}\n  modules: [x]"})
	t.Setenv("QORY_RUN_KEY", "")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a person's run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	clearRuns(t, root)

	out, err, n := inCheckoutRun(t, "claude")
	if want := moduleRefusal("x", realRoot, "qory.yaml", filepath.Join(outside, "x")); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}
}

// TestARunKeyReadsAnExportedStackOnlyInsideTheCheckout is a checkout whose qory.yaml
// extends a stack it exports from a link that resolves outside the checkout: a run given
// a run key stops before it reads the stack.
func TestARunKeyReadsAnExportedStackOnlyInsideTheCheckout(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := tempDir(t)
	writeFile(t, filepath.Join(outside, "web", "qory-stack.yaml"), "apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n  - name: core\n    source: {path: core}\n")
	writeManifest(t, filepath.Join(outside, "web", "core"), "core")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\nextends: {path: ., stack: web}\ntarget:\n  runtime: claude\n")
	configure(t, root, nil, []string{"exports:\n  dir: {stacks: link, modules: modules}\n  stacks: [web]"})
	t.Setenv("QORY_RUN_KEY", "")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a person's run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	clearRuns(t, root)

	out, err, n := inCheckoutRun(t, "claude")
	want := "move the stack that qory.yaml extends into the checkout " + realRoot + ", or extend a git source. A run given a run key reads a stack that a file of the checkout extends only from inside the checkout, and " + filepath.Join(outside, "web") + " is outside it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}
}

// TestARunKeyFetchesGitSourcesOverHTTPSOnly is a checkout whose qory.yaml lists a module
// by a git source that is not an https URL, in each form git reads: a run given a run key
// stops with exit 2 before git runs, so the git cache stays empty, and before the join.
func TestARunKeyFetchesGitSourcesOverHTTPSOnly(t *testing.T) {
	root := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	local := tempDir(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	const cause = ". A run from a control plane fetches git sources only over https, through the run's gateway; "
	for _, c := range []struct{ name, url, why string }{
		{"a local path", local, cause + local + " is a folder of this machine."},
		{"a file URL", "file://" + local, cause + "file://" + local + " is a folder of this machine."},
		{"an ssh URL", "ssh://git@example.com/acme/h.git", ", such as https://example.com/acme/h" + cause + "ssh would use this machine's keys."},
		{"the scp form", "git@example.com:acme/h.git", ", such as https://example.com/acme/h" + cause + "ssh would use this machine's keys."},
		{"a git URL", "git://example.com/acme/h.git", ", such as https://example.com/acme/h" + cause + "git:// would reach the host past the gateway."},
	} {
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: extra\n    source: {git: \""+c.url+"\", ref: main}\n")
		out, err, n := inCheckoutRun(t, "claude")
		want := "use an https URL for " + c.url + " in qory.yaml" + c.why
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(out, "\nqory run: "+want+"\n") {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), want, out)
		}
		if !n.stoppedBeforeJoin() {
			t.Errorf("%s: the run closed with %v, joined %v, want failed before the join", c.name, n.closes, n.joined)
		}
		if _, err := os.Stat(filepath.Join(cache, "qory", "sources")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: git ran and wrote the cache: %v", c.name, err)
		}
	}
}

// TestARunKeyExtendsAGitStackOverHTTPSOnly is a checkout whose qory.yaml extends a stack
// at a file URL: a person's run composes it, and a run given a run key stops before git
// runs.
func TestARunKeyExtendsAGitStackOverHTTPSOnly(t *testing.T) {
	url := baseRepo(t)
	root := consumerCheckout(t, url)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a person's run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	clearRuns(t, root)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cache, "qory", "sources")); err != nil {
		t.Fatal(err)
	}

	out, err, n := inCheckoutRun(t, "claude")
	want := "use an https URL for " + url + " in qory.yaml. A run from a control plane fetches git sources only over https, through the run's gateway; " + url + " is a folder of this machine."
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if !n.stoppedBeforeJoin() {
		t.Errorf("the run closed with %v, joined %v, want failed before the join", n.closes, n.joined)
	}
	if _, err := os.Stat(filepath.Join(cache, "qory", "sources")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("git ran and wrote the cache: %v", err)
	}
}

// TestARunKeyReadsNoSettingsOfTheCheckout is a checkout whose qory.yaml sets
// harness.model, harness.update, git.cache and an env key. A person's run composes with
// harness.model and stops at a value it cannot read. A run given a run key reads none of
// these keys: it composes with no model, and composes past each value a person's run
// stops at.
func TestARunKeyReadsNoSettingsOfTheCheckout(t *testing.T) {
	root := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	writeManifest(t, filepath.Join(root, "modules", "core"), "core")
	stack := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n"
	writeOwnStack(t, root, stack)
	configure(t, root, []string{"model: sonnet"}, nil)
	t.Setenv("QORY_RUN_KEY", "")
	if out, err := run(t, "run", "claude"); cmd.ExitCode(err) != 3 {
		t.Fatalf("a person's run: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if model := readReport(t, root).Target.Model; model != "sonnet" {
		t.Errorf("a person's run composed for the model %q, want sonnet", model)
	}
	clearRuns(t, root)
	out, _, n := inCheckoutRun(t, "claude")
	wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
	if model := runKeyReport(t, root).Target.Model; model != "" {
		t.Errorf("a run given a run key composed for the model %q of the checkout's qory.yaml", model)
	}

	for _, c := range []struct {
		name          string
		harness, top  []string
		personStopsAt string
	}{
		{"harness", []string{"update: sometimes"}, nil, "harness.update \"sometimes\" is not always or never"},
		{"git", nil, []string{"git:\n  cache: \"\""}, "git.cache is empty"},
		{"env", nil, []string{"env:\n  1BAD: x"}, "is not an environment variable name"},
	} {
		writeOwnStack(t, root, stack)
		configure(t, root, c.harness, c.top)
		clearRuns(t, root)
		t.Setenv("QORY_RUN_KEY", "")
		if out, err := run(t, "run", "claude"); err == nil || !strings.Contains(err.Error(), c.personStopsAt) {
			t.Errorf("%s: a person's run: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), c.personStopsAt, out)
		}
		out, _, n := inCheckoutRun(t, "claude")
		wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
		if !n.joined {
			t.Errorf("%s: the run did not go on to the join\n%s", c.name, out)
		}
	}
}

// TestARunKeyReadsTheCheckoutsDocumentOnlyInsideTheCheckout is a checkout whose qory.yaml
// is a link to a file outside the checkout, which lists a module by a path outside it: a
// run given a run key stops before it reads the module.
func TestARunKeyReadsTheCheckoutsDocumentOnlyInsideTheCheckout(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fakeLaunch(t, fakeRuntime(t))
	elsewhere := tempDir(t)
	outside := filepath.Join(tempDir(t), "extra")
	writeManifest(t, outside, "extra")
	doc := filepath.Join(elsewhere, "doc.yaml")
	writeFile(t, doc, ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: extra\n    source: {path: "+outside+"}\n"))
	if err := os.Symlink(doc, filepath.Join(root, "qory.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err, n := inCheckoutRun(t, "claude")
	want := "replace the link qory.yaml with a file inside the checkout " + realRoot + ". A run given a run key reads the document of the checkout only from inside the checkout, and qory.yaml links to " + doc + ", which is outside it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if len(n.closes) != 0 {
		t.Errorf("the run opened, and closed with %v; the check of the checkout comes before the open", n.closes)
	}
}

// TestARunKeyReadsTheTiersOfTheCheckout is a checkout whose qory.yaml sets harness.tiers
// and harness.model: a run given a run key composes the agent with the checkout's tier,
// and with no model.
func TestARunKeyReadsTheTiersOfTheCheckout(t *testing.T) {
	root := tierCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	tierModule(t, root, "claude", map[string]string{"explorer": "---\nname: explorer\nmodel: ${qory:light}\n---\nExplores.\n"},
		"model: sonnet",
		"tiers:",
		"  light: claude-haiku-5-5",
	)
	out, _, n := inCheckoutRun(t, "claude")
	wants(t, out, "qory run: composed 1 entry from 1 module for claude\n")
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
	agent := filepath.Join(runKeyHome(t, root), "claude", "agents", "explorer.md")
	if got, want := readText(t, agent), "---\nname: explorer\nmodel: claude-haiku-5-5\n---\nExplores.\n"; got != want {
		t.Errorf("agents/explorer.md:\n%s\nwant:\n%s", got, want)
	}
	if model := runKeyReport(t, root).Target.Model; model != "" {
		t.Errorf("a run given a run key composed for the model %q of the checkout's qory.yaml", model)
	}
}

// TestAGitStacksModulesResolveInsideTheClone is a checkout that extends a git stack whose
// modules resolve outside the stack's repository: by a path above it, and by a link the
// repository contains. The compose stops on each, and reads nothing outside the clone.
func TestAGitStacksModulesResolveInsideTheClone(t *testing.T) {
	outside := filepath.Join(tempDir(t), "evil")
	writeManifest(t, outside, "evil")
	writeFile(t, filepath.Join(outside, "skills", "evil", "SKILL.md"), "# evil\n")
	for _, c := range []struct{ name, path, link, want string }{
		{"a path above the repository", "../../../../../../../../../../.." + outside, "", "is outside the base stack's repository"},
		{"a link the repository contains", "evil", filepath.Join("web", "evil"), "links outside the repository"},
		{"an absolute path", outside, "", "is outside the base stack's repository"},
		{"a path inside the repository", "inside", "", ""},
	} {
		stack := "apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n  - name: evil\n    source: {path: " + c.path + "}\n"
		dir := tempDir(t)
		runGit(t, dir, "init", "-q", "-b", "main")
		runGit(t, dir, "config", "user.name", "Publisher")
		runGit(t, dir, "config", "user.email", "op@example.com")
		writeFile(t, filepath.Join(dir, "web", "qory-stack.yaml"), stack)
		if c.link != "" {
			if err := os.Symlink(outside, filepath.Join(dir, c.link)); err != nil {
				t.Fatal(err)
			}
		}
		if c.want == "" {
			writeManifest(t, filepath.Join(dir, "web", "inside"), "evil")
		}
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-q", "-m", "stack")
		root := newCheckout(t)
		// The cache of git sources is under HOME, which git prints with every link
		// resolved: so are the paths the stack is read from.
		home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {git: \"file://"+dir+"\", ref: main, path: web}\n  target: {runtime: claude}\n")
		out, err := run(t, "harness", "compose")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v\n%s", c.name, err, out)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), c.want, out)
		}
		if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "claude", "skills", "evil")); err == nil {
			t.Errorf("%s: the skill outside the clone was composed", c.name)
		}
	}
}

// TestARunKeyReadsTheFilesOfAModuleOnlyInsideTheCheckout is a checkout module whose
// qory-module.yaml, or a skill file in it, is a link that resolves outside the checkout:
// a run given a run key stops before it reads the module.
func TestARunKeyReadsTheFilesOfAModuleOnlyInsideTheCheckout(t *testing.T) {
	outside := tempDir(t)
	writeManifest(t, filepath.Join(outside, "core"), "core")
	writeFile(t, filepath.Join(outside, "SKILL.md"), "# secret\n")
	writeFile(t, filepath.Join(outside, "skills", "x", "SKILL.md"), "# secret\n")
	for _, c := range []struct{ name, link, target string }{
		{"the manifest", "qory-module.yaml", filepath.Join(outside, "core", "qory-module.yaml")},
		{"a skill file", filepath.Join("skills", "x", "SKILL.md"), filepath.Join(outside, "SKILL.md")},
		{"a directory", "skills", filepath.Join(outside, "skills")},
	} {
		root := newCheckout(t)
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		fakeLaunch(t, fakeRuntime(t))
		core := filepath.Join(root, "modules", "core")
		if c.link != "qory-module.yaml" {
			writeManifest(t, core, "core")
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(core, c.link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.target, filepath.Join(core, c.link)); err != nil {
			t.Fatal(err)
		}
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n")
		out, err, n := inCheckoutRun(t, "claude")
		link := filepath.Join("modules", "core", c.link)
		want := "replace the link " + link + " with a file inside the checkout " + realRoot + ". A run given a run key reads the files of a module or a stack that a file of the checkout lists only from inside the checkout, and " + link + " links to " + c.target + ", which is outside it"
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), want, out)
		}
		if !n.stoppedBeforeJoin() {
			t.Errorf("%s: the run closed with %v, joined %v, want failed before the join", c.name, n.closes, n.joined)
		}
	}
}

// TestARunKeyNamesTheLinkThatLeadsOutside is a checkout whose stack lists a module by an
// export of a directory whose qory.yaml is a link outside the checkout, and then extends a
// stack whose qory-stack.yaml is such a link: a run given a run key stops with a line that
// lists the link.
func TestARunKeyNamesTheLinkThatLeadsOutside(t *testing.T) {
	outside := tempDir(t)
	writeFile(t, filepath.Join(outside, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nexports:\n  modules: [x]\n")
	writeFile(t, filepath.Join(outside, "qory-stack.yaml"), "apiVersion: qory.dev/v1alpha1\nname: web\nextending:\n  kinds: [skills]\nmodules:\n  - name: x\n")
	for _, c := range []struct{ name, stack, link, target string }{
		{"an exports file", "target:\n  runtime: claude\nmodules:\n  - name: x\n    source: {path: lib, module: x}\n", filepath.Join("lib", "qory.yaml"), filepath.Join(outside, "qory.yaml")},
		{"a stack file", "extends: {path: lib}\ntarget:\n  runtime: claude\n", filepath.Join("lib", "qory-stack.yaml"), filepath.Join(outside, "qory-stack.yaml")},
	} {
		root := newCheckout(t)
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		fakeLaunch(t, fakeRuntime(t))
		writeManifest(t, filepath.Join(root, "lib", "modules", "x"), "x")
		if err := os.Symlink(c.target, filepath.Join(root, c.link)); err != nil {
			t.Fatal(err)
		}
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\n"+c.stack)
		out, err, n := inCheckoutRun(t, "claude")
		want := "replace the link " + c.link + " with a file inside the checkout " + realRoot + ". A run given a run key reads the files of a module or a stack that a file of the checkout lists only from inside the checkout, and " + c.link + " links to " + c.target + ", which is outside it"
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), want, out)
		}
		if !n.stoppedBeforeJoin() {
			t.Errorf("%s: the run closed with %v, joined %v, want failed before the join", c.name, n.closes, n.joined)
		}
	}
}

// TestARunKeyComposesADocumentAboveTheCheckout is a checkout with no document of its own
// below a directory whose qory.yaml the compose discovers: that file is no file of the
// checkout, so a run given a run key composes it and goes on to the join.
func TestARunKeyComposesADocumentAboveTheCheckout(t *testing.T) {
	root := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	core := filepath.Join(tempDir(t), "core")
	writeManifest(t, core, "core")
	writeFile(t, filepath.Join(filepath.Dir(root), "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: "+core+"}\n"))
	out, _, n := inCheckoutRun(t, "claude")
	wants(t, out, "qory run: composed 0 entries from 1 module for claude\n")
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
}

// TestARunKeyReadsNoConfigurationThroughALink is a checkout whose document is its
// qory-stack.yaml, and whose qory.yaml is a link to a file outside the checkout that sets
// harness.tiers: a run given a run key stops before it reads that file.
func TestARunKeyReadsNoConfigurationThroughALink(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fakeLaunch(t, fakeRuntime(t))
	writeManifest(t, filepath.Join(root, "modules", "core"), "core")
	writeFile(t, filepath.Join(root, "modules", "core", "agents", "explorer.md"), "---\nname: explorer\nmodel: ${qory:light}\n---\nExplores.\n")
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), "apiVersion: qory.dev/v1alpha1\nname: app\nextending:\n  kinds: [skills]\nmodules:\n  - name: core\n")
	elsewhere := filepath.Join(tempDir(t), "qory.yaml")
	writeFile(t, elsewhere, "apiVersion: qory.dev/v1alpha1\nharness:\n  tiers:\n    light: claude-haiku-5-5\n")
	if err := os.Symlink(elsewhere, filepath.Join(root, "qory.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err, n := inCheckoutRun(t, "claude")
	want := "replace the link qory.yaml with a file inside the checkout " + realRoot + ". A run given a run key reads the configuration of the checkout only from inside the checkout, and qory.yaml links to " + elsewhere + ", which is outside it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if len(n.closes) != 0 {
		t.Errorf("the run opened, and closed with %v; the check of the checkout comes before the open", n.closes)
	}
	if text, err := os.ReadFile(filepath.Join(root, ".qory", "harness", "claude", "agents", "explorer.md")); err == nil && strings.Contains(string(text), "claude-haiku-5-5") {
		t.Errorf("the tier of the file outside the checkout was composed:\n%s", text)
	}
}

// TestARunKeyReadsNoStackThroughALink is a checkout whose qory-stack.yaml is a link to a
// file outside the checkout: a run given a run key stops before it reads that file, and
// no part of the file is in its output or its error.
func TestARunKeyReadsNoStackThroughALink(t *testing.T) {
	root := newCheckout(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fakeLaunch(t, fakeRuntime(t))
	const content = "zq_marker_key: quokka-7731-ywvx"
	elsewhere := filepath.Join(tempDir(t), "outside.yaml")
	writeFile(t, elsewhere, content+"\n")
	if err := os.Symlink(elsewhere, filepath.Join(root, "qory-stack.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err, n := inCheckoutRun(t, "claude")
	want := "replace the link qory-stack.yaml with a file inside the checkout " + realRoot + ". A run given a run key reads the document of the checkout only from inside the checkout, and qory-stack.yaml links to " + elsewhere + ", which is outside it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if len(n.closes) != 0 {
		t.Errorf("the run opened, and closed with %v; the check of the checkout comes before the open", n.closes)
	}
	shown := out
	if err != nil {
		shown += err.Error()
	}
	for i := 0; i+4 <= len(content); i++ {
		if part := content[i : i+4]; strings.Contains(shown, part) {
			t.Errorf("the output or the error contains %q of the file outside the checkout:\n%s", part, shown)
			break
		}
	}
}
