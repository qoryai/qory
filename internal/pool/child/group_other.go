//go:build !unix

package child

import (
	"errors"
	"os"
	"os/exec"
)

// group leaves cmd as it is on a system without sessions.
func group(*exec.Cmd) {}

// kill kills cmd on a system without process groups.
func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

// SignalEach kills each process id of ids on a system without sessions, when kill is
// set; without kill it sends nothing, since such a system has no SIGTERM to send.
func SignalEach(ids []int, _ int, kill bool) {
	if !kill {
		return
	}
	for _, id := range ids {
		if p, err := os.FindProcess(id); err == nil {
			_ = p.Kill()
		}
	}
}

// Session is -1 on a system without sessions: no id is a session's.
func Session() int { return -1 }

// errNoSessions is the error of each call that reads sessions or starts.
var errNoSessions = errors.New("this system has no sessions")

// StartStamp reads no start on a system without sessions.
func StartStamp(int) (string, error) { return "", errNoSessions }

// CheckLeader fails on a system without sessions, so no session is signalled.
func CheckLeader(int, string) error { return errNoSessions }

// HasOpen lists no process on a system without sessions.
func HasOpen([]int, os.FileInfo) []int { return nil }

// OpenFilesRead is false on a system without sessions.
const OpenFilesRead = false

// SessionMembers fails on a system without sessions.
func SessionMembers(int) ([]int, error) { return nil, errNoSessions }
