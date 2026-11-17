package cmd

import (
	"context"
	"io"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/suite"
)

// suiteHome is the HOME of the full suite's command: the user of the container has no home
// folder, and the commit's folder is the only place it may write besides /tmp.
const suiteHome = "/tmp"

// suiteRunner is the [suite.Runner] of a walled run: the suite's command runs as sh -c in a
// container of the run's image, built as [gitContainerFor] builds the git session's, with the
// suite's clone as its only bind. The container has the label of the run, so the wall's removal of
// what a run left finds it, and it runs through [runGitContainer], which removes it when ctx
// ends.
func suiteRunner(run openedRun, spec *runSpec, runID string) suite.Runner {
	return func(ctx context.Context, job suite.Job, out io.Writer) (int, error) {
		gs := runcopy.GitSession{Command: "sh", Args: []string{"-c", job.Full.Command}, Env: []string{"HOME=" + suiteHome}, Dir: job.Dir}
		c := gitContainerFor(run, spec, runID, gs, []wall.Mount{{Path: job.Dir}}, out)
		c.Name = "qory-suite-" + runID + "-" + job.ID
		return runGitContainer(ctx, c)
	}
}
