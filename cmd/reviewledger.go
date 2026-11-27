package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/suite"
)

// The functions of this file turn the reports of a review round, the full suite and the
// build ledger into events of the findings of a work item, and read the findings that Qory
// Apiary stores. They open no file, send no request and read no clock: the caller reads
// the GET of the findings and posts the batches.

// The kinds of an event of a findings post, and the kinds of an event that a GET of the
// findings lists.
const (
	eventRaise         = "raise"
	eventRepeat        = "repeat"
	eventReplyFixed    = "reply_fixed"
	eventReplyDisputed = "reply_disputed"
	eventCloseFixed    = "close_fixed"
	eventCloseDismiss  = "close_dismissed"

	storedRaised        = "raised"
	storedRepeated      = "repeated"
	storedReopened      = "reopened"
	storedRepeatedAfter = "repeated_after_person_close"
	storedRepliedFixed  = "replied_fixed"
	storedRepliedDisp   = "replied_disputed"
	storedClosedFixed   = "closed_fixed"
	storedClosedDismiss = "closed_dismissed"
)

// The source kinds, the states and the checks that this file reads and writes.
const (
	sourceReviewer = "reviewer"
	sourceCheck    = "check"
	stateOpen      = "open"
	stateFixed     = "fixed"
	stateDismissed = "dismissed"
	checkSuite     = "suite"
	reportCheck    = "report-"
)

// The limits of a findings post and of a findings answer: the run contract's.
const (
	// maxPostEvents is the most events of one post.
	maxPostEvents = 100
	// maxPostBytes is the most bytes of a post's body.
	maxPostBytes = 8 << 20
	// maxListedFindings is the most findings that a GET lists, and maxListedEvents the most
	// events of each finding that it lists, the newest.
	maxListedFindings = 500
	maxListedEvents   = 100
	// maxPoint is the most characters of a point, maxProof the most of the proof of a check
	// finding that qory writes.
	maxPoint = 500
	maxProof = 4000
)

// fullWindow is the stop for a GET that lists maxListedFindings findings, the most that it
// lists: the work item may have more, which the list does not show, so the open findings
// and the findings of a role may be missing from it. what is the end of the first line:
// what the caller did not do. fullWindow returns nil for a shorter list.
func fullWindow(found contracts.Findings, what string) error {
	if len(found.Findings) < maxListedFindings {
		return nil
	}
	return reviewStop(fmt.Sprintf("The GET of the findings lists %d, its limit, so %s", maxListedFindings, what),
		"The work item may have more findings, which the list does not show")
}

// errFullWindow is the cause of the stop for a batch that is stored, when the GET that
// shows the batch lists maxListedFindings findings, see [fullWindow].
var errFullWindow = fmt.Errorf("The GET of the findings lists %d, its limit, and the work item may have more findings", maxListedFindings)

// where is the target and the stage that the events of a round are about. stage is the
// name of the stage as [stageName] makes it.
type where struct {
	target contracts.Target
	stage  string
}

// stageSlug is a run of characters other than a-z and 0-9.
var stageSlug = regexp.MustCompile(`[^a-z0-9]+`)

// stageName is the name that Qory Apiary gives the stage in a finding and in an event: the
// name of the stage in lower case, each run of characters other than a-z and 0-9 written
// as one hyphen, cut to 64 characters, then trimmed of hyphens, so "Code review" is
// "code-review". A name with no character of a-z or 0-9 is replaced by id without "st_".
//
// shortcut: qory sees no other stage of the process, so a name that two stages share,
// which Apiary replaces by the id of each stage, is not detected, and the case mapping of
// Go and Elixir may differ for a letter whose lower case is not one letter; replace when
// the run contract states the name of a stage.
func stageName(name, id string) string {
	slug := stageSlug.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug[:min(len(slug), 64)], "-")
	if slug != "" {
		return slug
	}
	return strings.TrimPrefix(id, "st_")
}

// hasOpenFinding reports whether found has an open finding of the stage with the source
// kind and name.
func hasOpenFinding(found []contracts.Finding, stage, kind, name string) bool {
	return slices.ContainsFunc(found, func(f contracts.Finding) bool {
		return f.Stage == stage && f.State == stateOpen && f.Source.Kind == kind && f.Source.Name == name
	})
}

// expectedRoles is the roles of group, in its order, whose report a review round needs:
// each role that matches a changed path (byPath, from [expectedReviewersFor]) and each
// role with an open finding of the stage in found, whatever its paths. A role that is not
// in group stays out, since the panel starts members of the group alone.
func expectedRoles(group, byPath []string, found contracts.Findings, stage string) []string {
	var roles []string
	for _, role := range group {
		if slices.Contains(byPath, role) || hasOpenFinding(found.Findings, stage, sourceReviewer, role) {
			roles = append(roles, role)
		}
	}
	return roles
}

// findingsFile is the file of findings that qory writes for a session, previous_findings
// for a reviewer and open-findings.json for the builder: round is the round of the stage
// that the session is in.
type findingsFile struct {
	Round    int           `json:"round"`
	Findings []fileFinding `json:"findings"`
}

// fileFinding is one finding of a [findingsFile].
type fileFinding struct {
	ID       string                  `json:"id"`
	Stage    string                  `json:"stage"`
	Source   contracts.FindingSource `json:"source"`
	Severity string                  `json:"severity"`
	State    string                  `json:"state"`
	Quote    string                  `json:"quote"`
	File     string                  `json:"file,omitempty"`
	Line     int                     `json:"line,omitempty"`
	Point    string                  `json:"point"`
	Problem  string                  `json:"problem"`
	Fix      string                  `json:"fix"`
	Proof    string                  `json:"proof"`
	Disputed bool                    `json:"disputed"`
	Replies  []fileReply             `json:"replies"`
}

// fileReply is a reply of the builder to a finding: kind is "fixed" or "disputed".
type fileReply struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Round  int    `json:"round"`
}

// runClosed reports whether the last close of f is a close by a run (closed_fixed or
// closed_dismissed), not one by a person (dismissed_by_person or accepted).
func runClosed(f contracts.Finding) bool {
	for _, e := range slices.Backward(f.Events) {
		switch e.Kind {
		case storedClosedFixed, storedClosedDismiss:
			return true
		case "dismissed_by_person", "accepted":
			return false
		}
	}
	return false
}

// fileOf is the finding f as a file has it, with its replies.
func fileOf(f contracts.Finding) fileFinding {
	out := fileFinding{ID: f.ID, Stage: f.Stage, Source: f.Source, Severity: f.Severity, State: f.State,
		Quote: f.Place.Quote, File: f.Place.File, Line: f.Place.Line, Point: f.Point, Problem: f.Problem,
		Fix: f.Fix, Proof: f.Proof, Disputed: f.Disputed, Replies: []fileReply{}}
	for _, e := range f.Events {
		switch e.Kind {
		case storedRepliedFixed:
			out.Replies = append(out.Replies, fileReply{Kind: stateFixed, Reason: e.Reason, Round: e.Round})
		case storedRepliedDisp:
			out.Replies = append(out.Replies, fileReply{Kind: "disputed", Reason: e.Reason, Round: e.Round})
		}
	}
	return out
}

// fileJSON is the file with the findings, indented, with <, > and & as they are.
func fileJSON(round int, findings []fileFinding) ([]byte, error) {
	if round < 1 {
		return nil, fmt.Errorf("Number the round from 1: the round of the findings file is %d.", round)
	}
	if findings == nil {
		findings = []fileFinding{}
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(findingsFile{Round: round, Findings: findings}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// previousFindings is previous_findings for the reviewers of a group, one file for all
// roles: each finding of the stage that a role of roles raised, open or closed by a run,
// with its replies, in the order of got. A finding that a person closed stays out. round
// is the number of the round that starts.
func previousFindings(round int, stage string, roles []string, got contracts.Findings) ([]byte, error) {
	if err := got.Check(); err != nil {
		return nil, err
	}
	var out []fileFinding
	for _, f := range got.Findings {
		if f.Stage != stage || f.Source.Kind != sourceReviewer || !slices.Contains(roles, f.Source.Name) {
			continue
		}
		if f.State == stateOpen || (f.State == stateFixed || f.State == stateDismissed) && runClosed(f) {
			out = append(out, fileOf(f))
		}
	}
	return fileJSON(round, out)
}

// builderFindings is open-findings.json for the builder: each open finding of every stage,
// of every source, with its replies, in the order of got, in the form of
// previous_findings.
func builderFindings(round int, got contracts.Findings) ([]byte, error) {
	if err := got.Check(); err != nil {
		return nil, err
	}
	var out []fileFinding
	for _, f := range got.Findings {
		if f.State == stateOpen {
			out = append(out, fileOf(f))
		}
	}
	return fileJSON(round, out)
}

// refs numbers the raises of a round: each ref of a post is unique, and r1, r2 and so on
// are in the grammar of a ref.
type refs struct{ n int }

// next is the ref of the next raise.
func (r *refs) next() string {
	r.n++
	return "r" + strconv.Itoa(r.n)
}

// roleReport is the report of one expected role: Report is nil when the role has no valid
// report, and Why is then the cause, one line, as reviewround.Read gives it, or the text
// of its Check.
type roleReport struct {
	Role   string
	Report *reviewround.Report
	Why    string
}

// readRoleReport is the [roleReport] of the role, from the result of reviewround.Read: the
// report is checked against the findings that the role has in the stage, so a report that
// repeats or closes an id that is not the role's has no valid report.
func readRoleReport(role string, report reviewround.Report, err error, got contracts.Findings, stage string) roleReport {
	if err != nil {
		return roleReport{Role: role, Why: err.Error()}
	}
	owned := map[string]bool{}
	for _, f := range got.Findings {
		if f.Stage == stage && f.Source.Kind == sourceReviewer && f.Source.Name == role {
			owned[f.ID] = true
		}
	}
	if err := report.Check(owned); err != nil {
		return roleReport{Role: role, Why: err.Error()}
	}
	return roleReport{Role: role, Report: &report}
}

// cut is s cut to at most n characters, ending with "…" when it is cut.
func cut(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// lastChars is the last n characters of s.
func lastChars(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// oneLine is s on one line: each character that a line has no room for, and each run of
// spaces, becomes one space, and the text is cut to n characters.
func oneLine(s string, n int) string {
	return cut(strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if primitives.StopsLine(r) {
			return ' '
		}
		return r
	}, s)), " "), n)
}

// raiseOf is the event that raises a new finding.
func (w where) raiseOf(r *refs, by, kind, name, severity string, place contracts.FindingPlace, point, problem, fix, proof string) contracts.FindingsPostEvent {
	target := w.target
	return contracts.FindingsPostEvent{Kind: eventRaise, By: by, Ref: r.next(), Target: &target, Stage: w.stage,
		Source: &contracts.FindingSource{Kind: kind, Name: name}, Severity: severity, Place: &place,
		Point: point, Problem: problem, Fix: fix, Proof: proof}
}

// checkText is the texts of a check finding that qory raises.
type checkText struct{ point, problem, fix, proof string }

// checkFailed is the events for a check named name that failed: a raise of a blocking
// finding of the check when none is open for the stage, and a repeat of each open one
// otherwise. The place of the finding is its point.
func (w where) checkFailed(r *refs, found contracts.Findings, name string, t checkText) []contracts.FindingsPostEvent {
	var events []contracts.FindingsPostEvent
	for _, f := range found.Findings {
		if f.Stage == w.stage && f.State == stateOpen && f.Source.Kind == sourceCheck && f.Source.Name == name {
			events = append(events, contracts.FindingsPostEvent{Kind: eventRepeat, By: name, ID: f.ID})
		}
	}
	if len(events) == 0 {
		events = append(events, w.raiseOf(r, name, sourceCheck, name, reviewround.Blocking,
			contracts.FindingPlace{Quote: t.point}, t.point, t.problem, t.fix, t.proof))
	}
	return events
}

// checkPassed is a close_fixed with the reason for each open finding of the check named
// name for the stage.
func (w where) checkPassed(found contracts.Findings, name, reason string) []contracts.FindingsPostEvent {
	var events []contracts.FindingsPostEvent
	for _, f := range found.Findings {
		if f.Stage == w.stage && f.State == stateOpen && f.Source.Kind == sourceCheck && f.Source.Name == name {
			events = append(events, contracts.FindingsPostEvent{Kind: eventCloseFixed, By: name, ID: f.ID, Reason: reason})
		}
	}
	return events
}

// reportEvents is the events of the report of one expected role. A valid report becomes
// a raise for each finding with no id, a repeat for each finding with an id, and a close
// for each close, and closes an open check finding report-<role>. A role with no valid
// report gets a blocking check finding report-<role>: a raise, or a repeat while one is
// open.
func (w where) reportEvents(r *refs, rr roleReport, found contracts.Findings) []contracts.FindingsPostEvent {
	name := reportCheck + rr.Role
	if rr.Report == nil {
		why := oneLine(maskText(rr.Why), maxPoint)
		if why == "" {
			why = "Reviewer " + rr.Role + " wrote no valid report."
		}
		return w.checkFailed(r, found, name, checkText{point: why, problem: why, proof: why,
			fix: "Write report.json of " + rr.Role + " in the review report format, then run the review again."})
	}
	events := w.checkPassed(found, name, "The report of "+rr.Role+" is valid.")
	for _, f := range rr.Report.Findings {
		if f.ID != "" {
			events = append(events, contracts.FindingsPostEvent{Kind: eventRepeat, By: rr.Role, ID: f.ID})
			continue
		}
		place := contracts.FindingPlace{Quote: f.Quote, File: f.File}
		if f.File != "" {
			place.Line = f.Line
		}
		events = append(events, w.raiseOf(r, rr.Role, sourceReviewer, rr.Role, f.Severity, place,
			f.Point, f.Problem, f.Fix, f.Proof))
	}
	for _, c := range rr.Report.Closes {
		kind := eventCloseFixed
		if c.State == reviewround.Dismissed {
			kind = eventCloseDismiss
		}
		events = append(events, contracts.FindingsPostEvent{Kind: kind, By: rr.Role, ID: c.ID, Reason: c.Reason})
	}
	return events
}

// staleReportCloses is a close_fixed for each open check finding report-<role> of the
// stage whose role is in group and is no longer expected: byPath, the roles whose paths
// match the files of the whole change, does not have it, and the role has no open reviewer
// finding, so its missing report is not a failure now. The reason is "No file of the
// change needs a report of <role>.". It closes none unless fromLedger is true (the change
// is the one from the base that Qory Apiary stores, not a part of it) and listed is true
// (the list of the changed files was read in full).
func (w where) staleReportCloses(group, byPath []string, found contracts.Findings, fromLedger, listed bool) []contracts.FindingsPostEvent {
	if !fromLedger || !listed {
		return nil
	}
	expected := expectedRoles(group, byPath, found, w.stage)
	var events []contracts.FindingsPostEvent
	for _, role := range group {
		if slices.Contains(expected, role) {
			continue
		}
		events = append(events, w.checkPassed(found, reportCheck+role, "No file of the change needs a report of "+role+".")...)
	}
	return events
}

// reportsEvents is the events of the reports of the expected roles, in the order of
// reports.
func (w where) reportsEvents(r *refs, reports []roleReport, found contracts.Findings) ([]contracts.FindingsPostEvent, error) {
	if err := found.Check(); err != nil {
		return nil, err
	}
	var events []contracts.FindingsPostEvent
	for _, rr := range reports {
		events = append(events, w.reportEvents(r, rr, found)...)
	}
	return events, nil
}

// suiteEnding is how the full suite of a session ended, as the findings need it: the
// state, the point that states the cause on one line, and the tail of the suite's output.
type suiteEnding struct {
	State suite.State
	Point string
	Tail  string
}

// suiteEvents is the events of the check finding suite for a session that ran the full
// suite: a red suite (failed, timed out or stopped, which includes a suite that did not
// start) raises a blocking finding, or repeats the open one; a suite that passed closes
// the open one as fixed; a skipped suite has no event. The point and the tail are masked
// before they are cut, so a cut cannot leave a part of a secret that the masker knows.
func (w where) suiteEvents(r *refs, s suiteEnding, found contracts.Findings) ([]contracts.FindingsPostEvent, error) {
	if err := found.Check(); err != nil {
		return nil, err
	}
	switch {
	case s.State.Red():
		point := oneLine(maskText(s.Point), maxPoint)
		if point == "" {
			point = "The full suite did not pass."
		}
		proof := lastChars(maskText(s.Tail), maxProof)
		if strings.TrimSpace(proof) == "" {
			proof = point
		}
		return w.checkFailed(r, found, checkSuite, checkText{point: point, problem: point, proof: proof,
			fix: "Fix the cause in the proof, then push again so that the full suite runs on the new commit."}), nil
	case s.State == suite.Passed:
		return w.checkPassed(found, checkSuite, "The full suite passed."), nil
	}
	return nil, nil
}

// ledgerRow is a row of the build ledger that qory reads: the id of the step, its pass (1
// for the first run of the step, one more for each later run; 0 when the row has none) and
// its reply.
type ledgerRow struct {
	ID    string       `json:"id,case:strict"`
	Pass  int          `json:"pass,case:strict"`
	Reply *ledgerReply `json:"reply,case:strict"`
}

// ledgerReply is the reply of a fix step: Kind is "fixed" or "disputed".
type ledgerReply struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// fixStep is a step id that fixes a finding: fix:f<number>.
var fixStep = regexp.MustCompile(`^fix:(f[1-9][0-9]{0,8})$`)

// replyEvents is the events for the replies of the build ledger, data, the newest
// build-ledger document: a reply_fixed or reply_disputed with the reason for each id
// fix:f<n> that has a row with a reply, when f<n> is an open finding that no reply follows
// since its last raise or repeat. The ledger is cumulative, so since, the highest pass of
// each fix:f<n> in the ledger before the session started (nil for none), keeps out the
// rows of earlier sessions: a row of a pass at or below since[id] has no event. A since
// that has the key [unknownPasses], for a ledger that qory could not read when the session
// started, gives no event at all. Of the rows that remain, the one of the highest pass
// that has a reply gives the event, and the later of two rows with one pass wins. A
// finding with no such row has no event: a fix step that is not run again after a repeat
// gives none, since the reply of the pass before it is not about the repeat. A row for a
// finding that is not open, or that the work item does not have, has no event. The error
// is for a ledger that is not JSON in the form, or a reply with another kind or a reason
// of no or more than 2,000 characters. by is the name of the workflow. A reply goes to an
// open finding of any stage of the work item: Qory Apiary stores a reply to a finding of
// another stage, and gives an error only for a raise, a close or a repeat on another
// stage.
//
// hidden is the number of ids that have a row with a reply and an open finding that no
// reply follows, and whose since is above every pass of the ledger: the ledger of an
// earlier session has a pass at or above since for each of its ids, so such a since is the
// pass of another ledger, and it keeps out each reply of the ledger. since has no such key
// for [unknownPasses].
//
// shortcut: a since that equals the highest pass of the ledger of an id is not counted,
// because a cumulative ledger of an earlier session has that too; replace it when the
// ledger names the session of each row.
//
// shortcut: a reply that a run stored and did not post before it ended is not posted by a
// later run, because findings have no time to tell it from a posted reply; replace it when
// findings carry the round of each reply.
func replyEvents(data []byte, by string, found contracts.Findings, since map[string]int) ([]contracts.FindingsPostEvent, int, error) {
	if _, unknown := since[unknownPasses]; unknown {
		return nil, 0, nil
	}
	if err := found.Check(); err != nil {
		return nil, 0, err
	}
	var ledger struct {
		Steps []ledgerRow `json:"steps,case:strict"`
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, 0, fmt.Errorf("The build ledger is not the JSON that the harness writes: %s", oneLine(err.Error(), 300))
	}
	var order []string
	latest := map[string]ledgerRow{}
	// top is the highest pass of each id in the ledger, and replied has the ids with a row
	// that has a reply.
	top := map[string]int{}
	replied := map[string]bool{}
	for _, row := range ledger.Steps {
		m := fixStep.FindStringSubmatch(row.ID)
		if m == nil {
			continue
		}
		top[m[1]] = max(top[m[1]], row.Pass)
		if row.Reply == nil {
			continue
		}
		replied[m[1]] = true
		if before, ran := since[m[1]]; ran && row.Pass <= before {
			continue
		}
		if top, seen := latest[m[1]]; !seen {
			order = append(order, m[1])
		} else if row.Pass < top.Pass {
			continue
		}
		latest[m[1]] = row
	}
	var events []contracts.FindingsPostEvent
	for _, id := range order {
		row := latest[id]
		if row.Reply.Kind != stateFixed && row.Reply.Kind != "disputed" {
			return nil, 0, fmt.Errorf("The reply of the step %s of the build ledger has the kind %s, not fixed or disputed", row.ID, strconv.Quote(cut(maskText(row.Reply.Kind), 40)))
		}
		if n := utf8.RuneCountInString(row.Reply.Reason); n < 1 || n > 2000 || strings.ContainsRune(row.Reply.Reason, 0) {
			return nil, 0, fmt.Errorf("The reply of the step %s of the build ledger has a reason of %d characters, not 1 to 2000 with no NUL", row.ID, n)
		}
		f := slices.IndexFunc(found.Findings, func(f contracts.Finding) bool { return f.ID == id })
		if f < 0 || found.Findings[f].State != stateOpen || repliedSinceOpened(found.Findings[f]) {
			continue
		}
		kind := eventReplyFixed
		if row.Reply.Kind == "disputed" {
			kind = eventReplyDisputed
		}
		events = append(events, contracts.FindingsPostEvent{Kind: kind, By: by, ID: id, Reason: row.Reply.Reason})
	}
	// A ledger that is cumulative has a pass at or above since for each id of an earlier
	// session. A since above each pass of the ledger is the pass of another ledger: it
	// hides the reply of each row that the finding waits for.
	hidden := 0
	for id := range replied {
		if f := slices.IndexFunc(found.Findings, func(f contracts.Finding) bool { return f.ID == id }); f >= 0 &&
			since[id] > top[id] && found.Findings[f].State == stateOpen && !repliedSinceOpened(found.Findings[f]) {
			hidden++
		}
	}
	return events, hidden, nil
}

// repliedSinceOpened reports whether f has a reply after its last raise or repeat.
func repliedSinceOpened(f contracts.Finding) bool {
	for _, e := range slices.Backward(f.Events) {
		switch e.Kind {
		case storedRepliedFixed, storedRepliedDisp:
			return true
		case storedRaised, storedRepeated, storedReopened, storedRepeatedAfter:
			return false
		}
	}
	return false
}

// sameTarget reports whether a and b are one target: the same collection and the same
// address, compared by value, or no address in both.
func sameTarget(a, b contracts.Target) bool {
	return a.Collection == b.Collection && (a.Address == nil) == (b.Address == nil) &&
		(a.Address == nil || *a.Address == *b.Address)
}

// basesToSend is the bases of the first batch of a round, for the target of the run: base,
// the commit that the diff of the round starts at, when no base is stored for the target
// (had, the bases of the GET), the run started with neither a version nor a pushed commit
// in the target's repository (version and pushed, as the run read them at its start,
// before the push of the run), and base is not "". A Verify round passes "" and sends
// none.
//
// shortcut: a version or a pushed commit of an earlier take in of the work item hides the
// first run of a new take in, so no base is written for that take in; replace when the
// snapshot names the take in. The run that writes no base leaves the reviewers of that
// take in the change of the run.
func basesToSend(had []contracts.FindingBase, target contracts.Target, base string, version []preparation.Repository, pushed []preparation.Landed) []contracts.FindingBase {
	if base == "" ||
		slices.ContainsFunc(had, func(b contracts.FindingBase) bool { return sameTarget(b.Target, target) }) {
		return nil
	}
	if a := target.Address; a != nil &&
		(slices.ContainsFunc(version, func(r preparation.Repository) bool { return r.System == a.System && r.Path == a.Path }) ||
			slices.ContainsFunc(pushed, func(l preparation.Landed) bool { return l.System == a.System && l.Path == a.Path })) {
		return nil
	}
	return []contracts.FindingBase{{Target: target, Base: base}}
}

// errBodyTooLarge is an event that makes a post of more than [maxPostBytes].
var errBodyTooLarge = errors.New("One event alone makes a findings post of more than 8 MiB")

// maskText is text masked as qory masks what it prints: with the run's secret values and
// the shapes of a key replaced. Text of an agent's file is masked before it is cut, since
// a cut inside a secret leaves the start of the secret in the text.
func maskText(text string) string { return secretmask.Default().Replace(text) }

// maskedEvents is events with each text that an agent's file can have written, the point,
// the problem, the fix, the proof, the reason and the place, masked as qory masks what
// it prints: the run's secret values and the shapes of a key. The reports and the build
// ledger are files that the run's masker has not seen.
func maskedEvents(events []contracts.FindingsPostEvent) []contracts.FindingsPostEvent {
	mask := maskText
	out := slices.Clone(events)
	for i := range out {
		e := &out[i]
		e.Point, e.Problem, e.Fix, e.Proof, e.Reason = mask(e.Point), mask(e.Problem), mask(e.Fix), mask(e.Proof), mask(e.Reason)
		if e.Place != nil {
			place := *e.Place
			place.Quote, place.File = mask(place.Quote), mask(place.File)
			e.Place = &place
		}
	}
	return out
}

// postBatches splits events into posts of at most 100 events and at most 8 MiB of body, in
// order, with bases in the first. Each post passes FindingsPost.Check. No events
// make no post.
func postBatches(bases []contracts.FindingBase, events []contracts.FindingsPostEvent) ([]contracts.FindingsPost, error) {
	var posts []contracts.FindingsPost
	for len(events) > 0 {
		post := contracts.FindingsPost{Events: []contracts.FindingsPostEvent{}}
		if len(posts) == 0 {
			post.Bases = bases
		}
		head, err := json.Marshal(post)
		if err != nil {
			return nil, err
		}
		size := len(head)
		for len(events) > 0 && len(post.Events) < maxPostEvents {
			data, err := json.Marshal(events[0])
			if err != nil {
				return nil, err
			}
			if size+len(data)+1 > maxPostBytes {
				break
			}
			size += len(data) + 1
			post.Events = append(post.Events, events[0])
			events = events[1:]
		}
		if len(post.Events) == 0 {
			return nil, errBodyTooLarge
		}
		if err := post.Check(); err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, nil
}

// errNotKnown is a batch that the two GETs do not show to be stored or not.
var errNotKnown = fmt.Errorf("The two GETs do not show whether the lost post was stored: a GET lists at most %d findings and %d events of each, so a second send could write the findings twice", maxListedFindings, maxListedEvents)

// storedKinds is the kinds of the event that a GET lists for each kind of an event of a
// post.
var storedKinds = map[string][]string{
	eventRaise:         {storedRaised},
	eventRepeat:        {storedRepeated, storedReopened, storedRepeatedAfter},
	eventReplyFixed:    {storedRepliedFixed},
	eventReplyDisputed: {storedRepliedDisp},
	eventCloseFixed:    {storedClosedFixed},
	eventCloseDismiss:  {storedClosedDismiss},
}

// eventCount is the number of events of f of the kinds, written by the qory actor by.
func eventCount(f contracts.Finding, kinds []string, by string) int {
	n := 0
	for _, e := range f.Events {
		if slices.Contains(kinds, e.Kind) && e.Actor.Kind == "qory" && e.Actor.Name == by {
			n++
		}
	}
	return n
}

// findingsStored reports whether batch is stored, after a post whose response was lost.
// before is the GET before the post, and after the GET after the lost response. A post is
// one transaction and each of its events writes one event, so one proven event proves the
// batch. A raise is proven by a finding that is new in after, with the actor of the raise
// and the point, the proof and the place of the event. Any other event is proven by more
// events of its kind by its actor on its finding in after than in before; a base proves
// nothing. When no event is proven and the answer is unknown, the error is to stop:
// before has 500 findings, which may hide the new ones, or a finding of the batch has 100
// events, which may hide the new one.
func findingsStored(batch contracts.FindingsPost, before, after contracts.Findings) (bool, error) {
	for _, g := range []contracts.Findings{before, after} {
		if err := g.Check(); err != nil {
			return false, err
		}
	}
	old := map[string]contracts.Finding{}
	for _, f := range before.Findings {
		old[f.ID] = f
	}
	unknown := len(before.Findings) >= maxListedFindings
	for _, e := range batch.Events {
		for _, f := range after.Findings {
			if e.Kind == eventRaise {
				if _, was := old[f.ID]; !was && sameRaise(e, f) {
					return true, nil
				}
				continue
			}
			if f.ID != e.ID {
				continue
			}
			kinds := storedKinds[e.Kind]
			if eventCount(f, kinds, e.By) > eventCount(old[f.ID], kinds, e.By) {
				return true, nil
			}
			if len(f.Events) >= maxListedEvents {
				unknown = true
			}
		}
	}
	if unknown {
		return false, errNotKnown
	}
	return false, nil
}

// sameRaise reports whether finding f is what the raise event e writes: its raise by the
// actor e.By, and the point, the proof and the place of e.
func sameRaise(e contracts.FindingsPostEvent, f contracts.Finding) bool {
	if e.Place == nil || f.Point != e.Point || f.Proof != e.Proof {
		return false
	}
	p := f.Place
	if p.Quote != e.Place.Quote || p.File != e.Place.File || p.Line != e.Place.Line {
		return false
	}
	return eventCount(f, []string{storedRaised}, e.By) > 0
}

// openBlockingOf is the count of open blocking findings of the stage after a post: from
// the answer when there is one (a stage that the answer does not list has none), and from
// got, a GET, otherwise. The answer and the GET are checked.
func openBlockingOf(stage string, answer *contracts.FindingsPostAnswer, got contracts.Findings) (int, error) {
	if answer != nil {
		if err := answer.Check(); err != nil {
			return 0, err
		}
		for _, o := range answer.OpenBlocking {
			if o.Stage == stage {
				return o.Count, nil
			}
		}
		return 0, nil
	}
	if err := got.Check(); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range got.Findings {
		if f.Stage == stage && f.State == stateOpen && f.Severity == reviewround.Blocking {
			n++
		}
	}
	return n, nil
}

// roundVerdict is the verdict of a review round for the stage: passed when no open
// blocking finding is left, and changes-requested, with no points, otherwise. See
// [openBlockingOf].
func roundVerdict(stage string, answer *contracts.FindingsPostAnswer, got contracts.Findings) (string, error) {
	n, err := openBlockingOf(stage, answer, got)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return runapi.VerdictPassed, nil
	}
	return runapi.VerdictChangesRequested, nil
}
