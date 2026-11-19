package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestThePushArgumentsAreTheCheckedCommitToTheNamedRemote is the git arguments of a local push: the
// commit and not the branch name, the remote origin by its name and not its address, and the options that stop a force,
// the person's pre-push hook, a submodule push and the tags that follow the commit.
func TestThePushArgumentsAreTheCheckedCommitToTheNamedRemote(t *testing.T) {
	repo := localRepository{Dir: "/work/app", Head: "cccccccccccccccccccccccccccccccccccccccc", Branch: "qory/run-1", PushURL: "https://git.example.com/acme/app.git"}
	want := []string{"-C", "/work/app", "push", "--quiet", "--no-verify", "--no-recurse-submodules", "--no-follow-tags", "--",
		"origin", "cccccccccccccccccccccccccccccccccccccccc:refs/heads/qory/run-1"}
	if got := pushArgs(repo); !slices.Equal(got, want) {
		t.Errorf("the arguments are %q, want %q", got, want)
	}
	for _, arg := range pushArgs(repo) {
		if arg == "--force" || arg == "-f" || arg == repo.PushURL {
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

// TestThePrintedCommandsRemoveEveryPushURLValue takes the commands from [textLocalPushURLs]
// and runs them in a repository with pushurl values in the repository file and in
// ~/.config/git/config beside an existing ~/.gitconfig: no value is left afterwards.
func TestThePrintedCommandsRemoveEveryPushURLValue(t *testing.T) {
	list := regexp.MustCompile(`git config --show-origin --get-all remote\.origin\.pushurl`).FindString(textLocalPushURLs)
	remove := regexp.MustCompile(`git config [^\n]*--unset-all remote\.origin\.pushurl`).FindString(textLocalPushURLs)
	if list == "" || remove == "" {
		t.Fatalf("the text has no list command %q or remove command %q:\n%s", list, remove, textLocalPushURLs)
	}
	home, repo := t.TempDir(), t.TempDir()
	for file, content := range map[string]string{
		".gitconfig":         "[user]\n\tname = a\n",
		".config/git/config": "[remote \"origin\"]\n\tpushurl = https://git.example.com/acme/xdg.git\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(home, file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_CONFIG_") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=")
	})
	// git returns the output and the error, because git config exits with 1 when no value is left.
	git := func(args ...string) (string, error) {
		c := exec.Command("git", args...)
		c.Dir = repo
		c.Env = append(slices.Clone(env), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "--add", "remote.origin.pushurl", "https://git.example.com/acme/one.git"},
		{"config", "--add", "remote.origin.pushurl", "https://git.example.com/acme/two.git"},
	} {
		if out, err := git(args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	listed, err := git(strings.Fields(list)[1:]...)
	if err != nil || strings.Count(listed, "\n") != 3 {
		t.Fatalf("%q has the error %v and the output %q, want 3 values", list, err, listed)
	}
	// The paths of the test have no character that git quotes, so each is the text after file:.
	files := []string{}
	for line := range strings.Lines(listed) {
		file, _, _ := strings.Cut(strings.TrimPrefix(line, "file:"), "\t")
		if !slices.Contains(files, file) {
			files = append(files, file)
		}
	}
	if !strings.Contains(remove, "<file>") {
		files = []string{""}
	}
	for _, file := range files {
		args := strings.Fields(remove)[1:]
		for i, arg := range args {
			if arg == "<file>" {
				args[i] = file
			}
		}
		if out, err := git(args...); err != nil {
			t.Fatalf("%q with the file %q: %v\n%s", remove, file, err, out)
		}
	}
	if left, _ := git("config", "--get-all", "remote.origin.pushurl"); left != "" {
		t.Errorf("%q leaves the values %q, want none", remove, left)
	}
}
