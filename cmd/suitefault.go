package cmd

import (
	"errors"
	"fmt"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
)

// suiteKind is how a suite that did not pass reaches the verdict.
type suiteKind int

const (
	// kindRed is a suite whose command ran and failed: the agent fixes the code.
	kindRed suiteKind = iota
	// kindFixable is a suite that did not start for a cause in the build plan the agent wrote.
	kindFixable
	// kindUnfixable is a suite that did not start or run for a cause outside the agent's reach.
	kindUnfixable
)

// suiteFault is why no suite runs after a session that qory set one for: the point of the
// verdict, and whether the agent can fix it.
type suiteFault struct {
	point   runapi.Point
	fixable bool
}

// kind is the suiteKind of the fault.
func (f *suiteFault) kind() suiteKind {
	if f.fixable {
		return kindFixable
	}
	return kindUnfixable
}

// The points of a suite that did not start before the after step was set. The text is line 1,
// the fix, when the agent can fix the cause, is the point's fix.
const (
	textFaultNoPlan   = "The full suite did not start: the work item has no usable build plan."
	textFixNoPlan     = "Write a build plan with a full row in Commands, then push it."
	textFaultTooBig   = "The full suite did not start: the build plan is larger than 1 MiB."
	textFixTooBig     = "Cut the build plan below 1 MiB, then push it."
	textFaultNoFull   = "The full suite did not start: %s has no full command."
	textFixNoTable    = "Add the table Commands with the columns Kind, Command, Result file and Source."
	textFixNoFull     = "Add a row of kind full to Commands in %s."
	textFaultNoCmd    = "The full suite did not start: the full row of the build plan has no command."
	textFixNoCmd      = "Write the command that runs the whole test suite in the full row."
	textFaultBadCmd   = "The full suite did not start: the command of the full row is too long or has a NUL."
	textFixBadCmd     = "Shorten the command, or move it into a script of the repository."
	textFaultBadFile  = "The full suite did not start: the result file of the full row is no path below the repository."
	textFixBadFile    = "Write a relative path below the repository as the Result file, or none."
	textFaultPlanRead = "The full suite did not start: qory could not read the build plan."
	textShownBadFile  = "The full suite did not start: the result file is no path below the repository."
	textFaultNoPlanIn = "The full suite did not start: the workflow %s names no plan in the stack."
	textFaultNoRunAPI = "The full suite did not start: qory cannot reach the run API."
	textFaultNoGit    = "The full suite did not start: the session's folder has no .git folder."
	textFaultBadStart = "The full suite did not start: qory could not read the commit the session started at."
	textPointNoAfter  = "The full suite did not run: the run closed before the after step."
)

// faultOf is the fault for err, an error of [launchFull] or of [suite.Command], for a workflow
// whose plan type is plan.
func faultOf(err error, plan string) *suiteFault {
	fixable := func(text, fix string) *suiteFault {
		return &suiteFault{point: runapi.Point{Text: text, Action: "run_again", Fix: fix}, fixable: true}
	}
	switch {
	case errors.Is(err, suite.ErrNoCommands):
		return fixable(fmt.Sprintf(textFaultNoFull, plan), textFixNoTable)
	case errors.Is(err, suite.ErrNoFull):
		return fixable(fmt.Sprintf(textFaultNoFull, plan), fmt.Sprintf(textFixNoFull, plan))
	case errors.Is(err, suite.ErrNoCommand):
		return fixable(textFaultNoCmd, textFixNoCmd)
	case errors.Is(err, suite.ErrBadCommand):
		return fixable(textFaultBadCmd, textFixBadCmd)
	case errors.Is(err, suite.ErrBadResultFile):
		return fixable(textFaultBadFile, textFixBadFile)
	case errors.Is(err, errBuildPlanTooBig):
		return fixable(textFaultTooBig, textFixTooBig)
	case err == errNoBuildPlan:
		return fixable(textFaultNoPlan, textFixNoPlan)
	case errors.Is(err, suite.ErrBadStart):
		return &suiteFault{point: runapi.Point{Text: textFaultBadStart, Action: "run_again"}}
	}
	return &suiteFault{point: runapi.Point{Text: textFaultPlanRead, Action: "run_again"}}
}

// line is the line of qory run for the fault: the fix first, then the cause on the next line; a
// fault with no fix is its cause alone.
func (f *suiteFault) line() string {
	if f.point.Fix == "" {
		return shownLine(f.point.Text)
	}
	return f.point.Fix + "\n  " + shownLine(f.point.Text)
}

// noRunAPIFault is the fault of a run whose run API cannot be reached at the launch.
func noRunAPIFault() *suiteFault {
	return &suiteFault{point: runapi.Point{Text: textFaultNoRunAPI, Action: "run_again"}}
}

// noPlanFault is the fault of a session whose workflow names no plan type in the stack, and whose
// HEAD moved: the agent cannot add a plan type to the stack.
func noPlanFault(workflow string) *suiteFault {
	return &suiteFault{point: runapi.Point{Text: fmt.Sprintf(textFaultNoPlanIn, workflow), Action: "run_again"}}
}

// noGitFault is the fault of a session's folder with no .git folder.
func noGitFault() *suiteFault {
	return &suiteFault{point: runapi.Point{Text: textFaultNoGit, Action: "run_again"}}
}

// noAfterFault is the fault of a session that succeeded while its after step did not run.
func noAfterFault() *suiteFault {
	return &suiteFault{point: runapi.Point{Text: textPointNoAfter, Action: "run_again"}}
}
