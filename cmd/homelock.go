package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/render"
)

// homeLockSuffix is appended to a home's path for its lock file, beside the home, so the
// compose that replaces the home folder leaves the lock alone.
const homeLockSuffix = ".lock"

// The lines of [homeInUse]: the first line is the fix, the second says which run holds the
// home.
const (
	textHomeInUse = "Wait for the run that uses this home to end, or set another --home or harness.home.\n" +
		"%s uses the home %s"
	textHomeLockFailed = "Make the folder of the home writable, or set another --home or harness.home, then run again.\n" +
		"Taking the lock on the home %s failed: %s"
	textHolderUnknown = "Another run"
)

// homeLockText is what a run writes into its home's lock file: its run id, when known, its
// process id and its start time, one per line.
func homeLockText(runID string) string {
	run := ""
	if runID != "" {
		run = "run " + runID + "\n"
	}
	return fmt.Sprintf("%spid %d\nstarted %s\n", run, os.Getpid(), time.Now().UTC().Format(time.RFC3339))
}

// holderOf is the phrase for the run that wrote text into a home's lock file, as
// "run <id>, pid <n>, started <time>"; "Another run" when the text is empty.
func holderOf(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		if line = integration.Printable(strings.TrimSpace(line)); line != "" {
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		return textHolderUnknown
	}
	phrase := strings.Join(parts, ", ")
	return strings.ToUpper(phrase[:1]) + phrase[1:]
}

// homeInUse is the error of a run that finds the home at home held by the run that wrote
// text into its lock file. It is no mistake in the input, so the exit status is 1.
func homeInUse(home, text string) error {
	return fmt.Errorf(textHomeInUse, holderOf(text), exports.Value(home, false))
}

// lockHomeAt is [lockHome] for the home of at. A home inside the checkout, .qory/harness,
// is kept out of git first, so the lock file that creates the qory directory shows as no
// untracked file when the run stops before its compose.
func lockHomeAt(at places, runID string) (*foragerdir.Lock, error) {
	if at.dir != "" {
		if err := render.Exclude(at.root, "/"+checkout.Dir); err != nil {
			return nil, err
		}
	}
	return lockHome(at.home, runID)
}

// lockHome takes the lock on the composed home at home for the run runID, before the
// compose replaces the home, and holds it until Release. A second run of the same home
// stops with [homeInUse] instead of composing over the home the first run reads, and of
// deleting its copy of inputs.json. The lock is a flock on home+".lock", beside the home
// and not in it; the file stays after the run, and the flock, not the file, is the lock.
// A home inside a link is left to the compose, which stops the run there.
func lockHome(home, runID string) (*foragerdir.Lock, error) {
	dir := filepath.Dir(home)
	if info, err := os.Lstat(dir); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil
	}
	path := home + homeLockSuffix
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf(textHomeLockFailed, exports.Value(home, false), shownError(err))
	}
	lock, err := foragerdir.LockFileText(path, homeLockText(runID))
	switch {
	case errors.Is(err, foragerdir.ErrLocked):
		return nil, homeInUse(home, foragerdir.LockText(path))
	case err != nil:
		return nil, fmt.Errorf(textHomeLockFailed, exports.Value(home, false), shownError(err))
	}
	return lock, nil
}
