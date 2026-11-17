package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/suite"
)

// The lines about the file of review.suite_result; each starts with its fix.
const (
	textSuiteResultPath = "Set review.suite_result in the run's inputs to a clean absolute path: qory writes no result.\n  The path is %q."
	textSuiteNoResult   = "Check the folder of review.suite_result, then run the stage again.\n  Writing the suite's result to %s failed: %s"
)

// stateNotRun is the state of a result for a session whose after step did not run.
const stateNotRun = "did_not_run"

// errResultFolder is why qory does not write the result file: its folder is no real folder.
var errResultFolder = errors.New("the folder of the file is no folder, or a link")

// suiteResultFile is the path that review.suite_result has in the run's inputs, "" when the
// inputs have none. An error is a path that is not a clean absolute path: qory writes nothing
// there.
func suiteResultFile(inputs []byte) (string, error) {
	if len(inputs) == 0 {
		return "", nil
	}
	var doc struct {
		Review struct {
			SuiteResult *string `json:"suite_result"`
		} `json:"review"`
	}
	if json.Unmarshal(inputs, &doc) != nil || doc.Review.SuiteResult == nil {
		return "", nil
	}
	file := *doc.Review.SuiteResult
	if !filepath.IsAbs(file) || filepath.Clean(file) != file || strings.ContainsRune(file, 0) {
		return "", fmt.Errorf(textSuiteResultPath, integration.PrintableWithin(file, 80))
	}
	return file, nil
}

// suiteResult is the file's content: the commit the suite ran on, its state, the counts or
// null, the minutes the command took or null, and the last 64 KiB of its output with the run's
// secrets masked.
type suiteResult struct {
	Commit  string        `json:"commit"`
	State   string        `json:"state"`
	Counts  *suite.Counts `json:"counts"`
	Minutes *int          `json:"minutes"`
	Output  string        `json:"output"`
}

// resultOf is the file's content for the suite got, or for a session whose after step did not
// run when ran is false. The cap is the minutes of a suite that the limit cut.
func resultOf(got suite.Outcome, ran bool, capMinutes int) suiteResult {
	if !ran {
		return suiteResult{State: stateNotRun}
	}
	res := suiteResult{Commit: got.Commit, State: string(got.State), Counts: got.Counts, Output: secretmask.Default().Replace(got.Output)}
	switch {
	case got.State == suite.Timeout:
		res.Minutes = &capMinutes
	case got.Seconds >= 0:
		m := max(1, (got.Seconds+30)/60)
		res.Minutes = &m
	}
	return res
}

// writeResult writes the result of the suite to the file of review.suite_result, before the verdict
// goes out; with no such file it writes nothing. A failure is a line of qory run: the reviewers
// find no file.
func (r *suiteRun) writeResult(got suite.Outcome, ran bool) {
	if r.resultFile == "" {
		return
	}
	body, err := json.Marshal(resultOf(got, ran, r.minutes))
	if err == nil {
		err = writeSuiteResult(r.resultFile, append(body, '\n'))
	}
	if err != nil {
		r.said(fmt.Sprintf(textSuiteNoResult, r.resultFile, shownError(err)))
	}
}

// writeSuiteResult writes data to file, mode 0644: whole or not at all, through a new file in
// the same folder. The folder is a real folder, not a link, and a link at file is replaced, not
// followed.
func writeSuiteResult(file string, data []byte) error {
	folder := filepath.Dir(file)
	if info, err := os.Lstat(folder); err != nil {
		return err
	} else if !info.IsDir() {
		return errResultFolder
	}
	tmp, err := os.CreateTemp(folder, ".suite-result-")
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
