//go:build unix

package prebuild_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/prebuild"
)

// gitFirst puts dir first on the PATH of o's environment.
func gitFirst(o *prebuild.Options, dir string) {
	for i, kv := range o.Env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			o.Env[i] = "PATH=" + dir + string(os.PathListSeparator) + v
		}
	}
}

// ran reports whether the fake git that touches dir/ran ran.
func ran(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "ran"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// TestBuildRunsNoGitFromARecordedPlace puts a git first on the PATH of the options'
// environment, in a folder a walled run used: the check of the git configuration runs
// another git, and the build goes on.
func TestBuildRunsNoGitFromARecordedPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	places.Forget()
	t.Cleanup(places.Forget)
	newFakes(t)
	place := t.TempDir()
	if err := hostgit.Record(t.Context(), place, places.KindMount, "run-1"); err != nil {
		t.Fatal(err)
	}
	writeFake(t, filepath.Join(place, "git"), "touch "+place+"/ran\n")
	repo := repository(t, shopConfig)
	withGitConfig(t, repo, "[core]\n\tbare = false\n")
	o := options(t, repo, io.Discard, io.Discard)
	gitFirst(&o, place)
	if _, err := prebuild.Build(t.Context(), o); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran(t, place) {
		t.Error("the git in a place a walled run used ran")
	}
}

// TestBuildStopsOnAGitOlderThan236 is a git that prints 2.34.1 first on the PATH of the
// options' environment: the build stops with the line that names the version, and that
// git runs nothing but git version.
func TestBuildStopsOnAGitOlderThan236(t *testing.T) {
	newFakes(t)
	dir := t.TempDir()
	writeFake(t, filepath.Join(dir, "git"), `if [ "$1" = version ]; then echo "git version 2.34.1"; exit 0; fi
touch `+dir+`/ran
`)
	repo := repository(t, shopConfig)
	withGitConfig(t, repo, "[core]\n\tbare = false\n")
	o := options(t, repo, io.Discard, io.Discard)
	gitFirst(&o, dir)
	want := "Install git 2.36 or later. qory reads git's worktree list in a form that git 2.34.1 does not print."
	if _, err := prebuild.Build(t.Context(), o); err == nil || err.Error() != want {
		t.Errorf("Build: %v, want %q", err, want)
	}
	if ran(t, dir) {
		t.Error("git 2.34.1 ran more than git version")
	}
}
