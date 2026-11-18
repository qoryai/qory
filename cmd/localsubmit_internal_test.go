package cmd

import (
	"slices"
	"testing"
)

// TestThePushArgumentsAreTheCheckedCommitToTheOneAddress is the git arguments of a local push: the
// commit and not the branch name, the one address after "--", and the options that stop a force,
// the person's pre-push hook, a submodule push and the tags that follow the commit.
func TestThePushArgumentsAreTheCheckedCommitToTheOneAddress(t *testing.T) {
	repo := localRepository{Dir: "/work/app", Head: "cccccccccccccccccccccccccccccccccccccccc", Branch: "qory/run-1", PushURL: "https://git.example.com/acme/app.git"}
	want := []string{"-C", "/work/app", "push", "--quiet", "--no-verify", "--no-recurse-submodules", "--no-follow-tags", "--",
		"https://git.example.com/acme/app.git", "cccccccccccccccccccccccccccccccccccccccc:refs/heads/qory/run-1"}
	if got := pushArgs(repo); !slices.Equal(got, want) {
		t.Errorf("the arguments are %q, want %q", got, want)
	}
	for _, arg := range pushArgs(repo) {
		if arg == "origin" || arg == "--force" || arg == "-f" {
			t.Errorf("the arguments have %q", arg)
		}
	}
}
