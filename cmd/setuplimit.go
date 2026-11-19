package cmd

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// setupLimit is how long the setup of a run may take: every step from the run's open to
// its agent's start, such as the fetch of its repositories, the compose, the install of
// its runtime, the copy, the fetch of its image and the setup commands. It is fixed, and
// no setting changes it. Session 1's time limit counts from the agent's start, see
// [sessionDeadline], so the setup's time is not in it.
const setupLimit = 30 * time.Minute

// setupBound is [setupLimit]; a test sets a shorter one.
var setupBound = setupLimit

// prepStep is a step of a run's setup, as the line of a setup past [setupLimit] names it.
type prepStep int

// The steps of a run's setup, in the order they run.
const (
	stepNone prepStep = iota
	stepBranchRead
	stepFetch
	stepCompose
	stepInstall
	stepPushChecks
	stepSwitch
	stepCopy
	stepImage
	stepSetupCommands
	stepSetupRemoval
	stepSetupRecord
)

// setupSteps has each step's name in the line of a setup past its limit, and the fix that
// is line 1 of that line for a run that is not dispatched. Line 1 has at most 100
// characters with "✗ " or "qory run: ".
var setupSteps = map[prepStep]struct{ name, fix string }{
	stepNone:          {"the start of its setup", "start the run again"},
	stepBranchRead:    {"the read of the work item's branch", "check this machine's connection to the gateway, then start the run again"},
	stepFetch:         {"the fetch of the run's repositories", "check the gateway's connection to the repositories' host, then start the run again"},
	stepCompose:       {"the compose of the harness", "check the connection to the harness's git hosts, or use fewer or smaller modules"},
	stepInstall:       {"the install of the runtime", "set pool.registry in forager.yaml to a runtime cache, then start the run again"},
	stepPushChecks:    {"the load of the push checks", "check that this machine's container engine responds, then start the run again"},
	stepSwitch:        {"the switch to the work item's branch", "check the disk that holds the checkout, then start the run again"},
	stepCopy:          {"the copy of the checkout", "make the checkout smaller, with less history, Git LFS content and .qory/artifacts"},
	stepImage:         {"the fetch of the run's image", "pull the run's image on this machine before the run, or start fewer runs at once"},
	stepSetupCommands: {"the setup commands", "move slow work of the lifecycle commands in devcontainer.json into the run's image"},
	stepSetupRemoval:  {"the removal of the setup container", "check that this machine's container engine responds, then start the run again"},
	stepSetupRecord:   {"the record of what the setup changed", "make the setup commands change fewer files in the checkout, then start the run again"},
}

// installs reports whether the run's install step installs runtime, see [installRuntime].
func installs(runtime string) bool {
	return installRuntimes && slices.Contains(runtimeinstall.Runtimes(), runtime)
}

// setupPassed is the error of a run whose setup passed its limit, limit, in step, before
// runtime, "" when it is not known yet, started. Line 1 is the step's fix, and the cause
// names the step and then the runtime and the limit on the two lines after it, which
// [ui.UI.Fail] indents. A dispatched run shows the cause alone, with its first letter in
// lower case and no full stop, since [codeUsed] may follow: the fix advises a person who
// runs qory. The cause's first line has no runtime's name, so its length does not
// depend on one.
func setupPassed(step prepStep, runtime string, limit time.Duration, dispatched bool) error {
	s := setupSteps[step]
	cause := fmt.Sprintf("the run was cancelled during %s:\nthe steps before %s starts may take at most %s", s.name, agentName(runtime), limitText(limit))
	if dispatched {
		return errors.New(cause)
	}
	return errors.New(s.fix + "\n" + cause)
}

// limitText is d as a line says a limit: "30 minutes" for whole minutes, else d itself.
func limitText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}
