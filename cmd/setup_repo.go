package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// moduleDir is the directory of the repository's own module, which setup repo writes.
const moduleDir = "harness"

// checkoutConfig is the qory.yaml setup repo writes into a repository: its own stack, one
// runtime and the repository's own module, and every key a repository commits shown.
// %[1]s is the repository's name, %[2]s and %[3]s the spaces that bring the comments
// after it to the column of the others.
const checkoutConfig = `apiVersion: qory.ai/v1alpha1

# This repository's stack. To take a stack as delivered instead, name it under extends in
# place of target: {git: https://github.com/acme/harness, ref: v2.4.0, path: stacks/nextjs}
harness:
  name: %[1]s%[2]s# the stack's name in the report
  target:
    runtime: claude            # or a list: [claude, codex]
  modules:
    - name: %[1]s%[3]s# this repository's own, committed with it
      source: {path: ./` + moduleDir + `}

# What a worktree of this repository needs; qory worktree add reads it.
worktree:
  #base: main                  # a new branch starts here; default: the remote's HEAD
  link: []                     # linked from the main checkout, e.g. [.env, .env.local]
  copy: []                     # copied once from the main checkout, e.g. [.env.local]
  run:
    add: []                    # run in the new worktree, e.g. [pnpm install]
    remove: []                 # run in a worktree before it is removed
`

// worktreeConfig is the qory.yaml setup repo writes beside a qory-stack.yaml that is there: the
// worktree section alone, since the stack file holds the stack.
const worktreeConfig = `apiVersion: qory.ai/v1alpha1

# What a worktree of this repository needs; qory worktree add reads it. The stack is in
# qory-stack.yaml.
worktree:
  #base: main                  # a new branch starts here; default: the remote's HEAD
  link: []                     # linked from the main checkout, e.g. [.env, .env.local]
  copy: []                     # copied once from the main checkout, e.g. [.env.local]
  run:
    add: []                    # run in the new worktree, e.g. [pnpm install]
    remove: []                 # run in a worktree before it is removed
`

// moduleFile is the qory-module.yaml of the repository's own module. %s is its name.
const moduleFile = `apiVersion: qory.ai/v1alpha1
name: %s
`

// agentsFile is the instruction file of the repository's own module. %s is its name.
const agentsFile = `# %s

Instructions every agent reads in this repository.
`

// userConfig is the qory.yaml setup machine writes: how qory runs on this machine, every
// value at its default.
const userConfig = `apiVersion: qory.ai/v1alpha1

# How qory runs on this machine. A checkout's qory.yaml overrides the keys it names.
harness:
  #runtime: claude             # render for this runtime, or a list, instead of the stack's
  #model: opus                 # write this model instead of the stack's
  force: false                 # replace a tracked, unmodified file where a link goes
  update: never                # always: fetch every git source again on each compose
worktree:
  dir: ..                      # where worktrees go, relative to the main checkout
  name: wt-{branch}            # the directory name; {branch} and {repo} are replaced
git:
  timeout: 10m                 # the longest one git command may run
  #cache: ~/.cache/qory        # where git sources are fetched to
#env:
#  FOO: bar                    # exported to every runtime with a place for it
`

// notAName matches what a module name does not carry, when it is made from a directory name.
var notAName = regexp.MustCompile(`[^a-z0-9._-]+`)

// newSetupRepo builds the setup repo verb, which sets the repository up for qory: a
// qory.yaml holding its own stack, and the module that stack names.
func newSetupRepo() *cobra.Command {
	return &cobra.Command{
		Use:   "repo",
		Short: "Write the repository's qory.yaml: its stack, its module, its worktree settings",
		Long: `Set the repository up for qory.

setup repo writes into the current directory a qory.yaml holding the repository's own
stack, one runtime and one module, with every key a repository commits shown, and that
module under harness with its manifest and AGENTS.md. A directory whose qory.yaml already
names a stack, or that holds a qory-stack.yaml, keeps its stack. A file that is already
there is kept.

This qory.yaml is committed and decides for everyone who clones the repository: the
stack under harness, and what a worktree needs under worktree. How qory runs on one
machine, for every repository, is the qory.yaml that setup machine writes.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := ui.New(cmd.OutOrStdout())
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			name := repositoryName(dir)
			u.Title(name)
			var rows [][2]string
			var files []struct{ path, content string }
			switch found, _ := config.DiscoverStack(dir); found {
			case filepath.Join(dir, config.FileName):
				rows = append(rows, [2]string{"kept", config.FileName + "  (its harness section names the stack)"})
			case filepath.Join(dir, stack.FileName):
				rows = append(rows, [2]string{"kept", stack.FileName + "  (the stack is here)"})
				files = append(files, struct{ path, content string }{config.FileName, worktreeConfig})
			default:
				files = append(files,
					struct{ path, content string }{config.FileName, fmt.Sprintf(checkoutConfig, name, column(name, 23), column(name, 19))},
					struct{ path, content string }{filepath.Join(moduleDir, module.ManifestName), fmt.Sprintf(moduleFile, name)},
					struct{ path, content string }{filepath.Join(moduleDir, "AGENTS.md"), fmt.Sprintf(agentsFile, name)})
			}
			for _, f := range files {
				row, err := writeOnce(dir, f.path, f.content)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			// The compose writes into a checkout only, so a directory inside no
			// repository becomes one here, and a row says so.
			if root, err := checkout.Root(dir); err != nil {
				return err
			} else if checkout.ExcludeFile(root) == "" {
				if err := checkout.Init(dir); err != nil {
					return fmt.Errorf("git init in %s: %w", dir, err)
				}
				rows = append(rows, [2]string{"git", "initialised a repository here; qory composes into a checkout"})
			}
			u.Fields(append(rows, [2]string{"next", "qory harness compose"}))
			return nil
		},
	}
}

// newSetupMachine builds the setup machine verb, which writes the machine's qory.yaml.
func newSetupMachine() *cobra.Command {
	return &cobra.Command{
		Use:   "machine",
		Short: "Write your qory.yaml in ~/.config/qory: how qory runs on this machine",
		Long: `Write the machine's qory.yaml, in $XDG_CONFIG_HOME/qory or ~/.config/qory: how qory runs
on this machine, every key shown at its default. A file that is already there is kept.

This qory.yaml is yours, never committed, and applies to every repository you work in:
the runtime and model to compose for instead of the stack's, force and update, where a
worktree goes and what it is called, the git timeout and cache, and environment
variables. The repository's own qory.yaml, which setup repo writes, is read on top of it,
and qory config shows every key with the file it came from.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := ui.New(cmd.OutOrStdout())
			dir := config.UserDir()
			u.Title("qory setup machine", ui.Short(dir, ""))
			row, err := writeOnce(dir, config.FileName, userConfig)
			if err != nil {
				return err
			}
			u.Fields([][2]string{row, {"next", "qory config"}})
			return nil
		},
	}
}

// column is the spaces that bring a comment after name to the column width holds, one
// space at least.
func column(name string, width int) string {
	return strings.Repeat(" ", max(1, width-len(name)))
}

// writeOnce writes content to path under dir unless something is there, and returns the
// row that says which.
func writeOnce(dir, path, content string) ([2]string, error) {
	full := filepath.Join(dir, path)
	if _, err := os.Lstat(full); err == nil {
		return [2]string{"kept", path + "  (already here)"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return [2]string{}, err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return [2]string{}, err
	}
	return [2]string{"wrote", path}, nil
}

// repositoryName is the directory's name as a module name: lower case, with anything
// that is not a letter, a digit, a dot, a dash or an underscore replaced by a dash.
func repositoryName(dir string) string {
	name := notAName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	if name = strings.Trim(name, "-"); name == "" {
		return "app"
	}
	return name
}
