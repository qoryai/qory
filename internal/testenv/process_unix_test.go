//go:build unix

package testenv_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/qoryai/qory/internal/testenv"
)

// TestEndedTellsARunningChildFromAZombie checks [testenv.Ended]: a child that runs has not
// ended, and, where /proc has stat files, a child that exited and is not reaped yet has.
func TestEndedTellsARunningChildFromAZombie(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Wait() }()
	if testenv.Ended(pid) {
		t.Errorf("the running child %d has ended", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc/self/stat: kill(pid, 0) succeeds on a zombie here")
	}
	if !testenv.Gone(pid) {
		t.Errorf("the zombie %d has not ended", pid)
	}
}
