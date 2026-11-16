// Package runtimeinstalltest is an in-memory source of a runtime's releases, npm
// tarballs with their checksums, for the tests of the runtime's install.
package runtimeinstalltest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"io"
	"sync"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// Entry is one entry of a test tarball: a regular file with Body when Kind is 0, else
// an entry of Kind, such as a link to Link.
type Entry struct {
	Name, Body, Link string
	Kind             byte
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
		h := &tar.Header{Name: e.Name, Typeflag: kind, Mode: 0o644, Linkname: e.Link}
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

// PlainTarball is a tarball of the claude package of version.
func PlainTarball(t *testing.T, version string) []byte {
	return Tarball(t,
		Entry{Name: "package/package.json", Body: `{"name":"@example/claude-linux"}`},
		Entry{Name: "package/claude", Body: Program(version)},
		Entry{Name: "package/README.md", Body: "readme\n"},
	)
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

// Fetch is the source's [runtimeinstall.Fetch].
func (s *Source) Fetch(_ context.Context, runtime string) (runtimeinstall.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	s.runtimes = append(s.runtimes, runtime)
	if s.err != nil {
		return runtimeinstall.Release{}, s.err
	}
	tarball := s.tarball
	return runtimeinstall.Release{
		Version: s.version, Integrity: s.integrity,
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
