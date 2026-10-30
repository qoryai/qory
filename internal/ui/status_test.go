//go:build unix

package ui_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/statusfd"
	"github.com/qoryai/qory/internal/ui"
)

// TestStatusLinesReachTheDescriptor is a UI on stderr after qory run opened its status
// descriptor: the lines of Success, Warn and Fail, continuations included, reach the
// descriptor as well as stderr, and a title, a field and prose do not. A UI on another
// writer, such as io.Discard, writes nothing to the descriptor.
func TestStatusLinesReachTheDescriptor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	const fd = 238
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := unix.Dup2(int(w.Fd()), fd); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Setenv(statusfd.Env, strconv.Itoa(fd))
	var buf bytes.Buffer
	statusfd.Setup(&buf)
	defer statusfd.Close()

	hidden := plain(t, &bytes.Buffer{})
	hidden.Success("on another writer")
	ui.New(io.Discard).Warn("on io.Discard")
	u := plain(t, &buf)
	u.Title("qory", "run")
	u.Success("claude exited %d", 0)
	u.Fields([][2]string{{"record", "/tmp/run"}})
	u.Text("prose")
	u.Warn("a warning")
	u.Fail(errors.New("the first line\nthe second line"))
	statusfd.Close()

	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := "✓ claude exited 0\n⚠ a warning\n✗ the first line\n  the second line\n"; string(got) != want {
		t.Errorf("the descriptor got %q, want %q", got, want)
	}
	if want := "✓ claude exited 0\n  record  /tmp/run\n  prose\n⚠ a warning\n✗ the first line\n  the second line\n"; !bytes.HasSuffix(buf.Bytes(), []byte(want)) {
		t.Errorf("the writer got %q", buf.String())
	}
}
