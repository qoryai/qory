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

// The first lines of the refusals of [SessionVerdict], [ReviewVerdict] and
// [NoSessionVerdict] for a verdict document: textVerdictNoSession for every document while
// no session runs, textVerdictFix for a verdict that a session that does not judge may not
// record, textVerdictReviewFix for one that a review session may not record, and either of
// those two for a document that cannot be read. A push that [readRunAPIPost] cannot read,
// and a path that is another spelling, have the refusal of [unreadPush].
const (
	textVerdictFix       = "Record escalate or no verdict: the next workflow in the loop judges this work."
	textVerdictReviewFix = "Record changes-requested, escalate or no verdict: qory sends passed after the round."
	textVerdictNoSession = "Post the verdict from a session of the run."
	// textVerdictBodyFix is the first line for a body of /verdict that cannot be read, when
	// no rule of the session applies.
	textVerdictBodyFix = "Post the verdict as one JSON object with each member once."
)

// verdictRule is what a session may record: allows reports whether it may record the
// verdict, and fix is the first line of the refusal of a verdict that it may not and of a
// document that cannot be read. A rule with no allows is the rule of a run with no
// session: no verdict is recorded, whatever the document.
type verdictRule struct {
	allows func(verdict string) bool
	fix    string
}

var (
	// escalateOnly is the rule of a session whose workflow does not judge.
	escalateOnly = verdictRule{func(v string) bool { return v == runapi.VerdictEscalate }, textVerdictFix}
	// notPassed is the rule of a review session: qory sends the verdict passed.
	notPassed = verdictRule{func(v string) bool {
		return v == runapi.VerdictChangesRequested || v == runapi.VerdictEscalate
	}, textVerdictReviewFix}
	// noSession is the rule of a POST while no session runs.
	noSession = verdictRule{fix: textVerdictNoSession}
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
	return verdictFailure(escalateOnly, req).Refusal()
}

// ReviewVerdict is the check of [SessionVerdict] for a POST from a review session, whose
// verdict qory sends after the round: the verdict passed is a refusal, and a verdict of
// [runapi.VerdictChangesRequested] or [runapi.VerdictEscalate] is not. Any other value, a
// body that cannot be read in one way and a path in another spelling are refusals, as
// they are for a session that does not judge, and the same POSTs are read.
func ReviewVerdict(req session.RunAPIRequest) *session.RunAPIRefusal {
	return verdictFailure(notPassed, req).Refusal()
}

// IsVerdict reports whether req records a session's verdict, as [verdictFailure] reads
// it: a POST of /verdict, whatever its body, or a push of /artifacts of the type verdict
// with files, which [readRunAPIPost] reads in one way. A withdrawal of the type verdict, a
// push of another type or one that cannot be read in one way, and a POST of another path
// are not. Qory Apiary reads a verdict body that qory cannot read in one way, so a POST of
// /verdict is a verdict even then.
func IsVerdict(req session.RunAPIRequest) bool {
	post, err := readRunAPIPost(req)
	if post.kind == postVerdict {
		return true
	}
	return err == nil && post.kind == postPush && post.artifactType == verdictType && !post.withdraw
}

// RecordsPassed reports whether req, a POST for which [IsVerdict] is true, records the
// verdict passed. A verdict value that cannot be read counts as passed: a body that is no
// JSON object, a verdict that is missing or not a string, and a push of the type verdict
// with no file. A push counts as passed when any of its files does, as [verdictFailure]
// checks each of them. A verdict of any other value, such as changes-requested or a name
// that is no verdict, is not passed.
func RecordsPassed(req session.RunAPIRequest) bool {
	post, _ := readRunAPIPost(req)
	if post.kind == postVerdict {
		return passedMembers(post.document)
	}
	for _, f := range post.files {
		if FilePassed(f.Content) {
			return true
		}
	}
	return len(post.files) == 0
}

// FilePassed reports whether content, the file of a verdict, records the verdict passed,
// by the rules of [RecordsPassed].
func FilePassed(content []byte) bool {
	top, err := members(content, verdictMembers)
	return err != nil || passedMembers(top)
}

// passedMembers reports whether the verdict member of top is passed or cannot be read.
func passedMembers(top map[string]json.RawMessage) bool {
	var verdict *string
	return json.Unmarshal(top["verdict"], &verdict) != nil || verdict == nil || *verdict == runapi.VerdictPassed
}

// UnreadablePost is the refusal of a POST of /verdict or /artifacts that [readRunAPIPost]
// cannot read in one way; nil for every other POST. A POST of /verdict has the texts that
// [verdictFailure] has for a body that it cannot read, with the first line
// [textVerdictBodyFix]; any other POST has the texts of [Checker.RunAPICheck]. A caller
// that acts on a verdict before it reaches Qory Apiary uses it to stop a POST that
// [IsVerdict] cannot classify.
func UnreadablePost(req session.RunAPIRequest) *session.RunAPIRefusal {
	post, err := readRunAPIPost(req)
	switch {
	case post.kind == postOther || err == nil:
		return nil
	case post.kind == postVerdict && !errors.Is(err, errPathSpelling):
		return checkVerdictDocument(verdictRule{allows: func(string) bool { return true }, fix: textVerdictBodyFix}, post.document, err).Refusal()
	}
	return unreadPush(err).Refusal()
}

// NoSessionVerdict is the check of [SessionVerdict] for a POST that arrives while no
// session runs, such as one of a setup command: every verdict is a refusal, escalate
// too, and the same POSTs are read.
func NoSessionVerdict(req session.RunAPIRequest) *session.RunAPIRefusal {
	return verdictFailure(noSession, req).Refusal()
}

// verdictFailure is the failure of [SessionVerdict], [ReviewVerdict] or
// [NoSessionVerdict], by the rule; nil for a POST that passes.
func verdictFailure(rule verdictRule, req session.RunAPIRequest) *Failure {
	post, err := readRunAPIPost(req)
	switch {
	case post.kind == postOther:
		return nil
	case err != nil && (post.kind == postPush || errors.Is(err, errPathSpelling)):
		return unreadPush(err)
	case post.kind == postVerdict:
		return checkVerdictDocument(rule, post.document, err)
	case post.artifactType != verdictType:
		return nil
	}
	for _, f := range post.files {
		top, err := members(f.Content, verdictMembers)
		if failed := checkVerdictDocument(rule, top, err); failed != nil {
			return failed
		}
	}
	return nil
}

// checkVerdictDocument is the check of [SessionVerdict] for one verdict document, whose
// members are top and whose read failed with err, by the rule.
func checkVerdictDocument(rule verdictRule, top map[string]json.RawMessage, err error) *Failure {
	if rule.allows == nil {
		return failureThen(textVerdictNoSession, "", "The run takes no verdict between its sessions or from a setup command.")
	}
	if err != nil {
		if errors.Is(err, errAmbiguous) {
			return failureThen(rule.fix, "", "The verdict has a member twice, or one that differs only in case.")
		}
		return failureThen(rule.fix, "", fmt.Sprintf("The body is no verdict: %s.", cut(cleanLine(err.Error()))))
	}
	var verdict string
	if json.Unmarshal(top["verdict"], &verdict) != nil {
		return failureThen(rule.fix, "", "The verdict is missing or not a string.")
	}
	if !rule.allows(verdict) {
		return failureThen(rule.fix, "", fmt.Sprintf("The session sent the verdict %s.", exports.Value(cut(cleanLine(verdict)), true)))
	}
	return nil
}
