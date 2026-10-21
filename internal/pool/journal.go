package pool

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/qoryai/forager/accesskey"
)

// JournalFile is the journal's name in the pool's folder.
const JournalFile = "dispatches"

// maxJournal is the most the journal may hold: far more than the dispatches a pool
// runs at once.
const maxJournal = 1 << 20

// The phases of a dispatch, as the journal holds them: claimed, its job's start
// recorded with dispatched, its handover about to be sent, its run handed over, and its
// run started. From handover on, the run may be handed over, so a replay releases it
// with retry false.
const (
	PhaseClaimed    = "claimed"
	PhaseDispatched = "dispatched"
	PhaseHandover   = "handover"
	PhaseHandedOver = "handed_over"
	PhaseStarted    = "started"
)

var (
	dispatchIDShape = regexp.MustCompile(`^dp_[0-9a-hjkmnp-tv-z]{16}$`)
	runIDShape      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// stepShape is a step, as the provisioner contract writes one.
	stepShape = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
)

// Entry is one live dispatch in the journal: its ids, its step and its phase. It
// holds no run key and no inputs.
type Entry struct {
	DispatchID, RunID, Step, Phase string
}

// line is e as the journal writes it.
func (e Entry) line() string {
	return e.DispatchID + " " + e.RunID + " " + e.Step + " " + e.Phase + "\n"
}

// valid reports whether each member of e has its shape.
func (e Entry) valid() bool {
	return dispatchIDShape.MatchString(e.DispatchID) && runIDShape.MatchString(e.RunID) && stepShape.MatchString(e.Step) &&
		slices.Contains([]string{PhaseClaimed, PhaseDispatched, PhaseHandover, PhaseHandedOver, PhaseStarted}, e.Phase)
}

// journal is the file <pool's folder>/dispatches, mode 0600: one line per live
// dispatch, its dispatch id, run id, step and phase, in the order of their claims. Each
// change writes the whole file anew, synced, and renames it into place, so a crash
// leaves the file before the change or after it.
type journal struct {
	path    string
	mu      sync.Mutex
	entries []Entry
}

// openJournal reads the journal in dir. A journal that is not there is empty; one that
// is not a regular file of at most a mebibyte, or whose lines are not each a dispatch
// id, a run id, a step and a phase, once per dispatch, is refused.
func openJournal(dir string) (*journal, error) {
	j := &journal{path: filepath.Join(dir, JournalFile)}
	info, err := os.Lstat(j.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return j, nil
	case err != nil:
		return nil, fmt.Errorf("%s: the journal of the pool's dispatches could not be read: %w", j.path, err)
	case !info.Mode().IsRegular() || info.Size() > maxJournal:
		return nil, fmt.Errorf("%s is not a journal of the pool's dispatches: it is not a regular file of at most 1 MiB, so no run is claimed", j.path)
	}
	f, err := os.Open(j.path)
	if err != nil {
		return nil, fmt.Errorf("%s: the journal of the pool's dispatches could not be read: %w", j.path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxJournal+1))
	if err != nil {
		return nil, fmt.Errorf("%s: the journal of the pool's dispatches could not be read: %w", j.path, err)
	}
	if len(b) > maxJournal {
		return nil, fmt.Errorf("%s is not a journal of the pool's dispatches: it is not a regular file of at most 1 MiB, so no run is claimed", j.path)
	}
	for n := 1; len(b) > 0; n++ {
		i := bytes.IndexByte(b, '\n')
		var fields []string
		if i >= 0 {
			fields = strings.Split(string(b[:i]), " ")
			b = b[i+1:]
		}
		var e Entry
		if len(fields) == 4 {
			e = Entry{fields[0], fields[1], fields[2], fields[3]}
		}
		if !e.valid() || slices.ContainsFunc(j.entries, func(x Entry) bool { return x.DispatchID == e.DispatchID }) {
			return nil, fmt.Errorf("%s is not a journal of the pool's dispatches: its line %d is not a dispatch id, a run id, a step and a phase, once per dispatch, so no run is claimed", j.path, n)
		}
		j.entries = append(j.entries, e)
	}
	return j, nil
}

// list is the journal's entries.
func (j *journal) list() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries)
}

// set writes e into the journal, in place of the entry of its dispatch, or after the
// others. On an error the journal is as it was.
func (j *journal) set(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := slices.Clone(j.entries)
	if i := slices.IndexFunc(next, func(x Entry) bool { return x.DispatchID == e.DispatchID }); i >= 0 {
		next[i] = e
	} else {
		next = append(next, e)
	}
	if err := j.write(next); err != nil {
		return err
	}
	j.entries = next
	return nil
}

// remove takes the dispatch id out of the journal.
func (j *journal) remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := slices.DeleteFunc(slices.Clone(j.entries), func(x Entry) bool { return x.DispatchID == id })
	if len(next) == len(j.entries) {
		return nil
	}
	if err := j.write(next); err != nil {
		return err
	}
	j.entries = next
	return nil
}

// write writes entries to a new file beside the journal, mode 0600, syncs it, renames
// it into place and syncs the folder.
func (j *journal) write(entries []Entry) error {
	var b strings.Builder
	for _, e := range entries {
		if !e.valid() {
			return fmt.Errorf("the dispatch %s cannot be written to the journal", accesskey.ShownName(e.DispatchID, false))
		}
		b.WriteString(e.line())
	}
	tmp := j.path + ".new"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(b.String())
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, j.path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
