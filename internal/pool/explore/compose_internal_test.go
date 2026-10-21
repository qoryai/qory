package explore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The variables that run TestComposeReal, on a developer's machine alone: the path of a
// local checkout of qory-github, and a commit or release tag of qory-github to fetch.
const (
	envHarnessPath = "QORY_POOL_TEST_HARNESS_PATH"
	envHarnessRef  = "QORY_POOL_TEST_HARNESS_REF"
)

// TestComposeReal composes the explore-an-item harness with qory built from this tree,
// from a local checkout and from the public repository at a ref, each when its variable
// is set. Everything it makes is under the test's temporary directory.
func TestComposeReal(t *testing.T) {
	path, ref := os.Getenv(envHarnessPath), os.Getenv(envHarnessRef)
	if path == "" && ref == "" {
		t.Skipf("set %s or %s to compose the harness for real", envHarnessPath, envHarnessRef)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	qory := filepath.Join(root, "qory")
	build := exec.Command("go", "build", "-o", qory, "github.com/qoryai/qory")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	for _, c := range []struct {
		name, path, ref string
	}{{"path", path, ""}, {"ref", "", ref}} {
		t.Run(c.name, func(t *testing.T) {
			if c.path == "" && c.ref == "" {
				t.Skip("not set")
			}
			s := Settings{
				State: filepath.Join(root, c.name, "state"), HarnessPath: c.path, HarnessRef: c.ref,
				RunConfig: filepath.Join(root, c.name, "runconfig"), Qory: qory, Log: func(line string) { t.Log(line) },
			}
			if err := os.MkdirAll(s.RunConfig, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			dir, err := prepare(ctx, s, Run{RunID: "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f", Inputs: []byte(`{"version":1}`)})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "homes"))
			if err != nil || len(entries) == 0 {
				t.Errorf("no harness was composed in homes: %v", err)
			}
		})
	}
}
