// Package provisioner is the pool program's client of Qory Apiary's provisioner
// contract, provisioner/v1, at revision 3: it claims a run for the node pool, says the
// run's job started, asks for the run key, gives a run back, and reports the image a
// prebuild built.
//
// Every request is signed with the pool's access key as Forager's accesskey signs one,
// with X-Qory-Access-Key-Id, X-Qory-Instance-Id, X-Qory-Signature-Ed25519 and
// X-Qory-Contract-Version 3, and its body is checked against its schema before it is
// sent. Every answer is read up to its bound, then checked under the pinned server keys,
// then against its schema and its revision, before anything in it is used. A POST's
// time is the clock's, in whole seconds, and a try sent again is built anew with a
// later time, since Qory Apiary refuses a signature it has seen. No redirect is
// followed and no cookie is kept.
//
// The run key a handover answers with is read into a [Hidden], which prints, logs and
// marshals as [hidden], and no string ever holds it. No error and no line the client
// logs holds the access key's secret or a run key.
package provisioner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"
)

// Revision is the revision of the provisioner contract the client speaks: the one its
// requests name in X-Qory-Contract-Version, and the one a claim's or a handover's
// answer must name. Qory Apiary serves this revision alone. Revision 3 makes a run's
// target optional for the step explore, and adds the handover answer's domain and
// run_settings.
const Revision = 3

// The steps the pool runs, as a claim names them.
const (
	StepExplore  = "explore"
	StepPrebuild = "prebuild"
)

// WaitSeconds is how long Qory Apiary may hold a claim while no run can be claimed, the
// most the contract allows. ClaimTimeout, a claim's timeout, is longer, so a held claim
// is not cut short; RequestTimeout is every other request's.
const (
	WaitSeconds    = 25
	ClaimTimeout   = 40 * time.Second
	RequestTimeout = 30 * time.Second
)

// The bounds of Qory Apiary's answers. A claim's 200 is read up to MaxClaimAnswer, far
// more than its members' limits add up to; a handover's 200 up to
// contracts.MaxHandoverAnswer, the contract's bound; every other answer, a refusal or a
// 204, up to MaxAnswer. A larger answer, whatever its status, is refused before its
// signature is read, nothing in it is used, and the request is sent again, built anew,
// as for an answer not signed.
const (
	MaxClaimAnswer = 64 << 10
	MaxAnswer      = 64 << 10
)

// The tries of one request: tries in all, the waits between them doubling from
// firstWait. A try that fails, with no answer, an answer over its bound or not signed
// under the pin, or a signed server error other than a handover's 503 unavailable, is
// built anew and sent again after the wait.
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
// not cover it; the answers whose content the revision bounds name it in their signed
// body.
const headerContractVersion = "X-Qory-Contract-Version"

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
// of the schema, and of the revision sent. WorkItem is set for the step explore and nil
// for the step prebuild; its URL and title are untrusted text. Target is nil, and Access
// empty, when the run has no target: a run with no target is given no access. The step
// prebuild always has a target. Access is "read" or "write" exactly when Target is set.
type Claimed struct {
	DispatchID string
	RunID      string
	PoolID     string
	// LeaseUntil is when the dispatch's lease lapses: its handover must be answered
	// before then.
	LeaseUntil time.Time
	Step       string
	Access     string
	Target     *contracts.Target
	WorkItem   *contracts.WorkItem
}

// Claim sends POST /v1/pools/claim, claim.schema.json, which Qory Apiary holds up to
// [WaitSeconds] while no run can be claimed. A signed 204 is no run: ok is false, and
// the pool claims again. A signed 200 is the run claimed, once it is checked, see
// [Claimed]; one that names another revision than the one sent is [ErrAnswerUnusable].
// A refusal is a [*Refusal]: [ErrForbidden], [ErrNoGateway], [ErrNotFound] or
// [ErrRateLimited].
func (c *Client) Claim(ctx context.Context) (claimed Claimed, ok bool, err error) {
	const what = "the claim"
	ans, err := c.do(ctx, request{
		what: what, kind: kindClaim, path: routeClaim, schema: "claim.schema.json",
		limit: MaxClaimAnswer, timeout: ClaimTimeout,
		body: func(t string) any { return contracts.Claim{Version: 1, Time: t, WaitSeconds: WaitSeconds} },
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
		return Claimed{}, false, unusable(what, err.Error())
	}
	return claimed, true, nil
}

// readClaim reads body, a claim's signed 200, into a [Claimed]. The schema checks the
// bytes before they are decoded, since a decoded "access":"" or "target":null reads as
// no target. Its error names the schema or a revision, never a value of body.
func readClaim(body []byte) (Claimed, error) {
	const name = "claim-answer.schema.json"
	var doc contracts.ClaimAnswer
	if err := readAnswer(name, body, &doc); err != nil {
		return Claimed{}, err
	}
	if doc.Revision != Revision {
		return Claimed{}, errors.New(revisionText(doc.Revision))
	}
	if err := checkTarget(name, doc.Step, doc.Access, doc.Target); err != nil {
		return Claimed{}, err
	}
	lease, err := time.Parse(time.RFC3339, doc.LeaseUntil)
	if err != nil {
		return Claimed{}, errors.New("its lease_until is not a time")
	}
	return Claimed{
		DispatchID: doc.DispatchID, RunID: doc.RunID, PoolID: doc.PoolID, LeaseUntil: lease,
		Step: doc.Step, Access: doc.Access, Target: doc.Target, WorkItem: doc.WorkItem,
	}, nil
}

// checkTarget checks again, on an answer of the schema name already checked, what the
// pool relies on: a run with no target has no access, a run with a target has read or
// write access, and the step prebuild has a target.
func checkTarget(name, step, access string, target *contracts.Target) error {
	switch {
	case target == nil && access != "",
		target != nil && access != "read" && access != "write",
		target == nil && step == StepPrebuild:
		return fmt.Errorf("it breaks %s", name)
	}
	return nil
}

// Dispatched sends POST /v1/dispatches/<dispatchID>/dispatched, dispatched.schema.json:
// the run's job started. It must come before the handover. A signed 204 is the answer.
// A refusal is a [*Refusal]: [ErrForbidden], [ErrNotFound], [ErrGone],
// [ErrLeaseExpired], on which the job is stopped, or [ErrRateLimited].
func (c *Client) Dispatched(ctx context.Context, dispatchID string) error {
	return c.toDispatch(ctx, "dispatched for "+dispatchID, kindDispatched, dispatchID, "dispatched", "dispatched.schema.json", func(t string) any {
		return contracts.Dispatched{Version: 1, Time: t}
	})
}

// Release sends POST /v1/dispatches/<dispatchID>/release, release.schema.json: the pool
// gives the run back, for reason, display text of 1 to 200 characters, and says whether
// it should wait for another claim. A signed 204 is the answer, after the handover too,
// where it ends the run. A refusal is a [*Refusal]: [ErrForbidden], [ErrNotFound],
// [ErrGone] or [ErrLeaseExpired], each meaning the dispatch is over already, or
// [ErrRateLimited].
func (c *Client) Release(ctx context.Context, dispatchID, reason string, retry bool) error {
	return c.toDispatch(ctx, "the release of "+dispatchID, kindRelease, dispatchID, "release", "release.schema.json", func(t string) any {
		return contracts.Release{Version: 1, Time: t, Reason: reason, Retry: retry}
	})
}

// Image is what a prebuild built, which [Client.Prebuilt] reports: the image pinned by
// its digest, its size in bytes, the environment_ref of the handover's inputs, and the
// commit it was built from.
type Image struct {
	Image          string
	Size           int64
	EnvironmentRef string
	Commit         string
}

// Prebuilt sends POST /v1/dispatches/<dispatchID>/prebuilt, prebuilt.schema.json: the
// prebuild of the run runID built img. A signed 204 is the answer: Qory Apiary recorded
// the image, and the run ended. A refusal is a [*Refusal], in prebuilt.schema.json's
// order: [ErrNotFound], [ErrForbidden], [ErrGone], [ErrLeaseExpired], [ErrNotPrebuild],
// [ErrNotHandedOver], [ErrRunMismatch], or [ErrRateLimited]; [Refusal.Resent] says
// whether a try before it may have taken effect.
func (c *Client) Prebuilt(ctx context.Context, dispatchID, runID string, img Image) error {
	what := "prebuilt for " + dispatchID
	if !runIDShape.MatchString(runID) {
		return fmt.Errorf("%s: the run id is not a UUID in the canonical lower-case form", what)
	}
	return c.toDispatch(ctx, what, kindPrebuilt, dispatchID, "prebuilt", "prebuilt.schema.json", func(t string) any {
		return contracts.Prebuilt{
			Version: 1, Time: t, RunID: runID, Image: img.Image, Size: img.Size,
			EnvironmentRef: img.EnvironmentRef, Commit: img.Commit,
		}
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
