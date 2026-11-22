// Package runtimeinstalltest is a source of a runtime's releases for the tests of the
// runtime's install: in memory, [Source], and as the runtime cache of a stub registry on
// loopback behind a stand-in for the run's host proxy, [Registry] and [Proxy]. Nothing
// reaches a network outside this machine.
package runtimeinstalltest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"io"
	"sync"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// Entry is one entry of a test tarball: a regular file with Body when Kind is 0, else
// an entry of Kind, such as a link to Link. Mode is the entry's mode, 0644 when it is 0.
type Entry struct {
	Name, Body, Link string
	Kind             byte
	Mode             int64
}

// Tarball is a gzip tar archive of the entries, as npm packs a package.
func Tarball(t *testing.T, entries ...Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		kind := e.Kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.Name, Typeflag: kind, Mode: mode, Linkname: e.Link}
		if kind == tar.TypeReg {
			h.Size = int64(len(e.Body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Program is the program of version in [PlainTarball].
func Program(version string) string {
	return "#!/bin/sh\necho claude " + version + "\n"
}

// CodexProgram is the program of version in [CodexTarball].
func CodexProgram(version string) string {
	return "#!/bin/sh\necho codex " + version + "\n"
}

// CodexTarball is a tarball of Codex's build of version for linux/arch, amd64 or arm64,
// in the layout of npm's @openai/codex@0.170.0-linux-x64 and -linux-arm64: the folder
// package/vendor/<x86_64 or aarch64>-unknown-linux-musl/ with the program bin/codex and
// the files beside it, see [CodexFolder], and package/package.json and package/codex
// outside it. As in npm's tarball, the archive has no entries for folders.
func CodexTarball(t *testing.T, version, arch string) []byte {
	triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch] + "-unknown-linux-musl"
	entries := []Entry{{Name: "package/package.json", Body: `{"name":"@openai/codex"}`}}
	for _, f := range CodexFolder(version + " " + arch) {
		entries = append(entries, Entry{Name: "package/vendor/" + triple + "/" + f.Name, Body: f.Body, Mode: f.Mode})
	}
	return Tarball(t, append(entries, Entry{Name: "package/codex", Body: "not the program\n"})...)
}

// CodexFolder is the folder of Codex's build of version in [CodexTarball], by each
// file's path in the folder: the program bin/codex, bin/codex-code-mode-host,
// codex-resources/bwrap and codex-path/rg, each mode 0755, and codex-package.json and
// codex-resources/voice/manifest.json, mode 0644.
func CodexFolder(version string) []Entry {
	return []Entry{
		{Name: "bin/codex", Body: CodexProgram(version), Mode: 0o755},
		{Name: "bin/codex-code-mode-host", Body: "#!/bin/sh\necho code mode host\n", Mode: 0o755},
		{Name: "codex-resources/bwrap", Body: "#!/bin/sh\necho bwrap\n", Mode: 0o755},
		{Name: "codex-path/rg", Body: "#!/bin/sh\necho rg\n", Mode: 0o755},
		{Name: "codex-package.json", Body: `{"layoutVersion":1,"entrypoint":"bin/codex","resourcesDir":"codex-resources","pathDir":"codex-path"}` + "\n", Mode: 0o644},
		{Name: "codex-resources/voice/manifest.json", Body: "{}\n", Mode: 0o644},
	}
}

// PlainTarball is a tarball of the claude package of version.
func PlainTarball(t *testing.T, version string) []byte {
	return Tarball(t,
		Entry{Name: "package/package.json", Body: `{"name":"@example/claude-linux"}`},
		Entry{Name: "package/claude", Body: Program(version)},
		Entry{Name: "package/README.md", Body: "readme\n"},
	)
}

// Digest is the sha256 digest of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Integrity is npm's checksum of b.
func Integrity(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// Source is the newest release of a runtime, in memory: its fetch returns it, and
// counts the fetches and the tarballs opened.
type Source struct {
	mu        sync.Mutex
	version   string
	integrity string
	tarball   []byte
	err       error
	fetches   int
	opens     int
	runtimes  []string
}

// Publish makes version, with the tarball and its checksum, the newest release.
func (s *Source) Publish(version string, tarball []byte) {
	s.PublishWith(version, tarball, Integrity(tarball))
}

// PublishWith makes version, with the tarball and the checksum integrity, the newest
// release.
func (s *Source) PublishWith(version string, tarball []byte, integrity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version, s.tarball, s.integrity, s.err = version, tarball, integrity, nil
}

// Fail makes each fetch return err.
func (s *Source) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Fetch is the source's [runtimeinstall.Fetch]: it returns the newest release whatever the version.
func (s *Source) Fetch(_ context.Context, runtime, _ string) (runtimeinstall.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	s.runtimes = append(s.runtimes, runtime)
	if s.err != nil {
		return runtimeinstall.Release{}, s.err
	}
	tarball := s.tarball
	signed := "@anthropic-ai/claude-code-linux-x64@" + s.version
	return runtimeinstall.Release{
		Version: s.version, Integrity: s.integrity,
		Signed: signed, Signatures: Signatures(signed, s.integrity),
		Open: func(context.Context) (io.ReadCloser, error) {
			s.mu.Lock()
			s.opens++
			s.mu.Unlock()
			return io.NopCloser(bytes.NewReader(tarball)), nil
		},
	}, nil
}

// Fetches is how many fetches the source answered.
func (s *Source) Fetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

// Opens is how many tarballs the source opened.
func (s *Source) Opens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens
}

// Runtimes is the runtime of each fetch, in order.
func (s *Source) Runtimes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.runtimes...)
}
