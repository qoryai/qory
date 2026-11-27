package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
)

// findingsRun is what a run needs to read and post the findings of its work item: the
// poster, the run's target, and the branch's Version and Pushed as the run read them at
// its start, before any push of the run.
type findingsRun struct {
	poster *findingsPoster
	run    openedRun
	code   *codeRun
	runID  string
	target contracts.Target
	// version, pushed and start are [preparation.WorkBranch.Version], its Pushed and the
	// Start of the target repository at the run's start. A push of the run changes
	// the Version of the branch, so a post after a push reads these.
	version []preparation.Repository
	pushed  []preparation.Landed
	start   string
	// occasions is the number of occasions that [findingsRun.occasion] handed out.
	occasions int
	// since is the highest pass of each fix:f<n> step in the newest build ledger of the stage
	// when the latest session that writes started, from any writer: that session posts no
	// reply of a pass at or below it, since the pass ran in an earlier session. It has the
	// key [unknownPasses] when that ledger was one that qory could not read.
	since map[string]int
}

// newFindingsRun is the findings of a dispatched run that has a target and a run API; nil
// for any other run, which has no findings to read or post.
func newFindingsRun(run openedRun, code *codeRun, runID string) *findingsRun {
	if code == nil || code.targetRef == nil || !hasRunAPI(run) {
		return nil
	}
	return &findingsRun{poster: newFindingsPoster(run), run: run, code: code, runID: runID, target: *code.targetRef}
}

// active reports whether the run reads and posts findings: the work item that the run read
// at its start has a stage with a round, as a review join needs it. A run of a work item
// with no stage has no findings.
func (f *findingsRun) active() bool {
	if f == nil || f.code.snapshot == nil {
		return false
	}
	s := f.code.snapshot.Stage
	return s.ID != nil && s.Round != nil && s.Rounds != nil
}

// begin records Version and Pushed of branch, and the Start of its target repository, as
// the run starts: call it once the branch is checked out and before any push.
func (f *findingsRun) begin(branch *preparation.WorkBranch) {
	if f == nil || branch == nil {
		return
	}
	f.version, f.pushed = slices.Clone(branch.Version), slices.Clone(branch.Pushed)
	for _, r := range branch.Repositories {
		if r.Dir == "." {
			f.start = r.Start
		}
	}
}

// occasion is a new number for the events of one step of a session, for
// [findingsPoster.post].
func (f *findingsRun) occasion() int {
	f.occasions++
	return f.occasions
}

// bases is the bases to send with the first batch of a post, see [basesToSend], for the
// diff base base and the GET found that was read before the post.
func (f *findingsRun) bases(found contracts.Findings, base string) []contracts.FindingBase {
	return basesToSend(found.Bases, f.target, base, f.version, f.pushed)
}

// ledgerBase is the base that the GET found has for target, "" for none.
func ledgerBase(found contracts.Findings, target contracts.Target) string {
	for _, b := range found.Bases {
		if sameTarget(b.Target, target) {
			return b.Base
		}
	}
	return ""
}

// stageNow is the stage of the work item as the run API shows it now.
type stageNow struct {
	id, name string
	round    int
}

// slug is the name of the stage in a finding, see [stageName].
func (s stageNow) slug() string { return stageName(s.name, s.id) }

// stageOf is the stage of snapshot, false when it has no id, round or number of rounds, or
// a round outside 1 to rounds.
func stageOf(snapshot *runapi.Snapshot) (stageNow, bool) {
	s := snapshot.Stage
	if s.ID == nil || s.Round == nil || s.Rounds == nil || *s.Round < 1 || *s.Round > *s.Rounds {
		return stageNow{}, false
	}
	now := stageNow{id: *s.ID, round: *s.Round}
	if s.Name != nil {
		now.name = *s.Name
	}
	return now, true
}

// readStage reads the work item again and returns its stage. The error is [reviewStop]'s
// form, "so ... did not" with the what of the step that needs the stage.
func readStage(ctx context.Context, client *runapi.Client, what string) (stageNow, error) {
	snapshot, err := client.WorkItem(ctx)
	if err != nil {
		return stageNow{}, withCause(reviewStop("Reading the stage's round failed, so "+what, shownError(err)), err)
	}
	now, ok := stageOf(snapshot)
	if !ok {
		return stageNow{}, reviewStop("The work item's stage has no valid round, so "+what, "The work item needs stage.round and stage.rounds with 1 <= round <= rounds")
	}
	return now, nil
}

// client is the client of the run's run API. what ends the cause of the stop: "so qory
// ...".
func (f *findingsRun) client(what string) (*runapi.Client, error) {
	httpClient, base, err := f.run.RunAPI()
	var client *runapi.Client
	if err == nil {
		client, err = runapi.New(base, httpClient)
	}
	if err != nil {
		return nil, withCause(reviewStop("The run API client was not made, so "+what, shownError(err)), err)
	}
	return client, nil
}

// maxLedger is the most bytes of the build ledger that qory reads.
const maxLedger = 4 << 20

// maxShownLedgerName is the most characters of a ledger's file name that a stop shows
// on line 2, and maxCheckedLedgerName the most that the check of the warning for a
// ledger of another writer shows, each with "..." after a cut, as [projectFilesStop]
// cuts a path. The check is shown cut at 200 characters, so its name is cut at 100 and
// the end of the check stays.
const (
	maxShownLedgerName   = 200
	maxCheckedLedgerName = 100
)

// newestLedger is the newest build-ledger version of the stage, nil for a work item with
// no such version or no artifacts. With a runID it is nil also for a newest version that
// another run or a person wrote; with "" the writer does not matter. effect is what the
// stop means for the run, as the cause line ends: "qory posted no replies".
func newestLedger(ctx context.Context, client *runapi.Client, stage, runID, effect string) (*runapi.Version, error) {
	index, err := client.Artifacts(ctx)
	if featureOff(err) {
		return nil, nil
	}
	if err != nil {
		return nil, withCause(reviewStop("Reading the build ledger failed, so "+effect, shownError(err)), err)
	}
	v := newestOfType(index, "build-ledger", stage)
	if v == nil || runID != "" && (v.Writer.Kind != "run" || v.Writer.RunID != runID) {
		return nil, nil
	}
	return v, nil
}

// ledgerUnread is the stop of a build ledger that fails check, the first check of qory
// that it fails, in the words of that check's error.
type ledgerUnread struct {
	stop, check error
}

func (e ledgerUnread) Error() string { return e.stop.Error() }
func (e ledgerUnread) Unwrap() error { return e.stop }

// readLedger is the file of v, a version from [newestLedger]. bad is the [ledgerUnread]
// for a version that is not one file of at most maxLedger bytes under a name that qory can
// write to disk: the harness writes no such version. err is the stop for a download that
// failed.
func readLedger(ctx context.Context, client *runapi.Client, v *runapi.Version, effect string) (data []byte, bad, err error) {
	detail := fmt.Sprintf("A build ledger is one file of at most %d bytes, and the version is %s", maxLedger, v.ID)
	switch {
	case len(v.Files) != 1:
		check := fmt.Errorf("The newest build ledger has %d files, and the harness writes one", len(v.Files))
		return nil, ledgerUnread{reviewStop(check.Error()+", so "+effect, detail), check}, nil
	case !runapi.LocalFilename(v.Files[0].Filename):
		name := v.Files[0].Filename
		check := fmt.Errorf("The newest build ledger has the file name %s, which is not allowed",
			strconv.Quote(integration.PrintableWithin(name, maxCheckedLedgerName)))
		stop := reviewStop("The newest build ledger has a file name that is not allowed, so "+effect,
			fmt.Sprintf("The file name is %s, and the version is %s",
				strconv.Quote(integration.PrintableWithin(name, maxShownLedgerName)), v.ID))
		return nil, ledgerUnread{stop, check}, nil
	case v.Files[0].Size > maxLedger:
		check := fmt.Errorf("The newest build ledger file has %d bytes, more than %d MiB", v.Files[0].Size, maxLedger>>20)
		return nil, ledgerUnread{reviewStop(check.Error()+", so "+effect, detail), check}, nil
	}
	var buf bytes.Buffer
	if err := client.Download(ctx, v.ID, v.Files[0], &buf); err != nil {
		return nil, nil, withCause(reviewStop("Fetching the build ledger failed, so "+effect, shownError(err)), err)
	}
	return buf.Bytes(), nil, nil
}

// buildLedger is the file of the newest build-ledger version of the stage, nil for a work
// item with no such version or no artifacts, as [newestLedger] has it. A version that
// [readLedger] finds unreadable is a stop.
func buildLedger(ctx context.Context, client *runapi.Client, stage, runID, effect string) ([]byte, error) {
	v, err := newestLedger(ctx, client, stage, runID, effect)
	if v == nil || err != nil {
		return nil, err
	}
	data, bad, err := readLedger(ctx, client, v, effect)
	if bad != nil {
		return nil, bad
	}
	return data, err
}

// maxLedgerPass is the highest pass that the harness writes: the build ledger's format
// (build-ledger.schema.json, "pass") has an integer from 1 to 100. A row with no pass
// reads as 0.
const maxLedgerPass = 100

// ledgerPasses is the highest pass of each id fix:f<n> that data, a build-ledger
// document, has a row for, keyed by f<n>: the fix steps that ran before the session that
// reads data. The error is for a document that is not JSON in the form of the ledger, or
// has a row with a pass that the harness does not write: one above [maxLedgerPass] would
// hide every reply, so the document cannot be trusted.
func ledgerPasses(data []byte) (map[string]int, error) {
	var ledger struct {
		Steps []ledgerRow `json:"steps,case:strict"`
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("The build ledger is not the JSON that the harness writes: %s", oneLine(err.Error(), 300))
	}
	passes := map[string]int{}
	for i, row := range ledger.Steps {
		if row.Pass < 0 || row.Pass > maxLedgerPass {
			return nil, fmt.Errorf("Row %d of the build ledger has the pass %d, and the harness writes 1 to %d", i+1, row.Pass, maxLedgerPass)
		}
		if m := fixStep.FindStringSubmatch(row.ID); m != nil {
			passes[m[1]] = max(passes[m[1]], row.Pass)
		}
	}
	return passes, nil
}

// clearOpen removes open-findings.json of the session before from in, the run's inputs
// folder, so a session that gets none does not read the file of an earlier one.
func (f *findingsRun) clearOpen(in string) error {
	path := filepath.Join(in, openFindingsFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return reviewStop(fmt.Sprintf("Removing %s failed, so the session did not start", path), shownError(err))
	}
	return nil
}

// unknownPasses is the key of a since for a ledger that qory cannot read: no step id has
// it, and [replyEvents] returns no event for a since that has it.
const unknownPasses = "unknown"

// textLedgerUnread is line 1 of qory run for a session that starts after a build ledger of
// another run or a person that qory cannot read, with the ledger's version number. It goes
// after "qory run: ", since the status output takes only the lines that start with it.
// Line 2 is the first check that the ledger fails.
const textLedgerUnread = "this session posts no replies: build ledger version %d fails qory's checks"

// earlierPasses is the highest pass of each fix:f<n> in v, the newest build ledger of
// the stage when a session that writes starts, as [ledgerPasses] has them. A ledger that
// this run wrote and that qory cannot read, and a failed download, stop the run. For a
// ledger of another run or a person that qory cannot read (see [readLedger] and
// [ledgerPasses]), the since is [unknownPasses], and w gets two lines of qory,
// [textLedgerUnread] and the first check that the ledger fails: the session starts and
// posts no replies, since qory cannot tell which passes ran before it.
func (f *findingsRun) earlierPasses(ctx context.Context, client *runapi.Client, v *runapi.Version, w io.Writer) (map[string]int, error) {
	const effect = "the session did not start"
	data, bad, err := readLedger(ctx, client, v, effect)
	if err != nil {
		return nil, err
	}
	if bad == nil {
		var passes map[string]int
		if passes, err = ledgerPasses(data); err == nil {
			return passes, nil
		}
		bad = ledgerUnread{reviewStop("The newest build ledger does not pass qory's checks, so "+effect, shownError(err)), err}
	}
	if v.Writer.Kind == "run" && v.Writer.RunID == f.runID {
		return nil, bad
	}
	check := bad
	var unread ledgerUnread
	if errors.As(bad, &unread) {
		check = unread.check
	}
	// Line 2 starts with "qory run: " too, so the status descriptor gets it, see
	// [stepLines.status].
	fmt.Fprintf(w, "qory run: "+textLedgerUnread+"\nqory run: %s\n", v.Number, shownError(check))
	return map[string]int{unknownPasses: 0}, nil
}

// writeOpen writes open-findings.json into in, the run's inputs folder, read-only: the
// open findings of the work item, with the replies to them, as [builderFindings] has them.
// A work item with no open finding gets no file. It also reads the passes of the newest
// build ledger of the stage into f.since, before the session runs; w gets the line for a
// ledger that qory cannot read, see [findingsRun.earlierPasses].
func (f *findingsRun) writeOpen(ctx context.Context, in string, w io.Writer) error {
	client, err := f.client("the session did not start")
	if err != nil {
		return err
	}
	stage, err := readStage(ctx, client, "the session did not start")
	if err != nil {
		return err
	}
	earlier, err := newestLedger(ctx, client, stage.id, "", "the session did not start")
	if err != nil {
		return err
	}
	f.since = nil
	if earlier != nil {
		if f.since, err = f.earlierPasses(ctx, client, earlier, w); err != nil {
			return err
		}
	}
	found, err := f.poster.read(ctx, "the session did not start")
	if err != nil {
		return err
	}
	if err := fullWindow(found, "the session did not start"); err != nil {
		return err
	}
	data, err := builderFindings(stage.round, found)
	if err != nil {
		return reviewStop("The findings of the work item do not pass qory's checks, so the session did not start", shownError(err))
	}
	var file findingsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if len(file.Findings) == 0 {
		return nil
	}
	path := filepath.Join(in, openFindingsFile)
	if err := os.WriteFile(path, data, 0o444); err != nil {
		return reviewStop(fmt.Sprintf("Writing %s failed, so the session did not start", path), shownError(err))
	}
	return nil
}

// openFindingsFile is the file of the open findings in the run's inputs folder.
const openFindingsFile = "open-findings.json"

// postReplies posts the replies of the build ledger to the findings of the work item,
// after a write session pushed: a reply for each row of the newest ledger of the stage
// that has one and whose finding is open and has no reply since it was raised or
// repeated. A work item with no ledger, or no new reply, posts nothing. When the passes
// of an earlier ledger keep out the replies of the ledger, w gets a line for them.
func (f *findingsRun) postReplies(ctx context.Context, w io.Writer) error {
	const what = "the stage did not end"
	client, err := f.client("qory posted no replies")
	if err != nil {
		return err
	}
	stage, err := readStage(ctx, client, "qory posted no replies")
	if err != nil {
		return err
	}
	data, err := buildLedger(ctx, client, stage.id, f.runID, "qory posted no replies")
	if err != nil || data == nil {
		return err
	}
	found, err := f.poster.read(ctx, what)
	if err != nil {
		return err
	}
	if err := fullWindow(found, what); err != nil {
		return err
	}
	events, hidden, err := replyEvents(data, f.code.workflow, found, f.since)
	if err != nil {
		return reviewStop("The build ledger has a reply that qory cannot post, so the stage did not end", shownError(err))
	}
	if hidden == 1 {
		fmt.Fprintln(w, "qory run: 1 reply of the build ledger is not posted: an earlier ledger has a higher pass")
	} else if hidden > 1 {
		fmt.Fprintf(w, "qory run: %d replies of the build ledger are not posted: an earlier ledger has a higher pass\n", hidden)
	}
	if len(events) == 0 {
		return nil
	}
	_, _, err = f.poster.post(ctx, f.occasion(), what, f.bases(found, f.start), events)
	return err
}

// postSuite posts the check finding suite for the full suite of a session that ended as
// ending: a red suite raises or repeats it, a green one closes it, a skipped one posts
// nothing, and a run with no findings posts nothing.
func (f *findingsRun) postSuite(ctx context.Context, ending suiteEnding) error {
	if !f.active() || ending.State == suite.Skipped {
		return nil
	}
	client, err := f.client("qory posted no finding of the suite")
	if err != nil {
		return err
	}
	stage, err := readStage(ctx, client, "qory posted no finding of the suite")
	if err != nil {
		return err
	}
	return f.postSuiteOf(ctx, stage.slug(), ending)
}

// postSuiteOf is [findingsRun.postSuite] for the stage whose name in a finding is slug.
func (f *findingsRun) postSuiteOf(ctx context.Context, slug string, ending suiteEnding) error {
	const what = "the session did not end"
	found, err := f.poster.read(ctx, what)
	if err != nil {
		return err
	}
	if err := fullWindow(found, what); err != nil {
		return err
	}
	events, err := where{f.target, slug}.suiteEvents(&refs{}, ending, found)
	if err != nil {
		return reviewStop("The findings of the work item do not pass qory's checks, so qory posted no finding of the suite", shownError(err))
	}
	if len(events) == 0 {
		return nil
	}
	_, _, err = f.poster.post(ctx, f.occasion(), what, f.bases(found, f.start), events)
	return err
}

// newestVerdict is the id of the newest verdict that this run wrote, "" for none.
func newestVerdict(ctx context.Context, client *runapi.Client, runID string) (string, error) {
	newest, err := newestVerdictVersion(ctx, client, runID)
	if newest == nil || err != nil {
		return "", err
	}
	return newest.ID, nil
}

// newestVerdictVersion is the newest verdict version that this run wrote, nil for none.
func newestVerdictVersion(ctx context.Context, client *runapi.Client, runID string) (*runapi.Version, error) {
	index, err := client.Artifacts(ctx)
	if featureOff(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var newest *runapi.Version
	for i := range index.Artifacts {
		a := &index.Artifacts[i]
		if a.Type != verdictType || a.Name != defaultName {
			continue
		}
		for j := range a.Versions {
			v := &a.Versions[j]
			if v.Writer.Kind == "run" && v.Writer.RunID == runID && !v.Cancelled && !v.Withdrawn && (newest == nil || v.Number > newest.Number) {
				newest = v
			}
		}
	}
	return newest, nil
}

// noteVerdict records the newest verdict that the run wrote before the review session
// joins, which [findingsRun.finishReview] compares with the newest one after it.
func (p *reviewJoinPlan) noteVerdict(ctx context.Context) error {
	if p.findings == nil {
		return nil
	}
	id, err := newestVerdict(ctx, p.client, p.findings.runID)
	if err != nil {
		return withCause(reviewStop("Reading the run's verdict failed, so the review did not start", shownError(err)), err)
	}
	p.verdictBefore = id
	return nil
}

// hasReports reports whether the reports folder has a file whose name ends in .json.
func hasReports(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasSuffix(e.Name(), ".json") })
}

// codeOpenBlocking is the code of the refusal of the verdict passed while the stage has an
// open blocking finding.
const codeOpenBlocking = "open_blocking_findings"

// roundEvents are the events of the review round of p, from found, the findings of the
// work item: the report of each expected role, read from the reports folder, and the
// closing of the missing-report finding of a role that is no longer expected. With
// missing, the round is the one of a session that left no report: it does not read the
// reports folder, so a report that a later check finds there is not posted in this round,
// and it is the raise or repeat of the report-<role> finding of each expected role only.
func (f *findingsRun) roundEvents(p *reviewJoinPlan, found contracts.Findings, missing bool) ([]contracts.FindingsPostEvent, error) {
	w := where{f.target, p.stageSlug}
	// A changed-files list that was not read makes every role of the group expected.
	byPath := p.roles
	if p.changedErr == nil {
		byPath = expectedReviewersFor(p.res, p.workflow, p.changed)
	}
	roles := expectedRoles(p.roles, byPath, found, p.stageSlug)
	var reports []roleReport
	for _, role := range roles {
		if missing {
			reports = append(reports, roleReport{Role: role, Why: reviewround.NoReport(role).Error()})
			continue
		}
		report, err := reviewround.Read(p.reports, role+".json", role, maskText)
		reports = append(reports, readRoleReport(role, report, err, found, p.stageSlug))
	}
	events, err := w.reportsEvents(&refs{}, reports, found)
	if err != nil {
		return nil, reviewStop("The findings of the work item do not pass qory's checks, so the review did not end", shownError(err))
	}
	if missing {
		return events, nil
	}
	return append(events, w.staleReportCloses(p.roles, byPath, found, p.fromLedger, p.changedErr == nil)...), nil
}

// finishReview posts the findings of the review round and sends its verdict, after the
// review session ended and succeeded. It reads the report of each expected role and posts
// the events. If the session recorded a verdict, qory sends none, and when the session
// also left no report, qory posts nothing. Otherwise the verdict is passed when no open
// blocking finding of the stage is left, and changes-requested when one is.
func (f *findingsRun) finishReview(ctx context.Context, p *reviewJoinPlan) error {
	if f == nil || p.findings == nil {
		return nil
	}
	id, err := newestVerdict(ctx, p.client, f.runID)
	if err != nil {
		return withCause(reviewStop("Reading the run's verdict failed, so the review did not end", shownError(err)), err)
	}
	recorded := id != p.verdictBefore
	if recorded && !hasReports(p.reports) {
		return nil
	}
	answer, after, err := f.poster.round(ctx, f.occasion(), "the review did not end", func(found contracts.Findings) ([]contracts.FindingBase, []contracts.FindingsPostEvent, error) {
		events, err := f.roundEvents(p, found, false)
		return f.bases(found, p.base), events, err
	})
	if err != nil || recorded {
		return err
	}
	verdict, err := roundVerdict(p.stageSlug, answer, after)
	if err != nil {
		return reviewStop("The answer of the findings does not pass qory's checks, so the review did not end", shownError(err))
	}
	err = postVerdict(ctx, p.client, verdict, nil, nil, maxSubmissionTries)
	if verdict == runapi.VerdictPassed && runapi.HasCode(err, codeOpenBlocking) {
		err = postVerdict(ctx, p.client, runapi.VerdictChangesRequested, nil, nil, maxSubmissionTries)
	}
	if err != nil {
		return withCause(reviewStop("Posting the verdict of the review failed, so the review did not end", shownError(err)), err)
	}
	return nil
}
