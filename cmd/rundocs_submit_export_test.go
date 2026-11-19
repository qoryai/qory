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
