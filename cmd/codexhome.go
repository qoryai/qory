package cmd

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render/codex"
	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/stack"
)

// The folders of a walled Codex run in the run's folder, <user cache>/qory/runs/<run id>:
// the writable Codex home, and the files and folders the run binds read-only over it.
// They share no path with each other or with the copy, as Forager asks of a mount with a
// target.
const (
	codexHomeName  = "codex-home"
	codexFixedName = "codex-fixed"
)

// writeWalledHome writes the read-only parts of a walled Codex run's home; a test
// replaces it to fail partway.
var writeWalledHome = codex.WriteWalledHome

// withCodexHome is the fixed variables and the defaults of a walled Codex run, with
// CODEX_HOME at [codex.HomeInWall] among the fixed ones and in neither set otherwise. A
// fixed value wins over every other source, and the runtime's deny list leaves it in.
func withCodexHome(fixed, defaults []string) ([]string, []string) {
	other := func(kv string) bool { return strings.HasPrefix(kv, codex.HomeVariable+"=") }
	fixed = slices.DeleteFunc(slices.Clone(fixed), other)
	defaults = slices.DeleteFunc(slices.Clone(defaults), other)
	fixed = append(fixed, codex.HomeVariable+"="+codex.HomeInWall)
	slices.Sort(fixed)
	return fixed, defaults
}

// walledCodexHome makes the Codex home of a walled Codex run in root, the run's folder,
// and returns its mounts. The writable home is the new folder codex-home, bound at
// [codex.HomeInWall]: Codex writes its sessions, its temporary files and its state
// database there. [codex.WriteWalledHome] writes the rest into the new folder codex-fixed,
// from the runtime's directory of the composed home under harnessHome, and each part is
// bound read-only at its target, one name below [codex.HomeInWall]. checkout is the folder
// the run binds as its checkout, the copy or the person's checkout, at its path; the
// trust entries and the disabled skills of the checkout are written for that path and for
// /workspace, where a run whose policy names its image by its reference sees it. workDir
// is the run's working folder, at or below checkout: it gets an untrusted entry at its
// path, and at its path under /workspace when it is below checkout, see
// [codex.WalledRun.WorkDirs]. A run whose
// access is read, readOnly, gets parts.readRules in rules/read-access.rules.
func walledCodexHome(root, harnessHome, checkout, workDir string, readOnly bool, parts codexParts) ([]wall.Mount, error) {
	home := filepath.Join(root, codexHomeName)
	if err := os.Mkdir(home, 0o700); err != nil {
		return nil, err
	}
	access := stack.AccessWrite
	if readOnly {
		access = stack.AccessRead
	}
	projects := []string{checkout}
	if checkout != workspaceInWall {
		projects = append(projects, workspaceInWall)
	}
	var workDirs []string
	if workDir != "" && workDir != checkout {
		workDirs = append(workDirs, workDir)
		rel, err := filepath.Rel(realPath(checkout), realPath(workDir))
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && checkout != workspaceInWall {
			workDirs = append(workDirs, path.Join(workspaceInWall, filepath.ToSlash(rel)))
		}
	}
	binds, err := writeWalledHome(filepath.Join(harnessHome, codex.Runtime), filepath.Join(root, codexFixedName), codex.WalledRun{
		Projects:  projects,
		Checkouts: []string{checkout},
		WorkDirs:  workDirs,
		Skills:    parts.skills,
		Access:    access,
		ReadRules: parts.readRules,
		LinkRoots: parts.sourceDirs,
	})
	if err != nil {
		return nil, err
	}
	mounts := []wall.Mount{{Path: home, Target: codex.HomeInWall}}
	for _, b := range binds {
		mounts = append(mounts, wall.Mount{Path: b.Source, ReadOnly: true, Target: b.Target})
	}
	return mounts, nil
}

// removeCodexHome removes the folders of [walledCodexHome] from root, the folder of a
// run's copy, when the run ends, also when the copy stays. report prints a line of qory
// run.
func removeCodexHome(root string, report func(string)) {
	for _, name := range []string{codexHomeName, codexFixedName} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if err := runcopy.RemoveFolder(path); err != nil {
			report(fmt.Sprintf(textCopyNoRemove, exports.Value(path, false), exports.HideError(err)))
		}
	}
}
