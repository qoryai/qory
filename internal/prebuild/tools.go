package prebuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/integration"
)

// Where the process of the build finds what the tools step adds to the image: the
// folder of the Node parsers and the node program, as the prebuild image has them. The
// prebuild image is built FROM the agent image, which installs both.
const (
	// ParsersPath is the folder of the Node parsers, with package.json and node_modules.
	// The image gets it at the same path.
	ParsersPath = "/opt/qory/parsers"
	// NodePath is the node program the step adds to an image that has none.
	NodePath = "/usr/local/bin/node"
)

// The words of the tools step.
const (
	textTools       = prefix + "added Chromium, ffmpeg and the parsers in %s"
	textNoParsers   = "build the prebuild image FROM Qory's agent image: it has no parsers in %s"
	textNoNode      = "build the prebuild image FROM Qory's agent image: it has no node program at %s, and the base image has none"
	textNotDebian   = "use a Debian base image: the build adds Chromium with apt, and this image's ID is %s"
	textNoApt       = "use a Debian base image that has apt-get: the build adds Chromium and ffmpeg with it"
	textBaseUnread  = "use a base image with sh and /etc/os-release: reading its system failed: %s"
	textToolsFailed = "read the build log for the step that failed: adding Chromium, ffmpeg and the parsers: %s"
)

// toolsPackages are the Debian packages the tools step installs: Chromium, ffmpeg with
// ffprobe, and the Noto fonts of the agent image for Latin and other scripts, CJK and
// color emoji.
var toolsPackages = []string{"chromium", "ffmpeg", "fonts-noto-cjk", "fonts-noto-color-emoji", "fonts-noto-core"}

// baseFacts is what the build reads of the image the target step starts from.
type baseFacts struct {
	// id is ID of its /etc/os-release, "" for none.
	id string
	// apt is whether it has apt-get on its PATH, and node whether it has node.
	apt, node bool
}

// probeScript prints the facts [readBase] reads, one name=value per line, as one line of
// shell.
const probeScript = `if [ -r /etc/os-release ]; then . /etc/os-release; fi; echo "id=${ID:-}"; ` +
	`if command -v apt-get >/dev/null 2>&1; then echo apt=yes; else echo apt=no; fi; ` +
	`if command -v node >/dev/null 2>&1; then echo node=yes; else echo node=no; fi`

// readBase reads the facts of image ref with a container that has no network and no
// capability, and is removed when it ends.
func (b *build) readBase(ctx context.Context, ref string) (baseFacts, error) {
	var f baseFacts
	b.probed = true
	out, err := b.e.output(ctx, "docker", "run", "--rm", "--name", b.probeName(), "--pull", "never",
		"--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--entrypoint", "sh", ref, "-c", probeScript)
	if err != nil {
		var fl *failed
		if errors.As(err, &fl) {
			return f, refused(fmt.Errorf(textBaseUnread, fl.last))
		}
		return f, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch name {
		case "id":
			f.id = value
		case "apt":
			f.apt = value == "yes"
		case "node":
			f.node = value == "yes"
		}
	}
	return f, nil
}

// probeName is the name of the container [build.readBase] starts.
func (b *build) probeName() string { return b.name + "-probe" }

// shownID is an ID of an os-release file as the refusal prints it: at most 32 printable
// characters in quotes, "empty" when it has none.
func shownID(id string) string {
	if id == "" {
		return "empty"
	}
	if len(id) > 32 {
		id = id[:32]
	}
	return `"` + integration.Printable(id) + `"`
}

// addTools builds tools from base, an image of Debian with apt, as the root user: it
// adds Chromium, ffmpeg and ffprobe with apt, the Node parsers from [Options.Parsers],
// and node from [Options.Node] when the image has none. It then runs, as the wall user,
// a check that each program starts, each parser loads at the version of package.json,
// and the wall user cannot write the parsers. It leaves the wall user as the image's
// user. A base that is not Debian, or has no apt-get, is refused before it builds.
func (b *build) addTools(ctx context.Context, base, tools string) error {
	start := time.Now()
	facts, err := b.readBase(ctx, base)
	if err != nil {
		return err
	}
	switch {
	case facts.id != "debian":
		return refused(fmt.Errorf(textNotDebian, shownID(facts.id)))
	case !facts.apt:
		return refused(errors.New(textNoApt))
	}
	node := b.nodePath()
	if !facts.node {
		if fi, err := os.Stat(node); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return refused(fmt.Errorf(textNoNode, integration.Printable(node)))
		}
	}
	dir := filepath.Join(b.tmp, "tools")
	if err := writeToolsContext(dir, base, b.u, b.parsersPath(), node, !facts.node); err != nil {
		return err
	}
	b.images = append(b.images, tools)
	// The environment of the step that builds the base: its docker configuration has the
	// proxies of a build behind a wall, which apt reaches the base's sources through.
	err = engine{env: b.buildEnv, log: b.e.log}.run(ctx, nil, io.Discard, "docker", "build",
		"--tag", tools, "--file", filepath.Join(dir, "Dockerfile"), dir)
	var f *failed
	if errors.As(err, &f) {
		return fmt.Errorf(textToolsFailed, f.last)
	}
	if err != nil {
		return err
	}
	b.line(textTools, seconds(time.Since(start)))
	return nil
}

// writeToolsContext writes the build context of the tools step to the new folder dir:
// the Dockerfile, a copy of the folder parsers and, when copyNode is set, a copy of the
// node program node.
func writeToolsContext(dir, base string, u user, parsers, node string, copyNode bool) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := copyToolsTree(parsers, filepath.Join(dir, "parsers")); err != nil {
		return err
	}
	if copyNode {
		if err := copyToolsFile(node, filepath.Join(dir, "node"), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(toolsDockerfile(base, u, copyNode)), 0o600)
}

// parsersPath is the folder of the parsers the build copies.
func (b *build) parsersPath() string {
	if b.o.Parsers != "" {
		return b.o.Parsers
	}
	return ParsersPath
}

// nodePath is the node program the build copies into an image that has none.
func (b *build) nodePath() string {
	if b.o.Node != "" {
		return b.o.Node
	}
	return NodePath
}

// checkParsers refuses a build whose folder of parsers is not one: it needs package.json
// and node_modules, as the agent image installs them.
func (b *build) checkParsers() error {
	p := b.parsersPath()
	for _, name := range []string{"package.json", "node_modules"} {
		if _, err := os.Stat(filepath.Join(p, name)); err != nil {
			return refused(fmt.Errorf(textNoParsers, integration.Printable(p)))
		}
	}
	return nil
}

// toolsDockerfile is the Dockerfile of the tools step, which builds from the context
// [build.addTools] writes: Dockerfile, parsers and, when copyNode is set, node. Debian's
// apt installs [toolsPackages] from the sources the base has, so their versions are the
// ones its suite serves on the day of the build; the last step prints them. The folder
// of the parsers belongs to root, mode 0755 for folders and 0644 for files, as the context
// has them, so the wall user reads it and cannot change it.
func toolsDockerfile(base string, u user, copyNode bool) string {
	var d strings.Builder
	d.WriteString("FROM " + base + "\nUSER 0:0\n")
	if copyNode {
		d.WriteString("COPY --chown=0:0 --chmod=0755 node /usr/local/bin/node\n")
	}
	d.WriteString("COPY --chown=0:0 parsers " + ParsersPath + "\n")
	d.WriteString(`RUN set -eu \
 && export DEBIAN_FRONTEND=noninteractive \
 && apt-get update \
 && apt-get install -y --no-install-recommends ` + strings.Join(toolsPackages, " ") + ` \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /var/cache/apt \
 && find /usr/lib/chromium -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +
`)
	d.WriteString("USER " + u.String() + "\n")
	d.WriteString(toolsCheck)
	return d.String()
}

// toolsCheck runs as the wall user. Each failure prints its fix first.
const toolsCheck = `RUN set -eu \
 && { test "$(command -v chromium)" = /usr/bin/chromium \
      || { echo "Install chromium at /usr/bin/chromium: command -v chromium prints '$(command -v chromium)'" >&2; exit 1; }; } \
 && echo "chromium: $(chromium --version)" \
 && echo "ffmpeg: $(ffmpeg -version | head -n 1)" \
 && echo "ffprobe: $(ffprobe -version | head -n 1)" \
 && { node --version >/dev/null \
      || { echo "Use a base image whose node runs as the wall user, 20.19 or later: node --version fails" >&2; exit 1; }; } \
 && echo "node: $(node --version)" \
 && for pkg in $(node -p "Object.keys(require('` + ParsersPath + `/package.json').dependencies).join(' ')"); do \
      dir="` + ParsersPath + `/node_modules/$pkg"; \
      node -e "require('$dir')" \
        || { echo "Use a base image with node 20.19 or later: require of $pkg from ` + ParsersPath + ` fails" >&2; exit 1; }; \
      want="$(node -p "require('` + ParsersPath + `/package.json').dependencies['$pkg']")"; \
      have="$(node -p "require('$dir/package.json').version")"; \
      [ "$have" = "$want" ] \
        || { echo "Run npm ci in parsers/: $pkg is $have, not $want" >&2; exit 1; }; \
      echo "$pkg: $have"; \
    done \
 && bad="$(find ` + ParsersPath + ` ! -type l \( -perm /022 -o ! -user root \) -print -quit)" \
 && { test -z "$bad" || { echo "Set root and mode 0755 or 0644 in ` + ParsersPath + `: $bad is not" >&2; exit 1; }; } \
 && { ! sh -c ': >> ` + ParsersPath + `/package.json' 2>/dev/null \
      || { echo "Make ` + ParsersPath + ` read-only for every user but root: the wall user changed a file there" >&2; exit 1; }; }
`

// copyToolsTree copies the folder src to the new folder dst: folders, regular files and links
// as they are, every file with mode 0644 and every folder 0755, whatever src has. It
// stops at any other kind of file.
func copyToolsTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if err := os.Mkdir(to, 0o755); err != nil {
				return err
			}
			return os.Chmod(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case d.Type().IsRegular():
			return copyToolsFile(p, to, 0o644)
		}
		return fmt.Errorf("copy %s: it is not a file, a folder or a link", integration.Printable(rel))
	})
}

// copyToolsFile writes the new file dst with src's content and the mode perm, whatever the
// umask.
func copyToolsFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}
