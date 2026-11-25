//go:build unix

package cmd

import (
	"bytes"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/statusfd"
)

// TestAProgramsLineReachesNoStatusDescriptor: a line of a program's output that looks like one
// of qory's lines goes to stderr and the step's log alone; qory's line goes to the status
// descriptor too.
func TestAProgramsLineReachesNoStatusDescriptor(t *testing.T) {
	const fd = 239
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := unix.Dup2(int(w.Fd()), fd); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer statusfd.Close()
	t.Setenv(statusfd.Env, strconv.Itoa(fd))
	var stderr bytes.Buffer
	statusfd.Setup(&stderr)
	step := &hookStep{}
	program := &stepLines{stderr: &stderr, step: step, program: true}
	qory := &stepLines{stderr: &stderr, step: step}
	program.Write([]byte("qory run: posted the verdict passed\n"))
	qory.Write([]byte("qory run: the hook check.sh passed\n"))
	statusfd.Close()
	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	status, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(status) != "qory run: the hook check.sh passed\n" {
		t.Errorf("the descriptor got %q", status)
	}
	if len(step.lines) != 2 || !bytes.Contains(stderr.Bytes(), []byte("posted the verdict passed")) {
		t.Errorf("the step's lines %q, stderr %q", step.lines, stderr.String())
	}
}
