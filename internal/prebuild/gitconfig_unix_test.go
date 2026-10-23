//go:build unix

package prebuild_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// withGitConfig writes the repository's .git/config.
func withGitConfig(t *testing.T, repo, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildRefusesACredentialForGit is a .git/config that holds a credential for git,
// or includes a file that may: refused before any program runs, naming the key and
// never its value.
func TestBuildRefusesACredentialForGit(t *testing.T) {
	const credential = " holds a credential for git in .git/config, "
	const include = " includes another file in .git/config, "
	for _, c := range []struct{ name, config, text, key string }{
		{"extraHeader for a URL", "[http \"https://example.com/\"]\n\textraHeader = AUTHORIZATION: basic " + secret + "\n", credential, "http.*.extraheader"},
		{"extraHeader for every URL", "[http]\n\tEXTRAHEADER = Authorization: bearer " + secret + "\n", credential, "http.extraheader"},
		{"credential helper", "[credential]\n\thelper = store --file /tmp/" + secret + "\n", credential, "credential.helper"},
		{"credential for a URL", "[credential \"https://" + secret + "@example.com\"]\n\tusername = bot\n", credential, "credential.*.username"},
		{"user in the remote's URL", "[remote \"origin\"]\n\turl = https://" + secret + "@example.com/acme/shop.git\n", credential, "remote.*.url"},
		{"password in the push URL", "[remote \"origin\"]\n\turl = https://example.com/acme/shop.git\n\tpushurl = https://bot:" + secret + "@example.com/acme/shop.git\n", credential, "remote.*.pushurl"},
		{"credential in insteadOf's base", "[url \"https://bot:" + secret + "@example.com/\"]\n\tinsteadOf = https://example.com/\n", credential, "url.*.insteadof"},
		{"credential in pushInsteadOf", "[url \"https://example.com/\"]\n\tpushInsteadOf = https://bot:" + secret + "@example.com/\n", credential, "url.*.pushinsteadof"},
		{"URL as the remote's name", "[remote \"https://bot:" + secret + "@example.com/x\"]\n\turl = https://example.com/acme/shop.git\n", credential, "remote.*.url"},
		{"password over ssh", "[remote \"origin\"]\n\turl = ssh://git:" + secret + "@example.com/acme/shop.git\n", credential, "remote.*.url"},
		{"submodule's URL", "[submodule \"lib\"]\n\turl = https://" + secret + "@example.com/acme/lib.git\n", credential, "submodule.*.url"},
		{"branch's remote as a URL", "[branch \"main\"]\n\tremote = https://bot:" + secret + "@example.com/acme/shop.git\n", credential, "branch.*.remote"},
		{"branch's pushRemote as a URL", "[branch \"main\"]\n\tpushRemote = https://" + secret + "@example.com/acme/shop.git\n", credential, "branch.*.pushremote"},
		{"pushDefault as a URL", "[remote]\n\tpushDefault = https://" + secret + "@example.com/acme/shop.git\n", credential, "remote.pushdefault"},
		{"http.proxy", "[http]\n\tproxy = http://bot:" + secret + "@proxy.example.com:3128\n", credential, "http.proxy"},
		{"http.<url>.proxy", "[http \"https://example.com/\"]\n\tproxy = http://" + secret + "@proxy.example.com:3128\n", credential, "http.*.proxy"},
		{"remote's proxy", "[remote \"origin\"]\n\turl = https://example.com/acme/shop.git\n\tproxy = http://bot:" + secret + "@proxy.example.com:3128\n", credential, "remote.*.proxy"},
		{"credential in an http section's URL", "[http \"https://bot:" + secret + "@example.com/\"]\n\tsslVerify = true\n", credential, "http.*.sslverify"},
		{"include", "[include]\n\tpath = /tmp/" + secret + "\n", include, "include.path"},
		{"includeIf", "[includeIf \"gitdir:/tmp/" + secret + "/\"]\n\tpath = shop.inc\n", include, "includeif.*.path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes(t)
			var out, log bytes.Buffer
			repo := repository(t, shopConfig)
			withGitConfig(t, repo, c.config)
			_, err := prebuild.Build(t.Context(), options(t, repo, &out, &log))
			var refusal *prebuild.RefusedError
			if !errors.As(err, &refusal) {
				t.Fatalf("Build: %v, not a refusal", err)
			}
			want := repo + c.text + c.key
			if !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "; the image is built only from a repository fetched without one") {
				t.Errorf("the refusal is %q, want it to start with %q", err, want)
			}
			if strings.Contains(err.Error()+out.String()+log.String(), secret) {
				t.Errorf("the value is shown: %q\n%s\n%s", err, out.String(), log.String())
			}
			if f.read("devcontainer.log") != "" || f.read("docker.log") != "" {
				t.Errorf("a program ran:\n%s\n%s", f.read("devcontainer.log"), f.read("docker.log"))
			}
		})
	}
}

// TestBuildRefusesAGitConfigGitDoesNotRead is a .git/config git refuses: refused
// before any program runs, and what git prints is not shown.
func TestBuildRefusesAGitConfigGitDoesNotRead(t *testing.T) {
	f := newFakes(t)
	var out, log bytes.Buffer
	repo := repository(t, shopConfig)
	withGitConfig(t, repo, "[remote \"origin\"\n\turl = https://"+secret+"@example.com/\n")
	_, err := prebuild.Build(t.Context(), options(t, repo, &out, &log))
	var refusal *prebuild.RefusedError
	if !errors.As(err, &refusal) || err.Error() != repo+" has a git configuration git does not read, .git/config; the image is built only from a repository whose git configuration git reads" {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(out.String()+log.String(), secret) || f.read("devcontainer.log") != "" || f.read("docker.log") != "" {
		t.Errorf("a value is shown or a program ran:\n%s\n%s\n%s", out.String(), log.String(), f.read("docker.log"))
	}
}

// TestBuildTakesAGitConfigWithoutACredential is a .git/config as git fetch writes one,
// with a remote reached over ssh by a path and rewritten URLs that hold no credential,
// and a global git configuration with a credential helper, which the check does not
// read: built.
func TestBuildTakesAGitConfigWithoutACredential(t *testing.T) {
	newFakes(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[credential]\n\thelper = store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	repo := repository(t, shopConfig)
	withGitConfig(t, repo, `[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
[remote "origin"]
	url = https://example.com/acme/shop.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[remote "mirror"]
	url = git@example.com:acme/shop.git
	pushurl = ssh://git@example.com/acme/shop.git
[remote "backup"]
	url = git+ssh://git@example.com/acme/shop.git
	fetch = main:refs/remotes/backup/main
[user]
	email = dev@example.com
	name = Shop Developer
[submodule "lib"]
	url = https://example.com/acme/lib.git
[branch "main"]
	remote = origin
	merge = refs/heads/main
[url "https://example.com/"]
	insteadOf = git@example.com:
[http]
	sslVerify = true
`)
	// A branch named config is no configuration, in the repository or a submodule.
	writeFiles(t, repo, map[string]string{
		".git/refs/heads/config":             "0123456789abcdef0123456789abcdef01234567\n",
		".git/modules/lib/HEAD":              "ref: refs/heads/main\n",
		".git/modules/lib/config":            "[core]\n\tbare = false\n",
		".git/modules/lib/refs/heads/config": "0123456789abcdef0123456789abcdef01234567\n",
	})
	if _, err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

// TestBuildRunsGitFromThePathOfItsPrograms is a git on the PATH of the options'
// environment and not on this process's: the check runs that one, with hostgit's flags.
func TestBuildRunsGitFromThePathOfItsPrograms(t *testing.T) {
	newFakes(t)
	dir := t.TempDir()
	writeFake(t, filepath.Join(dir, "git"), `echo "$*" > `+dir+`/git.log
`)
	repo := repository(t, shopConfig)
	withGitConfig(t, repo, "[core]\n\tbare = false\n")
	o := options(t, repo, io.Discard, io.Discard)
	for i, kv := range o.Env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			o.Env[i] = "PATH=" + dir + string(os.PathListSeparator) + v
		}
	}
	if _, err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "git.log"))
	// hostgit puts its -c flags before every git command.
	flags := "-c core.hooksPath=/dev/null -c core.fsmonitor=false -c protocol.ext.allow=never -c diff.ignoreSubmodules=all -c submodule.recurse=false -c fetch.recurseSubmodules=false -c core.useReplaceRefs=false -c core.commitGraph=false "
	if want := flags + "config --file " + filepath.Join(repo, ".git", "config") + " --no-includes --list -z\n"; err != nil || string(data) != want {
		t.Errorf("the git of the options' PATH ran %q, %v; want %q", data, err, want)
	}
}

// writeFiles writes files under root, each path relative to it with slashes.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestBuildRefusesACredentialInEveryGitConfiguration is a credential for git in each
// configuration file the image takes beside .git/config: a submodule's, a nested
// submodule's, one under a folder of a submodule's name, one whose HEAD is a link, as git
// writes it with core.preferSymlinkRefs, config.worktree, and a submodule's and a
// worktree's config.worktree. Refused before any program runs, naming the file and never
// the value.
func TestBuildRefusesACredentialInEveryGitConfiguration(t *testing.T) {
	const head = "ref: refs/heads/main\n"
	token := "[http]\n\textraHeader = Authorization: bearer " + secret + "\n"
	clean := "[core]\n\tbare = false\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		shown string
		// links are links to write, each path and what it leads to.
		links map[string]string
	}{
		{"submodule", map[string]string{".git/config": clean, ".git/modules/lib/HEAD": head, ".git/modules/lib/config": token}, ".git/modules/lib/config", nil},
		{"nested submodule", map[string]string{".git/modules/lib/HEAD": head, ".git/modules/lib/config": clean, ".git/modules/lib/modules/deep/HEAD": head, ".git/modules/lib/modules/deep/config": token}, ".git/modules/lib/modules/deep/config", nil},
		{"submodule named with a folder", map[string]string{".git/modules/vendor/lib/HEAD": head, ".git/modules/vendor/lib/config": token}, ".git/modules/vendor/lib/config", nil},
		{"submodule whose HEAD is a link", map[string]string{".git/modules/lib/refs/heads/main": "0123456789abcdef0123456789abcdef01234567\n", ".git/modules/lib/config": token}, ".git/modules/lib/config", map[string]string{".git/modules/lib/HEAD": "refs/heads/main"}},
		{"config.worktree", map[string]string{".git/config": clean, ".git/config.worktree": token}, ".git/config.worktree", nil},
		{"a worktree's config.worktree", map[string]string{".git/worktrees/w/HEAD": head, ".git/worktrees/w/config.worktree": token}, ".git/worktrees/w/config.worktree", nil},
		{"submodule's config.worktree", map[string]string{".git/modules/lib/HEAD": head, ".git/modules/lib/config.worktree": token}, ".git/modules/lib/config.worktree", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakes(t)
			var out, log bytes.Buffer
			repo := repository(t, shopConfig)
			writeFiles(t, repo, c.files)
			for name, target := range c.links {
				if err := os.Symlink(target, filepath.Join(repo, filepath.FromSlash(name))); err != nil {
					t.Fatal(err)
				}
			}
			_, err := prebuild.Build(t.Context(), options(t, repo, &out, &log))
			var refusal *prebuild.RefusedError
			want := repo + " holds a credential for git in " + c.shown + ", http.extraheader; the image is built only from a repository fetched without one"
			if !errors.As(err, &refusal) || err.Error() != want {
				t.Fatalf("Build: %v\nwant %s", err, want)
			}
			if strings.Contains(out.String()+log.String(), secret) || f.read("devcontainer.log") != "" || f.read("docker.log") != "" {
				t.Errorf("a value is shown or a program ran:\n%s\n%s\n%s", out.String(), log.String(), f.read("docker.log"))
			}
		})
	}
}

// TestBuildFollowsAGitFile is a .git file with a gitdir: line, as a worktree or a
// submodule has. A git folder inside the repository, which the image takes, is checked;
// one outside it, which the image does not take, is not.
func TestBuildFollowsAGitFile(t *testing.T) {
	token := "[remote \"origin\"]\n\turl = https://" + secret + "@example.com/acme/shop.git\n"
	gitFile := func(t *testing.T, repo, line string) {
		t.Helper()
		if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, repo, map[string]string{".git": line})
	}
	t.Run("inside", func(t *testing.T) {
		f := newFakes(t)
		repo := repository(t, shopConfig)
		gitFile(t, repo, "gitdir: .gitdata\n")
		writeFiles(t, repo, map[string]string{".gitdata/HEAD": "ref: refs/heads/main\n", ".gitdata/config": token})
		var out bytes.Buffer
		_, err := prebuild.Build(t.Context(), options(t, repo, &out, io.Discard))
		want := repo + " holds a credential for git in .gitdata/config, remote.*.url; the image is built only from a repository fetched without one"
		var refusal *prebuild.RefusedError
		if !errors.As(err, &refusal) || err.Error() != want {
			t.Fatalf("Build: %v\nwant %s", err, want)
		}
		if strings.Contains(out.String(), secret) || f.read("devcontainer.log") != "" {
			t.Error("a value is shown or a program ran")
		}
	})
	t.Run("outside", func(t *testing.T) {
		newFakes(t)
		repo := repository(t, shopConfig)
		elsewhere := t.TempDir()
		writeFiles(t, elsewhere, map[string]string{"HEAD": "ref: refs/heads/main\n", "config": token})
		gitFile(t, repo, "gitdir: "+elsewhere+"\n")
		if _, err := prebuild.Build(t.Context(), options(t, repo, io.Discard, io.Discard)); err != nil {
			t.Fatalf("Build: %v", err)
		}
	})
}
