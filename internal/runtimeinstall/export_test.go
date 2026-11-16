package runtimeinstall

import (
	"context"
	"sync"
	"time"
)

// SetLimits sets the most bytes of a tarball, of its program and of all it unpacks to,
// until the returned restore runs.
func SetLimits(tarball, program, unpacked int64) (restore func()) {
	t, p, u := maxTarball, maxProgram, maxUnpacked
	maxTarball, maxProgram, maxUnpacked = tarball, program, unpacked
	return func() { maxTarball, maxProgram, maxUnpacked = t, p, u }
}

// SetRetryClock makes the retries of the registry fetch stop after limit, and replaces
// their clock with one that each wait moves on by its length at once, calling onWait
// with it, until the returned restore runs.
func SetRetryClock(limit time.Duration, onWait func(time.Duration)) (restore func()) {
	l, n, s := retryLimit, now, sleep
	var mu sync.Mutex
	clock := time.Now()
	retryLimit = limit
	now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		clock = clock.Add(d)
		mu.Unlock()
		onWait(d)
		return ctx.Err()
	}
	return func() { retryLimit, now, sleep = l, n, s }
}
