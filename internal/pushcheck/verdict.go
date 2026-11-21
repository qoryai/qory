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

// SessionVerdict is the check of a POST to the run API from a session whose workflow
// entry has judges set to judges. For a workflow that judges it returns nil. Otherwise a
// later workflow in the loop judges the work, and the session may record escalate or no
// verdict: the result is nil for every request but a POST of /verdict and a POST of
// /artifacts of the type verdict, and for a verdict of [runapi.VerdictEscalate]. A
// verdict of any other value, and a body that is no verdict in the run API's form or that
// can be read in more than one way, such as one with a member twice, is an error. The
// first line of the error has the fix and is at most 100 characters. A push of the type
// verdict is checked in each of its files; a push that is no push in the run API's form
// is left to the run API.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
// shortcut: only a push of the type verdict is read as a verdict document. A session
// that pushes a verdict document as another type is not checked; how the run API treats
// that was not read, so check it before wiring.
func SessionVerdict(judges bool, req session.RunAPIRequest) error {
	if judges {
		return nil
	}
	switch req.Path {
	case verdictPath:
		return checkVerdictDocument(req.Body)
	case artifactsPath:
		artifactType, files, _, err := readPush(req.Body)
		if err != nil || artifactType != verdictType {
			return nil
		}
		for _, f := range files {
			if err := checkVerdictDocument(f.Content); err != nil {
				return err
			}
		}
	}
	return nil
}

// verdictType is the artifact type of a verdict document.
const verdictType = "verdict"

// checkVerdictDocument is the check of [SessionVerdict] for one verdict document.
func checkVerdictDocument(body []byte) error {
	const fix = "Record escalate or no verdict: the next workflow in the loop judges this work."
	top, err := members(body, []string{"verdict", "points", "track"})
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
	if verdict != runapi.VerdictEscalate {
		return fmt.Errorf("%s\n  The session sent the verdict %s.", fix, exports.Value(cut(cleanLine(verdict)), true))
	}
	return nil
}
