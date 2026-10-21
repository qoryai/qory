package cmd

import (
	"slices"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
)

// TestFetchOnlySpec is a walled run's spec made a fetch-only run's: no runtime and no
// preparation, the launch qory run prepare --fetch-only from the helper, on pipes, and
// no image the run could select beside the default; the rest as it was. A spec with no
// wall is refused.
func TestFetchOnlySpec(t *testing.T) {
	w := &wall.Docker{Helper: "/opt/qory/qory-linux"}
	rt, err := catalog.Lookup("claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := session.Spec{
		Runtime: rt,
		Command: "claude", Args: []string{"-p", "hi"}, Interactive: true, Wall: w,
		Preparation: []string{wall.HelperPath, "run", "prepare"},
		Exec:        []string{wall.HelperPath, "run", "exec"},
		Image:       "example.com/agent:1",
		Images:      []session.Image{{Name: "built", Ref: "example.com/built:1"}},
		ImagesAllow: []string{"example.com/"},
		Mounts:      []wall.Mount{{Path: "/work"}},
	}
	if err = fetchOnlySpec(&spec); err != nil {
		t.Fatal(err)
	}
	switch {
	case spec.Runtime != nil, spec.Preparation != nil, spec.Interactive:
		t.Errorf("runtime %v, preparation %q, interactive %v", spec.Runtime, spec.Preparation, spec.Interactive)
	case spec.Command != wall.HelperPath || !slices.Equal(spec.Args, []string{"run", "prepare", "--fetch-only"}):
		t.Errorf("the launch %s %q", spec.Command, spec.Args)
	case spec.Images != nil || spec.ImagesAllow != nil || spec.Image != "example.com/agent:1":
		t.Errorf("images %v, allow %v, default %s", spec.Images, spec.ImagesAllow, spec.Image)
	case spec.Wall != w || !slices.Equal(spec.Exec, []string{wall.HelperPath, "run", "exec"}) || len(spec.Mounts) != 1:
		t.Errorf("the wall, the exec step or the mounts changed: %+v", spec)
	}
	byName := session.Spec{Wall: w, Image: "go", Images: []session.Image{{Name: "built", Ref: "example.com/built:1"}, {Name: "go", Ref: "example.com/go:1"}}}
	if err := fetchOnlySpec(&byName); err != nil || !slices.Equal(byName.Images, []session.Image{{Name: "go", Ref: "example.com/go:1"}}) {
		t.Errorf("a default by name: images %v, %v", byName.Images, err)
	}
	if !slices.Equal(prepareArgs, []string{"run", "prepare"}) {
		t.Errorf("the preparation's arguments changed: %q", prepareArgs)
	}
	err = fetchOnlySpec(&session.Spec{Command: "claude"})
	if want := "--fetch-only fetches the run's code inside the wall, and the run has none: --wall docker, or wall in " + config.ForagerFileName; err == nil || err.Error() != want || ExitCode(err) != ExitInput {
		t.Errorf("no wall: %v", err)
	}
}
