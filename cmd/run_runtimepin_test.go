package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// pinCase is a runtime and the source a run installs it from, npm or the registry's
// runtime cache, with an older version and the newest, which is latest on npm and newest
// in the registry.
type pinCase struct {
	name, runtime, older, newest string
	registry                     bool
}

// pinCases are claude and codex, each from npm and from the registry's runtime cache.
var pinCases = []pinCase{
	{"claude from npm", "claude", "2.1.300", "2.1.320", false},
	{"claude from the registry", "claude", "2.1.300", "2.1.320", true},
	{"codex from npm", "codex", "0.167.2", "0.170.0", false},
	{"codex from the registry", "codex", "0.167.2", "0.170.0", true},
}

// setup is a walled dispatched run of c's runtime with the stack's runtimes key, from a
// source that has c's older and newest versions, as [runInstalling] runs it.
func (c pinCase) setup(t *testing.T, runtimes, version string) installSetup {
	s := installSetup{proxy: rt.StartProxy(t), walled: true, runtime: c.runtime, version: version, runtimes: runtimes}
	if c.registry {
		start := rt.StartRegistry
		if c.runtime == "codex" {
			start = rt.StartCodexRegistry
		}
		s.reg = start(t)
		s.reg.PublishPlain(c.older)
		s.reg.PublishPlain(c.newest)
		return s
	}
	start := rt.StartNpm
	if c.runtime == "codex" {
		start = rt.StartCodexNpm
	}
	s.npm = start(t)
	s.npm.Publish(c.older)
	s.npm.Publish(c.newest)
	return s
}

// pinOf is the runtimes key of a stack that pins runtime to version.
func pinOf(runtime, version string) string {
	return "runtimes:\n  " + runtime + ":\n    version: " + version + "\n"
}

// TestAStackPinsTheVersionTheRunInstalls runs claude and codex, from npm and from the
// registry's runtime cache, with a stack that pins the older of two versions: the run
// installs that version, prints its install line, and the session joins with it. A stack
// with no runtimes key installs the newest.
func TestAStackPinsTheVersionTheRunInstalls(t *testing.T) {
	for _, c := range pinCases {
		for _, pinned := range []bool{true, false} {
			want, runtimes := c.newest, ""
			if pinned {
				want, runtimes = c.older, pinOf(c.runtime, c.older)
			}
			t.Run(c.name+" "+want, func(t *testing.T) {
				r := runInstalling(t, c.setup(t, runtimes, want))
				if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
					t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
				}
				wants(t, r.out, "qory run: installed "+c.runtime+" "+want+"\n")
				if in := r.joins[0].RuntimeInstall; in == nil || in.Version != want || !r.installFirst {
					t.Fatalf("the session's RuntimeInstall is %+v, installed first %v, want %s", in, r.installFirst, want)
				}
			})
		}
	}
}

// TestAPinTheRegistryDoesNotHaveStopsADispatchedRun pins claude and codex to 9.9.9, which
// neither npm nor the registry's runtime cache has: the dispatched run prints the cause
// alone, with no fix line, and stops with no session.
func TestAPinTheRegistryDoesNotHaveStopsADispatchedRun(t *testing.T) {
	for _, c := range pinCases {
		t.Run(c.name, func(t *testing.T) {
			r := runInstalling(t, c.setup(t, pinOf(c.runtime, "9.9.9"), "9.9.9"))
			if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out, "qory run: the registry has no "+c.runtime+" 9.9.9\n")
			lacks(t, r.out, "Set runtimes.")
			runLinesFit(t, r.out)
		})
	}
}

// TestAPinTheRegistryDoesNotHaveStopsAPersonsRun runs claude in a checkout on a person's
// machine, with no run key, from npm, which has only claude 2.1.320. A pin of 9.9.9 in the
// checkout's qory.yaml, and a pin of 2.1.300 in the base stack that the checkout's
// qory.yaml extends, each stop the run before the session: qory run prints the fix line,
// with the name of the file that pins the version, then the cause.
func TestAPinTheRegistryDoesNotHaveStopsAPersonsRun(t *testing.T) {
	for _, c := range []struct {
		name, fixture, version, file string
		pin                          string
	}{
		{"a pin in the checkout's qory.yaml", "two-modules", "9.9.9", "qory.yaml", "  runtimes:\n    claude:\n      version: 9.9.9\n"},
		{"a pin in the base stack", "runtimes-nearer-layer-wins", "2.1.300", "qory-stack.yaml", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer cmd.SetInstallRuntimes(true)()
			defer cmd.SetRuntimeKeys(rt.Keys())()
			npm := rt.StartNpm(t)
			npm.Publish("2.1.320")
			defer cmd.SetNpmRegistry(npm.URL())()
			root := newCheckout(t)
			copyFixture(t, c.fixture, root)
			if c.pin != "" {
				doc, err := os.ReadFile(filepath.Join(root, "qory.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(root, "qory.yaml"), string(doc)+c.pin)
			}
			composedForFake(t, root, fakeRuntime(t))
			t.Setenv("QORY_RUN_KEY", "")
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err == nil || cmd.ExitCode(err) != 1 {
				t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
			}
			want := "Set runtimes.claude.version in " + c.file + " to a version the registry has.\n" +
				"  The registry has no claude " + c.version + "."
			wants(t, out+err.Error(), want)
			if first, _, _ := strings.Cut(want, "\n"); len("qory run: "+first) > 100 {
				t.Errorf("the fix line has %d characters with qory run:, over 100", len("qory run: "+first))
			}
			lacks(t, out, "hello from")
		})
	}
}

// TestAPinInTheUsersFileOrAnAncestorsStopsTheCompose pins claude in the user's qory.yaml,
// then in the qory.yaml of the checkout's parent folder: qory harness compose stops on
// each with the fix first and the file last, and composes once the pin is gone.
func TestAPinInTheUsersFileOrAnAncestorsStopsTheCompose(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	for _, path := range []string{user, filepath.Join(filepath.Dir(root), "qory.yaml")} {
		writeFile(t, path, "apiVersion: qory.dev/v1alpha1\nharness:\n  runtimes:\n    claude:\n      version: 2.1.300\n")
		out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links")
		if err == nil {
			t.Fatalf("%s: the compose passed\n%s", path, out)
		}
		wants(t, out+err.Error(), "Move runtimes to the checkout's qory.yaml or to the base qory-stack.yaml.\n"+
			"  Only the checkout's qory.yaml and the base stack pin a runtime's version.\n"+
			"  harness.runtimes is in "+path+".")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
