package provisioner

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/qoryai/forager/accesskey"
)

// The refusals of Qory Apiary the pool acts on, each matched with errors.Is against the
// [*Refusal] a request returns. A signed refusal matches only for a request whose schema
// gives it, with its status and its code: claim.schema.json, dispatched.schema.json,
// handover.schema.json, release.schema.json and prebuilt.schema.json of provisioner/v1.
var (
	// ErrForbidden is the signed 403 forbidden: a request outside the key's rights, or a
	// dispatch that is unknown or of another node pool.
	ErrForbidden = errors.New("403 forbidden")
	// ErrNotFound is the signed 404 not_found: a route Qory Apiary does not serve.
	ErrNotFound = errors.New("404 not_found")
	// ErrGone is the signed 410 gone: the dispatch ended, or, to a handover, the run
	// could not be formed and Qory Apiary ended it.
	ErrGone = errors.New("410 gone")
	// ErrHandedOver is a handover's signed 409 handed_over: the run was handed over
	// before, through this or another dispatch. Its run key is never given again.
	ErrHandedOver = errors.New("409 handed_over")
	// ErrLeaseExpired is the signed 409 lease_expired: the dispatch is stale, its lease
	// lapsed before its handover.
	ErrLeaseExpired = errors.New("409 lease_expired")
	// ErrNotDispatched is a handover's signed 409 not_dispatched: it came before
	// dispatched.
	ErrNotDispatched = errors.New("409 not_dispatched")
	// ErrNoGateway is the signed 409 no_gateway of a claim or a handover: the node pool
	// has no gateway a run could be registered through. After a handover's, the dispatch
	// stays claimed until its lease lapses.
	ErrNoGateway = errors.New("409 no_gateway")
	// ErrNotPrebuild is a prebuilt's signed 409 not_prebuild: the dispatch's step is not
	// prebuild.
	ErrNotPrebuild = errors.New("409 not_prebuild")
	// ErrNotHandedOver is a prebuilt's signed 409 not_handed_over: the dispatch was not
	// handed over yet.
	ErrNotHandedOver = errors.New("409 not_handed_over")
	// ErrRunMismatch is a prebuilt's signed 409 run_mismatch: its run id is not the
	// dispatch's run.
	ErrRunMismatch = errors.New("409 run_mismatch")
	// ErrUnavailable is a handover's signed 503 unavailable: Qory Apiary could not make
	// the run's inputs or its run key, and the handover took no effect. The handover is
	// returned after the one try; the contract lets it be sent again while the lease
	// lasts.
	ErrUnavailable = errors.New("503 unavailable")
	// ErrRateLimited is the signed 429 rate_limited; [Refusal.RetryAfter] is the wait it
	// asks for. An unsigned 429 is not one: it is [ErrUnsigned].
	ErrRateLimited = errors.New("429 rate_limited")
	// ErrUnsigned is an answer within its bound that is not signed under the pin, still
	// so after every try: a 401, which is always unsigned and never means the key is
	// revoked, a 413, 415 or 400 decided before the signature, or any other answer that
	// does not verify. It changes no state.
	ErrUnsigned = errors.New("an answer not signed under a key of apiary_public_key")
	// ErrTooLarge is an answer larger than its bound, whatever its status, still so after
	// every try: it is refused before its signature is read, so nothing says Qory Apiary
	// sent it, and it changes no state.
	ErrTooLarge = errors.New("an answer refused for its size before its signature was read")
	// ErrUnreachable is no answer from Qory Apiary after every try.
	ErrUnreachable = errors.New("Qory Apiary could not be reached")
	// ErrAnswerUnusable is a signed answer within its bound that breaks the contract: a
	// success status the request is not given, a body that is not one JSON document or
	// breaks its schema, another revision than the one sent, another run than the one
	// claimed, or inputs over their bound. Nothing in it is used, and the request is not
	// sent again, since Qory Apiary acted on it.
	ErrAnswerUnusable = errors.New("Qory Apiary's answer breaks the contract, so nothing in it is used")
)

// codeOf is the status and the code of each signed refusal above.
var codeOf = map[error]struct {
	status int
	code   string
}{
	ErrForbidden:     {http.StatusForbidden, "forbidden"},
	ErrNotFound:      {http.StatusNotFound, "not_found"},
	ErrGone:          {http.StatusGone, "gone"},
	ErrHandedOver:    {http.StatusConflict, "handed_over"},
	ErrLeaseExpired:  {http.StatusConflict, "lease_expired"},
	ErrNotDispatched: {http.StatusConflict, "not_dispatched"},
	ErrNoGateway:     {http.StatusConflict, "no_gateway"},
	ErrNotPrebuild:   {http.StatusConflict, "not_prebuild"},
	ErrNotHandedOver: {http.StatusConflict, "not_handed_over"},
	ErrRunMismatch:   {http.StatusConflict, "run_mismatch"},
	ErrUnavailable:   {http.StatusServiceUnavailable, "unavailable"},
	ErrRateLimited:   {http.StatusTooManyRequests, "rate_limited"},
}

// kind is which request a refusal answers.
type kind int

const (
	kindClaim kind = iota
	kindDispatched
	kindHandover
	kindRelease
	kindPrebuilt
)

// refusalsOf are the signed refusals each request's schema gives, in the order Qory
// Apiary decides them, with the 404 of a route it does not serve and the 429 every
// request may get, refusal.schema.json.
var refusalsOf = map[kind][]error{
	kindClaim:      {ErrForbidden, ErrNoGateway, ErrNotFound, ErrRateLimited},
	kindDispatched: {ErrForbidden, ErrNotFound, ErrGone, ErrLeaseExpired, ErrRateLimited},
	// A handover's 410 gone comes twice in its order: a dispatch that ended, third, and
	// a run Qory Apiary cannot form, after 409 no_gateway.
	kindHandover: {ErrForbidden, ErrNotFound, ErrGone, ErrHandedOver, ErrLeaseExpired, ErrNotDispatched, ErrNoGateway, ErrGone, ErrUnavailable, ErrRateLimited},
	kindRelease:  {ErrForbidden, ErrNotFound, ErrGone, ErrLeaseExpired, ErrRateLimited},
	kindPrebuilt: {ErrNotFound, ErrForbidden, ErrGone, ErrLeaseExpired, ErrNotPrebuild, ErrNotHandedOver, ErrRunMismatch, ErrRateLimited},
}

// Refusal is an answer that is not the one a request wants: its status, whether it was
// signed under the pin or refused for its size first, and the code of a signed one's
// body. errors.Is matches it against [ErrForbidden] and the other signed refusals by
// its request, its status and its code, against [ErrUnsigned] when it is within its
// bound and not signed, and against [ErrTooLarge] when it is over its bound. Its text
// names the request, the status and the code, never a body, a header or a secret.
type Refusal struct {
	// What is the request, as an error names it: "the claim", "the handover of dp_…".
	What string
	// Status is the answer's HTTP status.
	Status int
	// Signed is whether the answer's signature verified under the pin. A code is read
	// from a signed answer alone.
	Signed bool
	// Code is a signed answer's refusal code, refusal.schema.json's error, or "" when its
	// body holds none.
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
	// server error. Such a try may have taken effect, so a handover's 409 handed_over
	// may answer a handover whose 200 was lost.
	Resent bool

	kind kind
}

func (r *Refusal) Error() string {
	switch {
	case r.TooLarge:
		return fmt.Sprintf("%s: Qory Apiary's answer, %d, is larger than %d bytes, so it was refused before its signature was read", r.What, r.Status, r.Limit)
	case !r.Signed && r.Status == http.StatusUnauthorized:
		return r.What + ": Qory Apiary answered 401 unauthorized, which is never taken as the access key revoked"
	case !r.Signed:
		return fmt.Sprintf("%s: Qory Apiary answered %d, not signed under a key of apiary_public_key, so it is not used", r.What, r.Status)
	case r.Code == "":
		return fmt.Sprintf("%s: Qory Apiary answered %d", r.What, r.Status)
	case r.RetryAfter > 0:
		return fmt.Sprintf("%s: Qory Apiary answered %d %s, and asks to wait %s", r.What, r.Status, r.Code, r.RetryAfter)
	}
	return fmt.Sprintf("%s: Qory Apiary answered %d %s", r.What, r.Status, r.Code)
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
// body, and a code that holds what looks like a secret, names none. A 429's Retry-After
// is read from a second to an hour.
func (c *Client) refused(what string, k kind, ans *answer) error {
	if ans.status >= 200 && ans.status < 300 {
		return unusable(what, fmt.Sprintf("it is a %d, which the contract does not give this request", ans.status))
	}
	r := &Refusal{What: what, Status: ans.status, Signed: ans.signed, Resent: ans.resent, kind: k}
	var doc struct {
		Error string `json:"error"`
	}
	if readAnswer("refusal.schema.json", ans.body, &doc) == nil && !accesskey.ContainsSecret(doc.Error) {
		r.Code = doc.Error
	}
	if ans.status == http.StatusTooManyRequests {
		r.RetryAfter = retryAfter(ans.retryAfter, c.now())
	}
	return r
}

// unusable is [ErrAnswerUnusable] for the request what, and why: a fixed text that may
// name ids, statuses and revisions, never another value of the answer's body.
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
