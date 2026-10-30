package prebuild_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/pool/prebuild"
)

// closing is a run open at a gateway in this process, which records each close and
// what Forager's Run.Close returned for it. Its join is the fake join of e: no wall runs
// in a test.
type closing struct {
	*session.Run
	e      *env
	outs   []session.Outcome
	closed []*session.Closed
	errs   []error
}

func (c *closing) Join(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
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

// TestLaunchClosesAMissingEnvironmentImageAsForagerTakesIt is a prebuild from an
// environment image that is not in its registry, through a gateway in this process:
// Forager's Run.Close takes the close, failed with image_missing and -1 in a run no
// session joined.
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
	if c := run.closed[0]; c.State != "failed" || c.Reason != "image_missing" || c.ExitCode != -1 || c.RunClosed {
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
