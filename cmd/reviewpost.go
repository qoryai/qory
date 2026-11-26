package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/qoryai/integrations/contracts"
)

// The functions of this file send the findings of a round to Qory Apiary and read them
// back, over the host calls of the run's gateway link. [findingsPoster] sends one call at
// a time, repeats a call that Qory Apiary may not have stored only after it reads the
// findings, and posts the events of one occasion once.

// findingsCalls are the host calls of the findings of the run's work item: the GET and the
// POST of the run API's findings. The methods have the signatures of the methods of the
// same names of Forager's run. Each returns the body of the answer.
type findingsCalls interface {
	Findings(ctx context.Context) ([]byte, error)
	PostFindings(ctx context.Context, body []byte) ([]byte, error)
}

// The texts of the errors of a findings call, in the form of [reviewStop]: line 1 is the
// cause, ending with "so" and what the caller did not do, and line 2 is the detail, then,
// for a batch, the batches that stay stored. The text ends with [codeUsed].
const (
	textFindingsBatch    = "Findings batch %d of %d %s, so %s"
	textFindingsRead     = "The findings of the work item are not read, so %s"
	textFindingsNoPost   = "The findings of the round cannot be posted, so %s"
	textFindingsUnread   = "The %s does not pass qory's checks: %s"
	textFindingsStored   = "Batch 1 is stored"
	textFindingsStoredTo = "Batches 1 to %d are stored"
)

// findingsError is the error of a findings call on which qory stops. Its text is the form
// of [reviewStop]. err is the cause, which errors.Is and errors.As reach, so
// [runEndedLine] still reads the end of the run in it.
type findingsError struct {
	text string
	err  error
}

func (e *findingsError) Error() string { return e.text }

func (e *findingsError) Unwrap() error { return e.err }

// unreadFindings is a body of Qory Apiary that fails the checks of qory: what the body is,
// and the check that it fails.
type unreadFindings struct {
	what string
	err  error
}

func (e *unreadFindings) Error() string {
	return fmt.Sprintf(textFindingsUnread, e.what, oneLine(e.err.Error(), 300))
}

func (e *unreadFindings) Unwrap() error { return e.err }

// decodeFindingsBody reads data as the contract v: JSON, then the members that the reader
// must understand.
func decodeFindingsBody(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	return contracts.CheckMustUnderstand(data, v)
}

// readFindings is the GET answer in data, checked.
func readFindings(data []byte) (contracts.Findings, error) {
	var found contracts.Findings
	err := decodeFindingsBody(data, &found)
	if err == nil {
		err = found.Check()
	}
	if err != nil {
		return contracts.Findings{}, &unreadFindings{"list of the findings of the work item", err}
	}
	return found, nil
}

// readPostAnswer is the POST answer in data, checked.
func readPostAnswer(data []byte) (*contracts.FindingsPostAnswer, error) {
	var answer contracts.FindingsPostAnswer
	err := decodeFindingsBody(data, &answer)
	if err == nil {
		err = answer.Check()
	}
	if err != nil {
		return nil, &unreadFindings{"answer to the findings post", err}
	}
	return &answer, nil
}

// callFate is what a failed call leaves for the next step.
type callFate int

const (
	// fateStop is an error that a repeat does not change, or that ends the run: the call
	// sent nothing or Qory Apiary stored nothing, and a second send does not help.
	fateStop callFate = iota
	// fateWait is a 429, from the gateway or Qory Apiary: nothing is sent or stored, and
	// the call is sent again after Retry-After.
	fateWait
	// fateMaybe is a failure after which Qory Apiary may have stored a POST: no status
	// came back, or a status of 500 or above did, such as 502 and 503.
	fateMaybe
)

// fateOf is the fate of the call that failed with err. A run that is over, a 400, 404, 409
// and 413, and any other status below 500, are [fateStop].
//
// shortcut: two errors of Forager's run have no type: a run with no control plane, and a
// POST body over 8 MiB. Neither has a status, so both are [fateMaybe]. The first stops the
// GET before the first POST after 5 tries, with nothing posted. The second cannot occur,
// since [postBatches] makes bodies of at most 8 MiB. Replace both when Forager's errors
// have a type.
func fateOf(err error) callFate {
	if _, over := runEndedLine(err); over {
		return fateStop
	}
	status, _, _, responded := linkResponse(err)
	switch {
	case !responded:
		return fateMaybe
	case status == http.StatusTooManyRequests:
		return fateWait
	case status >= http.StatusInternalServerError:
		return fateMaybe
	}
	return fateStop
}

// findingsCause is the line of the cause that err shows.
func findingsCause(err error) string {
	var unread *unreadFindings
	switch {
	case errors.Is(err, errNotKnown):
		return errNotKnown.Error()
	case errors.As(err, &unread):
		return unread.Error()
	case errors.Is(err, errFullWindow):
		return err.Error()
	}
	return nextDetail(err)
}

// The states of a batch in [batchFailure].
const (
	batchNotStored = iota
	batchMaybeStored
	batchStored
)

// batchFailure is the error of batch k of n, which ended in err with the state of the
// batch, for a caller that did not do what. The batches before k stay stored.
func batchFailure(k, n, state int, what string, err error) error {
	word := "is not stored"
	switch {
	case errors.Is(err, errNotKnown) || state == batchMaybeStored:
		word = "may be stored"
	case state == batchStored:
		word = "is stored"
	}
	detail := findingsCause(err)
	switch {
	case k == 2:
		detail += "\n  " + textFindingsStored
	case k > 2:
		detail += "\n  " + fmt.Sprintf(textFindingsStoredTo, k-1)
	}
	return &findingsError{reviewStop(fmt.Sprintf(textFindingsBatch, k, n, word, what), detail).Error(), err}
}

// readFailure is the error of a read of the findings that failed with err, for a caller
// that did not do what.
func readFailure(err error, what string) error {
	return &findingsError{reviewStop(fmt.Sprintf(textFindingsRead, what), findingsCause(err)).Error(), err}
}

// postedRound is the result of the post of one occasion, with its error.
type postedRound struct {
	occasion int
	answer   *contracts.FindingsPostAnswer
	found    contracts.Findings
	err      error
}

// findingsPoster sends the findings calls of a run, one at a time. A call of the host is
// never sent while another is in flight, and the events of one occasion are posted once.
type findingsPoster struct {
	calls findingsCalls
	// lock has one slot. Its holder sends the calls and reads or sets last. A channel
	// lets a caller stop waiting when its context ends.
	lock chan struct{}
	// last is the occasion that was posted last, nil before the first post.
	last *postedRound
}

// newFindingsPoster is a poster that sends over calls.
func newFindingsPoster(calls findingsCalls) *findingsPoster {
	return &findingsPoster{calls: calls, lock: make(chan struct{}, 1)}
}

// acquire waits for the lock, or for the end of ctx.
func (p *findingsPoster) acquire(ctx context.Context) error {
	select {
	case p.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *findingsPoster) release() { <-p.lock }

// read is the findings of the work item: one GET, sent again up to [maxSubmissionTries]
// times after a failure that leaves nothing stored, or after a 429. The error is in the
// form of [reviewStop], with what the caller did not do as the end of its first line.
func (p *findingsPoster) read(ctx context.Context, what string) (contracts.Findings, error) {
	if err := p.acquire(ctx); err != nil {
		return contracts.Findings{}, readFailure(err, what)
	}
	defer p.release()
	found, err := p.get(ctx)
	if err != nil {
		return contracts.Findings{}, readFailure(err, what)
	}
	return found, nil
}

// get is the GET of the findings, for a caller that has the lock. It sends the request
// again, after Retry-After or 1 second, when the call has no response, returns a status of
// 500 or above, or returns 429, at most [maxSubmissionTries] times in all.
func (p *findingsPoster) get(ctx context.Context) (contracts.Findings, error) {
	var last error
	for try := 1; try <= maxSubmissionTries; try++ {
		if try > 1 {
			if err := submissionWait(ctx, retryAfter(last)); err != nil {
				return contracts.Findings{}, err
			}
		}
		one, cancel := context.WithTimeout(ctx, runAPITimeout)
		data, err := p.calls.Findings(one)
		cancel()
		if err == nil {
			return readFindings(data)
		}
		last = err
		if ctx.Err() != nil || fateOf(err) == fateStop {
			return contracts.Findings{}, err
		}
	}
	return contracts.Findings{}, last
}

// post sends the events of the occasion, bases with the first batch, as [postBatches]
// splits them, one batch after the other, with a GET before each. It returns the answer to
// the last batch, for [openBlockingOf]. When that answer is lost and the batch is stored,
// or when there is no batch, the answer is nil and found is a GET after the last batch.
// When batch k fails, batches 1 to k-1 stay stored, the error text lists them, and the
// caller sends no verdict. what is the end of the first line of an error: what the caller
// did not do. An occasion is a number that the caller gives to the events of
// one session's step, such as the findings of its round, so a second call for the same
// number, as the check of a session's verdict and the end of the session make, waits for
// the lock, then gets the result of the first post, or its error, and sends nothing.
//
// A batch is sent with these rules, which the caller does not repeat:
//   - A 429 sent nothing and stored nothing: wait Retry-After, send again.
//   - A call with no response, or a status of 500 or above, may have stored the batch:
//     read the findings, and send the batch again only when [findingsStored] finds it
//     absent.
//   - A 410 or a closed run is the end of the run; any other status below 500 stops.
//     Neither is sent again.
//
// Each batch has [maxSubmissionTries] sends, and each GET has as many tries.
func (p *findingsPoster) post(ctx context.Context, occasion int, what string, bases []contracts.FindingBase, events []contracts.FindingsPostEvent) (*contracts.FindingsPostAnswer, contracts.Findings, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, contracts.Findings{}, &findingsError{reviewStop(fmt.Sprintf(textFindingsNoPost, what), findingsCause(err)).Error(), err}
	}
	defer p.release()
	if p.last != nil && p.last.occasion == occasion {
		return p.last.answer, p.last.found, p.last.err
	}
	answer, found, err := p.postAll(ctx, what, bases, events)
	p.last = &postedRound{occasion: occasion, answer: answer, found: found, err: err}
	return answer, found, err
}

// posted reports whether the events of the occasion were posted, with the error of that
// post, nil for a post that stored them. It waits for the lock, so a post in flight is
// done when it returns. When ctx ends first, the result is true with the error of ctx.
func (p *findingsPoster) posted(ctx context.Context, occasion int) (bool, error) {
	if err := p.acquire(ctx); err != nil {
		return true, err
	}
	defer p.release()
	if p.last != nil && p.last.occasion == occasion {
		return true, p.last.err
	}
	return false, nil
}

// round reads the findings, makes the events of the occasion with build from them, and
// posts them as [findingsPoster.post] does, all under one hold of the lock: the check of a
// session's verdict and the end of the session make the call for one occasion, and the
// second gets the result of the first. build returns the bases of the first batch too.
// what is the end of the first line of an error, as for post. A GET that lists 500
// findings stops the round, see [fullWindow]. An error of the read or of build is not the
// occasion's result, so a later call tries again.
func (p *findingsPoster) round(ctx context.Context, occasion int, what string, build func(contracts.Findings) ([]contracts.FindingBase, []contracts.FindingsPostEvent, error)) (*contracts.FindingsPostAnswer, contracts.Findings, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, contracts.Findings{}, &findingsError{reviewStop(fmt.Sprintf(textFindingsNoPost, what), findingsCause(err)).Error(), err}
	}
	defer p.release()
	if p.last != nil && p.last.occasion == occasion {
		return p.last.answer, p.last.found, p.last.err
	}
	found, err := p.get(ctx)
	if err != nil {
		return nil, contracts.Findings{}, readFailure(err, what)
	}
	if err := fullWindow(found, what); err != nil {
		return nil, contracts.Findings{}, err
	}
	bases, events, err := build(found)
	if err != nil {
		return nil, contracts.Findings{}, err
	}
	answer, after, err := p.postAll(ctx, what, bases, events)
	p.last = &postedRound{occasion: occasion, answer: answer, found: after, err: err}
	return answer, after, err
}

// postAll is [findingsPoster.post] for an occasion not yet posted.
func (p *findingsPoster) postAll(ctx context.Context, what string, bases []contracts.FindingBase, events []contracts.FindingsPostEvent) (*contracts.FindingsPostAnswer, contracts.Findings, error) {
	posts, err := postBatches(bases, maskedEvents(events))
	if err != nil {
		return nil, contracts.Findings{}, &findingsError{reviewStop(fmt.Sprintf(textFindingsNoPost, what), shownError(err)).Error(), err}
	}
	var answer *contracts.FindingsPostAnswer
	var found contracts.Findings
	for k, post := range posts {
		if answer, found, err = p.sendBatch(ctx, k+1, len(posts), what, post); err != nil {
			return nil, contracts.Findings{}, err
		}
	}
	if len(posts) == 0 {
		if found, err = p.get(ctx); err != nil {
			return nil, contracts.Findings{}, readFailure(err, what)
		}
		if err := fullWindow(found, what); err != nil {
			return nil, contracts.Findings{}, err
		}
	}
	return answer, found, nil
}

// sendBatch posts batch k of n, see [findingsPoster.post]. It returns the answer, or, when
// the answer is lost and the batch is stored, nil and the GET that shows the batch.
func (p *findingsPoster) sendBatch(ctx context.Context, k, n int, what string, post contracts.FindingsPost) (*contracts.FindingsPostAnswer, contracts.Findings, error) {
	body, err := json.Marshal(post)
	if err != nil {
		return nil, contracts.Findings{}, err
	}
	before, err := p.get(ctx)
	if err != nil {
		return nil, contracts.Findings{}, batchFailure(k, n, batchNotStored, what, err)
	}
	// lost is whether a send of the batch may be stored: it stays set until the batch is
	// found, so each send after it follows a GET, also a send after a 429, since the
	// request that timed out may still be in flight at the gateway.
	lost := false
	state := func() int {
		if lost {
			return batchMaybeStored
		}
		return batchNotStored
	}
	var last error
	for sends := 0; ; {
		if sends > 0 && (lost || sends < maxSubmissionTries) {
			if err := submissionWait(ctx, retryAfter(last)); err != nil {
				return nil, contracts.Findings{}, batchFailure(k, n, state(), what, err)
			}
		}
		if lost {
			after, err := p.get(ctx)
			if err != nil {
				return nil, contracts.Findings{}, batchFailure(k, n, batchMaybeStored, what, err)
			}
			stored, err := findingsStored(post, before, after)
			if err != nil {
				return nil, contracts.Findings{}, batchFailure(k, n, batchMaybeStored, what, err)
			}
			if stored {
				// The GET stands in for the answer, so it must list all the findings. The batch is
				// stored, so line 1 of the stop is that of a stored batch.
				if fullWindow(after, what) != nil {
					return nil, contracts.Findings{}, batchFailure(k, n, batchStored, what, errFullWindow)
				}
				return nil, after, nil
			}
		}
		if sends == maxSubmissionTries {
			return nil, contracts.Findings{}, batchFailure(k, n, state(), what, last)
		}
		sends++
		one, cancel := context.WithTimeout(ctx, runAPITimeout)
		data, err := p.calls.PostFindings(one, body)
		cancel()
		if err == nil {
			answer, err := readPostAnswer(data)
			if err != nil {
				return nil, contracts.Findings{}, batchFailure(k, n, batchStored, what, err)
			}
			return answer, contracts.Findings{}, nil
		}
		last = err
		switch fate := fateOf(err); {
		case ctx.Err() != nil:
			return nil, contracts.Findings{}, batchFailure(k, n, batchMaybeStored, what, err)
		case fate == fateStop:
			return nil, contracts.Findings{}, batchFailure(k, n, state(), what, err)
		case fate == fateMaybe:
			lost = true
		}
	}
}
