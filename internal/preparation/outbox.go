package preparation

import (
	"context"
	"fmt"
	"strings"
)

// outboxDir is the folder of the checkout where a session writes its outputs. Only qory
// reads it, and no commit may have a path under it: the checkout's .git/info/exclude
// keeps it out of a commit of git add -A and git commit -a, and [Push] stops on a commit that
// has a path there anyway, as git add -f makes one.
const outboxDir = ".qory/outbox"

// textOutboxCommitted is the error of a push whose commits have a path under
// [outboxDir]: line 1 is the fix, then the cause and the values on indented lines.
const textOutboxCommitted = "qory run: Keep " + outboxDir + "/ out of the commits of this run; qory pushed nothing.\n  A commit of this run has a path under " + outboxDir + "/.\n  Repository %s.\n  Commit %s.\n  Path %s."

// OutboxCommitted is the error of a local run, whose commits are in the repository at dir
// with the path path on system: the check of [Push] on the commits from start, not included, to
// head, included.
func OutboxCommitted(ctx context.Context, dir, system, path, start, head string) error {
	return outboxCommitted(ctx, git, dir, system, WorkRepository{Path: path}, start, head, false)
}

// outboxCommitted checks the commits from start, not included, to head, included, of the
// repository at dir, merges included, and returns the refusal for the first commit that has
// a path under [outboxDir]; nil for none. A commit that adds the path and a later one that
// removes it still count: the file stays in the history that a push sends. fromCode is a run
// from a run code, whose failed list has [textListFailedFromCode].
func outboxCommitted(ctx context.Context, git gitRunner, dir, system string, r WorkRepository, start, head string, fromCode bool) error {
	out, err := git(ctx, dir, "rev-list", "--reverse", start+".."+head)
	if err != nil {
		return listFailed(fromCode, r, start, head, err)
	}
	for commit := range strings.FieldsSeq(out) {
		paths, err := git(ctx, dir, "diff-tree", "-r", "-m", "--root", "--no-commit-id", "--name-only", "-z", "--no-renames", commit, "--", ":(top,literal,icase)"+outboxDir)
		if err != nil {
			return fmt.Errorf(textNoHistory, shownPath(dir), shortCommit(start), shownText(err.Error()))
		}
		if path, _, _ := strings.Cut(paths, "\x00"); path != "" {
			return fmt.Errorf(textOutboxCommitted, shownPath(system)+"/"+shownPath(r.Path), commit, shownText(path))
		}
	}
	return nil
}
