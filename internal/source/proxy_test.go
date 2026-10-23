package source_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// TestResolveGitThroughTheHostProxy resolves a branch of a repository on a host only the
// host proxy leads to: the commit is the branch's, the clone is in the cache given, the
// fetch is reported once per URL and ref, and no file of the cache contains the proxy's
// password.
func TestResolveGitThroughTheHostProxy(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	commit := s.Repository(t, "acme/harness", "main", map[string]string{"modules/core/AGENTS.md": "# Core\n"})
	cache := t.TempDir()
	var fetched []string
	g := &source.Git{Proxy: s.Proxy, Fetched: func(url, ref, c string, took time.Duration) {
		fetched = append(fetched, url+"#"+ref+" "+c)
	}}
	memo := source.NewMemo()
	src := stack.Source{Git: s.URL("acme/harness"), Ref: "main", Path: "modules/core"}
	for range 2 {
		r, err := source.Resolve(t.TempDir(), src, source.Options{Cache: cache, Memo: memo, Git: g})
		if err != nil {
			t.Fatal(err)
		}
		if r.Commit != commit {
			t.Errorf("the commit is %s, want %s", r.Commit, commit)
		}
		if !strings.HasPrefix(r.Dir, cache) {
			t.Errorf("the module is at %s, outside the cache %s", r.Dir, cache)
		}
	}
	if want := []string{s.URL("acme/harness") + "#main " + commit}; strings.Join(fetched, "\n") != strings.Join(want, "\n") {
		t.Errorf("fetched %q, want %q", fetched, want)
	}
	if s.Tunnels() == 0 {
		t.Error("no request went through the host proxy")
	}
	filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), proxygittest.Password) {
				t.Errorf("%s contains the password", path)
			}
		}
		return nil
	})
}

// TestResolveGitThroughTheHostProxyFails is a ref the remote does not have, a fetch the
// proxy denies, and sources that are not https: a GoneError, a FetchError whose output
// contains no password, and a SchemeError before git runs.
func TestResolveGitThroughTheHostProxyFails(t *testing.T) {
	hermetic(t)
	s := proxygittest.Start(t)
	s.Repository(t, "acme/harness", "main", map[string]string{"AGENTS.md": "# Core\n"})
	g := &source.Git{Proxy: s.Proxy}
	_, err := source.Resolve(t.TempDir(), stack.Source{Git: s.URL("acme/harness"), Ref: "gone"}, source.Options{Cache: t.TempDir(), Git: g})
	var gone *source.GoneError
	if !errors.As(err, &gone) {
		t.Errorf("a ref the remote does not have: %v", err)
	}
	denied := &source.Git{Proxy: s.Proxy}
	denied.Proxy.URL = strings.Replace(denied.Proxy.URL, proxygittest.Password, "another-password", 1)
	_, err = source.Resolve(t.TempDir(), stack.Source{Git: s.URL("acme/harness"), Ref: "main"}, source.Options{Cache: t.TempDir(), Git: denied})
	var fetch *source.FetchError
	if !errors.As(err, &fetch) || fetch.URL != s.URL("acme/harness") || fetch.Ref != "main" || strings.Contains(fetch.Output, "another-password") {
		t.Errorf("a denied fetch: %v", err)
	}
	before := s.Tunnels() + s.Denied()
	for _, c := range []struct{ url, want string }{
		{"git@github.com:example-org/harness.git", "use an https URL for git@github.com:example-org/harness.git, such as https://github.com/example-org/harness. A run from a control plane fetches git sources only over https, through the run's gateway; ssh would use this machine's keys."},
		{"ssh://git@github.com:22/example-org/harness.git", "use an https URL for ssh://git@github.com:22/example-org/harness.git, such as https://github.com/example-org/harness. A run from a control plane fetches git sources only over https, through the run's gateway; ssh would use this machine's keys."},
		{"git://example.com/acme/harness", "use an https URL for git://example.com/acme/harness, such as https://example.com/acme/harness. A run from a control plane fetches git sources only over https, through the run's gateway; git:// would reach the host past the gateway."},
		{"file:///srv/harness", "use an https URL for file:///srv/harness. A run from a control plane fetches git sources only over https, through the run's gateway; file:///srv/harness is a folder of this machine."},
		{"../harness", "use an https URL for ../harness. A run from a control plane fetches git sources only over https, through the run's gateway; ../harness is a folder of this machine."},
		{"fd::3", "use an https URL for fd::3. A run from a control plane fetches git sources only over https, through the run's gateway; fd::3 is not an https URL."},
		{"ext::sh", "use an https URL for ext::sh. A run from a control plane fetches git sources only over https, through the run's gateway; ext::sh is not an https URL."},
	} {
		_, err := source.Resolve(t.TempDir(), stack.Source{Git: c.url, Ref: "main"}, source.Options{Cache: t.TempDir(), Git: g})
		var scheme *source.SchemeError
		if !errors.As(err, &scheme) || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.url, err, c.want)
		}
	}
	if after := s.Tunnels() + s.Denied(); after != before {
		t.Errorf("the sources that are not https reached the proxy %d times", after-before)
	}
	if line := source.HTTPSFix("git@github.com:example-org/harness.git", "qory.yaml"); !strings.HasPrefix(line, "use an https URL for git@github.com:example-org/harness.git in qory.yaml, such as https://github.com/example-org/harness. ") {
		t.Errorf("with the file: %s", line)
	}
	if line := source.HTTPSFix("https://example.com/acme/harness", "qory.yaml"); line != "" {
		t.Errorf("an https URL: %s", line)
	}
}
