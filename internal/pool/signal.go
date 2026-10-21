package pool

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// OnSignals is ctx, ended at the first SIGINT or SIGTERM the process gets: the pool
// stops, as [Pool.Run] says. At a second, exit is called at once with 128 and the
// signal's number, as a shell gives a process a signal ended; the journal holds what is
// left for the next start. stop stops listening.
func OnSignals(ctx context.Context, exit func(code int)) (_ context.Context, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-c:
			cancel()
		case <-done:
			return
		}
		select {
		case s := <-c:
			code := 1
			if n, ok := s.(syscall.Signal); ok {
				code = 128 + int(n)
			}
			exit(code)
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(c)
		close(done)
		cancel()
	}
}
