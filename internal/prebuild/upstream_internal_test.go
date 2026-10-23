package prebuild

import (
	"slices"
	"strings"
	"testing"
)

// testDigest is a digest of no image.
const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// testUpstream is the registry of the tests, with a credential for GitHub's.
var testUpstream = upstream{registry: "registry.example.com", credentialed: []string{"ghcr.io"}}

// TestUpstreamImage is each kind of image reference: on a public host with no
// credential, the registry's copy, with the tag or the digest kept and Docker Hub's
// short names written out; elsewhere, as written.
func TestUpstreamImage(t *testing.T) {
	const copied = "registry.example.com/upstream/"
	for ref, want := range map[string]string{
		"debian":                                 copied + "docker.io/library/debian",
		"debian:12":                              copied + "docker.io/library/debian:12",
		"library/debian:12":                      copied + "docker.io/library/debian:12",
		"docker.io/debian:12":                    copied + "docker.io/library/debian:12",
		"index.docker.io/library/debian:12":      copied + "docker.io/library/debian:12",
		"bitnami/redis:7":                        copied + "docker.io/bitnami/redis:7",
		"registry:2":                             copied + "docker.io/library/registry:2",
		"alpine:3.20@" + testDigest:              copied + "docker.io/library/alpine:3.20@" + testDigest,
		"mcr.microsoft.com/devcontainers/base:1": copied + "mcr.microsoft.com/devcontainers/base:1",
		"mcr.microsoft.com/base@" + testDigest:   copied + "mcr.microsoft.com/base@" + testDigest,
		"quay.io/acme/tool:1":                    copied + "quay.io/acme/tool:1",
		"QUAY.IO/acme/tool:1":                    copied + "quay.io/acme/tool:1",
		"mcr.microsoft.com/python:1-${VARIANT}":  copied + "mcr.microsoft.com/python:1-${VARIANT}",
		// On a host with a credential.
		"ghcr.io/acme/private:1": "ghcr.io/acme/private:1",
		// On no public host, the instance's registry and its standard images among them.
		"registry.example.com/shared/factory:1": "registry.example.com/shared/factory:1",
		"registry.example.com/upstream/x:1":     "registry.example.com/upstream/x:1",
		"example.com/acme/shop-dev:1":           "example.com/acme/shop-dev:1",
		"localhost:5000/shop":                   "localhost:5000/shop",
		"localhost/shop":                        "localhost/shop",
		// With a variable in the name.
		"${IMAGE}":        "${IMAGE}",
		"$IMAGE:1":        "$IMAGE:1",
		"debian${X}":      "debian${X}",
		"ghcr.io/${O}/x":  "ghcr.io/${O}/x",
		"docker.io/a${B}": "docker.io/a${B}",
		"":                "",
	} {
		if got := testUpstream.image(ref); got != want {
			t.Errorf("image(%q) = %q, want %q", ref, got, want)
		}
	}
	if got := (upstream{}).image("debian:12"); got != "debian:12" {
		t.Errorf("with no registry: %q", got)
	}
	port := upstream{registry: "registry.example.com", credentialed: []string{hostOf("GHCR.io:443")}}
	if got := port.image("ghcr.io/acme/private:1"); got != "ghcr.io/acme/private:1" {
		t.Errorf("with a credential for the host at port 443: %q", got)
	}
	if got := port.image("ghcr.io:443/acme/private:1"); got != "ghcr.io:443/acme/private:1" {
		t.Errorf("with a credential for the host, written with port 443: %q", got)
	}
	if got := (upstream{registry: "registry.example.com"}).image("ghcr.io:443/acme/x:1"); got != "registry.example.com/upstream/ghcr.io/acme/x:1" {
		t.Errorf("written with port 443: %q", got)
	}
	hub := upstream{registry: "registry.example.com", credentialed: []string{hostOf("index.docker.io")}}
	if got := hub.image("debian:12"); got != "debian:12" {
		t.Errorf("with a credential for Docker Hub by another name: %q", got)
	}
}

// TestRewriteDockerfile is a Dockerfile with each place an image is written: FROM with
// and without flags, a name and a variable in the tag, a FROM written over two lines, and
// COPY --from of an image, a stage's name and a stage's index. Images go to the
// registry's copy; stages, scratch, a credentialed host, the instance's registry, an
// image with a variable in its name and text in a here-document are left as they are.
func TestRewriteDockerfile(t *testing.T) {
	data := "# syntax=docker/dockerfile:1\n" +
		"ARG V=11\n" +
		"FROM mcr.microsoft.com/devcontainers/base:${V} AS base\n" +
		"FROM --platform=$BUILDPLATFORM golang:1.23 AS Tools\n" +
		"RUN <<EOF\nFROM inside-a-heredoc:1\nEOF\n" +
		"RUN <<-'END' sh\n\tFROM inside-another:1\n\tEND\n" +
		"FROM \\\n  # a comment\n  quay.io/acme/tool@" + testDigest + " AS tool\n" +
		"FROM scratch AS empty\n" +
		"FROM ${IMAGE}\n" +
		"FROM registry.example.com/shared/factory:1 AS factory\n" +
		"FROM base\n" +
		"COPY --from=tools /go/bin/x /usr/bin/x\n" +
		"COPY --chown=1:1 --from=ghcr.io/acme/private:1 /p /p\n" +
		"COPY --from=0 /a /a\n" +
		"copy --from=debian:12 /etc/os-release /os-release\n" +
		"COPY --from=quay.io/acme/tool@" + testDigest + " /t /t\n"
	got, done := testUpstream.rewriteDockerfile([]byte(data))
	want := strings.NewReplacer(
		"FROM mcr.microsoft.com/", "FROM registry.example.com/upstream/mcr.microsoft.com/",
		" golang:1.23", " registry.example.com/upstream/docker.io/library/golang:1.23",
		"  quay.io/acme/tool@", "  registry.example.com/upstream/quay.io/acme/tool@",
		"--from=debian:12", "--from=registry.example.com/upstream/docker.io/library/debian:12",
		"--from=quay.io/", "--from=registry.example.com/upstream/quay.io/",
	).Replace(data)
	if string(got) != want {
		t.Errorf("rewrote\n%s\nwant\n%s", got, want)
	}
	wantDone := [][2]string{
		{"mcr.microsoft.com/devcontainers/base:${V}", "registry.example.com/upstream/mcr.microsoft.com/devcontainers/base:${V}"},
		{"golang:1.23", "registry.example.com/upstream/docker.io/library/golang:1.23"},
		{"quay.io/acme/tool@" + testDigest, "registry.example.com/upstream/quay.io/acme/tool@" + testDigest},
		{"debian:12", "registry.example.com/upstream/docker.io/library/debian:12"},
	}
	if !slices.Equal(done, wantDone) {
		t.Errorf("the rewrites are\n%q\nwant\n%q", done, wantDone)
	}
	if same, done := testUpstream.rewriteDockerfile([]byte("FROM example.com/acme/base:1\n")); string(same) != "FROM example.com/acme/base:1\n" || done != nil {
		t.Errorf("a Dockerfile with nothing to rewrite became %q, %q", same, done)
	}
}

// TestRewriteDockerfileEscape is a Dockerfile whose escape directive sets the backtick:
// a line that ends with it goes on, and one that ends with \ does not.
func TestRewriteDockerfileEscape(t *testing.T) {
	data := "# escape=`\nFROM `\n  debian:12\nRUN dir c:\\\nFROM alpine:3\n"
	got, _ := testUpstream.rewriteDockerfile([]byte(data))
	want := "# escape=`\nFROM `\n  registry.example.com/upstream/docker.io/library/debian:12\nRUN dir c:\\\nFROM registry.example.com/upstream/docker.io/library/alpine:3\n"
	if string(got) != want {
		t.Errorf("rewrote\n%s\nwant\n%s", got, want)
	}
}

// TestDockerfileBase is the image each build starts from: the last stage's or
// build.target's, through the stages it starts from, with the ARGs before the first FROM
// and the build arguments, and the --platform of its FROM; none for scratch, a
// --platform with a variable and a variable with no value.
func TestDockerfileBase(t *testing.T) {
	const stages = "ARG V=11\nARG W\nFROM debian:${V} AS base\nFROM base AS dev\nFROM alpine:3 AS other\nFROM dev\n"
	cases := []struct {
		name, data, target string
		args               map[string]string
		want, platform     string
	}{
		{name: "last stage through two", data: stages, want: "debian:11"},
		{name: "a build argument", data: stages, args: map[string]string{"V": "12"}, want: "debian:12"},
		{name: "build.target", data: stages, target: "OTHER", want: "alpine:3"},
		{name: "one stage", data: "FROM mcr.microsoft.com/devcontainers/base:1\nRUN true\n", want: "mcr.microsoft.com/devcontainers/base:1"},
		{name: "a default", data: "FROM debian:${V:-bookworm}\n", want: "debian:bookworm"},
		{name: "a default for an empty value", data: "ARG V=\nFROM debian:${V:-bookworm}\n", want: "debian:bookworm"},
		{name: "a default for an unset value only", data: "ARG V=\nFROM debian${V-:x}\n", want: "debian"},
		{name: "an alternative", data: "ARG S=1\nFROM debian${S:+:12}\n", want: "debian:12"},
		{name: "a quoted value", data: "ARG V=\"12\"\nFROM debian:$V\n", want: "debian:12"},
		{name: "an ARG after the first FROM", data: "FROM alpine AS a\nARG V=12\nFROM debian:${V}\n"},
		{name: "a variable with no value", data: "ARG W\nFROM debian:${W}\n"},
		{name: "a variable not declared", data: "FROM debian:$V\n"},
		{name: "a default with a variable", data: "FROM debian:${V:-${W}}\n"},
		{name: "another form", data: "ARG V=12\nFROM debian:${V#1}\n"},
		{name: "scratch", data: "FROM scratch\n"},
		{name: "scratch through a stage", data: "FROM scratch AS s\nFROM s\n"},
		{name: "platform", data: "FROM --platform=linux/amd64 ubuntu:22.04\n", want: "ubuntu:22.04", platform: "linux/amd64"},
		{name: "the build's platform", data: "FROM --platform=$BUILDPLATFORM ubuntu:22.04\n", want: "ubuntu:22.04", platform: "linux/arm64"},
		{name: "the target's platform", data: "FROM --platform=${TARGETPLATFORM} ubuntu:22.04\n", want: "ubuntu:22.04", platform: "linux/arm64"},
		{name: "the target's platform set by an ARG", data: "ARG TARGETPLATFORM=linux/amd64\nFROM --platform=${TARGETPLATFORM} ubuntu:22.04\n"},
		{name: "the build's platform set by a build argument", data: "FROM --platform=$BUILDPLATFORM ubuntu:22.04\n", args: map[string]string{"BUILDPLATFORM": "linux/amd64"}},
		{name: "the build's architecture in the name", data: "FROM example.com/base-${TARGETARCH}:1\n", want: "example.com/base-arm64:1"},
		{name: "platform from an ARG", data: "ARG P=linux/amd64\nFROM --platform=$P ubuntu:22.04\n", want: "ubuntu:22.04", platform: "linux/amd64"},
		{name: "platform with a variable with no value", data: "FROM --platform=$P ubuntu:22.04\n"},
		{name: "a stage named as its image", data: "FROM golang AS golang\nRUN true\n", want: "golang"},
		{name: "a stage named as its image, then the stage", data: "FROM golang:1.23 AS golang\nFROM golang\n", want: "golang:1.23"},
		{name: "a word continued on the next line", data: "FROM ubu\\\nntu:22.04\n", want: "ubuntu:22.04"},
		{name: "no stage", data: "RUN true\n"},
		{name: "an unknown target", data: stages, target: "missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, platform, ok := dockerfileBase([]byte(c.data), c.target, c.args, "linux/arm64")
			if got != c.want || platform != c.platform || ok != (c.want != "") {
				t.Errorf("dockerfileBase = %q, %q, %v, want %q, %q", got, platform, ok, c.want, c.platform)
			}
		})
	}
}

// TestPinnedRef is a reference by digest, with a tag or none, as name@digest, and no
// other.
func TestPinnedRef(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine:3.20@" + testDigest:              "alpine@" + testDigest,
		"localhost:5000/x@" + testDigest:         "localhost:5000/x@" + testDigest,
		"alpine:3.20":                            "",
		"alpine@sha256:0123":                     "",
		"registry.example.com/env@" + testDigest: "registry.example.com/env@" + testDigest,
	} {
		got, ok := pinnedRef(ref)
		if got != want || ok != (want != "") {
			t.Errorf("pinnedRef(%q) = %q, %v, want %q", ref, got, ok, want)
		}
	}
}

// TestRewriteDockerfileWordsThatStartNoHereDocument are a here-string and a shift in
// RUN, which start no here-document: the FROM after them is rewritten.
func TestRewriteDockerfileWordsThatStartNoHereDocument(t *testing.T) {
	data := "FROM alpine:3 AS a\nRUN cat <<<\"hello\"\nRUN echo $((1<<4))\nFROM debian:12\n"
	got, _ := testUpstream.rewriteDockerfile([]byte(data))
	want := "FROM registry.example.com/upstream/docker.io/library/alpine:3 AS a\nRUN cat <<<\"hello\"\nRUN echo $((1<<4))\nFROM registry.example.com/upstream/docker.io/library/debian:12\n"
	if string(got) != want {
		t.Errorf("rewrote\n%s\nwant\n%s", got, want)
	}
	data = "RUN 3<<EOF cat <&3\nFROM inside:1\nEOF\nFROM debian:12\n"
	got, _ = testUpstream.rewriteDockerfile([]byte(data))
	if want := "RUN 3<<EOF cat <&3\nFROM inside:1\nEOF\nFROM registry.example.com/upstream/docker.io/library/debian:12\n"; string(got) != want {
		t.Errorf("rewrote\n%s\nwant\n%s", got, want)
	}
}

// TestRewriteDockerfileStageNamedAsItsImage is a stage whose name is its image's: its
// FROM is rewritten, and a later FROM of the stage is not.
func TestRewriteDockerfileStageNamedAsItsImage(t *testing.T) {
	got, _ := testUpstream.rewriteDockerfile([]byte("FROM golang AS golang\nFROM golang\nCOPY --from=golang /a /a\n"))
	if want := "FROM registry.example.com/upstream/docker.io/library/golang AS golang\nFROM golang\nCOPY --from=golang /a /a\n"; string(got) != want {
		t.Errorf("rewrote\n%s\nwant\n%s", got, want)
	}
}

// TestRewriteDockerfileWordOnTwoLines is a name continued on the next line, which
// BuildKit joins: the whole name is rewritten, on one line.
func TestRewriteDockerfileWordOnTwoLines(t *testing.T) {
	got, done := testUpstream.rewriteDockerfile([]byte("FROM ubu\\\nntu:22.04\n"))
	if want := "FROM registry.example.com/upstream/docker.io/library/ubuntu:22.04\n"; string(got) != want || len(done) != 1 || done[0][0] != "ubuntu:22.04" {
		t.Errorf("rewrote %q, %q", got, done)
	}
}
