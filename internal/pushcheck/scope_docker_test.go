package pushcheck_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
)

// TestARealScopeCheckReadsAReadOnlyClone runs a scope check in a container of this
// machine's engine, from the image QORY_TEST_GIT_IMAGE names, which has git: on a fresh
// clone of a commit past the start, mounted read-only, the program's git reads the commits
// as the clone's owner, ScopeUser, and writes neither the clone nor the snapshot. As
// another user, git stops on the clone's owner, which is why the check runs as its owner.
// It runs only with QORY_TEST_DOCKER=1, docker on the PATH and the image present.
func TestARealScopeCheckReadsAReadOnlyClone(t *testing.T) {
	image := os.Getenv("QORY_TEST_GIT_IMAGE")
	switch {
	case os.Getenv("QORY_TEST_DOCKER") != "1":
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	case image == "":
		t.Skip("QORY_TEST_GIT_IMAGE names no image with git")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("the engine does not hold %s", image)
	}
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	folder := filepath.Join(top, "work")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	git(folder, "init", "--quiet")
	git(folder, "commit", "--quiet", "--allow-empty", "-m", "start")
	start := git(folder, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(folder, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(folder, "add", "app.go")
	git(folder, "commit", "--quiet", "-m", "app")
	head := git(folder, "rev-parse", "HEAD")
	clone := filepath.Join(top, "clone")
	if err := preparation.ScopeClone(context.Background(), os.Environ(), folder, clone, preparation.WorkRepository{Commit: start, Start: start}, head); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(top, "snapshot")
	if err := os.Mkdir(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "index.json"), []byte(`{"artifacts":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(top, "home")
	program := filepath.Join(home, "modules", "core", "scope")
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -e\ntest \"$1 $3 $4\" = \"--base --artifacts " + pushcheck.FilesDir + "\"\n" +
		"git rev-parse HEAD\ngit status --porcelain\ngit merge-base --is-ancestor \"$2\" HEAD\n" +
		"git diff --name-only \"$2\" HEAD\ntest -f \"$4/index.json\"\n" +
		"! touch \"$4/x\" 2>/dev/null\n! touch x 2>/dev/null\nid -u\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &pushcheck.Checker{
		Home: home, Image: image, Label: "dev.qory.run=run-scope-check-test",
		Limits: wall.Limits{CPUs: "1", Memory: "256m"},
	}
	user := c.ScopeUser()
	end, err := c.Scope(context.Background(), []string{program}, clone, snapshot, start, user)
	if err != nil {
		t.Fatal(err)
	}
	if end.TimedOut || end.NotStarted != "" || end.ExitCode != 0 {
		t.Errorf("as the clone's owner %s: %+v", user, end)
	}
	if status := git(clone, "status", "--porcelain"); status != "" {
		t.Errorf("the check changed the clone:\n%s", status)
	}
	// Another user: git stops on the clone's owner.
	end, err = c.Scope(context.Background(), []string{program}, clone, snapshot, start, "4242:4242")
	if err != nil {
		t.Fatal(err)
	}
	if end.ExitCode == 0 || !strings.Contains(strings.Join(end.Lines, "\n"), "dubious ownership") {
		t.Logf("as uid 4242 the check got %+v; the engine maps the owner of a bind (such as a rootless engine or a desktop VM)", end)
	}
	out, err := exec.Command("docker", "ps", "--all", "--quiet", "--filter", "label=dev.qory.run=run-scope-check-test").Output()
	if err != nil || len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("containers of checks are left: %q, %v", out, err)
	}
}
