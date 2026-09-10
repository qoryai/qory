package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestSetupShellWritesTheHook is setup shell under zsh: it shows the lines, writes them
// on a yes, says how to reload, and finds them on a second run; a no writes nothing; a
// shell qory does not know is refused with an invitation.
func TestSetupShellWritesTheHook(t *testing.T) {
	newCheckout(t)
	t.Setenv("SHELL", "/bin/zsh")
	rc := filepath.Join(os.Getenv("HOME"), ".zshrc")
	out, _, err := runSplit(t, "n\n", "setup", "shell")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "These lines would go to the end of ~/.zshrc", "source <(command qory setup completion zsh)", "qory() {", "Add them? [y/N]", "Nothing written.")
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("a no wrote the file")
	}
	out, _, err = runSplit(t, "y\n", "setup", "shell")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "added to ~/.zshrc")
	wantsRow(t, out, "reload", "exec zsh")
	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "# qory: completions", "source <(command qory setup completion zsh)", "qory() {", `command qory "$@" --path`)
	out, _, err = runSplit(t, "y\n", "setup", "shell")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "~/.zshrc already holds the lines; nothing to add")
	if again, _ := os.ReadFile(rc); string(again) != string(data) {
		t.Error("the second run changed the file")
	}
	t.Setenv("SHELL", "/bin/tcsh")
	_, _, err = runSplit(t, "y\n", "setup", "shell")
	if err == nil || !strings.Contains(err.Error(), "the tcsh shell is not yet supported; qory knows bash, fish, zsh, and welcomes a contribution") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("tcsh: %v", err)
	}
	t.Setenv("SHELL", "/usr/local/bin/fish")
	stdout, _, err := runSplit(t, "", "setup", "shell", "--print")
	if err != nil || !strings.HasPrefix(stdout, "# qory: completions") || !strings.Contains(stdout, "command qory setup completion fish | source") || !strings.Contains(stdout, "function qory") {
		t.Fatalf("--print: %v\n%s", err, stdout)
	}
	out, _, err = runSplit(t, "y\n", "setup", "shell")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "added to ~/.config/fish/config.fish")
	if data, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config", "fish", "config.fish")); !strings.Contains(string(data), "function qory") {
		t.Errorf("fish hook: %q", data)
	}
}

// TestSetupCompletionPrintsTheScript is the completion script for the shell named, else
// the one in $SHELL; a shell with none is refused, and the built-in completion verb is
// not there.
func TestSetupCompletionPrintsTheScript(t *testing.T) {
	emptyDir(t)
	out, err := run(t, "setup", "completion", "zsh")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "#compdef qory")
	t.Setenv("SHELL", "/usr/local/bin/fish")
	out, err = run(t, "setup", "completion")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "complete -c qory")
	t.Setenv("SHELL", "/bin/tcsh")
	_, err = run(t, "setup", "completion")
	if err == nil || !strings.Contains(err.Error(), "the tcsh shell is not yet supported; qory completes bash, fish, powershell and zsh") {
		t.Errorf("tcsh: %v", err)
	}
	if _, err := run(t, "completion", "zsh"); err == nil {
		t.Error("qory completion is still there; setup completion replaces it")
	}
}
