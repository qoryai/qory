package preparation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
)

// localFetch are the options of the one git of [ScopeClone] that fetches from the
// repository's folder: a local path, which [protocols] turns off for every other git.
var localFetch = []string{"-c", "protocol.file.allow=always"}

// ScopeClone makes a fresh clone of the commits of r, the repository in folder, at clone,
// a path that does not exist yet: a new repository with no template, so no hook, into
// which git fetches head alone, by its id, with no tag and no ref, and checks it out
// detached. It holds no ref of folder, such as a remote branch that the agent wrote, and
// its configuration is git init's. It stops when head does not descend from r's
// [WorkRepository.Commit]. base is the environment git starts from, as [Push] takes it.
// An error is the cause alone.
func ScopeClone(ctx context.Context, base []string, folder, clone string, r WorkRepository, head string) error {
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	git := stepGit(noRemote, "")
	format, err := git(ctx, folder, "rev-parse", "--show-object-format")
	if err != nil {
		return cloneFailed("the read of the object format of the repository", err)
	}
	if err := os.Mkdir(clone, 0o700); err != nil {
		return fmt.Errorf("making the folder of the clone failed: %w", exports.HideError(err))
	}
	if _, err := git(ctx, clone, "init", "--quiet", "--template=", "--object-format="+strings.TrimSpace(format)); err != nil {
		return cloneFailed("git init", err)
	}
	ceiling := "GIT_CEILING_DIRECTORIES=" + filepath.Dir(clone)
	fetch := hostRunnerOf(noRemote, slices.Concat(noMaintenance, localFetch), "", []string{ceiling})
	if _, err := fetch(ctx, clone, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--update-shallow", "--", folder, head); err != nil {
		return cloneFailed("the fetch of commit "+shortCommit(head), err)
	}
	if _, err := git(ctx, clone, "checkout", "--quiet", "--detach", head); err != nil {
		return cloneFailed("the checkout of commit "+shortCommit(head), err)
	}
	// The fetch brings head's history alone, so a start that is not in it is no commit here.
	notAfter := fmt.Errorf("commit %s does not descend from commit %s, where the session started", shortCommit(head), shortCommit(r.Commit))
	if _, err := git(ctx, clone, "cat-file", "-e", r.Commit+"^{commit}"); err != nil {
		return notAfter
	}
	_, err = git(ctx, clone, "merge-base", "--is-ancestor", r.Commit, head)
	if ge := (*gitError)(nil); errors.As(err, &ge) && ge.exit == 1 {
		return notAfter
	}
	if err != nil {
		return cloneFailed("the check that commit "+shortCommit(head)+" descends from commit "+shortCommit(r.Commit), err)
	}
	return nil
}

// cloneFailed is the cause of a step of [ScopeClone] that failed with err.
func cloneFailed(step string, err error) error {
	return fmt.Errorf("%s failed: %s", step, integration.Printable(exports.HideURLError(err).Error()))
}

// ChangedPath is the first path outside .qory/ that a commit from start, not included, to
// head changes in the repository at dir, oldest commit first, with that commit; both ""
// when no commit changes such a path. A merge counts by its change from each parent, so
// a merge that takes a file back to an older version counts too. A commit with no parent
// is an error.
func ChangedPath(ctx context.Context, base []string, dir, start, head string) (commit, path string, err error) {
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	git := stepGit(noRemote, "")
	// Each commit, once per parent for a merge, is \x01<commit>\x02<parents>\x00, then a
	// status letter and a path for each path it changes, each ended by \x00. A path is read
	// by its place after a status, so no byte in a path can pass for a commit.
	out, err := git(ctx, dir, "-c", "core.quotePath=false", "log", "--reverse", "-m", "--no-renames", "--name-status", "-z",
		"--format=%x01%H%x02%P", start+".."+head, "--")
	if err != nil {
		return "", "", cloneFailed("the list of the commits and their paths", err)
	}
	if out == "" {
		return "", "", nil
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	for i := 0; i < len(fields); i++ {
		field := strings.TrimPrefix(fields[i], "\n")
		if header, ok := strings.CutPrefix(field, "\x01"); ok {
			c, parents, _ := strings.Cut(header, "\x02")
			if parents == "" {
				return "", "", fmt.Errorf("commit %s has no parent, so qory cannot list the paths it changes", shortCommit(c))
			}
			commit = c
			continue
		}
		if commit == "" || len(field) != 1 || i+1 == len(fields) {
			return "", "", fmt.Errorf("the list of the paths of the commits has an unknown line: %s", exports.Value(fields[i], false))
		}
		i++
		if !strings.HasPrefix(fields[i], ".qory/") {
			return commit, fields[i], nil
		}
	}
	return "", "", nil
}
