//go:build unix

package prebuild_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// toolsTag is the image the tools step builds.
const toolsTag = testName + ":tools"

// contextOf is the files of the build context of the image tag, as the fake docker
// listed them, one per line.
func contextOf(f fakes, tag string) []string {
	return strings.Fields(f.read("context-" + strings.NewReplacer(":", "_", "/", "_").Replace(tag)))
}

// TestBuildAddsTheToolsToTheImage is a Debian base that has node: the tools step builds
// FROM the base as root, with the parsers in its context and no node, then the wall
// user's step builds FROM the tools.
func TestBuildAddsTheToolsToTheImage(t *testing.T) {
	f := newFakes(t)
	var out bytes.Buffer
	o := options(t, repository(t, shopConfig), &out, io.Discard)
	if _, err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	got := contextOf(f, toolsTag)
	want := []string{"./Dockerfile", "./parsers/node_modules/.bin/parse5", "./parsers/node_modules/parse5/dist/index.js",
		"./parsers/node_modules/parse5/package.json", "./parsers/package.json"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the tools step's context is %v, want %v", got, want)
	}
	dockerfile := prebuild.ToolsDockerfile(testName+":base", "1234:2345", false)
	if !strings.HasPrefix(f.read("dockerfiles"), dockerfile) {
		t.Errorf("the Dockerfiles are\n%s", f.read("dockerfiles"))
	}
	root, install, user := strings.Index(dockerfile, "\nUSER 0:0\n"), strings.Index(dockerfile, "apt-get install"), strings.Index(dockerfile, "\nUSER 1234:2345\n")
	if root < 0 || install < root || user < install {
		t.Errorf("the install does not run between USER 0:0 and the wall user:\n%s", dockerfile)
	}
	for _, pkg := range []string{"chromium", "ffmpeg", "fonts-noto-core", "fonts-noto-cjk", "fonts-noto-color-emoji"} {
		if !strings.Contains(dockerfile[install:user], " "+pkg) {
			t.Errorf("the install lacks %s", pkg)
		}
	}
	if strings.Contains(dockerfile, "COPY --chown=0:0 --chmod=0755 node") {
		t.Error("the step copies node into an image that has it")
	}
	if !strings.Contains(out.String(), "prebuild: added Chromium, ffmpeg and the parsers in ") {
		t.Errorf("printed\n%s", out.String())
	}
}

// TestBuildCopiesTheParsersAsTheyAre is a folder of parsers whose files have mode 0600
// and whose .bin has a link: the context has the link as a link and every file mode
// 0644, which the Dockerfile's chown and chmod then make root's.
func TestBuildCopiesTheParsersAsTheyAre(t *testing.T) {
	f := newFakes(t)
	if _, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	root := f.path("ctx-" + strings.NewReplacer(":", "_", "/", "_").Replace(toolsTag))
	link := filepath.Join(root, "parsers", "node_modules", ".bin", "parse5")
	if target, err := os.Readlink(link); err != nil || target != "../parse5/dist/index.js" {
		t.Errorf("the link is %q: %v", target, err)
	}
	for _, name := range []string{"package.json", "node_modules/parse5/package.json", "node_modules/parse5/dist/index.js"} {
		fi, err := os.Stat(filepath.Join(root, "parsers", filepath.FromSlash(name)))
		if err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v, %v", name, fi, err)
		}
	}
}

// TestBuildCopiesNodeIntoAnImageWithoutIt is a base with no node: the context has the
// node program, and the Dockerfile copies it to /usr/local/bin/node as root, mode 0755,
// before the parsers.
func TestBuildCopiesNodeIntoAnImageWithoutIt(t *testing.T) {
	f := newFakes(t)
	f.set(t, "probe", "id=debian\napt=yes\nnode=no\n")
	o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
	if _, err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := contextOf(f, toolsTag); !strings.Contains(strings.Join(got, " "), "./node ") {
		t.Errorf("the context is %v", got)
	}
	if !strings.HasPrefix(f.read("dockerfiles"), prebuild.ToolsDockerfile(testName+":base", "1234:2345", true)) {
		t.Errorf("the Dockerfiles are\n%s", f.read("dockerfiles"))
	}
	if !strings.Contains(prebuild.ToolsDockerfile("base", "1234:2345", true), "\nCOPY --chown=0:0 --chmod=0755 node /usr/local/bin/node\n") {
		t.Error("the Dockerfile does not copy node")
	}
}

// TestBuildRefusesABaseItCannotAddTheToolsTo is a base that is not Debian, or has no
// apt-get, or whose system the probe cannot read, a folder of parsers that is none, or a
// base with no node while the build has none to copy: each refused with its fix first,
// in a line of at most 100 characters before the first newline, and the tools step
// builds nothing.
func TestBuildRefusesABaseItCannotAddTheToolsTo(t *testing.T) {
	for name, tc := range map[string]struct {
		probe, want string
		noShell     bool
		early       bool
		prepare     func(o *prebuild.Options)
	}{
		"ubuntu":   {probe: "id=ubuntu\napt=yes\nnode=yes\n", want: `use a Debian base image: the build adds Chromium with apt, and this image's ID is "ubuntu"`},
		"alpine":   {probe: "id=alpine\napt=no\nnode=no\n", want: `use a Debian base image: the build adds Chromium with apt, and this image's ID is "alpine"`},
		"no id":    {probe: "id=\napt=yes\nnode=yes\n", want: `use a Debian base image: the build adds Chromium with apt, and this image's ID is empty`},
		"no apt":   {probe: "id=debian\napt=no\nnode=yes\n", want: "use a Debian base image that has apt-get: the build adds Chromium and ffmpeg with it"},
		"odd id":   {probe: "id=\x1b[31mred\napt=yes\nnode=yes\n", want: `use a Debian base image: the build adds Chromium with apt, and this image's ID is "`},
		"no shell": {noShell: true, want: "use a base image with sh and /etc/os-release: reading its system failed: "},
		"no parsers": {early: true, want: "build the prebuild image FROM Qory's agent image: it has no parsers in ", prepare: func(o *prebuild.Options) {
			o.Parsers = t.TempDir()
		}},
		"no node": {probe: "id=debian\napt=yes\nnode=no\n", want: "build the prebuild image FROM Qory's agent image: it has no node program at ", prepare: func(o *prebuild.Options) {
			o.Node = filepath.Join(t.TempDir(), "node")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakes(t)
			if tc.probe != "" {
				f.set(t, "probe", tc.probe)
			}
			if tc.noShell {
				f.set(t, "probe-fail", "1")
			}
			o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
			if tc.prepare != nil {
				tc.prepare(&o)
			}
			_, err := prebuild.Build(t.Context(), o)
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("Build: %v", err)
			}
			if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 && !tc.early && name != "no node" {
				t.Errorf("the first line has %d characters: %q", len(first), first)
			}
			if strings.ContainsRune(err.Error(), 0x1b) {
				t.Errorf("the refusal has an escape: %q", err.Error())
			}
			if f.called("docker", "build --tag "+toolsTag) || f.called("docker", "create") {
				t.Errorf("docker ran\n%s", f.read("docker.log"))
			}
			if tc.early {
				if f.read("docker.log") != "" {
					t.Errorf("docker ran\n%s", f.read("docker.log"))
				}
				return
			}
			if !f.called("docker", "image rm --force "+testName+":base") {
				t.Errorf("the images are not removed:\n%s", f.read("docker.log"))
			}
		})
	}
}

// TestBuildReadsTheBaseWithoutNetworkOrCapability is the container that reads the base's
// system: no network, no capability, no new privilege, the name the clean-up removes,
// and no image pulled.
func TestBuildReadsTheBaseWithoutNetworkOrCapability(t *testing.T) {
	f := newFakes(t)
	if _, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "run --rm --name " + testName + "-probe --pull never --network none --cap-drop ALL --security-opt no-new-privileges --entrypoint sh " + testName + ":base -c "
	if !f.called("docker", want) {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
	if !f.called("docker", "rm --force --volumes "+testName+"-probe") {
		t.Errorf("the probe's container is not removed:\n%s", f.read("docker.log"))
	}
}

// TestBuildFailsInTheToolsStepWithItsFix is an apt-get or a check that failed in the
// tools step: the error starts with where to read, and the images are removed.
func TestBuildFailsInTheToolsStepWithItsFix(t *testing.T) {
	f := newFakes(t)
	f.set(t, "build-fails", toolsTag)
	var out bytes.Buffer
	_, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard))
	if err == nil || !strings.HasPrefix(err.Error(), "read the build log for the step that failed: adding Chromium, ffmpeg and the parsers: ") {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(out.String(), "added Chromium") || f.called("docker", "create") {
		t.Errorf("printed\n%s\ndocker ran\n%s", out.String(), f.read("docker.log"))
	}
	if !f.called("docker", "image rm --force "+testName+":base "+toolsTag) {
		t.Errorf("the images are not removed:\n%s", f.read("docker.log"))
	}
}

// TestBuildAddsTheToolsThroughTheWallsProxy is a build behind a wall: the tools step
// builds with the docker configuration that has the proxies, which apt reaches the
// base's sources through, and the step that sets the user builds without them.
func TestBuildAddsTheToolsThroughTheWallsProxy(t *testing.T) {
	f := newFakes(t)
	original := originalDockerConfig(t, `{"proxies":{"default":{"httpProxy":"http://172.30.0.1:3128","noProxy":"localhost"}}}`)
	t.Setenv("DOCKER_CONFIG", original)
	o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
	o.Env, o.Walled = os.Environ(), true
	if _, err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := f.read("build-config-" + strings.NewReplacer(":", "_", "/", "_").Replace(toolsTag)); !strings.Contains(got, "172.30.0.1:3128") {
		t.Errorf("the tools step read the docker configuration %q", got)
	}
	if got := f.read("build-config-" + testName + "_top"); strings.Contains(got, "proxies") {
		t.Errorf("the user step read the docker configuration %q", got)
	}
	if strings.Contains(f.read("dockerfiles"), "172.30.0.1") {
		t.Errorf("a Dockerfile has the proxy:\n%s", f.read("dockerfiles"))
	}
}
