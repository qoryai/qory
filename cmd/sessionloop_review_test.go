package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// reviewFiles are the files of the module hello that a review session needs: the group
// role reviewers with the member code-reviewer, whose agent file the module ships, and the
// skill workflow-review-changes. The manifest keeps the scope checks of the module.
var reviewFiles = map[string]string{
	"modules/hello/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: hello\n" + helloScope +
		"roles: {agents: {reviewers: many}}\nprovides: {agents: {reviewers: [code-reviewer]}}\n",
	"modules/hello/agents/code-reviewer.md":                 "---\nname: code-reviewer\ndescription: Review code.\n---\n\nReview.\n",
	"modules/hello/skills/workflow-review-changes/SKILL.md": "---\nname: workflow-review-changes\ndescription: Review.\n---\n\nReview.\n",
	"modules/hello/skills/workflow-plain-read/SKILL.md":     "---\nname: workflow-plain-read\ndescription: Read.\n---\n\nRead.\n",
}

// reviewWorkflows are the workflows beside hello, write: review-changes, read with the
// review group reviewers, and plain-read, read with no review group.
var reviewWorkflows = map[string]any{
	"review-changes": map[string]any{"access": "read", "review_group": "reviewers"},
	"plain-read":     map[string]any{"access": "read"},
}

// answerFor is the next-session answer that starts the workflow on claude, as
// [laterAnswer] starts hello.
func answerFor(n int, workflow string) string {
	return strings.Replace(laterAnswer(n, "claude", "example-provider-2", 30), `"hello"`, `"`+workflow+`"`, 1)
}

// reviewJoined is a join of a run of [reviewRun]: the session's spec, how many times
// RemoveSetup had been called when the session joined, the qory member of the session's
// inputs.json, and, for a session that has qory.review_session, what its files and the
// reports folder were during the join.
type reviewJoined struct {
	spec    session.SessionSpec
	removed int
	qory    map[string]any
	seen    *reviewSeen
	// reviewDir is whether the inputs folder that the session mounts had the folder
	// review-session, copyLeft whether the copy folder of the review join before and
	// reportsLeft whether its reports folder still existed, all when the session joined.
	reviewDir, copyLeft, reportsLeft bool
}

// reviewSeen is qory.review_session of a join and what it points to, as the session sees
// it: the text of each file, and the reports folder's entries, mode and owner.
type reviewSeen struct {
	commit, commitFolder, reports string
	diff, suite, previous, build  string
	hasDiff, hasSuite, hasBuild   bool
	hasPrevious                   bool
	modes                         map[string]os.FileMode
	entries                       []string
	reportsMode                   os.FileMode
	reportsOwner                  int
	copyFiles                     []string
	copyHasGit                    bool
	// copyPaths is every file of the copy, relative to it.
	copyPaths []string
	// workFiles is the entries of the session's working folder, workReadOnly whether it is a
	// read-only mount, and workWritable whether the folder let this test make a file in it.
	workFiles    []string
	workReadOnly bool
}

// inputsOf is the qory member of the inputs.json that the session of spec reads, from the
// read-only inputs folder it mounts; nil when it has none.
func inputsOf(spec session.SessionSpec) map[string]any {
	for _, m := range spec.Mounts {
		data, err := os.ReadFile(filepath.Join(m.Path, "inputs.json"))
		if err != nil || !m.ReadOnly {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			return nil
		}
		qory, _ := doc["qory"].(map[string]any)
		return qory
	}
	return nil
}

// seeReview reads what the review session of qory.review_session rs sees.
func seeReview(rs map[string]any, spec session.SessionSpec) *reviewSeen {
	str := func(k string) string { v, _ := rs[k].(string); return v }
	seen := &reviewSeen{commit: str("commit"), commitFolder: str("commit_folder"), reports: str("reports"), modes: map[string]os.FileMode{}}
	read := func(path string) (string, bool) {
		if path == "" {
			return "", false
		}
		data, err := os.ReadFile(path)
		if info, serr := os.Stat(path); serr == nil {
			seen.modes[path] = info.Mode().Perm()
		}
		return string(data), err == nil
	}
	seen.diff, seen.hasDiff = read(str("diff"))
	seen.suite, seen.hasSuite = read(str("suite_result"))
	seen.build, seen.hasBuild = read(str("build_report"))
	_, seen.hasPrevious = rs["previous_findings"]
	if entries, err := os.ReadDir(seen.reports); err == nil {
		for _, e := range entries {
			seen.entries = append(seen.entries, e.Name())
		}
	}
	if info, err := os.Stat(seen.reports); err == nil {
		seen.reportsMode = info.Mode().Perm()
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			seen.reportsOwner = int(st.Uid)
		}
	}
	if entries, err := os.ReadDir(seen.commitFolder); err == nil {
		for _, e := range entries {
			seen.copyFiles = append(seen.copyFiles, e.Name())
			if e.Name() == ".git" {
				seen.copyHasGit = true
			}
		}
	}
	_ = filepath.WalkDir(seen.commitFolder, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(seen.commitFolder, path)
			seen.copyPaths = append(seen.copyPaths, rel)
		}
		return nil
	})
	if entries, err := os.ReadDir(spec.Dir); err == nil {
		for _, e := range entries {
			seen.workFiles = append(seen.workFiles, e.Name())
		}
	}
	for _, m := range spec.Mounts {
		if m.Path == spec.Dir {
			seen.workReadOnly = m.ReadOnly
		}
	}
	return seen
}

// reviewCfg changes the run of [reviewRunWith].
type reviewCfg struct {
	// noSuite makes session 1 run no full suite, noRound leaves the round out of the work
	// item, and build is the text of a build-report version of the stage, "" for none.
	noSuite, noRound bool
	build            string
	// extra, when set, is called with the checkout and the number of each session that
	// writes, before its commit.
	extra func(dir string, n int)
	// left, when set, is called with the checkout and the number of each session that
	// writes, after its commit, to leave changes uncommitted.
	left func(dir string, n int)
	// remove, when set, is the run's RemoveSetup, called with the number of the call.
	remove func(call int) error
	// next is called with the number of each session that joins, after its agent ran.
	next func(p *pushRepo, n int)
	// writes is the sessions that write; the others review.
	writes []int
	// wallUser, when set, is the wall's user as it writes it for the uid and gid of the
	// test; the default is "<uid>:<gid>".
	wallUser func(uid, gid int) string
	// stack, when set, is the workflows table of [loopRepo], whose stack the harness
	// document extends in place of defining the workflows: a document cannot set judges.
	stack string
	// onSetCheck, when set, gets the check of the run API that the run sets.
	onSetCheck func(check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error
	// between, when set, is called with the number of the session that ended, when the run
	// asks for the next session: no session runs then.
	between func(n int)
	// files are files of the repository beside [reviewFiles].
	files map[string]string
	// failed, when not 0, is the number of the session whose agent exits 7.
	failed int
	// joined, when set, gets the spec of each session as it joins, before its agent runs.
	joined func(spec session.SessionSpec)
}

// walledAsThisUser makes the wall of the fake docker run as the uid of this process, with
// the user that user returns for the uid and gid of the process, "<uid>:<gid>" for nil.
// The uid that the run takes as qory's is that uid, so a walled run has one user, see
// [oneUser]. As root, the uid is 1000, which the run takes as qory's.
func walledAsThisUser(t *testing.T, user func(uid, gid int) string) {
	t.Helper()
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid, gid = 1000, 1000
	}
	t.Cleanup(cmd.SetQoryUID(uid))
	wallUser := strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
	if user != nil {
		wallUser = user(uid, gid)
	}
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \""+wallUser+"\"\n  image: example.com/agent:1\n")
}

// reviewRun is [reviewRunWith] with session 1 running the full suite, round 1 of 2, and no
// build report: a run that a review join passes.
func reviewRun(t *testing.T, remove func(call int) error, answers ...string) (r pushed, joins []reviewJoined, p *pushRepo) {
	t.Helper()
	return reviewRunWith(t, reviewCfg{remove: remove}, answers...)
}

// reviewRunWith runs a write run on the module hello with [reviewFiles]: session 1 is
// hello, which commits a file and runs the full suite, and the answers start the sessions
// after it. The run's counts of RemoveSetup calls come with each join.
func reviewRunWith(t *testing.T, cfg reviewCfg, answers ...string) (r pushed, joins []reviewJoined, p *pushRepo) {
	t.Helper()
	if cfg.stack != "" {
		p = loopRepo(t, cfg.stack, answers...)
	} else {
		files := maps.Clone(reviewFiles)
		maps.Copy(files, cfg.files)
		p, _ = sharedPushRepoWith(t, files, reviewWorkflows, answers...)
	}
	p.onSetCheck = cfg.onSetCheck
	if asked := p.nextSession; cfg.between != nil {
		ended := 0
		p.nextSession = func(ctx context.Context, body []byte) ([]byte, error) {
			ended++
			cfg.between(ended)
			return asked(ctx, body)
		}
	}
	change := p.change
	p.change = func(d map[string]any) {
		change(d)
		d["type_settings"] = map[string]any{"full_suite": !cfg.noSuite}
	}
	suiteServed(t, p, nil)
	walledAsThisUser(t, cfg.wallUser)
	if !cfg.noRound {
		p.api.setRound(1, 2)
	}
	if cfg.build != "" {
		p.api.addVersion("build-report", "build-report.md", []byte(cfg.build))
	}
	writes := cfg.writes
	if writes == nil {
		writes = []int{1, 3}
	}
	var mu sync.Mutex
	calls := 0
	p.removeSetup = func(context.Context) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if cfg.remove != nil {
			return cfg.remove(n)
		}
		return nil
	}
	var current session.SessionSpec
	p.mountsOf = func(spec session.SessionSpec) {
		mu.Lock()
		defer mu.Unlock()
		current = spec
		j := reviewJoined{spec: spec, removed: calls, qory: inputsOf(spec)}
		if rs, ok := j.qory["review_session"].(map[string]any); ok {
			j.seen = seeReview(rs, spec)
		}
		for _, m := range spec.Mounts {
			if exists(filepath.Join(m.Path, "review-session")) {
				j.reviewDir = true
			}
		}
		// The review join before is the last one with qory.review_session.
		for i := len(joins) - 1; i >= 0; i-- {
			if before := joins[i].seen; before != nil {
				j.copyLeft = exists(filepath.Dir(before.commitFolder))
				j.reportsLeft = exists(before.reports)
				break
			}
		}
		joins = append(joins, j)
		if cfg.joined != nil {
			cfg.joined(spec)
		}
	}
	p.noOutput = true
	r = p.run(t, func(d string) {
		mu.Lock()
		n := len(joins)
		mu.Unlock()
		if slices.Contains(writes, n) {
			if cfg.extra != nil {
				cfg.extra(d, n)
			}
			writeFile(t, filepath.Join(d, "s"+string(rune('0'+n))+".txt"), "s\n")
			commitAll(t, d, "Add file "+string(rune('0'+n)))
			if cfg.left != nil {
				cfg.left(d, n)
			}
			writeFile(t, filepath.Join(d, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a file"}`)
		}
		if cfg.next != nil {
			cfg.next(p, n)
		}
	}, failedIn(cfg.failed, suiteResultsIn(t, &current, func() string { return p.h.runDir })))
	return r, joins, p
}

// failedIn is result with the session of the number n failed, exit 7; result for n 0.
func failedIn(n int, result func() (*session.Result, error)) func() (*session.Result, error) {
	if n == 0 {
		return result
	}
	calls := 0
	return func() (*session.Result, error) {
		res, err := result()
		calls++
		if res != nil && calls == n {
			res.State, res.ExitCode = event.StateFailed, 7
		}
		return res, err
	}
}

// claudeSettings is the Claude Code settings that spec passes inline with --settings: the
// JSON after the last --settings, or nil.
func claudeSettings(t *testing.T, spec session.SessionSpec) map[string]any {
	t.Helper()
	i := slices.Index(spec.Args, "--settings")
	if i < 0 || i+1 >= len(spec.Args) {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(spec.Args[i+1]), &object); err != nil {
		t.Fatalf("--settings is not JSON: %v\n%s", err, spec.Args[i+1])
	}
	return object
}

// allowOf is permissions.allow of settings.
func allowOf(settings map[string]any) []string {
	permissions, _ := settings["permissions"].(map[string]any)
	list, _ := permissions["allow"].([]any)
	var rules []string
	for _, rule := range list {
		rules = append(rules, rule.(string))
	}
	return rules
}

// denyOf is permissions.deny of settings.
func denyOf(settings map[string]any) []string {
	permissions, _ := settings["permissions"].(map[string]any)
	list, _ := permissions["deny"].([]any)
	var rules []string
	for _, rule := range list {
		rules = append(rules, rule.(string))
	}
	return rules
}

// TestAReviewSessionJoinsReadOnlyBetweenTwoWriteSessions is a write run of three
// sessions: hello (write), review-changes (read, with a review group), then hello again.
// Session 2 joins with the workspace read-only, the tools list and the Claude Code
// settings of a review session, after the setup container was removed, and has no push
// step and no stored version. Session 3 joins writable as session 1 did, with no
// tools list, and pushes with its version stored. The pushes of sessions 1 and 3 are the
// run's two versions, and the run closes succeeded.
func TestAReviewSessionJoinsReadOnlyBetweenTwoWriteSessions(t *testing.T) {
	r, joins, p := reviewRun(t, nil, answerFor(2, "review-changes"), answerFor(3, "hello"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	for i, j := range joins {
		review := i == 1
		if j.spec.WorkspaceReadOnly != review {
			t.Errorf("session %d: WorkspaceReadOnly %v, want %v", i+1, j.spec.WorkspaceReadOnly, review)
		}
		if has := slices.Contains(j.spec.Args, "--tools"); has != review {
			t.Errorf("session %d: --tools in the arguments %v, want %v", i+1, has, review)
		}
		for _, m := range j.spec.Mounts {
			if m.Path == j.spec.Dir && !m.ReadOnly != !review {
				t.Errorf("session %d: the checkout's mount has ReadOnly %v, want %v", i+1, m.ReadOnly, review)
			}
		}
		allow := allowOf(claudeSettings(t, j.spec))
		checks := 0
		for _, rule := range allow {
			if strings.HasSuffix(rule, "/claude/plugin/skills/running-a-workflow/checks.cjs *)") {
				checks++
			}
		}
		if _, outbox := j.qory["outbox"]; outbox == review {
			t.Errorf("session %d has qory.outbox %v, want it for the sessions that write only", i+1, outbox)
		}
		verdict := slices.Contains(allow, "Bash("+wall.HelperPath+" run verdict *)")
		if review && (checks != 1 || !verdict) || !review && (checks != 0 || verdict) {
			t.Errorf("session %d: %d checks rules and verdict rule %v in %q, want a review session only", i+1, checks, verdict, allow)
		}
	}
	// The review join's settings allow a write of the reports folder and still deny the copy,
	// and no other session's settings allow a write there.
	seen := reviewSessionOf(t, joins, 1)
	settings := claudeSettings(t, joins[1].spec)
	if !slices.Contains(allowOf(settings), "Edit(/"+seen.reports+"/**)") || !slices.Contains(denyOf(settings), "Edit(/"+seen.commitFolder+"/**)") {
		t.Errorf("the review session's settings have allow %q and deny %q, want an Edit allow of %s and an Edit deny of %s", allowOf(settings), denyOf(settings), seen.reports, seen.commitFolder)
	}
	// Under dontAsk the session reads the copy, the review-session inputs folder and the
	// reports folder: each has a Read allow and is in additionalDirectories, and the inputs
	// folder lies in a mount that has the same path in the agent's container.
	diff, _ := joins[1].qory["review_session"].(map[string]any)["diff"].(string)
	inputs := filepath.Dir(diff)
	if filepath.Base(inputs) != "review-session" {
		t.Fatalf("the diff %q is not in the review-session folder", diff)
	}
	permissions, _ := settings["permissions"].(map[string]any)
	dirs, _ := permissions["additionalDirectories"].([]any)
	for _, dir := range []string{seen.commitFolder, inputs, seen.reports} {
		if !slices.Contains(allowOf(settings), "Read(/"+dir+"/**)") || !slices.Contains(dirs, any(dir)) {
			t.Errorf("the review session's settings have allow %q and additionalDirectories %q, want a Read allow of %s and the folder listed", allowOf(settings), dirs, dir)
		}
	}
	inputsMounted := false
	for _, m := range joins[1].spec.Mounts {
		if m.Path == filepath.Dir(inputs) && m.ReadOnly && m.Target == "" {
			inputsMounted = true
		}
	}
	if !inputsMounted {
		t.Errorf("no read-only mount of %s, which has the review-session folder: %v", filepath.Dir(inputs), joins[1].spec.Mounts)
	}
	for _, i := range []int{0, 2} {
		for _, rule := range allowOf(claudeSettings(t, joins[i].spec)) {
			if strings.Contains(rule, "review-reports") {
				t.Errorf("session %d allows %s", i+1, rule)
			}
		}
	}
	// The push step of session 1 removed the setup container, and the review join removed
	// it once more before its own join; session 2 has no push step and session 3's push
	// step is the third call.
	if joins[0].removed != 0 || joins[1].removed != 2 || joins[2].removed != 2 {
		t.Errorf("RemoveSetup had been called %d, %d and %d times at the joins, want 0, 2 and 2", joins[0].removed, joins[1].removed, joins[2].removed)
	}
	if got := r.pushSteps(); len(got) != 2 || got[0] != event.StateSucceeded || got[1] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want two succeeded", got)
	}
	if posts, _, stored := p.api.counts(); posts != 2 || stored != 2 {
		t.Errorf("%d POST and %d versions, want 2 and 2 (sessions 1 and 3)", posts, stored)
	}
	if got := proxygittest.Git(t, p.bare, "log", "--format=%s", p.first+".."+p.branchHead(t)); got != "Add file 3\nAdd file 1" {
		t.Errorf("the branch has the commits:\n%s\nwant those of sessions 1 and 3", got)
	}
}

// TestAReviewJoinComposesWithTheConfigurationOfSession1 is a write run whose checkout's
// qory.yaml maps the tier balanced to example-first at the effort low, and whose session 1
// commits a qory.yaml that maps it to example-later at the effort high. The reviewer's
// model is ${qory:balanced}. The review join of session 2 composes with the configuration
// of session 1, so its reviewer and its launch have example-first at low, never the
// values that session 1 wrote.
func TestAReviewJoinComposesWithTheConfigurationOfSession1(t *testing.T) {
	tiers := func(model, effort string) string {
		return "apiVersion: qory.dev/v1alpha1\nharness:\n  tiers:\n    balanced: {model: " + model + ", effort: " + effort + "}\n"
	}
	var agents, args []string
	r, joins, _ := reviewRunWith(t, reviewCfg{
		files: map[string]string{
			"qory.yaml": tiers("example-first", "low"),
			"modules/hello/agents/code-reviewer.md": "---\nname: code-reviewer\ndescription: Review code.\n" +
				"model: ${qory:balanced}\n---\n\nReview.\n",
		},
		extra: func(dir string, n int) {
			if n == 1 {
				writeFile(t, filepath.Join(dir, "qory.yaml"), tiers("example-later", "high"))
			}
		},
		joined: func(spec session.SessionSpec) {
			data, _ := os.ReadFile(filepath.Join(spec.HarnessHome, "claude", "plugin", "agents", "code-reviewer.md"))
			agents = append(agents, string(data))
			args = append(args, strings.Join(spec.Args, " "))
		},
	}, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 2 || len(agents) != 2 {
		t.Fatalf("%d joins, want 2\n%s", len(joins), r.out)
	}
	for i, agent := range agents {
		if !strings.Contains(agent, "model: example-first\n") || !strings.Contains(agent, "effort: low\n") {
			t.Errorf("session %d's reviewer is %q, want example-first at the effort low", i+1, agent)
		}
		if strings.Contains(args[i], "example-later") {
			t.Errorf("session %d's launch has the model that session 1 wrote: %s", i+1, args[i])
		}
	}
}

// TestAReviewJoinWhoseSetupRemovalFailsStopsTheRun is the run of
// [TestAReviewSessionJoinsReadOnlyBetweenTwoWriteSessions] whose second call of
// RemoveSetup, the review join's, fails: session 2 does not join, the run closes failed
// with the reason read_needs_setup_removed, and session 1's push stays.
func TestAReviewJoinWhoseSetupRemovalFailsStopsTheRun(t *testing.T) {
	r, joins, p := reviewRun(t, func(call int) error {
		if call == 2 {
			return errors.New("the engine refused")
		}
		return nil
	}, answerFor(2, "review-changes"), answerFor(3, "hello"))
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
	}
	r.wantsClose(t, event.StateFailed, 1)
	r.wantsReason(t, event.ReasonReadNeedsSetupRemoved)
	if r.err == nil || !strings.Contains(r.err.Error(), "The setup container of this read run was not removed, so the session did not start") {
		t.Errorf("the error is %v, want the line of the removal\n%s", r.err, r.out)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want session 1's alone", posts, stored)
	}
}

// TestAReadSessionWithNoReviewGroupStopsAWriteRun is a write run whose next session
// is plain-read, read with no review_group: the run stops before session 2 with the line
// about the access, and session 1's push stays.
func TestAReadSessionWithNoReviewGroupStopsAWriteRun(t *testing.T) {
	r, joins, p := reviewRun(t, nil, answerFor(2, "plain-read"))
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
	}
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "The workflow plain-read has access read and session 1 has write, so the session did not start\n  Only a read workflow with a review_group may have less access than session 1; the code is used\n")
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want session 1's alone", posts, stored)
	}
}

// reviewSession is qory.review_session of join i, or fails the test.
func reviewSessionOf(t *testing.T, joins []reviewJoined, i int) *reviewSeen {
	t.Helper()
	if i >= len(joins) {
		t.Fatalf("%d joins, want at least %d", len(joins), i+1)
	}
	if joins[i].seen == nil {
		t.Fatalf("session %d has no qory.review_session in %v", i+1, joins[i].qory)
	}
	return joins[i].seen
}

// TestAReviewJoinReadsAFreshCopyOfThePushedCommit is a write run of three sessions: hello
// writes and runs the full suite, review-changes reviews, and hello writes again. Session
// 2 has qory.review_session with the pushed head of session 1, the read-only copy of that
// commit as commit_folder, its diff, the suite result of that commit, a new empty reports
// folder that only it writes, and neither previous_findings nor build_report. Its working
// folder is an empty, read-only folder; the copy, with no .git, is a second read-only
// mount. Sessions 1 and 3 have no review_session. Session 3 finds neither the files of the
// review in its inputs folder nor the copy, and the reports folder stays.
func TestAReviewJoinReadsAFreshCopyOfThePushedCommit(t *testing.T) {
	var pushed string
	r, joins, _ := reviewRunWith(t, reviewCfg{next: func(p *pushRepo, n int) {
		if n == 2 {
			pushed = p.branchHead(t)
		}
	}}, answerFor(2, "review-changes"), answerFor(3, "hello"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	for _, i := range []int{0, 2} {
		if _, has := joins[i].qory["review_session"]; has {
			t.Errorf("session %d has qory.review_session, want only the review session", i+1)
		}
	}
	seen := reviewSessionOf(t, joins, 1)
	spec := joins[1].spec
	if seen.commit != pushed || pushed == "" {
		t.Errorf("review_session.commit is %q, want the pushed head %q", seen.commit, pushed)
	}
	if !seen.hasDiff || !strings.Contains(seen.diff, "+++ b/s1.txt") || strings.Contains(seen.diff, "s3.txt") {
		t.Errorf("review_session.diff is %q, want the change of session 1 from the run's start", seen.diff)
	}
	if !seen.hasSuite || !strings.Contains(seen.suite, `"failed":1`) || !strings.Contains(seen.suite, "qory-suite-head "+pushed) {
		t.Errorf("review_session.suite_result is %q, want the suite result of session 1 on %s", seen.suite, pushed)
	}
	if seen.hasPrevious || seen.hasBuild {
		t.Errorf("round 1 with no build report has previous_findings %v and build_report %v, want neither", seen.hasPrevious, seen.hasBuild)
	}
	for path, mode := range seen.modes {
		if mode != 0o444 {
			t.Errorf("%s has mode %v, want 0444", path, mode)
		}
	}
	if len(seen.entries) != 0 || seen.reportsMode != 0o700 || seen.reportsOwner != os.Getuid() {
		t.Errorf("the reports folder has %v, mode %v, owner %d, want it empty, 0700 and owned by the wall's user", seen.entries, seen.reportsMode, seen.reportsOwner)
	}
	under := filepath.Dir(seen.reports)
	commitFolder := filepath.Join(under, "review-copy-2", "commit")
	if seen.commitFolder != commitFolder {
		t.Errorf("review_session.commit_folder is %s, want the copy's commit folder %s", seen.commitFolder, commitFolder)
	}
	if work := filepath.Join(under, "review-work-2"); spec.Dir != work || !spec.WorkspaceReadOnly || !seen.workReadOnly || len(seen.workFiles) != 0 {
		t.Errorf("the working folder is %s with %v, read-only mount %v and workspace %v, want the empty read-only %s", spec.Dir, seen.workFiles, seen.workReadOnly, spec.WorkspaceReadOnly, work)
	}
	if !slices.Contains(seen.copyFiles, "s1.txt") || seen.copyHasGit {
		t.Errorf("the copy has %v, want the files of the commit and no .git", seen.copyFiles)
	}
	var copyMounts, workMounts, reportsMounts int
	for _, m := range spec.Mounts {
		switch {
		case m.Path == commitFolder && m.ReadOnly && m.Target == "":
			copyMounts++
		case m.Path == spec.Dir && m.ReadOnly && m.Target == "":
			workMounts++
		case m.Path == seen.reports && !m.ReadOnly && m.Target == "":
			reportsMounts++
		case m.Path == joins[0].spec.Dir:
			t.Errorf("the review join mounts the checkout %s", m.Path)
		case !m.ReadOnly:
			t.Errorf("the review join has the writable mount %s", m.Path)
		}
	}
	if copyMounts != 1 || workMounts != 1 || reportsMounts != 1 {
		t.Errorf("the join has %d read-only mounts of the copy, %d of the working folder and %d writable mounts of the reports folder, want 1, 1 and 1: %v", copyMounts, workMounts, reportsMounts, spec.Mounts)
	}
	// The run's session after the review joins at the checkout again, writable, with no
	// mount of the copy or the reports folder.
	if joins[2].spec.Dir != joins[0].spec.Dir || joins[2].spec.WorkspaceReadOnly {
		t.Errorf("session 3 has the workspace %s, read-only %v, want the checkout writable", joins[2].spec.Dir, joins[2].spec.WorkspaceReadOnly)
	}
	for _, m := range joins[2].spec.Mounts {
		if m.Path == seen.reports || m.Path == commitFolder {
			t.Errorf("session 3 mounts %s", m.Path)
		}
	}
	// Session 3 is read inside its mounts hook, before the run ends and removes the run's
	// folder: the files of the review and the copy are gone, and the reports stay.
	if !joins[1].reviewDir || joins[2].reviewDir || joins[2].copyLeft || !joins[2].reportsLeft {
		t.Errorf("the review-session folder was there for sessions 2 and 3: %v and %v; the copy of session 2 was still there at session 3: %v; its reports folder was: %v; want true, false, false and true",
			joins[1].reviewDir, joins[2].reviewDir, joins[2].copyLeft, joins[2].reportsLeft)
	}
}

// TestAReviewJoinWorksInAnEmptyFolderWhateverTheCommitHolds is a run whose session 1
// commits files that a runtime reads as configuration. Outside the project paths of Claude
// Code, such as the configuration of other runtimes: the review session's working folder
// stays empty, so the runtime finds none of them there, and the copy has them. Under the
// project paths: the review stops before the join, and the run closes failed.
func TestAReviewJoinWorksInAnEmptyFolderWhateverTheCommitHolds(t *testing.T) {
	commits := func(files []string) reviewCfg {
		return reviewCfg{extra: func(dir string, n int) {
			if n == 1 {
				for _, f := range files {
					writeFile(t, filepath.Join(dir, f), "x\n")
				}
			}
		}}
	}
	t.Run("files outside the project paths", func(t *testing.T) {
		files := []string{".codex/config.toml", ".claude-notes/settings.json", ".github/copilot-instructions.md", "GEMINI.md"}
		r, joins, _ := reviewRunWith(t, commits(files), answerFor(2, "review-changes"), answerFor(3, "hello"))
		r.wantsClose(t, event.StateSucceeded, 0)
		seen := reviewSessionOf(t, joins, 1)
		if len(seen.workFiles) != 0 || joins[1].spec.Dir == joins[0].spec.Dir {
			t.Errorf("the working folder %s has %v, want it empty", joins[1].spec.Dir, seen.workFiles)
		}
		for _, f := range files {
			if !slices.Contains(seen.copyPaths, f) {
				t.Errorf("%s is not in the copy, which has %v", f, seen.copyPaths)
			}
		}
	})
	t.Run("project files", func(t *testing.T) {
		files := []string{".claude/agents/helper.md", ".claude/settings.json", "CLAUDE.md", ".mcp.json"}
		r, joins, _ := reviewRunWith(t, commits(files), answerFor(2, "review-changes"))
		if len(joins) != 1 {
			t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
		}
		r.wantsClose(t, event.StateFailed, 1)
		want := "Move the change to project files out of the reviewed change in the Build session"
		if r.err == nil || !strings.HasPrefix(r.err.Error(), want) || !strings.Contains(r.err.Error(), `"CLAUDE.md"`) {
			t.Errorf("the error is %v, want the stop with CLAUDE.md\n%s", r.err, r.out)
		}
	})
}

// TestAReviewJoinInRound2GetsANewEmptyReportsFolder is a run whose sessions 2 and 4
// review, in rounds 1 and 2 of the work item's stage: each gets a copy and an empty
// reports folder, both named by the session's number, and the suite result of the commit
// it reads. The later round is the stage's last.
func TestAReviewJoinInRound2GetsANewEmptyReportsFolder(t *testing.T) {
	r, joins, _ := reviewRunWith(t, reviewCfg{next: func(p *pushRepo, n int) {
		if n == 2 {
			p.api.setRound(2, 2)
		}
	}}, answerFor(2, "review-changes"), fullSuiteAnswer(3, 30), answerFor(4, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 4 {
		t.Fatalf("%d joins, want 4\n%s", len(joins), r.out)
	}
	one, two := reviewSessionOf(t, joins, 1), reviewSessionOf(t, joins, 3)
	if one.reports == two.reports || one.commit == two.commit {
		t.Errorf("rounds 1 and 2 share the reports folder %s or the commit %s", two.reports, two.commit)
	}
	if !strings.HasSuffix(one.reports, "review-reports-2") || !strings.HasSuffix(two.reports, "review-reports-4") {
		t.Errorf("the reports folders are %s and %s, want one for each session", one.reports, two.reports)
	}
	if !joins[3].reportsLeft || joins[3].copyLeft {
		t.Errorf("at session 4 the reports folder of session 2 was there %v and its copy %v, want true and false", joins[3].reportsLeft, joins[3].copyLeft)
	}
	if len(two.entries) != 0 || !strings.Contains(two.diff, "s3.txt") {
		t.Errorf("round 2 has the entries %v and the diff %q, want an empty folder and the change of session 3", two.entries, two.diff)
	}
}

// TestAReviewJoinStopsBeforeTheJoinWithoutItsInputs is a run whose review session has no
// suite result file of the commit it reads, a suite result of another commit, or no round
// in the work item: the session does not join, the run closes failed with the cause only,
// and session 1's push stays.
func TestAReviewJoinStopsBeforeTheJoinWithoutItsInputs(t *testing.T) {
	for _, c := range []struct {
		name    string
		cfg     reviewCfg
		answers []string
		want    string
	}{
		{"no suite", reviewCfg{noSuite: true}, []string{answerFor(2, "review-changes")}, "The run has no suite result for the pushed commit, so the review did not start"},
		// Session 2 writes a commit past the suite of session 1, and runs no suite.
		{"a suite of another commit", reviewCfg{writes: []int{1, 2}}, []string{answerFor(2, "hello"), answerFor(3, "review-changes")}, "The last suite ran on another commit than the pushed one"},
		{"no round", reviewCfg{noRound: true}, []string{answerFor(2, "review-changes")}, "The work item's stage has no valid round, so the review did not start"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, joins, _ := reviewRunWith(t, c.cfg, c.answers...)
			wantJoins := 1
			if c.name == "a suite of another commit" {
				wantJoins = 2
			}
			if len(joins) != wantJoins {
				t.Fatalf("%d joins, want %d\n%s", len(joins), wantJoins, r.out)
			}
			r.wantsClose(t, event.StateFailed, 1)
			if r.err == nil || !strings.Contains(r.err.Error(), c.want) {
				t.Errorf("the error is %v, want it to contain %q\n%s", r.err, c.want, r.out)
			}
			if first, _, _ := strings.Cut(r.err.Error(), "\n"); len("qory run: ")+len(first) > 100 {
				t.Errorf("the first line has %d characters with qory run: %s", len(first), first)
			}
			if !strings.HasSuffix(r.err.Error(), "; the code is used") {
				t.Errorf("the error %q does not end with the code line", r.err)
			}
		})
	}
}

// TestAReviewJoinTakesAWallUserOfQorysUidWhateverItsGid is a run whose wall.user is qory's
// uid alone, or qory's uid with another gid than qory's: [oneUser] lets it run, and the
// review session joins, as with "<uid>:<gid>". qory gives no folder to the wall's user,
// since the folders belong to qory's user, whose uid is the wall user's.
func TestAReviewJoinTakesAWallUserOfQorysUidWhateverItsGid(t *testing.T) {
	for name, user := range map[string]func(uid, gid int) string{
		"uid only":        func(uid, _ int) string { return strconv.Itoa(uid) },
		"another gid":     func(uid, gid int) string { return strconv.Itoa(uid) + ":" + strconv.Itoa(gid+1) },
		"a gid of root":   func(uid, _ int) string { return strconv.Itoa(uid) + ":0" },
		"the default gid": nil,
	} {
		t.Run(name, func(t *testing.T) {
			r, joins, _ := reviewRunWith(t, reviewCfg{wallUser: user}, answerFor(2, "review-changes"), answerFor(3, "hello"))
			r.wantsClose(t, event.StateSucceeded, 0)
			if len(joins) != 3 {
				t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
			}
			if joins[1].seen == nil {
				t.Errorf("session 2 has no review session\n%s", r.out)
			}
		})
	}
}

// TestAReviewJoinHasTheBuildReportOfTheStage is a run with a build-report version of the
// stage: review_session.build_report is a read-only file with the report's text. A run
// with no such version leaves the member out, see
// [TestAReviewJoinReadsAFreshCopyOfThePushedCommit].
func TestAReviewJoinHasTheBuildReportOfTheStage(t *testing.T) {
	r, joins, _ := reviewRunWith(t, reviewCfg{build: "# Build report\n"}, answerFor(2, "review-changes"), answerFor(3, "hello"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	seen := reviewSessionOf(t, joins, 1)
	if !seen.hasBuild || seen.build != "# Build report\n" {
		t.Errorf("review_session.build_report is %q, want the text of the version", seen.build)
	}
}

// TestTheLoopKeepsTheSuiteOfTheLastSessionForTheReview is a write run of three sessions:
// sessions 1 and 2 write a commit and run the full suite, each red, and session 3 reviews.
// The review reads the suite result file of session 2, the last suite, with the counts of
// its result file and the output that starts with the marker of the commit that session 2
// pushed, and not the result of session 1.
func TestTheLoopKeepsTheSuiteOfTheLastSessionForTheReview(t *testing.T) {
	var first, second string
	r, joins, _ := reviewRunWith(t, reviewCfg{writes: []int{1, 2}, next: func(p *pushRepo, n int) {
		switch n {
		case 2:
			first = p.branchHead(t)
		case 3:
			second = p.branchHead(t)
		}
	}}, fullSuiteAnswer(2, 30), answerFor(3, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	seen := reviewSessionOf(t, joins, 2)
	if first == "" || second == "" || first == second || seen.commit != second {
		t.Fatalf("the review reads %s, want the head of session 2, %s, and not that of session 1, %s", seen.commit, second, first)
	}
	var got struct {
		Tests, Failed int
		OutputTail    string `json:"output_tail"`
	}
	if err := json.Unmarshal([]byte(seen.suite), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tests != 2 || got.Failed != 1 || !strings.HasPrefix(got.OutputTail, "qory-suite-head "+second) {
		t.Errorf("the suite result is %+v, want the counts 2 and 1 and the output of a suite on %s", got, second)
	}
}

// TestAReviewJoinAfterChangesLeftUncommittedStopsWithTheirText is a run whose session 1
// commits a file and leaves another uncommitted: the push step commits it as "Changes left
// by run 1" on top of the commit that the suite ran on, so the pushed head is not the
// suite's commit. The review session does not join; the run closes failed with the text
// of changes left behind, whose line 1 wraps after its comma, with both commits on line 2.
func TestAReviewJoinAfterChangesLeftUncommittedStopsWithTheirText(t *testing.T) {
	var suiteCommit string
	r, joins, p := reviewRunWith(t, reviewCfg{
		writes: []int{1},
		left: func(dir string, n int) {
			suiteCommit = proxygittest.Git(t, dir, "rev-parse", "HEAD")
			writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		},
	}, answerFor(2, "review-changes"))
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
	}
	r.wantsClose(t, event.StateFailed, 1)
	if suiteCommit == "" || r.err == nil {
		t.Fatalf("the run ended with %v and the suite's commit %q\n%s", r.err, suiteCommit, r.out)
	}
	head := p.branchHead(t)
	want := "The agent left changes uncommitted,\n  so the suite did not run on the pushed commit and the review did not start\n" +
		"  The push committed them as " + head + " on top of " + suiteCommit + ", the commit the suite ran on; the code is used"
	if r.err.Error() != want {
		t.Errorf("the error is %q, want %q", r.err.Error(), want)
	}
}
