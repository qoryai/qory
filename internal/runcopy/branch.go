package runcopy

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// branchTries is how many names [FreeBranch] tries before it stops.
const branchTries = 100

// FreeBranch is the branch a run's commits come back on: the first name that no branch of
// the repository at checkout, and of each repository under .qory/repositories in it, has or
// blocks. The names are qory/run-<run id>, then qory/run-<run id>-2, -3 and so on. A branch
// blocks a name below it or above it, as qory blocks qory/run-<run id>: then the names are
// qory-run-<run id>, qory-run-<run id>-2 and so on. Names that differ only in case count as
// the same, as on a disk that ignores case.
func FreeBranch(ctx context.Context, checkout, runID string) (string, error) {
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return "", fileErr(checkout, err)
	}
	further, err := furtherRepositories(checkout)
	if err != nil {
		return "", err
	}
	dirs := []string{checkout}
	for _, rel := range further {
		dirs = append(dirs, filepath.Join(checkout, filepath.FromSlash(rel)))
	}
	var branches []string
	for _, dir := range dirs {
		names, err := branchNames(ctx, dir)
		if err != nil {
			return "", err
		}
		branches = append(branches, names...)
	}
	return freeName(branches, runID, checkout)
}

// FreeBranchAgain is branch when no branch of the repository at repo has or blocks it, and
// else the first free name for the run runID there, as [FreeBranch] picks it. It reads only
// the branches of repo, not of the repositories under it.
func FreeBranchAgain(ctx context.Context, repo, branch, runID string) (string, error) {
	branches, err := branchNames(ctx, repo)
	if err != nil {
		return "", err
	}
	if !blocked(branches, branch) {
		return branch, nil
	}
	return freeName(branches, runID, repo)
}

// freeName is the first name of [FreeBranch] that none of branches has or blocks. at is the
// checkout the error shows.
func freeName(branches []string, runID, at string) (string, error) {
	base := "qory/run-" + runID
	if slices.ContainsFunc(branches, func(b string) bool { return strings.EqualFold(b, "qory") }) {
		base = "qory-run-" + runID
	}
	for n := 1; n <= branchTries; n++ {
		name := base
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		if !blocked(branches, name) {
			return name, nil
		}
	}
	return "", fmt.Errorf(textNoFreeBranch, shown(base), shown(base), shown(at), branchTries)
}

// blocked reports whether one of branches is name, lies below it or above it, with case
// ignored: git keeps no branch below another branch.
func blocked(branches []string, name string) bool {
	name = strings.ToLower(name)
	for _, b := range branches {
		b = strings.ToLower(b)
		if b == name || strings.HasPrefix(b, name+"/") || strings.HasPrefix(name, b+"/") {
			return true
		}
	}
	return false
}

// branchNames is every branch of the repository at dir, without refs/heads/.
func branchNames(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf(textGitInRepo, shown(dir), err)
	}
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if name, ok := strings.CutPrefix(line, "refs/heads/"); ok {
			names = append(names, name)
		}
	}
	return names, nil
}
