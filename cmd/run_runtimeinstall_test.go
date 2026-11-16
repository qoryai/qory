package cmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/cmd"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// installSource makes src the source of qory run's runtime releases until the test
// ends.
func installSource(t *testing.T, src *rt.Source) {
	t.Helper()
	t.Cleanup(cmd.SetFetchRuntime(src.Fetch))
}

// installedProgram is the program of claude 2.1.320 in qory's state folder.
func installedProgram() string {
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes", "claude", "2.1.320", "bin", "claude")
}

// TestAWalledRunInstallsItsRuntimeBeforeTheImage runs a walled run of claude with a
// source of claude 2.1.320: it prints "qory run: installed claude 2.1.320" once, and
// the program is at <XDG_STATE_HOME>/qory/runtimes/claude/2.1.320/bin/claude, mode
// 0755, before the image's fetch. The reuse of a version's folder by a later run is
// in the tests of internal/runtimeinstall: each run here has a new state folder.
func TestAWalledRunInstallsItsRuntimeBeforeTheImage(t *testing.T) {
	src := &rt.Source{}
	src.Publish("2.1.320", rt.PlainTarball(t, "2.1.320"))
	installSource(t, src)
	installedFirst := false
	r := runFetching(t, "", func(context.Context) error {
		info, err := os.Lstat(installedProgram())
		installedFirst = err == nil && info.Mode() == 0o755
		return nil
	})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.joins) != 1 {
		t.Fatalf("%d joins, %v\n%s", len(r.joins), r.err, r.out)
	}
	if !installedFirst {
		t.Fatal("the run fetched its image before the program was installed, mode 0755")
	}
	if n := strings.Count(r.out, "qory run: installed claude 2.1.320\n"); n != 1 {
		t.Fatalf("the run prints the install line %d times:\n%s", n, r.out)
	}
	if got := src.Runtimes(); !slices.Equal(got, []string{"claude"}) || src.Opens() != 1 {
		t.Fatalf("fetched %q and opened %d tarballs, want claude once and 1", got, src.Opens())
	}
}

// TestAFetchErrorEndsTheRunWithItsFix has the source fail: the run prints the fetch's
// error, whose first line is the fix, closes failed with -1 before the image's fetch,
// and no session joins.
func TestAFetchErrorEndsTheRunWithItsFix(t *testing.T) {
	src := &rt.Source{}
	src.Fail(errors.New("check that the run reaches the runtime cache, then run again\nThe cache has never held claude."))
	installSource(t, src)
	r := runFetching(t, "", func(context.Context) error { return nil })
	if len(r.joins) != 0 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	if !strings.Contains(r.out, "qory run: check that the run reaches the runtime cache, then run again\nThe cache has never held claude.\n") {
		t.Fatalf("the output has no fix-first line:\n%s", r.out)
	}
	if slices.Contains(r.calls, "fetch image") {
		t.Errorf("the calls: %v, want no image fetch", r.calls)
	}
	if len(r.closes) != 1 || r.closes[0].State != event.StateFailed || r.closes[0].ExitCode != -1 {
		t.Errorf("the closes: %+v, want one failed with -1", r.closes)
	}
}

// TestARunWithNoSourceInstallsNothing has no source set: a walled run of claude
// prints no install line and writes no runtimes folder.
func TestARunWithNoSourceInstallsNothing(t *testing.T) {
	r := runFetching(t, "", func(context.Context) error { return nil })
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || strings.Contains(r.out, "installed claude") {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes")); err == nil {
		t.Fatal("a runtimes folder was written")
	}
}
