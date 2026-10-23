package preparation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// TestFetchesThroughTheHostProxy fetches the run's repository and a further one over
// https, from a host only the host proxy leads to, with the proxy's password: the line
// says the commit, origin is the https URL, and no file of the working folder, its .git
// included, and no line contains the password or a credential. A fetch with another
// password is denied by the proxy and fails in the line that names the fix.
func TestFetchesThroughTheHostProxy(t *testing.T) {
	needGit(t)
	s := proxygittest.Start(t)
	head := s.Repository(t, "acme/shop", "main", map[string]string{"README.md": "shop\n"})
	s.Repository(t, "acme/lib", "main", map[string]string{"README.md": "lib\n"})
	dir := emptyFolder(t)
	var report strings.Builder
	fetched, err := Run(context.Background(), dir, os.Environ(), env(proxygittest.Host, "acme/shop,acme/lib"), s.Proxy, &report)
	if err != nil || !fetched {
		t.Fatalf("%v, %v", fetched, err)
	}
	if want := "qory run: fetched acme/shop at " + head[:12] + " and 1 further repository in "; !strings.HasPrefix(report.String(), want) {
		t.Errorf("the line is %q, want %q…", report.String(), want)
	}
	if got := run(t, dir, "remote", "get-url", "origin"); got != s.URL("acme/shop") {
		t.Errorf("origin is %s", got)
	}
	if s.Tunnels() == 0 {
		t.Error("no request went through the host proxy")
	}
	noneHolds(t, dir, proxygittest.Password)
	if strings.Contains(report.String(), proxygittest.Password) {
		t.Errorf("the line contains the password: %s", report.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".git", "config")); strings.Contains(string(got), "proxy") || strings.Contains(string(got), "credential") || strings.Contains(string(got), "sslcainfo") {
		t.Errorf("git's configuration names the proxy or a credential:\n%s", got)
	}

	wrong := s.Proxy
	wrong.URL = strings.Replace(wrong.URL, proxygittest.Password, "another-password", 1)
	_, err = Run(context.Background(), emptyFolder(t), os.Environ(), env(proxygittest.Host, "acme/shop"), wrong, &report)
	prefix := "qory run: check that the run's policy allows git.test and that git.test is reachable, then run again. The fetch of acme/shop through the run's gateway failed: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || strings.Contains(err.Error(), "another-password") || strings.Contains(err.Error(), "\n") {
		t.Errorf("a denied fetch: %v, want one line starting %q", err, prefix)
	}
	if s.Denied() == 0 {
		t.Error("the proxy denied no request")
	}
}

// TestFetchesOnlyOverHTTPS is a fetch whose URL is file://, with the protocols the run
// allows: git does not fetch it.
func TestFetchesOnlyOverHTTPS(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	protocols = proxygit.HTTPSOnly
	_, err := prepare(context.Background(), emptyFolder(t), env("git.example.com", "acme/shop"))
	if err == nil || !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Errorf("a file URL: %v", err)
	}
}

// noneHolds fails the test when a file under dir contains value.
func noneHolds(t *testing.T, dir, value string) {
	t.Helper()
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), value) {
			t.Errorf("%s contains the password", path)
		}
		return nil
	})
}
