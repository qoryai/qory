package preparation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/proxygit"
)

// TestOnlyAGitWithAProxyReachesARemote: with the file protocol allowed, a git with a proxy fetches
// from a path, and one with no proxy or with [noRemote] does not.
func TestOnlyAGitWithAProxyReachesARemote(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	from := filepath.Join(base, "acme", "shop.git")
	to := t.TempDir()
	run(t, to, "init", "--quiet", "--bare")
	for _, c := range []struct {
		name string
		p    *proxygit.Proxy
		want bool
	}{{"a proxy", &testProxy, true}, {"no proxy", nil, false}, {"noRemote", noRemote, false}} {
		_, err := hostRunnerOf(c.p, hasRemote(c.p), localFetch, "", nil)(context.Background(), to, "fetch", "--quiet", from, "main")
		if (err == nil) != c.want || err != nil && !strings.Contains(err.Error(), "transport 'file' not allowed") {
			t.Errorf("%s: the fetch returned %v", c.name, err)
		}
		_, err = hostRunner(c.p)(context.Background(), to, "ls-remote", "--quiet", from)
		if (err == nil) != c.want {
			t.Errorf("%s: ls-remote returned %v", c.name, err)
		}
		_, err = stepGit(c.p, "")(context.Background(), to, "ls-remote", "--quiet", from)
		if (err == nil) != c.want {
			t.Errorf("%s: ls-remote of the push step returned %v", c.name, err)
		}
		// A subcommand that reaches no remote has no protocol with a proxy either.
		if _, err := hostRunner(c.p)(context.Background(), to, "archive", "--remote="+from, "main"); err == nil {
			t.Errorf("%s: git archive reached a remote", c.name)
		}
	}
}
