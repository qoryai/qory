package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// TestShortcutsReachTheSameCommands runs each shortcut and its long form in a checkout of
// its own and compares what they printed, with the checkout's path taken out so two
// checkouts read alike.
func TestShortcutsReachTheSameCommands(t *testing.T) {
	tests := []struct {
		short string
		long  []string
	}{
		{short: "hc", long: []string{"harness", "compose"}},
		{short: "hi", long: []string{"harness", "inspect"}},
		{short: "hr", long: []string{"harness", "remove"}},
	}
	for _, tc := range tests {
		t.Run(tc.short, func(t *testing.T) {
			long := inComposedCheckout(t, tc.long...)
			short := inComposedCheckout(t, tc.short)
			if short != long {
				t.Errorf("%s printed\n%s\n%s printed\n%s", tc.short, short, strings.Join(tc.long, " "), long)
			}
		})
	}
}

// TestShortcutsStayOutOfTheHelp checks that the shortcuts are there and hidden: the long
// forms are what the help teaches.
func TestShortcutsStayOutOfTheHelp(t *testing.T) {
	for _, name := range []string{"hc", "hi", "hr"} {
		c, _, err := cmd.Root().Find([]string{name})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if c.Name() != name {
			t.Errorf("%s reached command %s", name, c.Name())
		}
		if !c.Hidden {
			t.Errorf("%s is in the help", name)
		}
	}
}

// TestExecuteRunsTheArgumentsTheProcessGot runs the entry point the main package calls: it
// takes the command line from the process arguments and writes to standard output.
func TestExecuteRunsTheArgumentsTheProcessGot(t *testing.T) {
	dir := emptyDir(t)
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	wasOut, wasArgs := os.Stdout, os.Args
	t.Cleanup(func() { os.Stdout, os.Args = wasOut, wasArgs })
	os.Stdout, os.Args = stdout, []string{"qory", "version"}

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	printed, err := os.ReadFile(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(printed), ui.Mark+" qory", "harness format")
}

// inComposedCheckout composes a fresh checkout, then runs args in it and returns what args
// printed, with the checkout's own path replaced so the output of two checkouts compares.
func inComposedCheckout(t *testing.T, args ...string) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("compose: %v\n%s", err, out)
	}
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.ReplaceAll(out, root, "CHECKOUT")
}

// TestEveryCommandTakesTheServerVariables is the four variables of the server and the run
// credential's in qory's environment, read into memory and removed when a command starts, before it starts
// anything: a worktree's add command, run through the shell, inherits none of them, and
// qory's environment holds none of them afterwards. No command of the tree sets a
// PersistentPreRun of its own, which would skip the root's.
func TestEveryCommandTakesTheServerVariables(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	configure(t, root, nil, []string{"worktree:", "  run:", `    add: ['printf "[%s]" "$QORY_ACCESS_KEY_SECRET$QORY_ACCESS_KEY_ID$QORY_APIARY_PUBLIC_KEY$QORY_SERVER_SECRET$QORY_RUN_CREDENTIAL_SECRET" > inherited.txt']`})
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "stack")
	localOrigin(t, root)
	runGit(t, root, "push", "--quiet", "origin", "main")
	runGit(t, root, "remote", "set-head", "origin", "main")
	want := config.ServerVariables{AccessKeyID: "ak_0123456789abcdef", AccessKeySecret: "qak_not-a-real-secret", ApiaryPublicKey: "[]", WorkspaceSecret: "a-workspace-secret", RunCredentialSecret: "header.claims.signature"}
	t.Setenv("QORY_ACCESS_KEY_ID", want.AccessKeyID)
	t.Setenv("QORY_ACCESS_KEY_SECRET", want.AccessKeySecret)
	t.Setenv("QORY_APIARY_PUBLIC_KEY", want.ApiaryPublicKey)
	t.Setenv("QORY_SERVER_SECRET", want.WorkspaceSecret)
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", want.RunCredentialSecret)
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	if out, err := run(t, "worktree", "add", "feature"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(root), "wt-feature", "inherited.txt")); err != nil || string(data) != "[]" {
		t.Errorf("the add command inherited %q, %v", data, err)
	}
	for _, name := range []string{"QORY_ACCESS_KEY_ID", "QORY_ACCESS_KEY_SECRET", "QORY_APIARY_PUBLIC_KEY", "QORY_SERVER_SECRET", "QORY_RUN_CREDENTIAL_SECRET"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("%s stayed in qory's environment", name)
		}
	}
	if got := config.TakenServerVariables(); got != want {
		t.Errorf("qory took %+v", got)
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasParent() && (c.PersistentPreRun != nil || c.PersistentPreRunE != nil) {
			t.Errorf("%s sets a PersistentPreRun of its own", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd.Root())
}
