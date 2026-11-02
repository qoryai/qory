// Package branchformat is version 1 of the branch format: a work item's branch and the
// commit in each repository, the file of a version of a structured artifact type branch.
// The contract is contracts/harness/v1/branch.schema.json.
package branchformat

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"

	"github.com/qoryai/qory/internal/exports"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// prefix starts every branch's name.
const prefix = "qory/"

// schemaURL is the schema's $id, which it is compiled under.
const schemaURL = "https://qory.dev/contracts/harness/v1/branch.schema.json"

// contractPath is the contract's schema, as a message prints it.
const contractPath = "contracts/harness/v1/branch.schema.json"

// branchSchema is a copy of contracts/harness/v1/branch.schema.json, byte for byte,
// which a test checks: go:embed does not reach a file above the package.
//
//go:embed branch.schema.json
var branchSchema []byte

// Repository is one repository of a branch document, with the commit of the branch in it.
type Repository struct {
	// System is the host the repository is on, such as github.com.
	System string `json:"system"`
	// Path is the repository's path on the host, such as acme/web.
	Path string `json:"path"`
	// Commit is a full commit id, 40 or 64 lower-case hexadecimal digits.
	Commit string `json:"commit"`
}

// Branch is one document of the format.
type Branch struct {
	// Ref is the branch's name, as [Name] builds it.
	Ref string `json:"ref"`
	// Repositories has at least one entry.
	Repositories []Repository `json:"repositories"`
}

// Name is the name of the branch of the work item with the id workItemID.
func Name(workItemID string) string { return prefix + workItemID }

// contract is the schema, compiled once.
var contract = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(branchSchema))
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
		return fmt.Errorf("write the branch document as one JSON object, the format of %s: %s", contractPath, exports.HideValues(err.Error()))
	}
	schema, err := contract()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("fix the branch document to match %s: %s", contractPath, exports.SchemaRules(err))
	}
	return nil
}
