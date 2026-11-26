package cmd

import (
	"slices"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pathglob"
)

// expectedReviewers returns the names of the members of a group role whose report a
// review round needs, in the order of members: a member with no paths (nil, which reads
// every change) and a member with a path glob that matches at least one of changed, the
// files of the change from the checkout root. members are the entries of the group
// agents/<review_group>, see [expectedReviewersFor]. qory decides this, so a session
// cannot shorten the list. The harness's panel verb picks the same members with the same
// match, see contracts/harness/v1/path-cases.json.
func expectedReviewers(members []compose.Entry, changed []string) []string {
	var names []string
	for _, m := range members {
		if m.Paths == nil || anyPathMatches(m.Paths, changed) {
			names = append(names, m.Name)
		}
	}
	return names
}

// expectedReviewersFor returns the names of the reviewers whose report a session of the
// workflow needs: the members of the group role agents/<review_group> in res.Groups,
// where review_group is the workflow's entry in the composed stack's table
// ([reviewGroupOf]), picked as [expectedReviewers] picks them. A workflow with no
// review_group has none, and so does a group that res does not have; the compose refuses
// the second, so it arises only for a result that a caller built.
func expectedReviewersFor(res *compose.Result, workflow string, changed []string) []string {
	if res == nil || res.Stack == nil {
		return nil
	}
	group := reviewGroupOf(res.Stack.Workflows, workflow)
	if group == "" {
		return nil
	}
	entries := map[string]compose.Entry{}
	for _, e := range res.Entries {
		if e.Kind == "agents" {
			entries[e.Name] = e
		}
	}
	var members []compose.Entry
	for _, name := range res.Groups["agents/"+group] {
		e, ok := entries[name]
		if !ok {
			e = compose.Entry{Name: name}
		}
		members = append(members, e)
	}
	return expectedReviewers(members, changed)
}

// reviewGroupRoles returns the names of the members of the group role
// agents/<review_group> of the workflow in res, in the order of the group, nil for a
// workflow with no review_group or a result with no stack.
func reviewGroupRoles(res *compose.Result, workflow string) []string {
	if res == nil || res.Stack == nil {
		return nil
	}
	group := reviewGroupOf(res.Stack.Workflows, workflow)
	if group == "" {
		return nil
	}
	return slices.Clone(res.Groups["agents/"+group])
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
