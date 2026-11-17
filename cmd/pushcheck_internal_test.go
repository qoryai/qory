package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
		false: `Switch the engine back to the one the run started on, and run again.
  The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a".`,
		true: `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts.
  The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a"; the code is used`,
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
// returns base and err, Dir returns dir, and Image returns registry.example/run:1. Its
// other methods are not called.
type runAPIOnly struct {
	openedRun
	base, dir string
	err       error
}

func (r runAPIOnly) RunAPI() (*http.Client, string, error) {
	return http.DefaultClient, r.base, r.err
}

func (r runAPIOnly) Dir() string { return r.dir }

func (r runAPIOnly) Image() session.Image { return session.Image{Ref: "registry.example/run:1"} }

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
	// An engine that answers every command with exit 0, as one that has every image.
	present, _ := loggingEngine(t, "")
	// An engine that does not have the image registry.example/agent:1.
	noImage := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(noImage, []byte("#!/bin/sh\ncase \"$*\" in\n\"image inspect --format {{.Id}} registry.example/agent:1\")\n\techo 'Error response from daemon: No such image: registry.example/agent:1' >&2\n\texit 1 ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		run          runAPIOnly
		unread       bool
		noWall       bool
		docker       *wall.Docker
		image        string
		controlPlane bool
		want         string
		fromCode     string
	}{
		{name: "no control plane", run: runAPIOnly{err: closed}},
		{
			name:         "a run API that fails",
			run:          runAPIOnly{err: closed},
			controlPlane: true,
			want:         "Open the run through a gateway that sends the run API address, and run again.\n  The push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed.",
			fromCode:     "Take the work item in again in Qory Apiary, through a gateway that sends the run API address.\n  The push checks read the artifacts through the run API, which failed: Open another run to reach the run API: this run is closed; the code is used",
		},
		{
			name:         "a bad address",
			run:          runAPIOnly{base: "ftp://run-api.qory.invalid/v1/runs/r1"},
			controlPlane: true,
			want:         "Update the run's gateway, and run again.\n  The run API address that the gateway sent is not one the push checks can use: Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>. It needs http or https and no user, query or fragment. The value without those is \"ftp://run-api.qory.invalid/v1/runs/r1\".",
			fromCode: `Update the run's gateway, then take the work item in again in Qory Apiary.
  The run API address that the gateway sent is not one the push checks can use: Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>. It needs http or https and no user, query or fragment. The value without those is "ftp://run-api.qory.invalid/v1/runs/r1"; the code is used`,
		},
		{
			name:         "a wall that is not docker",
			noWall:       true,
			controlPlane: true,
			want:         "Set wall.adapter to docker, and run again.\n  The push checks of " + home + " run in containers of the docker wall alone.",
			fromCode:     "Set wall.adapter to docker, then take the work item in again in Qory Apiary.\n  The push checks of " + home + " run in containers of the docker wall alone; the code is used",
		},
		{
			name:         "no folder",
			run:          runAPIOnly{base: goodAPI, dir: gone},
			docker:       &wall.Docker{Command: present},
			image:        "registry.example/agent:1",
			controlPlane: true,
			want:         "Check the folder of the push checks' files, and run again.\n  The folder is " + folder + ", and making it failed: " + folderCause + ".",
			fromCode:     "Check the folder of the push checks' files, then take the work item in again in Qory Apiary.\n  The folder is " + folder + ", and making it failed: " + folderCause + "; the code is used",
		},
		{
			name:         "another engine",
			run:          runAPIOnly{base: goodAPI},
			docker:       otherEngine,
			controlPlane: true,
			want:         "Switch the engine back to the one the run started on, and run again.\n  The push checks would run on the engine \"id-b\", and the run's containers are on the engine \"id-a\".",
			fromCode: `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts.
  The push checks would run on the engine "id-b", and the run's containers are on the engine "id-a"; the code is used`,
		},
		{
			name:         "an engine whose id cannot be read",
			run:          runAPIOnly{base: goodAPI},
			docker:       noIDEngine,
			controlPlane: true,
			want:         "Switch the engine back to the one the run started on, and run again.\n  The push checks would run on the engine with no id, and the run's containers are on the engine \"id-a\".",
			fromCode: `Take the work item in again in Qory Apiary, and do not switch the engine while that run starts.
  The push checks would run on the engine with no id, and the run's containers are on the engine "id-a"; the code is used`,
		},
		{
			name:         "no default image",
			run:          runAPIOnly{base: goodAPI},
			docker:       &wall.Docker{Command: noImage},
			image:        "registry.example/agent:1",
			controlPlane: true,
			want:         "Pull the wall's default image onto this machine, and run again.\n  Push checks run in it, and the engine does not have the image registry.example/agent:1.",
			fromCode:     "Pull the wall's default image, then take the work item in again in Qory Apiary.\n  Push checks run in it, and the engine does not have the image registry.example/agent:1; the code is used",
		},
		{
			name:     "push checks that do not read",
			unread:   true,
			want:     "Compose the harness again, and run again.\n  The push checks in /h/push-checks.json could not be read: a cause.",
			fromCode: "Take the work item in again in Qory Apiary, so that the new run composes the harness again.\n  The push checks in /h/push-checks.json could not be read: a cause; the code is used",
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
			spec.Image = c.image
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

// stubChecks makes the load of push checks return one check for proposal, for the test.
func stubChecks(t *testing.T) {
	t.Helper()
	was := loadPushChecks
	t.Cleanup(func() { loadPushChecks = was })
	loadPushChecks = func(string) ([]compose.PushCheck, error) {
		return []compose.PushCheck{{Type: "proposal"}}, nil
	}
}

// loggingEngine is a docker command that writes each command line to the log it
// returns, and answers an image inspect with errOut on stderr and exit 1 when errOut is
// not empty. Every other command exits 0 and prints nothing.
func loggingEngine(t *testing.T, errOut string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "engine.log")
	script := filepath.Join(dir, "docker")
	body := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if errOut != "" {
		body += "case \"$1 $2\" in\n\"image inspect\") echo '" + errOut + "' >&2; exit 1 ;;\nesac\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// TestAnInspectThatFailsLetsTheRunGoOn is an engine whose image inspect fails for
// another cause than a missing image: the load returns the checker and no error, so the
// run goes on, and a check that cannot start fails its push later.
func TestAnInspectThatFailsLetsTheRunGoOn(t *testing.T) {
	stubChecks(t)
	docker, log := loggingEngine(t, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")
	spec := &runSpec{}
	spec.Wall, spec.Image = &wall.Docker{Command: docker}, "registry.example/agent:1"
	run := runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	if err != nil || checker == nil {
		t.Fatalf("pushChecker = %v, %v, want a checker and no error", checker, err)
	}
	if checker.Image != "registry.example/agent:1" {
		t.Errorf("the checks' image is %q, want registry.example/agent:1", checker.Image)
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "image inspect --format {{.Id}} registry.example/agent:1") {
		t.Errorf("the engine did not get the inspect:\n%s", data)
	}
}

// runtimesEngine is a docker command that writes each command line to the log it returns
// and answers `info --format {{json .Runtimes}}` with out on stdout and exit code code,
// with errOut on stderr. Every other command exits 0 and prints nothing.
func runtimesEngine(t *testing.T, out string, code int, errOut string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "engine.log")
	script := filepath.Join(dir, "docker")
	body := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$1 $2\" in\n\"info --format\") echo '" + out + "'; echo '" + errOut + "' >&2; exit " + strconv.Itoa(code) + " ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

// runsc is the checks' runtime in the tests of the runtime's load check.
const runsc = "runsc"

// gvisorRun is an opened run of a walled run whose policy selected an image that runs
// under runsc, while the default image of wall.images has no runtime.
func gvisorRun(t *testing.T) (openedRun, *runSpec) {
	t.Helper()
	spec := &runSpec{}
	spec.Image = "plain"
	spec.Images = []session.Image{{Name: "plain", Ref: "registry.example/plain:1"}, {Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: runsc}}
	run := imageOnlyAPI{imageAPI: imageAPI{runAPIOnly: runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}}, img: spec.Images[1]}
	return run, spec
}

// TestAMissingRuntimeStopsTheLoad is a run whose checks run under runsc on an engine whose
// runtimes do not list it: the load stops with the fix line and the runtime on the next
// line, for a run from a run code and for one with none, and no container of a check is
// created.
func TestAMissingRuntimeStopsTheLoad(t *testing.T) {
	stubChecks(t)
	for fromCode, want := range map[bool]string{
		false: "Install the container runtime on the engine, and run again.\n  The push checks' container runs under the runtime runsc, and the engine does not have it.",
		true:  "Install the container runtime, then take the work item in again in Qory Apiary.\n  The push checks' container runs under the runtime runsc, and the engine does not have it; the code is used",
	} {
		docker, log := runtimesEngine(t, `{"runc":{"path":"runc"},"sysbox-runc":{"path":"sysbox-runc"}}`, 0, "")
		run, spec := gvisorRun(t)
		spec.Wall = &wall.Docker{Command: docker}
		checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, fromCode)
		if checker != nil || err == nil || err.Error() != want {
			t.Errorf("from a run code %v: %v, %v, want no checker and %q", fromCode, checker != nil, err, want)
		}
		data, _ := os.ReadFile(log)
		if !strings.Contains(string(data), "info --format {{json .Runtimes}}") {
			t.Errorf("from a run code %v: the engine did not get the info:\n%s", fromCode, data)
		}
		if strings.Contains(string(data), "create") || strings.Contains(string(data), "start") {
			t.Errorf("from a run code %v: a check's container was made:\n%s", fromCode, data)
		}
	}
}

// TestAMissingRuntimeOfTheDefaultStopsTheLoad is a default image with runtime runsc, which
// the engine does not have, and a run whose selected image has no runtime: the checks run
// under the default's runtime, and the load stops, not the first push.
func TestAMissingRuntimeOfTheDefaultStopsTheLoad(t *testing.T) {
	stubChecks(t)
	docker, _ := runtimesEngine(t, `{"runc":{}}`, 0, "")
	run, spec := gvisorRun(t)
	spec.Wall, spec.Image = &wall.Docker{Command: docker}, "gvisor"
	run = imageOnlyAPI{imageAPI: run.(imageOnlyAPI).imageAPI, img: spec.Images[0]}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	want := "Install the container runtime on the engine, and run again.\n  The push checks' container runs under the runtime runsc, and the engine does not have it."
	if checker != nil || err == nil || err.Error() != want {
		t.Errorf("pushChecker = %v, %v, want no checker and %q", checker != nil, err, want)
	}
}

// TestAMissingRuntimeOfARunWithNoDefaultStopsTheLoad is the same stop for a run with no
// default image, whose image of the policy has the runtime.
func TestAMissingRuntimeOfARunWithNoDefaultStopsTheLoad(t *testing.T) {
	stubChecks(t)
	docker, _ := runtimesEngine(t, `{"runc":{}}`, 0, "")
	run, spec := gvisorRun(t)
	spec.Wall, spec.Image = &wall.Docker{Command: docker}, ""
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	if checker != nil || err == nil || !strings.HasPrefix(err.Error(), "Install the container runtime on the engine, and run again.\n") {
		t.Errorf("pushChecker = %v, %v, want no checker and the fix line", checker != nil, err)
	}
}

// TestARuntimeTheEngineHasLetsTheLoadGoOn is a run whose checks run under runsc on an
// engine that lists it: the load returns the checker.
func TestARuntimeTheEngineHasLetsTheLoadGoOn(t *testing.T) {
	stubChecks(t)
	docker, log := runtimesEngine(t, `{"runc":{"path":"runc"},"runsc":{"path":"runsc"}}`, 0, "")
	run, spec := gvisorRun(t)
	spec.Wall = &wall.Docker{Command: docker}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	if err != nil || checker == nil || checker.Runtime != runsc {
		t.Errorf("pushChecker = %v, %v, want a checker of runtime runsc", checker != nil, err)
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "info --format {{json .Runtimes}}") {
		t.Errorf("the engine did not get the info:\n%s", data)
	}
}

// TestAnEngineInfoThatFailsLetsTheRunGoOn is an engine whose info fails, or whose runtimes
// do not read as Docker's list, such as Podman's: the load returns the checker and no
// error, as it does for an image inspect that fails, and a check that cannot start fails
// its push later.
func TestAnEngineInfoThatFailsLetsTheRunGoOn(t *testing.T) {
	stubChecks(t)
	for name, c := range map[string]struct {
		out     string
		code    int
		errOut  string
		wantErr bool
	}{
		"not reachable": {code: 1, errOut: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"},
		"no list":       {out: "null"},
		"not a list":    {out: "<no value>"},
	} {
		t.Run(name, func(t *testing.T) {
			docker, log := runtimesEngine(t, c.out, c.code, c.errOut)
			run, spec := gvisorRun(t)
			spec.Wall = &wall.Docker{Command: docker}
			checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
			if err != nil || checker == nil {
				t.Errorf("pushChecker = %v, %v, want a checker and no error", checker != nil, err)
			}
			if data, _ := os.ReadFile(log); !strings.Contains(string(data), "info --format {{json .Runtimes}}") {
				t.Errorf("the engine did not get the info:\n%s", data)
			}
		})
	}
}

// TestARuntimeLineStartsWithAFixOf100Characters is the first line of the stop for a missing
// runtime, with "qory run: " in front, as it reaches the terminal: at most 100 characters,
// for a run with a run code and one with none, and whatever the runtime's name is.
func TestARuntimeLineStartsWithAFixOf100Characters(t *testing.T) {
	stubChecks(t)
	for _, fromCode := range []bool{false, true} {
		docker, _ := runtimesEngine(t, `{"runc":{}}`, 0, "")
		run, spec := gvisorRun(t)
		spec.Wall = &wall.Docker{Command: docker}
		spec.Images[1].Runtime = "a-runtime-with-a-long-name-" + strings.Repeat("x", 60)
		run = imageOnlyAPI{imageAPI: run.(imageOnlyAPI).imageAPI, img: spec.Images[1]}
		_, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, fromCode)
		if err == nil {
			t.Fatalf("from a run code %v: no error", fromCode)
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		if n := len("qory run: " + first); n > 100 {
			t.Errorf("from a run code %v: the first line is %d characters: %q", fromCode, n, first)
		}
		if strings.Contains(first, "runtime-with-a-long-name") {
			t.Errorf("from a run code %v: the first line has the runtime's name: %q", fromCode, first)
		}
	}
}

// TestACheckImageThatIsNoReferenceStops is a default image, or a run's image with no
// default, whose value is no image reference, such as one that starts with -: the load
// stops with the fix line, and no command of the engine gets the value.
func TestACheckImageThatIsNoReferenceStops(t *testing.T) {
	stubChecks(t)
	for _, c := range []struct{ image, runImage, shown string }{
		{image: "--privileged", shown: `"--privileged"`},
		{runImage: "-v/:/host", shown: `"-v/:/host"`},
	} {
		lines := map[bool]string{
			false: "Set wall.image or --image to an image reference, and run again.\n  Push checks cannot start from " + c.shown + ". A reference starts with a letter or a digit and has only letters, digits, and . _ / : @ -.",
			true:  "Set wall.image or --image to an image reference, then take the work item in again in Qory Apiary.\n  Push checks cannot start from " + c.shown + "; the code is used",
		}
		if c.image == "" {
			// A run from a run code with no default image stops before its run's image is
			// read: see TestARunCodeWithNoDefaultImageStops.
			delete(lines, true)
		}
		for fromCode, want := range lines {
			docker, log := loggingEngine(t, "")
			spec := &runSpec{}
			spec.Wall, spec.Image = &wall.Docker{Command: docker}, c.image
			run := imageAPI{runAPIOnly: runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}, ref: c.runImage}
			checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, fromCode)
			if checker != nil || err == nil || err.Error() != want {
				t.Errorf("%q, from a run code %v: %v, %v, want %q", c.shown, fromCode, checker, err, want)
			}
			if data, _ := os.ReadFile(log); strings.Contains(string(data), strings.Trim(c.shown, `"`)) {
				t.Errorf("%q: the engine got the value:\n%s", c.shown, data)
			}
		}
	}
}

// TestARunCodeWithNoDefaultImageStops is a run from a run code with push checks and
// neither --image nor wall.image, whose run's image its policy from the server selects:
// the load stops with the fix line before any command of the engine runs, since that
// image may be built from the repository. A run with no run code runs the checks in the
// run's image.
func TestARunCodeWithNoDefaultImageStops(t *testing.T) {
	stubChecks(t)
	docker, log := loggingEngine(t, "")
	spec := &runSpec{}
	spec.Wall = &wall.Docker{Command: docker}
	run := imageAPI{runAPIOnly: runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}, ref: "registry.example/devcontainer:1"}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, true)
	want := "Set wall.image in forager.yaml, then take the work item in again in Qory Apiary.\n  Push checks run in the wall's default image; the code is used"
	if checker != nil || err == nil || err.Error() != want {
		t.Fatalf("from a run code: %v, %v, want %q", checker, err, want)
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "devcontainer") {
		t.Errorf("the engine got the run's image:\n%s", data)
	}
	checker, err = pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	if err != nil || checker == nil || checker.Image != "registry.example/devcontainer:1" {
		t.Fatalf("with no run code: %v, %v, want a checker of registry.example/devcontainer:1", checker, err)
	}
}

// imageAPI is [runAPIOnly] whose Image returns ref.
type imageAPI struct {
	runAPIOnly
	ref string
}

func (r imageAPI) Image() session.Image { return session.Image{Ref: r.ref} }

// imageOnly is an opened run whose Image returns img. Its other methods are not called.
type imageOnly struct {
	openedRun
	img session.Image
}

func (r imageOnly) Image() session.Image { return r.img }

// TestPushChecksRunInTheWallsDefaultImage is the image of a run's push checks: the ref of
// the image of wall.images that the default names, the default as a reference, and the
// run's image only for a run with no default.
func TestPushChecksRunInTheWallsDefaultImage(t *testing.T) {
	run := imageOnly{img: session.Image{Name: "devcontainer", Ref: "registry.example/devcontainer:1"}}
	images := []session.Image{{Name: "agent", Ref: "registry.example/agent:1"}, {Name: "devcontainer", Ref: "registry.example/devcontainer:1"}}
	for _, c := range []struct{ image, want string }{
		{"agent", "registry.example/agent:1"},
		{"registry.example/other:2", "registry.example/other:2"},
		{"", "registry.example/devcontainer:1"},
	} {
		spec := &runSpec{}
		spec.Image, spec.Images = c.image, images
		if got := checkImage(run, spec); got != c.want {
			t.Errorf("the default %q: %q, want %q", c.image, got, c.want)
		}
	}
}

// TestPushChecksRunUnderTheRunsRuntime is the runtime of a run's push checks: the runtime
// of the image that the run's policy selected, when it has one, so a check never runs
// under a weaker runtime than the run's container; otherwise the one Forager gives the
// default image, the runtime of the entry that the default names, none for a default that
// is a reference, and the run's image's for a run with no default.
func TestPushChecksRunUnderTheRunsRuntime(t *testing.T) {
	images := []session.Image{
		{Name: "agent", Ref: "registry.example/agent:1", Runtime: "runsc"},
		{Name: "plain", Ref: "registry.example/plain:1"},
		{Name: "devcontainer", Ref: "registry.example/devcontainer:1", Runtime: "kata"},
	}
	for _, c := range []struct {
		name, image string
		run         session.Image
		want        string
	}{
		{"selected entry with a runtime, default with none", "plain", session.Image{Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}, "runsc"},
		{"selected entry with a runtime, default with another", "devcontainer", session.Image{Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}, "runsc"},
		{"selected entry with a runtime, default a reference", "registry.example/other:2", session.Image{Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}, "runsc"},
		{"selected entry with a runtime, no default", "", session.Image{Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}, "runsc"},
		{"selected entry with none, default with a runtime", "agent", session.Image{Name: "plain", Ref: "registry.example/plain:1"}, "runsc"},
		{"selected entry with none, default with another", "devcontainer", session.Image{Name: "plain", Ref: "registry.example/plain:1"}, "kata"},
		{"selected entry with none, default with none", "plain", session.Image{Name: "plain", Ref: "registry.example/plain:1"}, ""},
		{"selected entry with none, default a reference", "registry.example/other:2", session.Image{Name: "plain", Ref: "registry.example/plain:1"}, ""},
		{"selected entry with none, no default", "", session.Image{Name: "plain", Ref: "registry.example/plain:1"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := &runSpec{}
			spec.Image, spec.Images = c.image, images
			if got := checkRuntime(imageOnly{img: c.run}, spec); got != c.want {
				t.Errorf("the default %q, the run's image %q: runtime %q, want %q", c.image, c.run.Name, got, c.want)
			}
		})
	}
}

// TestTheLoadedCheckerHasTheRuntime is the checker that pushChecker returns for a default
// that names an image of wall.images with a runtime, and for one with none.
func TestTheLoadedCheckerHasTheRuntime(t *testing.T) {
	stubChecks(t)
	images := []session.Image{{Name: "agent", Ref: "registry.example/agent:1", Runtime: "runsc"}, {Name: "plain", Ref: "registry.example/plain:1"}}
	for image, want := range map[string]string{"agent": "runsc", "plain": ""} {
		docker, _ := loggingEngine(t, "")
		spec := &runSpec{}
		spec.Wall, spec.Image, spec.Images = &wall.Docker{Command: docker}, image, images
		run := runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}
		checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
		if err != nil || checker == nil || checker.Runtime != want {
			t.Fatalf("the default %q: %v, %v, want a checker of runtime %q", image, checker, err, want)
		}
	}
}

// TestTheLoadedCheckerOfARunWithNoDefaultHasTheRunsRuntime is a run with no default
// image: pushChecker returns a checker of the run's image and of its runtime.
func TestTheLoadedCheckerOfARunWithNoDefaultHasTheRunsRuntime(t *testing.T) {
	stubChecks(t)
	docker, _ := loggingEngine(t, "")
	spec := &runSpec{}
	spec.Wall = &wall.Docker{Command: docker}
	run := imageOnlyAPI{imageAPI: imageAPI{runAPIOnly: runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}}, img: session.Image{Name: "agent", Ref: "registry.example/agent:1", Runtime: "runsc"}}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, false)
	if err != nil || checker == nil || checker.Image != "registry.example/agent:1" || checker.Runtime != "runsc" {
		t.Fatalf("with no default: %v, %v, want a checker of registry.example/agent:1 and runtime runsc", checker, err)
	}
}

// TestTheLoadedCheckerRunsUnderTheSelectedImagesRuntime is a run whose policy selects an
// entry of wall.images with runsc while the default entry has no runtime: pushChecker
// returns a checker of the default image and of runtime runsc.
func TestTheLoadedCheckerRunsUnderTheSelectedImagesRuntime(t *testing.T) {
	stubChecks(t)
	docker, _ := loggingEngine(t, "")
	spec := &runSpec{}
	spec.Wall, spec.Image = &wall.Docker{Command: docker}, "plain"
	spec.Images = []session.Image{{Name: "plain", Ref: "registry.example/plain:1"}, {Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}}
	run := imageOnlyAPI{imageAPI: imageAPI{runAPIOnly: runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}}, img: session.Image{Name: "gvisor", Ref: "registry.example/gvisor:1", Runtime: "runsc"}}
	checker, err := pushChecker(context.Background(), run, t.TempDir(), "r1", spec, true, true)
	if err != nil || checker == nil {
		t.Fatalf("pushChecker = %v, %v, want a checker", checker != nil, err)
	}
	if checker.Image != "registry.example/plain:1" || checker.Runtime != "runsc" {
		t.Errorf("the checker is of image %q and runtime %q, want registry.example/plain:1 and runsc", checker.Image, checker.Runtime)
	}
}

// imageOnlyAPI is [imageAPI] whose Image returns img, with its runtime.
type imageOnlyAPI struct {
	imageAPI
	img session.Image
}

func (r imageOnlyAPI) Image() session.Image { return r.img }

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
		false: "Run again.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: Call SetRunAPICheck before Setup: the setup started.",
		true:  "Take the work item in again in Qory Apiary.\n  Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: Call SetRunAPICheck before Setup: the setup started; the code is used",
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

// TestThePushChecksGetTheRunsInputs is the load of a run's push checks: the checker gets
// the path of the run's inputs.json and the bytes qory wrote there, and none for a run
// with none.
func TestThePushChecksGetTheRunsInputs(t *testing.T) {
	was := loadPushChecks
	t.Cleanup(func() { loadPushChecks = was })
	loadPushChecks = func(string) ([]compose.PushCheck, error) {
		return []compose.PushCheck{{Type: "proposal"}}, nil
	}
	unsetenv(t, "DOCKER_HOST")
	unsetenv(t, "DOCKER_CONTEXT")
	for _, inputs := range []string{filepath.Join(t.TempDir(), "in", inputsFile), ""} {
		var data []byte
		if inputs != "" {
			data = []byte(`{"qory":{"harness_extensions":{}}}`)
		}
		p := &runPushChecks{inputs: inputs, inputsData: data}
		spec := &runSpec{}
		spec.Wall = &wall.Docker{Command: engineScript(t, "a")}
		run := runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r1", dir: t.TempDir()}
		done, err := p.load(context.Background(), run, t.TempDir(), "r1", spec, true)
		if err != nil {
			t.Fatalf("with %q: %v", inputs, err)
		}
		done()
		if got := p.current(); got == nil || got.Inputs != inputs || !slices.Equal(got.InputsData, data) {
			t.Errorf("with %q: the checker %+v", inputs, got)
		}
	}
}
