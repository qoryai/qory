// Package provisionertest is a fake Qory Apiary for the pool program's tests: an
// httptest server that serves provisioner/v1, claim, dispatched, release and
// machine-log, and Forager's configuration document and a run's outcome.
//
// It checks every request as Qory Apiary does, in the contract's order: a body over a
// mebibyte is an unsigned 413, a POST of another content type an unsigned 415, a signing
// header sent twice an unsigned 400 bad_request, then the signature under the pool's
// key, failing which, or for a signature it has seen before, the answer is the unsigned
// 401; an outcome's same bytes again get the answer they got, as Forager's contract
// states. After that every answer is signed under [Server.Signer]: an instance id
// outside its pattern, a signed 400 bad_request; another revision than
// contracts.ProvisionerRevision on provisioner/v1, or than Forager's on its routes, a
// signed 400 unsupported_contract_version; a route it does not serve, a signed 404
// not_found; a body that breaks its schema, a signed 400 invalid_request; a time more than
// 300 seconds from [Server.Now], the unsigned 401. Then a reply scripted for the route
// with [Server.Script] answers, else the model below.
//
// The model keeps a queue of runs, which [Server.Offer] fills, and the dispatches it
// made of them. A claim takes the first run waiting into a new dispatch, with a run
// code and a gateway, or is held up to its wait_seconds and [Server.Hold] for one, then
// answered a signed 204. [Server.Redeem] stands for the machine's redemption of the run
// code, which another server serves. A dispatch is stale once its lease lapsed with
// neither dispatched nor the redemption before its lease_until, or once [Server.Expire]
// made it so, and it ends 15 minutes after its claim while its code is not redeemed.
// Each request's refusals follow the contract's order: dispatched gets 403 for an
// unknown dispatch, 410 for one that ended, then 409 lease_expired for a stale one; a
// release gets 403, 410, then 409 machine_exists after dispatched or the redemption,
// then 409 lease_expired; a machine log gets 403, then 409 machine_log_exists for a
// dispatch that has one, and is taken before or after the dispatch ends. A release
// ends the dispatch. A run whose dispatch was dispatched or redeemed may be given its
// outcome once; the same one again is answered as before, and another is 409
// outcome_given. Every request is recorded in order, [Server.Calls].
package provisionertest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	foragercontracts "github.com/qoryai/forager/contracts"
	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/provisioner"
)

// The ids the fake knows the pool by: its access key's id, the instance id
// [Server.Client] signs with, the node pool and its workspace.
const (
	KeyID       = "ak_f1xt0re000000000"
	InstanceID  = "i_AAAAAAAAAAAAAAAAAAAAAA"
	PoolID      = "np_f1xt0re000000000"
	WorkspaceID = "ws_f1xt0re000000000"
)

// Lease is a dispatch's lease when its [Run] sets none, Qory Apiary's.
const Lease = 600 * time.Second

// CodeLife is how long after its claim a dispatch whose run code is not redeemed ends.
const CodeLife = 15 * time.Minute

// The gateway and the lifetime of a dispatch whose [Run] sets none.
const (
	GatewayURL         = "https://gateway.example:8443"
	MaxLifetimeMinutes = 1470
)

// Window is how far a request's time may be from [Server.Now], either way.
const Window = 300 * time.Second

// maxBody is the largest request body the fake reads; a larger one is a 413.
const maxBody = 1 << 20

// Route is one endpoint of the fake.
type Route string

// The fake's routes.
const (
	RouteClaim         Route = "claim"
	RouteDispatched    Route = "dispatched"
	RouteRelease       Route = "release"
	RouteMachineLog    Route = "machine-log"
	RouteConfiguration Route = "configuration"
	RouteOutcome       Route = "outcome"
	// RouteUnknown is any other path, answered a signed 404 not_found once verified.
	RouteUnknown Route = "unknown"
)

// The run endpoint's path, under which an outcome is posted, and the events
// endpoint's, which the configuration document links to and the fake does not serve.
const (
	RunPath    = "/v1/runs"
	EventsPath = "/v1/events"
)

// Run is a run waiting in the queue, as [Server.Offer] puts it there. Each member left
// empty is filled in: a new run id, the step explore, the contract's documents fixture
// of the step, a new run code, [GatewayURL], [MaxLifetimeMinutes] and [Lease]. Machine
// empty sends no machine.
type Run struct {
	// RunID is the run the redemption of the code makes, which an outcome is posted for.
	RunID string
	Step  string
	// Documents are the run's documents, run/v1 documents.schema.json, which the
	// redemption of the code returns.
	Documents json.RawMessage
	Machine   string
	// RunCode is the run code the claim's answer contains, of the contract's shape:
	// qecr_, 26 upper-case Crockford base32 characters, a dot and 22 base64url
	// characters.
	RunCode            string
	GatewayURL         string
	MaxLifetimeMinutes int
	Lease              time.Duration
}

// Reply is one scripted answer, which [Server.Script] queues for a route. With Status
// 0 the model answers, and its 200's JSON body is passed to Edit first; with any other
// Status the model is left as it was, and the answer is Body, else a refusal of Code,
// else none.
type Reply struct {
	Status int
	// Code is a refusal's code, the body {"error": Code} when Body is empty.
	Code string
	Body string
	// Edit changes the model's 200 answer to a claim before it is signed: another
	// revision or a member the schema does not define, say.
	Edit func(doc map[string]any)
	// Unsigned leaves the answer unsigned, and WrongKey signs it under a key that is not
	// pinned.
	Unsigned bool
	WrongKey bool
	// RetryAfter is the answer's Retry-After, and Header more headers it has, each value
	// added after the answer's headers: a Location, a Set-Cookie, or a second
	// signature, say.
	RetryAfter string
	Header     http.Header
	// Pad is how many spaces follow the body, to make it larger than its bound.
	Pad int
	// Hold is how long the fake waits before it answers, or until the request is gone.
	Hold time.Duration
	// Drop closes the connection with no answer, after the model acted when Status is 0:
	// an answer lost on its way.
	Drop bool
}

// Call is one request the fake received, and what it answered.
type Call struct {
	Route Route
	// Target is the request's path and query, DispatchID the dispatch in the path, and
	// RunID the run in an outcome's path.
	Target     string
	DispatchID string
	RunID      string
	Header     http.Header
	Body       []byte
	// Time is the body's time, or a GET's X-Qory-Timestamp; zero when it has none.
	Time time.Time
	// Verified is whether the signature verified under the pool's key, and Replayed
	// whether the fake had seen it before.
	Verified bool
	Replayed bool
	// Status is the answer's status, and Signed whether it was signed under the pin;
	// Dropped is a connection closed with no answer.
	Status  int
	Signed  bool
	Dropped bool
}

// The states of a dispatch.
const (
	StateClaimed    = "claimed"
	StateDispatched = "dispatched"
	StateEnded      = "ended"
)

// Dispatch is one dispatch the fake made of a claimed run, and what became of it.
type Dispatch struct {
	ID         string
	Run        Run
	ClaimedAt  time.Time
	LeaseUntil time.Time
	State      string
	// Stale is a dispatch whose lease lapsed with neither dispatched nor the redemption
	// before its lease_until, and Redeemed one whose run code [Server.Redeem] redeemed.
	Stale    bool
	Redeemed bool
	// Dispatched is how many dispatched the fake accepted for it.
	Dispatched int
	// Release is the release that ended it, if one did, and MachineLog its machine log.
	Release    *contracts.Release
	MachineLog *contracts.MachineLog
}

// Outcome is a run's outcome as the fake recorded it.
type Outcome struct {
	RunID, State, Reason, Time string
}

// Server is the fake Qory Apiary. Set its fields before the first request.
type Server struct {
	// URL is the server's origin.
	URL string
	// Pool is the pool's access key, whose requests verify, under [KeyID].
	Pool *accesskey.Key
	// Signer signs every signed answer; [Server.Pin] pins it.
	Signer *accesskey.Key
	// Now is the clock a request's time is checked against and a lease runs on; nil is
	// time.Now.
	Now func() time.Time
	// Hold is the longest an empty claim is held, within its wait_seconds.
	Hold time.Duration

	srv     *httptest.Server
	other   *accesskey.Key
	closing chan struct{}

	mu         sync.Mutex
	queue      []Run
	wake       chan struct{}
	dispatches map[string]*Dispatch
	order      []string
	scripts    map[Route][]Reply
	calls      []Call
	seen       map[string]bool
	outcomes   map[string]outcome
}

// outcome is a run's outcome and the SHA-256 of the body that gave it.
type outcome struct {
	Outcome
	sum [sha256.Size]byte
}

// New starts a fake with a new pool key and a new signing key, holding empty claims up
// to a second, and closes it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		Pool: newKey(t), Signer: newKey(t), Hold: time.Second,
		other: newKey(t), closing: make(chan struct{}), wake: make(chan struct{}),
		dispatches: map[string]*Dispatch{}, scripts: map[Route][]Reply{}, seen: map[string]bool{}, outcomes: map[string]outcome{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	t.Cleanup(func() {
		close(s.closing)
		s.srv.Close()
	})
	return s
}

func newKey(t testing.TB) *accesskey.Key {
	t.Helper()
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Pin pins [Server.Signer].
func (s *Server) Pin() accesskey.Pin {
	return accesskey.Pin{{Alg: "ed25519", PublicKey: s.Signer.PublicKey().String()}}
}

// Client is a client of the fake, signed with the pool's key as [InstanceID], on the
// fake's clock.
func (s *Server) Client() *provisioner.Client {
	return &provisioner.Client{
		URL: s.URL, KeyID: KeyID, Key: s.Pool, Pin: s.Pin(), InstanceID: InstanceID,
		UserAgent: "qory-test", Now: s.Now,
	}
}

// Offer queues runs, filled in as [Run] describes, and returns them filled in.
func (s *Server) Offer(runs ...Run) []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Run
	for _, r := range runs {
		r = filled(r)
		s.queue = append(s.queue, r)
		out = append(out, r)
	}
	close(s.wake)
	s.wake = make(chan struct{})
	return out
}

// Script queues replies for route, each answering one request that passed the checks
// before the model.
func (s *Server) Script(route Route, replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[route] = append(s.scripts[route], replies...)
}

// Expire makes the dispatch stale, as a lease that lapsed with neither dispatched nor
// the redemption before it.
func (s *Server) Expire(dispatchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.dispatches[dispatchID]; d != nil && d.State == StateClaimed && !d.Redeemed {
		d.Stale = true
	}
}

// End ends the dispatch, as its work item closed or its run ended.
func (s *Server) End(dispatchID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.dispatches[dispatchID]; d != nil {
		d.State = StateEnded
	}
}

// Redeem redeems the dispatch's run code, as the machine's redemption does, and returns
// the run with its documents. ok is false, and nothing changes, when the code no longer
// works: the dispatch is unknown, ended or stale. A second redemption returns the same
// run.
func (s *Server) Redeem(dispatchID string) (run Run, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dispatches[dispatchID]
	if d == nil {
		return Run{}, false
	}
	s.refresh(d)
	if d.State == StateEnded || d.Stale {
		return Run{}, false
	}
	d.Redeemed = true
	return d.Run, true
}

// Calls are the requests the fake received, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.calls)
	for i := range out {
		out[i].Header = out[i].Header.Clone()
		out[i].Body = slices.Clone(out[i].Body)
	}
	return out
}

// Routes are the routes of the requests the fake received, in order.
func (s *Server) Routes() []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Route
	for _, c := range s.calls {
		out = append(out, c.Route)
	}
	return out
}

// Dispatches are the dispatches the fake made, in the order of their claims.
func (s *Server) Dispatches() []Dispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Dispatch
	for _, id := range s.order {
		out = append(out, *s.dispatches[id])
	}
	return out
}

// Dispatch is the dispatch id, if the fake made it.
func (s *Server) Dispatch(id string) (Dispatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dispatches[id]
	if d == nil {
		return Dispatch{}, false
	}
	return *d, true
}

// Outcomes are the outcomes the fake recorded, by run id.
func (s *Server) Outcomes() map[string]Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Outcome{}
	for id, o := range s.outcomes {
		out[id] = o.Outcome
	}
	return out
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var (
	dispatchPathShape = regexp.MustCompile(`^/v1/dispatches/([^/]+)/(dispatched|release|machine-log)$`)
	outcomePathShape  = regexp.MustCompile(`^` + RunPath + `/([^/]+)/outcome$`)
)

// fixtureDocuments are the contract's documents fixtures of the steps explore and
// prebuild, by step.
var fixtureDocuments = map[string]json.RawMessage{}

func init() {
	for _, step := range []string{provisioner.StepExplore, provisioner.StepPrebuild} {
		b, err := fs.ReadFile(contracts.RunFS, "fixtures/documents/"+step+".json")
		if err != nil {
			panic(err)
		}
		fixtureDocuments[step] = b
	}
}

// filled is r with every empty member filled in, as [Run] describes.
func filled(r Run) Run {
	if r.RunID == "" {
		r.RunID = newRunID()
	}
	if r.Step == "" {
		r.Step = provisioner.StepExplore
	}
	if r.Documents == nil {
		r.Documents = fixtureDocuments[r.Step]
	}
	if r.RunCode == "" {
		r.RunCode = newRunCode()
	}
	if r.GatewayURL == "" {
		r.GatewayURL = GatewayURL
	}
	if r.MaxLifetimeMinutes == 0 {
		r.MaxLifetimeMinutes = MaxLifetimeMinutes
	}
	if r.Lease == 0 {
		r.Lease = Lease
	}
	return r
}

// newRunID is a new run id, a version 4 UUID in the canonical lower-case form.
func newRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// crockford are the characters of an id after its prefix.
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// newDispatchID is a new dispatch id, dp_ and 16 lower-case Crockford base32 characters.
func newDispatchID() string {
	return "dp_" + randomOf(crockford, 16)
}

// newRunCode is a new run code: qecr_, 26 upper-case Crockford base32 characters, a dot
// and the 22 base64url characters of a fingerprint.
func newRunCode() string {
	fp := make([]byte, 16)
	_, _ = rand.Read(fp)
	return "qecr_" + randomOf(strings.ToUpper(crockford), 26) + "." + base64.RawURLEncoding.EncodeToString(fp)
}

// randomOf is n random characters of chars.
func randomOf(chars string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i, c := range b {
		b[i] = chars[int(c)%len(chars)]
	}
	return string(b)
}

// answer is what the fake answers: a status, a body, and the configuration digest its
// signature covers, or a refusal it signs.
type answer struct {
	status int
	body   []byte
	conf   string
	header http.Header
}

func refusal(status int, code string) answer {
	return answer{status: status, body: []byte(`{"error":"` + code + `"}`)}
}

// serve checks one request in the contract's order and answers it.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	route, id := routeOf(r)
	c := Call{Route: route, Target: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body}
	if route == RouteOutcome {
		c.RunID = id
	} else {
		c.DispatchID = id
	}
	sig := r.Header.Get(accesskey.HeaderSignature)
	c.Verified = r.Header.Get(accesskey.HeaderAccessKeyID) == KeyID && s.Pool.PublicKey().VerifyRequest(accesskey.Request{
		AccessKeyID: KeyID, InstanceID: r.Header.Get(accesskey.HeaderInstanceID), Method: r.Method,
		Target: r.URL.RequestURI(), Body: body, Timestamp: r.Header.Get(accesskey.HeaderTimestamp),
	}, sig)
	c.Time = timeOf(r, body)
	s.mu.Lock()
	c.Replayed = s.seen[sig]
	if c.Verified {
		s.seen[sig] = true
	}
	s.mu.Unlock()

	ans, rep, signed := s.decide(r, &c, route, id, body)
	if rep.Hold > 0 {
		select {
		case <-time.After(rep.Hold):
		case <-r.Context().Done():
		case <-s.closing:
		}
	}
	c.Status, c.Signed = ans.status, signed && !rep.Unsigned
	if rep.Drop {
		c.Status, c.Signed, c.Dropped = 0, false, true
	}
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	if rep.Drop {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	b := ans.body
	if rep.Pad > 0 {
		b = append(slices.Clone(b), bytes.Repeat([]byte(" "), rep.Pad)...)
	}
	for k, v := range ans.header {
		w.Header()[k] = v
	}
	if rep.RetryAfter != "" {
		w.Header().Set("Retry-After", rep.RetryAfter)
	}
	if ans.conf != "" {
		w.Header().Set(accesskey.HeaderConfiguration, ans.conf)
	}
	if signed && !rep.Unsigned {
		k := s.Signer
		if rep.WrongKey {
			k = s.other
		}
		w.Header().Set(accesskey.HeaderSignature, k.SignAnswer(accesskey.Answer{Status: ans.status, RequestSignature: sig, Body: b, Configuration: ans.conf}))
	}
	if len(b) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	for k, v := range rep.Header {
		for _, x := range v {
			w.Header().Add(k, x)
		}
	}
	w.WriteHeader(ans.status)
	_, _ = w.Write(b)
}

// decide is the answer to one request, the reply it follows, and whether the answer is
// signed.
func (s *Server) decide(r *http.Request, c *Call, route Route, id string, body []byte) (answer, Reply, bool) {
	switch {
	case len(body) > maxBody:
		return refusal(http.StatusRequestEntityTooLarge, "payload_too_large"), Reply{}, false
	case r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json":
		return refusal(http.StatusUnsupportedMediaType, "unsupported_media_type"), Reply{}, false
	case twice(r.Header):
		return refusal(http.StatusBadRequest, "bad_request"), Reply{}, false
	case !c.Verified || c.Replayed && route != RouteOutcome:
		return refusal(http.StatusUnauthorized, "unauthorized"), Reply{}, false
	case accesskey.CheckInstanceID(r.Header.Get(accesskey.HeaderInstanceID)) != nil:
		return refusal(http.StatusBadRequest, "bad_request"), Reply{}, true
	}
	revision := strconv.Itoa(contracts.ProvisionerRevision)
	if route == RouteConfiguration || route == RouteOutcome {
		revision = strconv.Itoa(foragercontracts.Revision)
	}
	if v := r.Header.Values("X-Qory-Contract-Version"); len(v) != 1 || v[0] != revision {
		return refusal(http.StatusBadRequest, "unsupported_contract_version"), Reply{}, true
	}
	if route == RouteUnknown || r.Method != methodOf(route) {
		return refusal(http.StatusNotFound, "not_found"), Reply{}, true
	}
	if r.Method == http.MethodPost && !validBody(route, body) {
		return refusal(http.StatusBadRequest, "invalid_request"), Reply{}, true
	}
	if c.Time.IsZero() || c.Time.Sub(s.now()).Abs() > Window {
		return refusal(http.StatusUnauthorized, "unauthorized"), Reply{}, false
	}
	s.mu.Lock()
	var rep Reply
	if q := s.scripts[route]; len(q) > 0 {
		rep, s.scripts[route] = q[0], q[1:]
	}
	s.mu.Unlock()
	if rep.Status != 0 {
		ans := answer{status: rep.Status, body: []byte(rep.Body)}
		if rep.Body == "" && rep.Code != "" {
			ans = refusal(rep.Status, rep.Code)
		}
		return ans, rep, true
	}
	ans := s.model(r.Context(), route, id, body)
	if rep.Edit != nil && ans.status == http.StatusOK {
		var doc map[string]any
		if json.Unmarshal(ans.body, &doc) == nil {
			rep.Edit(doc)
			ans.body, _ = json.Marshal(doc)
		}
	}
	return ans, rep, true
}

// twice reports whether a header the signature depends on is sent more than once.
func twice(h http.Header) bool {
	for _, name := range []string{accesskey.HeaderAccessKeyID, accesskey.HeaderInstanceID, accesskey.HeaderSignature, accesskey.HeaderTimestamp} {
		if len(h.Values(name)) > 1 {
			return true
		}
	}
	return false
}

// routeOf is the route of r's path, whatever its method, and the dispatch or the run
// in the path.
func routeOf(r *http.Request) (Route, string) {
	p := r.URL.Path
	switch {
	case p == "/v1/pools/claim":
		return RouteClaim, ""
	case p == "/.well-known/qory-configuration":
		return RouteConfiguration, ""
	}
	if m := dispatchPathShape.FindStringSubmatch(p); m != nil {
		return Route(m[2]), m[1]
	}
	if m := outcomePathShape.FindStringSubmatch(p); m != nil {
		return RouteOutcome, m[1]
	}
	return RouteUnknown, ""
}

// methodOf is the method a route is served for.
func methodOf(route Route) string {
	if route == RouteConfiguration {
		return http.MethodGet
	}
	return http.MethodPost
}

// timeOf is a POST body's time, or a GET's X-Qory-Timestamp; zero when it has none.
func timeOf(r *http.Request, body []byte) time.Time {
	if r.Method == http.MethodGet {
		sec, err := strconv.ParseInt(r.Header.Get(accesskey.HeaderTimestamp), 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(sec, 0).UTC()
	}
	var doc struct {
		Time string `json:"time"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, doc.Time)
	return t
}

// schemaOf is the schema each POST route's body must pass.
var schemaOf = map[Route]func() (*jsonschema.Schema, error){
	RouteClaim:      sync.OnceValues(func() (*jsonschema.Schema, error) { return contracts.CompileProvisioner("claim.schema.json") }),
	RouteDispatched: sync.OnceValues(func() (*jsonschema.Schema, error) { return contracts.CompileProvisioner("dispatched.schema.json") }),
	RouteRelease:    sync.OnceValues(func() (*jsonschema.Schema, error) { return contracts.CompileProvisioner("release.schema.json") }),
	RouteMachineLog: sync.OnceValues(func() (*jsonschema.Schema, error) { return contracts.CompileProvisioner("machine-log.schema.json") }),
	RouteOutcome:    sync.OnceValues(func() (*jsonschema.Schema, error) { return foragercontracts.Compile("run-outcome.schema.json") }),
}

// validBody reports whether body is one JSON document with no member twice, written
// as the contract writes integers, that the route's schema accepts.
func validBody(route Route, body []byte) bool {
	compile := schemaOf[route]
	if compile == nil || !jsontext.Value(body).IsValid() {
		return false
	}
	s, err := compile()
	if err != nil {
		panic(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil || s.Validate(doc) != nil {
		return false
	}
	return !hasFraction(doc)
}

// hasFraction reports whether a decoded document contains a number written with a
// fraction or an exponent, which the contract refuses for an integer.
func hasFraction(doc any) bool {
	switch v := doc.(type) {
	case json.Number:
		return strings.ContainsAny(string(v), ".eE")
	case map[string]any:
		for _, x := range v {
			if hasFraction(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if hasFraction(x) {
				return true
			}
		}
	}
	return false
}

// model is the model's answer to a request that passed every check.
func (s *Server) model(ctx context.Context, route Route, id string, body []byte) answer {
	switch route {
	case RouteClaim:
		return s.claim(ctx, body)
	case RouteConfiguration:
		return s.configuration()
	case RouteOutcome:
		return s.outcome(id, body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dispatches[id]
	if d == nil {
		return refusal(http.StatusForbidden, "forbidden")
	}
	if route == RouteMachineLog {
		if d.MachineLog != nil {
			return refusal(http.StatusConflict, "machine_log_exists")
		}
		var ml contracts.MachineLog
		_ = json.Unmarshal(body, &ml)
		d.MachineLog = &ml
		return answer{status: http.StatusNoContent}
	}
	s.refresh(d)
	if d.State == StateEnded {
		return refusal(http.StatusGone, "gone")
	}
	if route == RouteDispatched {
		if d.Stale {
			return refusal(http.StatusConflict, "lease_expired")
		}
		d.State = StateDispatched
		d.Dispatched++
		return answer{status: http.StatusNoContent}
	}
	switch {
	case d.State == StateDispatched || d.Redeemed:
		return refusal(http.StatusConflict, "machine_exists")
	case d.Stale:
		return refusal(http.StatusConflict, "lease_expired")
	}
	var rel contracts.Release
	_ = json.Unmarshal(body, &rel)
	d.State, d.Release = StateEnded, &rel
	return answer{status: http.StatusNoContent}
}

// refresh brings d to the fake's clock: a dispatch with neither dispatched nor the
// redemption goes stale at its lease_until, and one whose code is not redeemed ends
// [CodeLife] after its claim. The caller holds s.mu.
func (s *Server) refresh(d *Dispatch) {
	if d.State == StateEnded {
		return
	}
	now := s.now()
	if d.State == StateClaimed && !d.Redeemed && !now.Before(d.LeaseUntil) {
		d.Stale = true
	}
	if !d.Redeemed && !now.Before(d.ClaimedAt.Add(CodeLife)) {
		d.State = StateEnded
	}
}

// claim takes the first run waiting into a new dispatch, or holds the claim up to its
// wait_seconds and [Server.Hold] for one, then answers a signed 204.
func (s *Server) claim(ctx context.Context, body []byte) answer {
	var cl contracts.Claim
	_ = json.Unmarshal(body, &cl)
	hold := time.NewTimer(min(time.Duration(cl.WaitSeconds)*time.Second, s.Hold))
	defer hold.Stop()
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			defer s.mu.Unlock()
			run := s.queue[0]
			s.queue = s.queue[1:]
			now := s.now().UTC().Truncate(time.Second)
			d := &Dispatch{ID: newDispatchID(), Run: run, ClaimedAt: now, LeaseUntil: now.Add(run.Lease), State: StateClaimed}
			s.dispatches[d.ID] = d
			s.order = append(s.order, d.ID)
			b, _ := json.Marshal(contracts.ClaimAnswer{
				Version: 1, Revision: contracts.ProvisionerRevision, DispatchID: d.ID, PoolID: PoolID,
				LeaseUntil: contracts.FormatTime(d.LeaseUntil), Machine: run.Machine, RunCode: run.RunCode,
				GatewayURL: run.GatewayURL, MaxLifetimeMinutes: run.MaxLifetimeMinutes,
			})
			return answer{status: http.StatusOK, body: b, header: http.Header{"Cache-Control": {"no-store"}}}
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-hold.C:
			return answer{status: http.StatusNoContent}
		case <-ctx.Done():
			return answer{status: http.StatusNoContent}
		case <-s.closing:
			return answer{status: http.StatusNoContent}
		}
	}
}

// configuration is the configuration document of the pool's key, with its digest.
func (s *Server) configuration() answer {
	doc := map[string]any{
		"version": 1, "node_id": PoolID, "workspaces": []string{WorkspaceID},
		"events":            map[string]any{"url": s.URL + EventsPath, "types": []string{"*"}},
		"run":               map[string]any{"url": s.URL + RunPath},
		"apiary_public_key": s.Pin(),
	}
	b, _ := json.Marshal(doc)
	sum := sha256.Sum256(b)
	return answer{status: http.StatusOK, body: b, conf: "sha256=" + hex.EncodeToString(sum[:])}
}

// outcome records the outcome of a run whose dispatch was dispatched or redeemed: the
// same one again is answered as before, another is 409 outcome_given, and any other run
// is 404 not_found.
func (s *Server) outcome(runID string, body []byte) answer {
	var o struct{ State, Reason, Time string }
	_ = json.Unmarshal(body, &o)
	s.mu.Lock()
	defer s.mu.Unlock()
	started := false
	for _, d := range s.dispatches {
		started = started || d.Run.RunID == runID && (d.Dispatched > 0 || d.Redeemed)
	}
	if !started {
		return refusal(http.StatusNotFound, "not_found")
	}
	given := outcome{Outcome: Outcome{RunID: runID, State: o.State, Reason: o.Reason, Time: o.Time}, sum: sha256.Sum256(body)}
	if prev, ok := s.outcomes[runID]; ok && prev.sum != given.sum && (prev.State != given.State || prev.Reason != given.Reason) {
		return refusal(http.StatusConflict, "outcome_given")
	} else if !ok {
		s.outcomes[runID] = given
	}
	return answer{status: http.StatusOK, body: []byte(`{"version":1}`)}
}

// String is the dispatch's id, step and state, never its run code.
func (d Dispatch) String() string {
	return fmt.Sprintf("%s %s %s", d.ID, d.Run.Step, d.State)
}
