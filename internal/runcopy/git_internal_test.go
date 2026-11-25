package runcopy

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestGitByPathAloneReachesAPath: a git of [git] reaches no repository by its path, also with the
// file protocol allowed, and one of [gitByPath] does.
func TestGitByPathAloneReachesAPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx, from, to := context.Background(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "first"}} {
		if _, err := git(ctx, from, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := git(ctx, to, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(ctx, to, "fetch", "--quiet", "--", from, "HEAD"); err == nil || !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Errorf("git fetch: %v", err)
	}
	if _, err := gitByPath(ctx, to, nil, "fetch", "--quiet", "--", from, "HEAD"); err != nil {
		t.Errorf("gitByPath fetch: %v", err)
	}
}
