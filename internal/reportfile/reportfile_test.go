package reportfile

import (
	"errors"
	"io/fs"
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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.json"), []byte(`{"reviewer":"code-reviewer","findings":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := Read(root, "report.json", MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"reviewer":"code-reviewer","findings":[]}` {
		t.Errorf("read %q", data)
	}
	// A file of exactly the limit is read.
	if _, err := Read(root, "report.json", int64(len(data))); err != nil {
		t.Errorf("a file of the limit's size: %v", err)
	}
}

func TestReadReadsBelowARootUnderALink(t *testing.T) {
	// The root is below a link, as t.TempDir() is below /var on macOS: Read checks no folder
	// above the root.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real", "root", "round"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "linked", "root")
	if err := os.WriteFile(filepath.Join(root, "round", "report.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := Read(root, filepath.Join("round", "report.json"), MaxBytes); err != nil || string(data) != "{}" {
		t.Errorf("a file below a root under a link: %q, %v", data, err)
	}
}

func TestReadReadsNoLinkBelowTheRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside", "target.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"report.json": filepath.Join(outside, "target.json"),
		"near.json":   filepath.Join("inside", "target.json"),
		"round":       outside,
		"inner-round": "inside",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	// A link at the file, to a file outside the root or inside it, and a link at a folder
	// between the root and the file, are each refused as a link.
	for _, rel := range []string{"report.json", "near.json", filepath.Join("round", "target.json"), filepath.Join("inner-round", "target.json")} {
		data, err := Read(root, rel, MaxBytes)
		if got := causeOf(t, err); got != Link || data != nil {
			t.Errorf("%s: cause %d, data %q", rel, got, data)
		}
	}
	// The root itself as a link is a link too.
	linkedRoot := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if got := causeOf(t, second(Read(linkedRoot, filepath.Join("inside", "target.json"), MaxBytes))); got != Link {
		t.Errorf("a root that is a link: cause %d", got)
	}
}

func TestReadDoesNotWaitOnAFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "report.json"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- second(Read(root, "report.json", MaxBytes)) }()
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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.json"), []byte(strings.Repeat("x", 11)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := second(Read(root, "report.json", 10))
	if got := causeOf(t, err); got != TooLarge {
		t.Fatalf("an oversize file: cause %d", got)
	}
	if !strings.Contains(err.Error(), "has more than 10 bytes") {
		t.Errorf("the error is %q", err)
	}
}

func TestReadNamesAMissingFileAndABadPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := causeOf(t, second(Read(root, "report.json", MaxBytes))); got != Missing {
		t.Errorf("a missing file: cause %d", got)
	}
	if got := causeOf(t, second(Read(filepath.Join(root, "none"), "report.json", MaxBytes))); got != Missing {
		t.Errorf("a missing root: cause %d", got)
	}
	if got := causeOf(t, second(Read(root, "folder", MaxBytes))); got != NotRegular {
		t.Errorf("a folder: cause %d", got)
	}
	for _, c := range [][2]string{
		{root, filepath.Join("..", filepath.Base(root), "report.json")}, {root, ".."}, {root, "../x/report.json"},
		{root, "folder/../report.json"}, {root, "./report.json"}, {root, "/etc/passwd"}, {root, ""}, {root, "."},
		{"", "report.json"}, {"reports", "report.json"}, {root + "/", "report.json"},
	} {
		if got := causeOf(t, second(Read(c[0], c[1], MaxBytes))); got != BadPath {
			t.Errorf("the root %q and the path %q: cause %d", c[0], c[1], got)
		}
	}
}

func TestErrorStartsWithItsFix(t *testing.T) {
	err := &Error{Path: "/reports/report.json", Cause: Link, Limit: MaxBytes}
	lines := strings.Split(err.Error(), "\n")
	if lines[0] != "Write the report as one regular file of at most 1048576 bytes." || len(lines[0]) > 100 {
		t.Errorf("line 1 is %q", lines[0])
	}
	if lines[1] != `"/reports/report.json" is a link, or is below a folder that is a link.` {
		t.Errorf("line 2 is %q", lines[1])
	}
	unreadable := &Error{Path: "/r", Cause: Unreadable, Err: errors.New("permission denied\nmore")}
	if unreadable.What() != "cannot be read: permission denied" {
		t.Errorf("an unreadable file: %q", unreadable.What())
	}
	withPath := &Error{Path: "/r", Cause: Unreadable, Err: &fs.PathError{Op: "open", Path: "/home/person/reports/report.json", Err: fs.ErrPermission}}
	if withPath.What() != "cannot be read: permission denied" {
		t.Errorf("an unreadable file has the path in its cause: %q", withPath.What())
	}
}

func TestReadReadsNoFileWithASecondHardLink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "report.json"), filepath.Join(root, "other.json")); err != nil {
		t.Skipf("link: %v", err)
	}
	err := second(Read(root, "report.json", MaxBytes))
	if got := causeOf(t, err); got != HardLink {
		t.Fatalf("a file with a second hard link: cause %d", got)
	}
	if !strings.HasSuffix(err.Error(), "has another hard link.") {
		t.Errorf("the error is %q", err)
	}
}

// second is the error of a call that returns data and an error.
func second(_ []byte, err error) error { return err }
