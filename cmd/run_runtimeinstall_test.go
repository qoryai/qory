package cmd_test

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	"github.com/qoryai/qory/internal/compose"
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
	// runtime is the runtime of the run; empty means claude.
	runtime string
	// args are flags that follow the run key's.
	args []string
	// during, when set, runs in its own goroutine while the run goes on.
	during func()
	// before, when set, runs once the test's folders are made, before the run.
	before func()
	// fetchImage, when set, is the image's fetch, after the record of installFirst.
	fetchImage func(context.Context) error
}

// runInstalling runs qory run claude with a run key as s sets it up, with no
// repositories. The image's
// fetch, which only a walled run has, records whether the program of claude 2.1.320 was
// in qory's state folder by then, mode 0755.
func runInstalling(t *testing.T, s installSetup) installRun {
	t.Helper()
	defer cmd.SetInstallRuntimes(true)()
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
	program := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "2.1.320", "bin", "claude")
	defer cmd.SetJoinSession(func(s session.SessionSpec) { r.joins = append(r.joins, s) })()
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Proxy: s.proxy.URL(), Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		// A push proxy that takes no connection: the install reads through the host proxy.
		PushProxy: "http://qory:nowhere@127.0.0.1:9",
		FetchImage: func(ctx context.Context) error {
			info, err := os.Lstat(program)
			r.installFirst = err == nil && info.Mode() == 0o755
			if s.fetchImage != nil {
				return s.fetchImage(ctx)
			}
			return nil
		},
	})
	defer restore()
	writeFile(t, filepath.Join(h.dir, "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: "+runtime+"\nmodules:\n  - name: core\n    source: {git: \""+h.server.URL("acme/harness")+"\", ref: main, path: modules/core}\n"))
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

// TestACodexSessionStartsFromTheImage has a run of codex join with no RuntimeInstall
// and with the Command of its launch: qory installs no program of codex.
func TestACodexSessionStartsFromTheImage(t *testing.T) {
	r := runInstalling(t, installSetup{proxy: rt.StartProxy(t), walled: true, runtime: "codex"})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	if r.joins[0].RuntimeInstall != nil || strings.Contains(startedProgram(r.joins[0]), "runtimes") {
		t.Fatalf("the session has RuntimeInstall %+v and Command %q, want none of the installed program", r.joins[0].RuntimeInstall, startedProgram(r.joins[0]))
	}
}

// TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsFix has an empty registry: the run
// prints the fix first, closes failed with -1 before the image's fetch, no session
// joins, and qory run exits 1.
func TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsFix(t *testing.T) {
	r := runInstalling(t, installSetup{reg: rt.StartRegistry(t), proxy: rt.StartProxy(t), walled: true})
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	want := "qory run: Check that the instance's registry reaches the publisher of claude, then run again\n" +
		"The registry has never held claude and could not fetch it: shared/runtimes/claude:newest returned 404.\n"
	if !strings.Contains(r.out, want) {
		t.Fatalf("the output has no fix-first line:\n%s", r.out)
	}
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
// dist.integrity is another tarball's: the run stops with the checksum line, exit 1,
// before any session, and no version's folder is written.
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
	want := "qory run: Check the source of the runtime's releases\nThe tarball of claude 2.1.320 does not match the checksum " + rt.Integrity(other)
	if !strings.Contains(r.out, want) {
		t.Fatalf("the output has no checksum line:\n%s", r.out)
	}
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
// no realm, and the run's first line is the fix.
func TestARunWithNoAddressOfQoryApiaryStopsAtTheRegistrysRealm(t *testing.T) {
	reg := rt.StartRegistry(t)
	reg.PublishPlain("2.1.320")
	r := runInstalling(t, installSetup{reg: reg, proxy: rt.StartProxy(t), walled: true, noInstance: true})
	want := "qory run: Set gateway.server.url in forager.yaml to the address of Qory Apiary\n" +
		"The realm is " + reg.Instance() + "/registry/token, and the run has no address of Qory Apiary to check it against.\n"
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 || !strings.Contains(r.out, want) || reg.BlobGets() != 0 {
		t.Fatalf("%d joins, %v (exit %d), %d blobs\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), reg.BlobGets(), r.out)
	}
}

// TestARunCodeFileWithNoApiaryLineStopsAtTheRegistrysRealm runs from a run code file
// whose lines are the registry and the runtimes folder, as a pool without an address of
// Qory Apiary writes them: the run has no server in its forager.yaml, so the install
// stops at the registry's realm with the fix first.
func TestARunCodeFileWithNoApiaryLineStopsAtTheRegistrysRealm(t *testing.T) {
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	lines := "registry " + reg.Host() + "\nruntimes " + filepath.Join(t.TempDir(), "runtimes") + "\n"
	_, err := runCodeInstalling(t, lines, proxy, "")
	if err == nil || !strings.HasPrefix(err.Error(), "Set gateway.server.url in forager.yaml to the address of Qory Apiary\n") || reg.BlobGets() != 0 {
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
	if err == nil || !strings.HasPrefix(err.Error(), "Ask Qory Apiary's instance admin to put the registry's token realm at http://127.0.0.1:1/registry/token\n") {
		t.Fatalf("a wrong line: %v", err)
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

// TestARegistryThatKeepsReturning503StopsAtTheSetupLimit has the registry return 503 to
// every request, and a run whose setup has a limit of 3 seconds: the run waits with the
// registry's tries, tells so once on stderr, and ends at the limit with the line of a
// setup past its limit, which names the install, exit 124, with no line that asks for
// more time. The run's --timeout of 1h, which counts from the agent's start, does not
// end it. The run ends within 4.5 seconds of the registry's first request. A run that
// ended at the limit took 2.4 to 2.7 seconds from that request in 40 runs, 20 of them
// with 3 busy processes per CPU. A run that kept its tries after the limit would finish
// the 4-second wait that follows the second try, and end 6 seconds after the first
// request. The time from the start of the test is no bound, because the setup of the
// run's folders and repositories adds a time that varies with load.
func TestARegistryThatKeepsReturning503StopsAtTheSetupLimit(t *testing.T) {
	defer cmd.SetSetupLimit(3 * time.Second)()
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	reg.Unavailable(-1, "")
	first := make(chan time.Time, 1)
	ended := make(chan struct{})
	defer close(ended)
	r := endsWithin(t, 30*time.Second, func() installRun {
		return runInstalling(t, installSetup{reg: reg, npm: rt.StartNpm(t), proxy: proxy, walled: true, args: []string{"--timeout", "1h"}, during: func() {
			if pollUntil(30*time.Second, ended, func() bool { return len(reg.Paths()) > 0 }) {
				first <- time.Now()
			}
		}})
	})
	endedAt := time.Now()
	select {
	case at := <-first:
		if took := endedAt.Sub(at); took > 4500*time.Millisecond {
			t.Errorf("the run ended %s after the registry's first request, with a setup limit of 3s", took)
		}
	default:
		t.Error("the registry got no request before the run ended")
	}
	if cmd.ExitCode(r.err) != 124 || len(r.joins) != 0 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "✗ the run was cancelled during the install of the runtime:\n  the steps before claude starts may take at most 3s\n", "qory run: "+reg.Host()+" has not returned Claude Code (503); check pool.registry\n")
	if len(r.closes) != 1 || r.closes[0].State != event.StateCancelled || r.closes[0].Reason != event.ReasonTimeout || r.closes[0].ExitCode != 124 {
		t.Errorf("the closes: %+v, want one cancelled with timeout and 124", r.closes)
	}
	if n := strings.Count(r.out, " has not returned Claude Code"); n != 1 {
		t.Errorf("the run printed the waiting line %d times:\n%s", n, r.out)
	}
	lacks(t, r.out, "Run again with more time", "installed claude")
}

// TestAnImageFetchPastTheSetupLimitEndsTheRun is a dispatched walled run whose image's
// fetch waits until its context ends, with a setup limit of 5 seconds, which leaves the
// steps before the fetch time under load, and a --timeout of 1h: the fetch ends at the
// setup's limit, the run closes cancelled with timeout and 124, no session joins, and the
// line is the cause alone, which names the image's fetch.
func TestAnImageFetchPastTheSetupLimitEndsTheRun(t *testing.T) {
	defer cmd.SetSetupLimit(5 * time.Second)()
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	var cut error
	r := endsWithin(t, 30*time.Second, func() installRun {
		return runInstalling(t, installSetup{reg: reg, npm: rt.StartNpm(t), proxy: proxy, walled: true, args: []string{"--timeout", "1h"}, fetchImage: func(ctx context.Context) error {
			<-ctx.Done()
			cut = context.Cause(ctx)
			return ctx.Err()
		}})
	})
	if cmd.ExitCode(r.err) != 124 || len(r.joins) != 0 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	if !errors.Is(cut, context.DeadlineExceeded) {
		t.Errorf("the image's fetch ended with %v, want the setup's deadline", cut)
	}
	wants(t, r.out, "✗ the run was cancelled during the fetch of the run's image:\n  the steps before claude starts may take at most 5s\nqory run: the record is in ")
	lacks(t, r.out, "pull the run's image", "Qory Apiary")
	if len(r.closes) != 1 || r.closes[0].State != event.StateCancelled || r.closes[0].Reason != event.ReasonTimeout || r.closes[0].ExitCode != 124 {
		t.Errorf("the closes: %+v, want one cancelled with timeout and 124", r.closes)
	}
}

// TestAStepThatRunsPastTheSetupLimitIsTheOneNamed is a dispatched walled run whose read of
// the push checks takes 200ms past a setup limit of 5 seconds and then returns none, as a
// compose runs to its end past the limit: the copy after it fails at once, and the line
// names the push checks, the step that ran past the limit, not the copy.
func TestAStepThatRunsPastTheSetupLimitIsTheOneNamed(t *testing.T) {
	const limit = 5 * time.Second
	defer cmd.SetSetupLimit(limit)()
	defer cmd.SetLoadPushChecks(func(string) ([]compose.PushCheck, error) {
		time.Sleep(limit + 200*time.Millisecond)
		return nil, nil
	})()
	reg, proxy := rt.StartRegistry(t), rt.StartProxy(t)
	reg.PublishPlain("2.1.320")
	r := endsWithin(t, 30*time.Second, func() installRun {
		return runInstalling(t, installSetup{reg: reg, npm: rt.StartNpm(t), proxy: proxy, walled: true})
	})
	if cmd.ExitCode(r.err) != 124 || len(r.joins) != 0 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "✗ the run was cancelled during the load of the push checks:\n  the steps before claude starts may take at most 5s\n")
	lacks(t, r.out, "during the copy")
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
