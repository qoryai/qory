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
