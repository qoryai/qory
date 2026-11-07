package runcopy

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ActingChanges lists the acting keys, by package acting's list, that differ between
// the qory files at the repository's base commit and at the run's head, both in the
// quarantine: qory.yaml, harness.yaml and qory-stack.yaml at the top, by a name that
// matches regardless of letter case, and each stack file in the repository that one of
// them extends by a path. It also lists each link the run's commits add or change, and each
// folder they add that [gitDirLike] matches, which git takes as a repository at a checkout.
//
// Every file is read from the commit's tree with git ls-tree and git cat-file, which
// run no filter, part by part of its path, so a link or a submodule on the way counts
// as a change to the file. Path values are compared as written: nothing is resolved on
// this machine. The list is sorted by file, then by key.
func (q *Quarantine) ActingChanges(ctx context.Context) ([]ActingChange, error) {
	before, err := q.topNames(ctx, q.Repo.Base)
	if err != nil {
		return nil, fmt.Errorf(textActingFailed, shown(q.Path), err)
	}
	after, err := q.topNames(ctx, q.Head)
	if err != nil {
		return nil, fmt.Errorf(textActingFailed, shown(q.Path), err)
	}
	out, err := qoryChanges(before, q.treeReader(ctx, q.Repo.Base), after, q.treeReader(ctx, q.Head))
	if err != nil {
		return nil, fmt.Errorf(textActingFailed, shown(q.Path), err)
	}
	links, err := q.changedLinks(ctx)
	if err != nil {
		return nil, fmt.Errorf(textActingFailed, shown(q.Path), err)
	}
	for _, l := range links {
		out = append(out, ActingChange{File: l})
	}
	folders, err := q.addedGitFolders(ctx)
	if err != nil {
		return nil, fmt.Errorf(textActingFailed, shown(q.Path), err)
	}
	for _, f := range folders {
		out = append(out, ActingChange{File: f})
	}
	return sortChanges(out), nil
}

// topNames is the names at the top of commit's tree.
func (q *Quarantine) topNames(ctx context.Context, commit string) ([]string, error) {
	top, err := q.treeEntries(ctx, commit+"^{tree}", "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(top))
	for _, e := range top {
		names = append(names, e.name)
	}
	return names, nil
}

// treeReader reads a qory file at commit.
func (q *Quarantine) treeReader(ctx context.Context, commit string) readQoryFile {
	return func(rel string) (qoryFile, []byte, error) { return q.readTreeFile(ctx, commit, rel) }
}

// treeEntry is one entry of a tree, as git ls-tree prints it.
type treeEntry struct{ mode, kind, id, name string }

// treeEntries is the entries of tree that are named name, or all of them for "".
func (q *Quarantine) treeEntries(ctx context.Context, tree, name string) ([]treeEntry, error) {
	args := []string{"ls-tree", "-z", "--full-tree", tree}
	if name != "" {
		args = append(args, "--", name)
	}
	out, err := gitWith(ctx, q.Path, []string{"GIT_LITERAL_PATHSPECS=1"}, nil, args...)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for entry := range strings.SplitSeq(out, "\x00") {
		if entry == "" {
			continue
		}
		about, n, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(about)
		if !ok || len(fields) != 3 {
			return nil, &gitError{subcommand: "ls-tree", line: "printed " + shown(entry), exit: -1}
		}
		entries = append(entries, treeEntry{fields[0], fields[1], fields[2], n})
	}
	return entries, nil
}

// readTreeFile reads the entry rel at commit, part by part of its path: its shape, its
// id, and the content of a regular file of at most [maxQoryFile] bytes.
func (q *Quarantine) readTreeFile(ctx context.Context, commit, rel string) (qoryFile, []byte, error) {
	tree := commit + "^{tree}"
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		entries, err := q.treeEntries(ctx, tree, part)
		if err != nil {
			return qoryFile{}, nil, err
		}
		if len(entries) != 1 || entries[0].name != part {
			return qoryFile{shape: shapeMissing}, nil, nil
		}
		e := entries[0]
		switch {
		case e.mode == "120000" || e.mode == "160000":
			// A link or a submodule, on the way or at the end.
			return qoryFile{shape: shapeOther, id: strings.Join(parts[:i+1], "/") + " " + e.mode + " " + e.id}, nil, nil
		case i < len(parts)-1 && e.kind == "tree":
			tree = e.id
		case i < len(parts)-1:
			return qoryFile{shape: shapeMissing}, nil, nil
		case e.kind != "blob" || e.mode != "100644" && e.mode != "100755":
			return qoryFile{shape: shapeOther, id: e.mode + " " + e.id}, nil, nil
		default:
			return q.readBlob(ctx, e.id)
		}
	}
	return qoryFile{shape: shapeMissing}, nil, nil
}

// readBlob reads the blob id as a qory file, when it has at most [maxQoryFile] bytes.
func (q *Quarantine) readBlob(ctx context.Context, id string) (qoryFile, []byte, error) {
	size, err := git(ctx, q.Path, "cat-file", "-s", id)
	if err != nil {
		return qoryFile{}, nil, err
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64); err != nil || n > maxQoryFile {
		return qoryFile{shape: shapeOther, id: id}, nil, nil
	}
	data, err := git(ctx, q.Path, "cat-file", "blob", id)
	if err != nil {
		return qoryFile{}, nil, err
	}
	return qoryFile{shape: shapeYAML, id: id}, []byte(data), nil
}

// changedLinks is each path whose entry at the run's head is a link that the base
// commit does not have there as it is, by git diff-tree.
func (q *Quarantine) changedLinks(ctx context.Context) ([]string, error) {
	out, err := git(ctx, q.Path, "diff-tree", "-r", "-z", "--no-renames", "--no-commit-id", q.Repo.Base, q.Head)
	if err != nil {
		return nil, err
	}
	var links []string
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) == 5 && meta[1] == "120000" && meta[4] != "D" {
			links = append(links, fields[i+1])
		}
	}
	return links, nil
}

// addedGitFolders is each folder at the run's head that [gitDirLike] matches and that the base
// commit does not have as such, "." for the top.
func (q *Quarantine) addedGitFolders(ctx context.Context) ([]string, error) {
	before, err := q.gitFolders(ctx, q.Repo.Base)
	if err != nil {
		return nil, err
	}
	after, err := q.gitFolders(ctx, q.Head)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, f := range after {
		if !slices.Contains(before, f) {
			added = append(added, f)
		}
	}
	return added, nil
}

// gitFolders is each folder of commit's tree that [gitDirLike] matches, sorted.
func (q *Quarantine) gitFolders(ctx context.Context, commit string) ([]string, error) {
	out, err := git(ctx, q.Path, "ls-tree", "-r", "-z", "--name-only", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	names := map[string][]string{}
	for file := range strings.SplitSeq(out, "\x00") {
		for p := file; p != "" && p != "."; p = path.Dir(p) {
			names[path.Dir(p)] = append(names[path.Dir(p)], path.Base(p))
		}
	}
	var folders []string
	for f, in := range names {
		if gitDirLike(in) {
			folders = append(folders, f)
		}
	}
	slices.Sort(folders)
	return folders, nil
}
