package provisioner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The names of run/v1's schemas of the next-session request and its answer.
const (
	nextSessionRequestSchema = "next-session-request.schema.json"
	nextSessionAnswerSchema  = "next-session-answer.schema.json"
)

// compiledAnswer is run/v1 next-session-answer.schema.json, compiled once.
var compiledAnswer = sync.OnceValues(func() (*jsonschema.Schema, error) {
	s, err := contracts.CompileRun(nextSessionAnswerSchema)
	if err != nil {
		return nil, fmt.Errorf("the contract's schema %s does not compile", nextSessionAnswerSchema)
	}
	return s, nil
})

// compiledRequest is run/v1 next-session-request.schema.json, compiled once.
var compiledRequest = sync.OnceValues(func() (*jsonschema.Schema, error) {
	s, err := contracts.CompileRun(nextSessionRequestSchema)
	if err != nil {
		return nil, fmt.Errorf("the contract's schema %s does not compile", nextSessionRequestSchema)
	}
	return s, nil
})

// NextSessionBody is the body of the next-session request after the session numbered
// session by Forager, which ended in state with reason, "" for none: run/v1
// next-session-request.schema.json, checked against that schema before it is sent.
func NextSessionBody(session int, state, reason string) ([]byte, error) {
	req := contracts.NextSessionRequest{Session: session, State: state}
	if reason != "" {
		req.Reason = &reason
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	s, err := compiledRequest()
	if err != nil {
		return nil, err
	}
	var checked contracts.NextSessionRequest
	if err := readChecked(s, nextSessionRequestSchema, body, &checked); err != nil {
		return nil, err
	}
	return body, nil
}

// ReadNextSessionAnswer reads body, the answer to the next-session request, run/v1
// next-session-answer.schema.json. The schema checks the bytes before they are decoded.
// Then the rules that the schema states and cannot check are checked: the inputs,
// compacted, are at most contracts.MaxInputs bytes, and the model choice is checked as
// [checkRunSettings] checks run settings. The error contains the schema's name or a
// byte count, never a value of body.
func ReadNextSessionAnswer(body []byte) (contracts.NextSessionAnswer, error) {
	s, err := compiledAnswer()
	if err != nil {
		return contracts.NextSessionAnswer{}, err
	}
	var a contracts.NextSessionAnswer
	if err := readChecked(s, nextSessionAnswerSchema, body, &a); err != nil {
		return contracts.NextSessionAnswer{}, err
	}
	if a.Next == contracts.NextSessionKindEnd {
		return a, nil
	}
	var inputs bytes.Buffer
	if json.Compact(&inputs, a.Inputs) != nil || inputs.Len() > contracts.MaxInputs {
		return contracts.NextSessionAnswer{}, fmt.Errorf("its inputs are larger than %d bytes", contracts.MaxInputs)
	}
	if c := a.ModelChoice; c != nil {
		rs := &contracts.RunSettings{Runtime: c.Runtime, Model: c.Model, Effort: c.Effort, ProviderModels: c.ProviderModels, Tiers: c.Tiers}
		if err := checkRunSettings(nextSessionAnswerSchema, StepExplore, rs); err != nil {
			return contracts.NextSessionAnswer{}, err
		}
	}
	return a, nil
}
