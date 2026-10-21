package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// TestNoticeIsABoxWithTheVersionsAndTheCommand pins the notice a command ends with when
// a newer release exists: a frame, the two versions, the changelog and qory update.
func TestNoticeIsABoxWithTheVersionsAndTheCommand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out strings.Builder
	notice(ui.New(&out), "0.4.0", "0.5.0")
	for _, want := range []string{
		"╔═", "═╗", "╚═", "═╝",
		"║                  Update available! 0.4.0 → 0.5.0                  ║",
		"║   Changelog: https://github.com/qoryai/qory/releases/tag/v0.5.0   ║",
		"║                   Run \"qory update\" to update.                    ║",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the notice lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasPrefix(out.String(), "\n  ╔") || !strings.HasSuffix(out.String(), "╝\n\n") {
		t.Errorf("the notice is not set off by blank lines:\n%q", out.String())
	}
}

// TestStartUpdateCheckIsSilentOffATerminal pins that the daily look is skipped when the
// output is not a terminal, and when QORY_NO_UPDATE_CHECK or CI is set, by asking that
// the returned function prints nothing to a buffer with a release version behind
// anything.
func TestStartUpdateCheckIsSilentOffATerminal(t *testing.T) {
	was := Version
	t.Cleanup(func() { Version = was })
	Version = "0.0.1"
	var out strings.Builder
	StartUpdateCheck(&out, nil)()
	if out.Len() != 0 {
		t.Errorf("a buffer got a notice:\n%s", out.String())
	}
}

// TestNoUpdateCheckInsideTheWall feeds the command lines enclose gives the wall to run
// the qory binary as the relay, the start of a nested Docker, the hook forwarder, the
// preparation and the exec step, to the check that keeps the look for a release out of
// the container: each is a verb the wall runs inside, with -v before it too, and no
// verb a person runs is. The preparation is qory run prepare from the qory inside.
func TestNoUpdateCheckInsideTheWall(t *testing.T) {
	var spec session.Spec
	r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux"}}
	if err := enclose(&spec, r, wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d, ok := spec.Wall.(*wall.Docker)
	if !ok || spec.Forwarder[0] != wall.HelperPath || len(spec.Preparation) == 0 || spec.Preparation[0] != wall.HelperPath {
		t.Fatalf("the wall is %#v, the forwarder %v, the preparation %v", spec.Wall, spec.Forwarder, spec.Preparation)
	}
	if want := []string{wall.HelperPath, "run", "prepare"}; !slices.Equal(spec.Preparation, want) {
		t.Errorf("the preparation is %v, want %v", spec.Preparation, want)
	}
	for _, args := range [][]string{
		append(slices.Clone(d.RelayArgs), "3128=172.30.0.1:40000"),
		append(slices.Clone(d.NestArgs), "--user", "1000:1000", "--", "claude", "--settings", "/w/settings.json"),
		spec.Forwarder[1:],
		spec.Preparation[1:],
		append(slices.Clone(spec.Exec[1:]), "--unset", "CLAUDE_CONFIG_DIR", "--", "/bin/sh", "-c", "true"),
	} {
		if !walledVerb(args) {
			t.Errorf("%v: not a verb the wall runs inside", args)
		}
		if !walledVerb(append([]string{"-v"}, args...)) {
			t.Errorf("-v %v: not a verb the wall runs inside", args)
		}
	}
	for _, args := range [][]string{
		{"run", "claude", "--", "-p", "hi"},
		{"run", "--image", "go-docker"},
		{"run", "resend", "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"},
		{"run"},
		{"config"},
		{"no-such-verb"},
		nil,
	} {
		if walledVerb(args) {
			t.Errorf("%v: read as a verb the wall runs inside", args)
		}
	}
}

// TestEveryWalledRunStartsThroughExec holds enclose, where qory builds every walled
// session.Spec, to setting Spec.Exec to the helper inside the container in its exec mode,
// which Forager needs for a walled run: the default image, one the policy selects,
// --image, and --wall docker. A run without a wall gets none.
func TestEveryWalledRunStartsThroughExec(t *testing.T) {
	want := []string{wall.HelperPath, "run", "exec"}
	section := &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux",
		Images: []config.ForagerImage{{Name: "go", Ref: "example.com/agent-go:1"}}}
	for _, c := range []struct {
		name     string
		r        *config.Forager
		o        wallOptions
		selected string
	}{
		{"the default image", &config.Forager{Wall: section}, wallOptions{}, ""},
		{"the policy's image", &config.Forager{Wall: section}, wallOptions{}, "go"},
		{"--image", &config.Forager{Wall: section}, wallOptions{image: "example.com/other:2"}, ""},
		{"--wall docker", &config.Forager{Wall: &config.ForagerWall{Helper: "/opt/qory/qory-linux"}}, wallOptions{name: config.WallDocker, image: "example.com/agent:1"}, ""},
	} {
		var spec session.Spec
		if err := enclose(&spec, c.r, c.o, c.selected, false, "/opt/qory/qory-linux", t.TempDir(), t.TempDir()); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if spec.Wall == nil || !slices.Equal(spec.Exec, want) {
			t.Errorf("%s: the wall is %#v, Exec %v, want %v", c.name, spec.Wall, spec.Exec, want)
		}
	}
	var spec session.Spec
	if err := enclose(&spec, &config.Forager{Wall: section}, wallOptions{name: wallOff}, "", false, "/opt/qory/qory-linux", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if spec.Wall != nil || spec.Exec != nil {
		t.Errorf("without a wall: the wall is %#v, Exec %v", spec.Wall, spec.Exec)
	}
}

// TestWallImagesAllowGoesToForager holds enclose to passing wall.images_allow to
// Forager as Spec.ImagesAllow, and none when the file lists none. A policy that names
// its image by a reference pinned by digest is Forager's to allow or refuse under the
// list, not qory's to refuse as a name wall.images does not define; and a list without
// a default image, which an image by reference starts under, is refused as input.
func TestWallImagesAllowGoesToForager(t *testing.T) {
	const ref = "registry.example/agents/go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	allow := []string{"registry.example/agents/", "ghcr.io/acme"}
	section := func(image string, allow []string) *config.Forager {
		return &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: image, Helper: "/opt/qory/qory-linux", ImagesAllow: allow}}
	}
	for _, c := range []struct {
		name     string
		r        *config.Forager
		o        wallOptions
		selected string
		want     []string
	}{
		{"the list", section("example.com/agent:1", allow), wallOptions{}, "", allow},
		{"the list, a policy's reference", section("example.com/agent:1", allow), wallOptions{}, ref, allow},
		{"the list, --image the default", section("", allow), wallOptions{image: "example.com/agent:1"}, ref, allow},
		{"none", section("example.com/agent:1", nil), wallOptions{}, "", nil},
		{"none, a policy's reference", section("example.com/agent:1", nil), wallOptions{}, ref, nil},
	} {
		var spec session.Spec
		if err := enclose(&spec, c.r, c.o, c.selected, false, "/opt/qory/qory-linux", t.TempDir(), t.TempDir()); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.Equal(spec.ImagesAllow, c.want) || (c.want == nil) != (spec.ImagesAllow == nil) {
			t.Errorf("%s: Spec.ImagesAllow is %q, want %q", c.name, spec.ImagesAllow, c.want)
		}
	}
	var spec session.Spec
	err := enclose(&spec, section("", allow), wallOptions{}, ref, false, "/opt/qory/qory-linux", t.TempDir(), t.TempDir())
	want := "wall.images_allow in forager.yaml needs a default image, which an image by reference starts under: --image, or wall.image"
	if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
		t.Errorf("a list without a default: %v (exit %d), want %q", err, ExitCode(err), want)
	}
}
