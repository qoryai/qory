// Package runtimeinstall installs an agent runtime's program for qory run, before the
// run's first session, from the publisher's npm tarball.
//
// [Install] asks its [Fetch] for the runtime's newest release: the version, npm's
// sha512 checksum of the tarball, and a way to open the tarball. It uses the folder
// <Folder>/<runtime>/<version> when an earlier install of a tarball with the same
// checksum made it, and opens no tarball then. Otherwise it reads the tarball into a
// temporary file, checks its checksum, and extracts only package/<runtime> to
// bin/<runtime> in a temporary folder, which it then renames to the version's folder: a
// folder at that path is always a complete install.
package runtimeinstall

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
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
	// platform, whose program is package/<runtime>. Install calls it at most once, and
	// only when the version's folder is not complete.
	Open func(ctx context.Context) (io.ReadCloser, error)
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
}

// Program is the path of the runtime's program: <Dir>/bin/<Runtime>.
func (i Installed) Program() string { return filepath.Join(i.Dir, "bin", i.Runtime) }

// textRelease starts each refusal of what the fetch returned.
const textRelease = "check the source of the runtime's releases: "

// Install installs the runtime o names, as the package's documentation says, and
// returns it. It takes as long as ctx allows. Every error's text starts with what to
// fix.
func Install(ctx context.Context, o Options) (Installed, error) {
	if !runtimeShape.MatchString(o.Runtime) {
		return Installed{}, fmt.Errorf("name the runtime with a lowercase letter and up to 63 lowercase letters, digits or -: %q is not one", o.Runtime)
	}
	if !filepath.IsAbs(o.Folder) {
		return Installed{}, fmt.Errorf("keep runtimes in an absolute folder: %q is not one", o.Folder)
	}
	rel, err := o.Fetch(ctx, o.Runtime)
	if err != nil {
		return Installed{}, err
	}
	if !versionShape.MatchString(rel.Version) {
		return Installed{}, fmt.Errorf(textRelease+"the newest %s has the version %q, and a version is 1 to 64 letters, digits, ., + or -, the first a letter or a digit",
			o.Runtime, rel.Version)
	}
	sum, err := sha512Of(rel.Integrity)
	if err != nil {
		return Installed{}, fmt.Errorf(textRelease+"the checksum of %s %s is not sha512- and 64 bytes in base64", o.Runtime, rel.Version)
	}
	f := folder{base: filepath.Join(o.Folder, o.Runtime), runtime: o.Runtime}
	in := Installed{Runtime: o.Runtime, Version: rel.Version, Dir: filepath.Join(f.base, rel.Version)}
	done, err := f.complete(rel.Version, rel.Integrity)
	if err != nil || done {
		return in, err
	}
	if rel.Open == nil {
		return Installed{}, errors.New(textRelease + "it returned no tarball")
	}
	if err := f.install(ctx, rel, sum); err != nil {
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
