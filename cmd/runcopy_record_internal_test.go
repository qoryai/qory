package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/runcopy"
)

// recordCopy is a copy of a run whose setup made a repository at tool, as [runCopy.recordSetup]
// has it before [runCopy.recordRepositories].
func recordCopy(t *testing.T) *runCopy {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	dir, err := runcopy.New("run-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir.Work(), "tool", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	repos := []runcopy.Repository{{Base: strings.Repeat("a", 40)}}
	out := map[string]runcopy.LeftOut{"": {Setup: []runcopy.SetupPath{{Path: "a.txt", State: "missing"}, {Path: "tool", State: runcopy.StateRepo}}}}
	return &runCopy{dir: dir, repos: repos, out: out}
}

// recordSpec is the run and the spec of a run behind a wall with limits.
func recordSpec() (*nowhere, *runSpec) {
	run := &nowhere{image: session.Image{Ref: "registry.example/agent@sha256:" + strings.Repeat("a", 64), Runtime: "runsc"}}
	spec := &runSpec{}
	spec.RunID = "run-01"
	spec.Wall = &wall.Docker{Command: "/usr/local/bin/docker", User: "1001:1001"}
	spec.Limits = wall.Limits{CPUs: "1.5", Memory: "2g", PIDs: 512}
	return run, spec
}

// TestTheRecordContainerIsAGitContainer is the record of a repository that the setup made: it runs
// in a container named qory-record-<run id>, with the flags of the git container, the copy
// read-only and the record's folder read-write, within [runcopy.SessionBase] for a small copy. The
// state it writes is the repository's state.
func TestTheRecordContainerIsAGitContainer(t *testing.T) {
	c := recordCopy(t)
	run, spec := recordSpec()
	state := "repo:" + strings.Repeat("b", 40)
	var args []string
	before := runRecordContainer
	t.Cleanup(func() { runRecordContainer = before })
	runRecordContainer = func(ctx context.Context, rc gitContainer) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > runcopy.SessionBase {
			t.Errorf("the record's limit is %v, want at most %s", time.Until(deadline), runcopy.SessionBase)
		}
		var err error
		if args, err = rc.args(); err != nil {
			t.Fatal(err)
		}
		return 0, os.WriteFile(filepath.Join(c.dir.SetupRecord(), "0-0.state"), []byte(state+"\n"), 0o600)
	}
	if err := c.recordRepositories(t.Context(), run, spec); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	want := "run --rm --pull never --network none --cap-drop ALL --security-opt no-new-privileges --init --no-healthcheck" +
		" --user 1001:1001 --label dev.qory.run=run-01 --name qory-record-run-01 --runtime runsc --pids-limit 4096 --memory 2g --cpus 1.5" +
		" --mount type=bind,src=" + c.dir.Work() + ",dst=" + c.dir.Work() + ",readonly" +
		" --mount type=bind,src=" + c.dir.SetupRecord() + ",dst=" + c.dir.SetupRecord() +
		" --env PATH="
	if !strings.HasPrefix(got, want) {
		t.Errorf("docker %s\nwant   %s…", got, want)
	}
	if !slices.Contains(args, run.image.Ref) {
		t.Errorf("the record does not run the run's image: %s", got)
	}
	if setup := c.out[""].Setup; setup[0].State != "missing" || setup[1].State != state {
		t.Errorf("the setup's paths are %v", setup)
	}
}

// TestTheTimeLimitOfTheRecordGrowsWithTheCopy is a setup that left 20,000 more files in the copy:
// the record container has a minute more, as the git session has.
func TestTheTimeLimitOfTheRecordGrowsWithTheCopy(t *testing.T) {
	c := recordCopy(t)
	run, spec := recordSpec()
	many := filepath.Join(c.dir.Work(), "many")
	if err := os.Mkdir(many, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 20_000 {
		if err := os.WriteFile(filepath.Join(many, strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := runRecordContainer
	t.Cleanup(func() { runRecordContainer = before })
	ran := false
	runRecordContainer = func(ctx context.Context, rc gitContainer) (int, error) {
		ran = true
		if at, ok := ctx.Deadline(); !ok || time.Until(at) <= runcopy.SessionBase || time.Until(at) > runcopy.SessionBase+time.Minute {
			t.Errorf("the record has until %s, %v, want a minute past %s", at, ok, runcopy.SessionBase)
		}
		return 1, nil
	}
	if err := c.recordRepositories(t.Context(), run, spec); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("no record container started")
	}
}

// TestARecordContainerThatFailsMakesTheRepositoryOther is a record container that does not start,
// exits with an error or runs past its limit: the repository that the setup made is other, so the
// git session keeps the copy for it, and the record returns no error. A stop that ends the run's
// context is the error.
func TestARecordContainerThatFailsMakesTheRepositoryOther(t *testing.T) {
	for _, f := range []struct {
		name string
		end  func(ctx context.Context) (int, error)
	}{
		{"no start", func(context.Context) (int, error) { return -1, errors.New("docker: no such image") }},
		{"an exit of 1", func(context.Context) (int, error) { return 1, nil }},
		{"its limit", func(context.Context) (int, error) { return -1, context.DeadlineExceeded }},
	} {
		t.Run(f.name, func(t *testing.T) {
			c := recordCopy(t)
			run, spec := recordSpec()
			before := runRecordContainer
			t.Cleanup(func() { runRecordContainer = before })
			runRecordContainer = func(ctx context.Context, rc gitContainer) (int, error) {
				// A state the session wrote before it failed is not used.
				if err := os.WriteFile(filepath.Join(c.dir.SetupRecord(), "0-0.state"), []byte("repo:"+strings.Repeat("b", 40)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return f.end(ctx)
			}
			if err := c.recordRepositories(t.Context(), run, spec); err != nil {
				t.Fatal(err)
			}
			if setup := c.out[""].Setup; setup[1].State != "other" {
				t.Errorf("the setup's paths are %v", setup)
			}
		})
	}
	c := recordCopy(t)
	run, spec := recordSpec()
	before := runRecordContainer
	t.Cleanup(func() { runRecordContainer = before })
	ctx, cancel := context.WithCancel(t.Context())
	runRecordContainer = func(context.Context, gitContainer) (int, error) {
		cancel()
		return -1, context.Canceled
	}
	if err := c.recordRepositories(ctx, run, spec); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped record returns %v", err)
	}
}

// TestASetupWithNoRepositoryStartsNoRecordContainer is a setup that made no repository: no record
// container starts.
func TestASetupWithNoRepositoryStartsNoRecordContainer(t *testing.T) {
	c := recordCopy(t)
	c.out[""] = runcopy.LeftOut{Setup: []runcopy.SetupPath{{Path: "tool", State: "other"}}}
	run, spec := recordSpec()
	before := runRecordContainer
	t.Cleanup(func() { runRecordContainer = before })
	runRecordContainer = func(context.Context, gitContainer) (int, error) {
		t.Error("a record container started")
		return 0, nil
	}
	if err := c.recordRepositories(t.Context(), run, spec); err != nil {
		t.Fatal(err)
	}
}
