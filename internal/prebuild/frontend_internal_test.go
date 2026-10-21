package prebuild

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

const (
	bom        = "\xEF\xBB\xBF"
	evil       = "example.com/evil"
	digest     = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	configAt   = ".devcontainer/devcontainer.json"
	fileAt     = ".devcontainer/Dockerfile"
	wantSyntax = configAt + " builds from " + fileAt + ", whose syntax directive names a Dockerfile frontend other than docker/dockerfile; the image is built only with Docker's Dockerfile frontend"
	fromLine   = "FROM alpine:3.20\n"
)

// frontendRepo is a repository whose Dockerfile at fileAt is dockerfile, and the
// configuration that builds from it with args.
func frontendRepo(t *testing.T, dockerfile string, args map[string]string) (string, *devcontainer.Config) {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, fileAt), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, &devcontainer.Config{Path: configAt, Build: &devcontainer.Build{Dockerfile: "Dockerfile", Args: args}}
}

// TestCheckFrontendTakesDockersFrontend is Docker's Dockerfile frontend at a tag, a
// digest, both or neither, with docker.io before it or not, in each form of the
// directive, and a Dockerfile that names none: each built.
func TestCheckFrontendTakesDockersFrontend(t *testing.T) {
	for name, dockerfile := range map[string]string{
		"no directive":              fromLine,
		"empty":                     "",
		"a tag":                     "# syntax=docker/dockerfile:1\n" + fromLine,
		"the command's default":     "# syntax=docker/dockerfile:1.4\n" + fromLine,
		"spaced":                    "#  syntax = docker/dockerfile:1.7-labs \n" + fromLine,
		"a digest":                  "# syntax=docker/dockerfile" + digest + "\n" + fromLine,
		"a tag and a digest":        "# syntax=docker/dockerfile:1" + digest + "\n" + fromLine,
		"no tag":                    "# syntax=docker/dockerfile\n" + fromLine,
		"docker.io":                 "# syntax=docker.io/docker/dockerfile:1\n" + fromLine,
		"uppercase key":             "# SYNTAX=docker/dockerfile:1\n" + fromLine,
		"after escape and check":    "# escape=`\n# check=skip=all\n# syntax=docker/dockerfile:1\n" + fromLine,
		"after a BOM":               bom + "# syntax=docker/dockerfile:1\n" + fromLine,
		"after a shebang":           "#!/usr/bin/env buildctl\n# syntax=docker/dockerfile:1\n" + fromLine,
		"slashes":                   "// syntax=docker/dockerfile:1\n" + fromLine,
		"JSON":                      `{"syntax": "docker/dockerfile:1"}`,
		"words after it":            "# syntax=docker/dockerfile:1 --opt=x\n" + fromLine,
		"CRLF":                      "# syntax=docker/dockerfile:1\r\n" + fromLine,
		"a comment after the block": "# a comment\n# syntax=" + evil + "\n" + fromLine,
		"after FROM":                fromLine + "# syntax=" + evil + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			repo, c := frontendRepo(t, dockerfile, nil)
			if err := checkFrontend(repo, c, fileAt); err != nil {
				t.Errorf("checkFrontend: %v", err)
			}
		})
	}
	t.Run("build.args", func(t *testing.T) {
		repo, c := frontendRepo(t, fromLine, map[string]string{"BUILDKIT_SYNTAX": "docker/dockerfile:1", "OTHER": evil})
		if err := checkFrontend(repo, c, fileAt); err != nil {
			t.Errorf("checkFrontend: %v", err)
		}
	})
}

// TestCheckFrontendRefusesAnotherFrontend is another frontend in each form of the
// directive, after a byte order mark or a shebang, with any case, as BuildKit or the
// devcontainer command reads it, and a second syntax directive, which neither takes:
// each refused, the Dockerfile named and the value never quoted.
func TestCheckFrontendRefusesAnotherFrontend(t *testing.T) {
	for name, dockerfile := range map[string]string{
		"hash":                           "# syntax=" + evil + "\n" + fromLine,
		"hash, no space":                 "#syntax=" + evil + "\n" + fromLine,
		"slashes":                        "// syntax=" + evil + "\n" + fromLine,
		"JSON":                           `{"syntax": "` + evil + `"}`,
		"after a BOM":                    bom + "# syntax=" + evil + "\n" + fromLine,
		"after a shebang":                "#!/bin/sh\n# syntax=" + evil + "\n" + fromLine,
		"BOM and shebang":                bom + "#!/bin/sh\n// syntax=" + evil + "\n" + fromLine,
		"uppercase key":                  "# SYNTAX=" + evil + "\n" + fromLine,
		"mixed case key":                 "# Syntax=" + evil + "\n" + fromLine,
		"after escape":                   "# escape=\\\n# syntax=" + evil + "\n" + fromLine,
		"named twice":                    "# syntax=docker/dockerfile:1\n# syntax=" + evil + "\n" + fromLine,
		"after an unknown name":          "# foo=1\n# Syntax=" + evil + "\n" + fromLine,
		"slashes, after an unknown name": "// foo=1\n// syntax=docker/dockerfile:1\n// syntax=" + evil + "\n" + fromLine,
		"after a space":                  " # syntax=" + evil + "\n" + fromLine,
		"escape twice":                   "# escape=`\n# escape=`\n# syntax=" + evil + "\n" + fromLine,
		"upstream":                       "# syntax=docker/dockerfile-upstream:master\n" + fromLine,
		"docker.io upstream":             "# syntax=docker.io/docker/dockerfile-upstream:1\n" + fromLine,
		"index.docker.io":                "# syntax=index.docker.io/docker/dockerfile:1\n" + fromLine,
		"another registry":               "# syntax=example.com/docker/dockerfile:1\n" + fromLine,
		"a longer name":                  "# syntax=docker/dockerfile2:1\n" + fromLine,
		"uppercase name":                 "# syntax=Docker/Dockerfile:1\n" + fromLine,
		"a short digest":                 "# syntax=docker/dockerfile@sha256:0123\n" + fromLine,
		"a tab in the value":             "# syntax=docker/dockerfile:1\t" + evil + "\n" + fromLine,
		"empty value":                    "# syntax= \n" + fromLine,
		"a CR in the value":              "# syntax=docker/dockerfile:1\r" + evil + "\n" + fromLine,
		"the command's reading":          "\t# syntax=" + evil + "\n" + fromLine,
	} {
		t.Run(name, func(t *testing.T) {
			repo, c := frontendRepo(t, dockerfile, nil)
			err := checkFrontend(repo, c, fileAt)
			var refusal *RefusedError
			if !errors.As(err, &refusal) || err.Error() != wantSyntax {
				t.Errorf("checkFrontend: %v", err)
			}
		})
	}
}

// TestCheckFrontendRefusesASyntaxArgument is BUILDKIT_SYNTAX in build.args naming
// another frontend, in any case and with a name holding the =, as buildx cuts
// --build-arg: each refused, the argument named.
func TestCheckFrontendRefusesASyntaxArgument(t *testing.T) {
	for name, c := range map[string]struct {
		args map[string]string
		want string
	}{
		"another frontend":  {map[string]string{"BUILDKIT_SYNTAX": evil}, "BUILDKIT_SYNTAX"},
		"words after it":    {map[string]string{"BUILDKIT_SYNTAX": " " + evil + " docker/dockerfile:1"}, "BUILDKIT_SYNTAX"},
		"lowercase":         {map[string]string{"buildkit_syntax": evil}, "buildkit_syntax"},
		"the = in the name": {map[string]string{"BUILDKIT_SYNTAX=" + evil + " ": "x"}, "BUILDKIT_SYNTAX=" + evil + " "},
		"upstream":          {map[string]string{"BUILDKIT_SYNTAX": "docker/dockerfile-upstream:master"}, "BUILDKIT_SYNTAX"},
		"empty":             {map[string]string{"BUILDKIT_SYNTAX": " "}, "BUILDKIT_SYNTAX"},
	} {
		t.Run(name, func(t *testing.T) {
			repo, cfg := frontendRepo(t, "# syntax=docker/dockerfile:1\n"+fromLine, c.args)
			err := checkFrontend(repo, cfg, fileAt)
			want := configAt + " sets build.args." + c.want + " to a Dockerfile frontend other than docker/dockerfile; the image is built only with Docker's Dockerfile frontend"
			var refusal *RefusedError
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Errorf("checkFrontend: %v", err)
			}
		})
	}
}

// TestCheckFrontendRefusesADockerfileItCannotRead is a Dockerfile not there, a folder,
// one larger than 1 MiB and a link that leaves the repository: each refused.
func TestCheckFrontendRefusesADockerfileItCannotRead(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(t *testing.T, repo string)
		want  string
	}{
		"not there": {func(t *testing.T, repo string) { remove(t, repo) }, "no such file or directory"},
		"a folder": {func(t *testing.T, repo string) {
			remove(t, repo)
			if err := os.Mkdir(filepath.Join(repo, fileAt), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "it is not a regular file"},
		"too large": {func(t *testing.T, repo string) {
			if err := os.WriteFile(filepath.Join(repo, fileAt), []byte(strings.Repeat("#\n", DockerfileMaxSize)), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "it is larger than 1 MiB"},
		"a link out": {func(t *testing.T, repo string) {
			remove(t, repo)
			out := filepath.Join(t.TempDir(), "Dockerfile")
			if err := os.WriteFile(out, []byte("# syntax="+evil+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(out, filepath.Join(repo, fileAt)); err != nil {
				t.Fatal(err)
			}
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			repo, cfg := frontendRepo(t, fromLine, nil)
			c.setup(t, repo)
			err := checkFrontend(repo, cfg, fileAt)
			prefix := configAt + " sets build.dockerfile to " + fileAt + ", which cannot be read: "
			var refusal *RefusedError
			if !errors.As(err, &refusal) || !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), c.want) {
				t.Errorf("checkFrontend: %v", err)
			}
		})
	}
}

// remove removes the Dockerfile of a frontendRepo.
func remove(t *testing.T, repo string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repo, fileAt)); err != nil {
		t.Fatal(err)
	}
}
