package pushcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/render"
)

// LoadScope reads the scope checks of the home at home, by plan type, from
// [render.ScopeChecksFile]; nil when the home has no such file, as a harness whose modules
// declare no scope check. A file that cannot be read, or a check with no absolute path of
// a program, is an error, the cause alone.
func LoadScope(home string) (map[string]compose.ScopeCheck, error) {
	file := filepath.Join(home, render.ScopeChecksFile)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the scope checks in %s could not be read: %w", exports.Value(file, false), exports.HideError(err))
	}
	var checks render.ScopeChecks
	if err := json.Unmarshal(data, &checks); err != nil {
		return nil, fmt.Errorf("%s is not JSON in the form the compose writes: %w", exports.Value(file, false), err)
	}
	for typ, c := range checks.ScopeChecks {
		if len(c.Command) == 0 || !filepath.IsAbs(c.Command[0]) {
			return nil, fmt.Errorf("the scope check for %s in %s has no absolute path of a program", exports.Value(typ, true), exports.Value(file, false))
		}
	}
	return checks.ScopeChecks, nil
}

// maxScopeLines is the most lines of a scope check that its end keeps.
const maxScopeLines = 50

// ScopeEnd is how a scope check ended: TimedOut for a program that ran past the limit,
// NotStarted the cause of a program that did not start, and otherwise the program's exit
// code with its lines, as [Checker.Check] reads a check's findings, at most 50.
type ScopeEnd struct {
	TimedOut   bool
	NotStarted string
	ExitCode   int
	Lines      []string
}

// Scope runs command, the scope check of a plan type with its program's absolute path in
// the home, within [Budget], on clone, a fresh clone of a session's commits, as the
// writable working folder, with snapshot, the session's launch snapshot, read-only at
// [FilesDir], as the user user: command's arguments, then --inputs [Checker.Inputs] when the
// checker has it, then --base base --artifacts [FilesDir]. The container mounts a copy of
// [Checker.InputsData] read-only at [Checker.Inputs]. The container is the checker's, see
// [Checker.Check]. The engine's failures are errors.
func (c *Checker) Scope(ctx context.Context, command []string, clone, snapshot, base, user string) (ScopeEnd, error) {
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	args := slices.Clone(command[1:])
	if c.Inputs != "" {
		args = append(args, "--inputs", c.Inputs)
	}
	args = append(args, "--base", base, "--artifacts", FilesDir)
	scoped := *c
	scoped.User = user
	// The harness's git writes objects, so the clone alone is writable: it is a fresh copy
	// that qory removes after the check.
	binds := []bind{{src: c.Home, dst: c.Home}, {src: clone, dst: WorkDir, writable: true},
		{src: snapshot, dst: FilesDir}}
	if c.Inputs != "" {
		dir, err := c.tempFolder()
		if err != nil {
			return ScopeEnd{}, err
		}
		defer os.RemoveAll(dir)
		copied, err := copyInputs(c.Inputs, c.InputsData, dir)
		if err != nil {
			return ScopeEnd{}, err
		}
		binds = append(binds, bind{src: copied, dst: c.Inputs})
	}
	end, err := scoped.run(ctx, container{program: command[0], args: args, binds: binds, workdir: WorkDir})
	switch {
	case err != nil:
		return ScopeEnd{}, err
	case end.timedOut:
		return ScopeEnd{TimedOut: true}, nil
	case end.startErr != "":
		return ScopeEnd{NotStarted: strings.TrimSuffix(c.mask(cleanLine(end.startErr)), ".")}, nil
	case end.exitCode == 0:
		return ScopeEnd{}, nil
	}
	lines := findings(end.exitCode, end.stdout, end.stderr, c.mask)
	if len(lines) > maxScopeLines {
		lines = lines[:maxScopeLines]
	}
	return ScopeEnd{ExitCode: end.exitCode, Lines: lines}, nil
}

// ScopeUser is the uid:gid a scope check runs as, this process's: the owner of the clone it
// reads, since git stops on a repository of another user. A walled run of qory as root stops
// before its first container, so it is never root's.
// shortcut: a rootless engine maps uids, so git's owner check there fails closed; replace
// it when qory supports such an engine for scope checks.
func ScopeUser() string { return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) }

// ScopeInputs returns data, the run's inputs.json, as the scope check of the repository in
// folder reads it in its container: qory.start_artifacts is files, where the container has the
// launch snapshot, and the path of the entry of qory.start_commits for folder is work, where
// it has the clone. The harness's scope verb ends with exit 3 when --artifacts is not
// qory.start_artifacts, or when no entry's path is the checkout it runs in. Every other member
// and entry keeps its value. Data that is not an inputs.json with those members comes back
// unchanged, and the check refuses it.
func ScopeInputs(data []byte, folder, work, files string) []byte {
	var top, qory map[string]json.RawMessage
	var entries []map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || json.Unmarshal(top["qory"], &qory) != nil || json.Unmarshal(qory["start_commits"], &entries) != nil {
		return data
	}
	for _, e := range entries {
		var path string
		if json.Unmarshal(e["path"], &path) == nil && sameFolder(path, folder) {
			e["path"], _ = json.Marshal(work)
		}
	}
	var artifacts string
	if json.Unmarshal(qory["start_artifacts"], &artifacts) == nil && artifacts != "" {
		qory["start_artifacts"], _ = json.Marshal(files)
	}
	var err error
	if qory["start_commits"], err = json.Marshal(entries); err != nil {
		return data
	}
	if top["qory"], err = json.Marshal(qory); err != nil {
		return data
	}
	out, err := json.Marshal(top)
	if err != nil {
		return data
	}
	return out
}

// sameFolder is whether a and b are one folder, by path or after the links in them.
func sameFolder(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
