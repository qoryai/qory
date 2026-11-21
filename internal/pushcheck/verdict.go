package pushcheck

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
)

// verdictPath is the path of a POST that records the run's verdict.
const verdictPath = "/verdict"

// ReviewStageVerdict is the check of a POST to the run API in a stage with reviewers:
// nil for every request but a POST of /verdict, and for a verdict of
// [runapi.VerdictChangesRequested]. In that stage qory sends the other verdicts after the
// reviewers ran, so a session may record only changes-requested. A verdict of any other
// value, and a body that is no verdict in the run API's form or that can be read in more
// than one way, such as one with a member twice, is an error. The first line of the error
// has the fix and is at most 100 characters.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73). A verdict that a session
// pushes as an artifact of type verdict through /artifacts is not checked here.
func ReviewStageVerdict(req session.RunAPIRequest) error {
	if req.Path != verdictPath {
		return nil
	}
	const fix = "Record only changes-requested: qory sends every other verdict after the review."
	top, err := members(req.Body, []string{"verdict", "points", "track"})
	if err != nil {
		if errors.Is(err, errAmbiguous) {
			return fmt.Errorf("%s\n  The verdict has a member twice, or one that differs only in case.", fix)
		}
		return fmt.Errorf("%s\n  The body is no verdict: %s.", fix, cut(cleanLine(err.Error())))
	}
	var verdict string
	if json.Unmarshal(top["verdict"], &verdict) != nil {
		return fmt.Errorf("%s\n  The verdict is missing or not a string.", fix)
	}
	if verdict != runapi.VerdictChangesRequested {
		return fmt.Errorf("%s\n  The session sent the verdict %s.", fix, exports.Value(cut(cleanLine(verdict)), true))
	}
	return nil
}
