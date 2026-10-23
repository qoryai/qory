//go:build unix

package prebuild_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// testRegistry is the instance's registry of the tests.
const testRegistry = "registry.example.com"

// pinnedDigest is a digest of no image.
const pinnedDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestBuildKeepsTheEnvironmentImageByTheSharingRule is each case of the sharing rule:
// the image form and a build in .devcontainer/ keep the environment image apart, tagged
// and left for the caller; a Dockerfile, a build context or a local feature outside
// .devcontainer/, and a variable of the host, keep none, with a line with the
// reason.
func TestBuildKeepsTheEnvironmentImageByTheSharingRule(t *testing.T) {
	const dockerfile = "FROM alpine:3.20\n"
	cases := []struct {
		name   string
		files  map[string]string
		link   [2]string
		reason string
	}{
		{name: "image form", files: map[string]string{".devcontainer/devcontainer.json": `{"image": "alpine:3.20"}`}},
		{name: "image form in .devcontainer.json", files: map[string]string{".devcontainer.json": `{"image": "alpine:3.20"}`}},
		{name: "build in .devcontainer", files: map[string]string{
			".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile"}}`, ".devcontainer/Dockerfile": dockerfile}},
		{name: "build with a context in .devcontainer", files: map[string]string{
			".devcontainer/devcontainer.json": `{"build": {"dockerfile": "docker/Dockerfile", "context": "."}}`, ".devcontainer/docker/Dockerfile": dockerfile}},
		{name: "local feature in .devcontainer", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"./feature": {}}}`, ".devcontainer/feature/install.sh": "true\n"}},
		{name: "context at the root", files: map[string]string{
			".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile", "context": ".."}}`, ".devcontainer/Dockerfile": dockerfile},
			reason: "the build context is outside .devcontainer/"},
		{name: "context through a link to the root", files: map[string]string{
			".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile", "context": "root"}}`, ".devcontainer/Dockerfile": dockerfile},
			link: [2]string{"..", ".devcontainer/root"}, reason: "the build context is outside .devcontainer/"},
		{name: "build in .devcontainer.json", files: map[string]string{
			".devcontainer.json": `{"build": {"dockerfile": ".devcontainer/Dockerfile", "context": ".devcontainer"}}`, ".devcontainer/Dockerfile": dockerfile}},
		{name: "build in .devcontainer.json from the root", files: map[string]string{
			".devcontainer.json": `{"build": {"dockerfile": "Dockerfile"}}`, "Dockerfile": dockerfile},
			reason: "the Dockerfile is outside .devcontainer/"},
		{name: "Dockerfile at the root", files: map[string]string{
			".devcontainer/devcontainer.json": `{"build": {"dockerfile": "../Dockerfile", "context": "."}}`, "Dockerfile": dockerfile},
			reason: "the Dockerfile is outside .devcontainer/"},
		{name: "local feature at the root", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"../feature": {}}}`, "feature/install.sh": "true\n"},
			reason: "a local feature is outside .devcontainer/"},
		{name: "local feature of .devcontainer.json", files: map[string]string{
			".devcontainer.json": `{"image": "alpine:3.20", "features": {"./feature": {}}}`, "feature/install.sh": "true\n"},
			reason: "a local feature is outside .devcontainer/"},
		{name: "a variable of the host in a feature's option", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"ghcr.io/devcontainers/features/node:1": {"version": "${localEnv:NODE}"}}}`},
			reason: "a build member uses ${localEnv:…}, ${env:…}, ${localWorkspaceFolder…} or ${devcontainerId}"},
		{name: "a variable of the host by its earlier name in a feature's option", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"ghcr.io/devcontainers/features/node:1": {"version": "${env:NODE}"}}}`},
			reason: "a build member uses ${localEnv:…}, ${env:…}, ${localWorkspaceFolder…} or ${devcontainerId}"},
		{name: "the container's id in a feature's option", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"ghcr.io/devcontainers/features/node:1": {"name": "${devcontainerId}"}}}`},
			reason: "a build member uses ${localEnv:…}, ${env:…}, ${localWorkspaceFolder…} or ${devcontainerId}"},
		{name: "the workspace folder in a feature's option", files: map[string]string{
			".devcontainer/devcontainer.json": `{"image": "alpine:3.20", "features": {"ghcr.io/devcontainers/features/node:1": {"path": "${localWorkspaceFolderBasename}"}}}`},
			reason: "a build member uses ${localEnv:…}, ${env:…}, ${localWorkspaceFolder…} or ${devcontainerId}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes(t)
			repo := t.TempDir()
			writeFiles(t, repo, c.files)
			if c.link[0] != "" {
				if err := os.Symlink(c.link[0], filepath.Join(repo, filepath.FromSlash(c.link[1]))); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			res, err := prebuild.Build(t.Context(), options(t, repo, &out, io.Discard))
			if err != nil {
				t.Fatalf("Build: %v\n%s", err, out.String())
			}
			removed := f.calls("docker")[len(f.calls("docker"))-1]
			if c.reason == "" {
				if res.Environment != testName+":environment" || !f.called("docker", "tag "+testName+":base "+testName+":environment") {
					t.Errorf("kept %q; docker ran\n%s", res.Environment, f.read("docker.log"))
				}
				if !strings.Contains(out.String(), "prebuild: kept the environment image as "+testName+":environment\n") {
					t.Errorf("printed\n%s", out.String())
				}
				if strings.Contains(removed, ":environment") {
					t.Errorf("the environment image is removed: %s", removed)
				}
				return
			}
			if res.Environment != "" || f.called("docker", "tag") {
				t.Errorf("kept %q; docker ran\n%s", res.Environment, f.read("docker.log"))
			}
			want := "prebuild: kept no separate environment image, since " + c.reason + "; the target image contains the whole build\n"
			if !strings.Contains(out.String(), want) {
				t.Errorf("printed\n%s\nwant\n%s", out.String(), want)
			}
		})
	}
}

// TestBuildRemovesTheEnvironmentImageOnFailure is a build that kept the environment
// image apart and then failed: the image is removed with the others.
func TestBuildRemovesTheEnvironmentImageOnFailure(t *testing.T) {
	f := newFakes(t)
	f.set(t, "start.sh", "exit 137\n")
	o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
	o.EnvironmentTag = "acme/shop-environment:1"
	res, err := prebuild.Build(t.Context(), o)
	if err == nil || res != (prebuild.Result{}) {
		t.Fatalf("Build: %+v, %v", res, err)
	}
	if !f.called("docker", "tag "+testName+":base acme/shop-environment:1") ||
		!f.called("docker", "image rm --force "+testName+":base "+testName+":top acme/shop-environment:1") {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
}

// TestBuildStartsFromAStandardImage is the image form of a standard image, shared/<name>,
// with no feature and no Dockerfile: no environment step, the target step from the
// image by digest with the agent added there, and the image as the base. With a feature,
// or from a Dockerfile, the environment step builds, and the base is the standard image
// still.
func TestBuildStartsFromAStandardImage(t *testing.T) {
	const standard = testRegistry + "/shared/factory"
	t.Run("alone", func(t *testing.T) {
		f := newFakes(t)
		var out bytes.Buffer
		o := options(t, repository(t, `{"image": "`+standard+`:1", "onCreateCommand": "make"}`), &out, io.Discard)
		o.Registry = testRegistry
		res, err := prebuild.Build(t.Context(), o)
		if err != nil {
			t.Fatalf("Build: %v\n%s", err, out.String())
		}
		base := standard + "@sha256:" + baseDigest
		if res != (prebuild.Result{Target: "acme/shop:prebuild", Base: base}) {
			t.Errorf("the result is %+v", res)
		}
		if got := f.calls("devcontainer"); !slices.Equal(got, []string{"--version"}) {
			t.Errorf("devcontainer ran %q", got)
		}
		if !f.called("docker", "pull "+standard+":1") || f.called("docker", "tag") {
			t.Errorf("docker ran\n%s", f.read("docker.log"))
		}
		if !strings.HasPrefix(f.read("dockerfiles"), "FROM "+base+"\nCOPY --from="+agentImage+" /qory/agent /qory/agent\n") {
			t.Errorf("the Dockerfiles are\n%s", f.read("dockerfiles"))
		}
		if !strings.Contains(out.String(), "prebuild: pulled the standard image "+base+" in ") ||
			!strings.Contains(out.String(), "; the environment step adds nothing to it\n") ||
			strings.Contains(out.String(), "development container image") {
			t.Errorf("printed\n%s", out.String())
		}
	})
	for name, files := range map[string]map[string]string{
		"with a feature": {".devcontainer/devcontainer.json": `{"image": "` + standard + `:1", "features": {"ghcr.io/devcontainers/features/node:1": {}}}`},
		"from a Dockerfile": {".devcontainer/devcontainer.json": `{"build": {"dockerfile": "Dockerfile"}}`,
			".devcontainer/Dockerfile": "FROM " + standard + ":1\nRUN true\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakes(t)
			repo := t.TempDir()
			writeFiles(t, repo, files)
			o := options(t, repo, io.Discard, io.Discard)
			o.Registry = testRegistry
			res, err := prebuild.Build(t.Context(), o)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if res != (prebuild.Result{Environment: testName + ":environment", Target: "acme/shop:prebuild", Base: standard + "@sha256:" + baseDigest}) {
				t.Errorf("the result is %+v", res)
			}
			if !f.called("devcontainer", "build") || !f.called("docker", "pull "+standard+":1") {
				t.Errorf("ran\n%s\n%s", f.read("devcontainer.log"), f.read("docker.log"))
			}
		})
	}
}

// TestBuildStartsFromAnEnvironmentImage is a build given an environment image: no
// environment step and no rewrite, the target step from that image with the agent added
// there, no environment image kept and no base.
func TestBuildStartsFromAnEnvironmentImage(t *testing.T) {
	f := newFakes(t)
	env := testRegistry + "/acme/environments/0123456789abcdef@" + pinnedDigest
	var out bytes.Buffer
	o := options(t, repository(t, `{"image": "debian:12", "features": {"ghcr.io/devcontainers/features/node:1": {}}}`), &out, io.Discard)
	o.Registry, o.EnvironmentImage = testRegistry, env
	res, err := prebuild.Build(t.Context(), o)
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if res != (prebuild.Result{Target: "acme/shop:prebuild"}) {
		t.Errorf("the result is %+v", res)
	}
	if got := f.calls("devcontainer"); !slices.Equal(got, []string{"--version"}) {
		t.Errorf("devcontainer ran %q", got)
	}
	if !f.called("docker", "pull "+env) || f.called("docker", "image inspect --format {{json .RepoDigests}}") || !f.called("docker", "image inspect --format {{json .Config}} "+env) {
		t.Errorf("docker ran\n%s", f.read("docker.log"))
	}
	if !strings.HasPrefix(f.read("dockerfiles"), "FROM "+env+"\nCOPY --from="+agentImage+" /qory/agent /qory/agent\nUSER 1234:2345\n") {
		t.Errorf("the Dockerfiles are\n%s", f.read("dockerfiles"))
	}
	if want := "prebuild: pulled the environment image " + env + " in "; !strings.Contains(out.String(), want) || strings.Contains(out.String(), "rewrote") {
		t.Errorf("printed\n%s\nwant %q", out.String(), want)
	}
}

// TestBuildRefusesAPulledImage is an environment image whose devcontainer.metadata asks
// for access to the host, or whose environment changes what the agent does: refused,
// named, before the target step.
func TestBuildRefusesAPulledImage(t *testing.T) {
	env := testRegistry + "/acme/environments/0123456789abcdef@" + pinnedDigest
	for config, want := range map[string]string{
		`{"Labels":{"devcontainer.metadata":"[{\"privileged\":true}]"}}`: "build from an image whose devcontainer.metadata label sets no access to the host: " + env + " sets privileged",
		`{"Labels":{"devcontainer.metadata":"[{"}}`:                      "build from an image whose devcontainer.metadata label is valid JSON: the label of " + env + " is not",
		`{"Env":["LD_PRELOAD=/x.so"]}`:                                   "build from an image whose environment does not set LD_PRELOAD, since these variables change what the agent does: " + env + " sets them",
	} {
		t.Run(want[:40], func(t *testing.T) {
			f := newFakes(t)
			f.set(t, "config-"+strings.NewReplacer(":", "_", "/", "_").Replace(env), config)
			o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
			o.EnvironmentImage = env
			_, err := prebuild.Build(t.Context(), o)
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Fatalf("Build: %v\nwant %s", err, want)
			}
			if f.called("docker", "build") || f.called("docker", "create") {
				t.Errorf("docker ran\n%s", f.read("docker.log"))
			}
		})
	}
}

// TestBuildRefusesTheOptionsOfTheSteps is each option of the two steps no image is
// built from: refused before any program runs.
func TestBuildRefusesTheOptionsOfTheSteps(t *testing.T) {
	for want, set := range map[string]func(o *prebuild.Options){
		`give the build the registry's address as a host name, with a port or none: "https://registry.example.com" is not one`: func(o *prebuild.Options) { o.Registry = "https://registry.example.com" },
		`give the build each host that has a credential as a host name: "ghcr.io/acme" is not one`:                             func(o *prebuild.Options) { o.CredentialHosts = []string{"ghcr.io/acme"} },
		`give the environment image as name@sha256:<64 hexadecimal digits>: "` + testRegistry + `/acme/environments/0123456789abcdef:1" is not in that form`: func(o *prebuild.Options) {
			o.EnvironmentImage = testRegistry + "/acme/environments/0123456789abcdef:1"
		},
		`give the build a name for the environment image with no digest: "--privileged" is not one`: func(o *prebuild.Options) { o.EnvironmentTag = "--privileged" },
	} {
		t.Run(want[:30], func(t *testing.T) {
			f := newFakes(t)
			o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
			set(&o)
			_, err := prebuild.Build(t.Context(), o)
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Fatalf("Build: %v\nwant %s", err, want)
			}
			if f.read("docker.log") != "" || f.read("devcontainer.log") != "" {
				t.Error("a program ran")
			}
		})
	}
}

// TestBuildRewritesToTheRegistrysCopy is a definition with an image or a Dockerfile, and
// features: the devcontainer command reads a copy in which each public image on a host
// with no credential is the registry's copy, tag and digest kept, and each Feature ID
// stays as written, in features and overrideFeatureInstallOrder; the repository's files
// stay as they are; the base is pulled from the copy.
func TestBuildRewritesToTheRegistrysCopy(t *testing.T) {
	const copied = testRegistry + "/upstream/"
	t.Run("image", func(t *testing.T) {
		f := newFakes(t)
		config := `{"image": "debian:12", "features": {"ghcr.io/devcontainers/features/git:1": {}, "./local": {}},
			"overrideFeatureInstallOrder": ["./local", "ghcr.io/devcontainers/features/git"]}`
		repo := repository(t, config)
		var out bytes.Buffer
		o := options(t, repo, &out, io.Discard)
		o.Registry = testRegistry
		res, err := prebuild.Build(t.Context(), o)
		if err != nil {
			t.Fatalf("Build: %v\n%s", err, out.String())
		}
		var read struct {
			Image    string                     `json:"image"`
			Features map[string]json.RawMessage `json:"features"`
			Order    []string                   `json:"overrideFeatureInstallOrder"`
		}
		if err := json.Unmarshal([]byte(f.read("build.config")), &read); err != nil {
			t.Fatalf("the devcontainer command read %q: %v", f.read("build.config"), err)
		}
		if read.Image != copied+"docker.io/library/debian:12" || read.Features["ghcr.io/devcontainers/features/git:1"] == nil || len(read.Features) != 2 {
			t.Errorf("the devcontainer command read %s", f.read("build.config"))
		}
		// The local feature is moved; the one in a registry stays as written.
		if len(read.Order) != 2 || !strings.HasSuffix(read.Order[0], "/.devcontainer/local") || read.Order[1] != "ghcr.io/devcontainers/features/git" {
			t.Errorf("the install order is %q", read.Order)
		}
		if data, _ := os.ReadFile(filepath.Join(repo, ".devcontainer", "devcontainer.json")); string(data) != config {
			t.Errorf("the repository's configuration changed:\n%s", data)
		}
		if want := "prebuild: rewrote debian:12 to the registry's copy, " + copied + "docker.io/library/debian:12\n"; !strings.Contains(out.String(), want) || strings.Count(out.String(), "rewrote") != 1 {
			t.Errorf("printed\n%s\nwant\n%s", out.String(), want)
		}
		if !f.called("docker", "pull "+copied+"docker.io/library/debian:12") || res.Base != copied+"docker.io/library/debian@sha256:"+baseDigest {
			t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
		}
	})
	t.Run("pinned image", func(t *testing.T) {
		f := newFakes(t)
		o := options(t, repository(t, `{"image": "alpine:3.20@`+pinnedDigest+`"}`), io.Discard, io.Discard)
		o.Registry = testRegistry
		res, err := prebuild.Build(t.Context(), o)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if res.Base != copied+"docker.io/library/alpine@"+pinnedDigest || f.called("docker", "pull") {
			t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
		}
		if !strings.Contains(f.read("build.config"), `"image":"`+copied+`docker.io/library/alpine:3.20@`+pinnedDigest+`"`) {
			t.Errorf("the devcontainer command read %s", f.read("build.config"))
		}
	})
	t.Run("Dockerfile", func(t *testing.T) {
		f := newFakes(t)
		dockerfile := "ARG V=11\nFROM mcr.microsoft.com/devcontainers/base:${V} AS base\n" +
			"FROM base\nCOPY --from=ghcr.io/acme/private:1 /p /p\nCOPY --from=quay.io/acme/tool@" + pinnedDigest + " /t /t\n"
		config := `{"build": {"dockerfile": "Dockerfile", "args": {"V": "12"}}, "features": {"ghcr.io/devcontainers/features/node:1": {}}}`
		repo := repository(t, config)
		writeFiles(t, repo, map[string]string{".devcontainer/Dockerfile": dockerfile, ".devcontainer/Dockerfile.dockerignore": "secret\n"})
		// The fake keeps the Dockerfile's copy, which the build removes after.
		f.set(t, "build.sh", `cp -R "$(dirname "$config")/../dockerfile" `+f.path("dockerfile-copy")+"\n")
		var out bytes.Buffer
		o := options(t, repo, &out, io.Discard)
		o.Registry, o.CredentialHosts = testRegistry, []string{"GHCR.io"}
		res, err := prebuild.Build(t.Context(), o)
		if err != nil {
			t.Fatalf("Build: %v\n%s", err, out.String())
		}
		var read struct {
			Build struct {
				Dockerfile, Context string
			} `json:"build"`
			Features map[string]json.RawMessage `json:"features"`
		}
		if err := json.Unmarshal([]byte(f.read("build.config")), &read); err != nil {
			t.Fatalf("the devcontainer command read %q: %v", f.read("build.config"), err)
		}
		if !filepath.IsAbs(read.Build.Dockerfile) || filepath.Base(read.Build.Dockerfile) != "Dockerfile" || read.Features["ghcr.io/devcontainers/features/node:1"] == nil {
			t.Errorf("the devcontainer command read %s", f.read("build.config"))
		}
		// The context is the original Dockerfile's folder, from the copy's folder.
		if filepath.Clean(filepath.Join(filepath.Dir(read.Build.Dockerfile), "..", "devcontainer", read.Build.Context)) != filepath.Join(repo, ".devcontainer") {
			t.Errorf("the context is %q", read.Build.Context)
		}
		want := "ARG V=11\nFROM " + copied + "mcr.microsoft.com/devcontainers/base:${V} AS base\n" +
			"FROM base\nCOPY --from=ghcr.io/acme/private:1 /p /p\nCOPY --from=" + copied + "quay.io/acme/tool@" + pinnedDigest + " /t /t\n"
		if got := f.read("dockerfile-copy/Dockerfile"); got != want {
			t.Errorf("the Dockerfile's copy is\n%s\nwant\n%s", got, want)
		}
		if got := f.read("dockerfile-copy/Dockerfile.dockerignore"); got != "secret\n" {
			t.Errorf("the .dockerignore's copy is %q", got)
		}
		if data, _ := os.ReadFile(filepath.Join(repo, ".devcontainer", "Dockerfile")); string(data) != dockerfile {
			t.Errorf("the repository's Dockerfile changed:\n%s", data)
		}
		if data, _ := os.ReadFile(filepath.Join(repo, ".devcontainer", "devcontainer.json")); string(data) != config {
			t.Errorf("the repository's configuration changed:\n%s", data)
		}
		if !f.called("docker", "pull "+copied+"mcr.microsoft.com/devcontainers/base:12") || res.Base != copied+"mcr.microsoft.com/devcontainers/base@sha256:"+baseDigest {
			t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
		}
		if strings.Contains(out.String(), "ghcr.io/acme/private") || strings.Count(out.String(), "rewrote") != 2 {
			t.Errorf("printed\n%s", out.String())
		}
	})
	t.Run("no base to report", func(t *testing.T) {
		f := newFakes(t)
		repo := repository(t, `{"build": {"dockerfile": "Dockerfile"}}`)
		writeFiles(t, repo, map[string]string{".devcontainer/Dockerfile": "ARG IMAGE\nFROM ${IMAGE}\n"})
		var out bytes.Buffer
		o := options(t, repo, &out, io.Discard)
		o.Registry = testRegistry
		res, err := prebuild.Build(t.Context(), o)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if res.Base != "" || f.called("docker", "pull") || f.read("build.config") != f.read("read-configuration.config") {
			t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
		}
		if !strings.Contains(out.String(), "prebuild: no base image to report: the Dockerfile starts FROM scratch, or from an image whose name or platform is known only during the build\n") {
			t.Errorf("printed\n%s", out.String())
		}
	})
}

// TestBuildFailsWhenTheBasePullFails is a base image the engine cannot pull: the build
// fails before the environment step, with the image and docker's last line.
func TestBuildFailsWhenTheBasePullFails(t *testing.T) {
	f := newFakes(t)
	f.set(t, "pull.sh", "echo 'Error response from daemon: manifest unknown' >&2; exit 1\n")
	_, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
	want := "check that the Docker engine reaches the registry of example.com/acme/shop-dev:1: docker pull exited with an error: Error response from daemon: manifest unknown"
	if err == nil || err.Error() != want {
		t.Fatalf("Build: %v\nwant %s", err, want)
	}
	if f.called("devcontainer", "build") {
		t.Error("the environment step ran")
	}
}

// TestBuildPullsTheBaseForTheBuildsPlatform is a Dockerfile whose last stage starts
// FROM --platform=$BUILDPLATFORM: the base is pulled for the engine's platform, and a
// --platform whose variable has no value prints the line of no base.
func TestBuildPullsTheBaseForTheBuildsPlatform(t *testing.T) {
	f := newFakes(t)
	repo := repository(t, `{"build": {"dockerfile": "Dockerfile"}}`)
	writeFiles(t, repo, map[string]string{".devcontainer/Dockerfile": "FROM --platform=$BUILDPLATFORM ubuntu:22.04\n"})
	var out bytes.Buffer
	res, err := prebuild.Build(t.Context(), options(t, repo, &out, io.Discard))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Base != "ubuntu@sha256:"+baseDigest || !f.called("docker", "pull --platform linux/amd64 ubuntu:22.04") || strings.Contains(out.String(), "no base image") {
		t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
	}
	f = newFakes(t)
	repo = repository(t, `{"build": {"dockerfile": "Dockerfile"}}`)
	writeFiles(t, repo, map[string]string{".devcontainer/Dockerfile": "FROM --platform=$P ubuntu:22.04\n"})
	out.Reset()
	if res, err = prebuild.Build(t.Context(), options(t, repo, &out, io.Discard)); err != nil || res.Base != "" || f.called("docker", "pull") {
		t.Fatalf("Build: %v, base %q", err, res.Base)
	}
	if want := "prebuild: no base image to report: the Dockerfile starts FROM scratch, or from an image whose name or platform is known only during the build\n"; !strings.Contains(out.String(), want) {
		t.Errorf("printed\n%s\nwant %q", out.String(), want)
	}
}

// TestBuildPullsTheBaseForItsPlatform is a Dockerfile whose last stage sets --platform:
// the base is pulled for that platform and reported.
func TestBuildPullsTheBaseForItsPlatform(t *testing.T) {
	f := newFakes(t)
	repo := repository(t, `{"build": {"dockerfile": "Dockerfile"}}`)
	writeFiles(t, repo, map[string]string{".devcontainer/Dockerfile": "FROM --platform=linux/amd64 ubuntu:22.04\n"})
	res, err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Base != "ubuntu@sha256:"+baseDigest || !f.called("docker", "pull --platform linux/amd64 ubuntu:22.04") {
		t.Errorf("the base is %q; docker ran\n%s", res.Base, f.read("docker.log"))
	}
}

// TestBuildFindsTheEnvironmentImageMissing is a pull of the environment image that
// fails: ErrImageMissing only when Docker found no manifest, no repository, or no image
// by the reference; a denied pull, an unreachable registry and a timeout are ordinary
// failures, as is a missing base image. The text is the pull's either way.
func TestBuildFindsTheEnvironmentImageMissing(t *testing.T) {
	env := testRegistry + "/acme/environments/0123456789abcdef@" + pinnedDigest
	for why, missing := range map[string]bool{
		"Error response from daemon: manifest for " + env + " not found: manifest unknown: manifest unknown":                                                                                                                      true,
		"Error response from daemon: repository " + testRegistry + "/acme/environments/0123456789abcdef not found: name unknown: ":                                                                                                true,
		`Error response from daemon: failed to resolve reference "` + env + `": ` + env + `: not found`:                                                                                                                           true,
		`Error response from daemon: failed to resolve reference "` + env + `": ` + testRegistry + `/acme/base:1: not found`:                                                                                                      false,
		"Error response from daemon: pull access denied for " + testRegistry + "/acme/environments/0123456789abcdef, repository does not exist or may require 'docker login': denied: requested access to the resource is denied": false,
		`Error response from daemon: Get "https://` + testRegistry + `/v2/": dial tcp: lookup ` + testRegistry + `: no such host`:                                                                                                 false,
		`Error response from daemon: Get "https://` + testRegistry + `/v2/": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)`:                                            false,
	} {
		t.Run(why[28:60], func(t *testing.T) {
			f := newFakes(t)
			f.set(t, "pull.sh", "echo 'Pulling' >&2; printf '%s\\n' '"+strings.ReplaceAll(why, "'", `'\''`)+"' >&2; exit 1\n")
			o := options(t, repository(t, shopConfig), io.Discard, io.Discard)
			o.Registry, o.EnvironmentImage = testRegistry, env
			_, err := prebuild.Build(t.Context(), o)
			// The line is cut to one printable line; what is matched is the whole line.
			want := "check that the Docker engine reaches the registry of " + env + ": docker pull exited with an error: Error response from daemon: "
			if missing {
				want = "check that " + env + " is in its registry: docker pull found no manifest for it: Error response from daemon: "
			}
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("Build: %v\nwant %s", err, want)
			}
			if got := errors.Is(err, prebuild.ErrImageMissing); got != missing {
				t.Errorf("errors.Is(err, ErrImageMissing) is %v, want %v", got, missing)
			}
		})
	}
	// The base image of a build, missing, is an ordinary failure.
	f := newFakes(t)
	f.set(t, "pull.sh", "echo 'Error response from daemon: manifest for example.com/acme/shop-dev:1 not found: manifest unknown: manifest unknown' >&2; exit 1\n")
	_, err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), io.Discard, io.Discard))
	if err == nil || errors.Is(err, prebuild.ErrImageMissing) {
		t.Errorf("Build of a missing base: %v, ErrImageMissing %v", err, errors.Is(err, prebuild.ErrImageMissing))
	}
}
