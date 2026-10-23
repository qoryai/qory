package provisioner

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// documentsSchema is the name of run/v1's schema of a run's documents.
const documentsSchema = "documents.schema.json"

// compiledDocuments is run/v1 documents.schema.json, compiled once.
var compiledDocuments = sync.OnceValues(func() (*jsonschema.Schema, error) {
	s, err := contracts.CompileRun(documentsSchema)
	if err != nil {
		return nil, fmt.Errorf("the contract's schema %s does not compile", documentsSchema)
	}
	return s, nil
})

// ReadDocuments reads documents, the documents object of a run code's signed
// redemption, run/v1 documents.schema.json. The schema checks the bytes before they are
// decoded. A documents object with a run_key or a run_code member is refused before
// that, so the value of such a member is never decoded or copied. Then the rules the
// schema states and cannot check are checked: the inputs, compacted, are at most
// contracts.MaxInputs bytes; run_settings is checked as [checkRunSettings] describes; and for
// the step explore, inputs.domain must be the domain and inputs.work_item.id the work
// item's id, as the explore inputs schema states. Inputs are the documents' bytes,
// which the run reads unchanged. The error contains a member's name, the schema's name
// or a byte count, never a value of documents.
func ReadDocuments(documents json.RawMessage) (contracts.Documents, error) {
	v := jsontext.Value(documents)
	if !v.IsValid() {
		return contracts.Documents{}, errNotOneDocument
	}
	switch {
	case v.Kind() != '{':
		return contracts.Documents{}, errors.New("it is not a JSON object")
	case topLevelMember(documents, "run_key"):
		return contracts.Documents{}, errors.New("it contains a run_key member")
	case topLevelMember(documents, "run_code"):
		return contracts.Documents{}, errors.New("it contains a run_code member")
	}
	s, err := compiledDocuments()
	if err != nil {
		return contracts.Documents{}, err
	}
	var doc contracts.Documents
	if err := readChecked(s, documentsSchema, documents, &doc); err != nil {
		return contracts.Documents{}, err
	}
	var inputs bytes.Buffer
	if json.Compact(&inputs, doc.Inputs) != nil || inputs.Len() > contracts.MaxInputs {
		return contracts.Documents{}, fmt.Errorf("its inputs are larger than %d bytes", contracts.MaxInputs)
	}
	if err := checkRunSettings(documentsSchema, doc.Step, doc.RunSettings); err != nil {
		return contracts.Documents{}, err
	}
	if doc.Step == StepExplore {
		var in struct {
			Domain   string `json:"domain"`
			WorkItem *struct {
				ID string `json:"id"`
			} `json:"work_item"`
		}
		switch {
		case json.Unmarshal(doc.Inputs, &in) != nil:
			return contracts.Documents{}, errors.New("its inputs do not read as the explore step's inputs")
		case in.Domain != doc.Domain:
			return contracts.Documents{}, errors.New("its inputs.domain is not its domain")
		case in.WorkItem == nil || doc.WorkItem == nil || in.WorkItem.ID != doc.WorkItem.ID:
			return contracts.Documents{}, errors.New("its inputs.work_item.id is not its work item's id")
		}
	}
	return doc, nil
}

// checkRunSettings checks rs, the run_settings of documents of the schema name already
// checked. It checks again that run_settings comes only for the step explore, which the
// schema checks too. It then checks the rules the schema states and cannot check: the
// ids in provider_models are unique, effort is listed for model, each tier's model is
// listed, and each tier's effort is listed for that tier's model. The schema checks each
// value with Go's regexp, whose $ matches only at the end of the text, so a value with a
// trailing line feed is refused there.
func checkRunSettings(name, step string, rs *contracts.RunSettings) error {
	if rs == nil {
		return nil
	}
	broken := fmt.Errorf("it breaks %s", name)
	if step != StepExplore {
		return broken
	}
	efforts := make(map[string][]string, len(rs.ProviderModels))
	for _, m := range rs.ProviderModels {
		if _, twice := efforts[m.ID]; twice {
			return broken
		}
		efforts[m.ID] = m.Effort
	}
	listed := func(model, effort string) bool {
		e, ok := efforts[model]
		return ok && (effort == "" || slices.Contains(e, effort))
	}
	if rs.Model != "" && !listed(rs.Model, rs.Effort) {
		return broken
	}
	if rs.Tiers != nil {
		for _, t := range []*contracts.Tier{rs.Tiers.Light, rs.Tiers.Fast, rs.Tiers.Thinking} {
			if t != nil && !listed(t.Model, t.Effort) {
				return broken
			}
		}
	}
	return nil
}
