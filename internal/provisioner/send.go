package provisioner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// request is one request of the pool's: what it is, as an error shows it, which
// refusals apply, its path under [Client.URL], the schema its body is checked against
// before it is sent, the bound of its answer's 200, its timeout, and its body with the
// time set. Every request is a POST.
type request struct {
	what    string
	kind    kind
	path    string
	schema  string
	limit   int64
	timeout time.Duration
	body    func(time string) any
}

// answer is an answer to one request: its status, its body, whether its signature
// verified under the pin, its Retry-After, and whether it was over its bound, limit
// bytes, in which case neither its body nor its signature is read.
type answer struct {
	status     int
	body       []byte
	signed     bool
	retryAfter string
	tooLarge   bool
	limit      int64
	// resent is whether the answer is to a try sent after one whose answer was lost,
	// see [Refusal.Resent].
	resent bool
}

// retryLine is the line logged for a try sent again.
const retryLine = "Qory Apiary's response could not be used, so the request is sent again after a wait"

// do sends req until a try gets a signed status below 500 from Qory Apiary, up to
// [tries] tries. Each try is built anew, its time from
// [Client.nextTime], so its bytes and its signature are new. A try fails with no
// answer, an answer over its bound or not signed under the pin, whatever its status, a
// 429 among them, or any other signed server error: it changes nothing, and it is sent
// again after a wait that doubles from [firstWait]. After the last try, the failure is
// the error. Once ctx is done, its error is the error, and nothing more is logged or
// sent. The body of every answer it does not return is cleared.
func (c *Client) do(ctx context.Context, req request) (*answer, error) {
	b, err := json.Marshal(req.body(""))
	if err != nil {
		return nil, fmt.Errorf("%s: the request could not be made", req.what)
	}
	id := http.MethodPost + " " + c.URL + req.path + "\n" + string(b)
	if containsSecret(id) {
		return nil, fmt.Errorf("%s: %w", req.what, errSecretInRequest)
	}
	wait := firstWait
	for try := 1; ; try++ {
		t, err := c.nextTime(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", req.what, err)
		}
		body, err := c.build(req, t)
		if err != nil {
			return nil, err
		}
		ans, err := c.send(ctx, req, body)
		var failure error
		switch {
		case err != nil:
			failure = err
		case ans.tooLarge:
			failure = &Refusal{What: req.what, Status: ans.status, TooLarge: true, Limit: ans.limit, Resent: try > 1, kind: req.kind}
		case !ans.signed:
			failure = &Refusal{What: req.what, Status: ans.status, Resent: try > 1, kind: req.kind}
		case ans.status >= 500:
			ans.resent = try > 1
			failure = c.refused(req.what, req.kind, ans)
		default:
			ans.resent = try > 1
			return ans, nil
		}
		if ans != nil {
			clear(ans.body)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", req.what, err)
		}
		if try == tries {
			return nil, failure
		}
		if c.Log != nil {
			c.Log.Warn(retryLine, slog.String("request", req.what), slog.Duration("wait", wait), slog.String("cause", shown(failure.Error())))
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, fmt.Errorf("%s: %w", req.what, err)
		}
		wait *= 2
	}
}

// build is req's body with the time t, checked for what looks like a secret and
// against its schema before it is sent. Its error contains the request and the
// schema's name, never a value.
func (c *Client) build(req request, t time.Time) ([]byte, error) {
	b, err := json.Marshal(req.body(contracts.FormatTime(t)))
	if err != nil {
		return nil, fmt.Errorf("%s: the request could not be made", req.what)
	}
	if containsSecret(string(b)) {
		return nil, fmt.Errorf("%s: %w", req.what, errSecretInRequest)
	}
	s, err := schema(req.schema)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", req.what, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil || s.Validate(doc) != nil {
		return nil, fmt.Errorf("%s: the request breaks %s, so it is not sent", req.what, req.schema)
	}
	return b, nil
}

// send signs one POST of body to req.path and sends it, then reads the answer: a 200's
// body up to req.limit, any other up to [MaxAnswer]. A larger answer, whatever its
// status, is marked too large, and neither its body nor its signature is read. A 401 is
// never signed; any other answer is checked under the pin, bound to this request's
// signature, with each signed header sent at most once. An error is no answer,
// [ErrUnreachable]; it contains the request and never a header, a body or a secret.
func (c *Client) send(ctx context.Context, req request, body []byte) (*answer, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+req.path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: the request could not be made", req.what)
	}
	sig, err := c.Key.SignRequest(accesskey.Request{
		AccessKeyID: c.KeyID, InstanceID: c.InstanceID, Method: http.MethodPost, Target: hr.URL.RequestURI(), Body: body,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: the request could not be signed", req.what)
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		hr.Header.Set("User-Agent", c.UserAgent)
	}
	hr.Header.Set(accesskey.HeaderAccessKeyID, c.KeyID)
	hr.Header.Set(accesskey.HeaderInstanceID, c.InstanceID)
	hr.Header.Set(headerContractVersion, strconv.Itoa(contracts.ProvisionerRevision))
	hr.Header.Set(accesskey.HeaderSignature, sig)
	hc := http.Client{}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.Timeout = req.timeout
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(hr)
	if err != nil {
		return nil, unreachable(req.what, err)
	}
	defer resp.Body.Close()
	limit := int64(MaxAnswer)
	if resp.StatusCode == http.StatusOK {
		limit = req.limit
	}
	b, tooLarge, err := readLimited(resp.Body, resp.ContentLength, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: its response could not be read", req.what, ErrUnreachable)
	}
	if tooLarge {
		return &answer{status: resp.StatusCode, tooLarge: true, limit: limit}, nil
	}
	ans := &answer{status: resp.StatusCode, body: b, retryAfter: resp.Header.Get("Retry-After")}
	if resp.StatusCode == http.StatusUnauthorized {
		return ans, nil
	}
	one := func(name string) (string, bool) {
		v := resp.Header.Values(name)
		return strings.Join(v, ""), len(v) <= 1
	}
	signature, ok1 := one(accesskey.HeaderSignature)
	conf, ok2 := one(accesskey.HeaderConfiguration)
	run, ok3 := one(accesskey.HeaderRunConfiguration)
	ans.signed = ok1 && ok2 && ok3 && c.Pin.VerifyAnswer(accesskey.Answer{
		Status: resp.StatusCode, RequestSignature: sig, Body: b, Configuration: conf, RunConfiguration: run,
	}, signature)
	return ans, nil
}

// unreachable is [ErrUnreachable] for the request what, with the transport's cause,
// unless the cause's text contains what looks like a secret, which leaves it out.
func unreachable(what string, err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	if containsSecret(err.Error()) {
		return fmt.Errorf("%s: %w", what, ErrUnreachable)
	}
	return fmt.Errorf("%s: %w: %w", what, ErrUnreachable, err)
}

// readLimited reads r, whose Content-Length is length or -1, up to limit bytes, into
// one buffer it makes at the start, so a growing read leaves no copy of the body
// behind. tooLarge is a body over limit, which it clears and returns none of.
func readLimited(r io.Reader, length, limit int64) (b []byte, tooLarge bool, err error) {
	size := limit + 1
	if length >= 0 && length < size {
		size = length + 1
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(r, buf)
	switch {
	case err == nil && int64(n) > limit:
		clear(buf)
		return nil, true, nil
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return buf[:n:n], false, nil
	}
	clear(buf)
	return nil, false, err
}

// errNotOneDocument is a body that is not one JSON document with valid UTF-8 and no
// member twice.
var errNotOneDocument = errors.New("it is not one JSON document with valid UTF-8 and no member twice")

// readAnswer reads body, a signed answer's, into out: one JSON document with valid
// UTF-8 and no member twice in any object, which the schema name of provisioner/v1
// accepts. Its error contains the schema's name, never a value of body.
func readAnswer(name string, body []byte, out any) error {
	s, err := schema(name)
	if err != nil {
		return err
	}
	return readChecked(s, name, body, out)
}

// readChecked reads body into out: one JSON document with valid UTF-8 and no member
// twice in any object, which s, the schema name, accepts. The schema checks the bytes
// before they are decoded, so a decoded "access":"" or "target":null is never read as
// no target. Its error contains the schema's name, never a value of body.
func readChecked(s *jsonschema.Schema, name string, body []byte, out any) error {
	if !jsontext.Value(body).IsValid() {
		return errNotOneDocument
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil || s.Validate(doc) != nil {
		return fmt.Errorf("it breaks %s", name)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("it breaks %s", name)
	}
	return nil
}

// schemas has each schema of provisioner/v1 compiled, by its name.
var schemas sync.Map

// schema is the schema name of provisioner/v1, compiled once.
func schema(name string) (*jsonschema.Schema, error) {
	if s, ok := schemas.Load(name); ok {
		return s.(*jsonschema.Schema), nil
	}
	s, err := contracts.CompileProvisioner(name)
	if err != nil {
		return nil, fmt.Errorf("the contract's schema %s does not compile", name)
	}
	schemas.Store(name, s)
	return s, nil
}

// errSecretInRequest is a request whose bytes contain what looks like a secret.
var errSecretInRequest = errors.New("ask Qory Apiary's instance admin: the request contains what looks like an access key secret, a run key, a run code or an enrolment code, so it is not sent")

// containsSecret reports whether s contains what looks like an access key secret, a run
// key, a run code or an enrolment code, qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_
// in any case, as accesskey.ContainsSecret reports.
func containsSecret(s string) bool {
	return accesskey.ContainsSecret(s) || strings.Contains(strings.ToLower(s), accesskey.CodePrefixRun)
}

// shown is s, or [hidden] when s contains what looks like a secret, for a value logged.
func shown(s string) string {
	if containsSecret(s) {
		return hiddenText
	}
	return s
}

// nextTime is the time of a request whose identity, its method, its URL and its body
// built without the time, is id: the clock's second, once that identity was not sent
// within it. Until then, it waits for the clock's next second, so it never runs ahead
// of the clock. Once ctx is done, its error is the error.
func (c *Client) nextTime(ctx context.Context, id string) (time.Time, error) {
	key := sha256.Sum256([]byte(id))
	for {
		t, wait := c.take(key)
		if wait == 0 {
			return t, nil
		}
		if err := c.sleep(ctx, wait); err != nil {
			return time.Time{}, err
		}
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
	}
}

// take reads the clock and, once the clock's second is another than the one last
// pruned, forgets every identity sent at another second, after the clock steps back
// too. When the identity key was not sent within the clock's second, it keeps key at
// that second and returns it, with no wait; otherwise it returns the wait until the
// clock's next second. The clock is read under mu, so a read made before another
// request's is never used after it.
func (c *Client) take(key [sha256.Size]byte) (t time.Time, wait time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	t = now.Truncate(time.Second)
	if !t.Equal(c.pruned) {
		for k, last := range c.last {
			if !t.Equal(last) {
				delete(c.last, k)
			}
		}
		c.pruned = t
	}
	if last, held := c.last[key]; held {
		return t, max(last.Add(time.Second).Sub(now), time.Millisecond)
	}
	if c.last == nil {
		c.last = map[[sha256.Size]byte]time.Time{}
	}
	c.last[key] = t
	return t, 0
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
