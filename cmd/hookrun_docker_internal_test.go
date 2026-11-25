//go:build unix

package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hookrun"
)

// hookTestImage is the image of [TestAHookRunsInARealContainer], which the engine has: docker run
// pulls nothing.
const hookTestImage = "public.ecr.aws/docker/library/alpine:3.22"

// TestAHookRunsInARealContainer runs hooks in containers of this machine's engine. A hook starts
// from its program on the mount, cannot write .git or a read target, and its edits to the checkout
// are undone. A hook past its limit is red and leaves no container qory-hook-<run id>. It runs only with
// QORY_TEST_DOCKER=1, docker on the PATH and the image [hookTestImage] on the engine.
func TestAHookRunsInARealContainer(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if err := exec.Command("docker", "image", "inspect", hookTestImage).Run(); err != nil {
		t.Skipf("the engine has no image %s", hookTestImage)
	}
	script := "#!/bin/sh\necho inside\ntouch .git/planted && echo wrote-git\n" +
		"cd .qory/repositories/acme/lib\necho hook > lib.go && echo wrote-lib\ntouch .git/planted && echo wrote-lib-git\ncd \"$OLDPWD\"\n" +
		"echo changed > README.md\necho new > added.txt\nexit 3\n"
	edit := checkHook(0, "edit.sh", "scripts/edit.sh")
	slow := checkHook(1, "slow.sh", "scripts/slow.sh")
	slow.TimeoutSeconds = 2
	hc := newHookCase(t, map[string]string{
		"README.md": "readme\n", "scripts/edit.sh": script, "scripts/slow.sh": "#!/bin/sh\nsleep 60\n",
	}, edit, slow)
	lib := readTarget(t, hc.checkout)
	runHookContainer = dockerGitContainer
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	hc.setup.runID = "hooktest-" + hex.EncodeToString(id)
	run := hc.setup.run.(hookFakeRun)
	run.image = hookTestImage
	hc.setup.run = run
	// Docker Desktop shares /private, not the link /var to it.
	if dir, err := filepath.EvalSymlinks(hc.checkout); err == nil {
		hc.setup.checkout = dir
	}
	h := hc.open(t)
	hc.end(t, h)

	r := hc.records(t)
	if len(r) != 2 || r[0].Verdict != hookrun.Red || r[0].ExitCode != 3 || r[1].Verdict != hookrun.Red || r[1].ExitCode != -1 {
		t.Fatalf("records %+v", r)
	}
	if !strings.Contains(r[1].Cause, "limit of 2 seconds") {
		t.Errorf("the slow hook's cause %q", r[1].Cause)
	}
	lines := strings.Join(hc.steps[0].lines, "")
	if !strings.Contains(lines, "inside\n") || strings.Contains(lines, "wrote-git") || strings.Contains(lines, "wrote-lib") {
		t.Errorf("the edit hook's lines %q", lines)
	}
	if _, err := os.Lstat(filepath.Join(hc.checkout, ".git", "planted")); err == nil {
		t.Error("the hook wrote .git/planted")
	}
	if got, _ := os.ReadFile(filepath.Join(lib, "lib.go")); string(got) != "package lib\n" {
		t.Errorf("lib.go of the read target is %q", got)
	}
	if _, err := os.Lstat(filepath.Join(lib, ".git", "planted")); err == nil {
		t.Error("the hook wrote .git/planted of the read target")
	}
	if got, _ := os.ReadFile(filepath.Join(hc.checkout, "README.md")); string(got) != "readme\n" {
		t.Errorf("README.md is %q", got)
	}
	if _, err := os.Lstat(filepath.Join(hc.checkout, "added.txt")); err == nil {
		t.Error("added.txt stayed")
	}
	out, err := exec.Command("docker", "ps", "--all", "--quiet", "--filter", "name=^qory-hook-"+hc.setup.runID+"$").Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("containers %q, %v, want none", out, err)
	}
}
