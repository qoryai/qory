//go:build unix

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/testenv"
)

// helperPID reads the id of the background process that a program script wrote to file.
func helperPID(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("the helper's id is %q: %v", data, err)
	}
	if pid <= 1 {
		t.Fatalf("the helper's id is %d: kill(%d) would reach more than the helper", pid, pid)
	}
	return pid
}

// TestAProgramPastItsLimitStopsItsHelpers is a program that starts a helper in the background and
// waits: at the limit, the program and the helper are both gone.
func TestAProgramPastItsLimitStopsItsHelpers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pid")
	program := &submitProgram{command: []string{programScript(t, "sleep 60 &\necho $! > "+file+"\nwait\n")}, limit: 4 * time.Second}
	got, _ := runProgram(t, program, passedDocument())
	if string(got.Repositories[0].Draft) != `{"none":"program-timeout"}` {
		t.Errorf("the draft is %s", got.Repositories[0].Draft)
	}
	if pid := helperPID(t, file); !testenv.Gone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the helper %d runs after the limit", pid)
	}
}

// TestAProgramThatEndsLeavesNoHelper is a program that exits with a helper in the background: the
// helper is gone when the step ends.
func TestAProgramThatEndsLeavesNoHelper(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pid")
	program := &submitProgram{command: []string{programScript(t, "sleep 60 >/dev/null 2>&1 &\necho $! > "+file+"\nexit 0\n")}}
	runProgram(t, program, passedDocument())
	if pid := helperPID(t, file); !testenv.Gone(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the helper %d runs after the program ended", pid)
	}
}

// TestTheProgramStartsInANewSession is the process attribute that cuts the program off from the
// terminal, whether or not the test has one.
func TestTheProgramStartsInANewSession(t *testing.T) {
	cmd := exec.Command("true")
	inOwnGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("the attributes are %+v, want Setsid", cmd.SysProcAttr)
	}
}
