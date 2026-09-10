package cmd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestCollisionOutput composes the collision fixture from a checkout and checks that the
// output names what happened and prints the profile lines that fix it.
func TestCollisionOutput(t *testing.T) {
	src, err := filepath.Abs("../contracts/harness/v1/fixtures/collision-fails")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-R", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	if err := os.RemoveAll(filepath.Join(dir, "expected")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs([]string{"harness", "compose"})
	root.SetOut(&out)
	err = root.Execute()
	if !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("err = %v, want one marked reported", err)
	}
	if cmd.ExitCode(err) != cmd.ExitCollision {
		t.Errorf("exit code %d, want %d", cmd.ExitCode(err), cmd.ExitCollision)
	}
	for _, want := range []string{"skills/test is provided by 3 layers", "Fix", "- name: core", "- name: nextjs", "skills: [test]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "- name: team") {
		t.Errorf("the kept layer got an exclude:\n%s", out.String())
	}
}
