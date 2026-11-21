package cmd

import (
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pathglob"
)

// expectedReviewers returns the names of the members of the group role agents/reviewers
// whose report a review round needs, in the order of members: a member with no paths
// (nil, which reads every change) and a member with a path glob that matches at least one
// of changed, the files of the change from the checkout root. qory decides this, so a
// session cannot shorten the list. The harness's panel verb picks the same members with
// the same match, see contracts/harness/v1/path-cases.json.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func expectedReviewers(members []compose.Entry, changed []string) []string {
	var names []string
	for _, m := range members {
		if m.Paths == nil || anyPathMatches(m.Paths, changed) {
			names = append(names, m.Name)
		}
	}
	return names
}

// anyPathMatches reports whether one of globs matches one of files.
func anyPathMatches(globs, files []string) bool {
	for _, g := range globs {
		for _, f := range files {
			if pathglob.Match(g, f) {
				return true
			}
		}
	}
	return false
}
