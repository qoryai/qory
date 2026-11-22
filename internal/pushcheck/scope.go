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
	"strconv"
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
// [FilesDir], as the user user: command's arguments, then --base base --artifacts
// [FilesDir]. The container is the checker's, see [Checker.Check]. The engine's failures
// are errors.
func (c *Checker) Scope(ctx context.Context, command []string, clone, snapshot, base, user string) (ScopeEnd, error) {
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	args := append(slices.Clone(command[1:]), "--base", base, "--artifacts", FilesDir)
	scoped := *c
	scoped.User = user
	// The harness's git writes objects, so the clone alone is writable: it is a fresh copy
	// that qory removes after the check.
	binds := []bind{{src: c.Home, dst: c.Home}, {src: clone, dst: WorkDir, writable: true},
		{src: snapshot, dst: FilesDir}}
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

// nobody is the uid:gid a scope check runs as when qory runs as root and the wall sets no
// other user than root.
const nobody = "65534:65534"

// ScopeUser is the uid:gid a scope check runs as, the owner of the clone it reads, since
// git stops on a repository of another user: see [scopeUser].
// shortcut: a rootless engine maps uids, so git's owner check there fails closed; replace
// it when qory supports such an engine for scope checks.
func (c *Checker) ScopeUser() string {
	return scopeUser(os.Getuid(), os.Getgid(), c.User)
}

// scopeUser is the user of a scope check for a process of uid and gid and the wall's user
// wall: uid:gid when uid is not 0; for root, wall when its uid is a number above 0, else
// 65534:65534, so the check never reads the agent's commits as root.
func scopeUser(uid, gid int, wall string) string {
	if uid != 0 {
		return fmt.Sprintf("%d:%d", uid, gid)
	}
	text, _, _ := strings.Cut(wall, ":")
	if id, err := strconv.Atoi(text); err == nil && id > 0 {
		return wall
	}
	return nobody
}

// Chown makes user, a uid:gid, the owner of each folder of dirs and everything in it, not
// through a link, so a scope check that runs as user owns the clone it reads.
func Chown(user string, dirs ...string) error {
	uidText, gidText, _ := strings.Cut(user, ":")
	uid, err := strconv.Atoi(uidText)
	if err != nil {
		return fmt.Errorf("the user %s has no numeric uid", exports.Value(user, true))
	}
	gid := uid
	if gidText != "" {
		if gid, err = strconv.Atoi(gidText); err != nil {
			return fmt.Errorf("the user %s has no numeric gid", exports.Value(user, true))
		}
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(path, uid, gid)
		})
		if err != nil {
			return fmt.Errorf("making %s the owner of the scope check's folder failed: %w", exports.Value(user, true), exports.HideError(err))
		}
	}
	return nil
}
