package cmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// installRun is what a walled run of claude with pool.registry set came to.
type installRun struct {
	out          string
	err          error
	joins        []session.SessionSpec
	closes       []session.Outcome
	calls        []string
	installFirst bool
}

// runInstalling runs qory run claude with a run key behind the fake wall, with no
// repositories, pool.registry under reg's host when reg is not nil, and proxy as the
// run's host proxy. The image's fetch records whether the program of claude 2.1.320 was
// in qory's state folder by then, mode 0755.
func runInstalling(t *testing.T, reg *rt.Registry, proxy *rt.Proxy) installRun {
	t.Helper()
	h := newHostFetch(t, "", "main")
	docker, _ := fakeDocker(t)
	section := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	if reg != nil {
		section += "pool:\n  registry: " + reg.Host() + "/qory-env\n  run_config: " + t.TempDir() + "\n"
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), section)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	var r installRun
	program := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "2.1.320", "bin", "claude")
	defer cmd.SetJoinSession(func(s session.SessionSpec) { r.joins = append(r.joins, s) })()
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Proxy: proxy.URL(), Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		FetchImage: func(context.Context) error {
			info, err := os.Lstat(program)
			r.installFirst = err == nil && info.Mode() == 0o755
			return nil
		},
	})
	defer restore()
	writeFile(t, filepath.Join(h.dir, "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: \""+h.server.URL("acme/harness")+"\", ref: main, path: modules/core}\n"))
	r.out, r.err = run(t, "run", "claude", "--home", t.TempDir(), "--run-key-fd", descriptor(t, key))
	r.closes, r.calls = closes(), calls()
	return r
}

// TestAWalledRunInstallsItsRuntimeFromTheRegistry runs a walled run of claude on a
// machine whose forager.yaml sets pool.registry: qory reads claude 2.1.320 from the
// registry through the run's host proxy, prints "qory run: installed claude 2.1.320"
// once, and the program is in qory's state folder, mode 0755, before the image's
// fetch. The reuse of a version's folder by a later run is in the tests of
// internal/runtimeinstall: each run here has a new state folder.
func TestAWalledRunInstallsItsRuntimeFromTheRegistry(t *testing.T) {
	reg := rt.StartRegistry(t)
	reg.PublishPlain("2.1.320")
	proxy := rt.StartProxy(t)
	r := runInstalling(t, reg, proxy)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	if !r.installFirst {
		t.Fatal("the run fetched its image before the program was installed, mode 0755")
	}
	if n := strings.Count(r.out, "qory run: installed claude 2.1.320\n"); n != 1 {
		t.Fatalf("the run prints the install line %d times:\n%s", n, r.out)
	}
	if reg.BlobGets() != 1 || proxy.Passed() == 0 || proxy.Refusals() != 0 {
		t.Fatalf("%d blobs; the proxy passed %d and refused %d", reg.BlobGets(), proxy.Passed(), proxy.Refusals())
	}
	lacks(t, r.out, rt.ProxyPassword)
}

// TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsFix has an empty registry: the run
// prints the fix first, closes failed with -1 before the image's fetch, no session
// joins, and qory run exits 1.
func TestARuntimeTheRegistryNeverHeldEndsTheRunWithItsFix(t *testing.T) {
	r := runInstalling(t, rt.StartRegistry(t), rt.StartProxy(t))
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	want := "qory run: check that the instance's registry reaches the publisher of claude, then run again\n" +
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

// TestARunWithNoRegistryInstallsNothing has no pool.registry in forager.yaml: a walled
// run of claude prints no install line, opens no host proxy, and writes no runtimes
// folder.
func TestARunWithNoRegistryInstallsNothing(t *testing.T) {
	r := runInstalling(t, nil, rt.StartProxy(t))
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || strings.Contains(r.out, "installed claude") {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	for _, c := range r.calls {
		if c == "host proxy" {
			t.Errorf("the calls: %v, want no host proxy", r.calls)
		}
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes")); err == nil {
		t.Fatal("a runtimes folder was written")
	}
}
