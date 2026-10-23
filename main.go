// Command qory composes the harness a runtime loads from modules.
package main

import (
	"embed"
	"errors"
	"os"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
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

func main() {
	// First, before qory reads its environment, a configuration or a key: a core dump
	// or a debugger that attaches later could read what this process holds by then.
	if err := hardenProcess(); err != nil {
		fail(err)
		os.Exit(cmd.ExitCode(err))
	}
	cmd.Example = example
	notify := cmd.StartUpdateCheck(os.Stderr, os.Args[1:])
	err := cmd.Execute()
	if err != nil && !errors.Is(err, cmd.ErrReported) {
		fail(err)
	}
	notify()
	if err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}

// fail prints err on stderr, under qory's title unless a command printed one.
func fail(err error) {
	u := ui.New(os.Stderr)
	if !ui.Marked() {
		u.Title("qory")
	}
	u.Fail(err)
}
