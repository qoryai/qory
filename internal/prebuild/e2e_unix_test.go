//go:build unix

package prebuild_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// The images the test pulls, Docker Hub's official ones, named by the digest of each
// multi-platform index.
const (
	registryImage = "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	alpineImage   = "alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"
)

// docker runs docker and returns its standard output, failing the test when it fails.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	c := exec.Command("docker", args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// TestBuildWithDocker builds a tiny development container on this machine's engine with
// the reference devcontainer command: an alpine image whose onCreateCommand writes a
// file. The image keeps the file under /qory/seed, the wall user's; it has the agent
// image's /qory/agent, from an image pushed to a registry run for the test, its user is
// the wall user, its working folder /workspace, empty, and it has nothing at
// /qory/hooks. It runs only with QORY_TEST_DOCKER=1, docker and the devcontainer
// command at [prebuild.CLIVersion] on the PATH, and it pulls registry:2 and alpine, by digest.
func TestBuildWithDocker(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if out, err := exec.Command("devcontainer", "--version").Output(); err != nil || strings.TrimSpace(string(out)) != prebuild.CLIVersion {
		t.Skip("no devcontainer command at " + prebuild.CLIVersion)
	}
	var r [4]byte
	_, _ = rand.Read(r[:])
	id := hex.EncodeToString(r[:])
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

	registry := docker(t, "run", "--detach", "--rm", "--publish", "127.0.0.1::5000", registryImage)
	// With its anonymous volume: the image declares one, and a container removed by
	// docker rm leaves it unless asked.
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", "--volumes", registry).Run() })
	port := docker(t, "port", registry, "5000/tcp")
	_, port, _ = strings.Cut(strings.Split(port, "\n")[0], ":")
	agentName := "127.0.0.1:" + port + "/qory-test-agent"
	agentDir := filepath.Join(dir, "agent")
	for name, content := range map[string]string{
		"Dockerfile":          "FROM scratch\nCOPY qory /qory\n",
		"qory/agent/AGENT.md": "the agent\n",
	} {
		p := filepath.Join(agentDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docker(t, "build", "--quiet", "--tag", agentName+":"+id, agentDir)
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", agentName+":"+id).Run() })
	docker(t, "push", "--quiet", agentName+":"+id)
	var digests []string
	if err := json.Unmarshal([]byte(docker(t, "image", "inspect", "--format", "{{json .RepoDigests}}", agentName+":"+id)), &digests); err != nil {
		t.Fatal(err)
	}
	agent := ""
	for _, d := range digests {
		if strings.HasPrefix(d, agentName+"@") {
			agent = d
		}
	}
	if agent == "" {
		t.Fatalf("the engine holds no digest of the agent image: %v", digests)
	}

	repo := filepath.Join(dir, "repo")
	for name, content := range map[string]string{
		".devcontainer/devcontainer.json": `{"image": "` + alpineImage + `", "containerEnv": {"SHOP_MODE": "test"}, "onCreateCommand": "echo $SHOP_MODE > created.txt && echo home > ~/x"}`,
		".git/HEAD":                       "ref: refs/heads/main\n",
		"README.md":                       "the shop\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The build gets a proxy and an authority, which no program uses here: NO_PROXY
	// is *. Neither reaches the image. Nor do the proxies of the machine's docker
	// configuration, one with a password, which docker create would put in the
	// container's environment and docker commit in the image's.
	dockerConfigWithProxy := proxiedDockerConfig(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:3128")
	t.Setenv("http_proxy", "http://proxy.example.com:3128")
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
	tag := "qory-test-prebuilt:" + id
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", tag).Run() })
	var out, log bytes.Buffer
	err := prebuild.Build(t.Context(), prebuild.Options{
		Repository: repo, AgentImage: agent, User: "1234:2345", Tag: tag,
		Helper: helper, SeedArgs: []string{"run", "seed"}, Env: append(os.Environ(), "DOCKER_CONFIG="+dockerConfigWithProxy), Out: &out, Log: &log,
	})
	t.Logf("lines:\n%s", out.String())
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, log.String())
	}

	var cfg struct {
		User, WorkingDir string
		Entrypoint, Cmd  []string
	}
	if err := json.Unmarshal([]byte(docker(t, "image", "inspect", "--format", "{{json .Config}}", tag)), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.User != "1234:2345" || cfg.WorkingDir != "/workspace" || cfg.Entrypoint != nil || strings.Join(cfg.Cmd, " ") != "/bin/sh" {
		t.Errorf("the image's configuration is %+v", cfg)
	}
	for _, args := range [][]string{{"image", "inspect", tag}, {"history", "--no-trunc", tag}} {
		if text := docker(t, args...); strings.Contains(text, "proxy.example.com") || strings.Contains(strings.ToUpper(text), "_PROXY") {
			t.Errorf("docker %s shows the proxy:\n%s", strings.Join(args, " "), text)
		}
	}
	got := docker(t, "run", "--rm", "--entrypoint", "/bin/sh", tag, "-c",
		`stat -c %u:%g /qory/seed/created.txt /qory/seed/.git/HEAD /qory/seed /workspace; cat /qory/seed/created.txt; cat /qory/agent/AGENT.md; `+
			`echo $HOME; stat -c %u:%g:%a /home/agent /home/agent/x; cat ~/x; `+
			`ls -A /workspace | wc -l; test -e /qory/hooks || echo no hooks; id -u`)
	want := "1234:2345\n1234:2345\n1234:2345\n1234:2345\ntest\nthe agent\n/home/agent\n1234:2345:700\n1234:2345:644\nhome\n0\nno hooks\n1234"
	if got != want {
		t.Errorf("inside the image:\n%s\nwant\n%s", got, want)
	}

	// A Dockerfile that puts managed settings of Claude Code in the image: refused, and
	// nothing left tagged.
	managed := filepath.Join(dir, "managed")
	for name, content := range map[string]string{
		".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile"}, "onCreateCommand": "true"}`,
		".devcontainer/Dockerfile":        "FROM " + alpineImage + "\nRUN mkdir /etc/claude-code\n",
	} {
		p := filepath.Join(managed, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refusedTag := "qory-test-prebuilt:" + id + "-managed"
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", refusedTag).Run() })
	out.Reset()
	err = prebuild.Build(t.Context(), prebuild.Options{
		Repository: managed, AgentImage: agent, User: "1234:2345", Tag: refusedTag,
		Helper: helper, SeedArgs: []string{"run", "seed"}, Env: append(os.Environ(), "DOCKER_CONFIG="+dockerConfigWithProxy), Out: &out, Log: &log,
	})
	t.Logf("lines:\n%s", out.String())
	var shown *prebuild.ShownError
	if !errors.As(err, &shown) || !strings.HasPrefix(shown.Line, "the image has something at /etc/claude-code") {
		t.Errorf("Build with /etc/claude-code: %v", err)
	}
	if exec.Command("docker", "image", "inspect", refusedTag).Run() == nil {
		t.Error("the refused image is tagged")
	}

	// A context that leaves the repository, from which a Dockerfile would copy the
	// build's docker configuration into the image and print it: refused before
	// anything is built.
	escape := filepath.Join(dir, "escape")
	for name, content := range map[string]string{
		".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile", "context": "../../../.."}}`,
		".devcontainer/Dockerfile":        "FROM " + alpineImage + "\nCOPY qory-prebuild-*/docker/config.json /leak/config.json\nRUN cat /leak/config.json\n",
	} {
		p := filepath.Join(escape, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	escapeTag := "qory-test-prebuilt:" + id + "-escape"
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", escapeTag).Run() })
	out.Reset()
	err = prebuild.Build(t.Context(), prebuild.Options{
		Repository: escape, AgentImage: agent, User: "1234:2345", Tag: escapeTag,
		Helper: helper, SeedArgs: []string{"run", "seed"}, Env: append(os.Environ(), "DOCKER_CONFIG="+dockerConfigWithProxy), Out: &out, Log: &log,
	})
	var refusal *prebuild.RefusedError
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "sets build.context to a place outside the repository") {
		t.Errorf("Build with a context outside: %v", err)
	}
	if exec.Command("docker", "image", "inspect", escapeTag).Run() == nil {
		t.Error("the image with a context outside is tagged")
	}

	if left := docker(t, "ps", "--all", "--quiet", "--filter", "name=qory-prebuild-"); left != "" {
		t.Errorf("containers are left: %s", left)
	}
	if left := docker(t, "image", "ls", "--quiet", "--filter", "reference=qory-prebuild-*"); left != "" {
		t.Errorf("images are left: %s", left)
	}
}

// proxiedDockerConfig writes a docker configuration folder that is the machine's, the
// folder DOCKER_CONFIG names or ~/.docker, with proxies that carry a password and
// apply to no host, and links to its contexts and plugins, and returns it.
func proxiedDockerConfig(t *testing.T) string {
	t.Helper()
	original := os.Getenv("DOCKER_CONFIG")
	if original == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		original = filepath.Join(home, ".docker")
	}
	config := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(original, "config.json")); err == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
	}
	config["proxies"] = map[string]any{"default": map[string]string{
		"httpProxy": "http://agent:pw@proxy.example.com:3128", "httpsProxy": "http://agent:pw@proxy.example.com:3128", "noProxy": "*",
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"contexts", "cli-plugins"} {
		if fi, err := os.Stat(filepath.Join(original, name)); err == nil && fi.IsDir() {
			if err := os.Symlink(filepath.Join(original, name), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}
