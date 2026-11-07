package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes"

	"github.com/qoryai/qory/internal/runcopy"
)

// TestAFetchPastItsLimitHasItsLine is a fetch that ran past the limit: the line has the fetch,
// and a stop keeps the error for the line of the stop.
func TestAFetchPastItsLimitHasItsLine(t *testing.T) {
	limited, cancel := context.WithCancel(context.Background())
	cancel()
	failed := errors.New("the fetch failed")
	err := limitErr(context.Background(), limited, textCopyFetchLimit, failed)
	want := "the fetch of the run's commits ran past its limit of " + copyStepLimit.String()
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
	stop, cancelStop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelStop()
	<-stop.Done()
	if err := limitErr(stop, limited, textCopyFetchLimit, failed); !errors.Is(err, failed) {
		t.Errorf("after a stop the error is %v", err)
	}
}

// TestASignalInTheCheckOrFetchHasItsLine is a stop that ended the check of a run's commits or
// their fetch, which share the line: the line is true for both.
func TestASignalInTheCheckOrFetchHasItsLine(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	c := &runCopy{cutBy: func() string { return "qory run got a second signal" }}
	err := c.checkFailed(stopped, errors.New("the fetch failed"))
	want := "qory run got a second signal during the check or the fetch of the run's commits, so those commits did not come back"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
}

// TestALongCauseKeepsNoPartOfAPathOfTheCopy is a cause past the cut of [integration.Printable],
// in a long cache folder, with a path of the copy twice: the step's log and the line of a run with
// a push have no part of a path of the copy, and the line of a run with no push keeps the path.
func TestALongCauseKeepsNoPartOfAPathOfTheCopy(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-cache-folder/", 12))
	t.Setenv("HOME", long)
	t.Setenv("XDG_CACHE_HOME", long)
	dir, err := runcopy.New("01a1b738-63b1-76fa-9757-6552cfc987b5")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Remove(); err != nil {
			t.Error(err)
		}
	})
	cause := errors.New("mkdir " + dir.Bundle() + ": no space left on device, and " + dir.Bundle() + " stays")
	const logged = "mkdir the run's copy/bundle: no space left on device, and the run's copy/bundle stays"
	under := errors.New("stat " + filepath.Join(dir.Work(), "sub") + ": no such file")
	for _, c := range []struct {
		name   string
		pushes bool
		logged string
		stop   func(*runCopy) *copyStop
	}{
		{"no session", false, logged, func(c *runCopy) *copyStop { return c.noSession(context.Background(), cause) }},
		{"no session, push", true, logged, func(c *runCopy) *copyStop { return c.noSession(context.Background(), cause) }},
		{"another cause", false, logged, func(c *runCopy) *copyStop { return c.stoppedBy("%s", cause) }},
		{"another cause, push", true, logged, func(c *runCopy) *copyStop { return c.stoppedBy("%s", cause) }},
		{"a path under the working folder", false, "stat the run's copy/sub: no such file", func(c *runCopy) *copyStop { return c.stoppedBy("%s", under) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			stop := c.stop(&runCopy{dir: dir, pushes: c.pushes})
			if !strings.Contains(stop.log, c.logged) {
				t.Errorf("the log's line is %q, want %q in it", stop.log, c.logged)
			}
			if strings.Contains(stop.log, "a-long-cache-folder") || strings.Contains(stop.log, "qory/runs") {
				t.Errorf("the log's line has a part of a path: %q", stop.log)
			}
			if c.pushes && stop.Error() != stop.log {
				t.Errorf("the line of a run with a push is %q, want the log's line %q", stop.Error(), stop.log)
			}
			if !c.pushes && (!strings.Contains(stop.Error(), dir.Work()) || !strings.Contains(stop.Error(), "...")) {
				t.Errorf("the line of a run with no push is %q, want the path of the copy and the cut", stop.Error())
			}
		})
	}
}

// TestAnErrorOfTheCopyKeepsNoPartOfItsPath is a real error of [runcopy.Dir.GitSession], whose path
// is already cut or has its characters replaced: a bundle folder that is there, in a cache folder
// past the cut and in one with a tab in its name. The step's log has no part of the path.
func TestAnErrorOfTheCopyKeepsNoPartOfItsPath(t *testing.T) {
	for _, c := range []struct{ name, cache, part string }{
		{"a long cache folder", strings.Repeat("a-long-cache-folder/", 12), "a-long-cache-folder"},
		{"a tab in the cache folder", "a-cache\tfolder", "a-cache"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), c.cache)
			t.Setenv("HOME", cache)
			t.Setenv("XDG_CACHE_HOME", cache)
			dir, err := runcopy.New("01a1b738-63b1-76fa-9757-6552cfc987b5")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := dir.Remove(); err != nil {
					t.Error(err)
				}
			})
			if err := os.Mkdir(dir.Bundle(), 0o700); err != nil {
				t.Fatal(err)
			}
			_, err = dir.GitSession(context.Background(), "main", "message", nil, nil)
			if err == nil {
				t.Fatal("the session has a bundle folder that is there, and no error")
			}
			stop := (&runCopy{dir: dir}).noSession(context.Background(), err)
			if !strings.Contains(stop.log, textCopyFolder) || strings.Contains(stop.log, c.part) || strings.Contains(stop.log, "qory/runs") {
				t.Errorf("the log's line is %q, from the error %q", stop.log, err)
			}
		})
	}
}

// TestNowhereKeepsTheRulesOfACopyStep is the fake run against Forager's rules, which the tests of
// the copy step depend on: an agent's session joins in no host step, a bare one only in a copy
// step, and during a copy step HostProxy, Setup and RemoveSetup return an error, as do the
// step's Log and Exit during the join.
func TestNowhereKeepsTheRulesOfACopyStep(t *testing.T) {
	ctx := context.Background()
	_, _, restore := OpenNowhereWith(Nowhere{})
	defer restore()
	r, err := openRun(ctx, session.OpenSpec{RunID: "run-01"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := r.StartStep(event.StepPush, "push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{}); err == nil || errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("a bare join during a push step: %v", err)
	}
	if _, err := r.HostProxy(); err != nil {
		t.Errorf("HostProxy during a push step: %v", err)
	}
	if err := step.Exit(event.StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	if step, err = r.StartStep(event.StepCopy, textCopyStep); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{Runtime: runtimes.Bare("agent")}); err == nil || errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("an agent's join during a copy step: %v", err)
	}
	if _, err := r.Join(ctx, session.SessionSpec{}); !errors.Is(err, ErrJoinedNowhere) {
		t.Errorf("a bare join during a copy step: %v", err)
	}
	if _, err := r.HostProxy(); err == nil {
		t.Error("HostProxy during a copy step returned no error")
	}
	if err := r.Setup(ctx, session.SetupSpec{}); err == nil {
		t.Error("Setup during a copy step returned no error")
	}
	if err := r.RemoveSetup(ctx); err == nil {
		t.Error("RemoveSetup during a copy step returned no error")
	}
	defer SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
		if err := step.Log([]byte("line\n")); err == nil {
			t.Error("Log during the join returned no error")
		}
		if err := step.Exit(event.StateSucceeded, 0); err == nil {
			t.Error("Exit during the join returned no error")
		}
		if _, err := r.HostProxy(); err == nil {
			t.Error("HostProxy during the join returned no error")
		}
		return &session.Result{}, nil
	})()
	git := session.SessionSpec{Args: []string{runcopy.SessionScriptName, "/bin"}}
	if _, err := joinSession(r, ctx, git); err != nil {
		t.Errorf("the git session's join: %v", err)
	}
	if err := step.Log([]byte("line\n")); err != nil {
		t.Errorf("Log after the join: %v", err)
	}
	if err := step.Exit(event.StateFailed, 128); err != nil {
		t.Errorf("Exit after the join: %v", err)
	}
	if _, err := r.HostProxy(); err != nil {
		t.Errorf("HostProxy after the copy step: %v", err)
	}
}
