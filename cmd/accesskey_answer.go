package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qoryai/runner/accesskey"

	"github.com/qoryai/qory/internal/runnerdir"
)

// storedAnswer is enrolment-answer: the server's verified 201 to an enrolment as it came,
// with the request it answers and the server it came from. The answer's signature
// covers the request's proof, and the proof is the new key's signature over the code,
// the key and the name, so the record verifies for that key, that code and the server
// whose key the code names, and for no other.
type storedAnswer struct {
	Version          int                        `json:"version"`
	Server           string                     `json:"server"`
	Request          accesskey.EnrolmentRequest `json:"request"`
	Status           int                        `json:"status"`
	Body             []byte                     `json:"body"`
	Signature        string                     `json:"signature"`
	Configuration    string                     `json:"configuration"`
	RunConfiguration string                     `json:"run_configuration"`
}

// verify checks the record as the enrolment checked the answer when it came: the
// request's proof under its own public key, then the answer's signature under the
// server's key the code names.
func (s *storedAnswer) verify() (*accesskey.EnrolmentAnswer, error) {
	if s.Version != 1 {
		return nil, fmt.Errorf("version %d", s.Version)
	}
	if !s.Request.VerifyProof() {
		return nil, errors.New("the request's proof does not verify")
	}
	ans, err := s.Request.VerifyAnswer(accesskey.Answer{Status: s.Status, Body: s.Body, Configuration: s.Configuration, RunConfiguration: s.RunConfiguration}, s.Signature)
	if err != nil {
		return nil, err
	}
	if ans.Pin.Fixture() {
		return nil, errors.New("the answer lists the runner contract's published fixture key")
	}
	return ans, nil
}

// answerRecorder is an HTTP transport that keeps the one answer it carries, as it
// came: the body, at most what the enrolment reads, the status and the headers.
type answerRecorder struct {
	base   http.RoundTripper
	status int
	body   []byte
	header http.Header
}

func (a *answerRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	base := a.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, accesskey.MaxAnswer+1))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	a.status, a.body, a.header = resp.StatusCode, b, resp.Header.Clone()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, nil
}

// recordingClient is the enrolment's HTTP client, enrolClient or one with the runner's
// ten-second timeout, with a transport that keeps the answer.
func recordingClient() (*http.Client, *answerRecorder) {
	c := http.Client{Timeout: 10 * time.Second}
	if enrolClient != nil {
		c = *enrolClient
	}
	rec := &answerRecorder{base: c.Transport}
	c.Transport = rec
	return &c, rec
}

// record is the verified 201 the recorder kept, for req to server, checked again as it
// will be read back.
func (a *answerRecorder) record(server string, req *accesskey.EnrolmentRequest) (*storedAnswer, error) {
	sig := ""
	if v := a.header.Values(accesskey.HeaderSignature); len(v) == 1 {
		sig = v[0]
	}
	s := &storedAnswer{
		Version: 1, Server: server, Request: *req, Status: a.status, Body: a.body, Signature: sig,
		Configuration: a.header.Get(accesskey.HeaderConfiguration), RunConfiguration: a.header.Get(accesskey.HeaderRunConfiguration),
	}
	if _, err := s.verify(); err != nil {
		return nil, fmt.Errorf("the answer as kept: %w", err)
	}
	return s, nil
}

// keepAnswer writes the record into enrolment-answer.
func keepAnswer(dir runnerdir.Dir, s *storedAnswer) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return dir.WriteAnswer(b)
}

// answerKept is the enrolment of code with server the server has answered already, a
// 201 enrolment-answer keeps for the key access-key-secret.new or access-key-secret
// holds: that key and the verified answer. With no such answer it returns nil and no
// error, and an answer for another server, another code or a key neither file holds
// is none. A file that does not read as an answer, or an answer for this enrolment
// that does not verify, is an error: nothing may then post the enrolment again, since
// the server refuses a code it used and a key it enrolled.
func answerKept(dir runnerdir.Dir, server, code string) (*accesskey.Key, *accesskey.EnrolmentAnswer, error) {
	b, err := dir.ReadAnswer()
	if errors.Is(err, runnerdir.ErrNoAnswer) {
		return nil, nil, nil
	}
	path := dir.Path(runnerdir.AnswerFile)
	refused := func(err error) error {
		return fmt.Errorf("%s holds no answer of the server that verifies (%v), so the enrolment cannot be finished from it; nothing was changed: see https://github.com/qoryai/qory/blob/main/docs/run.md#replace-the-machines-key", path, err)
	}
	if err != nil {
		return nil, nil, refused(err)
	}
	var s storedAnswer
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, refused(err)
	}
	if s.Server != server || s.Request.Code != code {
		return nil, nil, nil
	}
	var key *accesskey.Key
	for _, read := range []func() (*accesskey.Key, error){dir.ReadNewSecret, dir.ReadSecret} {
		if k, err := read(); err == nil && k.PublicKey().String() == s.Request.PublicKey {
			key = k
			break
		}
	}
	if key == nil {
		return nil, nil, nil
	}
	ans, err := s.verify()
	if err != nil {
		return nil, nil, refused(err)
	}
	return key, ans, nil
}

// answeredFor reports whether enrolment-answer keeps an answer for the key whose public
// key is pub, verified or not: a key the server may hold active, which nothing moves
// aside.
func answeredFor(dir runnerdir.Dir, pub accesskey.PublicKey) bool {
	b, err := dir.ReadAnswer()
	if err != nil {
		return false
	}
	var s storedAnswer
	return json.Unmarshal(b, &s) == nil && s.Request.PublicKey == pub.String()
}
