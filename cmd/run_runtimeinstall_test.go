package cmd_test

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// unreachableNpm is an npm registry on a loopback port with nothing on it, for a run
// that must not reach npm.
const unreachableNpm = "http://127.0.0.1:9"

// installRun is what a run of claude that installs its runtime came to.
type installRun struct {
	out          string
	err          error
	joins        []session.SessionSpec
	closes       []session.Outcome
	calls        []string
	installFirst bool
}

// installSetup is the run [runInstalling] runs: behind the fake wall when walled, with
// pool.registry under reg's host when reg is not nil, npm at npm's URL when npm is not
// nil, and proxy as the run's host proxy.
type installSetup struct {
	reg    *rt.Registry
	npm    *rt.Npm
	proxy  *rt.Proxy
	walled bool
	// noInstance leaves the run with no address of Qory Apiary; otherwise reg's
	// instance is the run's.
	noInstance bool
	// runtime is the runtime of the run; empty means claude. version is the version the
	// image's fetch looks for in qory's state folder; empty means 2.1.320.
	runtime, version string
	// runtimes is the harness section's runtimes key, such as
	// "runtimes:\n  claude:\n    version: 2.1.300\n"; empty for none.
	runtimes string
	// args are flags that follow the run key's.
	args []string
	// during, when set, runs in its own goroutine while the run goes on.
	during func()
	// before, when set, runs once the test's folders are made, before the run.
	before func()
	// caBundle is the host proxy's CA bundle; empty means none. proxyErr, when not nil,
	// is the error of each opening of the host proxy.
	caBundle string
	proxyErr error
}

// runInstalling runs qory run claude, or s.runtime, with a run key as s sets it up, with
// no repositories. The image's fetch, which only a walled run has, records whether the
// program of claude 2.1.320, or of s.runtime at s.version, was in qory's state folder by
// then, mode 0755.
func runInstalling(t *testing.T, s installSetup) installRun {
	t.Helper()
	defer cmd.SetInstallRuntimes(true)()
	defer cmd.SetRuntimeKeys(rt.Keys())()
	npmURL := unreachableNpm
	if s.npm != nil {
		npmURL = s.npm.URL()
	}
	defer cmd.SetNpmRegistry(npmURL)()
	if s.reg != nil && !s.noInstance {
		defer cmd.SetInstanceAddress(func(*config.Forager) string { return s.reg.Instance() })()
	}
	h := newHostFetch(t, "", "main")
	if s.before != nil {
		s.before()
	}
	section := ""
	if s.walled {
		docker, _ := fakeDocker(t)
		section += "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	}
	if s.reg != nil {
		section += "pool:\n  registry: " + s.reg.Host() + "/qory-env\n  run_config: " + t.TempDir() + "\n"
	}
	if section != "" {
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), section)
	}
	gw := newKeyedGateway(t)
	runtime := cmp.Or(s.runtime, "claude")
	args := []string{"run", runtime, "--home", t.TempDir(), "--run-key-fd", descriptor(t, mintRunKey(t, keyRunID, gw.url()))}
	t.Setenv("QORY_RUN_KEY", "")
	var r installRun
	program := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", runtime, cmp.Or(s.version, "2.1.320"), "bin", runtime)
	defer cmd.SetJoinSession(func(s session.SessionSpec) { r.joins = append(r.joins, s) })()
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Proxy: s.proxy.URL(), CABundle: s.caBundle, ProxyErr: s.proxyErr, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		// A push proxy that takes no connection: the install reads through the host proxy.
		PushProxy: "http://qory:nowhere@127.0.0.1:9",
		FetchImage: func(context.Context) error {
			info, err := os.Lstat(program)
			r.installFirst = err == nil && info.Mode() == 0o755
			return nil
		},
	})
	defer restore()
	writeFile(t, filepath.Join(h.dir, "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: "+runtime+"\nmodules:\n  - name: core\n    source: {git: \""+h.server.URL("acme/harness")+"\", ref: main, path: modules/core}\n"+s.runtimes))
	args = append(args, s.args...)
	if s.during != nil {
		go s.during()
	}
	r.out, r.err = run(t, args...)
	r.closes, r.calls = closes(), calls()
	return r
}

// joined fails the test unless the run's one session joined and the run printed the
// install line once.
func (r installRun) joined(t *testing.T) {
	t.Helper()
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	if n := strings.Count(r.out, "qory run: installed claude 2.1.320\n"); n != 1 {
		t.Fatalf("the run prints the install line %d times:\n%s", n, r.out)
	}
	lacks(t, r.out, rt.ProxyPassword)
}

// TestAWalledRunInstallsItsRuntimeFromTheRegistry runs a walled run of claude on a
// machine whose forager.yaml sets pool.registry: qory reads claude 2.1.320 from the
// registry through the run's host proxy, prints "qory run: installed claude 2.1.320"
// once, and the program is in qory's state folder, mode 0755, before the image's
// fetch. npm gets no request.
func TestAWalledRunInstallsItsRuntimeFromTheRegistry(t *testing.T) {
	reg, npm, proxy := rt.StartRegistry(t), rt.StartNpm(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	npm.Publish("2.1.320")
	r := runInstalling(t, installSetup{reg: reg, npm: npm, proxy: proxy, walled: true})
	r.joined(t)
	if !r.installFirst {
		t.Fatal("the run fetched its image before the program was installed, mode 0755")
	}
	if reg.BlobGets() != 1 || len(npm.Paths()) != 0 || proxy.Passed() == 0 || proxy.Refusals() != 0 {
		t.Fatalf("%d blobs, %d npm requests; the proxy passed %d and refused %d", reg.BlobGets(), len(npm.Paths()), proxy.Passed(), proxy.Refusals())
	}
}

// TestAWalledClaudeSessionStartsTheInstalledProgram has the session's spec at the join
// carry RuntimeInstall with the version's folder and version, and the installed
// program as Command, which Forager requires of a runtime whose descriptor sets
// install_required.
func TestAWalledClaudeSessionStartsTheInstalledProgram(t *testing.T) {
	reg, npm, proxy := rt.StartRegistry(t), rt.StartNpm(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	r := runInstalling(t, installSetup{reg: reg, npm: npm, proxy: proxy, walled: true})
	r.joined(t)
	program := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "2.1.320", "bin", "claude")
	in := r.joins[0].RuntimeInstall
	if in == nil || in.Version != "2.1.320" || in.Dir != filepath.Dir(filepath.Dir(program)) {
		t.Fatalf("the session's RuntimeInstall is %+v, want the folder of claude 2.1.320", in)
	}
	if got := startedProgram(r.joins[0]); got != program {
		t.Fatalf("the session starts %q, want the installed program", got)
	}
}

// startedProgram is the program a session starts: its Command, or behind a wall, when
// Command is Forager's helper, the argument after the first --.
func startedProgram(s session.SessionSpec) string {
	if s.Command != wall.HelperPath {
		return s.Command
	}
	if i := slices.Index(s.Args, "--"); i >= 0 && i+1 < len(s.Args) {
		return s.Args[i+1]
	}
	return s.Command
}

// TestADispatchedCodexRunInstallsCodexAndRunsItBehindTheWall runs codex exec with a run
// key behind the fake wall, with no pool.registry: qory installs codex 0.170.0 from npm
// through the run's host proxy before the image's fetch, prints "qory run: installed
// codex 0.170.0" once, and the session joins with the version's folder as
// RuntimeInstall and the installed program as the program it starts, behind the wall:
// Forager binds the folder read-only at /qory/runtime/codex. Codex runs as a walled run
// starts it: on the home at /var/lib/qory-codex-home, with --sandbox danger-full-access
// after exec. The version's folder is Codex's whole vendor folder of the tarball, with
// bin/codex-code-mode-host, codex-resources/bwrap and codex-path/rg beside bin/codex.
func TestADispatchedCodexRunInstallsCodexAndRunsItBehindTheWall(t *testing.T) {
	npm, proxy := rt.StartCodexNpm(t), rt.StartProxy(t)
	npm.Publish("0.170.0")
	r := runInstalling(t, installSetup{npm: npm, proxy: proxy, walled: true, runtime: "codex", version: "0.170.0", args: []string{"--", "exec", "hi"}})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	if n := strings.Count(r.out, "qory run: installed codex 0.170.0\n"); n != 1 {
		t.Fatalf("the run prints the install line %d times:\n%s", n, r.out)
	}
	if !r.installFirst {
		t.Fatal("the run fetched its image before the program was installed, mode 0755")
	}
	if npm.TarballGets() != 1 || proxy.Passed() != 3 || proxy.Refusals() != 0 {
		t.Fatalf("npm requests %q; the proxy passed %d and refused %d", npm.Paths(), proxy.Passed(), proxy.Refusals())
	}
	program := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "codex", "0.170.0", "bin", "codex")
	if b, err := os.ReadFile(program); err != nil || string(b) != rt.CodexProgram("0.170.0 "+goruntime.GOARCH) {
		t.Fatalf("the installed program is %q (%v)", b, err)
	}
	codexFolderIs(t, filepath.Dir(filepath.Dir(program)), "0.170.0 "+goruntime.GOARCH)
	join := r.joins[0]
	if in := join.RuntimeInstall; in == nil || in.Version != "0.170.0" || in.Dir != filepath.Dir(filepath.Dir(program)) {
		t.Fatalf("the session's RuntimeInstall is %+v, want the folder of codex 0.170.0", in)
	}
	if got := startedProgram(join); got != program {
		t.Fatalf("the session starts %q, want the installed program", got)
	}
	wants(t, strings.Join(join.Args, " "), "exec --sandbox danger-full-access hi")
	wants(t, strings.Join(join.LaunchFixed, " "), "CODEX_HOME=/var/lib/qory-codex-home")
	lacks(t, r.out, rt.ProxyPassword)
}

// TestADispatchedCodexRunInstallsCodexFromTheRegistry runs codex with a run key behind
// the fake wall on a machine whose forager.yaml sets pool.registry: qory reads codex
// 0.170.0 from shared/runtimes/codex of the registry through the run's host proxy, and
// npm gets no request. The layer is npm's tarball: the version's folder is Codex's whole
// vendor folder, and the session starts its bin/codex.
func TestADispatchedCodexRunInstallsCodexFromTheRegistry(t *testing.T) {
	reg, npm, proxy := rt.StartCodexRegistry(t), rt.StartCodexNpm(t), rt.StartProxy(t)
	reg.PublishPlain("0.170.0")
	r := runInstalling(t, installSetup{reg: reg, npm: npm, proxy: proxy, walled: true, runtime: "codex", version: "0.170.0"})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	wants(t, r.out, "qory run: installed codex 0.170.0\n")
	if reg.BlobGets() != 1 || len(npm.Paths()) != 0 || !r.installFirst {
		t.Fatalf("%d blobs, %d npm requests, installed first %v", reg.BlobGets(), len(npm.Paths()), r.installFirst)
	}
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "codex", "0.170.0")
	codexFolderIs(t, dir, "0.170.0 "+goruntime.GOARCH)
	if in := r.joins[0].RuntimeInstall; in == nil || in.Dir != dir {
		t.Fatalf("the session's RuntimeInstall is %+v, want %s", in, dir)
	}
	if got := startedProgram(r.joins[0]); got != filepath.Join(dir, "bin", "codex") {
		t.Fatalf("the session starts %q, want bin/codex of %s", got, dir)
	}
}

// codexFolderIs fails the test unless dir has each file of Codex's folder of version, as
// [rt.CodexFolder] has it, with its body.
func codexFolderIs(t *testing.T, dir, version string) {
	t.Helper()
	for _, f := range rt.CodexFolder(version) {
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Name))); err != nil || string(b) != f.Body {
			t.Errorf("%s of the installed folder is %q (%v), want %q", f.Name, b, err, f.Body)
		}
	}
}

// TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsCause has an empty registry: the
// run, a dispatched one, prints the cause with no fix line, closes failed with -1 before
// the image's fetch, no session joins, and qory run exits 1.
func TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsCause(t *testing.T) {
	r := runInstalling(t, installSetup{reg: rt.StartRegistry(t), proxy: rt.StartProxy(t), walled: true})
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	want := "qory run: the registry has never held claude and could not fetch it:\n  shared/runtimes/claude:newest returned 404\n"
	if !strings.Contains(r.out, want) || strings.Contains(r.out, "then run again") {
		t.Fatalf("the output has no cause line, or has the fix line:\n%s", r.out)
	}
	runLinesFit(t, r.out)
	for _, c := range r.calls {
		if c == "fetch image" {
			t.Errorf("the calls: %v, want no image fetch", r.calls)
		}
	}
	if len(r.closes) != 1 || r.closes[0].State != event.StateFailed || r.closes[0].ExitCode != -1 {
		t.Errorf("the closes: %+v, want one failed with -1", r.closes)
	}
}

// TestAWalledRunWithNoRegistryInstallsFromNpm has no pool.registry in forager.yaml: a
// walled run of claude installs claude 2.1.320 from npm through the run's host proxy:
// the dist-tags, the version's document and its tarball, before the image's fetch.
func TestAWalledRunWithNoRegistryInstallsFromNpm(t *testing.T) {
	npm, proxy := rt.StartNpm(t), rt.StartProxy(t)
	npm.Publish("2.1.320")
	r := runInstalling(t, installSetup{npm: npm, proxy: proxy, walled: true})
	r.joined(t)
	if !r.installFirst {
		t.Fatal("the run fetched its image before the program was installed, mode 0755")
	}
	if len(npm.Paths()) != 3 || npm.TarballGets() != 1 || proxy.Passed() != 3 || proxy.Refusals() != 0 {
		t.Fatalf("npm requests %q; the proxy passed %d and refused %d", npm.Paths(), proxy.Passed(), proxy.Refusals())
	}
}

// TestAnUnwalledRunInstallsFromNpmThroughTheHostProxy runs claude with a run key and no
// wall: the install goes through the run's host proxy, and the session joins.
func TestAnUnwalledRunInstallsFromNpmThroughTheHostProxy(t *testing.T) {
	npm, proxy := rt.StartNpm(t), rt.StartProxy(t)
	npm.Publish("2.1.320")
	r := runInstalling(t, installSetup{npm: npm, proxy: proxy})
	r.joined(t)
	if npm.TarballGets() != 1 || proxy.Passed() != 3 {
		t.Fatalf("%d tarballs; the proxy passed %d", npm.TarballGets(), proxy.Passed())
	}
	if !slices.Contains(r.calls, "host proxy") || slices.Contains(r.calls, "push proxy") {
		t.Errorf("the install opened the proxies %q, want the host proxy and no push proxy", r.calls)
	}
}

// TestARunWithNoControlPlaneInstallsFromNpmDirectly runs claude in a checkout on a
// person's machine, with no wall and no run key, so with no host proxy: the install
// sends its three requests to npm directly, prints its line, and the session starts the
// installed program, not the launch's.
func TestARunWithNoControlPlaneInstallsFromNpmDirectly(t *testing.T) {
	defer cmd.SetInstallRuntimes(true)()
	defer cmd.SetRuntimeKeys(rt.Keys())()
	npm := rt.StartNpm(t)
	npm.Publish("2.1.320")
	defer cmd.SetNpmRegistry(npm.URL())()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: installed claude 2.1.320\n", "claude 2.1.320 ")
	lacks(t, out, "hello from ")
	if len(npm.Paths()) != 3 || npm.TarballGets() != 1 {
		t.Fatalf("npm requests %q", npm.Paths())
	}
}

// TestATarballThatDoesNotMatchItsChecksumEndsTheRun publishes a tarball on npm whose
// dist.integrity is another tarball's: the run, a dispatched one, stops with the
// checksum line and no fix line, exit 1, before any session, and no version's folder is
// written.
func TestATarballThatDoesNotMatchItsChecksumEndsTheRun(t *testing.T) {
	npm := rt.StartNpm(t)
	other := rt.PlainTarball(t, "other")
	for _, cpu := range []string{"x64", "arm64"} {
		npm.PublishWith(cpu, "2.1.320", rt.PlainTarball(t, "2.1.320"), rt.Integrity(other), "")
	}
	npm.SetLatest("2.1.320")
	r := runInstalling(t, installSetup{npm: npm, proxy: rt.StartProxy(t)})
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	want := "qory run: the tarball of claude 2.1.320 does not match the checksum\n  " + rt.Integrity(other) + "\n"
	if !strings.Contains(r.out, want) || strings.Contains(r.out, "Check the source") {
		t.Fatalf("the output has no checksum line:\n%s", r.out)
	}
	runLinesFit(t, r.out)
	if _, err := os.Lstat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "2.1.320")); err == nil {
		t.Fatal("the version's folder was written")
	}
}

// runCodeInstalling runs qory run from a run code whose file has lines after the code,
// as qory run pool writes it, with proxy as the run's host proxy, and no repositories,
// at the first commit of a version repository, as
// TestARunCodeAtATargetVersionWithNoRepositoryRunsOn runs it.
//
// server is the address that forager.yaml's gateway.server.url gives the run, "" for none.
func runCodeInstalling(t *testing.T, lines string, proxy *rt.Proxy, server string) (string, error) {
	t.Helper()
	defer cmd.SetInstallRuntimes(true)()
	defer cmd.SetRuntimeKeys(rt.Keys())()
	if server != "" {
		defer cmd.SetInstanceAddress(func(*config.Forager) string { return server })()
	}
	defer cmd.SetNpmRegistry(unreachableNpm)()
	h, first, _, files := versionRepository(t)
	for name, content := range files {
		writeFile(t, filepath.Join(h.dir, filepath.FromSlash(name)), content)
	}
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		target := map[string]any{"system": proxygittest.Host, "path": "acme/site"}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = first
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
	})
	defer cmd.SetJoinSession(func(session.SessionSpec) {})()
	t.Setenv("QORY_RUN_KEY", "")
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		Proxy: proxy.URL(), Dir: h.runDir,
		StackProxy: h.server.Proxy.URL, StackCABundle: h.server.Proxy.CABundle,
	})
	defer restore()
	return run(t, "run", "--run-code-file", codeFile(t, g.code()+"\n"+lines), "--gateway", g.URL)
}

// TestAPoolsRunsShareOneRuntimesFolder runs two runs from run codes whose files name the
// pool's registry and runtimes folder, as qory run pool writes them: the first installs
// claude 2.1.320 into that folder, the second reads the registry's newest again and no
// tarball, and neither writes a runtimes folder in its state folder.
func TestAPoolsRunsShareOneRuntimesFolder(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	folder := filepath.Join(t.TempDir(), "runtimes")
	lines := "registry " + reg.Host() + "\napiary " + reg.Instance() + "\nruntimes " + folder + "\n"
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		// Each run sets up its folders from the package's folder.
		t.Chdir(pkg)
		out, err := runCodeInstalling(t, lines, proxy, "")
		if !errors.Is(err, cmd.ErrJoinedNowhere) || !strings.Contains(out, "qory run: installed claude 2.1.320\n") {
			t.Fatalf("run %d: %v\n%s", i+1, err, out)
		}
	}
	if reg.BlobGets() != 1 {
		t.Fatalf("the registry returned %d tarballs, want 1", reg.BlobGets())
	}
	if _, err := os.Lstat(filepath.Join(folder, "claude", "2.1.320", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes")); err == nil {
		t.Fatal("a run wrote a runtimes folder in its state folder")
	}
}

// TestARunCodeFileTakesTheMachineLines reads run code files with the lines qory run
// pool writes after the code, and with lines that qory run refuses as it refuses
// anything else after the code.
func TestARunCodeFileTakesTheMachineLines(t *testing.T) {
	code := newCodeRelay(t).code()
	if _, err := cmd.GiveRunCode(codeFile(t, code+"\nregistry registry.example:5000\nruntimes /var/lib/qory-pool/runtimes\n")); err != nil {
		t.Fatalf("the machine's lines are refused: %v", err)
	}
	for _, rest := range []string{"\nfolder /x\n", "\nruntimes relative\n", "\n\nregistry registry.example\n", "\nregistry registry.example\nregistry registry.example\n",
		"\nregistry registry.example?x=1\n", "\nregistry registry.example#x\n", "\nregistry registry.example%2f\n", "\nregistry user@registry.example\n",
		"\nregistry registry.example/x\n", "\nregistry registry.example \n", "\nregistry registry.example:0\n", "\nregistry registry.example:65536\n"} {
		file := codeFile(t, code+rest)
		_, err := cmd.GiveRunCode(file)
		want := "Write the run code, then at most the lines `registry <host>`, `apiary <url>` and `runtimes <folder>`\nThe file " + file + " contains more than that."
		if err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %s", rest, err, want)
		}
		if first, _, _ := strings.Cut(want, "\n"); len(first) > 100 {
			t.Errorf("the first line has %d characters, want at most 100: %s", len(first), first)
		}
	}
}

// TestARunWithNoAddressOfQoryApiaryStopsAtTheRegistrysRealm runs from a run key, whose
// run has no gateway.server in forager.yaml, with a pool.registry that returns a 401
// challenge: qory has no address of Qory Apiary to check the realm against, so it takes
// no realm, and the run, a dispatched one, prints the cause with no fix line.
func TestARunWithNoAddressOfQoryApiaryStopsAtTheRegistrysRealm(t *testing.T) {
	reg := rt.StartRegistry(t)
	reg.PublishPlain("2.1.320")
	r := runInstalling(t, installSetup{reg: reg, proxy: rt.StartProxy(t), walled: true, noInstance: true})
	want := "qory run: the pool's forager.yaml sets no gateway.server.url,\n  so qory cannot check the registry's token realm " + reg.Instance() + "/registry/token\n"
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 || !strings.Contains(r.out, want) || reg.BlobGets() != 0 {
		t.Fatalf("%d joins, %v (exit %d), %d blobs\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), reg.BlobGets(), r.out)
	}
}

// TestARunCodeFileWithNoApiaryLineStopsAtTheRegistrysRealm runs from a run code file
// whose lines are the registry and the runtimes folder, as a pool without an address of
// Qory Apiary writes them: the run has no server in its forager.yaml, so the install
// stops at the registry's realm with the cause, and no fix line.
func TestARunCodeFileWithNoApiaryLineStopsAtTheRegistrysRealm(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	lines := "registry " + reg.Host() + "\nruntimes " + filepath.Join(t.TempDir(), "runtimes") + "\n"
	_, err := runCodeInstalling(t, lines, proxy, "")
	if err == nil || err.Error() != "the pool's forager.yaml sets no gateway.server.url,\n  so qory cannot check the registry's token realm "+reg.Instance()+"/registry/token; the code is used" || reg.BlobGets() != 0 {
		t.Fatalf("err = %v, %d blobs", err, reg.BlobGets())
	}
}

// TestTheInstanceAddressIsTheServersURL reads the address that a run checks the
// registry's realm against: gateway.server.url, and none without a server.
func TestTheInstanceAddressIsTheServersURL(t *testing.T) {
	if got := cmd.InstanceAddress(&config.Forager{Server: &config.ForagerServer{URL: "https://apiary.example.com"}}); got != "https://apiary.example.com" {
		t.Errorf("the address is %q", got)
	}
	for _, r := range []*config.Forager{nil, {}} {
		if got := cmd.InstanceAddress(r); got != "" {
			t.Errorf("the address is %q, want none", got)
		}
	}
}

// TestTheApiaryLineOfARunCodeFileWinsOverTheServersURL runs from run code files whose
// apiary line and gateway.server.url differ: the instance of the run code's line is the
// one that the registry's realm must be on, so a right line installs though the server's
// URL is another, and a wrong line stops the run though the server's URL is right.
func TestTheApiaryLineOfARunCodeFileWinsOverTheServersURL(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "runtimes")
	lines := func(instance string) string {
		return "registry " + reg.Host() + "\napiary " + instance + "\nruntimes " + folder + "\n"
	}
	out, err := runCodeInstalling(t, lines(reg.Instance()), proxy, "http://127.0.0.1:1")
	if !errors.Is(err, cmd.ErrJoinedNowhere) || !strings.Contains(out, "qory run: installed claude 2.1.320\n") {
		t.Fatalf("a right line: %v\n%s", err, out)
	}
	t.Chdir(pkg)
	_, err = runCodeInstalling(t, lines("http://127.0.0.1:1"), proxy, reg.Instance())
	if err == nil || err.Error() != "the registry's token realm is\n  "+reg.Instance()+"/registry/token,\n  and the instance's token route is http://127.0.0.1:1/registry/token; the code is used" {
		t.Fatalf("a wrong line: %v", err)
	}
}

// runLinesFit fails the test when a line of out that starts with "qory run: " has more
// than 100 characters.
func runLinesFit(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "qory run: ") && len(line) > 100 {
			t.Errorf("the line has %d characters: %s", len(line), line)
		}
	}
}

// endsWithin runs f, which runs a whole qory run, and fails the test when it does not
// return within limit: a run whose install keeps its tries after the run ended.
func endsWithin(t *testing.T, limit time.Duration, f func() installRun) installRun {
	t.Helper()
	done := make(chan installRun, 1)
	go func() { done <- f() }()
	select {
	case r := <-done:
		return r
	case <-time.After(limit):
		t.Fatalf("qory run did not end within %s of the end of its run", limit)
		return installRun{}
	}
}

// pollUntil calls ok every 20 ms until it returns true, and returns true; it returns false
// when stop is closed or ok has not returned true after limit. It takes no *testing.T, so
// a goroutine that outlives its test cannot log to a finished test: the caller's test
// reports the failure.
func pollUntil(limit time.Duration, stop <-chan struct{}, ok func() bool) bool {
	deadline := time.After(limit)
	for !ok() {
		select {
		case <-stop:
			return false
		case <-deadline:
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	return true
}

// TestARegistryThatKeepsReturning503StopsAtTheTimeLimit has the registry return 503 to
// every request, and a run with a time limit of 3 seconds: the run waits with the
// registry's tries, tells so once on stderr, and ends at the limit with the line of a run
// that ran out of time, exit 124, with no line that asks for more time. The run ends
// within 4.5 seconds of the registry's first request. A run that ended at the limit took
// 2.4 to 2.7 seconds from that request in 40 runs, 20 of them with 3 busy processes per
// CPU. A run that kept its tries after the limit would finish the 4-second wait that
// follows the second try, and end 6 seconds after the first request. The time from the
// start of the test is no bound, because the setup of the run's folders and repositories
// adds a time that varies with load.
func TestARegistryThatKeepsReturning503StopsAtTheTimeLimit(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	reg.Unavailable(-1, "")
	first := make(chan time.Time, 1)
	ended := make(chan struct{})
	defer close(ended)
	r := endsWithin(t, 30*time.Second, func() installRun {
		return runInstalling(t, installSetup{reg: reg, npm: rt.StartNpm(t), proxy: proxy, walled: true, args: []string{"--timeout", "3s"}, during: func() {
			if pollUntil(30*time.Second, ended, func() bool { return len(reg.Paths()) > 0 }) {
				first <- time.Now()
			}
		}})
	})
	endedAt := time.Now()
	select {
	case at := <-first:
		if took := endedAt.Sub(at); took > 4500*time.Millisecond {
			t.Errorf("the run ended %s after the registry's first request, with a time limit of 3s", took)
		}
	default:
		t.Error("the registry got no request before the run ended")
	}
	if cmd.ExitCode(r.err) != 124 || len(r.joins) != 0 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "the run was cancelled at its time limit of 3s, before claude started", "qory run: "+reg.Host()+" has not returned Claude Code (503); check pool.registry\n")
	if n := strings.Count(r.out, " has not returned Claude Code"); n != 1 {
		t.Errorf("the run printed the waiting line %d times:\n%s", n, r.out)
	}
	lacks(t, r.out, "Run again with more time", "installed claude")
}

// TestARegistryThatKeepsReturning503StopsAtSIGTERM sends SIGTERM to qory run while the
// registry's tries go on: the tries stop, the run is cancelled with the line of a run
// that got a signal before its session joined, exit 1, and no line asks for more time.
func TestARegistryThatKeepsReturning503StopsAtSIGTERM(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	reg.Unavailable(-1, "")
	ended := make(chan struct{})
	defer close(ended)
	var sent atomic.Bool
	t.Cleanup(func() {
		if !sent.Load() {
			t.Error("SIGTERM was not sent: the registry got no third /v2/ request before the run ended, or within 30s")
		}
	})
	r := endsWithin(t, 30*time.Second, func() installRun {
		return runInstalling(t, installSetup{reg: reg, npm: rt.StartNpm(t), proxy: proxy, walled: true, during: func() {
			// The first try is 3 requests: newest with no token, which gets 401, the token,
			// and newest with it, which gets 503. The third request to /v2/ is the first
			// retry, 2 seconds later, and qory prints the waiting line before it sends it.
			// With no retry in 30 seconds, or before the run ends, the run gets no SIGTERM
			// and the cleanup above fails the test.
			if !pollUntil(30*time.Second, ended, func() bool {
				n := 0
				for _, p := range reg.Paths() {
					if strings.HasPrefix(p, "/v2/") {
						n++
					}
				}
				return n >= 3
			}) {
				return
			}
			sent.Store(true)
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}})
	})
	if cmd.ExitCode(r.err) != 1 || len(r.joins) != 0 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: "+reg.Host()+" has not returned Claude Code", "the run was cancelled: qory run got SIGTERM before claude started")
	lacks(t, r.out, "Run again with more time", "installed claude")
}

// oldVersion makes the folder of claude 1.0.0 in qory's runtimes folder, as an install
// makes it, with no hold on it, and returns its path.
func oldVersion(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "1.0.0")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bin", "claude"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "bin", "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "integrity"), "sha512-x\n")
	return dir
}

// engineOnPath puts a program called docker first on PATH for the test. Its ps prints a
// container id, its inspect prints source as the container's mount, and it writes the
// mount.
func engineOnPath(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "docker"), "#!/bin/sh\ncase \"$1\" in\nps) echo abc123 ;;\ninspect) echo '"+source+"' ;;\nesac\n")
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestAnOrphanContainerKeepsItsVersionWithNoWall has a run with no wall in its
// forager.yaml, as a person's machine has, install claude 2.1.320 while the engine, the
// docker program on PATH, lists a running container, left by a run that ended, that
// mounts the folder of 1.0.0: 1.0.0 stays. When the container mounts another folder, the
// install removes 1.0.0.
func TestAnOrphanContainerKeepsItsVersionWithNoWall(t *testing.T) {
	for _, c := range []struct {
		name  string
		mount func(old string) string
		kept  bool
	}{
		{"a container that mounts the old version", func(old string) string { return old }, true},
		{"a container that mounts another folder", func(string) string { return "/srv/other" }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			npm, proxy := rt.StartNpm(t), rt.StartProxy(t)
			npm.Publish("2.1.320")
			var old string
			r := runInstalling(t, installSetup{npm: npm, proxy: proxy, before: func() {
				old = oldVersion(t)
				engineOnPath(t, c.mount(old))
			}})
			r.joined(t)
			if _, err := os.Lstat(old); (err == nil) != c.kept {
				t.Errorf("1.0.0 is kept: %v, want %v (%v)", err == nil, c.kept, err)
			}
		})
	}
}

// signatureCases are the npm signatures of claude 2.1.320 that stop an install, with the
// cause of each, and the fix line that a person's run prints before it. The cause's first
// line has no indent here; a person's run indents it two spaces.
var signatureCases = []struct {
	name, fix, cause string
	sign             func(signed, integrity string) any
}{
	{"no signature", "Check the source of claude's releases.", "npm has no signature of claude 2.1.320.",
		func(string, string) any { return nil }},
	{"a key qory does not have", "Update qory.", "npm signed claude 2.1.320 with a key that this qory does not have.\n  The key is SHA256:another.",
		func(s, i string) any {
			return []map[string]string{{"keyid": "SHA256:another", "sig": rt.Sign(s + ":" + i)}}
		}},
}

// TestADispatchedRunShowsOnlyTheCauseOfAFailedInstall runs claude with a run key, whose
// output Qory Apiary shows, on an npm whose signature of claude 2.1.320 stops the
// install: qory run prints the cause, its first letter in lower case, with no fix line,
// and exits 1 with no session.
func TestADispatchedRunShowsOnlyTheCauseOfAFailedInstall(t *testing.T) {
	for _, c := range signatureCases {
		t.Run(c.name, func(t *testing.T) {
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			npm.SignWith(c.sign)
			r := runInstalling(t, installSetup{npm: npm, proxy: rt.StartProxy(t)})
			if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			cause := strings.TrimSuffix(c.cause, ".")
			wants(t, r.out, "qory run: "+strings.ToLower(cause[:1])+cause[1:]+"\n")
			lacks(t, r.out, c.fix)
		})
	}
}

// TestADispatchedRunShowsTheCauseOfAOneLineInstallError runs claude with a run key on
// installs that stop on a state folder qory cannot write, a host proxy's CA bundle with
// no certificate, and a host proxy that does not open. Each error has a fix line and a
// cause line, so qory run prints the cause alone, with no fix line. The error of the
// state folder has the folder's path on its line, which is long in a test's folder.
func TestADispatchedRunShowsTheCauseOfAOneLineInstallError(t *testing.T) {
	state := func() string { return filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes") }
	bundle := filepath.Join(t.TempDir(), "ca.pem")
	writeFile(t, bundle, "no certificate\n")
	for _, c := range []struct {
		name string
		s    installSetup
		want func() string
		fix  string
		fits bool
	}{
		{"a state folder qory cannot write", installSetup{before: func() {
			if err := os.MkdirAll(state(), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(state(), 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(state(), 0o755) })
		}}, func() string { return "qory run: mkdir " + filepath.Join(state(), "claude") + ": permission denied\n" }, "Check that qory can write", false},
		{"a CA bundle with no certificate", installSetup{caBundle: bundle},
			func() string { return "qory run: the run's CA bundle\n  " + bundle + "\n  has no certificate\n" }, "Run again", true},
		{"a host proxy that does not open", installSetup{proxyErr: errors.New("the run's session is joining")},
			func() string {
				return "qory run: the run's host proxy, through which qory installs claude, did not open:\n  the run's session is joining\n"
			}, "Run again", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.s.before != nil && os.Geteuid() == 0 {
				t.Skip("root writes a folder of mode 0555")
			}
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			c.s.npm, c.s.proxy = npm, rt.StartProxy(t)
			r := runInstalling(t, c.s)
			if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out, c.want())
			lacks(t, r.out, c.fix)
			if c.fits {
				runLinesFit(t, r.out)
			}
		})
	}
}

// TestAPersonsRunShowsTheFixAndTheCauseOfAFailedInstall runs claude in a checkout on a
// person's machine, with no run key, on an npm whose signature of claude 2.1.320 stops
// the install: qory run prints the fix line, then the cause on indented lines.
func TestAPersonsRunShowsTheFixAndTheCauseOfAFailedInstall(t *testing.T) {
	for _, c := range signatureCases {
		t.Run(c.name, func(t *testing.T) {
			defer cmd.SetInstallRuntimes(true)()
			defer cmd.SetRuntimeKeys(rt.Keys())()
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			npm.SignWith(c.sign)
			defer cmd.SetNpmRegistry(npm.URL())()
			root := newCheckout(t)
			copyFixture(t, "two-modules", root)
			composedForFake(t, root, fakeRuntime(t))
			t.Setenv("QORY_RUN_KEY", "")
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err == nil || cmd.ExitCode(err) != 1 {
				t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
			}
			wants(t, out+err.Error(), c.fix+"\n  "+c.cause)
		})
	}
}
