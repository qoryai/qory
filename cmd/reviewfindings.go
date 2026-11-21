package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/qoryai/qory/internal/reviewround"
)

// openFindingsMode is the mode of open-findings.json: the next implement-changes
// session reads it through a read-only mount, and the file has no secret.
const openFindingsMode = 0o644

// writeOpenFindings writes open, the open findings of the last review report, as
// open-findings.json at path, for the next implement-changes session. It writes a new
// file, then renames it over the old one, so a session that reads the old file never
// reads half of the new.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func writeOpenFindings(path string, open reviewround.OpenFindings) error {
	data, err := open.JSON()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".open-findings-*")
	if err != nil {
		return fmt.Errorf("Give qory a folder that it can write.\n%w", err)
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(openFindingsMode)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("Free space for open-findings.json.\n%s: %w", strconv.Quote(path), err)
	}
	return nil
}
