package suite_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/suite"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeClone makes the folder of a suite with the files of the commit named in files.
func fakeClone(files map[string]string) func(context.Context, string, string, string) error {
	return func(_ context.Context, _, _, dest string) error {
		if err := os.Mkdir(dest, 0o700); err != nil {
			return err
		}
		for name, content := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dest, name)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dest, name), []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// writes is a runner that prints out, writes result to the result file of the job, and
// returns exit.
func writes(result string, exit int) suite.Runner {
	return func(_ context.Context, job suite.Job, out io.Writer) (int, error) {
		fmt.Fprintln(out, "ran "+job.Full.Command)
		if result != "" {
			if err := os.WriteFile(filepath.Join(job.Dir, job.Full.ResultFile), []byte(result), 0o644); err != nil {
				return -1, err
			}
		}
		return exit, nil
	}
}

// blocks is a runner that runs until its context ends.
func blocks(started chan<- string) suite.Runner {
	return func(ctx context.Context, job suite.Job, out io.Writer) (int, error) {
		started <- job.Commit
		<-ctx.Done()
		return -1, ctx.Err()
	}
}

func newRegistry(t *testing.T, ctx context.Context, run suite.Runner, mutate ...func(*suite.Config)) *suite.Registry {
	t.Helper()
	cfg := suite.Config{
		Source: "source", Root: t.TempDir(), Full: suite.Full{Command: "make test", ResultFile: "junit.xml"},
		Limit: time.Minute, Clone: fakeClone(map[string]string{"main.go": "package main"}), Run: run,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	r := suite.New(ctx, cfg)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// finish starts the suite of commit and waits for its end.
func finish(t *testing.T, r *suite.Registry, commit string) suite.Snapshot {
	t.Helper()
	if _, _, err := r.Start(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := r.Wait(context.Background())
	if err != nil || !ok {
		t.Fatalf("Wait = %v, %v", ok, err)
	}
	return snap
}

func TestTheSuiteStatesFollowTheResultFile(t *testing.T) {
	pass := `<testsuite tests="2"><testcase name="a"/><testcase name="b"/></testsuite>`
	for _, c := range []struct {
		name, result string
		exit         int
		state        suite.State
		counts       *suite.Counts
	}{
		{"all pass", pass, 0, suite.Passed, &suite.Counts{Tests: 2}},
		{"a failed test", `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`, 0, suite.Failed, &suite.Counts{Tests: 2, Failed: 1}},
		{"0 tests", `<testsuite tests="0"></testsuite>`, 0, suite.Failed, &suite.Counts{}},
		{"skipped tests do not fail", `<testsuite><testcase name="a"/><testcase name="b"><skipped/></testcase><testcase name="c"><skipped/></testcase></testsuite>`, 0, suite.Passed, &suite.Counts{Tests: 3, Skipped: 2}},
		{"every test skipped", `<testsuite><testcase name="a"><skipped/></testcase></testsuite>`, 0, suite.Passed, &suite.Counts{Tests: 1, Skipped: 1}},
		{"no result file", "", 0, suite.Passed, nil},
		{"unreadable result file", "tests: 4 passed", 0, suite.Passed, nil},
		{"totals do not match", `<testsuite tests="4"><testcase/></testsuite>`, 0, suite.Failed, nil},
		{"command fails and wrote counts", pass, 1, suite.Failed, &suite.Counts{Tests: 2}},
		{"command fails with a failed test", `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`, 1, suite.Failed, &suite.Counts{Tests: 2, Failed: 1}},
		{"command fails with an unreadable file", "tests: 4 passed", 1, suite.Failed, nil},
		{"command fails with totals that do not match", `<testsuite tests="4"><testcase/></testsuite>`, 1, suite.Failed, nil},
		{"command fails with no file", "", 2, suite.Failed, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRegistry(t, context.Background(), writes(c.result, c.exit))
			snap := finish(t, r, commitA)
			if snap.State != c.state {
				t.Errorf("state %s, want %s; output %q", snap.State, c.state, snap.Output)
			}
			if fmt.Sprint(snap.Counts) != fmt.Sprint(c.counts) {
				t.Errorf("counts %v, want %v", snap.Counts, c.counts)
			}
			if snap.Commit != commitA || !strings.HasPrefix(snap.ID, "su_") || snap.Minutes < 1 {
				t.Errorf("snapshot %+v", snap)
			}
			if !strings.Contains(snap.Output, "ran make test") {
				t.Errorf("output %q has no line of the command", snap.Output)
			}
		})
	}
}

func TestACommittedResultFileCountsForNothing(t *testing.T) {
	committed := map[string]string{"junit.xml": `<testsuite><testcase name="old"/></testsuite>`}
	r := newRegistry(t, context.Background(), writes("", 0), func(c *suite.Config) { c.Clone = fakeClone(committed) })
	snap := finish(t, r, commitA)
	if snap.Counts != nil || snap.State != suite.Passed {
		t.Errorf("a result file that the commit had and the command did not write gave %v, %s", snap.Counts, snap.State)
	}
}

func TestAResultFileThatIsALinkIsNotRead(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.xml")
	write(t, outside, `<testsuite><testcase/></testsuite>`)
	run := func(_ context.Context, job suite.Job, _ io.Writer) (int, error) {
		return 0, os.Symlink(outside, filepath.Join(job.Dir, job.Full.ResultFile))
	}
	snap := finish(t, newRegistry(t, context.Background(), run), commitA)
	if snap.Counts != nil {
		t.Errorf("counts %v from a link out of the folder", snap.Counts)
	}
}

func TestASuitePastItsLimitIsStoppedAndRed(t *testing.T) {
	started := make(chan string, 1)
	r := newRegistry(t, context.Background(), blocks(started), func(c *suite.Config) { c.Limit = 40 * time.Millisecond; c.Full.ResultFile = "" })
	begin := time.Now()
	snap := finish(t, r, commitA)
	if snap.State != suite.Timeout || !snap.State.Red() {
		t.Errorf("state %s, want timeout", snap.State)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("the suite ended after %s", took)
	}
}

func TestAStopEndsTheSuiteAsStopped(t *testing.T) {
	started := make(chan string, 1)
	r := newRegistry(t, context.Background(), blocks(started))
	if _, _, err := r.Start(context.Background(), commitA); err != nil {
		t.Fatal(err)
	}
	<-started
	r.Stop()
	snap, _ := r.Newest()
	if snap.State != suite.Stopped {
		t.Errorf("state %s, want stopped", snap.State)
	}
}

func TestTheRunsEndStopsTheSuite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 1)
	r := newRegistry(t, ctx, blocks(started))
	if _, _, err := r.Start(context.Background(), commitA); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	snap, ok, err := r.Wait(context.Background())
	if err != nil || !ok || snap.State != suite.Stopped {
		t.Errorf("Wait = %+v, %v, %v, want a stopped suite", snap, ok, err)
	}
}

func TestTheSameCommitIsTheSameSuiteAndANewerOneStopsTheOlder(t *testing.T) {
	started := make(chan string, 4)
	r := newRegistry(t, context.Background(), blocks(started))
	first, created, err := r.Start(context.Background(), commitA)
	if err != nil || !created {
		t.Fatalf("first start: %v, created %v", err, created)
	}
	<-started
	again, created, err := r.Start(context.Background(), commitA)
	if err != nil || created || again.ID != first.ID || again.State != suite.Running {
		t.Errorf("a second request for one commit gave %+v, created %v, %v", again, created, err)
	}
	second, created, err := r.Start(context.Background(), commitB)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("second commit: %+v, created %v, %v", second, created, err)
	}
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		old, ok := r.Get(first.ID)
		if !ok {
			t.Fatal("the first suite is gone")
		}
		if old.State == suite.Stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first suite is %s, want stopped", old.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if now, _ := r.Get(second.ID); now.State != suite.Running {
		t.Errorf("the newer suite is %s", now.State)
	}
	if newest, _ := r.Newest(); newest.ID != second.ID {
		t.Errorf("newest is %s, want %s", newest.ID, second.ID)
	}
}

func TestAnUnknownIDAndNoSuite(t *testing.T) {
	r := newRegistry(t, context.Background(), writes("", 0))
	if _, ok := r.Get("su_00000000"); ok {
		t.Error("an unknown id found a suite")
	}
	if _, ok, err := r.Wait(context.Background()); ok || err != nil {
		t.Errorf("Wait with no suite = %v, %v", ok, err)
	}
}

func TestACloneThatFailsStartsNothingAndLeavesTheOlderSuite(t *testing.T) {
	started := make(chan string, 1)
	failing := false
	var mu sync.Mutex
	clone := func(ctx context.Context, src, commit, dest string) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return fmt.Errorf("git fetch: not our ref")
		}
		return fakeClone(nil)(ctx, src, commit, dest)
	}
	r := newRegistry(t, context.Background(), blocks(started), func(c *suite.Config) { c.Clone = clone })
	first, _, err := r.Start(context.Background(), commitA)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	mu.Lock()
	failing = true
	mu.Unlock()
	if _, _, err := r.Start(context.Background(), commitB); err == nil || !strings.Contains(err.Error(), "not our ref") {
		t.Errorf("error %v, want the clone's", err)
	}
	if now, _ := r.Get(first.ID); now.State != suite.Running {
		t.Errorf("the older suite is %s after a failed start", now.State)
	}
}

func TestTheOutputKeepsTheLastBytesAndIsMasked(t *testing.T) {
	run := func(_ context.Context, _ suite.Job, out io.Writer) (int, error) {
		fmt.Fprint(out, strings.Repeat("a", suite.MaxOutput), "secret-value ", "end")
		return 0, nil
	}
	r := newRegistry(t, context.Background(), run, func(c *suite.Config) {
		c.Mask = func(s string) string { return strings.ReplaceAll(s, "secret-value", "***") }
	})
	snap := finish(t, r, commitA)
	if len(snap.Output) > suite.MaxOutput || !strings.HasSuffix(snap.Output, "*** end") {
		t.Errorf("output of %d bytes ends %q", len(snap.Output), snap.Output[max(0, len(snap.Output)-20):])
	}
}

func TestCloseRemovesTheClones(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, context.Background(), writes("", 0), func(c *suite.Config) { c.Root = root })
	finish(t, r, commitA)
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("%d folders in the root before the close, want 1", len(entries))
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("%d folders left after the close", len(entries))
	}
}

func TestNoSuiteStartsAfterClose(t *testing.T) {
	root := t.TempDir()
	r := newRegistry(t, context.Background(), writes("", 0), func(c *suite.Config) { c.Root = root })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Start(context.Background(), commitA); !errors.Is(err, context.Canceled) {
		t.Errorf("Start after Close returned %v, want context.Canceled", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("%d folders in the root after a Start that Close refused", len(entries))
	}
}

// TestACloneThatCloseOvertookIsRemoved is a Close that runs while the commit is cloned: the suite
// does not start, and its clone does not stay.
func TestACloneThatCloseOvertookIsRemoved(t *testing.T) {
	root := t.TempDir()
	var r *suite.Registry
	clone := fakeClone(map[string]string{"main.go": "package main"})
	r = newRegistry(t, context.Background(), writes("", 0), func(c *suite.Config) {
		c.Root = root
		c.Clone = func(ctx context.Context, src, commit, dest string) error {
			if err := clone(ctx, src, commit, dest); err != nil {
				return err
			}
			return r.Close()
		}
	})
	if _, _, err := r.Start(context.Background(), commitA); !errors.Is(err, context.Canceled) {
		t.Errorf("Start returned %v, want context.Canceled", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("%d folders left in the root", len(entries))
	}
}

// TestAStopDuringTheCloneOfAStartStartsNoSuite is a Stop that runs while a request clones its
// commit, as when the session exits with a request in flight: the suite does not start, and the
// clone does not stay.
func TestAStopDuringTheCloneOfAStartStartsNoSuite(t *testing.T) {
	root := t.TempDir()
	cloning, release := make(chan struct{}), make(chan struct{})
	clone := fakeClone(map[string]string{"main.go": "package main"})
	r := newRegistry(t, context.Background(), writes("", 0), func(c *suite.Config) {
		c.Root = root
		c.Clone = func(ctx context.Context, src, commit, dest string) error {
			err := clone(ctx, src, commit, dest)
			close(cloning)
			<-release
			return err
		}
	})
	errc := make(chan error, 1)
	go func() {
		_, _, err := r.Start(context.Background(), commitA)
		errc <- err
	}()
	<-cloning
	r.Stop()
	close(release)
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("Start returned %v, want context.Canceled", err)
	}
	if _, ok := r.Newest(); ok {
		t.Error("a suite started after Stop")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("%d folders left in the root", len(entries))
	}
	if _, _, err := r.Start(context.Background(), commitB); !errors.Is(err, context.Canceled) {
		t.Errorf("Start after Stop returned %v, want context.Canceled", err)
	}
}

// TestASuiteThatEndedBeforeTheContextIsNotCut is a Wait whose context is done and whose suite
// ended: the suite is returned with no error, whichever case the select reaches first.
func TestASuiteThatEndedBeforeTheContextIsNotCut(t *testing.T) {
	r := newRegistry(t, context.Background(), writes("", 1))
	finish(t, r, commitA)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 200 {
		snap, ok, err := r.Wait(ctx)
		if err != nil || !ok || snap.State != suite.Failed {
			t.Fatalf("Wait = %+v, %v, %v, want the ended suite and no error", snap, ok, err)
		}
	}
}

func TestEndedIsCalledOnceForEachSuiteWithItsEnd(t *testing.T) {
	var mu sync.Mutex
	var ended []suite.Snapshot
	r := newRegistry(t, context.Background(), writes(`<testsuite><testcase/></testsuite>`, 0), func(c *suite.Config) {
		c.Ended = func(s suite.Snapshot) { mu.Lock(); ended = append(ended, s); mu.Unlock() }
	})
	snap := finish(t, r, commitA)
	mu.Lock()
	defer mu.Unlock()
	if len(ended) != 1 || ended[0].ID != snap.ID || ended[0].State != suite.Passed || ended[0].Counts == nil || ended[0].Counts.Tests != 1 {
		t.Errorf("Ended got %+v, want one passed suite %s with its counts", ended, snap.ID)
	}
}
