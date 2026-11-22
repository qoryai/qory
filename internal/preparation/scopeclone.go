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
// when no commit changes such a path. A merge counts by its change from its first parent.
func ChangedPath(ctx context.Context, base []string, dir, start, head string) (commit, path string, err error) {
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	git := stepGit(noRemote, "")
	out, err := git(ctx, dir, "rev-list", "--reverse", start+".."+head)
	if err != nil {
		return "", "", cloneFailed("the list of the commits", err)
	}
	for _, c := range strings.Fields(out) {
		changed, err := git(ctx, dir, "-c", "core.quotePath=false", "diff-tree", "-r", "-z", "--no-renames", "--name-only", c+"^1", c)
		if err != nil {
			return "", "", cloneFailed("the list of the paths of commit "+shortCommit(c), err)
		}
		for _, path := range strings.Split(changed, "\x00") {
			if path != "" && !strings.HasPrefix(path, ".qory/") {
				return c, path, nil
			}
		}
	}
	return "", "", nil
}
