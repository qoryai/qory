package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qoryai/qory/internal/exports"
)

// The engine has one proxy setting, so while one run's engine proxy forwarder is up,
// another run's pull or push on the engine goes out on the first run's token, or that
// run's forwarder cannot listen at the address. The engine lock keeps one of them at a
// time on the machine: an exclusive flock of wall.engine_proxy_file itself, which every
// run with that engine proxy opens. A file replaced while a run holds the lock is a new
// file, which that run's lock does not cover.

// errEngineLocked is a flock of the engine proxy file that another open file holds.
var errEngineLocked = errors.New("the engine proxy file is locked")

// engineLockPoll is how often a run that waits for the engine lock tries it again; a
// test shortens it.
var engineLockPoll = 100 * time.Millisecond

// textEngineLockWait is the line of a run whose time limit passed while it waited for the
// engine lock of the engine proxy file %s, with the cause of the end after it.
const textEngineLockWait = "give the run a longer time limit, or start fewer runs at once: the run ended while another run's image pull held the lock of wall.engine_proxy_file %s: %v"

// textEngineLockStopped is the line of a run stopped while it waited for the engine lock
// of the engine proxy file %s, by a signal or the end of its dispatch, with the cause after
// it.
const textEngineLockStopped = "start the run again if it is still wanted: the run was stopped while another run's image pull held the lock of wall.engine_proxy_file %s: %v"

// lockEngine takes the engine lock of the engine proxy file at path: it opens the file
// as [readEngineProxy] does, with the same refusals, and takes an exclusive flock of the
// descriptor, trying again every [engineLockPoll] until ctx ends: [textEngineLockWait]
// when its deadline passed, [textEngineLockStopped] when it was cancelled. The function
// it returns releases the lock.
func lockEngine(ctx context.Context, path string) (func(), error) {
	f, _, err := openEngineProxy(path)
	if err != nil {
		return nil, err
	}
	for {
		err := flockEngine(f)
		switch {
		case err == nil:
			// Closing the descriptor releases its flock.
			return func() { f.Close() }, nil
		case !errors.Is(err, errEngineLocked):
			f.Close()
			return nil, fmt.Errorf("check wall.engine_proxy_file: qory cannot lock %s: %w", exports.Value(path, false), err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			text := textEngineLockStopped
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				text = textEngineLockWait
			}
			return nil, fmt.Errorf(text, exports.Value(path, false), context.Cause(ctx))
		case <-time.After(engineLockPoll):
		}
	}
}

// withEngineLock runs step, a pull on the engine, while it holds the engine lock
// of the engine proxy file at path, [lockEngine]; with no path, it runs step alone. An
// error of the lock is a [*runLineError], which ends with [codeUsed] for a run from a
// run code, fromCode.
func withEngineLock(ctx context.Context, path string, fromCode bool, step func() error) error {
	if path == "" {
		return step()
	}
	release, err := lockEngine(ctx, path)
	if err != nil {
		if fromCode {
			err = fmt.Errorf("%w%s", err, codeUsed)
		}
		return &runLineError{err}
	}
	defer release()
	return step()
}
