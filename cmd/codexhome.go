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
// is the run's working folder, at or below checkout, and each folder that a -C or --cd
// argument in args, the run's Codex arguments, names: each gets untrusted entries at its
// path and under /workspace, as given and with its links resolved, see
// [startDirsInWall] and [codex.WalledRun.WorkDirs]. A run whose
// access is read, readOnly, gets parts.readRules in rules/read-access.rules.
func walledCodexHome(root, harnessHome, checkout, workDir string, args []string, readOnly bool, parts codexParts) ([]wall.Mount, error) {
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
	// The working folder gets the entries of -C . beside those of the arguments.
	workDirs := startDirsInWall(checkout, workDir, append([]string{"."}, codex.StartDirs(args)...))
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

// startDirsInWall are the paths in the wall of dirs, the values of a walled Codex run's
// -C and --cd arguments, for a run whose checkout is checkout and whose working folder
// is workDir. The wall shows checkout at its path, or at /workspace with the working
// folder at the same path below it. For each of those two paths, each value is resolved
// as Codex resolves it (codex-rs at main 17a0e6f6, not the 0.167.2 tag): a relative
// value against the working folder, with . and .. taken by name and no link followed
// (utils/absolute-path/src/absolutize.rs). Codex looks up the trust of that path with
// every link resolved first, and then of the path as it is
// (config/src/project_trust.rs, from_paths). codex exec also loads a first config for
// the value with every link resolved, a link before a .. that follows it
// (exec/src/lib.rs, canonicalize_existing_preserving_symlinks). All three are returned;
// see [resolveInWall]. A path at or below checkout's path or /workspace is returned at
// the other one too. A path outside both stays as it resolves, also when it escapes the
// checkout.
func startDirsInWall(checkout, workDir string, dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	if workDir == "" {
		workDir = checkout
	}
	type form struct{ at, cwd string }
	forms := []form{{checkout, filepath.ToSlash(workDir)}}
	rel, err := filepath.Rel(realPath(checkout), realPath(workDir))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && checkout != workspaceInWall {
		forms = append(forms, form{workspaceInWall, path.Join(workspaceInWall, filepath.ToSlash(rel))})
	}
	var found []string
	for _, f := range forms {
		for _, d := range dirs {
			d = filepath.ToSlash(d)
			if !path.IsAbs(d) {
				d = f.cwd + "/" + d
			}
			lexical := path.Clean(d)
			found = append(found, lexical, resolveInWall(lexical, f.at, checkout), resolveInWall(d, f.at, checkout))
		}
	}
	var out []string
	for _, p := range found {
		out = append(out, p)
		for _, f := range forms {
			if r, ok := belowInWall(f.at, p); ok {
				for _, g := range forms {
					out = append(out, path.Join(g.at, r))
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resolveInWall is p, an absolute path in a wall that shows checkout at at, with
// each link resolved, as the operating system resolves it, a link before a .. that
// follows it. A link at or below at is read from checkout on this machine. A name
// outside at is taken as it is: the wall has it from the image, which qory does not
// read. After [maxLinkHops] links, the rest of p is taken as it is.
func resolveInWall(p, at, checkout string) string {
	todo := strings.Split(p, "/")
	cur := "/"
	for hops := 0; len(todo) > 0; {
		name := todo[0]
		todo = todo[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			cur = path.Dir(cur)
			continue
		}
		next := path.Join(cur, name)
		cur = next
		r, ok := belowInWall(at, next)
		if !ok || r == "." || hops >= maxLinkHops {
			continue
		}
		target, err := os.Readlink(filepath.Join(checkout, filepath.FromSlash(r)))
		if err != nil {
			continue
		}
		hops++
		cur = path.Dir(next)
		if path.IsAbs(target) {
			cur = "/"
		}
		todo = append(strings.Split(filepath.ToSlash(target), "/"), todo...)
	}
	return cur
}

// belowInWall is p relative to dir, both clean absolute paths in the wall, and true when
// p is dir or below it.
func belowInWall(dir, p string) (string, bool) {
	if p == dir {
		return ".", true
	}
	r, ok := strings.CutPrefix(p, strings.TrimSuffix(dir, "/")+"/")
	return r, ok
}

// removeCodexHome removes the folders of [walledCodexHome] from root, the folder of a
// run's copy, when the run ends, also when the copy stays, and before each later Codex
// session of a run. report prints a line of qory run for each folder it could not remove;
// stayed and err are the first such folder and its error, "" and nil when every folder went.
func removeCodexHome(root string, report func(string)) (stayed string, err error) {
	for _, name := range []string{codexHomeName, codexFixedName} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if removeErr := runcopy.RemoveFolder(path); removeErr != nil {
			report(fmt.Sprintf(textCopyNoRemove, exports.Value(path, false), exports.HideError(removeErr)))
			if err == nil {
				stayed, err = path, removeErr
			}
		}
	}
	return stayed, err
}
