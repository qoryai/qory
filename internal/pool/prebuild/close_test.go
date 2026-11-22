package prebuild_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/pool/prebuild"
)

// closing is a run open at a gateway in this process, which records each close and
// what Forager's Run.Close returned for it. Its join is a real join of a command that
// does nothing, so the gateway numbers a join of the run as it does for the step's session,
// and then the fake join of e: no wall runs in a test.
type closing struct {
	*session.Run
	e      *env
	outs   []session.Outcome
	closed []*session.Closed
	errs   []error
}

func (c *closing) Join(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
	if _, err := c.Run.Join(ctx, session.SessionSpec{Command: "/usr/bin/true", Dir: spec.Dir, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		return nil, err
	}
	return c.e.join(ctx, spec)
}

func (c *closing) Close(ctx context.Context, out session.Outcome) (*session.Closed, error) {
	closed, err := c.Run.Close(ctx, out)
	c.outs, c.closed, c.errs = append(c.outs, out), append(c.closed, closed), append(c.errs, err)
	return closed, err
}

// events is the numbered events a gateway writes, one JSON line each.
type events struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (e *events) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.b.Write(p)
}

// of is the data of each event of the type typ.
func (e *events) of(t *testing.T, typ string) []map[string]any {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var got []map[string]any
	for line := range strings.Lines(e.b.String()) {
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if ev.Type == typ {
			got = append(got, ev.Data)
		}
	}
	return got
}

// throughGateway makes e's launch open its run at a gateway in this process, with no
// wall, and returns the gateway's events and what returns the run once it is open. The
// gateway in this process applies no server's run configuration, so the run's policy is
// taken as the server's.
func throughGateway(t *testing.T, e *env) (*events, func() *closing) {
	t.Helper()
	evs := &events{}
	g, err := gateway.Start(context.Background(), gateway.Config{Dir: t.TempDir(), NoLinkSocket: true, Version: "test", Events: evs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		g.Close(ctx)
	})
	t.Cleanup(prebuild.SetPolicySource(func(string) string { return "fetched" }))
	var run *closing
	e.settings.Open = func(ctx context.Context, spec session.OpenSpec) (prebuild.Opened, error) {
		spec.Gateway = session.LocalGateway(g.LocalLink())
		spec.Wall, spec.Image, spec.Images, spec.CAEnv = nil, "", nil, nil
		r, err := session.Open(ctx, spec)
		if err != nil {
			return nil, err
		}
		run = &closing{Run: r, e: e}
		return run, nil
	}
	return evs, func() *closing { return run }
}

// TestLaunchClosesAnImageOverTheLimitWithItsEndCode is a prebuild whose repository image is over
// the limit, through a gateway in this process: Forager's Run.Close takes the close, failed with
// prebuilt_image_too_large and -1, as the session's check of the outcome has it.
func TestLaunchClosesAnImageOverTheLimitWithItsEndCode(t *testing.T) {
	e := newEnv(t)
	e.largest = 5000000000
	_, opened := throughGateway(t, e)
	e.script.Build = "toobig"
	if _, err := e.launch(t, false); err == nil {
		t.Error("the launch succeeded")
	}
	run := opened()
	if run == nil || len(run.outs) != 1 {
		t.Fatalf("the run was closed as %+v", run)
	}
	if run.outs[0] != closedTooLarge || run.errs[0] != nil {
		t.Fatalf("the close %+v: %v", run.outs[0], run.errs[0])
	}
	if c := run.closed[0]; c.State != "failed" || c.Reason != "prebuilt_image_too_large" || c.ExitCode != -1 {
		t.Errorf("Run.Close returned %+v", c)
	}
}

// TestLaunchClosesAMissingEnvironmentImageAsForagerTakesIt is a prebuild from an
// environment image that is not in its registry, through a gateway in this process:
// Forager's Run.Close takes the close, failed with environment_image_missing and -1, after the
// step's session joined.
func TestLaunchClosesAMissingEnvironmentImageAsForagerTakesIt(t *testing.T) {
	e := newEnv(t)
	_, opened := throughGateway(t, e)
	e.script.EnvironmentImage, e.script.Build = testEnvImage, "missing"
	if _, err := e.launch(t, false); err == nil {
		t.Error("the launch succeeded")
	}
	run := opened()
	if run == nil || len(run.outs) != 1 {
		t.Fatalf("the run was closed as %+v", run)
	}
	if run.outs[0] != closedImageMissing || run.errs[0] != nil {
		t.Fatalf("the close %+v: %v", run.outs[0], run.errs[0])
	}
	if c := run.closed[0]; c.State != "failed" || c.Reason != "environment_image_missing" || c.ExitCode != -1 || c.RunClosed {
		t.Errorf("Run.Close returned %+v", c)
	}
}

// TestLaunchWritesBuiltAsForagerTakesIt is a prebuild that pushes its environment image
// and its target image, through a gateway in this process: Forager's Run.Built writes
// dev.qory.run.built with the target image, its size, the commit, the environment_ref,
// the environment image and the base image, and the run closes succeeded with -1.
func TestLaunchWritesBuiltAsForagerTakesIt(t *testing.T) {
	e := newEnv(t)
	evs, opened := throughGateway(t, e)
	e.script.Environment, e.script.Base = testEnvironmentTag, testBase
	if _, err := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	run := opened()
	if run == nil || !slices.Equal(run.outs, []session.Outcome{closedSucceeded}) || run.errs[0] != nil {
		t.Fatalf("the run was closed as %+v", run)
	}
	digest := "sha256:" + sum([]byte(manifestOf("application/vnd.oci.image.manifest.v1+json", 100, 20, 3)))
	want := map[string]any{
		"image": testDestination + "@" + digest, "size": float64(123), "commit": testCommit, "environment_ref": "3f2a1b0c9d8e",
		"environment_image": testEnvironmentDestination + "@" + digest, "base": testBase,
	}
	got := evs.of(t, "dev.qory.run.built")
	if len(got) != 1 {
		t.Fatalf("dev.qory.run.built written %d times", len(got))
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("dev.qory.run.built's %s is %v, want %v", k, got[0][k], v)
		}
	}
}

// TestLaunchClosesOnceWithAFailedClose is a run whose close fails: after a failed build
// the launch logs the close's line and returns the build's error; after a build that
// succeeded the close's error is the launch's.
func TestLaunchClosesOnceWithAFailedClose(t *testing.T) {
	const closeLine = "update qory: the run could not be closed: the run is closed already"
	for _, c := range []struct {
		build string
		want  string
		log   bool
	}{
		{"missing", "prebuild: check that the Docker engine reaches the registry of r.example/env:1", true},
		{"", closeLine, false},
	} {
		t.Run("build "+c.build, func(t *testing.T) {
			e := newEnv(t)
			e.script.EnvironmentImage, e.script.Build, e.script.Close = testEnvImage, c.build, "fail"
			_, err := e.launch(t, false)
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("err %v, want %q", err, c.want)
			}
			if got := slices.Contains(e.lines(), testRunID+": "+closeLine); got != c.log {
				t.Errorf("logged\n%s\nwant the close's line %v", strings.Join(e.lines(), "\n"), c.log)
			}
			if e.run == nil || len(e.run.built) != 1 || e.run.built[0] != 1 {
				t.Errorf("the run closed as %+v", e.run)
			}
		})
	}
}

// TestForagerTakesImageMissingOnlyBeforeAJoin is a run at a gateway in this process: after a
// session joined, Run.Close refuses image_missing, which is for a close before a join, and takes
// the end code of the prebuild, environment_image_missing, with the same state and exit code. A
// close before a join takes image_missing, so the fetch's refusals keep it.
func TestForagerTakesImageMissingOnlyBeforeAJoin(t *testing.T) {
	ctx := context.Background()
	g, err := gateway.Start(ctx, gateway.Config{Dir: t.TempDir(), NoLinkSocket: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		g.Close(closeCtx)
	})
	open := func() *session.Run {
		r, err := session.Open(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}, Gateway: session.LocalGateway(g.LocalLink())})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	missing := session.Outcome{State: "failed", Reason: "image_missing", ExitCode: -1}
	before := open()
	if _, err := before.Close(ctx, missing); err != nil {
		t.Errorf("a close before a join refused image_missing: %v", err)
	}
	joined := open()
	if _, err := joined.Join(ctx, session.SessionSpec{Command: "/usr/bin/true", Dir: t.TempDir(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := joined.Close(ctx, missing); err == nil || !strings.Contains(err.Error(), "no session joined") {
		t.Errorf("a close after a join with image_missing returned %v, want the refusal", err)
	}
	if _, err := joined.Close(ctx, closedImageMissing); err != nil {
		t.Errorf("a close after a join with %q refused it: %v", closedImageMissing.Reason, err)
	}
}
