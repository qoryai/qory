package cmd

import (
	"encoding/json"
	"fmt"
)

// draftDocument is what the local submit step gives the submit program and reads back from
// it, for one repository at a time: the branch, the draft's text, and the repository with its
// commits and its draft.
type draftDocument struct {
	// Ref is the branch's name.
	Ref string
	// Text is the draft's title and description, nil when the session wrote none.
	Text *draftText
	// Repositories has at least one entry.
	Repositories []draftRepository
}

// draftRepository is one repository of a [draftDocument], with the head of the branch in it.
type draftRepository struct {
	System, Path string
	// Commit is the head of the branch.
	Commit string
	// Base is the commit the run started at, nil when the run pushed nothing.
	Base *draftBase
	// Commits are the commits from Base to Commit, oldest first.
	Commits []draftCommit
	// Draft is the draft pull request or the reason there is none, as [noDraft] has it.
	Draft json.RawMessage
}

// draftBase is the commit a run started at, with the repository's default branch.
type draftBase struct {
	Commit, Branch string
}

// draftCommit is one commit of a run, with the ledger step that made it.
type draftCommit struct {
	Commit, Step string
}

// draftText is the title and the description of the draft pull request.
type draftText struct {
	Title, Description string
}

// The verdicts of a local run's session: whether it ended with exit 0.
const (
	verdictPassed = "passed"
	verdictNone   = "none"
)

// The reasons a draft is none, the value of the member none.
const (
	noneNotPassed      = "not-passed"
	noneNoText         = "no-text"
	noneNoProgram      = "no-program"
	noneProgramFailed  = "program-failed"
	noneProgramTimeout = "program-timeout"
	noneLoginNeeded    = "login-needed"
)

// noDraft is the draft member of a repository with no draft: {"none": reason}, with the exit
// status for [noneProgramFailed] (0, 1 or 2) and [noneLoginNeeded] (3), and no exit for any
// other reason.
func noDraft(reason string, exit int) json.RawMessage {
	if reason == noneProgramFailed || reason == noneLoginNeeded {
		return json.RawMessage(fmt.Sprintf(`{"none":%q,"exit":%d}`, reason, exit))
	}
	return json.RawMessage(fmt.Sprintf(`{"none":%q}`, reason))
}
