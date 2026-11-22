package runtimeinstall

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// The time limits of one request of the install. The context of the install, which in
// qory run ends with the run's setup, applies on top; within it, these keep a registry
// that stops sending from holding the install until the setup's end.
const (
	// dialTimeout is the most time to open a connection.
	dialTimeout = 30 * time.Second
	// tlsHandshakeTimeout is the most time for a TLS handshake.
	tlsHandshakeTimeout = 30 * time.Second
	// responseHeaderTimeout is the most time from the request to the response's headers.
	// It is longer than the 30 seconds that a registry waits for its cache before it
	// returns 503.
	responseHeaderTimeout = 60 * time.Second
)

// stallLimit is the most time that a read of a response body waits for its next byte;
// a test shortens it.
var stallLimit = 60 * time.Second

// limitTransport sets the time limits of t, a transport that the install uses.
func limitTransport(t *http.Transport) {
	t.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = tlsHandshakeTimeout
	t.ResponseHeaderTimeout = responseHeaderTimeout
}

// DirectClient is the client of a run that sends its requests directly, with no host
// proxy: the transport of net/http, which follows the machine's proxy variables, with
// the time limits of the install. It follows no redirect, as [ProxyClient]'s client does,
// so a request's Authorization never leaves the host that the request names.
func DirectClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	limitTransport(t)
	return &http.Client{Transport: t, CheckRedirect: noRedirect}
}

// noRedirect is a redirect policy that follows no redirect: the client returns the
// redirect's response.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// send sends req through client, and returns a response whose body fails with a
// [stalledError] when a read waits more than [stallLimit] for a byte. The request ends
// when the caller closes the body.
func send(client *http.Client, req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &stallBody{body: resp.Body, cancel: cancel, idle: stallLimit}
	return resp, nil
}

// stalledError is the error of a read of a response body that got no byte for idle.
type stalledError struct{ idle time.Duration }

func (e *stalledError) Error() string {
	return fmt.Sprintf("the response sent no byte for %s", e.idle)
}

// stallBody is a response body whose reads end with a [stalledError] after idle with no
// byte.
type stallBody struct {
	body    io.ReadCloser
	cancel  context.CancelFunc
	idle    time.Duration
	stalled atomic.Bool
}

// Read reads from the body, and ends the request when the read waits more than idle.
func (b *stallBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(b.idle, func() {
		b.stalled.Store(true)
		b.cancel()
	})
	n, err := b.body.Read(p)
	timer.Stop()
	if err != nil && err != io.EOF && b.stalled.Load() {
		err = &stalledError{idle: b.idle}
	}
	return n, err
}

// Close closes the body and ends the request.
func (b *stallBody) Close() error {
	err := b.body.Close()
	b.cancel()
	return err
}
