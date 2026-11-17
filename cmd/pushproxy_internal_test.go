package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// TestForagerNoPushProxyErrorIsTheOneQoryMatches is a run that Forager opens at a gateway
// in this process with no push credential: its PushProxy returns an error that
// [pushProxyError] turns into [textPushNoCred], and any other error becomes
// [textPushNoProxy] with the error's text. It fails when Forager changes the words that
// [noPushProxyWords] matches.
func TestForagerNoPushProxyErrorIsTheOneQoryMatches(t *testing.T) {
	ctx := context.Background()
	at, err := openAtGateway(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer at.g.Close(ctx)
	_, err = foragerRun{at.run}.PushProxy()
	if err == nil {
		t.Fatal("a run with no push credential has a push proxy")
	}
	if got := pushProxyError(err).Error(); got != textPushNoCred {
		t.Errorf("Forager's error %q became %q, want %q", err, got, textPushNoCred)
	}
	if _, err := at.run.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	_, err = foragerRun{at.run}.PushProxy()
	if err == nil {
		t.Fatal("a closed run has a push proxy")
	}
	if got := pushProxyError(err).Error(); !strings.HasPrefix(got, "take the work item in again in Qory Apiary: the run's push proxy") || !strings.Contains(got, err.Error()) {
		t.Errorf("the error of a closed run became %q", got)
	}
}

// TestTheNoPushCredentialLineStartsWithTheFix is [textPushNoCred] as qory run prints it:
// the first line is at most 100 characters, and starts with the fix.
func TestTheNoPushCredentialLineStartsWithTheFix(t *testing.T) {
	line := (&runLineError{pushProxyError(errors.New(errNowhereNoPush))}).Error()
	if line != textPushNoCred {
		t.Fatalf("the line is %q, want %q", line, textPushNoCred)
	}
	if n := len("qory run: " + line); n > 100 {
		t.Errorf("the line is %d characters with its prefix, want at most 100", n)
	}
	if !strings.HasPrefix(line, "add a push credential to the run configuration") {
		t.Errorf("the line does not start with the fix: %q", line)
	}
}

// TestNowhereKeepsTheRulesOfThePushProxy is the fake run against Forager's rules of
// PushProxy, which the tests of the push step depend on: an error during a join, during
// a copy step, while the setup container is up and after Close, and for a run with no
// push credential; and an address, which is the host proxy's when none is set.
func TestNowhereKeepsTheRulesOfThePushProxy(t *testing.T) {
	ctx := context.Background()
	open := func(n Nowhere) (openedRun, func() []string) {
		_, calls, restore := OpenNowhereWith(n)
		t.Cleanup(restore)
		r, err := openRun(ctx, session.OpenSpec{RunID: "run-01"})
		if err != nil {
			t.Fatal(err)
		}
		return r, calls
	}
	r, _ := open(Nowhere{Proxy: "http://qory:host@127.0.0.1:9", PushProxy: "http://qory:push@127.0.0.1:9"})
	h, err := r.HostProxy()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.PushProxy()
	if err != nil {
		t.Fatal(err)
	}
	if h.proxy.URL == p.proxy.URL || !strings.Contains(p.proxy.URL, "push") {
		t.Errorf("the host proxy and the push proxy have addresses %q and %q, want two", h.proxy.URL, p.proxy.URL)
	}
	if err := r.Setup(ctx, session.SetupSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PushProxy(); err == nil || err.Error() != errNowhereSetupUp {
		t.Errorf("PushProxy while the setup container is up: %v", err)
	}
	if err := r.RemoveSetup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PushProxy(); err != nil {
		t.Errorf("PushProxy after RemoveSetup: %v", err)
	}
	step, err := r.StartStep(event.StepCopy, "copy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.PushProxy(); err == nil {
		t.Error("PushProxy during a copy step returned no error")
	}
	if err := step.Exit(event.StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PushProxy(); err == nil || err.Error() != errNowhereClosedPushText {
		t.Errorf("PushProxy after Close: %v", err)
	}

	r, _ = open(Nowhere{NoPushProxy: true})
	if _, err := r.PushProxy(); err == nil || err.Error() != errNowhereNoPush {
		t.Errorf("PushProxy of a run with no push credential: %v", err)
	}
	r, _ = open(Nowhere{Proxy: "http://qory:host@127.0.0.1:9"})
	if p, err := r.PushProxy(); err != nil || p.proxy.URL != "http://qory:host@127.0.0.1:9" {
		t.Errorf("PushProxy with no address set: %q, %v", p.proxy.URL, err)
	}
	r, _ = open(Nowhere{ContainerKept: true})
	if _, err := r.PushProxy(); err == nil || err.Error() != errNowhereKept {
		t.Errorf("PushProxy while ContainerKept is true: %v", err)
	}
}
