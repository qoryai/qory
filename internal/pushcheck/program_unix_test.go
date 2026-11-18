//go:build unix

package pushcheck_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pushcheck"
)

// TestAnotherUserGetsTheFilesGiven is a run's user that is not this process's, with a process
// that can give files to it (root): each folder, the socket and each file of the work and net
// folders belong to the run's user, with modes 0700 and 0600. A test run by another user skips
// it.
func TestAnotherUserGetsTheFilesGiven(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("giving a file to another user needs root")
	}
	owners := map[string]int{}
	modes := map[string]fs.FileMode{}
	f := newProgramFixture(t, func(_ *programFixture, w, n string) (string, string, int) {
		for name, p := range map[string]string{"work": w, "net": n, "socket": filepath.Join(n, "proxy.sock"), "file": filepath.Join(w, "submission.json"), "ca": filepath.Join(n, "ca.pem"), "env": filepath.Join(filepath.Dir(w), "env")} {
			if info, err := os.Lstat(p); err == nil {
				if st, ok := info.Sys().(*syscall.Stat_t); ok {
					owners[name] = int(st.Uid)
				}
				modes[name] = info.Mode().Perm()
			}
		}
		return "", "", 0
	})
	f.c.User = "4242:4242"
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"work", "net", "socket", "file", "ca"} {
		if owners[name] != 4242 {
			t.Errorf("%s belongs to user %d, want 4242", name, owners[name])
		}
		if modes[name]&0o077 != 0 {
			t.Errorf("%s has mode %o, want no access for group and others", name, modes[name])
		}
	}
	if owners["env"] == 4242 || modes["env"] != 0o600 {
		t.Errorf("env belongs to user %d with mode %o, want this process's user and 600", owners["env"], modes["env"])
	}
}

// TestTheEnvFileNeverHasALooserModeThanItsFinalOne is a process whose umask is 0: the file
// that has the proxy's password is created with mode 0600, not made 0600 after it was
// created, and each other path has the mode it keeps.
func TestTheEnvFileNeverHasALooserModeThanItsFinalOne(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	modes := map[string]fs.FileMode{}
	f := newProgramFixture(t, modesSeen(modes))
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env", "file", "ca", "socket"} {
		if modes[name] != 0o600 {
			t.Errorf("%s has mode %o under umask 0, want 600", name, modes[name])
		}
	}
	for _, name := range []string{"root", "work", "net"} {
		if modes[name] != 0o700 {
			t.Errorf("%s has mode %o under umask 0, want 700", name, modes[name])
		}
	}
}

// TestAFifoAsTheFileDoesNotStopTheRun is a program that leaves a named pipe as
// submission.json: opening it would wait for a writer for good. The run ends, with no file and
// the cause that the file is not a regular file.
func TestAFifoAsTheFileDoesNotStopTheRun(t *testing.T) {
	f := newProgramFixture(t, func(_ *programFixture, w, _ string) (string, string, int) {
		p := filepath.Join(w, "submission.json")
		os.Remove(p)
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Error(err)
		}
		return "", "", 0
	})
	done := make(chan struct{})
	var end pushcheck.ProgramEnd
	var err error
	go func() {
		defer close(done)
		end, err = f.c.RunProgram(context.Background(), f.program("{}"))
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the run waits on the pipe")
	}
	if err != nil || end.Submission != nil || end.FileErr != "the file is not a regular file" {
		t.Errorf("the end is %q %q, %v, want no file and that the file is not a regular file", end.Submission, end.FileErr, err)
	}
}
