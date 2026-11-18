// Package runtimeinstall installs an agent runtime's program for qory run, before the
// run's first session, from the publisher's npm tarball: from the runtime cache of the
// instance's registry, [RegistryFetch], or from npm, [NpmFetch].
//
// [Install] asks its [Fetch] for the runtime's newest release: the version, npm's
// sha512 checksum of the tarball, and a way to open the tarball. It takes the run's hold
// of the version, which keeps the version's folder until the run ends, and removes the
// older versions' folders that an install made, and the temporary entries, that no run
// holds. It uses the folder <Folder>/<runtime>/<version> when an earlier install of a
// tarball with the same checksum made it, and opens no tarball then. Otherwise it takes
// the version's install lock, so that a second run that needs the same version waits and
// then uses the folder, reads the tarball into a temporary file, checks its checksum, and
// extracts into a temporary folder either the program's entry, [Release.Program], to
// bin/<runtime>, or the release's folder, [Release.Folder], whole. It then renames that
// folder to the version's folder: a folder at that path is always a complete install.
package runtimeinstall

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The limits of what Install reads and writes; a test lowers them.
var (
	// maxTarball is the most bytes of a runtime's npm tarball: 1 GiB.
	maxTarball int64 = 1 << 30
	// maxProgram is the most bytes of the program extracted from the tarball: 1 GiB.
	maxProgram int64 = 1 << 30
	// maxUnpacked is the most bytes the tarball may unpack to, all entries counted: 4 GiB.
	maxUnpacked int64 = 4 << 30
)

// checksumFile is the file in a version's folder that contains npm's checksum of the
// tarball the folder was extracted from.
const checksumFile = "integrity"

var (
	// versionShape is a version Install takes: 1 to 64 letters, digits, ., + or -, the
	// first a letter or a digit, so the version is one folder name and never . or ..;
	// Forager takes 1 to 64 of the same characters.
	versionShape = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	// runtimeShape is a runtime's name, as Forager's descriptor schema has it.
	runtimeShape = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	// digestShape is a sha256 digest.
	digestShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// checkError is an error of a release's tarball reader whose text starts with its fix,
// such as bytes that do not match the layer's digest: Install returns it as it is.
type checkError struct{ err error }

func (e *checkError) Error() string { return e.err.Error() }

func (e *checkError) Unwrap() error { return e.err }

// Release is a runtime's newest release, as a [Fetch] returns it.
type Release struct {
	// Version is the publisher's version, such as 2.1.320.
	Version string
	// Integrity is npm's checksum of the tarball: sha512- and the 64 bytes of its sha512
	// digest in base64.
	Integrity string
	// Open opens the tarball, npm's gzip tar archive of the package for the machine's
	// platform, whose program is the entry Program. Install calls it at most once, and
	// only when the version's folder is not complete.
	Open func(ctx context.Context) (io.ReadCloser, error)
	// Program is the entry of the program in the tarball, see [layout]; "" stands for
	// package/<runtime>. Install extracts only this entry, to bin/<runtime>.
	Program string
	// Signed is the package and version that npm signs, <package>@<version>, such as
	// @openai/codex@0.170.0-linux-x64, and Signatures are npm's signatures of
	// <Signed>:<Integrity>. Install checks them before it uses the release.
	Signed     string
	Signatures []Signature
	// Folder, when it is set, is a folder of the tarball, ending in /, see [layout].
	// Install extracts it whole, in its layout, as the version's folder, in place of
	// Program: the folder's bin/<runtime> is the program.
	Folder string
}

// folderFiles are the files beside bin/<runtime> that a runtime's folder, see [layout],
// must contain, by their paths in the folder: the program runs them, so an install
// without one would fail later, in the session.
var folderFiles = map[string][]string{
	"codex": {"codex-resources/bwrap", "codex-path/rg", "bin/codex-code-mode-host"},
}

// layout is where runtime's program is in the npm tarball of its build for linux/arch,
// as a [Release] has it. Codex's build is the folder package/vendor/<x86_64 or
// aarch64>-unknown-linux-musl/: its codex-package.json sets bin/codex as the entry point
// and codex-resources and codex-path as the folders of its helpers, such as
// codex-resources/bwrap and codex-path/rg, and bin/codex-code-mode-host is beside
// bin/codex. Install extracts that folder whole. Any other runtime's program is the one
// entry package/<runtime>, such as package/claude for Claude Code.
func layout(runtime, arch string) (program, folder string) {
	if runtime == "codex" {
		triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
		return "", "package/vendor/" + triple + "-unknown-linux-musl/"
	}
	return "package/" + runtime, ""
}

// Fetch returns the newest release of the runtime. Every error's text starts with what
// to fix: Install returns it as it is.
type Fetch func(ctx context.Context, runtime string) (Release, error)

// Options are what [Install] installs, and where from.
type Options struct {
	// Fetch returns the runtime's newest release.
	Fetch Fetch
	// Runtime is the runtime's name, Forager's descriptor name, such as claude.
	Runtime string
	// Folder is where the versions are kept: <Folder>/<Runtime>/<version>.
	Folder string
	// Mounted, when set, returns the host folders that running containers mount. The
	// cleanup keeps a version's folder that one of them is, contains or lies in: a run
	// that ended while its container went on has no hold, but its container still runs
	// the program. When Mounted returns an error, the cleanup removes no version's folder.
	Mounted func(ctx context.Context) ([]string, error)
	// Progress, when set, gets a line, with no prefix, when a registry fetch waits for
	// the registry: at the first retry, and every five minutes after that.
	Progress func(line string)
	// Keys are the keys that npm signs with; nil stands for [NpmKeys], the keys built
	// into qory.
	Keys []SigningKey
}

// Installed is a runtime that [Install] installed, or found installed.
type Installed struct {
	// Runtime is the runtime's name, Version the publisher's version.
	Runtime, Version string
	// Dir is the version's folder, which contains bin/<Runtime>.
	Dir string
	// Downloaded is whether Install read the tarball, false for a folder an earlier
	// install made.
	Downloaded bool
	// hold is the open hold file of the version, see [Installed.Release].
	hold *os.File
}

// Release ends the run's hold of the version's folder: once no run holds it, an install
// of a newer version removes it. A run calls it when it ends; the hold also ends when
// the process exits, and when the Installed is garbage collected, so a run keeps it
// until it ends. It is safe to call more than once.
func (i *Installed) Release() {
	if i.hold != nil {
		i.hold.Close()
		i.hold = nil
	}
}

// Program is the path of the runtime's program: <Dir>/bin/<Runtime>.
func (i Installed) Program() string { return filepath.Join(i.Dir, "bin", i.Runtime) }

// textRelease is the first line of each refusal of what the fetch returned: the fix. The
// cause follows on the second line.
const textRelease = "Check the source of the runtime's releases\n"

// Install installs the runtime o names, as the package's documentation says, and
// returns it. It takes as long as ctx allows. Every error's text starts with what to
// fix.
func Install(ctx context.Context, o Options) (Installed, error) {
	if !runtimeShape.MatchString(o.Runtime) {
		return Installed{}, fmt.Errorf("Name the runtime with a lowercase letter, then lowercase letters, digits or -\n"+
			"%q is not a runtime's name, which has 1 to 64 characters.", o.Runtime)
	}
	if !filepath.IsAbs(o.Folder) {
		return Installed{}, fmt.Errorf("Keep runtimes in an absolute folder\nThe folder for runtimes is not an absolute path:\n  %q", o.Folder)
	}
	if o.Progress != nil {
		ctx = context.WithValue(ctx, progressKey{}, o.Progress)
	}
	rel, err := o.Fetch(ctx, o.Runtime)
	if err != nil {
		return Installed{}, err
	}
	if !versionShape.MatchString(rel.Version) {
		return Installed{}, fmt.Errorf(textRelease+"The newest %s has the version %q, and a version is 1 to 64 letters, digits, ., + or -, the first a letter or a digit", o.Runtime, rel.Version)
	}
	sum, err := sha512Of(rel.Integrity)
	if err != nil {
		return Installed{}, fmt.Errorf(textRelease+"The checksum of %s %s is not sha512- and 64 bytes in base64", o.Runtime, rel.Version)
	}
	keys := o.Keys
	if keys == nil {
		if keys, err = NpmKeys(); err != nil {
			return Installed{}, fmt.Errorf("Update qory.\n%w", err)
		}
	}
	if err := verify(rel, keys, time.Now(), o.Runtime+" "+rel.Version); err != nil {
		return Installed{}, err
	}
	f := folder{base: filepath.Join(o.Folder, o.Runtime), runtime: o.Runtime}
	if o.Mounted != nil {
		f.mounted = func() ([]string, error) { return o.Mounted(ctx) }
	}
	hold, err := f.hold(rel.Version)
	if err != nil {
		return Installed{}, err
	}
	in := Installed{Runtime: o.Runtime, Version: rel.Version, Dir: filepath.Join(f.base, rel.Version), hold: hold}
	done, err := f.complete(rel.Version, rel.Integrity)
	switch {
	case err != nil:
		in.Release()
		return Installed{}, err
	case done:
		return in, nil
	case rel.Open == nil:
		in.Release()
		return Installed{}, fmt.Errorf(textRelease+"The source of the runtime's releases returned no tarball of %s %s", o.Runtime, rel.Version)
	}
	// A second run that needs the version waits here for the run that reads its tarball,
	// and then finds the folder.
	lock, err := f.lockInstall(ctx, rel.Version)
	if err != nil {
		in.Release()
		return Installed{}, err
	}
	defer lock.Close()
	switch done, err := f.complete(rel.Version, rel.Integrity); {
	case err != nil:
		in.Release()
		return Installed{}, err
	case done:
		return in, nil
	}
	if err := f.install(ctx, rel, sum); err != nil {
		in.Release()
		return Installed{}, err
	}
	in.Downloaded = true
	return in, nil
}

// sha512Of is the digest of an npm checksum, sha512-<base64>.
func sha512Of(integrity string) ([]byte, error) {
	b64, ok := strings.CutPrefix(integrity, "sha512-")
	sum, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || len(sum) != 64 {
		return nil, errors.New("not a sha512 checksum")
	}
	return sum, nil
}

// upper is s with its first letter in upper case: the start of a second line of an
// error.
func upper(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}
