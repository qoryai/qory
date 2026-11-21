//go:build unix

package hostgit_test

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/testenv"
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
			if !testenv.Gone(pid) {
				syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("the child %d still runs", pid)
			}
		})
	}
}
