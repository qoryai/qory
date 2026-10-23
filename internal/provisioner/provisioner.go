// Package provisioner is the pool program's client of Qory Apiary's provisioner
// contract, provisioner/v1, at contracts.ProvisionerRevision: it claims a dispatch for
// the node pool, reports that the dispatch's job started, gives a dispatch back, and
// uploads a dispatch's machine log. It also reads the documents of a run code's
// redemption, run/v1 documents.schema.json.
//
// Every request is signed with the pool's access key as Forager's accesskey signs one,
// with X-Qory-Access-Key-Id, X-Qory-Instance-Id, X-Qory-Signature-Ed25519 and
// X-Qory-Contract-Version set to contracts.ProvisionerRevision, and its body is checked
// against its schema before it is sent. Every answer is read up to its bound, then
// checked under the pinned server keys, then against its schema and its revision, before
// anything in it is used. A POST's time is the clock's, in whole seconds, and a try sent
// again is built anew with a later time, since Qory Apiary returns 401 for a signature
// it has seen. No redirect is followed and no cookie is kept.
//
// The run code a claim's answer contains is read into a [Hidden], which prints, logs
// and marshals as [hidden], and no string ever contains it. No error and no line the
// client logs contains the access key's secret, a run key or a run code.
package provisioner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"
)

// The steps a run's documents set, run/v1 documents.schema.json, that qory knows.
const (
	StepExplore  = "explore"
	StepPrebuild = "prebuild"
)

// WaitSeconds is how long Qory Apiary may hold a claim while no dispatch can be made,
// the most the contract allows. ClaimTimeout, a claim's timeout, is longer, so a held
// claim is not cut short; RequestTimeout is every other request's.
const (
	WaitSeconds    = 25
	ClaimTimeout   = 40 * time.Second
	RequestTimeout = 30 * time.Second
)

// The bounds of Qory Apiary's answers. A claim's 200 is read up to MaxClaimAnswer, far
// more than its members' limits add up to; every other answer, a refusal or a 204, up
// to MaxAnswer. A larger answer, whatever its status, is refused before its signature
// is read, nothing in it is used, and the request is sent again, built anew, as for an
// answer not signed.
const (
	MaxClaimAnswer = 64 << 10
	MaxAnswer      = 64 << 10
)

// The tries of one request: tries in all, the waits between them doubling from
// firstWait. A try that fails, with no answer, an answer over its bound or not signed
// under the pin, or a signed server error, is built anew and sent again after the wait.
const (
	tries     = 5
	firstWait = time.Second
)

// The routes of the provisioner contract.
const (
	routeClaim      = "/v1/pools/claim"
	routeDispatches = "/v1/dispatches/"
)

// headerContractVersion carries the revision a request implements. The signature does
// not cover it; a claim's answer has the revision in its signed body.
const headerContractVersion = "X-Qory-Contract-Version"

var (
	// dispatchIDShape is the provisioner contract's dispatch_id. An id is checked before
	// it is put in a path.
	dispatchIDShape = regexp.MustCompile(`^dp_[0-9a-hjkmnp-tv-z]{16}$`)
	// runCodeShape is a claim answer's run_code, which the schema checks on every other
	// member of the answer.
	runCodeShape = regexp.MustCompile(`^qecr_[0-9A-HJKMNP-TV-Z]{26}(\.[A-Za-z0-9_-]{22}){1,2}$`)
)

// maxRunCode is the most characters a run code of runCodeShape has.
const maxRunCode = len("qecr_") + 26 + 2*23

// runCodeStandIn takes the run code's place in the copy of a claim's answer that the
// schema checks: a value of the run code's shape that is no run code.
var runCodeStandIn = []byte(`"qecr_00000000000000000000000000.0000000000000000000000"`)

// Client is the pool's client of Qory Apiary's provisioner contract. It is safe for
// concurrent use, and is used by its pointer. Within one Client, while the clock does
// not step back, the same bytes are never sent twice: a request with the same method,
// URL and body but its time as one sent within the clock's current second, a try sent
// again or an identical call, waits for the clock's next second first; a distinct
// request does not wait.
type Client struct {
	// URL is Qory Apiary's origin, without a trailing slash.
	URL string
	// KeyID and Key are the pool's access key: its id and its secret.
	KeyID string
	Key   *accesskey.Key
	// Pin are Qory Apiary's public keys, which every answer is checked under.
	Pin accesskey.Pin
	// InstanceID is the pool program's instance id, signed into every request.
	InstanceID string
	// UserAgent is sent as User-Agent.
	UserAgent string
	// HTTP is the client; nil is one with no other setting. Its timeout is set per
	// request, and its redirect policy and cookie jar are not used.
	HTTP *http.Client
	// Now is the clock a request's time is read from; nil is time.Now. Now must advance
	// across Sleep, and must not block or call back into the Client.
	Now func() time.Time
	// Sleep waits d or until ctx is done; nil waits on a timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Log takes a line for each try sent again; nil logs nothing.
	Log *slog.Logger

	// mu guards last, the SHA-256 of each request identity sent within the clock's
	// second pruned and the second it was sent at, and pruned. See [Client.nextTime].
	mu     sync.Mutex
	last   map[[sha256.Size]byte]time.Time
	pruned time.Time
}

// Claimed is a claim's answer, claim-answer.schema.json, checked: signed under the pin,
// of the schema, and of the revision sent. It is a request for one machine. Machine is
// the type of machine the claim asks for, or empty when the answer has none. RunCode is
// the run code the machine redeems, [Hidden]; the caller passes it to the machine it
// starts for the dispatch alone, and clears it once passed, or once the dispatch is
// dropped. GatewayURL is the gateway the code is bound to. MaxLifetimeMinutes is the
// minutes after which the machine stops, counted from its start, 15 to 1470.
type Claimed struct {
	DispatchID string
	PoolID     string
	// LeaseUntil is when the dispatch's lease lapses: the first dispatched must come
	// before then.
	LeaseUntil         time.Time
	Machine            string
	RunCode            Hidden
	GatewayURL         string
	MaxLifetimeMinutes int
}

// Claim sends POST /v1/pools/claim, claim.schema.json, which Qory Apiary holds up to
// [WaitSeconds] while no dispatch can be made. A signed 204 is no dispatch: ok is
// false, and the pool claims again. A signed 200 is the dispatch claimed, once it is
// checked, see [Claimed]; one with another revision than the one sent is
// [ErrAnswerUnusable]. A refusal is a [*Refusal]: [ErrForbidden], [ErrNoGateway],
// [ErrNotFound], [ErrUnsupportedRevision] or [ErrRateLimited].
//
// The answer's bytes are cleared once read, and the run code is unquoted from them
// straight into its [Hidden]: the schema checks a copy in which a stand-in takes the
// run code's place, so no string ever contains it.
func (c *Client) Claim(ctx context.Context) (claimed Claimed, ok bool, err error) {
	const what = "the claim"
	ans, err := c.do(ctx, request{
		what: what, kind: kindClaim, path: routeClaim, schema: "claim.schema.json",
		limit: MaxClaimAnswer, timeout: ClaimTimeout,
		body: func(t string) any {
			return contracts.Claim{Version: 1, Time: t, WaitSeconds: WaitSeconds}
		},
	})
	if err != nil {
		return Claimed{}, false, err
	}
	defer clear(ans.body)
	switch ans.status {
	case http.StatusNoContent:
		return Claimed{}, false, nil
	case http.StatusOK:
	default:
		return Claimed{}, false, c.refused(what, kindClaim, ans)
	}
	claimed, err = readClaim(ans.body)
	if err != nil {
		return Claimed{}, false, claimUnusable(what, err)
	}
	return claimed, true, nil
}

// readClaim reads body, a claim's signed 200, into a [Claimed]. The run code is
// unquoted from body straight into the Hidden and checked against its shape first. The
// schema then checks a copy of body with [runCodeStandIn] in the run code's place. The
// caller clears body. Its error contains the schema's name or a revision, never a value
// of body.
func readClaim(body []byte) (Claimed, error) {
	const name = "claim-answer.schema.json"
	broken := fmt.Errorf("it breaks %s", name)
	if !jsontext.Value(body).IsValid() {
		return Claimed{}, errNotOneDocument
	}
	start, end, ok := memberSpan(body, "run_code")
	if !ok {
		return Claimed{}, broken
	}
	code, err := jsontext.AppendUnquote(make([]byte, 0, end-start), body[start:end])
	if err != nil || len(code) > maxRunCode || !runCodeShape.Match(code) {
		clear(code)
		return Claimed{}, broken
	}
	stood := slices.Concat(body[:start], runCodeStandIn, body[end:])
	defer clear(stood)
	var doc contracts.ClaimAnswer
	err = readAnswer(name, stood, &doc)
	var lease time.Time
	switch {
	case err != nil:
	case doc.Revision != contracts.ProvisionerRevision:
		err = otherRevision(doc.Revision)
	default:
		if lease, err = time.Parse(time.RFC3339, doc.LeaseUntil); err != nil {
			err = errors.New("its lease_until is not a time")
		}
	}
	if err != nil {
		clear(code)
		return Claimed{}, err
	}
	return Claimed{
		DispatchID: doc.DispatchID, PoolID: doc.PoolID, LeaseUntil: lease, Machine: doc.Machine,
		RunCode: Hide(code), GatewayURL: doc.GatewayURL, MaxLifetimeMinutes: doc.MaxLifetimeMinutes,
	}, nil
}

// Dispatched sends POST /v1/dispatches/<dispatchID>/dispatched, dispatched.schema.json:
// the dispatch's job started, and has the run code. A signed 204 is the answer. A
// refusal is a [*Refusal]: [ErrForbidden], [ErrNotFound], [ErrGone] or
// [ErrLeaseExpired], on each of which the job is stopped, [ErrUnsupportedRevision] or
// [ErrRateLimited].
func (c *Client) Dispatched(ctx context.Context, dispatchID string) error {
	return c.toDispatch(ctx, "dispatched for "+dispatchID, kindDispatched, dispatchID, "dispatched", "dispatched.schema.json", func(t string) any {
		return contracts.Dispatched{Version: 1, Time: t}
	})
}

// Release sends POST /v1/dispatches/<dispatchID>/release, release.schema.json: the pool
// gives the dispatch back before dispatched, for reason, display text of 1 to 200
// characters. A signed 204 is the answer: the dispatch ended, and the run code is
// unusable. A refusal is a [*Refusal]: [ErrForbidden], [ErrNotFound], [ErrGone] or
// [ErrLeaseExpired], each meaning the dispatch is over already, [ErrMachineExists],
// after dispatched or after the run code was redeemed, [ErrUnsupportedRevision] or
// [ErrRateLimited].
func (c *Client) Release(ctx context.Context, dispatchID, reason string) error {
	return c.toDispatch(ctx, "the release of "+dispatchID, kindRelease, dispatchID, "release", "release.schema.json", func(t string) any {
		return contracts.Release{Version: 1, Time: t, Reason: reason}
	})
}

// toDispatch sends a request about one dispatch whose answer is a signed 204.
func (c *Client) toDispatch(ctx context.Context, what string, k kind, dispatchID, action, schema string, body func(string) any) error {
	if !dispatchIDShape.MatchString(dispatchID) {
		return fmt.Errorf("%s: the dispatch id is not dp_ and 16 lower-case Crockford base32 characters", what)
	}
	ans, err := c.do(ctx, request{
		what: what, kind: k, path: routeDispatches + dispatchID + "/" + action, schema: schema,
		limit: MaxAnswer, timeout: RequestTimeout, body: body,
	})
	if err != nil {
		return err
	}
	defer clear(ans.body)
	if ans.status != http.StatusNoContent {
		return c.refused(what, k, ans)
	}
	return nil
}

// memberSpan is where the value of body's top-level member name is, quotes included:
// body[start:end]. body is one JSON object; ok is false when it has no such member or
// its value is not a string. The decoder reads body in place, from a bytes.Buffer, so
// the value is never copied.
func memberSpan(body []byte, name string) (start, end int, ok bool) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return 0, 0, false
	}
	for dec.PeekKind() == '"' {
		tok, err := dec.ReadToken()
		if err != nil {
			return 0, 0, false
		}
		if tok.String() != name {
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

// topLevelMember reports whether body, one JSON object, has the top-level member name.
// The decoder reads body in place, from a bytes.Buffer, and stops at that member's name:
// its value is not read.
func topLevelMember(body []byte, name string) bool {
	dec := jsontext.NewDecoder(bytes.NewBuffer(body))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return false
	}
	for dec.PeekKind() == '"' {
		tok, err := dec.ReadToken()
		if err != nil {
			return false
		}
		if tok.String() == name {
			return true
		}
		if err := dec.SkipValue(); err != nil {
			return false
		}
	}
	return false
}

// revisionText is the error text of an answer with the revision n, not the one sent.
func revisionText(n int) string {
	return fmt.Sprintf("it has the contract's revision %d, not %d", n, contracts.ProvisionerRevision)
}

// otherRevision is the error of a claim's answer with the revision it is set to, not the
// one sent: [revisionText].
type otherRevision int

func (n otherRevision) Error() string { return revisionText(int(n)) }

// claimUnusable is [ErrAnswerUnusable] for the claim what, by err, [readClaim]'s error.
// An answer of a revision other than [contracts.ProvisionerRevision] that passes the
// schema starts with its fix, update qory; every other cause has none. Today the
// schema's const takes revision 1 alone, so it stops any other revision first.
func claimUnusable(what string, err error) error {
	if _, ok := errors.AsType[otherRevision](err); ok {
		return fmt.Errorf("%s: update qory: %w: %s", what, ErrAnswerUnusable, err)
	}
	return unusable(what, err.Error())
}
