package prebuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/pool/explore"
)

// TestDockerIsFoundOnTheCleanedPATH finds docker, when Docker is not set or is a name
// alone, on the PATH every command gets: a file of that name in an entry in the pool's
// runs folder, or linked into it, is passed over for the one in the next entry. Docker,
// when it is a path, is taken as it is. No file is run.
func TestDockerIsFoundOnTheCleanedPATH(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{State: filepath.Join(root, "state")}
	inRuns, kept := filepath.Join(explore.RunsDir(s.State), "0192f0c1", "bin"), filepath.Join(root, "bin")
	for _, d := range []string{inRuns, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "docker"), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(inRuns, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{filepath.Join(root, "link"), inRuns, kept}, string(os.PathListSeparator)))
	if got, err := s.docker(); err != nil || got != filepath.Join(kept, "docker") {
		t.Errorf("docker is %q, %v; want %q", got, err, filepath.Join(kept, "docker"))
	}
	s.Docker = "docker"
	if got, err := s.docker(); err != nil || got != filepath.Join(kept, "docker") {
		t.Errorf("docker by its name is %q, %v; want %q", got, err, filepath.Join(kept, "docker"))
	}
	s.Docker = "/opt/docker/bin/docker"
	if got, err := s.docker(); err != nil || got != s.Docker {
		t.Errorf("docker is %q, %v; want %q", got, err, s.Docker)
	}
}
