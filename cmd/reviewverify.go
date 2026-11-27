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
	"strconv"
	"strings"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/runapi"
)

// A Verify session is the first session of a run at write-no-push whose workflow starts
// reviewers, see [verifiesAsReview]. It writes its checkout and pushes nothing, so it
// keeps the checkout and joins beside what a review join has: a copy of the commit that
// the checkout is at, the diff from the base of the change to that commit, and a new
// reports folder, which are in qory.review_session. It has no suite, so no suite result
// and no finding of the suite.

// planVerify makes the plan of a Verify session, before its setup: the commit, the stage
// and the base of the diff. The base is the base that Qory Apiary stores for the target,
// else the base of the pull-request version that the stage's input has, see
// [pushedBaseOf], else the plan is an error: the checkout's start is the commit itself,
// and a run never takes it as the base. Nothing that the session writes is read here, and
// the plan makes no folder, see [reviewJoinPlan.folders]. Every error is a [reviewStop].
func planVerify(ctx context.Context, s reviewJoinSetup) (*reviewJoinPlan, error) {
	p := &reviewJoinPlan{
		home: s.home, artifacts: filepath.Join(checkout.QoryDir(s.root), artifactsDir), res: s.res,
		workflow: s.workflow, verify: true, root: s.root, access: s.access,
	}
	var err error
	if p.commit, _, p.source, err = reviewTarget(s.branch, s.cwd); err != nil {
		return nil, err
	}
	if p.client, err = reviewClient(s.run); err != nil {
		return nil, err
	}
	stage, err := readStage(ctx, p.client, "the review did not start")
	if err != nil {
		return nil, err
	}
	p.stage, p.stageSlug, p.round = stage.id, stage.slug(), stage.round
	if err := p.readLedger(ctx, s, s.run.System()); err != nil {
		return nil, err
	}
	if p.findings == nil {
		return nil, reviewStop("The run reads no findings of the work item, so the review did not start", "Verify posts its round to the findings of the work item")
	}
	if !p.fromLedger {
		if p.base, err = pushedBaseOf(ctx, p.client, s.snapshot, s.types, s.target, s.targetVersion, p.commit); err != nil {
			return nil, err
		}
		p.pushedBase = true
	}
	p.occasion, p.missingOccasion = p.findings.occasion(), p.findings.occasion()
	return p, nil
}

// checkVerify is the last check of a Verify session's join, on the final bind list and the
// places that the session may write: the reports folder keeps the rules of
// [checkReviewReports], with the checkout as the second writable mount.
func (p *reviewJoinPlan) checkVerify(join session.SessionSpec) error {
	if err := checkReviewReports(p.reports, p.root, p.artifacts, p.home, join.Mounts, os.Geteuid(), p.access); err != nil {
		return err
	}
	return p.checkWritten(join.Written)
}

// markedMember is the member of t whose x-qory-fill mark is mark, "" for none.
func markedMember(t exports.ProcessType, mark string) string {
	names := []string{}
	for name, m := range t.Marks {
		if m == mark {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// commitOfReference is the commit that raw, a member of a version's file, references in
// the collection and on the system of target: a primitive reference whose path is the
// commit's full id in lower case. ok is false for any other member.
func commitOfReference(raw json.RawMessage, target contracts.Target) (commit string, ok bool) {
	var ref struct {
		Collection string `json:"collection,case:strict"`
		Address    *struct {
			System string `json:"system,case:strict"`
			Path   string `json:"path,case:strict"`
		} `json:"address,case:strict"`
	}
	if json.Unmarshal(raw, &ref) != nil || ref.Address == nil || target.Address == nil ||
		ref.Collection != target.Collection || ref.Address.System != target.Address.System || !lowerCommitID(ref.Address.Path) {
		return "", false
	}
	return ref.Address.Path, true
}

// pushedBaseOf is the base commit that the version of an input of the stage records for
// head, the commit that the review reads. It reads the inputs of snapshot, the work item
// as the run read it at its start. An input counts when its version is in the index, is
// neither cancelled nor withdrawn, was written by a run, and has one JSON file whose
// members marked pushed-head and pushed-base reference a commit of target's repository,
// with head as the head. The members are found by the marks of the type in types, not by
// their names. The base of the counted versions is the result. Zero such versions, or
// two with other bases, are an error: the base then is not known. The versions of the
// stage are never read, since the run reads them before its session writes any. The error
// is a [reviewStop].
//
// A session of an earlier run may set a member that qory fills in a direct push, so the
// base counts only when it is a start that the run which wrote the version had, see
// [startsOfRun]; any other base is an error. The check reads the snapshot, the index and
// the files that [startsOfRun] reads, and targetVersion is the target version of the run's
// documents.
//
// The push check of a session's POST of /artifacts stops a session from setting a member
// that qory fills, see [pushcheck.FilledMember], so a run's version has the base that qory
// filled.
func pushedBaseOf(ctx context.Context, client *runapi.Client, snapshot *runapi.Snapshot, types []exports.ProcessType, target *contracts.Target, targetVersion, head string) (string, error) {
	none := func(why string) error {
		return reviewStop("The run has no base commit for the diff, so the review did not start",
			"Qory Apiary stores no base for the target, and "+why)
	}
	if snapshot == nil || target == nil || target.Address == nil {
		return "", none("the run has no target or no work item to read an input from")
	}
	index, err := client.Artifacts(ctx)
	if err != nil {
		return "", withCause(reviewStop("Reading the artifacts of the work item failed, so the review did not start", shownError(err)), err)
	}
	var bases []string
	for _, in := range snapshot.Inputs {
		t := typeOf(types, in.Type)
		headName, baseName := markedMember(t, fillHead), markedMember(t, fillBase)
		if in.Version == nil || headName == "" || baseName == "" {
			continue
		}
		var version *runapi.Version
		for _, a := range index.Artifacts {
			for i := range a.Versions {
				if a.Type == in.Type && a.Versions[i].ID == in.Version.ID {
					version = &a.Versions[i]
				}
			}
		}
		if version == nil || version.Cancelled || version.Withdrawn || len(version.Files) != 1 || version.Files[0].Size > maxStartFile {
			continue
		}
		// A person writes any member by hand, and qory fills them only at the push of a run.
		if version.Writer.Kind != "run" || version.Writer.RunID == "" {
			continue
		}
		var content bytes.Buffer
		if err := client.Download(ctx, version.ID, version.Files[0], &content); err != nil {
			return "", withCause(reviewStop("Fetching the pull request version failed, so the review did not start", shownError(err)), err)
		}
		var members map[string]json.RawMessage
		if json.Unmarshal(content.Bytes(), &members) != nil {
			continue
		}
		if h, ok := commitOfReference(members[headName], *target); !ok || h != head {
			continue
		}
		base, ok := commitOfReference(members[baseName], *target)
		if !ok {
			continue
		}
		fromTarget, earlier, err := startsOfRun(ctx, client, snapshot, index, target, types, targetVersion, version)
		if err != nil {
			return "", withCause(reviewStop("Reading a version of an earlier run failed, so the review did not start", shownError(err)), err)
		}
		if base != fromTarget && base != earlier {
			return "", reviewStop("The base of the pull request is not the start of its run, so the review did not start",
				fmt.Sprintf("Version %d of %s, written by run %s, has the base %s, and the run started at %s",
					version.Number, shownText(in.Type), shownText(version.Writer.RunID), base, startsText(fromTarget, earlier)))
		}
		if !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	switch len(bases) {
	case 1:
		return bases[0], nil
	case 0:
		return "", none("no input of the stage has a version with a base and the head " + head)
	}
	return "", none("the inputs of the stage have " + strconv.Itoa(len(bases)) + " versions with the head " + head + " and different bases")
}

// startsOfRun are the two commits of target's repository at which the run that wrote
// written may have started: fromTarget, the target version in lower case, and earlier, the
// start that a write run would have taken from the versions before written, "" for none.
// earlier is worked out as a write run works out its start, with [startVersion] and
// [startCommit], on the versions that a run before the writer wrote and a person did not
// cancel: the commit that one of those versions confirms. A pushed commit that no such
// version confirms is no start. The snapshot's pushed commits are ordered by run number
// and then by push order, so the runs before the writer are those with an entry before the
// first entry of the writer; a cancelled entry does not count. With no entry of the writer
// its place in the order is unknown, and earlier is "". The stage that startVersion reads
// is the stage of written. index is the work item's artifacts, as the run read them, and
// types the artifact types of the run's process: only a version of a type that has a
// delivery and a required member that qory fills with the pushed commits, see
// [exports.ProcessType] and [requiresCommits], confirms an earlier start. An error is that
// of a read, and ends with [codeUsed].
//
// shortcut: cancelled is read when the work item is read, not when the writer started, so
// a cancel after the start moves earlier to an older commit. If the target version moves
// between Build and Verify, Verify stops; replace it when the run API lists each run's
// start.
func startsOfRun(ctx context.Context, client *runapi.Client, snapshot *runapi.Snapshot, index *runapi.Index, target *contracts.Target, types []exports.ProcessType, targetVersion string, written *runapi.Version) (fromTarget, earlier string, err error) {
	fromTarget = strings.ToLower(targetVersion)
	writer := written.Writer.RunID
	first := slices.IndexFunc(snapshot.PushedCommits, func(e runapi.PushedEntry) bool { return e.RunID == writer })
	if first < 0 {
		return fromTarget, "", nil
	}
	pushed := slices.DeleteFunc(slices.Clone(snapshot.PushedCommits[:first]), func(e runapi.PushedEntry) bool { return e.Cancelled })
	before := map[string]bool{}
	for _, e := range pushed {
		before[e.RunID] = true
	}
	// The versions that existed before the writer started: those of the runs before it, of a
	// type that the process delivers and that has a required member that qory fills with the
	// pushed commits. A session may store a version of any other type under the name of the
	// delivered artifact with --name, or one that has no such member, and it can leave out a
	// member that is optional: such a version confirms no start.
	earlierIndex := &runapi.Index{}
	for _, a := range index.Artifacts {
		if t := typeOf(types, a.Type); t.Send == "" || !requiresCommits(t) {
			continue
		}
		a.Versions = slices.DeleteFunc(slices.Clone(a.Versions), func(v runapi.Version) bool {
			return v.Writer.Kind != "run" || !before[v.Writer.RunID]
		})
		earlierIndex.Artifacts = append(earlierIndex.Artifacts, a)
	}
	atStart := *snapshot
	atStart.Stage.ID = written.StageID
	version, artifact, _ := startVersion(&atStart, earlierIndex, target)
	if version == nil {
		return fromTarget, "", nil
	}
	earlier, _, err = startCommit(ctx, client, version, artifact, sameRunVersions(earlierIndex, target, version), pushed, target)
	// A version that confirms no commit means the run has no start before the writer; any
	// other error is a failed read.
	if errors.As(err, new(noStart)) {
		return fromTarget, "", nil
	}
	return fromTarget, earlier, err
}

// startsText is the starts of [startsOfRun] as a phrase of a stop.
func startsText(fromTarget, earlier string) string {
	text := "no commit"
	if fromTarget != "" {
		text = "the target version " + shownText(fromTarget)
	}
	if earlier == "" {
		return text
	}
	if fromTarget == "" {
		return "the commit " + shownText(earlier) + " of an earlier run"
	}
	return text + " or the commit " + shownText(earlier) + " of an earlier run"
}

// postVerify posts the findings of a Verify session's round once, and sends no verdict and
// no base: the session records the verdict itself. The check of the session's verdict
// calls it with atEnd false, so the round is stored before Qory Apiary reads the verdict,
// and the end of the session calls it with atEnd true. A round with reports posts under
// the one occasion of the plan, so the second call returns the result of the first. Each
// check of a verdict, and the end of a session whose round is not posted yet, read the
// checkout's HEAD first, see [reviewJoinPlan.checkHead]: a HEAD that is not the commit of
// the review posts nothing and stores no verdict.
//
// With no report in the reports folder, the round has the finding of the missing report of
// each expected role, as the round of a review join has, when the session records the
// verdict passed: passed is whether the POST at the check records it, and at the end the
// verdict that the session recorded is read. Qory Apiary then returns 409 for the verdict
// while that finding is open. Such a round has a separate occasion, see
// [reviewJoinPlan.missingOccasion], so the round of the reports that the session writes
// afterwards is still posted. That round does not read the reports folder, so a report
// that appears after the check is posted once, in the round of the one occasion. A round
// with no report posts nothing when the session records any other verdict, since a session
// may record changes-requested or escalate with no reviewer on purpose. At the end, a
// session that recorded no verdict raises the same finding.
func (f *findingsRun) postVerify(ctx context.Context, p *reviewJoinPlan, atEnd, passed bool) error {
	const what = "the review did not end"
	if !atEnd {
		if err := p.checkHead(ctx, what); err != nil {
			return err
		}
	}
	if done, err := f.poster.posted(ctx, p.occasion); done {
		return err
	}
	occasion, missing := p.occasion, false
	switch {
	case !atEnd && !hasReports(p.reports):
		if !passed {
			return nil
		}
		occasion, missing = p.missingOccasion, true
	case atEnd:
		version, err := newestVerdictVersion(ctx, p.client, f.runID)
		if err != nil {
			return withCause(reviewStop("Reading the run's verdict failed, so the review did not end", shownError(err)), err)
		}
		if version != nil && version.ID != p.verdictBefore && !hasReports(p.reports) {
			if passed, err = versionRecordsPassed(ctx, p.client, version); err != nil {
				return withCause(reviewStop("Reading the run's verdict failed, so the review did not end", shownError(err)), err)
			}
			if !passed {
				return nil
			}
			occasion, missing = p.missingOccasion, true
		}
		if err := p.checkHead(ctx, what); err != nil {
			return err
		}
	}
	_, _, err := f.poster.round(ctx, occasion, what, func(found contracts.Findings) ([]contracts.FindingBase, []contracts.FindingsPostEvent, error) {
		events, err := f.roundEvents(p, found, missing)
		return nil, events, err
	})
	return err
}

// versionRecordsPassed reports whether the verdict version of the run records passed: it
// downloads the files of the version and reads them as [pushcheck.FilePassed] does, so a
// version with no file or a file that cannot be read counts as passed.
func versionRecordsPassed(ctx context.Context, client *runapi.Client, version *runapi.Version) (bool, error) {
	for _, file := range version.Files {
		if file.Size > maxStartFile {
			return true, nil
		}
		var content bytes.Buffer
		if err := client.Download(ctx, version.ID, file, &content); err != nil {
			return false, err
		}
		if pushcheck.FilePassed(content.Bytes()) {
			return true, nil
		}
	}
	return len(version.Files) == 0, nil
}
