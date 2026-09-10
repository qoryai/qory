package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
	"github.com/qoryai/qory/internal/user"
)

// Example is the file tree qory harness init writes, rooted at ExampleRoot. The main
// package sets it from the files embedded in the binary.
var Example fs.FS

// ExampleRoot is the directory inside Example that holds the example.
const ExampleRoot = "examples/hello"

// newInit builds the init verb, which writes the hello example into the current directory
// and refuses to overwrite any file the example would write.
func newInit(use string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Aliases: aliases,
		Short:   "Write the hello example into the current directory: a stack and two modules",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title(filepath.Base(dir))
			if _, err := os.Stat(filepath.Join(dir, stack.FileName)); err == nil {
				return input(fmt.Errorf("%s already has a %s; qory does not overwrite it", dir, stack.FileName))
			}
			if existing, err := standing(dir); err != nil {
				return err
			} else if existing != "" {
				return input(fmt.Errorf("%s already has a %s; qory does not overwrite it", dir, existing))
			}
			written, err := writeExample(dir)
			if err != nil {
				return err
			}
			// The compose writes into a checkout only, so a directory inside no
			// repository becomes one here, and a row says so.
			var initialised bool
			if root, err := checkout.Root(dir); err != nil {
				return err
			} else if checkout.ExcludeFile(root) == "" {
				if err := checkout.Init(dir); err != nil {
					return fmt.Errorf("git init in %s: %w", dir, err)
				}
				initialised = true
			}
			if name := user.Name(); name != "" {
				u.Success("Hello, %s. Wrote %d files", name, len(written))
			} else {
				u.Success("wrote %d files", len(written))
			}
			var rows [][]string
			for _, w := range written {
				rows = append(rows, []string{w})
			}
			u.Table(rows)
			u.Blank()
			var fields [][2]string
			if initialised {
				fields = append(fields, [2]string{"git", "initialised a repository here; qory composes into a checkout"})
			}
			u.Fields(append(fields, [2]string{"next", "qory harness compose"}))
			return nil
		},
	}
}

// standing returns the first file of the example that already exists in dir, relative
// to dir, or "" when none does.
func standing(dir string) (string, error) {
	if Example == nil {
		return "", errors.New("this build carries no example")
	}
	root, err := fs.Sub(Example, ExampleRoot)
	if err != nil {
		return "", err
	}
	var found string
	err = fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return err
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path))); err == nil {
			found = filepath.ToSlash(path)
		}
		return nil
	})
	return found, err
}

// writeExample copies the example tree into dir and returns the paths it wrote, relative to dir.
func writeExample(dir string) ([]string, error) {
	if Example == nil {
		return nil, errors.New("this build carries no example")
	}
	root, err := fs.Sub(Example, ExampleRoot)
	if err != nil {
		return nil, err
	}
	var written []string
	err = fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		written = append(written, filepath.ToSlash(path))
		return nil
	})
	return written, err
}
