package prebuild

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/devcontainer"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// DockerfileMaxSize is the largest Dockerfile the build reads for its syntax directive:
// 1 MiB.
const DockerfileMaxSize = 1 << 20

// The words of a refused Dockerfile frontend. Each is one format string.
const (
	// textSyntax is a Dockerfile whose syntax directive names another frontend: the
	// configuration's path and the Dockerfile's.
	textSyntax = "%s builds from %s, whose syntax directive names a Dockerfile frontend other than docker/dockerfile; the image is built only with Docker's Dockerfile frontend"
	// textSyntaxArg is a build argument that names another frontend: the
	// configuration's path and the argument's name.
	textSyntaxArg = "%s sets build.args.%s to a Dockerfile frontend other than docker/dockerfile; the image is built only with Docker's Dockerfile frontend"
	// textDockerfileUnread is a Dockerfile the build cannot read: the configuration's
	// path, the Dockerfile's and why.
	textDockerfileUnread = "%s sets build.dockerfile to %s, which cannot be read: %s"
	// textDockerfileBig is why a Dockerfile larger than [DockerfileMaxSize] is not read.
	textDockerfileBig = "it is larger than 1 MiB"
	// textDockerfileOdd is why a Dockerfile that is no regular file is not read.
	textDockerfileOdd = "it is not a regular file"
)

// syntaxArg is the build argument that names the frontend in place of the Dockerfile's
// syntax directive (buildx build/opt.go; buildkit frontend/dockerfile/builder/build.go,
// keySyntaxArg).
const syntaxArg = "BUILDKIT_SYNTAX"

var (
	// directiveLine is a parser directive as BuildKit reads one, once the comment and
	// the spaces before it are cut (buildkit v0.34.0
	// frontend/dockerfile/parser/directives.go, directiveRegexp).
	directiveLine = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)
	// cliDirective is a parser directive as the devcontainer command reads one from a
	// line of the Dockerfile before its first FROM (spec-node/dockerfileUtils.ts,
	// directives): JavaScript's \s, \S and ., written out.
	cliDirective = regexp.MustCompile(`^` + jsSpace + `*#` + jsSpace + `*([^` + jsSpaces + `]+)` + jsSpace + `*=` + jsSpace + `*([^\n\r\x{2028}\x{2029}]+)`)
	// officialFrontend is Docker's Dockerfile frontend, docker/dockerfile, at a tag, a
	// digest, both or neither, and with docker.io before it or not.
	officialFrontend = regexp.MustCompile(`^(?:docker\.io/)?docker/dockerfile(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[0-9a-f]{64})?$`)
	// cliLines is where the devcontainer command splits the Dockerfile into lines.
	cliLines = regexp.MustCompile(`\r?\n`)
)

// jsSpaces are the characters JavaScript's \s matches, and jsSpace is the class of them.
const (
	jsSpaces = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	jsSpace  = `[` + jsSpaces + `]`
)

// checkFrontend refuses a build that names a Dockerfile frontend other than Docker's,
// docker/dockerfile: BuildKit runs whatever image a syntax directive or the
// BUILDKIT_SYNTAX build argument names as the frontend, with the build's context and
// secrets. dockerfile is the Dockerfile's path relative to the repository at repo, with
// slashes, as [confine] resolved it; "" when the configuration builds from no
// Dockerfile.
//
// The Dockerfile is read through an [os.Root] of the repository, so a link that leaves
// it is not followed, and checked as BuildKit reads it and as the devcontainer command
// reads it, since the command writes the syntax it read into the Dockerfile it builds
// when the configuration has features. Each build argument is checked as buildx reads
// --build-arg name=value, which the command passes, so a name holding = is cut there,
// and named in any case.
func checkFrontend(repo string, c *devcontainer.Config, dockerfile string) error {
	if c.Build == nil {
		return nil
	}
	for name, value := range c.Build.Args {
		key, v, _ := strings.Cut(name+"="+value, "=")
		if !strings.EqualFold(key, syntaxArg) {
			continue
		}
		ref, _, _ := strings.Cut(strings.TrimSpace(v), " ")
		if !officialFrontend.MatchString(ref) {
			return refused(fmt.Errorf(textSyntaxArg, c.Path, integration.Printable(accesskey.ShownName(name, false))))
		}
	}
	if dockerfile == "" {
		return nil
	}
	data, err := readDockerfile(repo, dockerfile)
	if err != nil {
		return refused(fmt.Errorf(textDockerfileUnread, c.Path, integration.Printable(exports.Value(dockerfile, false)), integration.Printable(exports.HideError(err).Error())))
	}
	for _, ref := range syntaxes(data) {
		if !officialFrontend.MatchString(ref) {
			return refused(fmt.Errorf(textSyntax, c.Path, integration.Printable(exports.Value(dockerfile, false))))
		}
	}
	return nil
}

// readDockerfile is the Dockerfile at rel in repo, read through an [os.Root] of repo: a
// regular file of at most [DockerfileMaxSize] bytes.
func readDockerfile(repo, rel string) ([]byte, error) {
	r, err := os.OpenRoot(repo)
	if err != nil {
		return nil, errors.New(reason(err))
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, errors.New(reason(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, errors.New(reason(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New(textDockerfileOdd)
	}
	data, err := io.ReadAll(io.LimitReader(f, DockerfileMaxSize+1))
	if err != nil {
		return nil, errors.New(reason(err))
	}
	if len(data) > DockerfileMaxSize {
		return nil, errors.New(textDockerfileBig)
	}
	return data, nil
}

// syntaxes are the frontends data, a Dockerfile, names, each as BuildKit takes it from
// a directive's value, its first word.
//
// BuildKit (buildkit v0.34.0 frontend/dockerfile/parser/directives.go, DetectSyntax)
// drops a byte order mark and a first line that starts with #!, then reads the leading
// lines that are each a comment of # and a directive: a name, any case, =, and a value
// (see [directives]). Failing a syntax there, it reads them with // in
// place of #, and failing that, the whole text as a JSON object's "syntax" member.
// The devcontainer command reads the lines before the first FROM with another pattern
// (spec-node/dockerfileUtils.ts, extractDirectives), and writes "# syntax=" and the
// value it read first in the Dockerfile it builds with features. Every syntax each
// reading finds is returned, so a Dockerfile two of them read differently is checked
// by both.
func syntaxes(data []byte) []string {
	var out []string
	body := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if first, rest, _ := bytes.Cut(body, []byte("\n")); bytes.HasPrefix(first, []byte("#!")) {
		body = rest
	}
	for _, comment := range []string{"#", "//"} {
		out = append(out, directives(body, comment)...)
	}
	var whole map[string]any
	if json.Unmarshal(body, &whole) == nil {
		if v, ok := whole["syntax"].(string); ok {
			out = append(out, firstWord(v))
		}
	}
	if v, ok := cliSyntax(string(data)); ok {
		// The command writes "# syntax=" and v, and BuildKit reads that line.
		if m := directiveLine.FindStringSubmatch("syntax=" + v); m != nil {
			out = append(out, firstWord(m[2]))
		} else {
			out = append(out, v)
		}
	}
	return out
}

// directives are the syntax directives' first words in the leading lines of body that
// are each comment and a directive: a name, =, and a value. BuildKit v0.34.0 stops at a
// name other than syntax, escape and check, and at one named twice; an earlier BuildKit
// read on past any name and took the last syntax. This reads on past both, and takes
// spaces before the comment, so every syntax any of them reads there is checked.
func directives(body []byte, comment string) []string {
	var out []string
	for line := range bytes.Lines(body) {
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		rest, ok := bytes.CutPrefix(bytes.TrimLeftFunc(line, unicode.IsSpace), []byte(comment))
		if !ok {
			break
		}
		m := directiveLine.FindSubmatch(bytes.TrimLeftFunc(rest, unicode.IsSpace))
		if m == nil {
			break
		}
		if strings.EqualFold(string(m[1]), "syntax") {
			out = append(out, firstWord(string(m[2])))
		}
	}
	return out
}

// cliSyntax is the syntax directive's value the devcontainer command reads from text: in
// the lines before the first FROM, split at \n or \r\n, those that each match its
// directive pattern, the first named syntax, in that case.
func cliSyntax(text string) (string, bool) {
	for _, line := range cliLines.Split(text, -1) {
		m := cliDirective.FindStringSubmatch(line)
		if m == nil {
			return "", false
		}
		if m[1] == "syntax" {
			return m[2], true
		}
	}
	return "", false
}

// firstWord is v up to its first space, as BuildKit takes a frontend's reference.
func firstWord(v string) string {
	w, _, _ := strings.Cut(v, " ")
	return w
}
