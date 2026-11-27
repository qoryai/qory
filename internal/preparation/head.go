package preparation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the head check before a push. The text is the one of the submit step's
// contract: the branch, the repository, and the three commits that the person compares. A
// head that is not there reads "none".
const (
	textHeadChanged  = "qory run: Set the branch to the expected commit or delete it; its head is not that commit.\n  Branch %[1]s. Repository %[2]s/%[3]s. Head: %[4]s. Expected: %[5]s, %[6]s. qory did not push."
	textHeadMissing  = "qory run: Create the branch at the expected commit on the forge; it is not there.\n  Branch %[1]s. Repository %[2]s/%[3]s. Head: %[4]s. Expected: %[5]s, %[6]s. qory did not push.\n  Run git push <remote> %[5]s:refs/heads/%[1]s, where <remote> is the remote that points to %[2]s/%[3]s; fetch the commit first if the checkout has no such commit."
	textHeadLoss     = "\n  Setting the branch to that commit or deleting it loses the commits on it that no version has."
	textHeadRecorded = "\n  Expected is this run's last push: %s works only if Qory Apiary recorded that push."
	textHeadStart    = "the start commit"
	textHeadLastPush = "this run's last push"
	textHeadNone     = "none"
	textHeadNoRead   = "qory run: Take the work item in again in Qory Apiary: the run's host proxy could not read the head.\n  qory could not read the head of the branch, so it pushed nothing.\n  Repository %[2]s/%[1]s.\n  Cause: %[3]s; the code is used"
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
	format := textHeadChanged
	if e.Head == "" {
		format = textHeadMissing
	}
	text := fmt.Sprintf(format, e.Branch, shownPath(e.System), shownPath(e.Path), head, e.Expected, from)
	if e.Head != "" {
		text += textHeadLoss
	}
	if e.LastPush {
		action := "moving the branch to it"
		if e.Head == "" {
			action = "creating the branch at it"
		}
		text += fmt.Sprintf(textHeadRecorded, action)
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

// mergeBases returns the merge bases of the commits a and b, each a full commit id: none for
// commits with no common ancestor, one for most, two or more for a history that two
// merges crossed.
func (h *history) mergeBases(ctx context.Context, a, b string) ([]string, error) {
	out, err := h.read(ctx, h.dir, "merge-base", "--all", "--", a, b)
	if ge := (*gitError)(nil); errors.As(err, &ge) && ge.exit == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bases []string
	for line := range strings.Lines(out) {
		if commit := strings.TrimSpace(line); commit != "" {
			bases = append(bases, commit)
		}
	}
	return bases, nil
}

// projectBase is the merge base of head with the head of r's default branch at the forge,
// fetched by its id at depth one into the checkout dir, so that a review of head can diff
// from it; "" when r has no default branch, the branch has no head, or a read, the merge
// base or the fetch fails. The history is fetched into a scratch repository, never into
// dir, whose .git the agent could change. The fetch takes the URL that qory builds, not
// dir's origin.
//
// The reads end after [projectBaseLimit], since every push of every run makes them and the
// history fetch grows with the count of commits: a slow forge or a long history then
// leaves no project base and does not hold up the run.
func projectBase(ctx context.Context, read gitRunner, dir, system string, r WorkRepository, head string) string {
	if r.DefaultBranch == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, projectBaseLimit)
	defer cancel()
	tip, err := remoteHead(ctx, read, dir, system, r.Path, r.DefaultBranch)
	if err != nil || tip == "" {
		return ""
	}
	h, remove, err := openHistory(ctx, read, system, r.Path, head, tip)
	defer remove()
	if err != nil {
		return ""
	}
	out, err := h.read(ctx, h.dir, "merge-base", head, tip)
	base := strings.TrimSpace(out)
	if err != nil || !CommitID(base) {
		return ""
	}
	if _, err := read(ctx, dir, "fetch", "--quiet", "--no-tags", "--depth", "1", "--", h.url, base); err != nil {
		return ""
	}
	return base
}

// projectBaseLimit bounds all the reads of [projectBase] together, and all the reads of
// the history in [WorkBranch.findBase] together. It is a variable so that a test can
// shorten it.
var projectBaseLimit = 60 * time.Second

// headCheck is the verdict of the head check of one repository.
type headCheck struct {
	// lease is the head to put in --force-with-lease, "" for a plain push.
	lease string
}

// checkHead reads the head of the branch of b at the forge through read and decides how r's
// push may go, where r.Commit is the commit the run expected and head the commit it
// pushes. A head at the expected commit is a plain push. A branch that is not there is a
// plain push only for a repository that [WorkBranch.fresh] says starts at the run
// documents' checkout. A head of [WorkBranch.Cancelled], which a cancelled run or a session
// with no version pushed, and that descends from the commit the run started at is a push that
// replaces the branch with a lease on that head, before the run's first push alone, unless a
// commit of [WorkBranch.Pushed] other than the expected one is above the start commit and is
// that head or below it: the lease would drop a commit of a run that nobody cancelled. The start
// run's unconfirmed commits are in Cancelled, not in Pushed. Any other head is a [*HeadChanged].
//
// Equality with a recorded head is enough: the record lists one head for each push of a
// cancelled run, and the push replaces the position of the branch that this run set, with every
// commit below it. A branch head that is below a recorded head, above it or beside it is not a
// position that a cancelled run set. qory fetches the history of the head only when a cancelled
// run recorded that head and the run has not pushed yet, and then checks that the head descends
// from the start commit. A head that no record lists, or any head after the run's first push,
// is refused before any fetch: the refusal is the same text for every such head, so the check
// downloads no history for a head that someone else pushed.
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
	if r.Commit != r.Start || !b.cancelledHead(b.system, r.Path, got) {
		return headCheck{}, changed
	}
	// Only a head that a record lists reaches the history: any other head refuses with no fetch.
	h, remove, err := openHistory(ctx, read, b.system, r.Path, r.Commit, got)
	defer remove()
	if err != nil {
		return headCheck{}, fmt.Errorf(textHeadNoRead, shownPath(r.Path), shownPath(b.system), integration.Printable(exports.HideURLError(err).Error()))
	}
	// The commits above the start that are the head or below it: none when the head does not
	// descend from the start commit. The lease replaces them all, so a commit of a run that
	// nobody cancelled, [WorkBranch.Pushed], must not be one of them. The history holds the
	// ancestors of the start and of the head alone, so a commit that the forge lacks is not in it.
	span, err := h.between(ctx, r.Commit, got)
	if err != nil {
		return headCheck{}, fmt.Errorf(textHeadNoRead, shownPath(r.Path), shownPath(b.system), integration.Printable(exports.HideURLError(err).Error()))
	}
	if len(span) == 0 || len(span) > headSpanLimit {
		return headCheck{}, changed
	}
	for _, p := range b.Pushed {
		if p.System == b.system && sameRepository(b.system == ForgeGitHub, p.Path, r.Path) && slices.Contains(span, p.Commit) {
			return headCheck{}, changed
		}
	}
	return headCheck{lease: got}, nil
}

// headSpanLimit is the most commits between the start commit and a recorded head that the head
// check reads. A span past it is refused as a head that changed, with the check not made.
var headSpanLimit = 10000

// between lists the commits above start that are head or below it, newest first, at most
// [headSpanLimit]+1 of them. It reads the fetched history and makes no request to the forge.
func (h *history) between(ctx context.Context, start, head string) ([]string, error) {
	out, err := h.read(ctx, h.dir, "rev-list", "--ancestry-path", "--max-count="+strconv.Itoa(headSpanLimit+1), start+".."+head)
	return strings.Fields(out), err
}

// cancelledHead reports whether commit is a head that a cancelled run pushed to the repository
// path on system.
func (b *WorkBranch) cancelledHead(system, path, commit string) bool {
	return slices.ContainsFunc(b.Cancelled, func(l Landed) bool {
		return l.System == system && sameRepository(system == ForgeGitHub, l.Path, path) && l.Commit == commit
	})
}
