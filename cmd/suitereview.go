package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/suite"
)

// The errors of [suiteRun.forReview]; each starts with its fix, and its cause is on the next line.
const (
	textReviewNoSuite  = "Start the run behind the docker wall, with a build plan that has a full row: it has no suite.\n  %s"
	textReviewNoResult = "Check the folder of review.suite_result, then run the stage again.\n  Writing the suite's result to %s failed: %s"
)

// errNoWall is why a run has no suite: it has no wall.
var errNoWall = errors.New("the run has no wall")

// forReview is the full suite of a stage with reviewers: it starts the suite on commit as a
// request of a session would, or takes the suite of that commit that a request started, waits
// for it to end, and writes the body that GET /v1/suites/<id> returns to resultFile, the file at
// review.suite_result in the run's inputs. It posts no verdict. A suite that the end
// of ctx stops is in the file as stopped, and ctx's error is not an error here. The caller, a
// stage with reviewers, decides what a red suite means.
func (r *suiteRun) forReview(ctx context.Context, commit, resultFile string) (suite.Snapshot, error) {
	switch {
	case r == nil:
		return suite.Snapshot{}, fmt.Errorf(textReviewNoSuite, errNoWall)
	case r.reg == nil:
		return suite.Snapshot{}, fmt.Errorf(textReviewNoSuite, shownError(r.none))
	case !suite.ValidCommit(commit):
		return suite.Snapshot{}, fmt.Errorf(textReviewNoSuite, "the commit is not a full commit id")
	}
	if _, _, err := r.reg.Start(ctx, commit); err != nil {
		return suite.Snapshot{}, fmt.Errorf(textReviewNoSuite, shownError(err))
	}
	snap, _, err := r.reg.Wait(ctx)
	if err != nil {
		r.reg.Stop()
		snap, _ = r.reg.Newest()
	}
	body, err := json.Marshal(suiteBody(snap))
	if err != nil {
		return snap, err
	}
	if err := writeSuiteResult(resultFile, append(body, '\n')); err != nil {
		return snap, fmt.Errorf(textReviewNoResult, shownError(errors.New(resultFile)), shownError(err))
	}
	return snap, nil
}

// writeSuiteResult writes data to file, mode 0644: whole or not at all, through a new file in
// the same folder.
func writeSuiteResult(file string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".suite-result-")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
