// Package explore contains what a run of the pool's runs folder needs on this machine:
// its run directory, <state>/pool/runs/<run_id>, mode 0700, the folder qory run keeps
// the run's record in (see [RecordDir]), and the removal of both.
package explore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/qoryai/qory/internal/exports"
)

// runIDShape is a run id: a UUID in the canonical lower-case form.
var runIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Dir is the run directory of the run runID: <state>/pool/runs/<runID>.
func Dir(state, runID string) string {
	return filepath.Join(RunsDir(state), runID)
}

// RunsDir is the pool's runs folder under the pool program's state directory state,
// <state>/pool/runs, which contains every run directory: where walled runs write.
func RunsDir(state string) string {
	return filepath.Join(state, "pool", "runs")
}

// RecordDir is the folder qory run keeps the record of the run runID in, when it runs
// in work with XDG_STATE_HOME set to state, an absolute directory:
// <state>/qory/runs/<checkout>-<hash>/<runID>, as qory run's documentation says it.
// <checkout> is work's name and <hash> the first 12 hex digits of the SHA-256 of its
// path, both with links resolved.
func RecordDir(state, work, runID string) (string, error) {
	if !filepath.IsAbs(state) {
		return "", fmt.Errorf("the run's state directory %s is not an absolute path", exports.Value(state, true))
	}
	real, err := filepath.EvalSymlinks(work)
	if err != nil {
		return "", err
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", err
	}
	return filepath.Join(state, "qory", "runs", checkoutFolder(real), runID), nil
}

// Remove removes the run directory of the run runID, and drops its place record.
func Remove(state, runID string) error {
	if !runIDShape.MatchString(runID) {
		return errors.New("the run id is not a UUID in the canonical lower-case form")
	}
	dir := Dir(state, runID)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	dropPlace(dir)
	return nil
}

// dropPlace drops the place record of the run directory dir, once it is removed. It
// is a variable so a test sees it called.
//
// TODO: drop it through internal/places when the host-git work carries that package
// onto integrate. Until then there is no place record, and nothing to drop.
var dropPlace = func(dir string) { _ = dir }

// checkoutFolder is the name of the folder of a checkout's records, for the checkout
// at real, an absolute path with links resolved: its name, "-", and the first 12 hex
// digits of the SHA-256 of the path.
func checkoutFolder(real string) string {
	sum := sha256.Sum256([]byte(real))
	return filepath.Base(real) + "-" + hex.EncodeToString(sum[:])[:12]
}
