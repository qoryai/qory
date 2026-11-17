package cmd_test

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
)

// noPushCredentialLine is the line of a push step whose run has no push credential.
const noPushCredentialLine = "qory run: add a push credential to the run configuration: no push proxy; the code is used"

// TestThePushGoesThroughThePushProxy is a write run whose host proxy and push proxy are
// two stand-in proxies to one forge, with a count of their tunnels each. The fetch opens
// a tunnel through the host proxy before the push step starts. The push opens tunnels
// through the push proxy, the branch is on the forge, and the host proxy opens no tunnel
// from the push step on.
func TestThePushGoesThroughThePushProxy(t *testing.T) {
	p := walledPush(t)
	p.pushVia = p.h.server.Stack
	hostAtPush := -1
	p.pushStarted = func() { hostAtPush = p.h.server.Tunnels() }
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if hostAtPush < 1 {
		t.Errorf("the host proxy had %d tunnels when the push step started, want the fetch's", hostAtPush)
	}
	if got := p.h.server.Tunnels(); got != hostAtPush {
		t.Errorf("the host proxy opened %d tunnels from the push step on", got-hostAtPush)
	}
	if got := p.h.server.StackTunnels(); got < 1 {
		t.Errorf("the push proxy opened %d tunnels, want the push's", got)
	}
	if p.branchHead(t) == "" {
		t.Errorf("the forge has no branch\n%s", r.out)
	}
	if posts, _, _ := p.api.counts(); posts != 1 {
		t.Errorf("%d POST, want 1", posts)
	}
}

// TestARunWithNoPushCredentialStopsAtThePush is a write run whose PushProxy returns
// Forager's error for a run with no push credential: the push step fails with the line
// that starts with the fix, in at most 100 characters, and no git runs against the forge
// from the push step on, so no tunnel opens, the forge has no branch and the run API gets
// no POST.
func TestARunWithNoPushCredentialStopsAtThePush(t *testing.T) {
	p := walledPush(t)
	p.noPush = true
	p.pushVia = p.h.server.Stack
	hostAtPush := -1
	p.pushStarted = func() { hostAtPush = p.h.server.Tunnels() }
	r := p.run(t, writesAndCommits(t), nil)
	if _, state := r.step(); state != event.StateFailed {
		t.Errorf("the step ended %q, want failed\n%s", state, r.out)
	}
	wants(t, r.out, noPushCredentialLine+"\n")
	for _, line := range strings.Split(r.out, "\n") {
		if strings.HasPrefix(line, "qory run: add a push credential") && len(line) > 100 {
			t.Errorf("the line has %d characters, want at most 100: %q", len(line), line)
		}
	}
	lacks(t, r.out, "Add a push credential")
	if got := p.h.server.Tunnels(); got != hostAtPush {
		t.Errorf("the host proxy opened %d tunnels from the push step on", got-hostAtPush)
	}
	if got := p.h.server.StackTunnels(); got != 0 {
		t.Errorf("the push proxy opened %d tunnels", got)
	}
	if head := p.branchHead(t); head != "" {
		t.Errorf("the forge has the branch at %s", head)
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST, want 0", posts)
	}
}

// TestThePushProxyOpensAfterTheSetupContainerIsRemoved is a walled write run with a
// setup step: the push proxy opens once, after the session and the removal of
// the setup container, and the push lands. The fake run, as Forager's, returns an error
// from PushProxy while the setup container is up.
func TestThePushProxyOpensAfterTheSetupContainerIsRemoved(t *testing.T) {
	p := walledPush(t)
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	setup, remove, push := slices.Index(r.calls, "setup"), slices.Index(r.calls, "remove setup"),
		slices.Index(r.calls, "push proxy")
	if setup < 0 || remove < setup || push < remove {
		t.Errorf("the calls %q, want setup, remove setup, push proxy in that order", r.calls)
	}
	if n := slices.DeleteFunc(slices.Clone(r.calls), func(c string) bool { return c != "push proxy" }); len(n) != 1 {
		t.Errorf("%d push proxy opens in %q, want 1", len(n), r.calls)
	}
	if p.branchHead(t) == "" {
		t.Errorf("the forge has no branch\n%s", r.out)
	}
}

// TestThePushProxyEndsWhenThePushReturns is a write run whose run API reads the calls the
// run made when it gets the POST that stores the version: the push proxy was opened and
// closed by then, so the check and the store hold no proxy that reaches the forge.
func TestThePushProxyEndsWhenThePushReturns(t *testing.T) {
	p := walledPush(t)
	var mu sync.Mutex
	var seen []string
	var atPost []string
	p.onCall = func(call string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, call)
	}
	p.api.post = func(int, http.ResponseWriter) (handled, store bool) {
		mu.Lock()
		defer mu.Unlock()
		atPost = slices.Clone(seen)
		return false, true
	}
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	open, closed := slices.Index(atPost, "push proxy"), slices.Index(atPost, "push proxy closed")
	if open < 0 || closed < open {
		t.Errorf("the calls at the POST were %q, want push proxy, then push proxy closed", atPost)
	}
}
