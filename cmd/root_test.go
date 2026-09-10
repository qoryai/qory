package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
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
