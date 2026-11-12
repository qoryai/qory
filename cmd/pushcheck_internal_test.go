package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pushcheck"
)

// engineScript is a docker command whose context show prints the contexts in order,
// one a call, the last one again once they run out, and whose info prints the id of
// the engine the context selects, id-<DOCKER_CONTEXT>, and exits 1 for the context none.
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
	body := "#!/bin/sh\ncase \"$1\" in\ncontext)\n\thead -n 1 " + list + "\n\tif [ \"$(wc -l < " + list + ")\" -gt 1 ]; then sed -i.bak 1d " + list + "; fi ;;\ninfo) [ \"$DOCKER_CONTEXT\" != none ] || exit 1; echo \"id-$DOCKER_CONTEXT\" ;;\nesac\n"
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
	if got, err := checkEngine(ctx, d, false); err != nil || got.Command != script || got.Env != nil {
		t.Errorf("with no recorded selection: %+v, %v, want the wall's command alone", got, err)
	}
	d.Engine(ctx)
	got, err := checkEngine(ctx, d, false)
	if err != nil || got.Command != script || !slices.Contains(got.Env, "DOCKER_CONTEXT=a") || !slices.Contains(got.Env, "DOCKER_CONFIG=/wall/config") {
		t.Errorf("with the recorded selection: %+v, want the command and the selection", got)
	}

	// The context changed after the open: the new selection reaches another engine.
	for fromCode, want := range map[bool]string{
		false: `Switch the engine back to the one the run started on, and run again. The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a".`,
		true:  `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts: the push checks would run on the engine "id-b", and the run's containers are on the engine "id-a"; the code is used`,
	} {
		other := engineScript(t, "a", "b")
		d = &wall.Docker{Command: other}
		d.Engine(ctx)
		if got, err := checkEngine(ctx, d, fromCode); err == nil || err.Error() != want {
			t.Errorf("with another engine now, from a run code %v: %+v, %v, want %q", fromCode, got, err, want)
		}
	}
}

// runAPIOnly is an opened run with a run API, for the load of its push checks: RunAPI
// returns base and err, Dir returns dir, and Image returns no image. Its other methods are
// not called.
type runAPIOnly struct {
	openedRun
	base, dir string
	err       error
}

func (r runAPIOnly) RunAPI() (*http.Client, string, error) {
	return http.DefaultClient, r.base, r.err
}

func (r runAPIOnly) Dir() string { return r.dir }

func (r runAPIOnly) Image() session.Image { return session.Image{} }

// TestTheLoadOfPushChecksStopsWithItsLine is each stop of the load, with the line of a run
// with no run code and the line of a run from a run code.
func TestTheLoadOfPushChecksStopsWithItsLine(t *testing.T) {
	was := loadPushChecks
	t.Cleanup(func() { loadPushChecks = was })
	closed := errors.New("Open another run to reach the run API: this run is closed.")
	const goodAPI = "http://run-api.qory.invalid/v1/runs/r1"
	home := t.TempDir()
	gone := filepath.Join(t.TempDir(), "gone")
	folder := filepath.Join(gone, pushChecksDir)
	folderCause := "mkdir " + folder + ": no such file or directory"
	unsetenv(t, "DOCKER_HOST")
	unsetenv(t, "DOCKER_CONTEXT")
	// The wall recorded the context a at the open, and the next selection reads the context b.
	otherEngine := &wall.Docker{Command: engineScript(t, "a", "b")}
	otherEngine.Engine(context.Background())
	// The same, with the next selection on an engine whose id cannot be read.
	noIDEngine := &wall.Docker{Command: engineScript(t, "a", "none")}
	noIDEngine.Engine(context.Background())
	for _, c := range []struct {
		name         string
		run          runAPIOnly
		unread       bool
		noWall       bool
		docker       *wall.Docker
		controlPlane bool
		want         string
		fromCode     string
	}{
		{name: "no control plane", run: runAPIOnly{err: closed}},
		{
			name:         "a run API that fails",
			run:          runAPIOnly{err: closed},
			controlPlane: true,
			want:         "Open the run through a gateway that sends the run API address, and run again. The push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed.",
			fromCode:     "Take the work item in again in Qory Apiary, through a gateway that sends the run API address: the push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed; the code is used",
		},
		{
			name:         "a bad address",
			run:          runAPIOnly{base: "ftp://run-api.qory.invalid/v1/runs/r1"},
			controlPlane: true,
			want:         `Update the run's gateway, and run again. The run API address that the gateway sent is not one the push checks can use: Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>. It needs http or https and no user, query or fragment. The value without those is "ftp://run-api.qory.invalid/v1/runs/r1".`,
			fromCode:     `Update the run's gateway, then take the work item in again in Qory Apiary: the run API address that the gateway sent is not one the push checks can use: Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>. It needs http or https and no user, query or fragment. The value without those is "ftp://run-api.qory.invalid/v1/runs/r1"; the code is used`,
		},
		{
			name:         "a wall that is not docker",
			noWall:       true,
			controlPlane: true,
			want:         "Set wall.adapter to docker, and run again. The push checks of " + home + " run in containers of the docker wall alone.",
			fromCode:     "Set wall.adapter to docker, then take the work item in again in Qory Apiary: the push checks of " + home + " run in containers of the docker wall alone; the code is used",
		},
		{
			name:         "no folder",
			run:          runAPIOnly{base: goodAPI, dir: gone},
			controlPlane: true,
			want:         "Check " + folder + ", and run again. Making the folder of the push checks' files failed: " + folderCause + ".",
			fromCode:     "Check " + folder + ", then take the work item in again in Qory Apiary: making the folder of the push checks' files failed: " + folderCause + "; the code is used",
		},
		{
			name:         "another engine",
			run:          runAPIOnly{base: goodAPI},
			docker:       otherEngine,
			controlPlane: true,
			want:         `Switch the engine back to the one the run started on, and run again. The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a".`,
			fromCode:     `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts: the push checks would run on the engine "id-b", and the run's containers are on the engine "id-a"; the code is used`,
		},
		{
			name:         "an engine whose id cannot be read",
			run:          runAPIOnly{base: goodAPI},
			docker:       noIDEngine,
			controlPlane: true,
			want:         `Switch the engine back to the one the run started on, and run again. The push checks would run on the engine with no id, and the run's containers are on the engine "id-a".`,
			fromCode:     `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts: the push checks would run on the engine with no id, and the run's containers are on the engine "id-a"; the code is used`,
		},
		{
			name:     "push checks that do not read",
			unread:   true,
			want:     "Compose the harness again, and run again. The push checks in /h/push-checks.json could not be read: a cause.",
			fromCode: "Take the work item in again in Qory Apiary, so that the new run composes the harness again: the push checks in /h/push-checks.json could not be read: a cause; the code is used",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			loadPushChecks = func(string) ([]compose.PushCheck, error) {
				if c.unread {
					return nil, &pushcheck.UnreadError{File: "/h/push-checks.json", Cause: "a cause"}
				}
				return []compose.PushCheck{{Type: "proposal"}}, nil
			}
			spec := &runSpec{}
			if c.docker != nil {
				spec.Wall = c.docker
			} else if !c.noWall {
				spec.Wall = &wall.Docker{}
			}
			for fromCode, want := range map[bool]string{false: c.want, true: c.fromCode} {
				checker, err := pushChecker(context.Background(), c.run, home, "r1", spec, c.controlPlane, fromCode)
				if checker != nil {
					t.Errorf("from a run code %v: checker %v, want none", fromCode, checker)
				}
				got := ""
				if err != nil {
					got = err.Error()
				}
				if got != want {
					t.Errorf("from a run code %v: error %q, want %q", fromCode, got, want)
				}
			}
		})
	}
}

// checkNotSet is an opened run whose SetRunAPICheck returns err.
type checkNotSet struct {
	openedRun
	err error
}

func (r checkNotSet) SetRunAPICheck(func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
	return r.err
}

// TestACheckThatIsNotSetStopsWithItsLine is the line of a check that could not be turned on,
// in a run with no run code and in a run from a run code, each with exit 1.
func TestACheckThatIsNotSetStopsWithItsLine(t *testing.T) {
	run := checkNotSet{err: errors.New("Call SetRunAPICheck before Setup: the setup started.")}
	for fromCode, want := range map[bool]string{
		false: "Run again. Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: Call SetRunAPICheck before Setup: the setup started.",
		true:  "Take the work item in again in Qory Apiary: turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: Call SetRunAPICheck before Setup: the setup started; the code is used",
	} {
		p := &runPushChecks{checker: &pushcheck.Checker{}, fromCode: fromCode}
		err := p.set(run)
		if err == nil || err.Error() != want {
			t.Errorf("from a run code %v: %v, want %q", fromCode, err, want)
		}
		if code := ExitCode(err); code != 1 {
			t.Errorf("from a run code %v: exit %d, want 1", fromCode, code)
		}
	}
}

// TestAnAgentThatCouldChangeThePushChecksStops is each line of [pushChecksWritable], in a
// run with no run code and in a run from a run code, each with the exit status of input.
func TestAnAgentThatCouldChangeThePushChecksStops(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "home")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "home")
	mount := filepath.Join(outside, "mounted")
	for _, c := range []struct {
		name, home, want, fromCode string
		writable                   []string
	}{
		{
			name: "a home in the checkout", home: inside,
			want:     "Set harness.home to a folder outside the checkout for a walled run. " + inside + " is inside the checkout, where the agent can change the push checks.",
			fromCode: "Set harness.home to a folder outside the checkout for a walled run. " + inside + " is inside the checkout, where the agent can change the push checks; the code is used",
		},
		{
			name: "a mount in the home", home: outside, writable: []string{mount},
			want:     "Mount " + mount + " read-only, or move it out of the harness home, for a walled run with push checks. The agent could change the push checks through it.",
			fromCode: "Mount " + mount + " read-only, or move it out of the harness home, for a walled run with push checks. The agent could change the push checks through it; the code is used",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pr := &prepared{at: places{root: root, home: c.home}, res: &compose.Result{PushChecks: []compose.PushCheck{{Type: "spec"}}}}
			for fromCode, want := range map[bool]string{false: c.want, true: c.fromCode} {
				o := composeOptions{run: true, walled: true, fromCode: fromCode, writable: c.writable}
				err := pushChecksWritable(o, pr)
				if err == nil || err.Error() != want {
					t.Errorf("from a run code %v: %v, want %q", fromCode, err, want)
				}
				if code := ExitCode(err); code != ExitInput {
					t.Errorf("from a run code %v: exit %d, want %d", fromCode, code, ExitInput)
				}
			}
		})
	}
}
