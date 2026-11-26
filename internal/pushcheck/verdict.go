package pushcheck

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
)

// verdictType is the artifact type of a verdict document. The run API reads only an
// artifact of the exact type verdict as a verdict: type names are lower case and match
// exactly, and a file named verdict.json in another type is never read as one.
const verdictType = "verdict"

// verdictMembers are the members of a verdict document that the check reads.
var verdictMembers = []string{"verdict", "points", "track"}

// The first lines of the refusals of [SessionVerdict] and [NoSessionVerdict] for a verdict
// document: textVerdictNoSession for every document while no session runs, and
// textVerdictFix for a verdict that the session may not record and for a document that
// cannot be read. A push that [readRunAPIPost] cannot read, and a path that is another
// spelling, have the refusal of [unreadPush].
const (
	textVerdictFix       = "Record escalate or no verdict: the next workflow in the loop judges this work."
	textVerdictNoSession = "Post the verdict from a session of the run."
)

// SessionVerdict is the check of a POST to the run API from a session whose workflow
// entry has judges set to judges. For a workflow that judges it returns nil. Otherwise a
// later workflow in the loop judges the work, and the session may record escalate or no
// verdict: the result is nil for every POST but one of /verdict and one of /artifacts of
// the type verdict, and for a verdict of [runapi.VerdictEscalate]. A push of the type
// verdict is a verdict under any name and is checked in each of its files. A verdict of
// any other value, and a body that is no verdict in the run API's form or that can be read
// in more than one way, such as one with a member twice, is a refusal. So is a POST that
// [readRunAPIPost] cannot read in one way, with the texts of [Checker.RunAPICheck]: a
// push body that is no push or has a member twice, and a path that spells /verdict or
// /artifacts in another way. The refusal is the 422 of [Failure.Refusal]; its first line
// has the fix and is at most 100 characters.
func SessionVerdict(judges bool, req session.RunAPIRequest) *session.RunAPIRefusal {
	if judges {
		return nil
	}
	return verdictFailure(true, req).Refusal()
}

// NoSessionVerdict is the check of [SessionVerdict] for a POST that arrives while no
// session runs, such as one of a setup command: every verdict is a refusal, escalate
// too, and the same POSTs are read.
func NoSessionVerdict(req session.RunAPIRequest) *session.RunAPIRefusal {
	return verdictFailure(false, req).Refusal()
}

// verdictFailure is the failure of [SessionVerdict], with running false for
// [NoSessionVerdict]; nil for a POST that passes.
func verdictFailure(running bool, req session.RunAPIRequest) *Failure {
	post, err := readRunAPIPost(req)
	switch {
	case post.kind == postOther:
		return nil
	case err != nil && (post.kind == postPush || errors.Is(err, errPathSpelling)):
		return unreadPush(err)
	case post.kind == postVerdict:
		return checkVerdictDocument(running, post.document, err)
	case post.artifactType != verdictType:
		return nil
	}
	for _, f := range post.files {
		top, err := members(f.Content, verdictMembers)
		if failed := checkVerdictDocument(running, top, err); failed != nil {
			return failed
		}
	}
	return nil
}

// checkVerdictDocument is the check of [SessionVerdict] for one verdict document, whose
// members are top and whose read failed with err.
func checkVerdictDocument(running bool, top map[string]json.RawMessage, err error) *Failure {
	if !running {
		return failureThen(textVerdictNoSession, "", "The run takes no verdict between its sessions or from a setup command.")
	}
	if err != nil {
		if errors.Is(err, errAmbiguous) {
			return failureThen(textVerdictFix, "", "The verdict has a member twice, or one that differs only in case.")
		}
		return failureThen(textVerdictFix, "", fmt.Sprintf("The body is no verdict: %s.", cut(cleanLine(err.Error()))))
	}
	var verdict string
	if json.Unmarshal(top["verdict"], &verdict) != nil {
		return failureThen(textVerdictFix, "", "The verdict is missing or not a string.")
	}
	if verdict != runapi.VerdictEscalate {
		return failureThen(textVerdictFix, "", fmt.Sprintf("The session sent the verdict %s.", exports.Value(cut(cleanLine(verdict)), true)))
	}
	return nil
}
