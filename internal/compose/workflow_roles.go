package compose

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/stack"
)

// workflowRoles are the failures of a compose in which a workflow of the stack starts an
// agent role that nothing fills, as lines of [UnresolvedError], and the pairs they
// replace among rule 4's lines, each "<kind>/<name> <reference>": the entry and the
// reference. A run starts a workflow at its first composed entry of skill
// workflow-<name>, command workflow-<name>, skill <name> and command <name>, and that
// entry starts each agent its documents reference. A reference to an agent that no module
// ships or provides, that the stack does not bind, that is no group role, and that no
// exclude left out, is one block, by workflow and then by agent. The exports check refuses a process with a
// workflow that the stack does not declare, so the stack's workflows include every
// workflow of its process, and the check covers what the process lists. Any other unresolved reference is rule 4's alone, see
// [checkResolved]: dropped are the roles a module provides with an entry that a block
// left out.
//
// The stack is shown as the base stack for a stack that extends base, as as when set,
// and otherwise by its file.
func workflowRoles(res *Result, dropped map[string][]Role, base *Base, as string) ([]string, map[string]bool) {
	p := res.Stack
	if p == nil || len(p.Workflows) == 0 {
		return nil, nil
	}
	entries := map[string]Entry{}
	for _, e := range res.Entries {
		entries[e.Kind+"/"+e.Name] = e
	}
	var blocks []string
	var replaced map[string]bool
	for _, w := range slices.Sorted(maps.Keys(p.Workflows)) {
		start, ok := startedBy(entries, w)
		if !ok {
			continue
		}
		for _, ref := range start.References {
			kind, name, _ := strings.Cut(ref, "/")
			if kind != "agents" {
				continue
			}
			if _, bound := res.Bind[ref]; bound {
				continue
			}
			// A group role, with members or none, is rule 4's: it refuses a reference to a
			// group with no member.
			if _, group := res.Groups[ref]; group {
				continue
			}
			if _, composed := entries[ref]; composed {
				continue
			}
			if excludeOf(res, kind, name) != nil || len(dropped[ref]) > 0 {
				continue
			}
			blocks = append(blocks, fmt.Sprintf("Add a module that fills %s, or drop %s from the workflows and process.\n  %s declares the workflow %s. Its %s of module %s starts %s, and no module ships or provides that agent, and the stack does not bind it.",
				shownKey(ref), shown(w), stackPhrase(p, base, as), shown(w), described(start.Kind, start.Name), checkoutName(res, start.Module), shownKey(ref)))
			if replaced == nil {
				replaced = map[string]bool{}
			}
			replaced[start.Kind+"/"+start.Name+" "+ref] = true
		}
	}
	return blocks, replaced
}

// startedBy is the entry a run starts the workflow w at, in the order a run looks the
// names up, see [stack.WorkflowEntries], and whether entries has one.
func startedBy(entries map[string]Entry, w string) (Entry, bool) {
	for _, name := range stack.WorkflowEntries(w) {
		for _, kind := range []string{"skills", "commands"} {
			if e, ok := entries[kind+"/"+name]; ok {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// stackPhrase is the stack p at the start of a sentence: "The base stack <name>@<pin>"
// for a stack that extends base, as with its first letter in upper case when set, "The
// stack in <file>" for a stack read from a file, and "The stack" otherwise.
func stackPhrase(p *stack.Stack, base *Base, as string) string {
	switch {
	case base != nil:
		return "The base stack " + base.String()
	case as != "":
		r, n := utf8.DecodeRuneInString(as)
		return string(unicode.ToUpper(r)) + as[n:]
	case p.File != "":
		return "The stack in " + filepath.Base(p.File)
	}
	return "The stack"
}
