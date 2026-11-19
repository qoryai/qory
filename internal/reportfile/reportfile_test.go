package reportfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// causeOf is the cause of err, an [Error], and 0 for nil.
func causeOf(t *testing.T, err error) Cause {
	t.Helper()
	if err == nil {
		return 0
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the error %v is no *Error", err)
	}
	return e.Cause
}

func TestReadReadsARegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, []byte(`{"reviewer":"code-reviewer","findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := Read(path, MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"reviewer":"code-reviewer","findings":[]}` {
		t.Errorf("read %q", data)
	}
	// A file of exactly the limit is read.
	if _, err := Read(path, int64(len(data))); err != nil {
		t.Errorf("a file of the limit's size: %v", err)
	}
}

func TestReadReadsNoLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "report.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	data, err := Read(link, MaxBytes)
	if got := causeOf(t, err); got != Link || data != nil {
		t.Errorf("a link: cause %d, data %q", got, data)
	}
	// A link to a folder that has the report is a link too.
	reports := filepath.Join(dir, "reports")
	if err := os.Symlink(dir, reports); err != nil {
		t.Fatal(err)
	}
	if got := causeOf(t, second(Read(filepath.Join(reports, "target.json"), MaxBytes))); got != Link {
		t.Errorf("a file in a linked folder: cause %d", got)
	}
}

func TestReadDoesNotWaitOnAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- second(Read(path, MaxBytes)) }()
	select {
	case err := <-done:
		if got := causeOf(t, err); got != NotRegular {
			t.Errorf("a FIFO: cause %d", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read waited on the FIFO")
	}
}

func TestReadReadsNoOversizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 11)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := second(Read(path, 10))
	if got := causeOf(t, err); got != TooLarge {
		t.Fatalf("an oversize file: cause %d", got)
	}
	if !strings.Contains(err.Error(), "has more than 10 bytes") {
		t.Errorf("the error is %q", err)
	}
}

func TestReadNamesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	if got := causeOf(t, second(Read(filepath.Join(dir, "report.json"), MaxBytes))); got != Missing {
		t.Errorf("a missing file: cause %d", got)
	}
	if got := causeOf(t, second(Read(dir, MaxBytes))); got != NotRegular {
		t.Errorf("a folder: cause %d", got)
	}
	for _, bad := range []string{"", "report.json", dir + "/./report.json", dir + "/x/../report.json", dir + "/"} {
		if got := causeOf(t, second(Read(bad, MaxBytes))); got != BadPath {
			t.Errorf("the path %q: cause %d", bad, got)
		}
	}
}

func TestErrorStartsWithItsFix(t *testing.T) {
	err := &Error{Path: "/reports/report.json", Cause: Link, Limit: MaxBytes}
	lines := strings.Split(err.Error(), "\n")
	if lines[0] != "Write the report as one regular file of at most 1048576 bytes." || len(lines[0]) > 100 {
		t.Errorf("line 1 is %q", lines[0])
	}
	if lines[1] != `"/reports/report.json" is a link, or is in a folder that is a link.` {
		t.Errorf("line 2 is %q", lines[1])
	}
	unreadable := &Error{Path: "/r", Cause: Unreadable, Err: errors.New("permission denied\nmore")}
	if unreadable.What() != "cannot be read: permission denied" {
		t.Errorf("an unreadable file: %q", unreadable.What())
	}
}

// second is the error of a call that returns data and an error.
func second(_ []byte, err error) error { return err }
