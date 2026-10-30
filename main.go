// Command qory composes the harness a runtime loads from modules.
package main

import (
	"embed"
	"errors"
	"os"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/statusfd"
	"github.com/qoryai/qory/internal/ui"
)

// example is the hello example, carried in the binary so that qory setup example can write
// it. The qory-stack.yaml beside its qory.yaml is left out: a folder with both does not
// compose, and the stack file is there for a run that takes the folder by reference.
//
//go:embed examples/hello/qory.yaml examples/hello/README.md examples/hello/modules
var example embed.FS

// hardenProcess denies core dumps of this process and debugger attaches to it. A test
// replaces it.
var hardenProcess = gateway.HardenProcess

// startUpdateCheck starts the check for a newer release. A test replaces it.
var startUpdateCheck = cmd.StartUpdateCheck

func main() {
	// First, before qory reads its environment, a configuration or a key: a core dump
	// or a debugger that attaches later could read what this process holds by then.
	if err := hardenProcess(); err != nil {
		// The process exits: qory run's status descriptor gets the error line too.
		startStatus()
		fail(err)
		os.Exit(cmd.ExitCode(err))
	}
	// Next, before qory opens a file: a file that took the number of a status
	// descriptor that was never passed would look like that descriptor.
	startStatus()
	cmd.Example = example
	notify := startUpdateCheck(os.Stderr, os.Args[1:])
	err := cmd.Execute()
	if err != nil && !errors.Is(err, cmd.ErrReported) {
		fail(err)
	}
	notify()
	if err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}

// startStatus claims the status descriptor, see [statusfd.Claim], and for qory run starts
// the writes to it before cobra parses the flags, so every error line of qory run, an
// error of its flags and the last error line among them, reaches the descriptor.
func startStatus() {
	statusfd.Claim()
	if cmd.RunsQoryRun(os.Args[1:]) {
		statusfd.Setup(os.Stderr)
	}
}

// fail prints err on stderr, under qory's title unless a command printed one, with each
// value of the process's mask list, which qory run fills as it starts, replaced with ***.
func fail(err error) {
	u := ui.New(os.Stderr)
	if !ui.Marked() {
		u.Title("qory")
	}
	u.Fail(errors.New(secretmask.Default().Replace(err.Error())))
}
