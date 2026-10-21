package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestAnExploreRunThatSucceeds is one run, from its claim to its outcome: claim,
// dispatched, handover, then the launch with the handover's run key and inputs, then
// the signed discovery and the outcome succeeded, with no release. The key's holder is
// cleared, the run directory removed, and the journal left empty.
func TestAnExploreRunThatSucceeds(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.srv.Offer(exploreRun(runA))
	h.start()
	h.settled(1)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	d := h.only()
	routes := h.srv.Routes()
	want := []provisionertest.Route{provisionertest.RouteClaim, provisionertest.RouteDispatched, provisionertest.RouteHandover, provisionertest.RouteConfiguration, provisionertest.RouteOutcome}
	if i := slices.Index(routes, provisionertest.RouteDispatched); i < 1 || !slices.Equal(routes[i-1:i+4], want) {
		t.Errorf("the fake was sent %v, want %v among them", routes, want)
	}
	h.notReleased()
	if o, ok := h.outcome(runA); !ok || o.State != explore.StateSucceeded || o.Reason != "" {
		t.Errorf("the outcome is %+v, %v", o, ok)
	}
	l := h.launched()
	if len(l) != 1 || l[0].RunID != runA || l[0].KeySum != sum([]byte(d.Run.RunKey)) || l[0].Inputs != compact(t, d.Run.Inputs) || !l[0].Cleared {
		t.Errorf("the launches were %+v", l)
	}
	if _, err := os.Stat(explore.Dir(h.pool.Settings.Explore.State, runA)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run directory is still there: %v", err)
	}
	for _, msg := range []string{"claimed a run", "started the run", "posted the run's outcome"} {
		if len(h.log.with(msg)) != 1 {
			t.Errorf("%q was logged %d times:\n%s", msg, len(h.log.with(msg)), h.log.text())
		}
	}
	if m := h.log.with("posted the run's outcome"); len(m) == 1 && (m[0]["state"] != "succeeded" || m[0]["run_id"] != runA || m[0]["dispatch_id"] != d.ID) {
		t.Errorf("the outcome's line is %v", m[0])
	}
	if m := h.log.with("claimed a run"); len(m) == 1 && (m[0]["step"] != "explore" || m[0]["run_id"] != runA) {
		t.Errorf("the claim's line is %v", m[0])
	}
}

// TestEachEndingOfAnExploreRun maps how a run ended to what the pool does: a run that
// made its record posts its outcome and is not released, a failed one too; one that
// made none is released with retry false, the run could not be started, and posts
// nothing.
func TestEachEndingOfAnExploreRun(t *testing.T) {
	for _, c := range []struct {
		name          string
		record        bool
		run           child.Result
		err           error
		state, reason string
		release       bool
	}{
		{name: "succeeded", record: true, state: "succeeded"},
		{name: "its time limit", record: true, run: child.Result{ExitCode: 124}, state: "failed", reason: "time_limit"},
		{name: "failed", record: true, run: child.Result{ExitCode: 3}, state: "failed"},
		{name: "a signal", record: true, run: child.Result{ExitCode: -1, Signal: 9}, state: "failed"},
		{name: "no record", run: child.Result{ExitCode: 2}, release: true},
		{name: "not launched", err: errors.New("qory harness compose exited 1"), release: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			h.setLaunch(ending(c.record, c.run, c.err))
			h.srv.Offer(exploreRun(runA))
			h.start()
			h.settled(1)
			h.stop()
			d := h.only()
			o, posted := h.outcome(runA)
			if c.release {
				h.released(d, "the run could not be started on this machine", false)
				if posted {
					t.Errorf("an outcome was posted: %+v", o)
				}
			} else {
				h.notReleased()
				if !posted || o.State != c.state || o.Reason != c.reason {
					t.Errorf("the outcome is %+v, %v; want %s %s", o, posted, c.state, c.reason)
				}
			}
			if c.err != nil && len(h.log.with("the run could not be started")) != 1 {
				t.Errorf("the launch's error was not logged:\n%s", h.log.text())
			}
			if c.release {
				if m := h.log.with("released the run"); len(m) != 1 || m[0]["retry"] != false || m[0]["reason"] != "the run could not be started on this machine" {
					t.Errorf("the release's lines are %v", m)
				}
			}
		})
	}
}

// TestTheStepCheck releases a claim before dispatched: a step the pool program does not
// run with retry false, and a lease that passed with retry true.
func TestTheStepCheck(t *testing.T) {
	for _, c := range []struct {
		name   string
		run    provisionertest.Run
		edit   func(map[string]any)
		reason string
		retry  bool
	}{
		{name: "another step", run: provisionertest.Run{RunID: runA, Step: otherStep, Inputs: json.RawMessage(`{"version":1}`)}, reason: "the pool program runs only the explore and prebuild steps"},
		{name: "a lease that passed", run: exploreRun(runA), edit: func(doc map[string]any) {
			doc["lease_until"] = contracts.FormatTime(time.Now().Add(-time.Minute))
		}, reason: "the lease ended before the run could be started", retry: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			if c.edit != nil {
				h.srv.Script(provisionertest.RouteClaim, provisionertest.Reply{Edit: c.edit})
			}
			h.srv.Offer(c.run)
			h.start()
			h.waitFor("the release", func() bool { return h.count(provisionertest.RouteRelease) == 1 })
			h.settled(1)
			h.stop()
			h.released(h.only(), c.reason, c.retry)
			if n := h.count(provisionertest.RouteDispatched); n != 0 {
				t.Errorf("dispatched was sent %d times", n)
			}
			if len(h.launched()) != 0 {
				t.Error("a run was launched")
			}
		})
	}
}

// TestAnExploreRunWithNoTargetIsLaunched runs an explore run handed over with no
// target and with run settings: the launcher gets the handover's domain and run
// settings beside its inputs, and the run ends as any other.
func TestAnExploreRunWithNoTargetIsLaunched(t *testing.T) {
	h := newHarness(t, 1, 1)
	rs := &contracts.RunSettings{
		Runtime: "claude", Model: "example-large",
		ProviderModels: []contracts.ProviderModel{{ID: "example-large"}},
	}
	var got explore.Run
	h.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
		got = r
		return explore.Result{Record: true, Ending: explore.EndingOf(true, child.Result{})}, nil
	})
	run := exploreRun(runA)
	run.NoTarget, run.RunSettings = true, rs
	h.srv.Offer(run)
	h.start()
	h.settled(1)
	h.stop()
	d := h.only()
	sent, _ := json.Marshal(rs)
	kept, _ := json.Marshal(got.RunSettings)
	if got.RunID != runA || got.Domain != d.Run.Domain || got.Domain == "" || string(kept) != string(sent) || string(got.Inputs) != compact(t, d.Run.Inputs) {
		t.Errorf("the launcher got %+v, run settings %s", got, kept)
	}
	if o, ok := h.outcome(runA); !ok || o.State != "succeeded" {
		t.Errorf("the outcome is %+v", o)
	}
	h.notReleased()
}

// TestTheRunsCap sends no claim while pool.runs dispatches are live, and claims again
// once one ended.
func TestTheRunsCap(t *testing.T) {
	h := newHarness(t, 1, 1)
	running := make(chan struct{})
	h.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
		if r.RunID == runA {
			<-running
		}
		return explore.Result{Record: true, Ending: explore.EndingOf(true, child.Result{})}, nil
	})
	h.srv.Offer(exploreRun(runA), exploreRun(runB))
	h.start()
	h.waitFor("the first run's handover", func() bool { return h.count(provisionertest.RouteHandover) == 1 })
	time.Sleep(1500 * time.Millisecond)
	if n := h.count(provisionertest.RouteClaim); n != 1 {
		t.Errorf("%d claims went out while one run of one was live", n)
	}
	close(running)
	h.settled(2)
	h.stop()
	if _, ok := h.outcome(runB); !ok {
		t.Error("the second run did not run")
	}
}

// TestThePrebuildsCap releases a prebuild claimed while pool.prebuilds builds are
// going on, with retry true and before dispatched, and runs an explore run beside the
// build.
func TestThePrebuildsCap(t *testing.T) {
	h := newHarness(t, 2, 1)
	building := make(chan struct{})
	h.pool.Prebuild = func(ctx context.Context, d pool.Prebuild) explore.Ending {
		d.Key.Clear()
		<-building
		return explore.Ending{Post: true, State: explore.StateSucceeded}
	}
	h.srv.Offer(prebuildRun(runA), prebuildRun(runB))
	h.start()
	h.waitFor("the second build's release", func() bool { return h.count(provisionertest.RouteRelease) == 1 })
	h.srv.Offer(exploreRun(runC))
	h.waitFor("the explore run beside the build", func() bool { _, ok := h.outcome(runC); return ok })
	close(building)
	h.settled(3)
	h.stop()
	ds := h.srv.Dispatches()
	h.released(ds[1], "this machine is running as many builds as it may at once", true)
	for _, c := range h.srv.Calls() {
		if c.Route == provisionertest.RouteDispatched && c.DispatchID == ds[1].ID {
			t.Error("the build over the cap was sent dispatched")
		}
	}
	if o, ok := h.outcome(runA); !ok || o.State != "succeeded" {
		t.Errorf("the first build's outcome is %+v", o)
	}
}

// TestAPrebuild launches a build with the handover's run key, inputs and target, and
// its report sends prebuilt for the dispatch; after the handover a release is retry
// false, whatever the launcher says.
func TestAPrebuild(t *testing.T) {
	img := provisioner.Image{Image: "localhost:5001/qory-env/0123456789abcdef@sha256:" + strings.Repeat("a", 64), Size: 1024, EnvironmentRef: "env-3f2a91c4", Commit: strings.Repeat("b", 40)}
	t.Run("built", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		var got pool.Prebuild
		var keySum string
		h.pool.Prebuild = func(ctx context.Context, d pool.Prebuild) explore.Ending {
			got, keySum = d, sum(d.Key.Bytes())
			d.Key.Clear()
			if err := d.Report(ctx, img); err != nil {
				t.Error(err)
			}
			return explore.Ending{Post: true, State: explore.StateSucceeded}
		}
		h.srv.Offer(prebuildRun(runA))
		h.start()
		h.settled(1)
		h.stop()
		d := h.only()
		if got.RunID != runA || got.Target != *d.Run.Target || string(got.Inputs) != compact(t, d.Run.Inputs) || keySum != sum([]byte(d.Run.RunKey)) {
			t.Errorf("the launcher got %+v", got)
		}
		if d.Prebuilt == nil || d.Prebuilt.Image != img.Image || d.Prebuilt.RunID != runA {
			t.Errorf("prebuilt was %+v", d.Prebuilt)
		}
		h.notReleased()
		if o, ok := h.outcome(runA); !ok || o.State != "succeeded" {
			t.Errorf("the outcome is %+v", o)
		}
	})
	t.Run("one after the other", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.pool.Prebuild = func(ctx context.Context, d pool.Prebuild) explore.Ending {
			d.Key.Clear()
			return explore.Ending{Post: true, State: explore.StateSucceeded}
		}
		h.srv.Offer(prebuildRun(runA))
		h.start()
		h.waitFor("the first build", func() bool { _, ok := h.outcome(runA); return ok })
		h.srv.Offer(prebuildRun(runB))
		h.settled(2)
		h.stop()
		h.notReleased()
		if _, ok := h.outcome(runB); !ok {
			t.Error("the second build did not run")
		}
	})
	t.Run("failed", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.pool.Prebuild = func(ctx context.Context, d pool.Prebuild) explore.Ending {
			d.Key.Clear()
			return explore.Ending{Release: true, Retry: true, ReleaseReason: "the code could not be fetched", Post: true, State: explore.StateFailed}
		}
		h.srv.Offer(prebuildRun(runA))
		h.start()
		h.settled(1)
		h.stop()
		h.released(h.only(), "the code could not be fetched", false)
		if o, ok := h.outcome(runA); !ok || o.State != "failed" {
			t.Errorf("the outcome is %+v", o)
		}
	})
}

// TestDispatchedFailures: a signed 429 is waited out up to three times; 409
// lease_expired and 410 drop the dispatch with no release; anything else releases it
// with retry true. None goes on to the handover.
func TestDispatchedFailures(t *testing.T) {
	limited := provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "2"}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		release bool
		reason  string
		ran     bool
		waits   int
	}{
		{name: "429 three times", replies: []provisionertest.Reply{limited, limited, limited}, ran: true, waits: 3},
		{name: "429 four times", replies: []provisionertest.Reply{limited, limited, limited, limited}, release: true, waits: 3},
		{name: "lease_expired", replies: []provisionertest.Reply{{Status: 409, Code: "lease_expired"}}},
		{name: "gone", replies: []provisionertest.Reply{{Status: 410, Code: "gone"}}},
		{name: "forbidden", replies: []provisionertest.Reply{{Status: 403, Code: "forbidden"}}, release: true},
		{name: "unsigned", replies: slices.Repeat([]provisionertest.Reply{{Status: 503, Unsigned: true}}, 5), release: true},
		{name: "429 past the lease", replies: []provisionertest.Reply{{Status: 429, Code: "rate_limited", RetryAfter: "3600"}}, release: true, reason: "the lease ended before the run could be started"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			h.srv.Script(provisionertest.RouteDispatched, c.replies...)
			h.srv.Offer(exploreRun(runA))
			h.start()
			h.waitFor("the dispatch's end", func() bool {
				return len(h.srv.Dispatches()) == 1 && (h.count(provisionertest.RouteRelease) > 0 || len(h.log.with("Qory Apiary did not record the run's start")) > 0 || h.count(provisionertest.RouteOutcome) > 0)
			})
			h.settled(1)
			h.stop()
			d := h.only()
			if c.release {
				reason := c.reason
				if reason == "" {
					reason = "the run could not be started on this machine"
				}
				h.released(d, reason, true)
			} else {
				h.notReleased()
			}
			if ran := len(h.launched()) == 1; ran != c.ran {
				t.Errorf("ran %v, want %v; the fake was sent %v", ran, c.ran, h.srv.Routes())
			}
			if !c.ran && h.count(provisionertest.RouteHandover) != 0 {
				t.Error("the handover was sent")
			}
			if got := slices.Collect(func(yield func(time.Duration) bool) {
				for _, w := range h.waitsOf() {
					if w == 2*time.Second && !yield(w) {
						return
					}
				}
			}); len(got) != c.waits {
				t.Errorf("the pool waited %v, want %d waits of 2s", h.waitsOf(), c.waits)
			}
		})
	}
}

// TestHandoverFailures: no_gateway, a 503, no answer, an unsigned answer and a server
// error send the handover again after a wait; handed_over releases with retry true;
// lease_expired and 410 drop the dispatch with no release, as over already, and 403
// drops it as refused; a 200 for the pool's other step releases with retry false, as
// handed over for another step, one for a step that is neither explore nor prebuild
// with retry false, as a step the pool program does not run, and one that breaks the
// contract releases with retry false; a lease that ends while it waits, a 429 four times and any other refusal
// release with retry true.
func TestHandoverFailures(t *testing.T) {
	limited := provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "2"}
	noGateway := provisionertest.Reply{Status: 409, Code: "no_gateway"}
	otherStep := func(doc map[string]any) {
		doc["step"] = provisioner.StepPrebuild
		delete(doc, "work_item")
		doc["inputs"] = map[string]any{"version": 1, "environment_ref": "env-3f2a91c4"}
	}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		ran     bool
		release bool
		reason  string
		retry   bool
		again   int
		// drop is the line of a dispatch dropped with no release, the over-already line
		// when empty.
		drop string
	}{
		{name: "no_gateway", replies: []provisionertest.Reply{noGateway, noGateway}, ran: true, again: 2},
		{name: "unavailable", replies: []provisionertest.Reply{{Status: 503, Code: "unavailable"}}, ran: true, again: 1},
		{name: "unsigned", replies: slices.Repeat([]provisionertest.Reply{{Status: 502, Unsigned: true}}, 5), ran: true, again: 1},
		{name: "no answer", replies: slices.Repeat([]provisionertest.Reply{{Status: 503, Drop: true}}, 5), ran: true, again: 1},
		{name: "a server error", replies: slices.Repeat([]provisionertest.Reply{{Status: 500, Code: "internal"}}, 5), ran: true, again: 1},
		{name: "429 three times", replies: []provisionertest.Reply{limited, limited, limited}, ran: true, again: 3},
		{name: "429 four times", replies: []provisionertest.Reply{limited, limited, limited, limited}, release: true, reason: "the run key could not be handed over", retry: true, again: 3},
		{name: "handed_over", replies: []provisionertest.Reply{{Status: 409, Code: "handed_over"}}, release: true, reason: "the run was handed over before, so this machine did not start it", retry: true},
		{name: "lease_expired", replies: []provisionertest.Reply{{Status: 409, Code: "lease_expired"}}},
		{name: "gone", replies: []provisionertest.Reply{{Status: 410, Code: "gone"}}},
		{name: "forbidden", replies: []provisionertest.Reply{{Status: 403, Code: "forbidden"}}, drop: "the handover was refused, so nothing was released"},
		{name: "not_dispatched", replies: []provisionertest.Reply{{Status: 409, Code: "not_dispatched"}}, release: true, reason: "the run key could not be handed over", retry: true},
		{name: "the pool's other step", replies: []provisionertest.Reply{{Edit: otherStep}}, release: true, reason: "the run was handed over for another step than the one it was claimed for"},
		{name: "a step it does not run", replies: []provisionertest.Reply{{Edit: func(doc map[string]any) {
			doc["step"] = "review"
			delete(doc, "work_item")
			doc["inputs"] = map[string]any{"version": 1}
		}}}, release: true, reason: "the pool program runs only the explore and prebuild steps"},
		{name: "another revision", replies: []provisionertest.Reply{{Edit: func(doc map[string]any) { doc["revision"] = 1 }}}, release: true, reason: "the run key could not be handed over"},
		{name: "the lease ends", replies: slices.Repeat([]provisionertest.Reply{noGateway}, 40), release: true, reason: "the lease ended before the run could be started", retry: true, again: -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			h.srv.Script(provisionertest.RouteHandover, c.replies...)
			h.srv.Offer(exploreRun(runA))
			h.start()
			h.waitFor("the dispatch's end", func() bool {
				return h.count(provisionertest.RouteRelease) > 0 || h.count(provisionertest.RouteOutcome) > 0 ||
					len(h.log.with("the dispatch was over already, so nothing was released")) > 0 || len(h.log.with("the handover was refused, so nothing was released")) > 0
			})
			h.settled(1)
			h.stop()
			d := h.only()
			if c.release {
				h.released(d, c.reason, c.retry)
			} else {
				h.notReleased()
			}
			if ran := len(h.launched()) == 1; ran != c.ran {
				t.Errorf("ran %v, want %v; the fake was sent %v", ran, c.ran, h.srv.Routes())
			}
			if again := len(h.log.with("the handover is sent again after a wait")); c.again >= 0 && again != c.again {
				t.Errorf("the handover was sent again %d times, want %d:\n%s", again, c.again, h.log.text())
			}
			if !c.release && !c.ran {
				drop, other := "the dispatch was over already, so nothing was released", "the handover was refused, so nothing was released"
				if c.drop != "" {
					drop, other = c.drop, drop
				}
				if len(h.log.with(drop)) != 1 || len(h.log.with(other)) != 0 {
					t.Errorf("the drop was not logged as %q:\n%s", drop, h.log.text())
				}
			}
		})
	}
	t.Run("the waits double", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.srv.Script(provisionertest.RouteHandover, slices.Repeat([]provisionertest.Reply{noGateway}, 7)...)
		h.srv.Offer(exploreRun(runA))
		h.start()
		h.settled(1)
		h.stop()
		want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
		var got []time.Duration
		for _, w := range h.waitsOf() {
			got = append(got, w/time.Second)
		}
		if !slices.Equal(got, want) {
			t.Errorf("the handover's waits were %v seconds, want %v", got, want)
		}
	})
}

// TestTheLeaseEndsDuringTheHandover gives up a handover that has no answer when the
// lease ends, and releases the run with retry true.
func TestTheLeaseEndsDuringTheHandover(t *testing.T) {
	h := newHarness(t, 1, 1)
	defer h.gate.holdNext("/handover")()
	h.srv.Offer(provisionertest.Run{RunID: runA, Lease: 5 * time.Second})
	h.start()
	<-h.gate.reached
	h.waitFor("the release", func() bool { return h.count(provisionertest.RouteRelease) == 1 })
	h.stop()
	h.released(h.only(), "the lease ended before the run could be started", true)
	if len(h.launched()) != 0 {
		t.Error("the run was launched")
	}
}

// TestTheRelease waits out a signed 429 to a release up to three times, takes 410 and
// lease_expired as the dispatch over already, and logs a release that failed.
func TestTheRelease(t *testing.T) {
	limited := provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "3"}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		line    string
		waits   int
	}{
		{name: "429 twice", replies: []provisionertest.Reply{limited, limited}, line: "released the run", waits: 2},
		{name: "429 four times", replies: []provisionertest.Reply{limited, limited, limited, limited}, line: "the run could not be released", waits: 3},
		{name: "gone", replies: []provisionertest.Reply{{Status: 410, Code: "gone"}}, line: "the dispatch was over already, so nothing was released"},
		{name: "lease_expired", replies: []provisionertest.Reply{{Status: 409, Code: "lease_expired"}}, line: "the dispatch was over already, so nothing was released"},
		{name: "unsigned", replies: slices.Repeat([]provisionertest.Reply{{Status: 500, Unsigned: true}}, 5), line: "the run could not be released"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			h.srv.Script(provisionertest.RouteRelease, c.replies...)
			h.srv.Offer(provisionertest.Run{RunID: runA, Step: otherStep, Inputs: json.RawMessage(`{"version":1}`)})
			h.start()
			h.waitFor("the release's line", func() bool { return len(h.log.with(c.line)) == 1 })
			h.settled(1)
			h.stop()
			n := 0
			for _, w := range h.waitsOf() {
				if w == 3*time.Second {
					n++
				}
			}
			if n != c.waits {
				t.Errorf("the pool waited %v, want %d waits of 3s", h.waitsOf(), c.waits)
			}
		})
	}
}

// TestTheOutcome posts the outcome again, the same bytes, after a failure, and logs
// one it could not post after its tries; the run directory goes either way.
func TestTheOutcome(t *testing.T) {
	t.Run("posted again", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.srv.Script(provisionertest.RouteOutcome, provisionertest.Reply{Status: 500, Code: "internal"})
		h.srv.Offer(exploreRun(runA))
		h.start()
		h.settled(1)
		h.stop()
		var bodies []string
		for _, c := range h.srv.Calls() {
			if c.Route == provisionertest.RouteOutcome {
				bodies = append(bodies, string(c.Body))
			}
		}
		if len(bodies) != 2 || bodies[0] != bodies[1] {
			t.Errorf("the outcome was sent as %q", bodies)
		}
		if o, ok := h.outcome(runA); !ok || o.State != "succeeded" {
			t.Errorf("the outcome is %+v", o)
		}
	})
	t.Run("not posted", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.srv.Script(provisionertest.RouteOutcome, slices.Repeat([]provisionertest.Reply{{Status: 500, Code: "internal"}}, 3)...)
		h.srv.Offer(exploreRun(runA))
		h.start()
		h.waitFor("the line", func() bool { return len(h.log.with("the run's outcome could not be posted")) == 1 })
		h.settled(1)
		h.stop()
		if n := h.count(provisionertest.RouteOutcome); n != 3 {
			t.Errorf("the outcome was sent %d times", n)
		}
		if _, err := os.Stat(explore.Dir(h.pool.Settings.Explore.State, runA)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the run directory is still there: %v", err)
		}
		h.notReleased()
	})
}

// TestTheHandoverIsSentAgainAfterATimeout sends a handover that had no answer in time,
// while the lease lasts, again after its wait: the fake stalls past a short timeout the
// client's transport gives each handover, for every try of the client's twice, and the
// third handover starts the run, with nothing released. The provisioner client sets its HTTP client's Timeout to
// its RequestTimeout, so the short timeout is the transport's; the error is a request's
// timeout either way, one errors.Is reads as context.DeadlineExceeded.
func TestTheHandoverIsSentAgainAfterATimeout(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.pool.Client.HTTP.Transport = timeoutTransport{next: h.pool.Client.HTTP.Transport, d: 300 * time.Millisecond}
	stall := provisionertest.Reply{Status: 503, Code: "unavailable", Hold: 10 * time.Second}
	h.srv.Script(provisionertest.RouteHandover, slices.Repeat([]provisionertest.Reply{stall}, 2*clientTries)...)
	h.srv.Offer(exploreRun(runA))
	h.start()
	h.waitFor("the run's start", func() bool { return len(h.launched()) == 1 })
	h.settled(1)
	h.stop()
	h.notReleased()
	if n := h.count(provisionertest.RouteHandover); n != 2*clientTries+1 {
		t.Errorf("the handover was sent %d times, want %d; the fake was sent %v", n, 2*clientTries+1, h.srv.Routes())
	}
	again := h.log.with("the handover is sent again after a wait")
	if len(again) != 2 {
		t.Fatalf("the handover was sent again %d times, want 2:\n%s", len(again), h.log.text())
	}
	if e, _ := again[0]["error"].(string); !strings.Contains(e, "deadline exceeded") {
		t.Errorf("the first handover ended %q, want its timeout", e)
	}
}

// clientTries is how many tries the provisioner client sends a request in before it
// returns the last one's failure.
const clientTries = 5

// timeoutTransport gives each handover d to be answered in, and passes every request on
// to next.
type timeoutTransport struct {
	next http.RoundTripper
	d    time.Duration
}

func (t timeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/handover") {
		return t.next.RoundTrip(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), t.d)
	resp, err := t.next.RoundTrip(r.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose is an answer's body that ends its request's context once closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelOnClose) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// TestTheClaimBacksOff sends a claim that failed again after a wait that doubles from
// a second to a minute, for an unsigned answer, a 401, no answer, a server error and
// 409 no_gateway; a signed 429 waits its Retry-After; and a claim answered starts the
// waits again from a second. Each wait is logged.
func TestTheClaimBacksOff(t *testing.T) {
	five := func(r provisionertest.Reply) []provisionertest.Reply {
		return slices.Repeat([]provisionertest.Reply{r}, 5)
	}
	noGateway := provisionertest.Reply{Status: 409, Code: "no_gateway"}
	var replies []provisionertest.Reply
	replies = append(replies, five(provisionertest.Reply{Status: 503, Unsigned: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 401, Unsigned: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 503, Drop: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 500, Code: "internal"})...)
	replies = append(replies, noGateway, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: retryAfter}, provisionertest.Reply{Status: 204}, noGateway)
	replies = append(replies, slices.Repeat([]provisionertest.Reply{noGateway}, 7)...)
	h := newHarness(t, 1, 1)
	h.srv.Script(provisionertest.RouteClaim, replies...)
	h.start()
	want := []time.Duration{1, 2, 4, 8, 16, 7, 1, 2, 4, 8, 16, 32, 60, 60}
	h.waitFor("the waits", func() bool { return len(h.waitsOf()) >= len(want) })
	h.stop()
	var got []time.Duration
	for _, w := range h.waitsOf() {
		got = append(got, w/time.Second)
	}
	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("the claim's waits were %v seconds, want %v", got, want)
	}
	m := h.log.with("the claim failed, so it is sent again after a wait")
	if len(m) < len(want) || m[0]["wait"] == nil || m[0]["error"] == nil {
		t.Errorf("the claim's lines are %v", m)
	}
}

// TestAStopDuringAClaimReleasesNothing gives up the claim being held and returns.
func TestAStopDuringAClaimReleasesNothing(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.srv.Hold = time.Minute
	h.start()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the pool took %s to stop", time.Since(start))
	}
	h.notReleased()
}

// TestAStopAtEachPhase stops the pool at each phase of a dispatch: before the handover
// the run is released with retry true, after it and before the start with retry false,
// both the pool program stopped before the run started; a running run is stopped
// through its launcher's context and posts cancelled, pool_stopped; a run stopped
// before its record is released with retry false; a build stopped is released with
// retry false, for the reason its launcher gives. Run returns nil each time.
func TestAStopAtEachPhase(t *testing.T) {
	const before = "the pool program stopped before the run started"
	t.Run("dispatched in flight", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		let := h.gate.holdNext("/dispatched")
		h.srv.Offer(exploreRun(runA))
		h.start()
		<-h.gate.reached
		stopped := make(chan error, 1)
		go func() { stopped <- h.stop() }()
		time.Sleep(100 * time.Millisecond)
		let()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		h.released(h.only(), before, true)
		if h.count(provisionertest.RouteHandover) != 0 {
			t.Error("the handover was sent")
		}
	})
	t.Run("a wait of dispatched", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "2"})
		h.setSleep(func(ctx context.Context, d time.Duration) error {
			if d == 2*time.Second {
				go h.cancel()
				<-ctx.Done()
			}
			return ctx.Err()
		})
		h.srv.Offer(exploreRun(runA))
		h.start()
		h.waitFor("the release", func() bool { return h.count(provisionertest.RouteRelease) == 1 })
		h.stop()
		h.released(h.only(), before, true)
	})
	t.Run("the handover in flight", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		let := h.gate.holdNext("/handover")
		h.srv.Offer(exploreRun(runA))
		h.start()
		<-h.gate.reached
		stopped := make(chan error, 1)
		go func() { stopped <- h.stop() }()
		time.Sleep(100 * time.Millisecond)
		let()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		d := h.only()
		if !d.HandedOver {
			t.Fatal("the handover was not answered")
		}
		h.released(d, before, false)
		if len(h.launched()) != 0 {
			t.Error("the run was launched")
		}
	})
	t.Run("a wait of the handover", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		h.srv.Script(provisionertest.RouteHandover, provisionertest.Reply{Status: 409, Code: "no_gateway"})
		h.setSleep(func(ctx context.Context, d time.Duration) error {
			go h.cancel()
			<-ctx.Done()
			return ctx.Err()
		})
		h.srv.Offer(exploreRun(runA))
		h.start()
		h.waitFor("the release", func() bool { return h.count(provisionertest.RouteRelease) == 1 })
		h.stop()
		h.released(h.only(), before, true)
	})
	for _, c := range []struct {
		name   string
		record bool
	}{{"a running run", true}, {"a run before its record", false}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			running := make(chan struct{})
			h.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
				close(running)
				<-ctx.Done()
				res := child.Result{ExitCode: 1, Stopped: true}
				return explore.Result{Record: c.record, Run: res, Ending: explore.EndingOf(c.record, res)}, nil
			})
			h.srv.Offer(exploreRun(runA))
			h.start()
			<-running
			if err := h.stop(); err != nil {
				t.Fatal(err)
			}
			o, posted := h.outcome(runA)
			if c.record {
				h.notReleased()
				if !posted || o.State != "cancelled" || o.Reason != "pool_stopped" {
					t.Errorf("the outcome is %+v, %v", o, posted)
				}
			} else {
				h.released(h.only(), before, false)
				if posted {
					t.Errorf("an outcome was posted: %+v", o)
				}
			}
		})
	}
	t.Run("a build", func(t *testing.T) {
		h := newHarness(t, 1, 1)
		building := make(chan struct{})
		h.pool.Prebuild = func(ctx context.Context, d pool.Prebuild) explore.Ending {
			d.Key.Clear()
			close(building)
			<-ctx.Done()
			return explore.Ending{Release: true, ReleaseReason: "the pool program stopped during the build", Post: true, State: explore.StateFailed}
		}
		h.srv.Offer(prebuildRun(runA))
		h.start()
		<-building
		if err := h.stop(); err != nil {
			t.Fatal(err)
		}
		h.released(h.only(), "the pool program stopped during the build", false)
		if o, ok := h.outcome(runA); !ok || o.State != "failed" {
			t.Errorf("the outcome is %+v", o)
		}
	})
}

// journalOf is the journal's lines.
func journalOf(t *testing.T, state string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, pool.JournalFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// TestTheJournalAfterAKill kills the pool at each phase, by taking the journal as it is
// on disk then, and starts a second pool with it: before its first claim, the second
// releases the dispatch, with retry true when the kill came before the handover was
// sent, and retry false once it was being sent and after the start, for the pool
// program restarted. The journal is 0600 in
// a 0700 folder and holds the ids, the step and the phase alone.
func TestTheJournalAfterAKill(t *testing.T) {
	for _, c := range []struct {
		phase string
		hold  string
		retry bool
	}{
		{pool.PhaseClaimed, "/dispatched", true},
		{pool.PhaseHandover, "/handover", false},
		{pool.PhaseStarted, "", false},
	} {
		t.Run(c.phase, func(t *testing.T) {
			first := newHarness(t, 1, 1)
			running := make(chan struct{}, 1)
			first.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
				running <- struct{}{}
				<-ctx.Done()
				return explore.Result{Ending: explore.EndingOf(true, child.Result{ExitCode: 1, Stopped: true})}, nil
			})
			if c.hold != "" {
				defer first.gate.holdNext(c.hold)()
			}
			first.srv.Offer(exploreRun(runA))
			first.start()
			if c.hold != "" {
				<-first.gate.reached
			} else {
				<-running
			}
			d := first.only()
			lines := journalOf(t, first.state)
			if want := d.ID + " " + runA + " explore " + c.phase; len(lines) != 1 || lines[0] != want {
				t.Fatalf("the journal is %q, want %q", lines, want)
			}
			info, err := os.Stat(filepath.Join(first.state, pool.JournalFile))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the journal: %v, %v", info, err)
			}
			if info, err := os.Stat(first.state); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("the pool's folder: %v, %v", info, err)
			}
			b, _ := os.ReadFile(filepath.Join(first.state, pool.JournalFile))
			first.gate.close()

			second := newHarness(t, 1, 1)
			second.srv = first.srv
			second.pool.Client = first.srv.Client()
			second.pool.Client.Now = first.clk.Now
			second.pool.Outcomes = first.pool.Outcomes
			if err := os.MkdirAll(second.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(second.state, pool.JournalFile), b, 0o600); err != nil {
				t.Fatal(err)
			}
			before := len(first.srv.Calls())
			second.start()
			second.waitFor("the release", func() bool {
				return slices.ContainsFunc(first.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteRelease })
			})
			second.waitFor("a claim", func() bool {
				calls := first.srv.Calls()[before:]
				return slices.ContainsFunc(calls, func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
			})
			second.stop()
			first.stop()
			second.released(d, "the pool program restarted before the run ended", c.retry)
			calls := first.srv.Calls()[before:]
			if calls[0].Route != provisionertest.RouteRelease || calls[0].DispatchID != d.ID {
				t.Errorf("the second pool's first request was %s %s", calls[0].Route, calls[0].DispatchID)
			}
			if lines := journalOf(t, second.state); len(lines) != 0 {
				t.Errorf("the second pool's journal is %q", lines)
			}
		})
	}
}

// claimTo brings a run of the fake to phase, as a pool killed then would have left it,
// and returns its journal entry.
func claimTo(t *testing.T, srv *provisionertest.Server, step, phase string) pool.Entry {
	t.Helper()
	ctx := context.Background()
	c := srv.Client()
	claimed, ok, err := c.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("%v, %v", ok, err)
	}
	if phase != pool.PhaseClaimed {
		if err := c.Dispatched(ctx, claimed.DispatchID); err != nil {
			t.Fatal(err)
		}
	}
	if phase == pool.PhaseHandedOver || phase == pool.PhaseStarted {
		h, err := c.Handover(ctx, claimed.DispatchID, claimed.RunID)
		if err != nil {
			t.Fatal(err)
		}
		h.RunKey.Clear()
	}
	return pool.Entry{DispatchID: claimed.DispatchID, RunID: claimed.RunID, Step: step, Phase: phase}
}

// TestTheJournalIsReplayedBeforeTheFirstClaim releases each entry of the journal it
// starts with, for the pool program restarted: claimed and dispatched with retry true,
// handed_over and started with retry false. One over already, 410, leaves the journal
// as one released does, and so does its run directory, and one refused 403 too; one
// Qory Apiary does not answer stays for the next start.
func TestTheJournalIsReplayedBeforeTheFirstClaim(t *testing.T) {
	h := newHarness(t, 1, 1)
	ids := h.srv.Offer(exploreRun(runA), exploreRun(runB), exploreRun(runC), prebuildRun(""), exploreRun(""), exploreRun(""))
	var entries []pool.Entry
	for i, phase := range []string{pool.PhaseClaimed, pool.PhaseDispatched, pool.PhaseHandedOver, pool.PhaseStarted, pool.PhaseStarted, pool.PhaseDispatched} {
		step := provisioner.StepExplore
		if i == 3 {
			step = provisioner.StepPrebuild
		}
		entries = append(entries, claimTo(t, h.srv, step, phase))
	}
	h.srv.End(entries[4].DispatchID)
	// A dispatch the fake does not know is a signed 403, which no later start would
	// change.
	entries = append(entries[:5], pool.Entry{DispatchID: "dp_0000000000000000", RunID: "0192f0c1-7d4e-7a2b-8c3d-0000000000ff", Step: provisioner.StepExplore, Phase: pool.PhaseClaimed}, entries[5])
	h.srv.Script(provisionertest.RouteRelease, slices.Repeat([]provisionertest.Reply{{}}, 6)...)
	h.srv.Script(provisionertest.RouteRelease, slices.Repeat([]provisionertest.Reply{{Status: 503, Unsigned: true}}, 5)...)
	var journal string
	for _, e := range entries {
		journal += fmt.Sprintf("%s %s %s %s\n", e.DispatchID, e.RunID, e.Step, e.Phase)
		if err := os.MkdirAll(explore.Dir(h.pool.Settings.Explore.State, e.RunID), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(h.srv.Calls())
	h.start()
	h.waitFor("a claim", func() bool {
		return slices.ContainsFunc(h.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
	})
	h.stop()
	const reason = "the pool program restarted before the run ended"
	for i, retry := range []bool{true, true, false, false} {
		d, _ := h.srv.Dispatch(entries[i].DispatchID)
		h.released(d, reason, retry)
	}
	calls := h.srv.Calls()[before:]
	for i, c := range calls[:6] {
		if c.Route != provisionertest.RouteRelease || c.DispatchID != entries[i].DispatchID {
			t.Errorf("request %d was %s %s, want the release of %s", i, c.Route, c.DispatchID, entries[i].DispatchID)
		}
	}
	if len(h.log.with("the dispatch was over already, so nothing was released")) != 1 {
		t.Errorf("the ended dispatch's release was not logged as over already:\n%s", h.log.text())
	}
	lines := journalOf(t, h.state)
	if want := entries[6].DispatchID + " " + entries[6].RunID + " explore dispatched"; len(lines) != 1 || lines[0] != want {
		t.Errorf("the journal is %q, want %q", lines, want)
	}
	for i, e := range entries {
		_, err := os.Stat(explore.Dir(h.pool.Settings.Explore.State, e.RunID))
		if gone := errors.Is(err, os.ErrNotExist); gone != (i < 6) {
			t.Errorf("the run directory of entry %d: %v", i, err)
		}
	}
	_ = ids
}

// TestAHandoverJournaledIsNeverQueuedAgain starts with a journal that holds two
// dispatches at their handover, as a pool killed then leaves it: one whose handover was
// answered 200, the kill before handed_over was written, and one whose handover never
// left. Each is released for the pool program restarted with retry false, and never with
// retry true, so neither run is queued again.
func TestAHandoverJournaledIsNeverQueuedAgain(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.srv.Offer(exploreRun(runA), exploreRun(runB))
	handed := claimTo(t, h.srv, provisioner.StepExplore, pool.PhaseDispatched)
	got, err := h.srv.Client().Handover(context.Background(), handed.DispatchID, handed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got.RunKey.Clear()
	unsent := claimTo(t, h.srv, provisioner.StepExplore, pool.PhaseDispatched)
	handed.Phase, unsent.Phase = pool.PhaseHandover, pool.PhaseHandover
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := fmt.Sprintf("%s %s %s %s\n%s %s %s %s\n", handed.DispatchID, handed.RunID, handed.Step, handed.Phase,
		unsent.DispatchID, unsent.RunID, unsent.Step, unsent.Phase)
	if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(h.srv.Calls())
	h.start()
	h.waitFor("a claim", func() bool {
		return slices.ContainsFunc(h.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
	})
	h.stop()
	for _, e := range []pool.Entry{handed, unsent} {
		n := 0
		for _, c := range h.srv.Calls() {
			if c.Route != provisionertest.RouteRelease || c.DispatchID != e.DispatchID {
				continue
			}
			n++
			var rel contracts.Release
			if err := json.Unmarshal(c.Body, &rel); err != nil {
				t.Fatal(err)
			}
			if rel.Retry || rel.Reason != "the pool program restarted before the run ended" {
				t.Errorf("%s was released for %q, retry %v", e.DispatchID, rel.Reason, rel.Retry)
			}
		}
		if n != 1 {
			t.Errorf("%s was released %d times, want once", e.DispatchID, n)
		}
	}
	if lines := journalOf(t, h.state); len(lines) != 0 {
		t.Errorf("the journal is %q", lines)
	}
}

// TestAMalformedJournalIsRefused refuses a journal it cannot read, with a clear error,
// and claims nothing.
func TestAMalformedJournalIsRefused(t *testing.T) {
	const entry = "dp_0000000000000000 0192f0c1-7d4e-7a2b-8c3d-00000000000a explore claimed"
	for _, c := range []struct {
		name, content string
		link          bool
		line          int
	}{
		{name: "a line of three", content: entry + "\ndp_0000000000000001 0192f0c1-7d4e-7a2b-8c3d-00000000000a explore\n", line: 2},
		{name: "no line end", content: entry, line: 1},
		{name: "an unknown phase", content: "dp_0000000000000000 0192f0c1-7d4e-7a2b-8c3d-00000000000a explore running\n", line: 1},
		{name: "a dispatch twice", content: entry + "\n" + entry + "\n", line: 2},
		{name: "a run id out of shape", content: "dp_0000000000000000 run-a explore claimed\n", line: 1},
		{name: "two spaces", content: "dp_0000000000000000  0192f0c1-7d4e-7a2b-8c3d-00000000000a explore claimed\n", line: 1},
		{name: "a link", link: true},
		{name: "too large", content: strings.Repeat(entry+"\n", 1<<20/len(entry)+1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			if err := os.MkdirAll(h.state, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.state, pool.JournalFile)
			if c.link {
				target := filepath.Join(h.root, "elsewhere")
				if err := os.WriteFile(target, []byte(entry+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := h.pool.Run(context.Background())
			want := path + " is not a journal of the pool's dispatches: it is not a regular file of at most 1 MiB, so no run is claimed"
			if c.line > 0 {
				want = fmt.Sprintf("%s is not a journal of the pool's dispatches: its line %d is not a dispatch id, a run id, a step and a phase, once per dispatch, so no run is claimed", path, c.line)
			}
			if err == nil || err.Error() != want {
				t.Errorf("%v, want %q", err, want)
			}
			if n := len(h.srv.Calls()); n != 0 {
				t.Errorf("the fake was sent %d requests", n)
			}
		})
	}
}

// TestTheSettingsAreChecked refuses to run by settings it cannot.
func TestTheSettingsAreChecked(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.pool.Prebuild = nil
	if err := h.pool.Run(context.Background()); err == nil || err.Error() != "the pool has no launcher of builds" {
		t.Errorf("no launcher of builds: %v", err)
	}
	for _, c := range []struct{ runs, prebuilds int }{{0, 1}, {1, 0}, {1, 2}} {
		h := newHarness(t, c.runs, c.prebuilds)
		if err := h.pool.Run(context.Background()); err == nil {
			t.Errorf("runs %d and prebuilds %d were taken", c.runs, c.prebuilds)
		}
	}
	h = newHarness(t, 1, 1)
	h.pool.Settings.State = "pool"
	if err := h.pool.Run(context.Background()); err == nil || err.Error() != `the pool's folder "pool" is not an absolute path` {
		t.Errorf("a relative folder: %v", err)
	}
}

// compact is b compacted, as the handover's answer carries it.
func compact(t *testing.T, b []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
