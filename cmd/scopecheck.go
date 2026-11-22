package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
)

// The texts of a push that the scope check of the session's plan stops, see
// [runPushChecks.scopeCheck]: commits outside the plan, commits of a workflow with no
// plan, a check past its limit, a check that ended with an error, and one that qory could
// not run.
const (
	textScopeOutside  = "The commits do not pass the scope check of the session's plan, so qory pushed nothing.\n  Repository %s, plan type %s, commits %s to %s."
	textScopeNoPlan   = "This workflow has no plan, so its commits may change no file; qory pushed nothing.\n  Workflow %s. Repository %s. Commit %s changes %s."
	textScopeTimedOut = "The scope check of the session's plan did not end within 30 seconds; qory pushed nothing.\n  Repository %s."
	textScopeError    = "The scope check of the session's plan ended with an error, so qory pushed nothing.\n  Repository %s.\n  Cause: %s"
	textScopeNotRun   = "qory could not run the scope check of the session's plan, so it pushed nothing.\n  Repository %s.\n  Cause: %s"
)

// scopeChecksDir is the folder of the run directory where the scope checks' clones are
// made, a folder that no mount reaches.
const scopeChecksDir = "scope-checks"

// loadScopeChecks reads the scope checks of a home, scopeClone makes the fresh clone of a
// scope check, and scopeCheckLimit is how long the check may run, 0 for
// [pushcheck.Limit]. Tests replace them.
var (
	loadScopeChecks = pushcheck.LoadScope
	scopeClone      = preparation.ScopeClone
	scopeCheckLimit time.Duration
)

// runScope is what the push step's scope check needs: the checks of the home by plan
// type, or the error of their read, the checker that runs them, nil for a home with none,
// and the session's workflow, plan type and launch snapshot, see [startArtifacts].
type runScope struct {
	runDir                   string
	checks                   map[string]compose.ScopeCheck
	unread                   error
	checker                  *pushcheck.Checker
	workflow, plan, snapshot string
}

// scopeStop is the error of a push that the scope check stops, with its end code.
type scopeStop struct {
	code contracts.EndCode
	text string
}

func (e *scopeStop) Error() string { return e.text }

// EndCode is the end code of the stop.
func (e *scopeStop) EndCode() contracts.EndCode { return e.code }

// loadScope reads the scope checks of home, the home qory run composed for a session of
// run, and, when pushes is true and the home has scope checks, makes their checker once
// for the run, as [newChecker] makes one, with no push check: a run whose engine cannot
// run them stops here, as for push checks. A later session's home updates the checker's.
func (p *runPushChecks) loadScope(ctx context.Context, run openedRun, home, runID string, spec *runSpec, pushes bool) (done func(), err error) {
	s := &p.scope
	s.runDir = run.Dir()
	s.checks, s.unread = loadScopeChecks(home)
	if !pushes || len(s.checks) == 0 {
		return func() {}, nil
	}
	if s.checker != nil {
		s.checker.Home = home
		return func() {}, nil
	}
	checker, err := newChecker(ctx, run, home, runID, spec, p.fromCode, nil, filepath.Join(run.Dir(), scopeChecksDir))
	if err != nil {
		return func() {}, err
	}
	checker.Limit = scopeCheckLimit
	s.checker = checker
	return func() { os.RemoveAll(checker.Dir) }, nil
}

// setSession sets the workflow, the plan type, "" for none, and the launch snapshot of the
// session that the next push follows.
func (p *runPushChecks) setSession(workflow, plan, snapshot string) {
	p.scope.workflow, p.scope.plan, p.scope.snapshot = workflow, plan, snapshot
}

// scopeCheck is the check of each repository of branch, in dir, the working folder, whose
// head in version, the version that the push leaves, is past its
// [preparation.WorkRepository.Commit], where the session started. Each runs on a fresh
// clone, see [preparation.ScopeClone]: for a workflow with no plan, a commit may change no
// path outside .qory/; otherwise the scope check that the home declares for the plan type
// runs with --base at the session's start. The first failure is the error, a [*scopeStop].
func (p *runPushChecks) scopeCheck(ctx context.Context, branch *preparation.WorkBranch, dir string, version []preparation.Repository) error {
	for _, r := range branch.Repositories {
		head := headOf(version, r.Path)
		if head == "" || head == r.Commit {
			continue
		}
		if err := p.scope.check(ctx, filepath.Join(dir, filepath.FromSlash(r.Dir)), r, head); err != nil {
			return err
		}
	}
	return nil
}

// headOf is the commit of the repository path in version, "" for none.
func headOf(version []preparation.Repository, path string) string {
	for _, v := range version {
		if preparation.SameRepository(v.System, v.Path, path) {
			return v.Commit
		}
	}
	return ""
}

// check is the scope check of r, the repository in folder, at head, in a new folder that
// it removes after.
func (s *runScope) check(ctx context.Context, folder string, r preparation.WorkRepository, head string) error {
	repo := preparation.ShownPath(r.Path)
	stop := func(code contracts.EndCode, text string, args ...any) error {
		return &scopeStop{code: code, text: fmt.Sprintf(text, args...)}
	}
	notRun := func(code contracts.EndCode, cause string) error {
		return stop(code, textScopeNotRun, repo, cause)
	}
	parent := s.runDir
	if s.checker != nil {
		parent = s.checker.Dir
	}
	if parent == "" {
		return notRun(contracts.EndCodeMachineSetupInvalid, "the run has no folder for the clone of the commits.")
	}
	work, err := os.MkdirTemp(parent, "scope-")
	if err != nil {
		return notRun(contracts.EndCodeMachineSetupInvalid, sentence("making the folder of the clone failed: "+shownError(exports.HideError(err))))
	}
	defer os.RemoveAll(work)
	clone := filepath.Join(work, "clone")
	if err := scopeClone(ctx, os.Environ(), folder, clone, r, head); err != nil {
		return notRun(contracts.EndCodeRepositoryCheckFailed, sentence(err.Error()))
	}
	if s.plan == "" {
		commit, path, err := preparation.ChangedPath(ctx, os.Environ(), clone, r.Commit, head)
		if err != nil {
			return notRun(contracts.EndCodeRepositoryCheckFailed, sentence(err.Error()))
		}
		if path != "" {
			return stop(contracts.EndCodePushScopeRefused, textScopeNoPlan, exports.Value(s.workflow, false), repo, shortCommit(commit), preparation.ShownPath(path))
		}
		return nil
	}
	command, err := s.command()
	if err != nil {
		return notRun(contracts.EndCodeHarnessInvalid, err.Error())
	}
	if s.checker == nil || s.snapshot == "" {
		return notRun(contracts.EndCodeMachineSetupInvalid, "the run has no container or launch snapshot for the check.")
	}
	snapshot := filepath.Join(work, "artifacts")
	if err := copyTree(s.snapshot, snapshot); err != nil {
		return notRun(contracts.EndCodeMachineSetupInvalid, sentence("copying the launch snapshot failed: "+shownError(exports.HideError(err))))
	}
	user := s.checker.ScopeUser()
	if os.Getuid() == 0 {
		if err := pushcheck.Chown(user, clone, snapshot); err != nil {
			return notRun(contracts.EndCodeMachineSetupInvalid, sentence(err.Error()))
		}
	}
	end, err := s.checker.Scope(ctx, command, clone, snapshot, r.Commit, user)
	switch {
	case err != nil:
		return notRun(contracts.EndCodeMachineSetupInvalid, sentence(shownError(err)))
	case end.NotStarted != "":
		return notRun(contracts.EndCodeMachineSetupInvalid, sentence(end.NotStarted))
	case end.TimedOut:
		return stop(contracts.EndCodePushScopeRefused, textScopeTimedOut, repo)
	case end.ExitCode == 0:
		return nil
	case end.ExitCode == 1:
		// shortcut: a key that the check finds ends push_scope_refused, not push_key_found;
		// replace it when the push step tells a key apart from a path outside the plan.
		text := fmt.Sprintf(textScopeOutside, repo, exports.Value(s.plan, false), shortCommit(r.Commit), shortCommit(head))
		for _, line := range end.Lines {
			text += "\n  " + line
		}
		return &scopeStop{code: contracts.EndCodePushScopeRefused, text: text}
	}
	// A check that printed on stderr alone has a first line that says so; the cause keeps
	// the next two lines with it.
	return stop(contracts.EndCodeHarnessInvalid, textScopeError, repo, strings.Join(end.Lines[:min(3, len(end.Lines))], " "))
}

// command is the scope check that the home declares for the session's plan type; an error,
// one sentence, for a home whose scope checks did not read or that has none for the type.
func (s *runScope) command() ([]string, error) {
	switch c, ok := s.checks[s.plan]; {
	case s.unread != nil:
		return nil, errors.New(sentence(shownError(s.unread)))
	case s.checks == nil:
		return nil, errors.New("the home has no scope checks, so no module declares one for the plan type " + exports.Value(s.plan, false) + ".")
	case !ok:
		return nil, errors.New("no module of the harness declares a scope check for the plan type " + exports.Value(s.plan, false) + ".")
	default:
		return c.Command, nil
	}
}

// sentence is cause with one full stop at its end.
func sentence(cause string) string {
	return strings.TrimSuffix(cause, ".") + "."
}
