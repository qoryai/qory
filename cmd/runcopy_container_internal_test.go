package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/runcopy"
)

// TestTheGitContainerHasNoNetwork is the docker run of the git session: no network, no
// capability, the wall's user and runtime, the run's image, a limit of 4096 processes and none of
// the agent's limits, and a bind of the copy and of the bundle folder alone, each at its path.
func TestTheGitContainerHasNoNetwork(t *testing.T) {
	run := &nowhere{image: session.Image{Ref: "registry.example/agent@sha256:" + strings.Repeat("a", 64), Runtime: "runsc"}}
	spec := &runSpec{}
	spec.Wall = &wall.Docker{Command: "/usr/local/bin/docker", User: "1001:1001"}
	spec.Limits = wall.Limits{CPUs: "1.5", Memory: "2g", PIDs: 512, ShmSize: "1g"}
	mounts := []wall.Mount{{Path: "/cache/qory/runs/run-01/work"}, {Path: "/cache/qory/runs/run-01/bundle"}}
	gs := runcopy.GitSession{Command: "/bin/sh", Args: []string{"/cache/qory/runs/run-01/bundle/" + runcopy.SessionScriptName, "/usr/bin:/bin"}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: "/cache/qory/runs/run-01/work"}
	c := gitContainerFor(run, spec, "run-01", gs, mounts, io.Discard)
	if c.Docker != "/usr/local/bin/docker" {
		t.Errorf("the docker command is %s", c.Docker)
	}
	args, err := c.args()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	want := "run --rm --pull never --network none --cap-drop ALL --security-opt no-new-privileges --init --no-healthcheck" +
		" --user 1001:1001 --label dev.qory.run=run-01 --name qory-copy-run-01 --runtime runsc --pids-limit 4096" +
		" --mount type=bind,src=/cache/qory/runs/run-01/work,dst=/cache/qory/runs/run-01/work" +
		" --mount type=bind,src=/cache/qory/runs/run-01/bundle,dst=/cache/qory/runs/run-01/bundle" +
		" --env PATH=/usr/bin:/bin --workdir /cache/qory/runs/run-01/work --entrypoint /bin/sh " + run.image.Ref +
		" /cache/qory/runs/run-01/bundle/" + runcopy.SessionScriptName + " /usr/bin:/bin"
	if got != want {
		t.Errorf("docker %s\nwant   %s", got, want)
	}
	for i, a := range args {
		if a == "--mount" && !slices.ContainsFunc(mounts, func(m wall.Mount) bool { return strings.Contains(args[i+1], "src="+m.Path+",") }) {
			t.Errorf("a bind that is not a session mount: %s", args[i+1])
		}
	}
	spec.Wall = &wall.Docker{}
	run.image.Runtime = ""
	args, err = gitContainerFor(run, spec, "run-01", gs, mounts, io.Discard).args()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "--runtime") {
		t.Errorf("a run with no runtime has --runtime: %v", args)
	}
	if i := slices.Index(args, "--user"); i < 0 || args[i+1] == "" {
		t.Errorf("the container has no user: %v", args)
	}
}

// TestABindWithACommaIsQuoted is a path with a comma in it: the field is quoted as docker reads a
// --mount value.
func TestABindWithACommaIsQuoted(t *testing.T) {
	got, err := bindAt(wall.Mount{Path: "/a,b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `type=bind,"src=/a,b","dst=/a,b"`; got != want {
		t.Errorf("%s, want %s", got, want)
	}
}

// fakeEngine is a docker command that logs its arguments to log, one call per line: docker run
// waits, docker rm ends at once, and with fail set docker run prints a line and exits 125.
func fakeEngine(t *testing.T, fail bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if fail {
		script += "if [ \"$1\" = run ]; then echo 'docker: Error response from daemon: no such image' >&2; exit 125; fi\n"
	}
	script += "if [ \"$1\" = run ]; then exec sleep 30; fi\n"
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return docker, log
}

// TestACutRemovesTheGitContainer is a git container whose context ends: the docker run ends, and a
// docker rm removes the container before the return.
func TestACutRemovesTheGitContainer(t *testing.T) {
	docker, log := fakeEngine(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := gitContainer{Docker: docker, Name: "qory-copy-run-01", Label: "dev.qory.run=run-01", User: "1001:1001", Image: "agent", Output: io.Discard, Session: runcopy.GitSession{Command: "/bin/sh", Dir: "/w"}}
	code, err := dockerGitContainer(ctx, c)
	if code != -1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%d, %v, want -1 and the end of the context", code, err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "run --rm ") || lines[1] != "rm --force --volumes qory-copy-run-01" {
		t.Errorf("the engine's calls:\n%s", b)
	}
}

// TestAGitContainerThatDoesNotStartHasDockersLine is a docker run that fails before the container
// runs: the error is docker's last line, and no exit code.
func TestAGitContainerThatDoesNotStartHasDockersLine(t *testing.T) {
	docker, _ := fakeEngine(t, true)
	c := gitContainer{Docker: docker, Name: "qory-copy-run-01", Label: "dev.qory.run=run-01", User: "1001:1001", Image: "agent", Output: io.Discard, Session: runcopy.GitSession{Command: "/bin/sh", Dir: "/w"}}
	code, err := dockerGitContainer(context.Background(), c)
	if code != -1 || err == nil || err.Error() != "docker: Error response from daemon: no such image" {
		t.Errorf("%d, %v", code, err)
	}
}
