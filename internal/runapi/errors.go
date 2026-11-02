package runapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The codes of the run API's errors that a caller may act on, as [Error.Code] has them.
const (
	CodeInvalidRequest  = "invalid_request"
	CodeUnauthorized    = "unauthorized"
	CodeTypeNotWritable = "type_not_writable"
	CodeNotFound        = "not_found"
	CodeRunEnded        = "run_ended"
	CodeInputsInvalid   = "inputs_invalid"
	CodeTicketGone      = "ticket_gone"
	CodePayloadTooLarge = "payload_too_large"
	CodeRateLimited     = "rate_limited"
	CodeUnknownType     = "unknown_type"
	CodeTooManyFiles    = "too_many_files"
	CodeTooLarge        = "too_large"
	CodeInvalidArtifact = "invalid_artifact"
	CodeUnavailable     = "unavailable"
)

// Error is an error status of the run API, or, with Status 0, a version too large to
// send. Error returns one line that starts with the fix: Message when the response has
// one, or a line made from the code.
type Error struct {
	// Status is the HTTP status, 0 for a request that was not sent.
	Status int
	// Code is the response's `error`, empty for a response that has none, such as the
	// error page of an instance without the run API.
	Code string
	// Type is the artifact type, and Limit the type's limit, where the error has them.
	Type  string
	Limit int64
	// Names has the members or files that failed, such as `files[0].filename`.
	Names []string
	// Message is the response's `message`, which starts with the fix.
	Message string
	// RetryAfter is the `Retry-After` header in seconds, 0 for none.
	RetryAfter int
	// Request is the method and path, relative to the base URL.
	Request string
}

// Error returns the line for a person or a log, with the fix first.
func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}

	status := strings.TrimSpace(strconv.Itoa(e.Status) + " " + e.Code)

	switch e.Code {
	case CodeInvalidRequest:
		return fmt.Sprintf("Fix %s in the request. %s returned %s.", strings.Join(e.Names, ", "), e.Request, status)
	case CodeUnauthorized:
		return fmt.Sprintf("Check the access key of the run's gateway in Qory Apiary. The signature of %s did not verify (%s).",
			e.Request, status)
	case CodeNotFound:
		if strings.Contains(e.Request, "/files/") {
			return fmt.Sprintf("Pull the artifact index again, and use a version id and a filename from it. %s returned %s.",
				e.Request, status)
		}

		return fmt.Sprintf("Check the run id in the run API's base URL. %s returned %s: the run is unknown, "+
			"goes through another gateway, or has no work item.", e.Request, status)
	case CodeInputsInvalid:
		return fmt.Sprintf("Fix the ticket of this work item at its source. A value of the ticket breaks the rules "+
			"for a run's inputs (%s returned %s).", e.Request, status)
	case CodeTicketGone:
		return fmt.Sprintf("Take the ticket in again. The ticket of this work item is no longer in Qory Apiary "+
			"(%s returned %s).", e.Request, status)
	case CodePayloadTooLarge:
		return fmt.Sprintf("Shrink the request to at most %d bytes encoded. %s returned %s.", MaxBody, e.Request, status)
	case CodeRateLimited:
		wait := "a minute"
		if e.RetryAfter > 0 {
			wait = fmt.Sprintf("%d seconds", e.RetryAfter)
		}

		return fmt.Sprintf("Try again in %s. The gateway's access key is over its rate limit (%s returned %s).",
			wait, e.Request, status)
	case CodeUnavailable:
		return fmt.Sprintf("Try again. Qory Apiary could not complete %s (%s).", e.Request, status)
	case "":
		if e.Status == http.StatusNotFound {
			return fmt.Sprintf("Turn on the feature workflow_model in Qory Apiary, and check that the run's gateway "+
				"forwards the run API. %s returned 404 with no error code.", e.Request)
		}
	}

	return fmt.Sprintf("Check the logs of the run's gateway. %s returned %s.", e.Request, status)
}

// HasCode reports whether err is an [Error] with code.
func HasCode(err error, code string) bool {
	var apiError *Error

	return errors.As(err, &apiError) && apiError.Code == code
}

// readError returns the [Error] of a response with a status the request does not expect.
// A body that is not the run API's JSON leaves the code and the other members empty.
func readError(response *http.Response, request string) *Error {
	apiError := &Error{Status: response.StatusCode, Request: request}

	var body struct {
		Error   string   `json:"error"`
		Type    string   `json:"type"`
		Limit   int64    `json:"limit"`
		Names   []string `json:"names"`
		Message string   `json:"message"`
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxErrorResponse))
	if err == nil && json.Unmarshal(raw, &body) == nil {
		apiError.Code = body.Error
		apiError.Type = body.Type
		apiError.Limit = body.Limit
		apiError.Names = body.Names
		apiError.Message = body.Message
	}

	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		apiError.RetryAfter = seconds
	}

	return apiError
}

// versionTooLarge returns the error of a version of artifactType that is size bytes
// encoded, over [MaxBody], which the client does not send.
func versionTooLarge(artifactType string, size int) *Error {
	return &Error{
		Code:  CodePayloadTooLarge,
		Type:  artifactType,
		Limit: MaxBody,
		Message: fmt.Sprintf("Shrink the files of this version of %s to about 24 MiB together. A request body is at "+
			"most %d bytes encoded, and this version is %d.", artifactType, MaxBody, size),
	}
}
