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
	// verify is the Verify guard of the runtime of a Verify session, whose settings get
	// [verifyRules], see [render.VerifyGuardOf]; nil for any other session.
	verify render.VerifyGuard
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

// verifyRules is the settings of a Verify session, the JSON that the guard f.verify makes
// of object, the runtime's settings, see [render.VerifyGuard]: the session reads and
// writes f.reports beside its checkout. The error of the guard stops the run.
func verifyRules(object map[string]any, shown string, f reviewFolders) ([]byte, error) {
	data, err := f.verify.VerifySettings(object, shown, f.reports)
	if err != nil {
		return nil, input(err)
	}
	return data, nil
}

// verifyFoldersOf is the folders of a Verify session on rt, the runtime named name, whose
// reports folder is reports. A runtime that inlines no settings gets none, and its
// session runs with no rules. A [render.SettingsInliner] with no [render.VerifyGuard]
// stops the session before it starts, with [noVerifyGuard], since its settings would
// get no rules for the reports folder.
func verifyFoldersOf(rt render.Runtime, name, reports string) (*reviewFolders, error) {
	if _, inlines := rt.(render.SettingsInliner); !inlines {
		return nil, nil
	}
	guard, ok := render.VerifyGuardOf(rt)
	if !ok {
		return nil, noVerifyGuard(name)
	}
	return &reviewFolders{reports: reports, verify: guard}, nil
}
