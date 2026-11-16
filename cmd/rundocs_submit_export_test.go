package cmd

import (
	"encoding/json"

	"github.com/qoryai/integrations/contracts"
)

// SubmitOf is the submit and the submit_module that a run from a run code takes from
// documents, the documents of the answer to the code, as [codeRunOf] reads them: nil
// for none.
func SubmitOf(documents json.RawMessage) (*contracts.Submit, *contracts.SubmitModule, error) {
	doc, err := codeDocumentsOf(documents)
	if err != nil {
		return nil, nil, err
	}
	r, err := codeRunOf(doc)
	if err != nil {
		return nil, nil, err
	}
	return r.submit, r.submitModule, nil
}
