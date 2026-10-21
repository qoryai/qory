package provisioner

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/qoryai/integrations/contracts"
)

// maxRunKey is the most characters a handover's run_key has, handover-answer.schema.json.
const maxRunKey = 2048

var (
	// dispatchIDShape and runIDShape are the provisioner contract's dispatch_id and
	// run_id. An id is checked before it is put in a path or a body.
	dispatchIDShape = regexp.MustCompile(`^dp_[0-9a-hjkmnp-tv-z]{16}$`)
	runIDShape      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// runKeyShape is a handover's run_key, which the schema checks on every other
	// answer member.
	runKeyShape = regexp.MustCompile(`^qrk_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

// standIn takes the run key's place in the copy of a handover's answer the schema
// checks: a value of the run key's shape that is no run key.
var standIn = []byte(`"qrk_0.0"`)

// Handed is a handover's answer, handover-answer.schema.json, checked: signed under the
// pin, of the schema and its bounds, of the revision sent, and for the claimed run. The
// run key is [Hidden]; the caller clears it once the run has it, or once the dispatch is
// dropped. WorkItem is set for the step explore and nil for the step prebuild. Domain is
// the domain of the workspace the run is for, such as software. Target is nil, and
// Access empty, when the run has no target: a run with no target is given no access.
// The step prebuild always has a target. Access is "read" or "write" exactly when Target
// is set. RunSettings is the runtime and model settings Qory Apiary sent for the step
// explore, checked as [checkRunSettings] says, or nil. Inputs are the step's input as the
// answer's bytes, checked by the schema alone, which the run reads unchanged: for the
// step prebuild, exactly version and environment_ref.
type Handed struct {
	RunID       string
	RunKey      Hidden
	Step        string
	Domain      string
	Access      string
	Target      *contracts.Target
	WorkItem    *contracts.WorkItem
	RunSettings *contracts.RunSettings
	Inputs      []byte
}

// Handover sends POST /v1/dispatches/<dispatchID>/handover, handover.schema.json, for
// the job started for the dispatch of the claimed run runID. A signed 200 is the run key
// and the run's input, once checked, see [Handed]: its run_id must be runID, and its
// inputs at most contracts.MaxInputs bytes, compacted. A refusal is a [*Refusal], in
// handover.schema.json's order: [ErrForbidden], [ErrNotFound], [ErrGone] for a
// dispatch that ended, [ErrHandedOver], [ErrLeaseExpired], [ErrNotDispatched],
// [ErrNoGateway], [ErrGone] again for a run Qory Apiary cannot form and so ended,
// [ErrUnavailable], or [ErrRateLimited]. Either [ErrGone] is returned after one try,
// and the dispatch is over: nothing is released. A signed 503 unavailable is returned after one
// try: the handover took no effect, and the contract lets it be sent again while the
// lease lasts, as after a 409 no_gateway. ctx bounds every try, so a deadline at the
// lease's end stops them there.
//
// The answer's bytes are cleared once read, and the run key is unquoted from them
// straight into its [Hidden]: the schema checks a copy in which a stand-in holds the run
// key's place, so no string ever holds it.
func (c *Client) Handover(ctx context.Context, dispatchID, runID string) (Handed, error) {
	what := "the handover of " + dispatchID
	if !dispatchIDShape.MatchString(dispatchID) {
		return Handed{}, fmt.Errorf("%s: the dispatch id is not dp_ and 16 lower-case Crockford base32 characters", what)
	}
	if !runIDShape.MatchString(runID) {
		return Handed{}, fmt.Errorf("%s: the run id is not a UUID in the canonical lower-case form", what)
	}
	ans, err := c.do(ctx, request{
		what: what, kind: kindHandover, path: routeDispatches + dispatchID + "/handover", schema: "handover.schema.json",
		limit: contracts.MaxHandoverAnswer, timeout: RequestTimeout,
		body: func(t string) any { return contracts.Handover{Version: 1, Time: t} },
	})
	if err != nil {
		return Handed{}, err
	}
	defer clear(ans.body)
	if ans.status != http.StatusOK {
		return Handed{}, c.refused(what, kindHandover, ans)
	}
	h, err := readHandover(ans.body, runID)
	if err != nil {
		return Handed{}, unusable(what, err.Error())
	}
	return h, nil
}

// readHandover reads body, a handover's signed 200, into a [Handed] for the claimed run
// runID. The run key is unquoted from body straight into the Hidden and checked against
// its shape first. The schema then checks a copy of body with [standIn] in the run key's
// place, before the rest of the answer is decoded, since a decoded "access":"" or
// "target":null reads as no target. The caller clears body. Its error names the schema,
// an id or a revision, never a value of body otherwise.
func readHandover(body []byte, runID string) (Handed, error) {
	if !jsontext.Value(body).IsValid() {
		return Handed{}, errNotOneDocument
	}
	start, end, ok := runKeySpan(body)
	if !ok {
		return Handed{}, errors.New("it breaks handover-answer.schema.json")
	}
	key, err := jsontext.AppendUnquote(make([]byte, 0, end-start), body[start:end])
	if err != nil || len(key) > maxRunKey || !runKeyShape.Match(key) {
		clear(key)
		return Handed{}, errors.New("it breaks handover-answer.schema.json")
	}
	stood := slices.Concat(body[:start], standIn, body[end:])
	const name = "handover-answer.schema.json"
	var doc contracts.HandoverAnswer
	err = readAnswer(name, stood, &doc)
	clear(stood)
	var inputs bytes.Buffer
	switch {
	case err != nil:
	case doc.Revision != Revision:
		err = errors.New(revisionText(doc.Revision))
	case doc.RunID != runID:
		err = fmt.Errorf("it is for the run %s, not the claimed run %s", doc.RunID, runID)
	case json.Compact(&inputs, doc.Inputs) != nil || inputs.Len() > contracts.MaxInputs:
		err = fmt.Errorf("its inputs are larger than %d bytes", contracts.MaxInputs)
	case doc.Domain == "":
		err = fmt.Errorf("it breaks %s", name)
	default:
		err = checkTarget(name, doc.Step, doc.Access, doc.Target)
		if err == nil {
			err = checkRunSettings(name, doc.Step, doc.RunSettings)
		}
	}
	if err != nil {
		clear(key)
		return Handed{}, err
	}
	return Handed{
		RunID: doc.RunID, RunKey: Hide(key), Step: doc.Step, Domain: doc.Domain, Access: doc.Access,
		Target: doc.Target, WorkItem: doc.WorkItem, RunSettings: doc.RunSettings, Inputs: doc.Inputs,
	}, nil
}

// checkRunSettings checks rs, the run_settings of an answer of the schema name already
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

// runKeySpan is where the value of body's top-level run_key member is, quotes included:
// body[start:end]. body is one JSON object; ok is false when it has no such member. The
// decoder reads body in place, from a bytes.Buffer, so the value is never copied.
func runKeySpan(body []byte) (start, end int, ok bool) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return 0, 0, false
	}
	for dec.PeekKind() == '"' {
		name, err := dec.ReadToken()
		if err != nil {
			return 0, 0, false
		}
		if name.String() != "run_key" {
			if err := dec.SkipValue(); err != nil {
				return 0, 0, false
			}
			continue
		}
		v, err := dec.ReadValue()
		if err != nil || v.Kind() != '"' {
			return 0, 0, false
		}
		end := int(dec.InputOffset())
		return end - len(v), end, true
	}
	return 0, 0, false
}

// revisionText says an answer names the revision n, not the one sent.
func revisionText(n int) string {
	return fmt.Sprintf("it names the contract's revision %d, not %d", n, Revision)
}
