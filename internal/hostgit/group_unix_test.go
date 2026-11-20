//go:build unix

package hostgit_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
)

// TestGroupStopsAHelperAtTheContextsEnd is a command that starts a child, as git starts
// git-remote-https, and waits for it: at the context's end the child stops with the
// command, and with [hostgit.StopGroup] a child the command left behind stops too.
func TestGroupStopsAHelperAtTheContextsEnd(t *testing.T) {
	for name, script := range map[string]string{
		"cancelled": "sleep 60 & echo $!; wait",
		"left":      "sleep 60 >/dev/null 2>&1 & echo $!",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", script)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.WaitDelay = time.Second
			hostgit.Group(cmd)
			_ = cmd.Run()
			hostgit.StopGroup(cmd)
			pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
			if err != nil {
				t.Fatalf("the child's pid: %q", out.String())
			}
			if !gone(pid) {
				syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("the child %d still runs", pid)
			}
		})
	}
}

// gone reports whether process pid ends within 2 seconds, see [ended].
func gone(pid int) bool {
	for range 40 {
		if ended(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ended reports whether pid has no process, or only a zombie's entry. On Linux, the new
// parent of an orphan, such as PID 1 of a container, may leave it a zombie for a while,
// and kill(pid, 0) succeeds on a zombie; /proc/<pid>/stat has its state, Z. Where
// /proc has no stat file, as on macOS, kill(pid, 0) alone decides.
func ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the program's name, in parentheses that may contain ") ".
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return false
	}
	return stat[i+2] == 'Z' || stat[i+2] == 'X'
}

// TestEndedTellsARunningChildFromAZombie checks [ended]: a child that runs has not
// ended, and, where /proc has stat files, a child that exited and is not reaped yet has.
func TestEndedTellsARunningChildFromAZombie(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Wait() }()
	if ended(pid) {
		t.Errorf("the running child %d has ended", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc/self/stat: kill(pid, 0) succeeds on a zombie here")
	}
	if !gone(pid) {
		t.Errorf("the zombie %d has not ended", pid)
	}
}
