package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/forager/wall"
	"github.com/qoryai/qory/internal/compose"
)

// engineScript is a docker command whose context show prints the contexts in order,
// one a call, the last one again once they run out, and whose info prints the id of
// the engine the context selects, id-<DOCKER_CONTEXT>.
func engineScript(t *testing.T, contexts ...string) string {
	t.Helper()
	dir := t.TempDir()
	list := filepath.Join(dir, "contexts")
	content := ""
	for _, c := range contexts {
		content += c + "\n"
	}
	if err := os.WriteFile(list, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "docker")
	body := "#!/bin/sh\ncase \"$1\" in\ncontext)\n\thead -n 1 " + list + "\n\tif [ \"$(wc -l < " + list + ")\" -gt 1 ]; then sed -i.bak 1d " + list + "; fi ;;\ninfo) echo \"id-$DOCKER_CONTEXT\" ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// unsetenv unsets name for the test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

// TestTheChecksEngineIsTheWalls is the engine of a run's push checks: with a selection
// the wall recorded, and an engine that has the id it had then, the wall's command by
// its absolute path with that selection; with none recorded, the wall's command with
// this process's environment; with a selection that reaches another engine now, an
// error with the fix first.
func TestTheChecksEngineIsTheWalls(t *testing.T) {
	unsetenv(t, "DOCKER_HOST")
	unsetenv(t, "DOCKER_CONTEXT")
	t.Setenv("DOCKER_CONFIG", "/wall/config")
	ctx := context.Background()

	script := engineScript(t, "a")
	d := &wall.Docker{Command: script}
	if got, err := checkEngine(ctx, d); err != nil || got.Command != script || got.Env != nil {
		t.Errorf("with no recorded selection: %+v, %v, want the wall's command alone", got, err)
	}
	d.Engine(ctx)
	got, err := checkEngine(ctx, d)
	if err != nil || got.Command != script || !slices.Contains(got.Env, "DOCKER_CONTEXT=a") || !slices.Contains(got.Env, "DOCKER_CONFIG=/wall/config") {
		t.Errorf("with the recorded selection: %+v, want the command and the selection", got)
	}

	// The context changed after the open: the new selection reaches another engine.
	other := engineScript(t, "a", "b")
	d = &wall.Docker{Command: other}
	d.Engine(ctx)
	want := `Switch the engine back to the one the run started on, and run again. The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a".`
	if got, err := checkEngine(ctx, d); err == nil || err.Error() != want {
		t.Errorf("with another engine now: %+v, %v, want %q", got, err, want)
	}
}

// runAPIOnly is an opened run whose RunAPI returns base and err; its other methods are
// not called.
type runAPIOnly struct {
	openedRun
	base string
	err  error
}

func (r runAPIOnly) RunAPI() (*http.Client, string, error) {
	return http.DefaultClient, r.base, r.err
}

func TestTheRunAPIOfARunWithPushChecksMustWork(t *testing.T) {
	was := loadPushChecks
	t.Cleanup(func() { loadPushChecks = was })
	loadPushChecks = func(string) ([]compose.PushCheck, error) {
		return []compose.PushCheck{{Type: "proposal"}}, nil
	}
	spec := &runSpec{}
	spec.Wall = &wall.Docker{}
	closed := errors.New("Open another run to reach the run API: this run is closed.")
	for _, c := range []struct {
		name         string
		run          runAPIOnly
		controlPlane bool
		want         string
	}{
		{"no control plane", runAPIOnly{err: closed}, false, ""},
		{"a run API that fails", runAPIOnly{err: closed}, true, "Open the run through a gateway that sends the run API address, and run again. The push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed."},
		{"a bad address", runAPIOnly{base: "ftp://run-api.qory.invalid/v1/runs/r1"}, true, `Update the run's gateway, and run again. The run API address that the gateway sent is not one the push checks can use: Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>. It needs http or https and no user, query or fragment. The value without those is "ftp://run-api.qory.invalid/v1/runs/r1".`},
	} {
		t.Run(c.name, func(t *testing.T) {
			checker, err := pushChecker(context.Background(), c.run, t.TempDir(), "r1", spec, c.controlPlane)
			if checker != nil {
				t.Errorf("checker %v, want none", checker)
			}
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.want {
				t.Errorf("error %q, want %q", got, c.want)
			}
		})
	}
}
