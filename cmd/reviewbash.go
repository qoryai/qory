package cmd

import (
	"github.com/qoryai/qory/internal/render"
)

// reviewFolders are the folders of a review join that the session reads or writes beside
// the copy: inputs, the review-session folder of the inputs folder, and reports, the
// session's reports folder; and guard, the review guard of the session's runtime, see
// [render.ReviewGuard].
type reviewFolders struct {
	inputs, reports string
	guard           render.CommandGuard
}

// reviewRules is the settings of a review join, the JSON that the guard f.guard makes of
// object, the runtime's settings, see [render.CommandGuard]: the session reads
// [tempStoreDir], where artifact pull writes, the copy's commit folder root, f.inputs and
// f.reports, in that order, writes f.reports alone, and edits neither root nor
// [workspaceInWall]. The error of the guard stops the run.
func reviewRules(object map[string]any, shown, root, home string, f reviewFolders) ([]byte, error) {
	data, err := f.guard.ReviewSettings(object, shown, render.ReviewPlaces{
		Home:   home,
		Read:   []string{tempStoreDir, root, f.inputs, f.reports},
		Write:  f.reports,
		NoEdit: []string{root, workspaceInWall},
	})
	if err != nil {
		return nil, input(err)
	}
	return data, nil
}
