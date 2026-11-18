package preparation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the head check before a push. The text is the one of the submit step's
// contract: the branch, the repository, and the three commits that the person compares. A
// head that is not there reads "none".
const (
	textHeadChanged  = "qory run: Check branch %[1]s: its head is not the commit this run expected, so qory did not push.\n  Repository %[2]s/%[3]s. Head: %[4]s. Expected: %[5]s, %[6]s."
	textHeadNotAbove = "\n  The head does not descend from the expected commit."
	textHeadStart    = "the start commit"
	textHeadLastPush = "this run's last push"
	textHeadNone     = "none"
	textHeadNoRead   = "qory run: The run's host proxy could not read the head of the branch, so qory did not push.\n  Repository %[2]s/%[1]s.\n  Cause: %[3]s; the code is used"
)

// HeadChanged is the error of a push that qory did not make because the head of the
// branch at the forge is not the commit the run expected: another push moved it, a push of
// a run that is not cancelled is on it, or it is missing where the run expected a commit.
// The run ends failed with the code push_head_changed.
type HeadChanged struct {
	// Branch is the branch's name, System and Path the repository.
	Branch, System, Path string
	// Head is the branch's head at the forge, "" when the branch is missing. Expected is the
	// commit the run expected, and LastPush is whether it is the run's last push, not
	// its start commit.
	Head, Expected string
	LastPush       bool
	// NotAbove is whether Head does not descend from Expected.
	NotAbove bool
}

// Error is the refusal's text.
func (e *HeadChanged) Error() string {
	head, from := e.Head, textHeadStart
	if head == "" {
		head = textHeadNone
	}
	if e.LastPush {
		from = textHeadLastPush
	}
	text := fmt.Sprintf(textHeadChanged, e.Branch, shownPath(e.System), shownPath(e.Path), head, e.Expected, from)
	if e.NotAbove {
		text += textHeadNotAbove
	}
	return text
}

// remoteHead is the head of the branch ref at the remote of the repository path, "" when
// the branch is not there, read with ls-remote through read.
func remoteHead(ctx context.Context, read gitRunner, dir, system, path, ref string) (string, error) {
	out, err := read(ctx, dir, "ls-remote", "--", repoURL(system, path), "refs/heads/"+ref)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "refs/heads/"+ref && CommitID(fields[0]) {
			return fields[0], nil
		}
	}
	return "", nil
}

// history is a scratch bare repository that holds the commits, with no trees, of the
// repository path on system that a check needs: the checkout has one commit, so the history
// is fetched beside it and removed with the scratch folder.
type history struct {
	read gitRunner
	dir  string
	url  string
}

// openHistory makes the scratch repository and fetches commits into it by their ids.
// The caller removes the folder with the returned function.
func openHistory(ctx context.Context, read gitRunner, system, path string, commits ...string) (*history, func(), error) {
	dir, err := os.MkdirTemp("", "qory-history-")
	if err != nil {
		return nil, func() {}, local(err)
	}
	remove := func() { os.RemoveAll(dir) }
	h := &history{read: read, dir: dir, url: repoURL(system, path)}
	if _, err := read(ctx, dir, "init", "--quiet", "--bare"); err != nil {
		return nil, remove, err
	}
	args := slices.Concat([]string{"fetch", "--quiet", "--no-tags", "--filter=tree:0", "--", h.url}, commits)
	if _, err := read(ctx, dir, args...); err != nil {
		return nil, remove, err
	}
	return h, remove, nil
}

// descends reports whether descendant has ancestor in its history; a commit descends from
// itself.
func (h *history) descends(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := h.read(ctx, h.dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if ge := (*gitError)(nil); errors.As(err, &ge) && ge.exit == 1 {
		return false, nil
	}
	return err == nil, err
}

// headCheck is the verdict of the head check of one repository.
type headCheck struct {
	// lease is the head to put in --force-with-lease, "" for a plain push.
	lease string
}

// checkHead reads the head of the branch of b at the forge through read and decides how r's
// push may go, where r.Commit is the commit the run expected and head the commit it
// pushes. A head at the expected commit is a plain push. A branch that is not there is a
// plain push only for a repository that [WorkBranch.fresh] says starts at the run
// documents' checkout. A head that is a head a cancelled run pushed ([WorkBranch.Cancelled])
// and that descends from the commit the run started at is a push that replaces the branch
// with a lease on that head, before the run's first push alone. Any other head is a
// [*HeadChanged].
//
// Equality with a recorded head is enough: the record lists one head for each push of a
// cancelled run, and the push replaces the position of the branch that this run set, with every
// commit below it. A branch head that is below a recorded head, above it or beside it is not a
// position that a cancelled run set, so no history is read to cover commits one by one.
func checkHead(ctx context.Context, read gitRunner, dir string, b *WorkBranch, r WorkRepository) (headCheck, error) {
	got, err := remoteHead(ctx, read, dir, b.system, r.Path, b.Ref)
	if err != nil {
		return headCheck{}, fmt.Errorf(textHeadNoRead, shownPath(r.Path), shownPath(b.system), integration.Printable(exports.HideURLError(err).Error()))
	}
	changed := &HeadChanged{Branch: b.Ref, System: b.system, Path: r.Path, Head: got, Expected: r.Commit, LastPush: r.Commit != r.Start}
	switch {
	case got == r.Commit:
		return headCheck{}, nil
	case got == "" && b.fresh(b.system, r.Path) && r.Commit == r.Start:
		return headCheck{}, nil
	case got == "":
		return headCheck{}, changed
	}
	h, remove, err := openHistory(ctx, read, b.system, r.Path, r.Commit, got)
	defer remove()
	if err != nil {
		return headCheck{}, fmt.Errorf(textHeadNoRead, shownPath(r.Path), shownPath(b.system), integration.Printable(exports.HideURLError(err).Error()))
	}
	above, err := h.descends(ctx, r.Commit, got)
	if err != nil {
		return headCheck{}, fmt.Errorf(textHeadNoRead, shownPath(r.Path), shownPath(b.system), integration.Printable(exports.HideURLError(err).Error()))
	}
	changed.NotAbove = !above
	if !above || r.Commit != r.Start {
		return headCheck{}, changed
	}
	if !b.cancelledHead(b.system, r.Path, got) {
		return headCheck{}, changed
	}
	return headCheck{lease: got}, nil
}

// cancelledHead reports whether commit is a head that a cancelled run pushed to the repository
// path on system.
func (b *WorkBranch) cancelledHead(system, path, commit string) bool {
	return slices.ContainsFunc(b.Cancelled, func(l Landed) bool {
		return l.System == system && sameRepository(system == ForgeGitHub, l.Path, path) && l.Commit == commit
	})
}
