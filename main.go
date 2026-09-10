// Command qory composes the harness a runtime loads from layers.
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
	if err := cmd.Execute(); err != nil {
		if !errors.Is(err, cmd.ErrReported) {
			u := ui.New(os.Stderr)
			if !ui.Marked() {
				u.Title("qory")
			}
			u.Fail(err)
		}
		os.Exit(cmd.ExitCode(err))
	}
}
