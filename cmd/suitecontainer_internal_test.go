package cmd

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/suite"
)

// imageRun is an opened run that has an image and nothing else.
type imageRun struct {
	openedRun
	image session.Image
}

func (r imageRun) Image() session.Image { return r.image }

// TestTheSuiteRunsInAContainerOfTheRunsImageWithOnlyItsClone reads the docker run of a suite: the
// run's image and label, no network, the clone as the one bind, and the command as sh -c.
func TestTheSuiteRunsInAContainerOfTheRunsImageWithOnlyItsClone(t *testing.T) {
	var got gitContainer
	before := runGitContainer
	runGitContainer = func(_ context.Context, c gitContainer) (int, error) { got = c; return 3, nil }
	t.Cleanup(func() { runGitContainer = before })
	run := imageRun{image: session.Image{Ref: "example.test/build:1", Runtime: "runsc"}}
	spec := &runSpec{
		OpenSpec:    session.OpenSpec{Wall: &wall.Docker{Command: "podman", User: "1000:1000"}},
		SessionSpec: session.SessionSpec{Limits: wall.Limits{Memory: "2g", CPUs: "2"}},
	}
	exit, err := suiteRunner(run, spec, "run-1")(context.Background(), suite.Job{ID: "su_ab12", Dir: "/clones/su_ab12", Full: suite.Full{Command: "make test | tee log"}}, nil)
	if exit != 3 || err != nil {
		t.Fatalf("exit %d, error %v, want the container's 3", exit, err)
	}
	args, err := got.args()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{
		"--network none", "--label dev.qory.run=run-1", "--name qory-suite-run-1-su_ab12", "--user 1000:1000", "--runtime runsc",
		"--memory 2g", "--cpus 2", "--mount type=bind,src=/clones/su_ab12,dst=/clones/su_ab12", "--workdir /clones/su_ab12",
		"--env HOME=/tmp", "--entrypoint sh example.test/build:1 -c make test | tee log",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("docker run %q has no %q", line, want)
		}
	}
	if got.Docker != "podman" {
		t.Errorf("docker command %q, want the wall's", got.Docker)
	}
	if n := strings.Count(line, "--mount"); n != 1 || slices.Contains(args, "readonly") {
		t.Errorf("%d binds in %q, want the clone alone, writable", n, line)
	}
}
