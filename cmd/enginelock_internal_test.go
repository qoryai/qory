package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
)

// slowFetch is a run whose image step takes a while, and that counts the image steps
// running at once.
type slowFetch struct {
	nowhere
	mu            sync.Mutex
	running, most int
	steps         int
	wait          time.Duration
}

func (r *slowFetch) FetchImage(context.Context) error {
	r.mu.Lock()
	r.running++
	r.steps++
	r.most = max(r.most, r.running)
	r.mu.Unlock()
	time.Sleep(r.wait)
	r.mu.Lock()
	r.running--
	r.mu.Unlock()
	return nil
}

// shortLockPoll makes a run that waits for the engine lock try it again every 5 ms.
func shortLockPoll(t *testing.T) {
	t.Helper()
	was := engineLockPoll
	engineLockPoll = 5 * time.Millisecond
	t.Cleanup(func() { engineLockPoll = was })
}

// TestTheEngineLockRunsOneImageStepAtATime is two image steps at once with one engine
// proxy file, each with a separate descriptor of it, as two qory runs open it: the second
// waits for the first, and both succeed.
func TestTheEngineLockRunsOneImageStepAtATime(t *testing.T) {
	shortLockPoll(t)
	file := engineFile(t, engineComplete, 0o400)
	proxy := &session.EngineProxy{Addr: "127.0.0.1:3129", Password: testEnginePassword}
	run := &slowFetch{wait: 100 * time.Millisecond}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			errs[i] = withEngineLock(t.Context(), file, false, func() error {
				return fetchImage(t.Context(), run, proxy, false)
			})
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("an image step: %v", err)
		}
	}
	if run.steps != 2 || run.most != 1 {
		t.Errorf("%d image steps ran, at most %d at once; want 2, one at a time", run.steps, run.most)
	}
	// Released, the lock is free for the next step.
	release, err := lockEngine(t.Context(), file)
	if err != nil {
		t.Fatalf("the lock after both steps: %v", err)
	}
	release()
}

// TestARunThatEndsWhileItWaitsForTheEngineLockStopsWithItsLine is a run whose time
// limit passes while another holds the lock: its step does not run, and its line starts
// with the fix, with "; the code is used" after it for a run from a run code. A run
// stopped while it waits gets the line of a stop.
func TestARunThatEndsWhileItWaitsForTheEngineLockStopsWithItsLine(t *testing.T) {
	shortLockPoll(t)
	file := engineFile(t, engineComplete, 0o600)
	release, err := lockEngine(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, fromCode := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		ran := false
		err := withEngineLock(ctx, file, fromCode, func() error { ran = true; return nil })
		cancel()
		want := "give the run a longer time limit, or start fewer runs at once: the run ended while another run's image pull held the lock of wall.engine_proxy_file " + file + ": context deadline exceeded"
		if fromCode {
			want += codeUsed
		}
		var line *runLineError
		if !errors.As(err, &line) || line.Error() != want {
			t.Errorf("from a run code %v: %v, want the line %q", fromCode, err, want)
		}
		if ran {
			t.Error("the step ran without the lock")
		}
	}
	// A run stopped while it waits, by a signal or the end of its dispatch, ends with a
	// line of a stop, not of a time limit.
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(30*time.Millisecond, cancel)
	err = withEngineLock(ctx, file, false, func() error { t.Error("the step ran without the lock"); return nil })
	want := "start the run again if it is still wanted: the run was stopped while another run's image pull held the lock of wall.engine_proxy_file " + file + ": context canceled"
	var line *runLineError
	if !errors.As(err, &line) || line.Error() != want {
		t.Errorf("a stopped run: %v, want the line %q", err, want)
	}
}

// TestTheEngineLockStopsAtAFileAsTheReadDoes is a file that is missing, a link, not a
// regular file, or unreadable at the lock: each stops the step with the refusal that
// reading the file gives, exit 2, and the step does not run. With no file, the step
// runs with no lock.
func TestTheEngineLockStopsAtAFileAsTheReadDoes(t *testing.T) {
	dir := t.TempDir()
	good := engineFile(t, engineComplete, 0o600)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	unreadable := engineFile(t, engineComplete, 0o000)
	cases := []struct {
		name, path, want string
	}{
		{"missing", filepath.Join(dir, "none.json"), "check wall.engine_proxy_file: qory cannot open " + filepath.Join(dir, "none.json") + ": no such file or directory"},
		{"a link", link, "set wall.engine_proxy_file to the file itself: " + link + " is a link"},
		{"a folder", dir, "set wall.engine_proxy_file to a regular file: " + dir + " is not one"},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, struct{ name, path, want string }{"unreadable", unreadable, "check wall.engine_proxy_file: qory cannot open " + unreadable + ": permission denied"})
	}
	for _, c := range cases {
		ran := false
		err := withEngineLock(t.Context(), c.path, false, func() error { ran = true; return nil })
		if err == nil || err.Error() != c.want || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d), want %q, exit %d", c.name, err, ExitCode(err), c.want, ExitInput)
		}
		if ran {
			t.Errorf("%s: the step ran", c.name)
		}
		if _, err := readEngineProxy(c.path); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: the read refuses with %v, the lock with %q", c.name, err, c.want)
		}
	}
	ran := false
	if err := withEngineLock(t.Context(), "", false, func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("with no engine proxy file: %v, ran %v", err, ran)
	}
}
