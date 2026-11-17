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
	dockerfile := prebuild.ToolsDockerfile(testName+":base", "debian", "1234:2345", false)
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
	if !strings.HasPrefix(f.read("dockerfiles"), prebuild.ToolsDockerfile(testName+":base", "debian", "1234:2345", true)) {
		t.Errorf("the Dockerfiles are\n%s", f.read("dockerfiles"))
	}
	if !strings.Contains(prebuild.ToolsDockerfile("base", "debian", "1234:2345", true), "\nCOPY --chown=0:0 --chmod=0755 node /usr/local/bin/node\n") {
		t.Error("the Dockerfile does not copy node")
	}
}

// TestBuildRefusesABaseItCannotAddTheToolsTo is a base that is not Debian, or has no
// apt-get, or whose system the probe cannot read, a folder of parsers that is none, or a
// base with no node while the build has none to copy: each refused with its fix first,
// in a line of at most 100 characters before the first newline, and the tools step
// builds nothing.
func TestBuildRefusesABaseItCannotAddTheToolsTo(t *testing.T) {
	deep := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), strings.Repeat("deep-folder-", 12))
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	longValue := strings.Repeat("x", 80)
	const (
		notServed = "use a Debian or Ubuntu image: the build adds Chromium only to these"
		noApt     = "use a Debian or Ubuntu image that has apt-get: the build installs ffmpeg and fonts with it"
	)
	for name, tc := range map[string]struct {
		probe, first, second string
		noShell              bool
		early                bool
		prepare              func(o *prebuild.Options)
	}{
		"ubuntu 20.04":     {probe: "id=ubuntu\nversion=20.04\narch=amd64\napt=yes\nnode=yes\n", first: "use Ubuntu 22.04 or 24.04, or Debian: the build adds Chromium only to these", second: `this image is Ubuntu "20.04"`},
		"ubuntu long":      {probe: "id=ubuntu\nversion=" + longValue + "\narch=amd64\napt=yes\nnode=yes\n", first: "use Ubuntu 22.04 or 24.04, or Debian: the build adds Chromium only to these", second: `this image is Ubuntu "` + longValue[:32] + `"`},
		"ubuntu arch":      {probe: "id=ubuntu\nversion=24.04\narch=s390x\napt=yes\nnode=yes\n", first: "use an amd64 or arm64 Ubuntu image: the build adds Chromium only for these", second: `this image's architecture is "s390x"`},
		"ubuntu long arch": {probe: "id=ubuntu\nversion=24.04\narch=" + longValue + "\napt=yes\nnode=yes\n", first: "use an amd64 or arm64 Ubuntu image: the build adds Chromium only for these", second: `this image's architecture is "` + longValue[:32] + `"`},
		"ubuntu no apt":    {probe: "id=ubuntu\nversion=24.04\narch=amd64\napt=no\nnode=yes\n", first: noApt},
		"alpine":           {probe: "id=alpine\napt=no\nnode=no\n", first: notServed, second: `this image's ID is "alpine"`},
		"long id":          {probe: "id=" + longValue + "\napt=yes\nnode=yes\n", first: notServed, second: `this image's ID is "` + longValue[:32] + `"`},
		"no id":            {probe: "id=\napt=yes\nnode=yes\n", first: notServed, second: "this image's ID is empty"},
		"no apt":           {probe: "id=debian\napt=no\nnode=yes\n", first: noApt},
		"odd id":           {probe: "id=\x1b[31mred\napt=yes\nnode=yes\n", first: notServed, second: `this image's ID is "`},
		"no shell":         {noShell: true, first: "use a base image with sh and /etc/os-release: the build could not read its system", second: "exec: sh: not found"},
		"no parsers": {early: true, first: "build the prebuild image FROM Qory's agent image, which has the parsers", second: "the folder ", prepare: func(o *prebuild.Options) {
			o.Parsers = deep(t)
		}},
		"no node": {probe: "id=debian\napt=yes\nnode=no\n", first: "use a base image that has node, or build the prebuild image FROM Qory's agent image", second: "there is no node program at ", prepare: func(o *prebuild.Options) {
			o.Node = filepath.Join(deep(t), "node")
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
			if !errors.As(err, &refusal) || err == nil {
				t.Fatalf("Build: %v", err)
			}
			first, rest, _ := strings.Cut(err.Error(), "\n")
			if first != tc.first || !strings.HasPrefix(rest, tc.second) || len(first) > 100 {
				t.Errorf("the refusal is %q (%d characters in line 1), want line 1 %q and line 2 starting %q", err.Error(), len(first), tc.first, tc.second)
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

// TestBuildAddsChromiumToUbuntuFromTheDownload is an Ubuntu 22.04 and a 24.04 base, on
// either architecture: Chromium is not installed with apt, which has a snap for it, but
// downloaded, checked against the sum committed in the code before it is extracted, and
// linked at /usr/bin/chromium, with the release's libraries and no chromium package.
func TestBuildAddsChromiumToUbuntuFromTheDownload(t *testing.T) {
	for release, lib := range map[string]string{"22.04": " libasound2 ", "24.04": " libasound2t64 "} {
		t.Run(release, func(t *testing.T) {
			f := newFakes(t)
			f.set(t, "probe", "id=ubuntu\nversion="+release+"\narch=arm64\napt=yes\nnode=yes\n")
			if _, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard)); err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := f.read("dockerfiles")
			if !strings.HasPrefix(got, prebuild.ToolsDockerfile(testName+":base", "ubuntu"+release, "1234:2345", false)) {
				t.Fatalf("the Dockerfiles are\n%s", got)
			}
			for _, want := range []string{lib, " ffmpeg ", " fonts-noto-cjk ", " unzip ", "--no-install-recommends",
				"amd64) archive=linux64/chrome-linux64.zip; sum=370601106c1b5619aae5f06f7a3fdc60836486135934779461e2a208d777f596 ;;",
				"arm64) archive=linux-arm64/chrome-linux-arm64.zip; sum=4a9ea5f9422e268fff9df19908365b6d1e92da154607d0f90c7138cf6c42ce38 ;;",
				"https://storage.googleapis.com/chrome-for-testing-public/159.0.8154.6/$archive", "sha256sum -c -",
				"--proto '=https' --proto-redir '=https' --max-redirs 0", "--write-out '%{http_code}'", `test "$status" = 200`, "ln -s /opt/qory/chromium/chrome /usr/bin/chromium",
				"apt-get clean", "rm -rf /var/lib/apt/lists/*"} {
				if !strings.Contains(got, want) {
					t.Errorf("the Dockerfile lacks %q:\n%s", want, got)
				}
			}
			// The sum is checked before the archive is extracted.
			if strings.Index(got, `test "$status" = 200`) > strings.Index(got, "sha256sum -c -") || strings.Contains(got, "--fail") {
				t.Error("the status is not checked before the sum, or --fail hides a redirect")
			}
			if strings.Contains(got, "--location") || strings.Contains(got, "curl -L") || strings.Contains(got, "cdn.playwright.dev") {
				t.Error("the download follows a redirect or uses Playwright's site")
			}
			if strings.Index(got, "sha256sum -c -") > strings.Index(got, "unzip -q") {
				t.Error("the archive is extracted before its sum is checked")
			}
			if i := strings.Index(got, "apt-get install"); strings.Contains(got[i:i+strings.Index(got[i:], "apt-get clean")], "chromium") {
				t.Errorf("the Dockerfile installs a chromium package:\n%s", got)
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
	if err == nil || !strings.HasPrefix(err.Error(), "read the build log for the step that failed, adding Chromium, ffmpeg and the parsers\nERROR: failed to solve") {
		t.Fatalf("Build: %v", err)
	}
	if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
		t.Errorf("line 1 has %d characters", len(first))
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
