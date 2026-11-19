package cmd

import (
	"encoding/json"

	"github.com/qoryai/integrations/contracts"
)

// SubmitOf is the submit that a run from a run code takes from documents, the documents
// of the answer to the code, as [codeRunOf] reads them: nil for none.
func SubmitOf(documents json.RawMessage) (*contracts.Submit, error) {
	doc, err := codeDocumentsOf(documents)
	if err != nil {
		return nil, err
	}
	r, err := codeRunOf(doc)
	if err != nil {
		return nil, err
	}
	return r.submit, nil
}

// SubmitAt is [SubmitOf] for a run whose level, [accessOf], is level: the submit that
// the run keeps after [codeRun.limitSubmit], nil for none.
func SubmitAt(documents json.RawMessage, level string) (*contracts.Submit, error) {
	doc, err := codeDocumentsOf(documents)
	if err != nil {
		return nil, err
	}
	r, err := codeRunOf(doc)
	if err != nil {
		return nil, err
	}
	r.limitSubmit(level)
	return r.submit, nil
}
