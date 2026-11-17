//go:build unix

package prebuild_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// stageTools makes what the tools step copies, in the folder dir: the node program of
// images/agent's Node image and its parsers folder, npm ci of the committed lock, for
// the engine's platform. Neither is in a checkout.
func stageTools(t *testing.T, dir string) (parsers, node string) {
	t.Helper()
	agent := filepath.Join("..", "..", "images", "agent")
	text, err := os.ReadFile(filepath.Join(agent, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG NODE_IMAGE=(\S+)$`).FindSubmatch(text)
	if m == nil {
		t.Fatal("images/agent/Dockerfile has no ARG NODE_IMAGE")
	}
	nodeImage := string(m[1])
	var r [4]byte
	_, _ = rand.Read(r[:])
	id := hex.EncodeToString(r[:])
	ctx := filepath.Join(dir, "stage")
	if err := os.MkdirAll(ctx, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join(agent, "parsers", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ctx, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dockerfile := "FROM " + nodeImage + "\nCOPY package.json package-lock.json /opt/qory/parsers/\n" +
		"RUN cd /opt/qory/parsers && npm ci --ignore-scripts --no-audit --no-fund --cache /tmp/npm-cache\n"
	if err := os.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := "qory-test-stage:" + id
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", tag).Run() })
	docker(t, "build", "--tag", tag, ctx)
	c := docker(t, "create", tag)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", c).Run() })
	parsers, node = filepath.Join(dir, "parsers"), filepath.Join(dir, "node")
	docker(t, "cp", c+":/opt/qory/parsers", parsers)
	docker(t, "cp", c+":/usr/local/bin/node", node)
	return parsers, node
}

// toolsBases are the prebuilt development container images the tools step is tried on,
// by the digest of their index, as qory's probe of 2026-11-15 found them: none has a
// browser or ffmpeg, and only typescript-node and universal have node. The images of
// typescript-node and universal with the tools are over [prebuild.MaxSize], so a build
// refuses them: their test builds the tools step alone.
var toolsBases = map[string]toolsBaseImage{
	"base":            {"mcr.microsoft.com/devcontainers/base@sha256:38f9e6c291f054e3c868d95a5477cbfcf76c0fa7c218dda65dc6aa3fbfe06871", "debian", ""},
	"typescript-node": {"mcr.microsoft.com/devcontainers/typescript-node@sha256:e1fd824e03c68d8f406ef45b574023a2d683a5acc98906743f8d60aaf78b0174", "debian", ""},
	"ubuntu-22.04":    {"mcr.microsoft.com/devcontainers/base@sha256:81380e4c9c14e8a629ff39029639e4b7893e67400246fa7782a0fe7dc193a02a", "ubuntu22.04", ""},
	"ubuntu-24.04":    {"mcr.microsoft.com/devcontainers/base@sha256:d94c97dd9cacf183d0a6fd12a8e87b526e9e928307674ae9c94139139c0c6eae", "ubuntu24.04", ""},
	// universal is published for amd64 alone, so an arm64 engine runs it emulated.
	"universal": {"mcr.microsoft.com/devcontainers/universal@sha256:d7b342e610144566fdbe459a8d22322e3fffc511bf22b1083beea8d8f46d7c67", "ubuntu24.04", "linux/amd64"},
}

// toolsBaseImage is an image by digest, the kind of base the step takes it for, and the
// platform to run it on, "" for the engine's.
type toolsBaseImage struct{ ref, kind, platform string }

// platformArgs are the arguments of docker build and docker run that select the platform.
func (b toolsBaseImage) platformArgs() []string {
	if b.platform == "" {
		return nil
	}
	return []string{"--platform", b.platform}
}

// filmScript runs in the image as the wall user, with a writable HOME, which Chromium
// needs and a run's session has: it starts Chromium headless on a page,
// writes a screenshot, makes a film of it with ffmpeg, reads the film with ffprobe, then
// runs the parsers' smoke test, which arrives on stdin.
const filmScript = `set -eu
[ -w "${HOME:-/nonexistent}" ] || { export HOME=/tmp/film-home; mkdir -p "$HOME"; }
cat > /tmp/smoke.js
chromium --headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --user-data-dir=/tmp/profile \
  --window-size=320,240 --screenshot=/tmp/shot.png 'data:text/html,<h1>film</h1>' >/dev/null 2>&1
test -s /tmp/shot.png
ffmpeg -loglevel error -y -loop 1 -framerate 2 -i /tmp/shot.png -t 1 -pix_fmt yuv420p /tmp/film.mp4
test -s /tmp/film.mp4
echo "film: $(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 /tmp/film.mp4)"
echo "ids: $(id -u):$(id -g) root=$(stat -c %u /opt/qory/parsers /opt/qory/parsers/node_modules/typescript/package.json | tr '\n' ' ')"
node /tmp/smoke.js /opt/qory/parsers
`

// skipUnlessDocker skips the test unless QORY_TEST_DOCKER=1 and docker are there, and
// when needCLI is set, the devcontainer command at [prebuild.CLIVersion] too.
func skipUnlessDocker(t *testing.T, needCLI bool) {
	t.Helper()
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if !needCLI {
		return
	}
	if out, err := exec.Command("devcontainer", "--version").Output(); err != nil || strings.TrimSpace(string(out)) != prebuild.CLIVersion {
		t.Skip("no devcontainer command at " + prebuild.CLIVersion)
	}
}

// runFilm runs [filmScript] in the image tag as the wall user, with no capability, no
// new privilege and no network, and checks what it printed: Chromium started, ffmpeg
// wrote a film file, root owns the parsers and each parser loads.
func runFilm(t *testing.T, tag string, platform []string) {
	t.Helper()
	smoke, err := os.ReadFile(filepath.Join("..", "..", "images", "agent", "parsers", "smoke.js"))
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"run", "--rm", "-i"}, platform...)
	args = append(args, "--user", "1234:2345", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--network", "none", "--entrypoint", "/bin/sh", tag, "-c", filmScript)
	c := exec.Command("docker", args...)
	c.Stdin = bytes.NewReader(smoke)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		t.Fatalf("the film script: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	t.Logf("in the image:\n%s", stdout.String())
	for _, want := range []string{"film: h264,", "ids: 1234:2345 root=0 0 ", "parsers: 5 packages in /opt/qory/parsers load and parse"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the film script printed\n%s\nwant %q", stdout.String(), want)
		}
	}
}

// TestBuildAddsTheToolsWithDocker builds a prebuilt development container image of
// Microsoft's, base, which has no node, on this machine's engine, and runs the built
// image: Chromium starts, ffmpeg writes a film file, and each parser loads with the node
// the step copied. It runs only with QORY_TEST_DOCKER=1, docker and the devcontainer
// command at [prebuild.CLIVersion] on the PATH, and it pulls the images, by digest.
func TestBuildAddsTheToolsWithDocker(t *testing.T) {
	skipUnlessDocker(t, true)
	dir := t.TempDir()
	platform := docker(t, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	_, arch, _ := strings.Cut(platform, "/")
	helper := filepath.Join(dir, "qory-linux")
	build := exec.Command("go", "build", "-o", helper, ".")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	parsers, node := stageTools(t, dir)
	var r [4]byte
	_, _ = rand.Read(r[:])
	repo := filepath.Join(dir, "repo")
	p := filepath.Join(repo, ".devcontainer", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"image": "`+toolsBases["base"].ref+`", "onCreateCommand": "true"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := "qory-test-tools:" + hex.EncodeToString(r[:])
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", tag).Run() })
	var out, log bytes.Buffer
	res, err := prebuild.Build(t.Context(), prebuild.Options{
		Repository: repo, User: "1234:2345", Tag: tag, Helper: helper, SeedArgs: []string{"run", "seed"},
		Env: os.Environ(), Out: &out, Log: &log, Parsers: parsers, Node: node,
	})
	t.Logf("lines:\n%s", out.String())
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, log.String())
	}
	if res.Environment != "" {
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", res.Environment).Run() })
	}
	if !strings.Contains(out.String(), "prebuild: added Chromium, ffmpeg and the parsers in ") {
		t.Errorf("the tools step printed no line:\n%s", out.String())
	}
	runFilm(t, tag, nil)
}

// TestToolsStepWithDocker builds the tools step alone, as qory builds it, FROM each of
// [toolsBases] with the wall user 1234:2345, and runs the result as in
// [TestBuildAddsTheToolsWithDocker]. It does not need the devcontainer command, and it
// runs only with QORY_TEST_DOCKER=1 and docker.
func TestToolsStepWithDocker(t *testing.T) {
	skipUnlessDocker(t, false)
	dir := t.TempDir()
	parsers, node := stageTools(t, dir)
	var r [4]byte
	_, _ = rand.Read(r[:])
	id := hex.EncodeToString(r[:])
	for name, base := range toolsBases {
		t.Run(name, func(t *testing.T) {
			// Whether the base has node, as the step's probe reads it.
			hasNode := exec.Command("docker", append(append([]string{"run", "--rm"}, base.platformArgs()...), "--entrypoint", "sh", base.ref, "-c", "command -v node")...).Run() == nil
			ctx := filepath.Join(dir, "ctx-"+name)
			if err := prebuild.WriteToolsContext(ctx, base.ref, base.kind, "1234:2345", parsers, node, !hasNode); err != nil {
				t.Fatal(err)
			}
			tag := "qory-test-tools:" + id + "-" + name
			t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", tag).Run() })
			docker(t, append(append([]string{"build"}, base.platformArgs()...), "--tag", tag, "--file", filepath.Join(ctx, "Dockerfile"), ctx)...)
			t.Logf("node in the base: %v; size of the base %s, of the image %s", hasNode,
				docker(t, "image", "inspect", "--format", "{{.Size}}", base.ref), docker(t, "image", "inspect", "--format", "{{.Size}}", tag))
			runFilm(t, tag, base.platformArgs())
		})
	}
}
