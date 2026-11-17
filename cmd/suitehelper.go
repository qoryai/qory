package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
	"github.com/qoryai/qory/internal/verdictformat"
)

// The type and the file of the build plan, and the most bytes of its file qory reads.
const (
	buildPlanType = "build-plan"
	buildPlanFile = "build-plan.md"
	maxBuildPlan  = 1 << 20
)

// The lines of qory run about the full suite. A line with an instruction starts with the fix,
// and has at most 100 characters in its first line.
const (
	textSuiteNotStarted = "Add a full row to the build plan and take the work item in again: no suite starts.\n  %s"
	textSuiteNoGit      = "Run the session in the top folder of a git repository: no suite starts without a .git folder."
	textSuiteSkipped    = "the full suite did not run: the session left HEAD at the commit it started at"
	textSuiteNoAfter    = "the full suite did not run: the run was closed before the after step started"
	textSuiteEnded      = "the full suite%s is %s%s"
	textSuiteGreen      = "the full suite passed, so qory posts no verdict"
	textSuiteCut        = "qory run was stopped while the full suite ran, so the run closes cancelled"
	textSuiteStands     = "the verdict %s of the agent stands over the red full suite"
	textSuitePosted     = "posted the verdict %s for the red full suite"
	textSuiteNoRoom     = "the verdict escalate has 20 points already, so qory adds none for the red full suite"
	textSuiteNoPost     = "Take the work item in again in Qory Apiary: qory could not post the verdict of the red full suite.\n  %s"
)

// The lines of the point of the verdict that a red suite sets. Each is one sentence on one line
// of at most 100 characters in its first line.
const (
	textPointHosts    = "Allow %s in the run's policy, or change the suite: it failed after %s %s refused."
	textPointDenied   = "Refused connections: the run's Connections tab, Denied."
	textPointFailed   = "The full suite failed: %d of %d tests failed."
	textPointExit     = "The full suite failed: its command exited with status %d."
	textPointNoTests  = "The full suite ran 0 tests, so it proves nothing."
	textPointMismatch = "The full suite failed: the totals of its result file do not match its testcases."
	textPointNoStart  = "The full suite did not start: its script exited with status %d before the command ran."
	textPointStopped  = "The full suite did not finish: the run was stopped."
	textPointTimeout  = "The full suite did not finish within %d minutes, and qory stopped it."
	maxPointLine      = 100
)

// suiteMinutes is the time the suite's cap counts in: the cap is limit minutes.
const suiteMinutes = time.Minute

// maxVerdictPoints is the most points a verdict has, 20, as the verdict format allows.
const maxVerdictPoints = 20

// errNoVerdictFile is the error of a version of the verdict of the run that has no file.
var errNoVerdictFile = errors.New("the run's verdict has no file")

// errNoBuildPlan is why a run has no suite command: the run started with no build plan, or qory
// could not read it.
var errNoBuildPlan = errors.New("the run started with no build plan")

// nextSession is what qory knows of the next session from Qory Apiary's next-session answer.
// qory reads no such answer yet, so no run has one.
type nextSession struct {
	// RunsSuite is the answer's yes or no to whether the session that ends runs the full
	// suite: yes for a workflow in a loop or with first_attempt, no otherwise.
	RunsSuite bool
}

// nextSessionAnswer is the answer for the session that is about to join. It returns nil until
// qory calls Qory Apiary's next-session call, which a run of several sessions needs.
var nextSessionAnswer = func() *nextSession { return nil }

// runsSuite reports whether the session runs the full suite: only when the answer is yes.
func runsSuite(answer *nextSession) bool { return answer != nil && answer.RunsSuite }

// suiteSetup is what [openSuites] needs from a run that has its session about to join.
type suiteSetup struct {
	run    openedRun
	spec   *runSpec
	runID  string
	walled bool
	// readOnly is whether the run only reads the checkout, so no commit comes of it.
	readOnly bool
	// extensions is the harness extensions of the report that the run composed.
	extensions map[string]any
	said       func(string)
}

// suiteRun is the full suite of one session: the after step that runs it, and the judgement of
// its outcome and the post of its verdict, see [suiteRun.after].
type suiteRun struct {
	run     openedRun
	runID   string
	said    func(string)
	dir     string
	full    suite.Full
	minutes int
}

// openSuites sets the after step of the full suite on the spec of the session that is about to
// join, and returns the run that judges it afterwards; nil when the session runs none: a run with
// no wall, no run API or no write access, a next-session answer that is no, a build plan with no
// full row, or a checkout with no .git folder. The command is the full row of the build plan of
// the run's launch, read here from the run API, so a plan that the agent pushes during the session
// changes nothing. The after step starts after the agent's container is removed, and its
// time limit is the suite's cap.
func openSuites(ctx context.Context, s suiteSetup) *suiteRun {
	if !s.walled || s.readOnly || !runsSuite(nextSessionAnswer()) {
		return nil
	}
	if _, _, err := s.run.RunAPI(); err != nil {
		return nil
	}
	planCtx, cancelPlan := context.WithTimeout(ctx, suitePlanWait)
	full, err := launchFull(planCtx, s.run)
	cancelPlan()
	if err != nil {
		s.said(fmt.Sprintf(textSuiteNotStarted, shownError(err)))
		return nil
	}
	if info, err := os.Lstat(filepath.Join(s.spec.Dir, ".git")); err != nil || !info.IsDir() {
		s.said(textSuiteNoGit)
		return nil
	}
	after, err := suite.Command(headCommit(ctx, s.spec.Dir), full)
	if err != nil {
		s.said(fmt.Sprintf(textSuiteNotStarted, shownError(err)))
		return nil
	}
	minutes := suite.LimitMinutes(s.extensions)
	s.spec.After, s.spec.AfterTimeout = after, time.Duration(minutes)*suiteMinutes
	return &suiteRun{run: s.run, runID: s.runID, said: s.said, dir: s.spec.Dir, full: full, minutes: minutes}
}

// suitePlanWait is the longest the read of the build plan at the run's start may take.
const suitePlanWait = 15 * time.Second

// headCommit is the commit that HEAD has in dir, read with git as qory runs it on this machine
// before the agent runs, "" when dir has none.
func headCommit(ctx context.Context, dir string) string {
	args := slices.Concat(proxygit.Hardened, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"})
	cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), args...)
	if err != nil {
		return ""
	}
	cmd.Env = proxygit.Env(cmd.Env, &proxygit.Proxy{})
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// launchFull is the full row of the build plan that the run's work item has at the run's launch,
// read through the run API, from the work item's artifacts. An error is [errNoBuildPlan] or an error of [suite.FullRow].
func launchFull(ctx context.Context, run openedRun) (suite.Full, error) {
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	text, err := readBuildPlan(ctx, client)
	if err != nil {
		return suite.Full{}, err
	}
	return suite.FullRow(text)
}

// readBuildPlan is the text of the newest version of the artifact build-plan, as the index
// lists it: the name main, or the one name; a withdrawn name has none.
func readBuildPlan(ctx context.Context, client *runapi.Client) (string, error) {
	index, err := client.Artifacts(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	var plans []runapi.Artifact
	for _, a := range index.Artifacts {
		if a.Type == buildPlanType && !withdrawn(a) {
			plans = append(plans, a)
		}
	}
	chosen := -1
	for i, a := range plans {
		if a.Name == defaultName || len(plans) == 1 {
			chosen = i
			break
		}
	}
	if chosen < 0 {
		return "", errNoBuildPlan
	}
	version := newestBeforeWithdrawal(plans[chosen])
	if version == nil {
		return "", errNoBuildPlan
	}
	for _, file := range version.Files {
		if file.Filename != buildPlanFile {
			continue
		}
		if file.Size > maxBuildPlan {
			return "", fmt.Errorf("%w: %s is over %d bytes", errNoBuildPlan, buildPlanFile, maxBuildPlan)
		}
		var text bytes.Buffer
		if err := client.Download(ctx, version.ID, file, &text); err != nil {
			return "", fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
		}
		return text.String(), nil
	}
	return "", errNoBuildPlan
}

// after judges the suite once the session exited with out and res, before the push step, and
// posts the verdict of a red suite, see [suiteRun.post]. A red suite does not change the outcome
// the run closes with: the run closes as its session did, and the posted verdict starts the fix
// round. A post that fails closes the run failed, with exit status 1. The state folder of the
// workspace is removed in every case.
func (r *suiteRun) after(ctx, again context.Context, limit time.Time, res *session.Result, out session.Outcome) (session.Outcome, error) {
	if r == nil {
		return out, nil
	}
	defer func() {
		if err := suite.Clear(r.dir); err != nil {
			r.said("remove " + suite.StateFolder + " in the workspace: " + shownError(err))
		}
	}()
	if out.State != event.StateSucceeded {
		return out, nil
	}
	if res == nil || res.After == nil {
		r.said(textSuiteNoAfter)
		return out, nil
	}
	a := res.After
	got := suite.Judge(r.dir, r.full, suite.End{
		Succeeded: a.State == event.StateSucceeded,
		Cancelled: a.State == event.StateCancelled,
		TimedOut:  a.Reason == event.ReasonTimeout,
		ExitCode:  a.ExitCode,
	})
	switch {
	case got.State == suite.Skipped:
		r.said(textSuiteSkipped)
		return out, nil
	case got.State == suite.Passed:
		r.said(fmt.Sprintf(textSuiteEnded, onCommit(got.Commit), got.State, countsSuffix(got.Counts)))
		r.said(textSuiteGreen)
		return out, nil
	case got.State == suite.Stopped:
		return r.cut(ctx, again, limit, got)
	}
	r.said(fmt.Sprintf(textSuiteEnded, onCommit(got.Commit), got.State, countsSuffix(got.Counts)))
	refused := suite.ReadRefused(filepath.Join(r.run.Dir(), sink.EventsFile), a.Step)
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	posted, err := r.post(postCtx, suitePoint(got, refused, r.minutes), maxBranchTries)
	if err != nil {
		r.said(fmt.Sprintf(textSuiteNoPost, shownError(err)))
		return session.Outcome{State: event.StateFailed, ExitCode: 1}, &exitError{code: 1}
	}
	if posted != "" {
		r.said(posted)
	}
	return out, nil
}

// cutPostWait is the longest the post of the verdict may take once a stop cut the suite, with one
// try: the push step follows within the time that a signal leaves.
const cutPostWait = 5 * time.Second

// cut ends the run after a stop of qory run cut the after step: the session had succeeded, so the
// run must not close succeeded over a suite that did not finish. qory posts the verdict of the
// red suite with one try, which a failure does not turn into another close, and the run closes
// cancelled with the reason interrupted, with exit status 1, as a cut push step closes it.
func (r *suiteRun) cut(ctx, again context.Context, limit time.Time, got suite.Outcome) (session.Outcome, error) {
	r.said(textSuiteCut)
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	postCtx, cancelWait := context.WithTimeout(postCtx, cutPostWait)
	defer cancelWait()
	posted, err := r.post(postCtx, suitePoint(got, suite.Refused{Local: true}, r.minutes), 1)
	switch {
	case err != nil:
		r.said(fmt.Sprintf(textSuiteNoPost, shownError(err)))
	case posted != "":
		r.said(posted)
	}
	return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}, &exitError{code: 1}
}

// countsSuffix is the counts of a suite as the end of its line shows them: ", 2 of 140 tests
// failed" or ", 140 tests", and "" for no counts.
func countsSuffix(c *suite.Counts) string {
	switch {
	case c == nil:
		return ""
	case c.Failed > 0:
		return fmt.Sprintf(", %d of %d tests failed", c.Failed, c.Tests)
	}
	return fmt.Sprintf(", %d tests", c.Tests)
}

// post posts the verdict of a red suite whose point is point, with at most tries requests, and
// returns the line that describes what it posted: "" when the agent's verdict stands and nothing
// went out.
func (r *suiteRun) post(ctx context.Context, point runapi.Point, tries int) (string, error) {
	httpClient, base, err := r.run.RunAPI()
	if err != nil {
		return "", err
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return "", err
	}
	agent, err := agentVerdict(ctx, client, r.runID)
	if err != nil {
		return "", err
	}
	switch {
	case agent == nil || agent.Verdict == runapi.VerdictPassed:
		body, err := runapi.VerdictBody(runapi.VerdictChangesRequested, []runapi.Point{point})
		if err != nil {
			return "", err
		}
		if err := postVerdict(ctx, client, body, tries); err != nil {
			return "", err
		}
		return fmt.Sprintf(textSuitePosted, runapi.VerdictChangesRequested), nil
	case agent.Verdict == runapi.VerdictEscalate && len(agent.Points) < maxVerdictPoints:
		agent.Points = append(agent.Points, point)
		body, err := encodeVerdict(agent)
		if err != nil {
			return "", err
		}
		if err := postVerdict(ctx, client, body, tries); err != nil {
			return "", err
		}
		return fmt.Sprintf(textSuitePosted, runapi.VerdictEscalate), nil
	case agent.Verdict == runapi.VerdictEscalate:
		return textSuiteNoRoom, nil
	}
	return fmt.Sprintf(textSuiteStands, agent.Verdict), nil
}

// suitePoint is the one point of the verdict for the red suite got. Its text is the first line:
// the hosts that the proxy refused while the suite ran when the run's events have some, and
// otherwise what the suite's end shows. Its fix is the second line, for a suite whose refused
// connections the events cannot show in full.
func suitePoint(got suite.Outcome, refused suite.Refused, minutes int) runapi.Point {
	p := runapi.Point{Text: pointLine(got, refused, minutes), Action: "run_again"}
	if !refused.Local || refused.Denied > 0 {
		p.Fix = textPointDenied
	}
	return p
}

// pointLine is the first line of the point of a red suite: at most [maxPointLine] characters.
func pointLine(got suite.Outcome, refused suite.Refused, minutes int) string {
	switch c := got.Counts; {
	case got.State == suite.Stopped:
		return textPointStopped
	case len(refused.Hosts) > 0:
		return hostsLine(refused.Hosts)
	case got.State == suite.Timeout:
		return fmt.Sprintf(textPointTimeout, minutes)
	case got.Mismatch:
		return textPointMismatch
	case !got.Started:
		return fmt.Sprintf(textPointNoStart, got.ExitCode)
	case c != nil && c.Tests == 0:
		return textPointNoTests
	case c != nil && c.Failed > 0:
		return fmt.Sprintf(textPointFailed, c.Failed, c.Tests)
	}
	return fmt.Sprintf(textPointExit, got.ExitCode)
}

// hostsLine is the line for the hosts that the proxy refused: the first host, "and N more" for the
// others. The line has at most [maxPointLine] characters, and the words around the host leave
// about 20 for the host named twice. So the first mention keeps the host whole when it can, the
// second is cut to what is left, and the second loses "and N more" before the first is cut. A
// cut host ends in "...".
func hostsLine(hosts []string) string {
	host := hosts[0]
	suffix, verb := "", "was"
	if more := len(hosts) - 1; more > 0 {
		suffix, verb = fmt.Sprintf(" and %d more", more), "were"
	}
	build := func(first, second, tail string) string {
		return fmt.Sprintf(textPointHosts, first+suffix, second+tail, verb)
	}
	for _, tail := range []string{suffix, ""} {
		if room := maxPointLine - len(build("", "", tail)); room >= len(host)+minHostShown {
			return build(host, cutHost(host, room-len(host)), tail)
		}
	}
	room := maxPointLine - len(build("", "", ""))
	first := max(room-minHostShown, minHostShown)
	return build(cutHost(host, first), cutHost(host, max(room-first, minHostShown)), "")
}

// minHostShown is the fewest characters of a host that a line shows, "..." included.
const minHostShown = 4

// cutHost is host in at most n characters, with "..." at the end of a host that was cut.
func cutHost(host string, n int) string {
	if len(host) <= n {
		return host
	}
	return host[:n-3] + "..."
}

// onCommit is " on <commit>" for a suite that ran on a commit, and "" for one that never got to it.
func onCommit(commit string) string {
	if commit == "" {
		return ""
	}
	return " on " + shortCommit(commit)
}

// shortCommit is the first 12 characters of a commit id, for a line of qory run.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// verdictDocument is a verdict as the run's version of it has it.
type verdictDocument struct {
	Verdict string          `json:"verdict"`
	Points  []runapi.Point  `json:"points,omitempty"`
	Track   json.RawMessage `json:"track,omitempty"`
}

// encodeVerdict is the body of POST verdict for d.
func encodeVerdict(d *verdictDocument) ([]byte, error) {
	return json.Marshal(d)
}

// agentVerdict is the newest verdict that the run wrote, nil when it wrote none. It is read
// from the artifact index and its file, since the work item's newest verdict can be an earlier
// run's.
func agentVerdict(ctx context.Context, client *runapi.Client, runID string) (*verdictDocument, error) {
	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	defer cancel()
	index, err := client.Artifacts(ctx)
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
	if newest == nil {
		return nil, nil
	}
	if len(newest.Files) == 0 || newest.Files[0].Size > 64<<10 {
		return nil, errNoVerdictFile
	}
	var file bytes.Buffer
	if err := client.Download(ctx, newest.ID, newest.Files[0], &file); err != nil {
		return nil, err
	}
	var d verdictDocument
	if err := json.Unmarshal(file.Bytes(), &d); err != nil {
		return nil, fmt.Errorf("the run's verdict is not a verdict document: %w", err)
	}
	return &d, nil
}

// postVerdict posts body as the run's verdict, up to tries times: a verdict sent twice is stored
// twice with the same content, so a request that got no response is sent again.
func postVerdict(ctx context.Context, client *runapi.Client, body []byte, tries int) error {
	if err := verdictformat.Check(body); err != nil {
		return err
	}
	var last error
	for try := 1; try <= tries; try++ {
		if try > 1 {
			if err := branchWait(ctx, retryAfter(last)); err != nil {
				return err
			}
		}
		one, cancel := context.WithTimeout(ctx, runAPITimeout)
		_, last = client.PushVerdictDocument(one, body)
		cancel()
		switch {
		case last == nil:
			return nil
		case ctx.Err() != nil:
			return last
		case runapi.HasCode(last, runapi.CodeRateLimited), noResponse(last):
		default:
			return last
		}
	}
	return last
}
