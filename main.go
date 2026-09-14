// Command qory composes the harness a runtime loads from modules.
package main

import (
	"embed"
	"errors"
	"os"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/ui"
)

// example is the hello example, carried in the binary so that qory harness init can write it.
//
//go:embed examples/hello
var example embed.FS

func main() {
	cmd.Example = example
	notify := cmd.StartUpdateCheck(os.Stderr)
	err := cmd.Execute()
	if err != nil && !errors.Is(err, cmd.ErrReported) {
		u := ui.New(os.Stderr)
		if !ui.Marked() {
			u.Title("qory")
		}
		u.Fail(err)
	}
	notify()
	if err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}
