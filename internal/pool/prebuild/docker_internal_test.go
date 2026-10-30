package prebuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDockerIsFoundOnTheStepsPATH finds docker for the push on the PATH of the step's
// environment, the session's, not on this process's: an entry with no docker, or one
// that is not executable, is passed over. A name that no entry has stays a name.
func TestDockerIsFoundOnTheStepsPATH(t *testing.T) {
	root := t.TempDir()
	empty, notExec, kept := filepath.Join(root, "empty"), filepath.Join(root, "noexec"), filepath.Join(root, "bin")
	for _, d := range []string{empty, notExec, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(notExec, "docker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kept, "docker"), nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "elsewhere"))
	env := []string{"HOME=/home/x", "PATH=" + strings.Join([]string{empty, notExec, kept}, string(os.PathListSeparator))}
	if got := lookIn("docker", env); got != filepath.Join(kept, "docker") {
		t.Errorf("docker is %q, want %q", got, filepath.Join(kept, "docker"))
	}
	if got := lookIn("podman", env); got != "podman" {
		t.Errorf("podman is %q, want the name", got)
	}
}
