package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	_ "github.com/qoryai/qory/internal/render/claude"
	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
)

const (
	headA = "0123456789abcdef0123456789abcdef01234567"
	headB = "89abcdef0123456789abcdef0123456789abcdef"
)

// reportVersion is a version of the artifact type build-report that the fake run API of
// [buildReportClient] lists. files are the names of its files.
type reportVersion struct {
	id               string
	number           int
	at               string
	stage            string
	cancelled, wdraw bool
	files            []string
}

// buildReportClient is a run API client whose index lists versions as one artifact of the
// type build-report, and whose file downloads return "text of <version id>".
func buildReportClient(t *testing.T, versions ...reportVersion) *runapi.Client {
	t.Helper()
	return buildReportClientWith(t, nil, versions...)
}

// buildReportClientWith is [buildReportClient] with onDownload, which runs on each file
// download before the response and returns true when it answered the request itself. nil
// is no hook.
func buildReportClientWith(t *testing.T, onDownload func(http.ResponseWriter) bool, versions ...reportVersion) *runapi.Client {
	t.Helper()
	var list []string
	for _, v := range versions {
		var files []string
		for _, name := range v.files {
			text := "text of " + v.id
			sum := sha256.Sum256([]byte(text))
			files = append(files, fmt.Sprintf(`{"filename":%q,"content_type":"text/markdown","size":%d,"sha256":"%s"}`,
				name, len(text), hex.EncodeToString(sum[:])))
		}
		list = append(list, fmt.Sprintf(`{"id":%q,"number":%d,"writer":{"kind":"run","run_id":"r"},"cancelled":%v,"withdrawn":%v,"stage_id":%q,"note":null,"at":%q,"files":[%s],"approvals":[]}`,
			v.id, v.number, v.cancelled, v.wdraw, v.stage, v.at, strings.Join(files, ",")))
	}
	index := `{"version":1,"artifacts":[{"type":"build-report","name":"main","stage_id":null,"approved_version":null,"newest_version":9,"changed_after_approval":false,"versions":[` +
		strings.Join(list, ",") + `]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/runs/r")
		if path == "/artifacts" {
			_, _ = w.Write([]byte(index))
			return
		}
		if id, ok := strings.CutPrefix(path, "/artifacts/"); ok {
			if onDownload != nil && onDownload(w) {
				return
			}
			id, _, _ = strings.Cut(id, "/")
			_, _ = w.Write([]byte("text of " + id))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestBuildReportPicksTheNewestVersionOfTheStage: the report is the newest version of
// the stage that is neither cancelled nor withdrawn, newest by its time and then by its
// number; a version of another stage does not count, and a version with two files is an
// error.
func TestBuildReportPicksTheNewestVersionOfTheStage(t *testing.T) {
	const stage = "st1"
	one := []string{"report.md"}
	for _, c := range []struct {
		name     string
		versions []reportVersion
		want     string // the id of the version whose text the file has, "" for none
	}{
		{"no version", nil, ""},
		{"a cancelled version alone", []reportVersion{{"c", 3, "2026-11-22T10:00:03Z", stage, true, false, one}}, ""},
		{"a withdrawn version alone", []reportVersion{{"w", 3, "2026-11-22T10:00:03Z", stage, false, true, one}}, ""},
		{"another stage alone", []reportVersion{{"o", 3, "2026-11-22T10:00:03Z", "st2", false, false, one}}, ""},
		{"newest by the time", []reportVersion{
			{"late", 1, "2026-11-22T10:00:05Z", stage, false, false, one},
			{"early", 2, "2026-11-22T10:00:01Z", stage, false, false, one},
		}, "late"},
		{"the same time, newest by the number", []reportVersion{
			{"n1", 1, "2026-11-22T10:00:05Z", stage, false, false, one},
			{"n2", 2, "2026-11-22T10:00:05Z", stage, false, false, one},
		}, "n2"},
		{"newer versions that do not count", []reportVersion{
			{"ok", 1, "2026-11-22T10:00:01Z", stage, false, false, one},
			{"cancelled", 2, "2026-11-22T10:00:02Z", stage, true, false, one},
			{"withdrawn", 3, "2026-11-22T10:00:03Z", stage, false, true, one},
			{"other", 4, "2026-11-22T10:00:04Z", "st2", false, false, one},
		}, "ok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &reviewJoinPlan{client: buildReportClient(t, c.versions...), stage: stage}
			dir := filepath.Join(t.TempDir(), "build-report")
			path, err := p.buildReport(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if path != "" {
					t.Errorf("the report is %s, want none", path)
				}
				if _, err := os.Stat(dir); err == nil {
					t.Error("a run with no report made its folder")
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "text of "+c.want {
				t.Errorf("the report is %q (%v), want the text of %s", data, err, c.want)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o444 {
				t.Errorf("the report has mode %v (%v), want 0444", info, err)
			}
		})
	}

	two := reportVersion{"two", 1, "2026-11-22T10:00:01Z", stage, false, false, []string{"a.md", "b.md"}}
	p := &reviewJoinPlan{client: buildReportClient(t, two), stage: stage}
	_, err := p.buildReport(t.Context(), filepath.Join(t.TempDir(), "build-report"))
	if !hasStop(err, "The newest build report has 2 files, so the review did not start") {
		t.Errorf("a report of two files returned %v", err)
	}
}

// TestReviewTarget is the pushed head, base and git folder of the branch, and each stop,
// and the project base of the repository at ".", not of another repository or the start.
func TestReviewTarget(t *testing.T) {
	cwd := t.TempDir()
	branch := func(dir string) *preparation.WorkBranch {
		return &preparation.WorkBranch{Repositories: []preparation.WorkRepository{{Dir: dir, Commit: headB, Start: headA}}}
	}
	if err := os.Mkdir(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit, base, source, err := reviewTarget(branch("."), cwd)
	if err != nil || commit != headB || base != headA || source != filepath.Join(cwd, ".git") {
		t.Errorf("reviewTarget returned %q, %q, %q, %v", commit, base, source, err)
	}
	if _, _, _, err := reviewTarget(nil, cwd); !hasStop(err, "The run has no branch, so it has no pushed commit and the review did not start") {
		t.Errorf("no branch returned %v", err)
	}
	target := &preparation.WorkBranch{Repositories: []preparation.WorkRepository{
		{Dir: "sub", ProjectBase: headB}, {Dir: ".", Commit: headB, Start: headB, ProjectBase: headA},
	}}
	if got := targetProjectBase(target); got != headA {
		t.Errorf("the project base of the target is %q, want %s", got, headA)
	}
	if got := targetProjectBase(nil); got != "" {
		t.Errorf("no branch has the project base %q", got)
	}
	if _, _, _, err := reviewTarget(branch("sub"), cwd); !hasStop(err, `The branch has no repository at ".", so the review did not start`) {
		t.Errorf("no repository at . returned %v", err)
	}
	if _, _, _, err := reviewTarget(branch("."), "relative"); !hasStop(err, "The working folder is not an absolute, clean path, so the review did not start") {
		t.Errorf("a relative working folder returned %v", err)
	}

	file, link := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(file, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(link, ".git")); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"a .git file": file, "a .git link": link} {
		if _, _, _, err := reviewTarget(branch("."), dir); !hasStop(err, "The checkout's .git is a link or a file, so the review did not start") {
			t.Errorf("%s returned %v", name, err)
		}
	}
}

// TestReviewSuiteFileReadsTheMarkerAndNotTheStatusFile: the suite of the pushed commit is
// the one whose marker line has that commit. A record with no marker, one whose suite did
// not start, none, and one whose marker is another commit each stop the join.
func TestReviewSuiteFileReadsTheMarkerAndNotTheStatusFile(t *testing.T) {
	started := suite.Outcome{Started: true}
	ok := &suiteRecord{path: "/run/review/suite-1.json", head: headA, outcome: started}
	if got, err := reviewSuiteFile(t.Context(), ok, headA, ""); err != nil || got != ok.path {
		t.Errorf("a suite of the pushed commit returned %q, %v", got, err)
	}
	for _, c := range []struct {
		name string
		last *suiteRecord
		want string
	}{
		{"no record", nil, "The run has no suite result for the pushed commit, so the review did not start"},
		{"no file", &suiteRecord{head: headA, outcome: started}, "The run has no suite result for the pushed commit, so the review did not start"},
		{"no marker", &suiteRecord{path: ok.path, outcome: started}, "The run has no suite result for the pushed commit, so the review did not start"},
		{"a suite that did not start", &suiteRecord{path: ok.path}, "The full suite of the pushed commit did not start, so the review did not start"},
		{"another commit", &suiteRecord{path: ok.path, head: headB, outcome: started}, "The last suite ran on another commit than the pushed one, so the review did not start"},
	} {
		if _, err := reviewSuiteFile(t.Context(), c.last, headA, ""); !hasStop(err, c.want) {
			t.Errorf("%s returned %v, want %q", c.name, err, c.want)
		}
	}
}

// TestReviewSuiteFileNamesChangesLeftByTheAgent: a suite of the parent of the pushed
// commit, whose message is "Changes left by run N", stops the join with the text of the
// changes the agent left; a suite of any other commit, and a pushed commit that git cannot
// read, stop with the text of a suite of another commit.
func TestReviewSuiteFileNamesChangesLeftByTheAgent(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test",
		}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("commit", "--quiet", "--allow-empty", "-m", "Work of the agent")
	first := git("rev-parse", "HEAD")
	git("commit", "--quiet", "--allow-empty", "-m", fmt.Sprintf(textChangesLeft, 1))
	left := git("rev-parse", "HEAD")
	git("commit", "--quiet", "--allow-empty", "-m", "Another change")
	plain := git("rev-parse", "HEAD")
	source := filepath.Join(dir, ".git")
	started := suite.Outcome{Started: true}
	suiteOf := func(head string) *suiteRecord {
		return &suiteRecord{path: "/run/suite.json", head: head, outcome: started}
	}

	_, err := reviewSuiteFile(t.Context(), suiteOf(first), left, source)
	want := "The agent left changes uncommitted,\n  so the suite did not run on the pushed commit and the review did not start\n" +
		"  The push committed them as " + left + " on top of " + first + ", the commit the suite ran on" + codeUsed
	if err == nil || err.Error() != want {
		t.Errorf("changes left by the agent returned %q, want %q", err, want)
	}
	other := "The last suite ran on another commit than the pushed one, so the review did not start"
	for name, c := range map[string]struct{ suite, head, source string }{
		"a commit that is not a push of changes": {left, plain, source},
		"a suite of an older commit":             {first, plain, source},
		"a suite of another parent":              {plain, left, source},
		"a git folder that is missing":           {first, left, filepath.Join(dir, "none")},
		"a commit that is missing":               {first, headB, source},
	} {
		if _, err := reviewSuiteFile(t.Context(), suiteOf(c.suite), c.head, c.source); !hasStop(err, other) {
			t.Errorf("%s returned %v, want the text of another commit", name, err)
		}
	}
}

// TestReviewCopyStopMakesTheStopOfTheJoin: a stop of the copy has the cause, the words
// "so the review did not start", its detail on line 2 and codeUsed; line 1 goes on after
// its last ", so " when "qory run: " and line 1 have more than 100 characters.
func TestReviewCopyStopMakesTheStopOfTheJoin(t *testing.T) {
	_, err := reviewcopy.New("review")
	want := "The path of the review's copy is not an absolute, clean path on one line,\n  so the review did not start\n  The path is \"review\"" + codeUsed
	if err = reviewCopyStop(err); err == nil || err.Error() != want {
		t.Errorf("a relative path returned %q, want %q", err, want)
	}
	f, err := reviewcopy.New(filepath.Join(t.TempDir(), "review"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Session("src/.git", headA, "", "", nil)
	if err = reviewCopyStop(err); err == nil || !strings.HasPrefix(err.Error(), "The git folder of the review's copy is not an absolute, clean path,\n  so the review did not start\n  The path is ") {
		t.Errorf("a relative git folder returned %q", err)
	}
	other := errors.New("other")
	if reviewCopyStop(other) != other {
		t.Error("an error that is no stop of the copy changed")
	}
}

// exists is whether path exists, as a link too.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// noAPIRun is an opened run with no run API.
type noAPIRun struct{ openedRun }

func (noAPIRun) RunAPI() (*http.Client, string, error) { return nil, "", nil }

func TestReviewClientNeedsARunAPI(t *testing.T) {
	if _, err := reviewClient(noAPIRun{}); !hasStop(err, "The run has no run API, so the review did not start") {
		t.Errorf("a run with no run API returned %v", err)
	}
	if _, err := reviewClient(runAPIOnly{base: "http://run-api.qory.invalid/v1/runs/r"}); err != nil {
		t.Errorf("a run with a run API returned %v", err)
	}
}

func TestCopyReadOnlyCopiesAFileAndRefusesALink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	if err := os.WriteFile(src, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.json")
	if err := copyReadOnly(src, dst); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("the copy has %v (%v), want mode 0444", info, err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	if err := copyReadOnly(link, filepath.Join(dir, "other.json")); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("a link returned %v", err)
	}
	if err := copyReadOnly(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); err == nil {
		t.Error("a missing file returned no error")
	}
}

// copyRun is an opened run whose image is for [gitContainerFor].
type copyRun struct{ openedRun }

func (copyRun) Image() session.Image { return session.Image{Ref: "example.test/run:1"} }

// copyPlan is a plan whose copy folder is new, with the source git folder.
func copyPlan(t *testing.T) *reviewJoinPlan {
	t.Helper()
	under := t.TempDir()
	copied, err := reviewcopy.New(filepath.Join(under, "review-copy-2"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(under, "checkout", ".git")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	return &reviewJoinPlan{commit: headB, base: headA, source: source, copy: copied}
}

// TestMakeCopyStopsWithTheCauseOfEachFailure: the copy container does not start, exits
// with a code and no result, exits 0 and leaves no result, and the run is cut; a copy that
// ends well prints how many links and submodules it left out.
func TestMakeCopyStopsWithTheCauseOfEachFailure(t *testing.T) {
	was := runGitContainer
	t.Cleanup(func() { runGitContainer = was })
	spec := &runSpec{}
	spec.RunID = "r1"

	run := func(p *reviewJoinPlan, f func(c gitContainer) (int, error)) (string, error) {
		runGitContainer = func(_ context.Context, c gitContainer) (int, error) { return f(c) }
		var out bytes.Buffer
		err := p.makeCopy(t.Context(), copyRun{}, spec, &out)
		return out.String(), err
	}

	_, err := run(copyPlan(t), func(gitContainer) (int, error) { return -1, errors.New("no engine") })
	if !hasStop(err, "The container that copies the commit did not start, so the review did not start") || !strings.Contains(err.Error(), "no engine") {
		t.Errorf("a container that did not start returned %v", err)
	}
	_, err = run(copyPlan(t), func(gitContainer) (int, error) { return 3, nil })
	if !hasStop(err, "The session that copies the commit exited with 3, so the review did not start") {
		t.Errorf("an exit with 3 and no result returned %v", err)
	}
	_, err = run(copyPlan(t), func(gitContainer) (int, error) { return 0, nil })
	if !hasStop(err, "The session that copies the commit ended before the copy was complete, so the review did not start") {
		t.Errorf("an exit with 0 and no result returned %v", err)
	}
	cut, cancel := context.WithCancel(t.Context())
	cancel()
	runGitContainer = func(context.Context, gitContainer) (int, error) { return -1, context.Canceled }
	if err := copyPlan(t).makeCopy(cut, copyRun{}, spec, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cut run returned %v, want its context error", err)
	}

	p := copyPlan(t)
	out, err := run(p, func(c gitContainer) (int, error) {
		root := p.copy.Root()
		state := filepath.Join(root, "state")
		for _, d := range []string{state, p.copy.Commit()} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return -1, err
			}
		}
		for path, text := range map[string]string{
			filepath.Join(state, "done"):       "ok\n",
			filepath.Join(state, "left-out"):   "120000 blob abc\tlink\n160000 commit def\tsub\n",
			filepath.Join(state, "changed"):    "",
			filepath.Join(root, "commit.diff"): "diff\n",
		} {
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return -1, err
			}
		}
		if len(c.Mounts) != 2 || !c.Mounts[0].ReadOnly || c.Mounts[0].Path != p.source || c.Mounts[1].ReadOnly || c.Mounts[1].Path != root {
			t.Errorf("the copy container has the mounts %+v, want the source read-only and the copy writable", c.Mounts)
		}
		return 0, nil
	})
	if err != nil || !strings.Contains(out, "leaves out 2 links and submodules") {
		t.Errorf("a copy that ended well printed %q and returned %v", out, err)
	}
}

// TestAReviewOfAChangeToProjectFilesStops: the review of a change from the project base
// that adds .claude/skills/x/SKILL.md, changes .mcp.json or CLAUDE.md, adds
// sub/CLAUDE.local.md or AGENTS.md, or adds sub/.claude/agents/a.md or sub/.mcp.json below
// the root stops before the session, with the fix on line 1 and the paths on line 2, and
// the same skill at the start passes. Line 2 shows at most five paths, each cut at 200
// characters. The copy's script runs with this machine's git in place of the copy
// container, with the project paths of Claude Code's review guard.
func TestAReviewOfAChangeToProjectFilesStops(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	path := filepath.Dir(gitPath) + ":/usr/bin:/bin"
	rt, _ := render.Lookup("claude")
	guard, ok := render.ReviewGuard(rt)
	if !ok {
		t.Fatal("claude has no review guard")
	}
	was := runGitContainer
	t.Cleanup(func() { runGitContainer = was })
	runGitContainer = func(ctx context.Context, c gitContainer) (int, error) {
		args := slices.Clone(c.Session.Args)
		args[1] = path
		cmd := exec.CommandContext(ctx, c.Session.Command, args...)
		cmd.Dir = c.Session.Dir
		for _, kv := range c.Session.Env {
			if strings.HasPrefix(kv, "PATH=") {
				kv = "PATH=" + path
			}
			cmd.Env = append(cmd.Env, kv)
		}
		cmd.Stdout, cmd.Stderr = c.Output, c.Output
		var exit *exec.ExitError
		if err := cmd.Run(); errors.As(err, &exit) {
			return exit.ExitCode(), nil
		} else if err != nil {
			return -1, err
		}
		return 0, nil
	}
	spec := &runSpec{}
	spec.RunID = "r1"
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=T",
			"GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	const skill = ".claude/skills/x/SKILL.md"
	for name, c := range map[string]struct {
		base, head map[string]string
		// paths are the quoted paths of line 2, "" for a review that passes.
		paths string
		// start is the commit of a rerun between base and head, where the run starts; nil
		// for a first run, which starts at base. noProjectBase is a run with no project
		// base, whose list has each project file of the commit.
		start         map[string]string
		noProjectBase bool
	}{
		"an added skill":       {map[string]string{"README.md": "a\n"}, map[string]string{skill: "---\nname: x\n---\n"}, strconv.Quote(skill), nil, false},
		"a skill at the start": {map[string]string{skill: "---\nname: x\n---\n"}, map[string]string{"README.md": "b\n"}, "", nil, false},
		"a changed .mcp.json":  {map[string]string{".mcp.json": "{}\n"}, map[string]string{".mcp.json": "{\"mcpServers\": {}}\n"}, `".mcp.json"`, nil, false},
		"a changed CLAUDE.md":  {map[string]string{"CLAUDE.md": "a\n"}, map[string]string{"CLAUDE.md": "b\n"}, `"CLAUDE.md"`, nil, false},
		"a CLAUDE.local.md":    {map[string]string{"README.md": "a\n"}, map[string]string{"sub/CLAUDE.local.md": "c\n"}, `"sub/CLAUDE.local.md"`, nil, false},
		"an AGENTS.md":         {map[string]string{"README.md": "a\n"}, map[string]string{"AGENTS.md": "c\n"}, `"AGENTS.md"`, nil, false},
		"nested files": {map[string]string{"README.md": "a\n"}, map[string]string{"sub/.claude/agents/a.md": "a\n", "sub/.mcp.json": "{}\n"},
			`"sub/.claude/agents/a.md", "sub/.mcp.json"`, nil, false},
		"a rerun whose start has the skill": {map[string]string{"README.md": "a\n"}, map[string]string{"README.md": "b\n"},
			strconv.Quote(skill), map[string]string{skill: "---\nname: x\n---\n"}, false},
		"a timed-out base with a skill in the commit": {map[string]string{skill: "---\nname: x\n---\n"}, map[string]string{"README.md": "b\n"},
			strconv.Quote(skill), nil, true},
		"a timed-out base with no project file": {map[string]string{"README.md": "a\n"}, map[string]string{"README.md": "b\n"},
			"", nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			git(dir, "init", "-q", "-b", "main")
			commit := func(files map[string]string) string {
				for rel, text := range files {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, rel), []byte(text), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				git(dir, "add", "-A")
				git(dir, "commit", "-q", "-m", "c")
				return git(dir, "rev-parse", "HEAD")
			}
			base := commit(c.base)
			start := base
			if c.start != nil {
				start = commit(c.start)
			}
			head := commit(c.head)
			projectBase := base
			if c.noProjectBase {
				projectBase = ""
			}
			copied, err := reviewcopy.New(filepath.Join(t.TempDir(), "review-copy-1"))
			if err != nil {
				t.Fatal(err)
			}
			p := &reviewJoinPlan{commit: head, base: start, source: filepath.Join(dir, ".git"), copy: copied, project: guard.ProjectPaths(), projectBase: projectBase}
			t.Cleanup(p.removeCopy)
			var out bytes.Buffer
			err = p.makeCopy(t.Context(), copyRun{}, spec, &out)
			if c.paths == "" {
				if err != nil {
					t.Errorf("the review stopped: %v\n%s", err, out.String())
				}
				return
			}
			want := "Move the change to project files out of the reviewed change in the Build session\n" +
				"  The review did not start, since the session could load the settings and tools of " + c.paths +
				", which the change adds, changes or removes. The run code is used: start the stage again from the work item in Qory Apiary."
			if c.noProjectBase {
				want = "Move the change to project files out of the reviewed change in the Build session\n" +
					"  The review did not start: qory could not read the change's base, so it treats these files in the commit as changed: " + c.paths +
					". The run code is used: start the stage again from the work item in Qory Apiary."
			}
			if err == nil || err.Error() != want {
				t.Errorf("the review returned %v\nwant %q\n%s", err, want, out.String())
			}
		})
	}
	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	if err := projectFilesStop(many, true); !strings.Contains(err.Error(), `"a", "b", "c", "d", "e" and 2 more, which`) {
		t.Errorf("seven paths returned %v", err)
	}
	long := ".claude/" + strings.Repeat("a", 300) + "\x1b"
	if err := projectFilesStop([]string{long}, true); !strings.Contains(err.Error(), strconv.Quote(long[:200]+"...")+", which") {
		t.Errorf("a path of 309 characters returned %v", err)
	}
	if err := projectFilesStop(many, false); !strings.HasSuffix(err.Error(), `as changed: "a", "b", "c", "d", "e" and 2 more. The run code is used: start the stage again from the work item in Qory Apiary.`) {
		t.Errorf("seven paths with no project base returned %v", err)
	}
	if err := projectFilesStop([]string{long}, false); !strings.Contains(err.Error(), strconv.Quote(long[:200]+"...")+". The run code") {
		t.Errorf("a path of 309 characters with no project base returned %v", err)
	}
}

// TestRemoveCopyKeepsTheReportsAndRemoveTakesAll: removeCopy removes the copy, which the
// copy session made read-only, and the working folder, and leaves the reports folder;
// remove removes the reports folder too. Both can run twice.
func TestRemoveCopyKeepsTheReportsAndRemoveTakesAll(t *testing.T) {
	under := t.TempDir()
	copied, err := reviewcopy.New(filepath.Join(under, "review-copy-2"))
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(copied.Root(), "commit", "sub")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{locked, filepath.Dir(locked), copied.Root()} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	work, reports := filepath.Join(under, "review-work-2"), filepath.Join(under, "review-reports-2")
	for _, d := range []string{work, reports} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p := &reviewJoinPlan{copy: copied, work: work, reports: reports}
	p.removeCopy()
	p.removeCopy()
	if exists(copied.Root()) || exists(work) || !exists(reports) {
		t.Errorf("after removeCopy the copy is there %v, the working folder %v and the reports %v; want false, false and true",
			exists(copied.Root()), exists(work), exists(reports))
	}
	p.remove()
	if exists(reports) {
		t.Error("remove left the reports folder")
	}
	(&reviewJoinPlan{}).remove()
}

// TestAReviewJoinThatCannotRunStopsBeforeAnyFolder: a runtime other than claude, an image
// with an inner Docker daemon, a setup inside the agent's container and a run with no wall
// stop at the plan's first check, before the plan makes its copy, working folder or
// reports folder.
func TestAReviewJoinThatCannotRunStopsBeforeAnyFolder(t *testing.T) {
	for _, c := range []struct {
		name    string
		runtime string
		img     session.Image
		inside  bool
		noWall  bool
		want    string
	}{
		{"codex", "codex", session.Image{}, false, false, "qory has no argument that limits a review session's tools on codex,\n  so the review did not start" + codeUsed},
		{"nested Docker", "claude", session.Image{Docker: true}, false, false, "The run's image runs an inner Docker daemon, so the review did not start\n  A read-only review session cannot run beside it" + codeUsed},
		{"setup inside", "claude", session.Image{}, true, false, "The workflow review-changes only reads the checkout and wall.setup is inside,\n  so the review did not start\n  A setup inside runs in the agent's container, which sees the checkout read-only" + codeUsed},
		{"no wall", "claude", session.Image{}, false, true, "The workflow review-changes only reads the checkout and the run has no wall,\n  so the review did not start\n  A run with no wall mounts nothing read-only" + codeUsed},
	} {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			in := filepath.Join(parent, "in")
			if err := os.Mkdir(in, 0o755); err != nil {
				t.Fatal(err)
			}
			spec := &runSpec{}
			if !c.noWall {
				spec.Wall = &wall.Docker{}
			}
			spec.Dir, spec.Mounts = "/checkout", []wall.Mount{{Path: "/checkout"}}
			if c.inside {
				spec.setupWhere = config.SetupInside
			}
			_, err := planReviewJoin(t.Context(), reviewJoinSetup{
				spec: spec, workflow: "review-changes", root: "/checkout", cwd: "/checkout", in: in,
				number: 2, runtime: c.runtime, img: c.img,
			})
			if err == nil || err.Error() != c.want {
				t.Errorf("the plan returned %v, want %q", err, c.want)
			}
			entries, rerr := os.ReadDir(parent)
			if rerr != nil || len(entries) != 1 {
				t.Errorf("the plan made %v beside the inputs folder (%v), want no folder", entries, rerr)
			}
		})
	}
}

// TestTheFinalCheckOfTheJoinStopsAWritableMountThatIsNotTheReportsFolder: the join's
// mounts are the working folder and the copy, read-only, and the reports folder,
// writable; another writable mount, a missing copy or working folder, or a workspace that
// is not the working folder read-only stops the join.
func TestTheFinalCheckOfTheJoinStopsAWritableMountThatIsNotTheReportsFolder(t *testing.T) {
	under := t.TempDir()
	mkdir := func(parts ...string) string {
		p := filepath.Join(append([]string{under}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	copied, err := reviewcopy.New(filepath.Join(under, "review-copy-2"))
	if err != nil {
		t.Fatal(err)
	}
	mkdir("review-copy-2", "commit")
	p := &reviewJoinPlan{
		copy: copied, work: mkdir("review-work-2"), reports: mkdir("review-reports-2"),
		artifacts: mkdir("checkout", ".qory", "artifacts"), home: mkdir("home"),
	}
	p.reportsMount = wall.Mount{Path: p.reports}
	good := func() session.SessionSpec {
		return session.SessionSpec{
			WorkspaceReadOnly: true, Dir: p.work,
			Mounts: []wall.Mount{
				{Path: "/cache", ReadOnly: true}, {Path: p.work, ReadOnly: true},
				{Path: copied.Commit(), ReadOnly: true}, p.reportsMount,
			},
			Written: []string{p.reports},
		}
	}
	if err := p.check(good()); err != nil {
		t.Fatalf("a join that holds the rules returned %v", err)
	}

	const writable = "A mount other than the reports folder is writable, so the review did not start"
	const workFolder = "The review's working folder is not the empty folder qory made, so the review did not start"
	const noCopy = "The review has no copy of the commit or no writable reports folder, so it did not start"
	for _, c := range []struct {
		name   string
		change func(*session.SessionSpec)
		want   string
	}{
		{"a writable cache", func(s *session.SessionSpec) { s.Mounts[0].ReadOnly = false }, writable},
		{"a writable copy", func(s *session.SessionSpec) { s.Mounts[2].ReadOnly = false }, writable},
		{"a writable working folder", func(s *session.SessionSpec) { s.Mounts[1].ReadOnly = false }, writable},
		{"a writable checkout", func(s *session.SessionSpec) { s.Mounts = append(s.Mounts, wall.Mount{Path: "/checkout"}) }, writable},
		{"a writable mount at the copy's target", func(s *session.SessionSpec) {
			s.Mounts = append(s.Mounts, wall.Mount{Path: "/other", Target: copied.Commit()})
		}, writable},
		{"no copy", func(s *session.SessionSpec) { s.Mounts = append(s.Mounts[:2], s.Mounts[3]) }, noCopy},
		{"no working folder mount", func(s *session.SessionSpec) { s.Mounts = append(s.Mounts[:1], s.Mounts[2:]...) }, workFolder},
		{"a writable workspace", func(s *session.SessionSpec) { s.WorkspaceReadOnly = false }, workFolder},
		{"another working folder", func(s *session.SessionSpec) { s.Dir = "/checkout" }, workFolder},
		{"a writable place that holds the reports", func(s *session.SessionSpec) { s.Written = append(s.Written, under) },
			"The review's reports folder lies in a folder that the session can write, so the review did not start"},
	} {
		join := good()
		c.change(&join)
		if err := p.check(join); !hasStop(err, c.want) {
			t.Errorf("%s: returned %v, want %q", c.name, err, c.want)
		}
	}
}

// TestReviewStopWrapsLine1AtTheLastCommaSo: line 1 with "qory run: " and, for a text of
// one line, codeUsed has at most 100 characters, so a line of 100 stays and a line of 101
// goes on after its last ", so "; a comma before it, in a path or a clause, never wraps. A
// detail is line 2 with no full stop at its end, and an empty detail makes no line 2.
func TestReviewStopWrapsLine1AtTheLastCommaSo(t *testing.T) {
	// cause makes a cause of n characters that has a clause with a comma before its last
	// ", so ".
	cause := func(n int) string {
		const tail = ", so the review did not start"
		head := "The absolute, clean path is " + strings.Repeat("x", n-len("The absolute, clean path is ")-len(tail)) + tail
		if len(head) != n {
			t.Fatalf("made %d characters, want %d", len(head), n)
		}
		return head
	}
	wrapped := func(c string) string {
		i := strings.LastIndex(c, ", so ")
		return c[:i+1] + "\n  " + c[i+2:]
	}
	oneLine := 100 - len("qory run: ") - len(codeUsed)
	withDetail := 100 - len("qory run: ")
	for name, c := range map[string]struct {
		cause, detail, want string
	}{
		"100 characters, no detail":    {cause(oneLine), "", cause(oneLine) + codeUsed},
		"101 characters, no detail":    {cause(oneLine + 1), "", wrapped(cause(oneLine+1)) + codeUsed},
		"100 characters, a detail":     {cause(withDetail), "The value is x", cause(withDetail) + "\n  The value is x" + codeUsed},
		"101 characters, a detail":     {cause(withDetail + 1), "The value is x", wrapped(cause(withDetail+1)) + "\n  The value is x" + codeUsed},
		"a detail with a full stop":    {"The review stopped", "x exists.", "The review stopped\n  x exists" + codeUsed},
		"a detail with two full stops": {"The review stopped", "x exists..", "The review stopped\n  x exists." + codeUsed},
		"an empty detail":              {"The review stopped", "", "The review stopped" + codeUsed},
		"a detail of one full stop":    {"The review stopped", ".", "The review stopped" + codeUsed},
		"no ', so ' in a long line":    {strings.Repeat("a, b ", 25), "", strings.Repeat("a, b ", 25) + codeUsed},
	} {
		if got := reviewStop(c.cause, c.detail).Error(); got != c.want {
			t.Errorf("%s: reviewStop returned %q, want %q", name, got, c.want)
		}
	}
}

// TestAFolderOfTheCopyThatCannotBeMadeIsAStopOfTheJoin: the error of the mkdir is the
// detail of a stop with the cause of text 65, not a bare error.
func TestAFolderOfTheCopyThatCannotBeMadeIsAStopOfTheJoin(t *testing.T) {
	under := t.TempDir()
	// A folder that is a file makes the mkdir fail with "not a directory".
	if err := os.WriteFile(filepath.Join(under, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reviewcopy.New(filepath.Join(under, "file", "review-copy-2"))
	got := reviewCopyStop(err)
	var detail string
	if got != nil {
		_, detail, _ = strings.Cut(got.Error(), "\n  ")
	}
	if want := "Making the folder of the commit's copy failed, so the review did not start\n  "; got == nil || !strings.HasPrefix(got.Error(), want) || !strings.Contains(detail, "not a directory") || !strings.HasSuffix(got.Error(), codeUsed) {
		t.Errorf("a copy folder that cannot be made returned %q, want %q, the error and the code line", got, want)
	}
}

// TestAMissingGitFolderStopsWithThatCause: a checkout with no .git folder stops with that
// cause, and a .git that is a file keeps the cause of a link or a file.
func TestAMissingGitFolderStopsWithThatCause(t *testing.T) {
	dir := t.TempDir()
	branch := &preparation.WorkBranch{Repositories: []preparation.WorkRepository{{Dir: "."}}}
	_, _, _, err := reviewTarget(branch, dir)
	want := "The checkout has no .git folder, so the review did not start\n  The review reads the commit from " + strconv.Quote(filepath.Join(dir, ".git")) + codeUsed
	if err == nil || err.Error() != want {
		t.Errorf("a missing .git returned %q, want %q", err, want)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = reviewTarget(branch, dir)
	want = "The checkout's .git is a link or a file, so the review did not start\n  The review reads the commit from " + strconv.Quote(filepath.Join(dir, ".git")) + codeUsed
	if err == nil || err.Error() != want {
		t.Errorf("a .git file returned %q, want %q", err, want)
	}
}

// TestCommittedAtPushReadsOnlyTheStartOfALongMessage: a pushed commit whose message is
// "Changes left by run N" and 200 KiB long is still read, since only its header and the
// start of the message count, and a commit that git cannot read is false.
func TestCommittedAtPushReadsOnlyTheStartOfALongMessage(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test",
		}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("commit", "--quiet", "--allow-empty", "-m", "Work of the agent")
	parent := git("rev-parse", "HEAD")
	message := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(message, []byte(fmt.Sprintf(textChangesLeft, 1)+"\n\n"+strings.Repeat("long line of text\n", 12000)), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "--quiet", "--allow-empty", "-F", message)
	head := git("rev-parse", "HEAD")
	source := filepath.Join(dir, ".git")
	if !committedAtPush(t.Context(), source, head, parent) {
		t.Error("a long message was not read")
	}
	if committedAtPush(t.Context(), source, headA, parent) {
		t.Error("a commit that git cannot read counts as a push of changes")
	}
}

// cutDetail is detail as a stop shows it: cut after a fixed number of characters with
// "...", and with one full stop less, as reviewStop trims the detail.
func cutDetail(detail string) string {
	return strings.TrimSuffix(integration.Printable(detail), ".")
}

// modesStopText is the text of a stop for a failed change of a mode, with its detail.
func modesStopText(detail string) string {
	return "qory could not set the modes of the review's folders, so the review did not start\n  " + cutDetail(detail) + codeUsed
}

// removeOnDownload is a download hook that removes the file at path, which the build
// report's writer has open, and lets the download go on.
func removeOnDownload(t *testing.T, path string) func(http.ResponseWriter) bool {
	return func(http.ResponseWriter) bool {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		return false
	}
}

// TestAFailedChmodOfTheBuildReportIsAStopOfTheJoin (text 66): the file of the build report
// is gone when buildReport sets its mode, so the chmod fails with "no such file". The stop
// has the cause of text 66, the error of the chmod as its detail and the code line, and it
// is the same text when inputs returns it. The chmods of the folders in inputs and in
// buildReport follow their mkdir at once, so no test reaches them without a seam.
func TestAFailedChmodOfTheBuildReportIsAStopOfTheJoin(t *testing.T) {
	const stage = "st1"
	version := reportVersion{"v1", 1, "2026-11-22T10:00:01Z", stage, false, false, []string{"report.md"}}

	t.Run("the report file, from buildReport", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "build-report")
		path := filepath.Join(dir, "report.md")
		p := &reviewJoinPlan{client: buildReportClientWith(t, removeOnDownload(t, path), version), stage: stage}
		got, err := p.buildReport(t.Context(), dir)
		want := modesStopText("chmod " + path + ": no such file or directory")
		if got != "" || err == nil || err.Error() != want {
			t.Errorf("buildReport returned %q and %q, want no path and %q", got, err, want)
		}
	})

	t.Run("the report file, through inputs", func(t *testing.T) {
		under := t.TempDir()
		in := filepath.Join(under, "in")
		if err := os.Mkdir(in, 0o755); err != nil {
			t.Fatal(err)
		}
		copied, err := reviewcopy.New(filepath.Join(under, "review-copy-2"))
		if err != nil {
			t.Fatal(err)
		}
		suiteFile := filepath.Join(under, "suite-result.json")
		for _, f := range []string{suiteFile, copied.Diff()} {
			if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(in, reviewInputsDir, "build-report", "report.md")
		p := &reviewJoinPlan{
			client: buildReportClientWith(t, removeOnDownload(t, path), version), stage: stage,
			commit: headB, suiteFile: suiteFile, copy: copied,
		}
		_, err = p.inputs(t.Context(), in)
		want := modesStopText("chmod " + path + ": no such file or directory")
		if err == nil || err.Error() != want {
			t.Errorf("inputs returned %q, want %q", err, want)
		}
	})
}

// TestAFailedFetchOfTheBuildReportShowsOnlyTheUnderlyingError (text 11): the detail of the
// stop is the error of the download, or the error of the open that made the file fail,
// with no "Check <path>. Making the file failed" of writeFresh in front of it.
func TestAFailedFetchOfTheBuildReportShowsOnlyTheUnderlyingError(t *testing.T) {
	const (
		stage = "st1"
		line1 = "Fetching the build report failed, so the review did not start\n  "
	)

	t.Run("the download fails", func(t *testing.T) {
		version := reportVersion{"v1", 1, "2026-11-22T10:00:01Z", stage, false, false, []string{"report.md"}}
		client := buildReportClientWith(t, func(w http.ResponseWriter) bool {
			http.Error(w, "storage is down", http.StatusInternalServerError)
			return true
		}, version)
		p := &reviewJoinPlan{client: client, stage: stage}
		dir := filepath.Join(t.TempDir(), "build-report")
		_, err := p.buildReport(t.Context(), dir)
		want := line1 + "Check the logs of the run's gateway. GET /artifacts/v1/files/report.md returned 500" + codeUsed
		if err == nil || err.Error() != want {
			t.Errorf("a failed download returned %q, want %q", err, want)
		}
	})

	t.Run("the file cannot be opened", func(t *testing.T) {
		// A folder whose path is a few bytes short of the longest path that a mkdir
		// accepts, so the open of a file of 255 bytes in it fails with ENAMETOOLONG.
		dir := nearLongestPath(t)
		name := strings.Repeat("r", runapi.MaxFilename)
		version := reportVersion{"v1", 1, "2026-11-22T10:00:01Z", stage, false, false, []string{name}}
		p := &reviewJoinPlan{client: buildReportClient(t, version), stage: stage}
		_, err := p.buildReport(t.Context(), dir)
		// The path is long, so the cut drops the end of the error, which is ENAMETOOLONG.
		want := line1 + cutDetail("open "+filepath.Join(dir, name)+": "+syscall.ENAMETOOLONG.Error()) + codeUsed
		if err == nil || err.Error() != want {
			t.Errorf("a file that cannot be opened returned %q, want %q", err, want)
		}
	})
}

// nearLongestPath is a path of a folder that does not exist yet, whose parent exists and
// whose mkdir succeeds, with a length that is within 100 bytes of the longest path that
// the system accepts. It makes nested folders of 100 bytes each until a mkdir fails with
// ENAMETOOLONG.
func nearLongestPath(t *testing.T) string {
	t.Helper()
	deepest := t.TempDir()
	for {
		next := filepath.Join(deepest, strings.Repeat("d", 100))
		err := os.Mkdir(next, 0o755)
		if errors.Is(err, syscall.ENAMETOOLONG) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		deepest = next
	}
	// A short name below the deepest folder is the last one that can still be made.
	for _, name := range []string{"b", ""} {
		dir := filepath.Join(deepest, name)
		if name == "" {
			dir = filepath.Join(filepath.Dir(deepest), "b")
		}
		if err := os.Mkdir(dir, 0o755); err == nil {
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			return dir
		}
	}
	t.Fatal("no folder near the longest path can be made")
	return ""
}

// TestMakeCopyOfAVerifySessionComparesTheHeadOfTheCheckoutWithTheCommit: a copy session
// that read the HEAD of the checkout at the commit of the plan ends well, and one that
// read another id stops the review with both ids; a session of a plan with no verify reads
// no HEAD, so the copy session does not write the file.
func TestMakeCopyOfAVerifySessionComparesTheHeadOfTheCheckoutWithTheCommit(t *testing.T) {
	was := runGitContainer
	t.Cleanup(func() { runGitContainer = was })
	spec := &runSpec{}
	spec.RunID = "r1"

	run := func(verify bool, head string) error {
		p := copyPlan(t)
		p.verify = verify
		var args []string
		runGitContainer = func(_ context.Context, c gitContainer) (int, error) {
			args = c.Session.Args
			root := p.copy.Root()
			state := filepath.Join(root, "state")
			for _, d := range []string{state, p.copy.Commit()} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return -1, err
				}
			}
			for path, text := range map[string]string{
				filepath.Join(state, "done"): "ok\n", filepath.Join(state, "left-out"): "",
				filepath.Join(root, "commit.diff"): "diff\n", filepath.Join(state, "head"): head + "\n",
				filepath.Join(state, "changed"): "",
			} {
				if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
					return -1, err
				}
			}
			return 0, nil
		}
		err := p.makeCopy(t.Context(), copyRun{}, spec, &bytes.Buffer{})
		if got := args[len(args)-1] == "head"; got != verify {
			t.Errorf("verify %v: the copy session's last argument is %q", verify, args[len(args)-1])
		}
		return err
	}
	if err := run(true, headB); err != nil {
		t.Errorf("a checkout at the commit returned %v", err)
	}
	err := run(true, headA)
	if !hasStop(err, "The checkout's HEAD is not the commit the review reads, so the review did not start") ||
		!strings.Contains(err.Error(), headA) || !strings.Contains(err.Error(), headB) {
		t.Errorf("a checkout at another commit returned %v", err)
	}
	if err := run(false, headA); err != nil {
		t.Errorf("a review join, whose HEAD is not read, returned %v", err)
	}
}

// TestCheckHeadStopsWithTheCauseOfEachFailure is the second read of the checkout's HEAD of
// a Verify session: a HEAD at the commit passes, and the mounts, the name and the script
// of the container are those of the read of HEAD; another HEAD is a headMoved with both
// ids; a .git that is a link, a container that does not start, an exit with a code and a
// run that passes the limit each stop with their cause, and none is a headMoved.
func TestCheckHeadStopsWithTheCauseOfEachFailure(t *testing.T) {
	was := runGitContainer
	t.Cleanup(func() { runGitContainer = was })
	spec := &runSpec{}
	spec.RunID = "r1"
	const what = "the review did not end"

	// plan is a plan of a Verify session whose copy folder has the file state/head with head.
	plan := func(t *testing.T, head string) *reviewJoinPlan {
		t.Helper()
		p := copyPlan(t)
		p.verify, p.copyRun, p.copySpec = true, copyRun{}, spec
		state := filepath.Join(p.copy.Root(), "state")
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "head"), []byte(head+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// writeHead is the file that the script of the container writes.
	writeHead := func(t *testing.T, p *reviewJoinPlan, head string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(p.copy.Root(), "state", "head"), []byte(head+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a HEAD at the commit", func(t *testing.T) {
		p := plan(t, headA)
		runGitContainer = func(_ context.Context, c gitContainer) (int, error) {
			writeHead(t, p, headB)
			if len(c.Mounts) != 2 || !c.Mounts[0].ReadOnly || c.Mounts[0].Path != p.source || c.Mounts[1].ReadOnly || c.Mounts[1].Path != p.copy.Root() {
				t.Errorf("the container has the mounts %+v, want the source read-only and the copy writable", c.Mounts)
			}
			if c.Name != "qory-review-head-r1" || c.Session.Command != "/bin/sh" || c.Session.Args[0] != "-c" {
				t.Errorf("the container is %q with %s %q", c.Name, c.Session.Command, c.Session.Args[:1])
			}
			return 0, nil
		}
		if err := p.checkHead(t.Context(), what); err != nil {
			t.Errorf("a HEAD at the commit returned %v", err)
		}
	})
	t.Run("another HEAD", func(t *testing.T) {
		p := plan(t, headB)
		runGitContainer = func(context.Context, gitContainer) (int, error) { writeHead(t, p, headA); return 0, nil }
		err := p.checkHead(t.Context(), what)
		var moved headMoved
		if !errors.As(err, &moved) || !hasStop(err, "The checkout's HEAD is not the commit the review read, so the review did not end") ||
			!strings.Contains(err.Error(), strconv.Quote(headA)) || !strings.Contains(err.Error(), headB) {
			t.Errorf("another HEAD returned %v, want a headMoved with both commits", err)
		}
	})
	// A read that writes no file leaves no commit of an earlier read: the file that the plan
	// has, at the commit of the review, is removed before the container runs.
	t.Run("a read that writes no HEAD", func(t *testing.T) {
		p := plan(t, headB)
		runGitContainer = func(context.Context, gitContainer) (int, error) { return 0, nil }
		err := p.checkHead(t.Context(), what)
		var moved headMoved
		if err == nil || errors.As(err, &moved) {
			t.Errorf("a read that wrote no HEAD returned %v, want a stop that is no headMoved", err)
		}
		// The words after "so" are those of the caller, at the end of a session and at a
		// check of a verdict, not those of the start of the review.
		for _, at := range []string{"the review did not end", "the review did not start"} {
			err := p.checkHead(t.Context(), at)
			if !hasStop(err, "The commit of the checkout's HEAD could not be read, so "+at) {
				t.Errorf("a read that wrote no HEAD, for %q, returned %v", at, err)
			}
		}
	})
	for _, c := range []struct {
		name string
		run  func(context.Context, gitContainer) (int, error)
		want string
	}{
		{"a container that does not start", func(context.Context, gitContainer) (int, error) { return -1, errors.New("no engine") },
			"The container that reads the checkout's HEAD did not start, so the review did not end"},
		{"an exit with a code", func(_ context.Context, c gitContainer) (int, error) {
			fmt.Fprintln(c.Output, "qory review copy: the checkout's .git has a commondir file")
			return 3, nil
		}, "The checkout's .git has a commondir file, so the review did not end\n  Reading the checkout's HEAD exited with 3"},
		{"an exit with a code and a line of another program", func(_ context.Context, c gitContainer) (int, error) {
			fmt.Fprintln(c.Output, "sh: out of memory")
			return 137, nil
		}, "Reading the checkout's HEAD exited with 137, so the review did not end\n  sh: out of memory"},
		{"the limit", func(ctx context.Context, _ gitContainer) (int, error) {
			<-ctx.Done()
			return -1, ctx.Err()
		}, "Reading the checkout's HEAD passed its limit of 10ms, so the review did not end"},
	} {
		t.Run(c.name, func(t *testing.T) {
			limit := headCheckLimit
			headCheckLimit = 10 * time.Millisecond
			t.Cleanup(func() { headCheckLimit = limit })
			p := plan(t, headB)
			runGitContainer = c.run
			err := p.checkHead(t.Context(), what)
			var moved headMoved
			// A want with a line end is compared line by line.
			has := hasStop(err, c.want)
			if strings.Contains(c.want, "\n") {
				has = err != nil && strings.HasPrefix(err.Error(), c.want)
			}
			if !has || errors.As(err, &moved) {
				t.Errorf("%s returned %v, want the stop %q", c.name, err, c.want)
			}
		})
	}
	// A path that the session cannot take stops with the words of the caller too.
	t.Run("a git folder with a line end in its path", func(t *testing.T) {
		p := plan(t, headB)
		p.source = filepath.Join(t.TempDir(), "a\nb")
		if err := os.Mkdir(p.source, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitContainer = func(context.Context, gitContainer) (int, error) {
			t.Error("the container started for a path that is not clean")
			return 0, nil
		}
		if err := p.checkHead(t.Context(), what); !hasStop(err, "The git folder of the review's copy is not an absolute, clean path, so the review did not end") {
			t.Errorf("a git folder with a line end in its path returned %v", err)
		}
	})
	t.Run("a .git that is a link", func(t *testing.T) {
		p := plan(t, headB)
		if err := os.Remove(p.source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), p.source); err != nil {
			t.Fatal(err)
		}
		runGitContainer = func(context.Context, gitContainer) (int, error) {
			t.Error("the container started for a .git that is a link")
			return 0, nil
		}
		if err := p.checkHead(t.Context(), what); !hasStop(err, "The checkout's .git is not a folder any more, so the review did not end") {
			t.Errorf("a .git that is a link returned %v", err)
		}
	})
}

// TestCheckHeadReadsOneAtATime: two calls of checkHead at once run their containers one
// after the other, since the container has one name and each read replaces state/head.
func TestCheckHeadReadsOneAtATime(t *testing.T) {
	was := runGitContainer
	t.Cleanup(func() { runGitContainer = was })
	spec := &runSpec{}
	spec.RunID = "r1"
	p := copyPlan(t)
	p.verify, p.copyRun, p.copySpec = true, copyRun{}, spec
	if err := os.MkdirAll(filepath.Join(p.copy.Root(), "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	var running, most, calls atomic.Int32
	runGitContainer = func(context.Context, gitContainer) (int, error) {
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		if err := os.WriteFile(filepath.Join(p.copy.Root(), "state", "head"), []byte(headB+"\n"), 0o644); err != nil {
			t.Error(err)
		}
		return 0, nil
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.checkHead(t.Context(), "the review did not end"); err != nil {
				t.Errorf("a read at the same time returned %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 || most.Load() != 1 {
		t.Errorf("%d containers ran, at most %d at once, want 2 and 1", calls.Load(), most.Load())
	}
}

// TestCheckHeadStopsWaitingWhenItsContextEnds: a call of checkHead that waits for another
// read returns the error of its context when the context ends, and leaves the other read
// the lock.
func TestCheckHeadStopsWaitingWhenItsContextEnds(t *testing.T) {
	p := &reviewJoinPlan{}
	if err := p.headLock.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.checkHead(ctx, "the review did not end") }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a call that waited returned %v, want %v", err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call that waits for another read did not return when its context ended")
	}
	// The lock stays taken by the first read, and a second acquire waits for it.
	waiting, stop := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer stop()
	if err := p.headLock.acquire(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second acquire returned %v, want %v", err, context.DeadlineExceeded)
	}
	p.headLock.release()
	if err := p.headLock.acquire(t.Context()); err != nil {
		t.Errorf("an acquire after the release returned %v", err)
	}
}

// TestHeadReadStopCapitalisesTheReasonOfTheScript: line 1 is the reason that the script
// wrote, with a capital first letter, also for a letter of more than one byte, and the
// exit code is line 2; a line with no reason, or with no reason after the prefix, follows
// the exit code.
func TestHeadReadStopCapitalisesTheReasonOfTheScript(t *testing.T) {
	const what = "the review did not end"
	for _, c := range []struct{ name, text, want string }{
		{"a reason", "qory review copy: the checkout's .git has a commondir file", "The checkout's .git has a commondir file, so the review did not end\n  Reading the checkout's HEAD exited with 3"},
		{"a reason with a letter of two bytes", "qory review copy: élan of the script", "Élan of the script, so the review did not end\n  Reading the checkout's HEAD exited with 3"},
		{"a reason that starts with a digit", "qory review copy: 3 files", "3 files, so the review did not end\n  Reading the checkout's HEAD exited with 3"},
		{"the prefix with no reason", "qory review copy: ", "Reading the checkout's HEAD exited with 3, so the review did not end\n  qory review copy: "},
		{"an empty line", "", "Reading the checkout's HEAD exited with 3, so the review did not end"},
		{"a line of another program", "sh: out of memory", "Reading the checkout's HEAD exited with 3, so the review did not end\n  sh: out of memory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := headReadStop(3, c.text, what); err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("headReadStop returned %q, want %q", err, c.want)
			}
		})
	}
}

func TestCheckWallUserStopsAnotherUidAndKeepsAnyGid(t *testing.T) {
	defer func(before func() int) { qoryUID = before }(qoryUID)
	qoryUID = func() int { return 501 }
	for _, c := range []struct {
		user string
		stop bool
	}{
		{"", false}, {"501", false}, {"501:501", false}, {"501:0", false}, {"501:20", false},
		{"1000", true}, {"1000:501", true}, {"0:0", true}, {"qory", true}, {"501:", true},
		{"501:qory", true}, {"-501", true},
	} {
		err := checkWallUser(&runSpec{OpenSpec: session.OpenSpec{Wall: &wall.Docker{User: c.user}}})
		if (err != nil) != c.stop {
			t.Errorf("wall.user %q: error %v, want a stop: %v", c.user, err, c.stop)
		}
		if err != nil {
			want := "The wall's user is not qory's user, so the review did not start"
			if first, _, _ := strings.Cut(err.Error(), "\n"); !strings.HasPrefix(first, want) {
				t.Errorf("wall.user %q: line 1 is %q, want it to start with %q", c.user, first, want)
			}
		}
	}
	if err := checkWallUser(&runSpec{}); err != nil {
		t.Errorf("a run with no Docker wall: %v", err)
	}
}
