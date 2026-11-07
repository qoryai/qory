package cmd

import (
	"context"
	"errors"
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
