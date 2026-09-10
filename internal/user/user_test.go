package user_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/user"
)

// TestName covers the order Name asks in: git's user.name first, then gh's active
// github.com account, then nothing. Every case sets HOME and the git configuration to
// temporary paths, so none of them reads the machine's own identity.
func TestName(t *testing.T) {
	cases := []struct {
		name      string
		gitConfig string
		hosts     string
		want      string
	}{
		{
			name:      "the first word of the git user name",
			gitConfig: "[user]\n\tname = Ada Lovelace\n",
			want:      "Ada",
		},
		{
			name:      "a one-word git user name",
			gitConfig: "[user]\n\tname = Ada\n",
			want:      "Ada",
		},
		{
			name:      "surrounding space is trimmed",
			gitConfig: "[user]\n\tname = \"  Ada Lovelace  \"\n",
			want:      "Ada",
		},
		{
			name:      "git wins over gh",
			gitConfig: "[user]\n\tname = Ada Lovelace\n",
			hosts:     "github.com:\n    user: octocat\n",
			want:      "Ada",
		},
		{
			name:  "falls back to the GitHub user",
			hosts: "github.com:\n    user: octocat\n",
			want:  "octocat",
		},
		{
			name:      "an empty git user name falls back to the GitHub user",
			gitConfig: "[user]\n\tname = \"\"\n",
			hosts:     "github.com:\n    user: octocat\n",
			want:      "octocat",
		},
		{
			name:  "another host in the gh file is ignored",
			hosts: "git.example.com:\n    user: octocat\n",
			want:  "",
		},
		{
			name:  "a malformed gh file yields nothing",
			hosts: "github.com: [\n",
			want:  "",
		},
		{
			name: "neither git nor gh knows a name",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("GH_CONFIG_DIR", "")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			config := filepath.Join(home, "gitconfig")
			if err := os.WriteFile(config, []byte(c.gitConfig), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", config)
			// A working directory outside any checkout keeps a repository's own
			// user.name out of the answer.
			t.Chdir(home)
			if c.hosts != "" {
				dir := filepath.Join(home, ".config", "gh")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(c.hosts), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := user.Name(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestNameReadsTheGhConfigDirectory covers GH_CONFIG_DIR taking the place of the home
// directory when gh's configuration lives elsewhere.
func TestNameReadsTheGhConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Chdir(home)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	hosts := "github.com:\n    user: octocat\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(hosts), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := user.Name(); got != "octocat" {
		t.Errorf("got %q, want octocat", got)
	}
}

// TestNameIsEmptyWithoutAHomeDirectory covers the case where gh's file cannot be located at
// all: no GH_CONFIG_DIR and no home directory.
func TestNameIsEmptyWithoutAHomeDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("HOME", "")
	t.Chdir(dir)
	if got := user.Name(); got != "" {
		t.Errorf("got %q, want the empty string", got)
	}
}
