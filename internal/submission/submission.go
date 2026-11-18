// Package submission is version 1 of the submission format: what a run submits for a work
// item, its branch with the commits in each repository, the verdict and the draft pull
// request, the file of a version of the structured artifact type submission. Only qory's
// submit step writes it. The contract is contracts/harness/v1/submission.schema.json.
package submission

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/qoryai/qory/internal/exports"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// prefix starts every branch's name.
const prefix = "qory/"

// schemaURL is the schema's $id, which it is compiled under.
const schemaURL = "https://qory.dev/contracts/harness/v1/submission.schema.json"

// contractPath is the contract's schema, as a message prints it.
const contractPath = "contracts/harness/v1/submission.schema.json"

// submissionSchema is a copy of contracts/harness/v1/submission.schema.json, byte for byte,
// which a test checks: go:embed does not reach a file above the package.
//
//go:embed submission.schema.json
var submissionSchema []byte

// Repository is one repository of a submission, with the commit of the branch in it.
type Repository struct {
	// System is the host the repository is on, such as github.com.
	System string `json:"system"`
	// Path is the repository's path on the host, such as acme/web.
	Path string `json:"path"`
	// Commit is the head of the branch: a full commit id, 40 or 64 lower-case hexadecimal
	// digits.
	Commit string `json:"commit"`
	// CommitURL is the forge's address of Commit, written by the submit program.
	CommitURL string `json:"commit_url,omitempty"`
	// Base is the commit the run started at, nil when the run pushed nothing.
	Base *Base `json:"base,omitempty"`
	// Commits are the commits from Base to Commit, oldest first.
	Commits []Commit `json:"commits,omitempty"`
	// Draft is the draft pull request or the reason there is none, as [Draft] has it.
	Draft json.RawMessage `json:"draft"`
}

// Base is the commit a run started at in a repository.
type Base struct {
	Commit    string `json:"commit"`
	CommitURL string `json:"commit_url,omitempty"`
}

// Commit is one commit of a run, with the ledger step that made it.
type Commit struct {
	Commit    string `json:"commit"`
	Step      string `json:"step"`
	CommitURL string `json:"commit_url,omitempty"`
}

// Text is the title and the description of the draft pull request.
type Text struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// The verdicts of a submission.
const (
	VerdictPassed           = "passed"
	VerdictChangesRequested = "changes-requested"
	VerdictEscalate         = "escalate"
	VerdictNone             = "none"
)

// The reasons a draft is none, the value of the member none.
const (
	NoneNotPassed      = "not-passed"
	NoneNoText         = "no-text"
	NoneEscalated      = "escalated"
	NoneNoProgram      = "no-program"
	NoneProgramFailed  = "program-failed"
	NoneProgramTimeout = "program-timeout"
)

// Submission is one document of the format.
type Submission struct {
	// Ref is the branch's name, as [Name] builds it.
	Ref string `json:"ref"`
	// Verdict is one of the Verdict constants.
	Verdict string `json:"verdict"`
	// Text is the draft's title and description, nil when the session wrote none.
	Text *Text `json:"text,omitempty"`
	// Repositories has at least one entry.
	Repositories []Repository `json:"repositories"`
}

// None is the draft member of a repository with no draft: {"none": reason}, with the
// exit status for [NoneProgramFailed], 0 for any other reason.
func None(reason string, exit int) json.RawMessage {
	if exit != 0 {
		return json.RawMessage(fmt.Sprintf(`{"none":%q,"exit":%d}`, reason, exit))
	}
	return json.RawMessage(fmt.Sprintf(`{"none":%q}`, reason))
}

// Name is the name of the branch of the work item with the id workItemID.
func Name(workItemID string) string { return prefix + workItemID }

// contract is the schema, compiled once.
var contract = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(submissionSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(schemaURL)
})

// Check checks document, one JSON document, against the schema. The error starts with
// the fix, and then has each rule the document breaks, with its place in the document.
func Check(document []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("write the submission as one JSON object, the format of %s: %s", contractPath, exports.HideValues(err.Error()))
	}
	schema, err := contract()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("fix the submission to match %s: %s", contractPath, exports.SchemaRules(err))
	}
	return nil
}
