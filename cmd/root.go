package cmd

import "github.com/spf13/cobra"

// Version is the release version, set at build time with
//
//	go build -ldflags "-X github.com/qoryai/qory/cmd.Version=v1.2.3"
//
// A build without that flag leaves it "dev", and qory version then reports what Go
// recorded about the build instead: the module version for an install from a tag, else the
// commit of the checkout it was built from.
var Version = "dev"

// Root builds the command tree and returns its root command. Every caller builds its own
// tree: [Execute] to run one, and the gendocs generator to write the command reference
// under docs/commands. The tree carries no state between calls, so a test may build it,
// point its output at a buffer, and run it with SetArgs.
//
// Usage and errors are silenced on the root, because this tool prints both itself: a
// command reports its own failure through the ui package, and the main package prints
// whatever reaches it. A flag cobra cannot parse and an argument a command does not take
// come back as input errors, so [ExitCode] gives them [ExitInput].
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "qory",
		Short: "Compose the harness a runtime loads from modules",
		Long: `Compose the harness a runtime loads from modules, for Claude Code, Codex, Gemini CLI,
OpenCode, Cursor, Copilot CLI, Amp, Goose, and any tool that reads AGENTS.md.

Shortcuts:
  hc  harness compose
  hi  harness inspect
  hr  harness remove`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersion(), newHarness(), newConfig())
	root.AddCommand(shortcuts()...)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return input(err) })
	return root
}

// noArgs is [cobra.NoArgs] returning an input error, so a stray argument exits with
// [ExitInput].
func noArgs(cmd *cobra.Command, args []string) error {
	return input(cobra.NoArgs(cmd, args))
}

// Execute builds the command tree, runs the command the arguments name, and returns its
// error. The caller decides what to print and which status to exit with: [ErrReported]
// says whether the command printed the failure itself, [ExitCode] which status it gets.
func Execute() error {
	return Root().Execute()
}
