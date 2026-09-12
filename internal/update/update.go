// Package update finds the newest release of qory and installs it.
//
// A release is a tag on GitHub, and every way qory is installed follows those tags: the
// Homebrew cask, the install script, and go install. [Site.Latest] asks GitHub which
// release is newest, [Newer] says whether that is ahead of the running binary, and
// [Site.Install] replaces a release binary with the newest release's archive for its
// platform, the way the install script does, after checking the archive against the
// release's checksums.
//
// [Check] is what every command runs: it reads the answer from [Cache] when the last look
// is less than [Interval] old and asks GitHub otherwise, so an hour of commands costs one
// request. The cache records the version that looked as well as what it found, so a
// binary that has been replaced since starts over rather than reading the old binary's
// answer. [Detect] tells how the running binary was installed, which decides how
// qory update replaces it.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// Repo is the GitHub repository the releases are published from.
const Repo = "qoryai/qory"

// Site is where releases are read and downloaded from. [GitHub] is the real one; a test
// points both URLs at its own server.
type Site struct {
	// API is the base of the REST API, https://api.github.com.
	API string
	// Downloads is the base the release archives are downloaded from, https://github.com.
	Downloads string
	// Client makes the requests. It carries the timeout, so a check that hangs gives up
	// rather than holding a command.
	Client *http.Client
}

// GitHub is the site the releases live on. The client gives up after a few seconds,
// which bounds how long a command may wait for the daily check.
var GitHub = Site{
	API:       "https://api.github.com",
	Downloads: "https://github.com",
	Client:    &http.Client{Timeout: 3 * time.Second},
}

// Latest returns the version of the newest release without its leading v, read from the
// repository's latest-release endpoint. A rate-limited or otherwise refused request is
// an error that says so.
func (s Site) Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "qory")
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s for the latest release of %s", resp.Status, Repo)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("reading the latest release of %s: %w", Repo, err)
	}
	version := strings.TrimPrefix(release.Tag, "v")
	if !semver.IsValid("v" + version) {
		return "", fmt.Errorf("the latest release of %s is tagged %q, which is not a version", Repo, release.Tag)
	}
	return version, nil
}

// Newer reports whether latest is a newer version than current. Both are versions
// without a leading v, and current may be a pseudo-version, which is how a build from
// the main branch between releases is stamped: v0.4.1-0.<time>-<commit> sorts after the
// release v0.4.0 it follows and before a release v0.4.1, so such a build is told of a
// release only when it is behind one. It is false when either is not a version, so a
// build that carries none is never told to update, and false when the two are equal or
// current is ahead.
func Newer(current, latest string) bool {
	c, l := "v"+current, "v"+latest
	if !semver.IsValid(c) || !semver.IsValid(l) {
		return false
	}
	return semver.Compare(c, l) < 0
}

// Ahead reports whether current is a newer version than latest: a build from the main
// branch after the newest release, or a release the site has not published yet. Like
// [Newer] it is false when either is not a version.
func Ahead(current, latest string) bool {
	return Newer(latest, current)
}

// ReleaseURL is the page of the release tagged with version, where its changelog is read.
func ReleaseURL(version string) string {
	return "https://github.com/" + Repo + "/releases/tag/v" + version
}

// Cache remembers the newest release seen, when it was looked for and which version of
// qory looked, in one JSON file, so that one command an hour asks GitHub and every other
// command reads the answer.
type Cache struct {
	// Path is the file. [CachePath] is where it is kept.
	Path string
}

// CachePath is the cache file: qory/update-check.json under the user's cache directory,
// ~/Library/Caches on macOS and $XDG_CACHE_HOME or ~/.cache on Linux.
func CachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "qory", "update-check.json"), nil
}

// Record is the file's content: when the look was made, the version of qory that made
// it, and the newest release it found.
type Record struct {
	Checked time.Time `json:"checked"`
	Current string    `json:"current"`
	Latest  string    `json:"latest"`
}

// Read returns what the cache remembers. ok is false when the file is missing, unreadable
// or names no release, which reads as never checked.
func (c Cache) Read() (r Record, ok bool) {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return Record{}, false
	}
	if err := json.Unmarshal(data, &r); err != nil || r.Latest == "" {
		return Record{}, false
	}
	return r, true
}

// Write remembers r. The file is written beside its final name and renamed into place,
// so a command that exits mid-write leaves the old answer rather than half of the new
// one.
func (c Cache) Write(r Record) error {
	r.Checked = r.Checked.UTC()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	tmp := c.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}

// Clear forgets what the cache remembers, so the next command looks again. It is what
// an update does once the binary is replaced. A missing file is already clear.
func (c Cache) Clear() error {
	err := os.Remove(c.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Interval is how long the cached answer is trusted before GitHub is asked again.
const Interval = time.Hour

// Check returns the newest release for the binary running current: the cached one when
// current made the look less than [Interval] before now, else the one GitHub reports,
// which is then cached under current. A look another version made is not trusted,
// because the binary was replaced since and starts over. A failed request is returned as
// an error and leaves the cache as it was, so the next command tries again.
func Check(ctx context.Context, s Site, c Cache, current string, now time.Time) (string, error) {
	if r, ok := c.Read(); ok && r.Current == current && now.Sub(r.Checked) < Interval && now.Sub(r.Checked) >= 0 {
		return r.Latest, nil
	}
	latest, err := s.Latest(ctx)
	if err != nil {
		return "", err
	}
	if err := c.Write(Record{Checked: now, Current: current, Latest: latest}); err != nil {
		return latest, err
	}
	return latest, nil
}

// Channel is how the running binary was installed, which decides how it is updated.
type Channel int

const (
	// Source is a build from a checkout, go build or a go install of a commit, which
	// nothing replaces: the person rebuilds it or installs a release.
	Source Channel = iota
	// Homebrew is the cask, updated by brew upgrade.
	Homebrew
	// GoInstall is go install into GOBIN, updated by running it again at the latest tag.
	GoInstall
	// Release is a release binary, the install script's or a downloaded one, updated by
	// replacing the file with the newest release's.
	Release
)

// String names the channel the way qory update reports it.
func (c Channel) String() string {
	switch c {
	case Homebrew:
		return "homebrew"
	case GoInstall:
		return "go install"
	case Release:
		return "release binary"
	}
	return "source build"
}

// Detect tells how the binary at exe was installed. A path inside a Homebrew cellar or
// caskroom is [Homebrew] however it was built. Otherwise a release build, one whose
// version was set at build time, is [Release]; a source build under gobin, the directory
// go install writes to, is [GoInstall]; and any other source build is [Source]. exe is
// read with its links resolved, since Homebrew and go install both link or copy the
// binary into a bin directory on PATH.
func Detect(exe string, release bool, gobin string) Channel {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	sep := string(filepath.Separator)
	if strings.Contains(exe, sep+"Cellar"+sep) || strings.Contains(exe, sep+"Caskroom"+sep) {
		return Homebrew
	}
	if release {
		return Release
	}
	if gobin != "" {
		if resolved, err := filepath.EvalSymlinks(gobin); err == nil {
			gobin = resolved
		}
		if filepath.Dir(exe) == filepath.Clean(gobin) {
			return GoInstall
		}
	}
	return Source
}

// GoBin is the directory go install writes binaries to: GOBIN when set, else bin under
// the first GOPATH entry, else ~/go/bin. It asks the go command, which knows a value set
// with go env -w, and reads the environment when there is no go command.
func GoBin() string {
	if out, err := exec.Command("go", "env", "GOBIN", "GOPATH").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 2 {
			if gobin := strings.TrimSpace(lines[0]); gobin != "" {
				return gobin
			}
			if gopath := strings.TrimSpace(lines[1]); gopath != "" {
				return filepath.Join(filepath.SplitList(gopath)[0], "bin")
			}
		}
	}
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		return gobin
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		return filepath.Join(filepath.SplitList(gopath)[0], "bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "bin")
}

// Archive is the name of the release archive for version on the platform goos and
// goarch, as goreleaser names it: qory_0.4.0_darwin_arm64.tar.gz.
func Archive(version, goos, goarch string) string {
	return fmt.Sprintf("qory_%s_%s_%s.tar.gz", version, goos, goarch)
}

// Install replaces the binary at exe with the release of version for goos and goarch. It
// downloads the release's archive and its checksums, refuses an archive whose SHA-256 is
// not the one the release lists, takes the qory binary out of the archive, writes it
// beside exe and renames it over exe, so the file is the old binary or the new one at
// every moment and never half of either. An error names what failed: a platform the
// release has no build for, a checksum that does not match, or a directory the process
// may not write to.
func (s Site) Install(ctx context.Context, version, exe, goos, goarch string) error {
	archive := Archive(version, goos, goarch)
	base := s.Downloads + "/" + Repo + "/releases/download/v" + version + "/"
	data, err := s.fetch(ctx, base+archive)
	if err != nil {
		return fmt.Errorf("release %s has no archive %s: %w", version, archive, err)
	}
	sums, err := s.fetch(ctx, base+"checksums.txt")
	if err != nil {
		return fmt.Errorf("release %s has no checksums.txt: %w", version, err)
	}
	if err := verify(data, sums, archive); err != nil {
		return err
	}
	binary, err := extract(data)
	if err != nil {
		return fmt.Errorf("reading %s: %w", archive, err)
	}
	return replace(exe, binary)
}

// fetch downloads one URL whole. A release archive is a few megabytes.
func (s Site) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "qory")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// verify checks that sums, the release's checksums.txt, lists the SHA-256 of data under
// the name archive. The file has one line per archive, the hex sum, two spaces, the
// name, as sha256sum writes it.
func verify(data, sums []byte, archive string) error {
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == archive {
			if fields[0] == want {
				return nil
			}
			return fmt.Errorf("the checksum of %s does not match the release; nothing was installed", archive)
		}
	}
	return fmt.Errorf("the release's checksums.txt does not list %s; nothing was installed", archive)
}

// extract returns the qory binary from the release archive, a gzipped tar with the
// binary at its root beside the licence files.
func extract(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive holds no qory binary")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "qory" {
			return io.ReadAll(tr)
		}
	}
}

// replace writes binary beside exe, executable, and renames it over exe. The rename is
// what makes the swap atomic, and it works while the old binary runs, because the
// running process holds the old inode.
func replace(exe string, binary []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".qory-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s, where qory is installed: %w", dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, exe); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	return nil
}
