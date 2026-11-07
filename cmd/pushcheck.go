package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
)

// textNotChecked is the cause in [textNotStored] of a version of the push step that did
// not pass its push check.
const textNotChecked = "the version did not pass the check for %s"

// The texts of qory run for its push checks. The load's errors start with their fix;
// textCheckNotFinished is the status line of a push whose check returned an error.
const (
	textChecksNoWall     = "Set wall.adapter to docker, and run again. The push checks of %s run in containers of the docker wall alone."
	textChecksNoRunAPI   = "Run again. The run API, through which the push checks read the artifacts, failed: %s."
	textChecksNoFolder   = "Check %s, and run again. Making the folder of the push checks' files failed: %s."
	textChecksNotSet     = "Run again. Turning on the push checks for the POSTs from behind the wall failed, so the run stopped before the setup: %s."
	textCheckNotFinished = "the check before a push did not finish, and the push was not stored (503 " + runapi.CodePushCheckUnavailable + "): %s"
)

// The folder in the run directory that holds the temporary folders of the run's push
// checks, and the label, NAME=value, that the wall removes the run's containers by,
// followed by the run's id, as Forager's wall has it.
const (
	pushChecksDir = "push-checks"
	runLabel      = "dev.qory.run="
)

// engineSelectWait is how long the selection of the push checks' engine may take.
const engineSelectWait = 30 * time.Second

// loadPushChecks reads the push checks of a home; tests replace it.
var loadPushChecks = pushcheck.Load

// runPushChecks is the push checks of a walled run, from the home qory run composed for
// it: [runPushChecks.runAPICheck] checks each POST of the run API from behind the wall,
// once [runPushChecks.set] turned it on, and [runPushChecks.check] the push step's
// version. In a run whose home has no push check, nothing is checked.
type runPushChecks struct {
	mu      sync.Mutex
	checker *pushcheck.Checker
	// report writes one status line of qory run; nil writes none.
	report func(string)
}

// current is the checker, nil before the load or for a home with no push check.
func (p *runPushChecks) current() *pushcheck.Checker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checker
}

// set turns on the check of each POST of the run API from behind the wall of run, with
// [runPushChecks.runAPICheck], when the load found push checks; with none, it sets no
// check. Call it after the load and before the run's setup and join.
func (p *runPushChecks) set(run openedRun) error {
	if p.current() == nil {
		return nil
	}
	if err := run.SetRunAPICheck(p.runAPICheck); err != nil {
		return fmt.Errorf(textChecksNotSet, strings.TrimSuffix(shownError(err), "."))
	}
	return nil
}

// runAPICheck is [pushcheck.Checker.RunAPICheck] of the run's checker, for
// [session.Run.SetRunAPICheck]. Forager writes no line for its error, so qory run
// writes it as a status line, masked.
func (p *runPushChecks) runAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	c := p.current()
	if c == nil {
		return nil, nil
	}
	refusal, err := c.RunAPICheck(ctx, req)
	if err != nil && p.report != nil {
		p.report(secretmask.Default().Replace(fmt.Sprintf(textCheckNotFinished, shownError(err))))
	}
	return refusal, err
}

// check is [pushcheck.Checker.Check] of the run's checker.
func (p *runPushChecks) check(ctx context.Context, artifactType string, files []pushcheck.File) (*pushcheck.Failure, error) {
	c := p.current()
	if c == nil {
		return nil, nil
	}
	return c.Check(ctx, artifactType, files)
}

// load reads the push checks of home, the home qory run composed for the run runID,
// which the wall of spec mounts read-only at that path. The checks run in a container
// from the run's image on the engine of the wall, as the run's user, with the wall's
// limits and the run's label, and read the artifacts through the run's run API on this
// machine. Their temporary folders are in a folder of the run directory, which done
// removes. A home with no push check, and a run with no control plane, load nothing. An
// error starts with its fix.
func (p *runPushChecks) load(ctx context.Context, run openedRun, home, runID string, spec *runSpec) (done func(), err error) {
	checker, err := pushChecker(ctx, run, home, runID, spec)
	if err != nil || checker == nil {
		return func() {}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checker = checker
	return func() { os.RemoveAll(checker.Dir) }, nil
}

// pushChecker is the checker of [runPushChecks.load], nil for a home with no push check
// and for a run with no control plane.
func pushChecker(ctx context.Context, run openedRun, home, runID string, spec *runSpec) (*pushcheck.Checker, error) {
	checks, err := loadPushChecks(home)
	if err != nil || len(checks) == 0 {
		return nil, err
	}
	d, ok := spec.Wall.(*wall.Docker)
	if !ok {
		return nil, fmt.Errorf(textChecksNoWall, exports.Value(home, false))
	}
	// Forager's run is open here, so the run API fails only for a run with no control
	// plane, which takes no push: such a run checks nothing.
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, nil
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, fmt.Errorf(textChecksNoRunAPI, strings.TrimSuffix(shownError(err), "."))
	}
	user := d.User
	if user == "" {
		// The wall's default: the uid:gid of this process.
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	checker := &pushcheck.Checker{
		Home: home, Checks: checks, Image: run.Image().Ref, User: user,
		Engine: checkEngine(ctx, d),
		Fetch:  artifactsForCheck(client),
		Dir:    filepath.Join(run.Dir(), pushChecksDir),
		Limits: wall.Limits{CPUs: spec.Limits.CPUs, Memory: spec.Limits.Memory, PIDs: spec.Limits.PIDs},
		Label:  runLabel + runID,
		Mask:   secretmask.Default().Replace,
	}
	// The folder is qory's user's alone. For a run's user that is another user, its mode
	// lets that user pass through to the folders of a check, which it cannot list.
	mode := fs.FileMode(0o700)
	if uid, _, _ := strings.Cut(user, ":"); uid != strconv.Itoa(os.Getuid()) {
		mode = 0o711
	}
	if err := os.Mkdir(checker.Dir, 0o700); err != nil {
		return nil, fmt.Errorf(textChecksNoFolder, exports.Value(checker.Dir, false), strings.TrimSuffix(shownError(exports.HideError(err)), "."))
	}
	if err := os.Chmod(checker.Dir, mode); err != nil {
		os.RemoveAll(checker.Dir)
		return nil, fmt.Errorf(textChecksNoFolder, exports.Value(checker.Dir, false), strings.TrimSuffix(shownError(exports.HideError(err)), "."))
	}
	return checker, nil
}

// checkEngine is the engine of the push checks of a run behind d: d's command, with the
// selection of the engine that the wall recorded at the run's open, as a new
// [wall.Docker.Engine] reads it, when the engine it reaches has the id of the engine of
// that selection. Otherwise, such as for an engine with no id, it is d's command with
// this process's environment.
func checkEngine(ctx context.Context, d *wall.Docker) pushcheck.Engine {
	ctx, cancel := context.WithTimeout(ctx, engineSelectWait)
	defer cancel()
	e := pushcheck.Engine{Command: d.Command}
	id, err := d.EngineID(ctx)
	if err != nil || id == "" {
		return e
	}
	selected := (&wall.Docker{Command: d.Command}).Engine(ctx)
	if selected.ID != id {
		return e
	}
	return pushcheck.Engine{Command: selected.Command, Env: append([]string{}, selected.Env...)}
}

// artifactsForCheck is the fetch of a push check through client: the artifact index,
// and the newest version of each artifact of the types, as qory artifact pull writes
// them into its store.
func artifactsForCheck(client *runapi.Client) pushcheck.Fetch {
	return func(ctx context.Context, dir string, types []string) error {
		index, err := client.Artifacts(ctx)
		if err != nil {
			return err
		}
		store := &artifactStore{root: filepath.Dir(dir), dir: dir, fresh: true}
		for _, artifact := range index.Artifacts {
			if !slices.Contains(types, artifact.Type) || artifact.NewestVersion < 1 {
				continue
			}
			chosen := chosenVersion(artifact, nil, artifact.NewestVersion)
			if chosen == nil {
				continue
			}
			if err := store.fetch(ctx, client, artifact, *chosen); err != nil {
				return err
			}
		}
		return store.writeIndex(index.Raw)
	}
}

// checkBranchVersion runs the push check of the artifact type branch on content, the
// file of the push step's version of branch at ref, before the step stores it. A check
// that does not pass is the step's error: the step's fix line, then the check's line and
// its findings, each a line of qory run. So is a check that could not run.
func checkBranchVersion(ctx context.Context, checks *runPushChecks, branch *preparation.WorkBranch, ref string, content []byte) error {
	failed, err := checks.check(ctx, branchType, []pushcheck.File{{Name: branchFile, Content: content}})
	if err != nil {
		return fmt.Errorf(textNotStored, branch.VersionWays(), ref, shownError(err))
	}
	if failed == nil {
		return nil
	}
	lines := []string{fmt.Sprintf(textNotStored, branch.VersionWays(), ref, fmt.Sprintf(textNotChecked, branchType)), failed.Step}
	lines = append(lines, failed.Findings...)
	return errors.New(strings.Join(lines, "\nqory run: "))
}
