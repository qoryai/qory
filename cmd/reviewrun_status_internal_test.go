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

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/statusfd"
)

// TestALedgerOfAnotherWriterThatFailsACheckPrintsTwoLines is a newest build ledger of
// another run with two files: the session starts with the since of unknown passes, and
// qory prints line 1 with the version number and line 2 with the check that the ledger
// fails, both on stderr, in the step's log and on the status descriptor. The same ledger
// of this run stops the session with that check and prints nothing.
func TestALedgerOfAnotherWriterThatFailsACheckPrintsTwoLines(t *testing.T) {
	const fd = 236
	r, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := unix.Dup2(int(pw.Fd()), fd); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	defer statusfd.Close()
	t.Setenv(statusfd.Env, strconv.Itoa(fd))
	var stderr bytes.Buffer
	statusfd.Setup(&stderr)
	step := &hookStep{}
	w := &stepLines{stderr: &stderr, step: step}

	v := &runapi.Version{ID: "av_7", Number: 7, Writer: runapi.Writer{Kind: "run", RunID: "run_old"},
		Files: []runapi.File{{Filename: "a.json", Size: 2}, {Filename: "b.json", Size: 2}}}
	f := &findingsRun{runID: "run_new"}
	since, err := f.earlierPasses(t.Context(), nil, v, w)
	if err != nil || len(since) != 1 || since[unknownPasses] != 0 {
		t.Fatalf("returned %v, %v; want the since of unknown passes", since, err)
	}
	const line1 = "qory run: this session posts no replies: build ledger version 7 fails qory's checks"
	if len(line1) > 100 {
		t.Errorf("line 1 has %d characters, want at most 100", len(line1))
	}
	want := line1 + "\nqory run: The newest build ledger has 2 files, and the harness writes one\n"
	if stderr.String() != want {
		t.Errorf("stderr got %q, want %q", stderr.String(), want)
	}
	if len(step.lines) != 2 {
		t.Errorf("the step's log got %q, want the two lines", step.lines)
	}

	f.runID = "run_old"
	stderr.Reset()
	if _, err := f.earlierPasses(t.Context(), nil, v, w); !hasStop(err, "The newest build ledger has 2 files, and the harness writes one, so the session did not start") || stderr.Len() != 0 {
		t.Errorf("a ledger of this run returned %v and printed %q, want the stop alone", err, stderr.String())
	}

	statusfd.Close()
	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	status, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(status) != want {
		t.Errorf("the status descriptor got %q, want %q", status, want)
	}
}
