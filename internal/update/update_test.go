package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// server serves a fake GitHub: the latest release tagged tag, and under downloads the
// archives given by name.
func server(t *testing.T, tag string, files map[string][]byte) Site {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if tag == "" {
			http.Error(w, "rate limit exceeded", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"` + tag + `","name":"` + tag + `"}`))
	})
	mux.HandleFunc("/"+Repo+"/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return Site{API: s.URL, Downloads: s.URL, Client: s.Client()}
}

// archive builds a release archive holding binary as qory beside a README, and the
// checksums.txt line that lists it.
func archive(t *testing.T, name string, binary []byte) (data []byte, sums []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("read me")}, {"qory", binary}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

// TestLatestReadsTheTagWithoutItsV pins that the latest release is its tag without the v,
// and that a refused request, GitHub's rate limit, is an error rather than a version.
func TestLatestReadsTheTagWithoutItsV(t *testing.T) {
	latest, err := server(t, "v0.5.0", nil).Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest != "0.5.0" {
		t.Errorf("latest = %q, want 0.5.0", latest)
	}
	if _, err := server(t, "", nil).Latest(context.Background()); err == nil {
		t.Error("a refused request gave no error")
	}
	if _, err := server(t, "nightly", nil).Latest(context.Background()); err == nil {
		t.Error("a tag that is not a version gave no error")
	}
}

// TestNewerComparesVersionsAndRefusesWhatIsNotOne pins the comparison: a higher release
// is newer, the same or a lower one is not, and a build with no version or a
// pseudo-version is never behind, so it is never told to update.
func TestNewerComparesVersionsAndRefusesWhatIsNotOne(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"0.4.0", "0.5.0", true},
		{"0.4.0", "0.4.1", true},
		{"0.4.0", "1.0.0", true},
		{"0.4.0", "0.4.0", false},
		{"0.5.0", "0.4.0", false},
		{"0.5.0-rc.1", "0.5.0", true},
		{"", "0.5.0", false},
		// A build from main after v0.4.0 is behind v0.5.0 and v0.4.1, not v0.4.0.
		{"0.4.1-0.20260911000000-abc123456789", "0.5.0", true},
		{"0.4.1-0.20260911000000-abc123456789", "0.4.1", true},
		{"0.4.1-0.20260911000000-abc123456789", "0.4.0", false},
		{"0.4.0", "", false},
		{"0.4.0", "latest", false},
	}
	for _, tc := range tests {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

// TestCheckAsksOnceAnHour pins the cache: the first check asks GitHub and writes the
// file, a check within the interval reads the file and never asks, a check after the
// interval asks again, a check by another version of qory asks again, a failed request
// leaves the file as it was, and Clear forgets it all.
func TestCheckAsksOnceAnHour(t *testing.T) {
	c := Cache{Path: filepath.Join(t.TempDir(), "qory", "update-check.json")}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	latest, err := Check(ctx, server(t, "v0.5.0", nil), c, "0.4.0", now)
	if err != nil || latest != "0.5.0" {
		t.Fatalf("first check = %q, %v; want 0.5.0", latest, err)
	}
	r, ok := c.Read()
	if !ok || r.Latest != "0.5.0" || r.Current != "0.4.0" || !r.Checked.Equal(now) {
		t.Fatalf("cache = %+v, %v; want 0.5.0 found by 0.4.0 at %v", r, ok, now)
	}

	// The site now says 0.6.0, but the cache is fresh, so the answer is still 0.5.0.
	latest, err = Check(ctx, server(t, "v0.6.0", nil), c, "0.4.0", now.Add(Interval-time.Minute))
	if err != nil || latest != "0.5.0" {
		t.Errorf("fresh check = %q, %v; want the cached 0.5.0", latest, err)
	}

	// The binary was replaced: a fresh record made by another version is not trusted.
	latest, err = Check(ctx, server(t, "v0.6.0", nil), c, "0.5.0", now.Add(time.Minute))
	if err != nil || latest != "0.6.0" {
		t.Errorf("check by another version = %q, %v; want a fresh 0.6.0", latest, err)
	}
	if r, _ := c.Read(); r.Current != "0.5.0" {
		t.Errorf("cache is by %q, want 0.5.0", r.Current)
	}

	// A failed request after the interval is an error and keeps the cache.
	if _, err := Check(ctx, server(t, "", nil), c, "0.5.0", now.Add(Interval+2*time.Minute)); err == nil {
		t.Error("a refused request after the interval gave no error")
	}
	if r, _ := c.Read(); r.Latest != "0.6.0" {
		t.Errorf("cache after the failed request = %q, want 0.6.0", r.Latest)
	}

	latest, err = Check(ctx, server(t, "v0.7.0", nil), c, "0.5.0", now.Add(Interval+2*time.Minute))
	if err != nil || latest != "0.7.0" {
		t.Errorf("stale check = %q, %v; want 0.7.0", latest, err)
	}

	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Read(); ok {
		t.Error("the cache reads after Clear")
	}
	if err := c.Clear(); err != nil {
		t.Errorf("clearing a cleared cache: %v", err)
	}
}

// TestDetectTellsTheChannelFromThePath pins how an install is recognised: a cellar or
// caskroom path is Homebrew, a release build anywhere else is a release binary, a source
// build in GOBIN is a go install, and a source build elsewhere is a checkout's.
func TestDetectTellsTheChannelFromThePath(t *testing.T) {
	gobin := filepath.Join(t.TempDir(), "go", "bin")
	tests := []struct {
		name    string
		exe     string
		release bool
		want    Channel
	}{
		{"a cask", "/opt/homebrew/Caskroom/qory/0.4.0/qory", true, Homebrew},
		{"a formula", "/usr/local/Cellar/qory/0.4.0/bin/qory", true, Homebrew},
		{"the install script's", "/home/me/.local/bin/qory", true, Release},
		{"a go install", filepath.Join(gobin, "qory"), false, GoInstall},
		{"a go build", "/home/me/code/qory/qory", false, Source},
	}
	for _, tc := range tests {
		if got := Detect(tc.exe, tc.release, gobin); got != tc.want {
			t.Errorf("%s: Detect = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestInstallReplacesTheBinaryAfterCheckingTheSum pins the swap: the binary in the
// archive lands at the executable's path, executable, and an archive whose sum the
// release does not list leaves the old binary alone.
func TestInstallReplacesTheBinaryAfterCheckingTheSum(t *testing.T) {
	name := Archive("0.5.0", runtime.GOOS, runtime.GOARCH)
	data, sums := archive(t, name, []byte("new binary"))
	exe := filepath.Join(t.TempDir(), "qory")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := server(t, "v0.5.0", map[string][]byte{name: data, "checksums.txt": sums})
	if err := s.Install(context.Background(), "0.5.0", exe, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("the binary reads %q, want the new one", got)
	}
	if info, _ := os.Stat(exe); info.Mode()&0o111 == 0 {
		t.Error("the new binary is not executable")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".qory-update-*")); len(left) > 0 {
		t.Errorf("a temporary file was left behind: %v", left)
	}

	// The sums list another archive, so the download does not verify.
	_, wrong := archive(t, name, []byte("something else"))
	s = server(t, "v0.5.0", map[string][]byte{name: data, "checksums.txt": wrong})
	if err := s.Install(context.Background(), "0.5.0", exe, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Error("an archive with the wrong checksum was installed")
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Errorf("the binary reads %q after the refused install, want it untouched", got)
	}

	// A platform the release has no archive for.
	if err := s.Install(context.Background(), "0.5.0", exe, "plan9", "mips"); err == nil {
		t.Error("a missing archive was installed")
	}
}
