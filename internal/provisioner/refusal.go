package provisioner

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/qoryai/integrations/contracts"
)

// The refusals of Qory Apiary the pool acts on, each matched with errors.Is against the
// [*Refusal] a request returns. A signed refusal matches only for a request whose schema
// gives it, with its status and its code: claim.schema.json, dispatched.schema.json,
// release.schema.json and machine-log.schema.json of provisioner/v1, with
// refusal.schema.json.
var (
	// ErrForbidden is the signed 403 forbidden: a request outside the key's rights, or a
	// dispatch that is unknown or of another node pool.
	ErrForbidden = errors.New("403 forbidden")
	// ErrNotFound is the signed 404 not_found: a route Qory Apiary does not serve.
	ErrNotFound = errors.New("404 not_found")
	// ErrGone is the signed 410 gone: the dispatch ended.
	ErrGone = errors.New("410 gone")
	// ErrLeaseExpired is the signed 409 lease_expired: the dispatch is stale, since
	// neither dispatched nor the redemption of its run code came before its lease_until.
	ErrLeaseExpired = errors.New("409 lease_expired")
	// ErrMachineExists is a release's signed 409 machine_exists: Qory Apiary accepted
	// dispatched for the dispatch, or the machine redeemed its run code.
	ErrMachineExists = errors.New("409 machine_exists")
	// ErrMachineLogExists is a machine log's signed 409 machine_log_exists: the dispatch
	// has a machine log already.
	ErrMachineLogExists = errors.New("409 machine_log_exists")
	// ErrNoGateway is a claim's signed 409 no_gateway: the node pool has no gateway a
	// run code could be bound to.
	ErrNoGateway = errors.New("409 no_gateway")
	// ErrUnsupportedRevision is the signed 400 unsupported_contract_version: Qory Apiary
	// serves another revision of the provisioner contract than
	// contracts.ProvisionerRevision, the one the request sent.
	ErrUnsupportedRevision = errors.New("400 unsupported_contract_version")
	// ErrRateLimited is the signed 429 rate_limited; [Refusal.RetryAfter] is the wait it
	// asks for. An unsigned 429 is not one: it is [ErrUnsigned].
	ErrRateLimited = errors.New("429 rate_limited")
	// ErrUnsigned is an answer within its bound that is not signed under the pin, still
	// so after every try: a 401, which is always unsigned and never means the key is
	// revoked, a 413, 415 or 400 decided before the signature, or any other answer that
	// does not verify. It changes no state.
	ErrUnsigned = errors.New("an answer not signed under a key of apiary_public_key")
	// ErrTooLarge is an answer larger than its bound, whatever its status, still so after
	// every try: it is refused before its signature is read, so nothing shows that Qory
	// Apiary sent it, and it changes no state.
	ErrTooLarge = errors.New("an answer refused for its size before its signature was read")
	// ErrUnreachable is no answer from Qory Apiary after every try.
	ErrUnreachable = errors.New("Qory Apiary could not be reached")
	// ErrAnswerUnusable is a signed answer within its bound that breaks the contract: a
	// success status the request is not given, a body that is not one JSON document or
	// breaks its schema, or another revision than the one sent. Nothing in it is used,
	// and the request is not sent again, since Qory Apiary acted on it.
	ErrAnswerUnusable = errors.New("Qory Apiary's response breaks the contract, so nothing in it is used")
)

// codeOf is the status and the code of each signed refusal above.
var codeOf = map[error]struct {
	status int
	code   string
}{
	ErrForbidden:           {http.StatusForbidden, "forbidden"},
	ErrNotFound:            {http.StatusNotFound, "not_found"},
	ErrGone:                {http.StatusGone, "gone"},
	ErrLeaseExpired:        {http.StatusConflict, "lease_expired"},
	ErrMachineExists:       {http.StatusConflict, "machine_exists"},
	ErrMachineLogExists:    {http.StatusConflict, "machine_log_exists"},
	ErrNoGateway:           {http.StatusConflict, "no_gateway"},
	ErrUnsupportedRevision: {http.StatusBadRequest, "unsupported_contract_version"},
	ErrRateLimited:         {http.StatusTooManyRequests, "rate_limited"},
}

// kind is which request a refusal answers.
type kind int

const (
	kindClaim kind = iota
	kindDispatched
	kindRelease
	kindMachineLog
)

// refusalsOf are the signed refusals each request's schema gives, in the order Qory
// Apiary decides them, refusal.schema.json, with the 400 unsupported_contract_version
// and the 404 of a route it does not serve before them and the 429 every request may
// get after them.
var refusalsOf = map[kind][]error{
	kindClaim:      {ErrUnsupportedRevision, ErrNotFound, ErrForbidden, ErrNoGateway, ErrRateLimited},
	kindDispatched: {ErrUnsupportedRevision, ErrNotFound, ErrForbidden, ErrGone, ErrLeaseExpired, ErrRateLimited},
	kindRelease:    {ErrUnsupportedRevision, ErrNotFound, ErrForbidden, ErrGone, ErrMachineExists, ErrLeaseExpired, ErrRateLimited},
	kindMachineLog: {ErrUnsupportedRevision, ErrNotFound, ErrForbidden, ErrMachineLogExists, ErrRateLimited},
}

// Refusal is an answer that is not the one a request wants: its status, whether it was
// signed under the pin or refused for its size first, and the code of a signed one's
// body. errors.Is matches it against [ErrForbidden] and the other signed refusals by
// its request, its status and its code, against [ErrUnsigned] when it is within its
// bound and not signed, and against [ErrTooLarge] when it is over its bound. Its text
// contains the request, the status and the code, never a body, a header or a secret.
type Refusal struct {
	// What is the request, as an error shows it: "the claim", "the release of dp_…".
	What string
	// Status is the answer's HTTP status.
	Status int
	// Signed is whether the answer's signature verified under the pin. A code is read
	// from a signed answer alone.
	Signed bool
	// Code is a signed answer's refusal code, refusal.schema.json's error, or "" when its
	// body has none.
	Code string
	// RetryAfter is the wait a signed 429 asks for, from a second to an hour; 0
	// otherwise.
	RetryAfter time.Duration
	// TooLarge is whether the answer was over its bound, Limit bytes, and so refused
	// before its signature was read.
	TooLarge bool
	Limit    int64
	// Resent is whether the request was sent before in the same call, after a try whose
	// answer was lost: no answer, one over its bound, one not signed under the pin, or a
	// server error. Such a try may have taken effect, so a release's 410 gone may answer
	// a release whose 204 was lost.
	Resent bool

	kind kind
}

func (r *Refusal) Error() string {
	switch {
	case r.TooLarge:
		return fmt.Sprintf("%s: Qory Apiary's response, %d, is larger than %d bytes, so qory stopped reading it before its signature was read", r.What, r.Status, r.Limit)
	case !r.Signed && r.Status == http.StatusUnauthorized:
		return r.What + ": Qory Apiary returned 401 unauthorized, which is never taken as the access key revoked"
	case !r.Signed:
		return fmt.Sprintf("%s: Qory Apiary returned %d, not signed under a key of apiary_public_key, so it is not used", r.What, r.Status)
	case r.Is(ErrUnsupportedRevision):
		return fmt.Sprintf("update qory: Qory Apiary takes another provisioner revision, and this qory sends revision %d (%s: %d %s)", contracts.ProvisionerRevision, r.What, r.Status, r.Code)
	case r.Code == "":
		return fmt.Sprintf("%s: Qory Apiary returned %d", r.What, r.Status)
	case r.RetryAfter > 0:
		return fmt.Sprintf("%s: Qory Apiary returned %d %s; qory takes a wait of %s from the Retry-After header, kept between 1 s and 1 h, or 1 s without a readable header", r.What, r.Status, r.Code, r.RetryAfter)
	}
	return fmt.Sprintf("%s: Qory Apiary returned %d %s", r.What, r.Status, r.Code)
}

// Is reports whether the refusal is target: [ErrTooLarge] for an answer over its bound,
// [ErrUnsigned] for one within it that is not signed, or a signed refusal the request's
// schema gives, with its status and its code.
func (r *Refusal) Is(target error) bool {
	switch target {
	case ErrTooLarge:
		return r.TooLarge
	case ErrUnsigned:
		return !r.Signed && !r.TooLarge
	}
	c, ok := codeOf[target]
	return ok && r.Signed && r.Status == c.status && r.Code == c.code && slices.Contains(refusalsOf[r.kind], target)
}

// refused is the [*Refusal] of a signed answer that is not the one wanted, or
// [ErrAnswerUnusable] for a success status the request is not given. Its code is read
// only from a body that is one JSON document refusal.schema.json accepts; any other
// body, and a code that contains what looks like a secret, gives none. A 429's Retry-After
// is read from a second to an hour.
func (c *Client) refused(what string, k kind, ans *answer) error {
	if ans.status >= 200 && ans.status < 300 {
		return unusable(what, fmt.Sprintf("it is a %d, which the contract does not give this request", ans.status))
	}
	r := &Refusal{What: what, Status: ans.status, Signed: ans.signed, Resent: ans.resent, kind: k}
	var doc struct {
		Error string `json:"error"`
	}
	if readAnswer("refusal.schema.json", ans.body, &doc) == nil && !containsSecret(doc.Error) {
		r.Code = doc.Error
	}
	if ans.status == http.StatusTooManyRequests {
		r.RetryAfter = retryAfter(ans.retryAfter, c.now())
	}
	return r
}

// unusable is [ErrAnswerUnusable] for the request what, and why: a fixed text that may
// contain ids, statuses and revisions, never another value of the answer's body.
func unusable(what, why string) error {
	return fmt.Errorf("%s: %w: %s", what, ErrAnswerUnusable, why)
}

// The bounds of a signed 429's Retry-After.
const (
	minRetryAfter = time.Second
	maxRetryAfter = time.Hour
)

// retryAfter reads Retry-After, seconds or an HTTP date, from a second to an hour; a
// value it cannot read is a second.
func retryAfter(v string, now time.Time) time.Duration {
	d := minRetryAfter
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
		d = time.Duration(min(n, int64(maxRetryAfter/time.Second))) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, minRetryAfter), maxRetryAfter)
}
