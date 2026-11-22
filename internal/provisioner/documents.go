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
// decoded, and the documents are read as [readChecked] reads them: a member that the
// contract does not define is not read, and a must_understand that lists one ends the
// read. A documents object with a run_key or a run_code member is refused before
// that, so the value of such a member is never decoded or copied. Then the rules the
// schema states and cannot check are checked: the inputs, compacted, are at most
// contracts.MaxInputs bytes; run_settings is checked as [checkRunSettings] describes; and for
// a subject of the kind work_item, inputs.domain must be the domain and
// inputs.work_item.id the subject's id, as documents.schema.json states. A subject of
// another kind has no such rule. Inputs are the documents' bytes, which the run reads
// unchanged. The rules of [contracts.Documents.Check] are checked after the schema, and
// documents that break one get a [*BrokenRule]. The error contains a member's name, the
// schema's name or a byte count, and a [*BrokenRule] a deliver type, never another value
// of documents.
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
	if doc.Check() != nil {
		return contracts.Documents{}, &BrokenRule{Detail: brokenRuleDetail(doc)}
	}
	var inputs bytes.Buffer
	if json.Compact(&inputs, doc.Inputs) != nil || inputs.Len() > contracts.MaxInputs {
		return contracts.Documents{}, fmt.Errorf("its inputs are larger than %d bytes", contracts.MaxInputs)
	}
	// run_settings comes only for the step workflow, which the schema checks too.
	if doc.RunSettings != nil && doc.Step != StepWorkflow {
		return contracts.Documents{}, fmt.Errorf("it breaks %s", documentsSchema)
	}
	if err := checkRunSettings(documentsSchema, doc.RunSettings); err != nil {
		return contracts.Documents{}, err
	}
	if doc.Subject != nil && doc.Subject.Kind == SubjectWorkItem {
		// case:strict: inputs may contain members beside these, so a decode that ignores
		// letter case would read a later DOMAIN in place of domain.
		var in struct {
			Domain   string `json:"domain,case:strict"`
			WorkItem *struct {
				ID string `json:"id,case:strict"`
			} `json:"work_item,case:strict"`
		}
		switch {
		case json.Unmarshal(doc.Inputs, &in) != nil:
			return contracts.Documents{}, errors.New("its inputs do not read as the inputs of a work item's run")
		case in.Domain != doc.Domain:
			return contracts.Documents{}, errors.New("its inputs.domain is not its domain")
		case in.WorkItem == nil || in.WorkItem.ID != doc.Subject.ID:
			return contracts.Documents{}, errors.New("its inputs.work_item.id is not its subject's id")
		}
	}
	return doc, nil
}

// checkRunSettings checks rs, the model settings of a document of the schema name already
// checked, for the rules the schema states and cannot check: the ids in provider_models
// are unique, effort is listed for model, each tier's model is
// listed, and each tier's effort is listed for that tier's model. The schema checks each
// value with Go's regexp, whose $ matches only at the end of the text, so a value with a
// trailing line feed is refused there.
func checkRunSettings(name string, rs *contracts.RunSettings) error {
	if rs == nil {
		return nil
	}
	broken := fmt.Errorf("it breaks %s", name)
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
		for _, t := range []*contracts.Tier{rs.Tiers.Light, rs.Tiers.Balanced, rs.Tiers.Thinking} {
			if t != nil && !listed(t.Model, t.Effort) {
				return broken
			}
		}
	}
	return nil
}

// BrokenRule is the error of documents that pass the schema and break a rule of
// [contracts.Documents.Check]: its text is that of documents that break the schema, and
// Detail is the rule, for a line under it.
type BrokenRule struct {
	Detail string
}

func (e *BrokenRule) Error() string { return "it breaks " + documentsSchema }

// The details of a [BrokenRule]. textDeliverTwice has the type, which the schema's
// pattern keeps to lower-case letters, digits and hyphens, cut to [maxShownType]
// characters.
const (
	textDeliverTwice = "deliver lists %s twice"
	textEntryTwice   = "read_targets or the destinations list one entry twice"
	maxShownType     = 19
)

// brokenRuleDetail is the detail of doc, documents that [contracts.Documents.Check]
// stops: [textDeliverTwice] for the first type that deliver repeats, else
// [textEntryTwice]. It reads no message of Check.
func brokenRuleDetail(doc contracts.Documents) string {
	seen := make(map[string]bool, len(doc.Deliver))
	for _, d := range doc.Deliver {
		if seen[d.Type] {
			return fmt.Sprintf(textDeliverTwice, ShownType(d.Type))
		}
		seen[d.Type] = true
	}
	return textEntryTwice
}

// ShownType is the artifact type name typ as a text shows it: its first [maxShownType]
// characters.
func ShownType(typ string) string {
	if r := []rune(typ); len(r) > maxShownType {
		return string(r[:maxShownType])
	}
	return typ
}
