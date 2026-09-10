// Command gendocs writes the command reference, one Markdown page per command, into the
// directory given as its argument.
//
// The reference is checked in under docs/commands. Regenerate it after changing a command
// and commit the result:
//
//	go run ./internal/gendocs docs/commands
//
// CI runs gendocs into a scratch directory and diffs the output against docs/commands, so
// a page that no longer matches its command fails the build.
//
// The pages come from cobra's Markdown generator over the tree [cmd.Root] builds. The
// generator's timestamp line is turned off, so regenerating an unchanged tree writes the
// same bytes and the diff stays empty. gendocs creates the directory when it is missing
// and overwrites the pages of the commands it generates, leaving any other file in there
// alone. It exits 2 when it is not given exactly one argument, and 1 when it cannot
// create the directory or write a page.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/qoryai/qory/cmd"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gendocs <directory>")
		os.Exit(2)
	}
	root := cmd.Root()
	root.DisableAutoGenTag = true
	if err := os.MkdirAll(os.Args[1], 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := doc.GenMarkdownTree(root, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
