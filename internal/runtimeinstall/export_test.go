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

// SetRetryClock replaces the wait between two tries of the registry fetch with one that
// moves a fake clock on by its length and returns at once, with the context's error, and
// calls onWait with the wait's length, until the returned restore runs.
func SetRetryClock(onWait func(time.Duration)) (restore func()) {
	s, n := sleep, now
	var mu sync.Mutex
	clock := time.Now()
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
	return func() { sleep, now = s, n }
}

// CompareVersions is compareVersions.
var CompareVersions = compareVersions

// SetStallLimit sets the most time that a read of a response body waits for its next
// byte, until the returned restore runs.
func SetStallLimit(limit time.Duration) (restore func()) {
	old := stallLimit
	stallLimit = limit
	return func() { stallLimit = old }
}

// SetCurrentUID makes the install take uid for the user of this process, until the
// returned restore runs.
func SetCurrentUID(uid int) (restore func()) {
	before := currentUID
	currentUID = func() int { return uid }
	return func() { currentUID = before }
}
