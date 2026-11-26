// Package hookrun is what qory run needs to run the workflow hooks of a session: it reads
// hooks.json from the session's home, reads each hook's program from git at a commit into a
// file outside the checkout, takes a snapshot of the checkout before a hook runs and restores
// the checkout from it after, and writes the hooks' records. The hooks run in a container that
// qory run starts; this package starts none.
package hookrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/reviewround"
)

// The places of a hook point in its workflow's session, as hooks.json has them in point_at.
const (
	// PointEnd is a point at the end of a session: its hooks run once the agent exited.
	PointEnd = "end"
	// PointStart is a point at the start of a session: its hooks run before the agent starts.
	PointStart = "start"
)

// Folder is the folder of the run directory with the hooks' programs and records.
const Folder = "hooks"

// The verdicts of a hook.
const (
	Green = "green"
	Red   = "red"
)

// Read is the workflow hooks of the home, in the order hooks.json lists them: none when the
// home has no hooks.json.
func Read(home string) ([]compose.Hook, error) {
	data, err := os.ReadFile(filepath.Join(home, render.HooksFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hooks render.Hooks
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, fmt.Errorf("%s is not a hooks file: %w", render.HooksFile, err)
	}
	return hooks.Hooks, nil
}

// At is the hooks of workflow at the place pointAt, in their order.
func At(hooks []compose.Hook, workflow, pointAt string) []compose.Hook {
	var at []compose.Hook
	for _, h := range hooks {
		if h.Workflow == workflow && h.PointAt == pointAt {
			at = append(at, h)
		}
	}
	return at
}

// Record is the record of one hook of a session: its name, place and kind, its verdict, the
// cause of a red verdict, and its findings. Reviewer and Findings have the form of a
// reviewer's entry of a review report, [reviewround.ReviewerFindings], see [Record.Entry].
// The point of each finding of a red required hook goes into the changes-requested verdict
// of the session.
type Record struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	Verdict  string `json:"verdict"`
	// Cause is why the hook is red, one line; "" for a green hook.
	Cause string `json:"cause,omitempty"`
	// ExitCode is the program's exit status, -1 for a hook with none.
	ExitCode int                   `json:"exit_code"`
	Reviewer string                `json:"reviewer"`
	Findings []reviewround.Finding `json:"findings"`
}

// Entry is the record's Reviewer and Findings in the form of a review report's entry.
func (r Record) Entry() reviewround.ReviewerFindings {
	return reviewround.ReviewerFindings{Reviewer: r.Reviewer, Findings: r.Findings}
}

// Reviewer is the reviewer's name of the hook at index of harness.hooks: hook-<index>, of the
// form of a reviewer's name, which a hook's name need not have.
func Reviewer(index int) string { return "hook-" + strconv.Itoa(index) }

// Records is the file of a session's hook records.
type Records struct {
	Session int      `json:"session"`
	Hooks   []Record `json:"hooks"`
}

// Write writes the records of session n to the file path, mode 0600.
func Write(path string, n int, records []Record) error {
	data, err := json.MarshalIndent(Records{Session: n, Hooks: records}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
