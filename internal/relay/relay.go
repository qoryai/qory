// Package relay joins a unix socket to one TCP address for the submit program's container.
// That container has no network, so the program reaches the run's push proxy only through
// the socket that the relay listens on in a folder that the container mounts.
//
// Who can reach the relay: the folder is made by the caller, mode 0700 below a folder that is
// mode 0700 too, and the socket in it has the mode the caller gives: 0600 when the program that
// connects runs as the caller's user or the caller gives the socket to it, and a wider mode
// only below a folder that no other user opens. The container mounts the folder, so the
// container is the only other place that reaches the socket, and it has no network.
//
// What the relay carries: the bytes of each connection, both ways, to the one address it
// was started with. A client that closes its writing side after its request, as a half-close,
// still reads the reply: each direction ends when its copy ends, whatever the other does, and the connections close when both
// have ended or the relay closes. That is the push proxy's address, so the program's requests carry the
// proxy's password. The relay reads, checks and adds nothing: every limit on what a request
// may reach, the push credential's list of paths and hosts, stays with the push proxy.
//
// What the relay cannot do: dial any address but the one it was started with, accept more
// connections than its limit, or outlive [Relay.Close]. A listener that has accepted its
// limit closes at once, so no later connection reaches it.
package relay

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// SocketName is the name of the socket in the folder.
const SocketName = "proxy.sock"

// Relay is a listener on a unix socket that forwards each connection to one address.
type Relay struct {
	// Path is the socket's path.
	Path string

	listener net.Listener
	target   string
	limit    int

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	accepts int
	closed  bool
	done    sync.WaitGroup
}

// Start listens on the socket [SocketName] in dir, a folder that only this user can open,
// and forwards the first limit connections to target, a host:port. The socket has mode, 0600
// when the program that connects runs as this user. Further connections are never
// accepted: the listener closes after the limit-th.
func Start(dir, target string, limit int, mode os.FileMode) (*Relay, error) {
	path := filepath.Join(dir, SocketName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		listener.Close()
		return nil, err
	}
	r := &Relay{Path: path, listener: listener, target: target, limit: limit, conns: map[net.Conn]struct{}{}}
	r.done.Add(1)
	go r.accept()
	return r, nil
}

// accept takes connections until the limit or the close.
func (r *Relay) accept() {
	defer r.done.Done()
	for {
		in, err := r.listener.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.accepts++
		last := r.accepts >= r.limit
		if r.closed {
			r.mu.Unlock()
			in.Close()
			return
		}
		r.conns[in] = struct{}{}
		r.done.Add(1)
		r.mu.Unlock()
		go r.forward(in)
		if last {
			// The listener goes with its socket file; the connection taken last goes on.
			r.listener.Close()
			return
		}
	}
}

// forward copies between in and a new connection to the target until both directions end.
func (r *Relay) forward(in net.Conn) {
	defer r.done.Done()
	defer r.forget(in)
	out, err := net.Dial("tcp", r.target)
	if err != nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		out.Close()
		return
	}
	r.conns[out] = struct{}{}
	r.mu.Unlock()
	defer r.forget(out)
	ended := make(chan struct{}, 2)
	// pipe copies src to dst, and then closes the writing side of dst, so the peer of dst
	// reads the end of the data while the other direction goes on. A connection that cannot
	// half-close closes whole.
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if half, ok := dst.(interface{ CloseWrite() error }); ok {
			half.CloseWrite()
		} else {
			dst.Close()
		}
		ended <- struct{}{}
	}
	go pipe(out, in)
	go pipe(in, out)
	// Both directions end before the connections close; [Relay.Close] ends a direction that
	// has no end by closing every connection.
	<-ended
	<-ended
}

// forget closes c and drops it from the open connections.
func (r *Relay) forget(c net.Conn) {
	c.Close()
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

// Close stops the listener, closes every open connection, removes the socket and waits for
// the relay's goroutines. It is safe to call twice.
func (r *Relay) Close() error {
	r.mu.Lock()
	r.closed = true
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	err := r.listener.Close()
	r.done.Wait()
	if rm := os.Remove(r.Path); rm != nil && !errors.Is(rm, os.ErrNotExist) && err == nil {
		err = rm
	}
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}
