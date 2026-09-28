package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/ui"
)

// newSetup builds the setup group: the repository, the machine, and the running shell.
func newSetup() *cobra.Command {
	c := &cobra.Command{
		Use:   "setup",
		Short: "Set up a repository, this machine, or your shell",
		Long: `Set up a repository, this machine, or your shell.

Start with qory setup repo in a repository, or qory setup example to try qory.`,
	}
	c.AddCommand(newSetupRepo(), newSetupExample(), newSetupMachine(), newSetupShell(), newSetupCompletion())
	return c
}

// shells are the shells qory setup shell knows: the rc file it appends to and the
// lines that make the shell follow a worktree add and remove.
var shells = map[string]struct {
	rc     string
	reload string
	// load is the line that loads the completion script; in zsh only once compinit has
	// defined compdef, so an rc file without it still gets the function and no error.
	load  string
	lines string
}{
	"zsh":  {"~/.zshrc", "exec zsh", "(( $+functions[compdef] )) && source <(command qory setup completion zsh)", posixHook},
	"bash": {"~/.bashrc", "exec bash", "source <(command qory setup completion bash)", posixHook},
	"fish": {"~/.config/fish/config.fish", "exec fish", "command qory setup completion fish | source", fishHook},
}

// hookMarker is the first line of what setup shell writes, and what a second run finds.
const hookMarker = "# qory: completions, and a function that follows a worktree add into the worktree and a remove back to the main checkout"

const posixHook = hookMarker + `
{completion}
qory() {
  case "$1" in
    wa|wr) ;;
    worktree) case "$2" in add|remove) ;; *) command qory "$@"; return;; esac ;;
    *) command qory "$@"; return;;
  esac
  local dir
  dir="$(command qory "$@" --path)" || return $?
  if [ -d "$dir" ]; then cd "$dir"; else printf '%s\n' "$dir"; fi
}
`

const fishHook = hookMarker + `
{completion}
function qory
    set -l follow 0
    switch "$argv[1]"
        case wa wr
            set follow 1
        case worktree
            if contains -- "$argv[2]" add remove; set follow 1; end
    end
    if test $follow = 0
        command qory $argv
        return
    end
    set -l dir (command qory $argv --path); or return $status
    if test -d "$dir"; cd $dir; else; printf '%s\n' $dir; end
end
`

// newSetupShell builds the setup shell verb, which loads qory's completions into the
// running shell and makes it follow a worktree add into the worktree and a remove back to
// the main checkout. A program cannot change the directory of the shell that ran it, so
// the lines define a shell function around qory that runs the two verbs with --path and
// cd's to what they print. The shell is the one in $SHELL; the verb shows the lines, asks
// before writing them to the shell's rc file, and says how to reload.
func newSetupShell() *cobra.Command {
	var print bool
	c := &cobra.Command{
		Use:   "shell",
		Short: "Add completions, and make your shell follow worktree add and remove",
		Long: `Add qory's completions to your shell's rc file, and a function that follows worktrees:
it changes into a new worktree on add, and back to the main checkout on remove.

A program cannot change the directory of the shell that ran it. So the function runs
worktree add and remove with --path, and changes to the path they print.

The shell is the one in $SHELL. setup shell shows the lines, asks before writing them,
and says how to reload. --print prints them and writes nothing.`,
		Example: `  qory setup shell                     # add the lines to your rc file
  eval "$(qory setup shell --print)"   # or load them from an rc file a tool of yours owns`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			u := ui.New(out)
			shell := filepath.Base(os.Getenv("SHELL"))
			if !print {
				u.Title("qory setup shell", shell)
			}
			sh, ok := shells[shell]
			if !ok {
				names := make([]string, 0, len(shells))
				for name := range shells {
					names = append(names, name)
				}
				sort.Strings(names)
				return input(fmt.Errorf("the %s shell is not supported; qory has a setup for %s, and welcomes a contribution for yours at https://github.com/qoryai/qory", nameOrNone(shell), strings.Join(names, ", ")))
			}
			lines := strings.ReplaceAll(sh.lines, "{completion}", sh.load)
			if print {
				fmt.Fprint(out, lines)
				return nil
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			rc := filepath.Join(home, strings.TrimPrefix(sh.rc, "~/"))
			if data, err := os.ReadFile(rc); err == nil && strings.Contains(string(data), hookMarker) {
				u.Success("%s already contains the lines; nothing to add", sh.rc)
				return nil
			}
			u.Text("These lines are added to the end of " + sh.rc + " if you answer y:")
			u.Blank()
			u.Code(strings.Split(strings.TrimRight(lines, "\n"), "\n")...)
			u.Blank()
			fmt.Fprint(out, "Add them? [y/N] ")
			answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				u.Text("Nothing written.")
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			prefix := "\n"
			if data, err := os.ReadFile(rc); err == nil && (len(data) == 0 || strings.HasSuffix(string(data), "\n\n")) {
				prefix = ""
			}
			if _, err := io.WriteString(f, prefix+lines); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			u.Success("added to %s", sh.rc)
			u.Fields([][2]string{{"reload", sh.reload}})
			return nil
		},
	}
	c.Flags().BoolVar(&print, "print", false, "print the lines and write nothing")
	return c
}

func nameOrNone(shell string) string {
	if shell == "" || shell == "." {
		return "current"
	}
	return shell
}

// newSetupCompletion builds the setup completion verb, which prints the completion script
// of a shell, the one in $SHELL unless named.
func newSetupCompletion() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Print the completion script for your shell",
		Long: `Print the completion script for the shell in $SHELL, or the one the argument names.

Source it at every start; do not keep a copy. It is generated from this binary, so it
always matches it. The lines setup shell adds already source it.`,
		Example: `  source <(qory setup completion zsh)   # zsh or bash
  qory setup completion fish | source   # fish`,
		Args:      maxArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := filepath.Base(os.Getenv("SHELL"))
			if len(args) == 1 {
				shell = args[0]
			}
			out := cmd.OutOrStdout()
			root := cmd.Root()
			switch shell {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(out)
			}
			return input(fmt.Errorf("the %s shell is not supported; qory completes bash, fish, powershell and zsh, and welcomes a contribution for yours at https://github.com/qoryai/qory", nameOrNone(shell)))
		},
	}
}
