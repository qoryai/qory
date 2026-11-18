package relay_test

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/relay"
)

// shortDir is a folder with a path short enough for a unix socket, mode 0700.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// upper is a TCP server that answers each line it reads with the line in upper case, and
// counts the connections it took.
func upper(t *testing.T) (addr string, accepted func() int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	count := make(chan struct{}, 100)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			count <- struct{}{}
			go func() {
				defer c.Close()
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				for i := range buf[:n] {
					if buf[i] >= 'a' && buf[i] <= 'z' {
						buf[i] -= 'a' - 'A'
					}
				}
				c.Write(buf[:n])
			}()
		}
	}()
	return l.Addr().String(), func() int { return len(count) }
}

// request sends text over the socket and returns the answer.
func request(t *testing.T, path, text string) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(text)); err != nil {
		return "", err
	}
	got, err := io.ReadAll(c)
	return string(got), err
}

// TestTheRelayCarriesOneRequestToItsTarget is a request over the socket that reaches the
// target and brings its answer back.
func TestTheRelayCarriesOneRequestToItsTarget(t *testing.T) {
	addr, _ := upper(t)
	r, err := relay.Start(shortDir(t), addr, 2, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := request(t, r.Path, "connect\n")
	if err != nil || got != "CONNECT\n" {
		t.Errorf("the answer is %q, %v, want CONNECT", got, err)
	}
}

// TestTheSocketIsPrivate is the socket's mode, 0600, in the folder that the caller made.
func TestTheSocketIsPrivate(t *testing.T) {
	addr, _ := upper(t)
	dir := shortDir(t)
	r, err := relay.Start(dir, addr, 2, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info, err := os.Stat(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Errorf("the socket's mode is %v, want a socket with 0600", info.Mode())
	}
	if r.Path != filepath.Join(dir, relay.SocketName) {
		t.Errorf("the socket is at %s", r.Path)
	}
}

// TestTheRelayRefusesAConnectionOverTheLimit is a limit of 1: the first connection is
// carried, the second finds no socket.
func TestTheRelayRefusesAConnectionOverTheLimit(t *testing.T) {
	addr, accepted := upper(t)
	r, err := relay.Start(shortDir(t), addr, 1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got, err := request(t, r.Path, "one\n"); err != nil || got != "ONE\n" {
		t.Fatalf("the first request got %q, %v", got, err)
	}
	if got, err := request(t, r.Path, "two\n"); err == nil {
		t.Errorf("the second connection got %q, want a refusal", got)
	}
	if n := accepted(); n != 1 {
		t.Errorf("the target took %d connections, want 1", n)
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Errorf("the socket is still there after the limit: %v", err)
	}
}

// TestTheRelayRefusesAfterItIsClosed is a close that removes the socket and ends the
// connection that was open.
func TestTheRelayRefusesAfterItIsClosed(t *testing.T) {
	addr, _ := upper(t)
	r, err := relay.Start(shortDir(t), addr, 5, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	open, err := net.Dial("unix", r.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("the second Close = %v", err)
	}
	if _, err := net.Dial("unix", r.Path); err == nil {
		t.Error("a connection after the close was accepted")
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Errorf("the socket is still there: %v", err)
	}
	open.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(open); err != nil {
		t.Errorf("the open connection was not ended by the close: %v", err)
	}
}

// TestTheRelayDialsOnlyItsTarget is a target that is not reachable: the connection ends
// with no byte, and nothing else is dialed.
func TestTheRelayDialsOnlyItsTarget(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r, err := relay.Start(shortDir(t), addr, 2, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got, _ := request(t, r.Path, "x\n"); got != "" {
		t.Errorf("an unreachable target answered %q", got)
	}
}
