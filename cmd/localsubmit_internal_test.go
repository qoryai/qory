package cmd

import (
	"context"
	"slices"
	"strings"
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

// TestOnlyTheAddressOfAForgeHasASystemAndAPath is the push addresses that qory takes the system and
// the path of the checks and the program from: a host and a repository path of a forge, over
// https, http, ssh, git or the scp form. A file address, an ext address, a path, an address with no
// host, a host with no dot and a path with a dot segment are refused.
func TestOnlyTheAddressOfAForgeHasASystemAndAPath(t *testing.T) {
	for _, c := range []struct {
		address, system, path string
	}{
		{"https://github.com/acme/shop.git", "github.com", "acme/shop"},
		{"https://user:pw@GitHub.com:8443/acme/shop", "github.com", "acme/shop"},
		{"ssh://git@gitlab.example.org/group/sub/shop.git", "gitlab.example.org", "group/sub/shop"},
		{"git@github.com:acme/shop.git", "github.com", "acme/shop"},
		{"git://git.example.com/acme/shop", "git.example.com", "acme/shop"},
		{"file:///srv/git/shop.git", "", ""},
		{"file://host.example.com/srv/shop.git", "", ""},
		{"ext::sh -c touch% /tmp/x", "", ""},
		{"/srv/git/shop.git", "", ""},
		{"../shop.git", "", ""},
		{"C:/repos/shop.git", "", ""},
		{"https://localhost/acme/shop.git", "", ""},
		{"https://git.example.com/shop.git", "", ""},
		{"https://git.example.com/acme/../shop.git", "", ""},
		{"https://git.example.com/acme/shop name.git", "", ""},
		{"https:///acme/shop.git", "", ""},
	} {
		system, path, ok := remoteSystemPath(c.address)
		if ok != (c.system != "") || system != c.system || path != c.path {
			t.Errorf("%q gives %q, %q, %v, want %q and %q", c.address, system, path, ok, c.system, c.path)
		}
	}
}

// TestEveryGitOfTheLocalStepRunsWithNoPrompt checks the push and the reads without running a git
// that could read the person's credential helper: the push keeps the person's configuration, so
// it has no -c credential.helper, and its environment has GIT_TERMINAL_PROMPT=0 as the last
// setting, whatever the person's shell has. The same holds for the git of the reads.
func TestEveryGitOfTheLocalStepRunsWithNoPrompt(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	repo := localRepository{Dir: "/work", PushURL: "https://git.example.com/acme/app.git", Branch: "qory/run-1", Head: strings.Repeat("a", 40)}
	for name, env := range map[string][]string{"push": pushCommand(context.Background(), repo).Env, "reads": localGitEnv()} {
		var prompts []string
		for _, kv := range env {
			if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
				prompts = append(prompts, kv)
			}
		}
		if !slices.Equal(prompts, []string{"GIT_TERMINAL_PROMPT=0"}) {
			t.Errorf("the %s environment has %q, want GIT_TERMINAL_PROMPT=0 once", name, prompts)
		}
	}
	if args := pushArgs(repo); slices.Contains(args, "-c") {
		t.Errorf("the push has %q, want the person's git configuration, with no -c", args)
	}
}
