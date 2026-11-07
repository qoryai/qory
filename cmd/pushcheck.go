package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
)

// textNotChecked is the cause in [textNotStored] of a version of the push step that did
// not pass its push check.
const textNotChecked = "the version did not pass the check for %s"

// runPushChecks is the push checks of a walled run, from the home qory run composed for
// it: [runPushChecks.runAPICheck] checks each POST of the run API from behind the wall,
// and [runPushChecks.check] the push step's version. Until [runPushChecks.load] reads
// the home, and in a run whose home has no push check, nothing is checked. A home whose
// push checks do not read fails every check, so nothing passes unchecked.
type runPushChecks struct {
	mu      sync.Mutex
	checker *pushcheck.Checker
	err     error
}

// current is the checker and the error of the load.
func (p *runPushChecks) current() (*pushcheck.Checker, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checker, p.err
}

// runAPICheck is [pushcheck.Checker.RunAPICheck] of the run's checker, for
// [session.OpenSpec.RunAPICheck].
func (p *runPushChecks) runAPICheck(ctx context.Context, req session.RunAPIRequest) (*session.RunAPIRefusal, error) {
	c, err := p.current()
	if err != nil || c == nil {
		return nil, err
	}
	return c.RunAPICheck(ctx, req)
}

// check is [pushcheck.Checker.Check] of the run's checker.
func (p *runPushChecks) check(ctx context.Context, artifactType string, files []pushcheck.File) (*session.RunAPIRefusal, error) {
	c, err := p.current()
	if err != nil || c == nil {
		return nil, err
	}
	return c.Check(ctx, artifactType, files)
}

// load reads the push checks of home, the home qory run composed for run, which the wall
// w mounts read-only at that path. The checks run in a container from the run's image
// on the engine of w, as the run's user, and read the artifacts through the run's run
// API on this machine.
func (p *runPushChecks) load(run openedRun, home string, w wall.Wall) {
	checker, err := pushChecker(run, home, w)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checker, p.err = checker, err
}

// pushChecker is the checker of [runPushChecks.load], nil for a home with no push check.
func pushChecker(run openedRun, home string, w wall.Wall) (*pushcheck.Checker, error) {
	checks, err := pushcheck.Load(home)
	if err != nil || len(checks) == 0 {
		return nil, err
	}
	d, ok := w.(*wall.Docker)
	if !ok {
		return nil, fmt.Errorf("the run's wall is not the docker wall, which push checks run on")
	}
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return nil, err
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return nil, err
	}
	user := d.User
	if user == "" {
		// The wall's default: the uid:gid of this process.
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	return &pushcheck.Checker{
		Home: home, Checks: checks, Image: run.Image().Ref, User: user,
		Engine: pushcheck.Engine{Command: d.Command},
		Fetch:  artifactsForCheck(client),
		Mask:   secretmask.Default().Replace,
	}, nil
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
		store := &artifactStore{root: filepath.Dir(dir), dir: dir}
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
// that does not pass writes its fix line and its findings to w, as the step's lines, and
// is the step's error; so is a check that could not run.
func checkBranchVersion(ctx context.Context, checks *runPushChecks, w io.Writer, branch *preparation.WorkBranch, ref string, content []byte) error {
	refusal, err := checks.check(ctx, branchType, []pushcheck.File{{Name: branchFile, Content: content}})
	if err != nil {
		return fmt.Errorf(textNotStored, branch.VersionWays(), ref, shownError(err))
	}
	if refusal == nil {
		return nil
	}
	fmt.Fprintln(w, "qory run: "+refusal.Message)
	for _, finding := range refusal.Findings {
		fmt.Fprintln(w, "qory run: "+finding)
	}
	return fmt.Errorf(textNotStored, branch.VersionWays(), ref, fmt.Sprintf(textNotChecked, branchType))
}
