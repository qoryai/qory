package cmd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// submitCommit is the commit of the source of the documents' submit_module.
const submitCommit = "0123456789abcdef0123456789abcdef01234567"

// withSubmit sets the documents' submit to typ and send, and their submit_module to the
// module forge-submit at [submitCommit].
func withSubmit(typ, send string) func(map[string]any) {
	return func(d map[string]any) {
		d["submit"] = map[string]any{"type": typ, "send": send}
		d["submit_module"] = map[string]any{"name": "forge-submit", "source": map[string]any{"git": "https://git.test/example-org/forge.git", "ref": submitCommit, "path": "modules/forge-submit"}}
	}
}

// harnessSubmit is the files of the base's repository with a process.yaml in the folder
// of the module hello whose submit is typ and send.
func harnessSubmit(typ, send string) map[string]string {
	return map[string]string{"examples/hello/modules/hello/process.yaml": "submit:\n  type: " + typ + "\n  send: " + send + "\n"}
}

// submitRun runs qory run from a run code whose documents are changed by change, with
// the base's files, and fails the test unless the session joins. It returns the run.
func submitRun(t *testing.T, change func(map[string]any), files map[string]string) harnessRun {
	t.Helper()
	r := codeHarness{change: change, files: files}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	return r
}

// submitLines is the lines of out that start with the submit line's prefix.
func submitLines(out string) []string {
	var lines []string
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "qory run: submit: ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestARunSubmitsWithTheDocumentsSubmitWhenTheHarnessHasNone is a run from a run code
// whose documents have submit reply-draft at end_of_run and a submit_module, and whose
// composed harness has no process.yaml: the run takes the documents' submit and
// submit_module as they are, and prints no submit line.
func TestARunSubmitsWithTheDocumentsSubmitWhenTheHarnessHasNone(t *testing.T) {
	change := withSubmit("reply-draft", "end_of_run")
	r := submitRun(t, change, nil)
	if lines := submitLines(r.out); len(lines) != 0 {
		t.Errorf("the run prints %q, want no submit line\n%s", lines, r.out)
	}
	submit, module, err := cmd.SubmitOf(codeDocuments(t, change))
	if err != nil {
		t.Fatal(err)
	}
	if submit == nil || submit.Type != "reply-draft" || submit.Send != "end_of_run" {
		t.Errorf("the run's submit is %+v, want reply-draft at end_of_run", submit)
	}
	if module == nil || module.Name != "forge-submit" || module.Source.Ref != submitCommit {
		t.Errorf("the run's submit_module is %+v, want forge-submit at %s", module, submitCommit)
	}
}

// TestARunWhoseHarnessHasAnotherSubmitUsesTheDocumentsAndPrintsBoth is a run from a run
// code whose documents have submit reply-draft at end_of_run, and whose composed harness
// has a process.yaml in the module hello with submit reply-draft at after_gate: the run
// takes the documents' submit, and prints one line, in the compose step, with both
// values. With the same submit in both, the run prints no submit line.
func TestARunWhoseHarnessHasAnotherSubmitUsesTheDocumentsAndPrintsBoth(t *testing.T) {
	change := withSubmit("reply-draft", "end_of_run")
	t.Run("another submit", func(t *testing.T) {
		r := submitRun(t, change, harnessSubmit("reply-draft", "after_gate"))
		want := "qory run: submit: the run documents' reply-draft at end_of_run replaces the harness's reply-draft at after_gate\n"
		if lines := submitLines(r.out); len(lines) != 1 || lines[0] != want {
			t.Errorf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
		}
		if !strings.Contains(r.logged(), want) {
			t.Errorf("the compose step's lines have no submit line:\n%s", r.logged())
		}
		submit, _, err := cmd.SubmitOf(codeDocuments(t, change))
		if err != nil {
			t.Fatal(err)
		}
		if submit == nil || submit.Type != "reply-draft" || submit.Send != "end_of_run" {
			t.Errorf("the run's submit is %+v, want the documents' reply-draft at end_of_run", submit)
		}
	})
	t.Run("the same submit", func(t *testing.T) {
		r := submitRun(t, change, harnessSubmit("reply-draft", "end_of_run"))
		if lines := submitLines(r.out); len(lines) != 0 {
			t.Errorf("the run prints %q, want no submit line\n%s", lines, r.out)
		}
	})
}

// TestARunWhoseDocumentsHaveNoSubmitHasNoSubmitStep is a run from a run code whose
// documents have no submit, and whose composed harness has a process.yaml in the module
// hello with submit reply-draft at after_gate: the run has no submit settings, and
// prints one line that it has no submit step and does not use the harness's.
func TestARunWhoseDocumentsHaveNoSubmitHasNoSubmitStep(t *testing.T) {
	r := submitRun(t, nil, harnessSubmit("reply-draft", "after_gate"))
	want := "qory run: submit: the run documents have none, so the run has no submit step and does not use the harness's reply-draft at after_gate\n"
	if lines := submitLines(r.out); len(lines) != 1 || lines[0] != want {
		t.Errorf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
	}
	submit, module, err := cmd.SubmitOf(codeDocuments(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if submit != nil || module != nil {
		t.Errorf("the run's submit is %+v and its submit_module %+v, want none", submit, module)
	}
}

// TestAReadRunWhoseHarnessHasASubmitPrintsNoSubmitLine is a run from a run code whose
// documents have access read, so no submit, and whose composed harness has a
// process.yaml in the module hello with a submit: the run has no submit step by its
// access, and prints no submit line.
func TestAReadRunWhoseHarnessHasASubmitPrintsNoSubmitLine(t *testing.T) {
	r := submitRun(t, lowered, harnessSubmit("reply-draft", "after_gate"))
	if lines := submitLines(r.out); len(lines) != 0 {
		t.Errorf("the run prints %q, want no submit line\n%s", lines, r.out)
	}
}

// harnessModules is the files of the base's repository with a process.yaml in each of
// the modules hello and world, the stack's two modules in that order, set to the
// given text.
func harnessModules(hello, world string) map[string]string {
	files := map[string]string{}
	if hello != "" {
		files["examples/hello/modules/hello/process.yaml"] = hello
	}
	if world != "" {
		files["examples/hello/modules/world/process.yaml"] = world
	}
	return files
}

// submitText is a process.yaml with a submit of typ and send.
func submitText(typ, send string) string {
	return "submit:\n  type: " + typ + "\n  send: " + send + "\n"
}

// TestARunWhoseHarnessSubmitDiffersInOneValueUsesTheDocumentsAndPrintsBoth is a run from
// a run code whose documents have submit reply-draft at end_of_run, and whose composed
// harness has a submit that differs only in type, or only in send: the run prints the
// one line with both values.
func TestARunWhoseHarnessSubmitDiffersInOneValueUsesTheDocumentsAndPrintsBoth(t *testing.T) {
	for _, c := range []struct{ name, typ, send string }{
		{"only the type", "other-draft", "end_of_run"},
		{"only the send", "reply-draft", "after_gate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := submitRun(t, withSubmit("reply-draft", "end_of_run"), harnessSubmit(c.typ, c.send))
			want := "qory run: submit: the run documents' reply-draft at end_of_run replaces the harness's " + c.typ + " at " + c.send + "\n"
			if lines := submitLines(r.out); len(lines) != 1 || lines[0] != want {
				t.Errorf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
			}
		})
	}
}

// TestARunTakesTheHarnessSubmitFromTheFirstModuleThatHasAWellFormedOne is a run from a
// run code whose documents have no submit, and whose composed harness has a submit in
// the process.yaml of its second module only: the first module's process.yaml has no
// submit, or a submit with a send that is neither end_of_run nor after_gate. The line
// shows the second module's submit.
func TestARunTakesTheHarnessSubmitFromTheFirstModuleThatHasAWellFormedOne(t *testing.T) {
	want := "qory run: submit: the run documents have none, so the run has no submit step and does not use the harness's reply-draft at after_gate\n"
	for _, c := range []struct{ name, first string }{
		{"a first module with no submit", "summary: no submit here\n"},
		{"a first module with a malformed submit", submitText("other-draft", "sometimes")},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := submitRun(t, nil, harnessModules(c.first, submitText("reply-draft", "after_gate")))
			if lines := submitLines(r.out); len(lines) != 1 || lines[0] != want {
				t.Errorf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
			}
		})
	}
}

// TestAWriteNoPushRunWhoseHarnessHasASubmitPrintsNoSubmitLine is a run from a run code
// whose documents have access write-no-push, so no submit, and whose composed harness
// has a submit: the run prints no submit line, as the contract allows submit only for
// access write.
func TestAWriteNoPushRunWhoseHarnessHasASubmitPrintsNoSubmitLine(t *testing.T) {
	r := submitRun(t, func(d map[string]any) { d["access"] = "write-no-push" }, harnessSubmit("reply-draft", "after_gate"))
	if lines := submitLines(r.out); len(lines) != 0 {
		t.Errorf("the run prints %q, want no submit line\n%s", lines, r.out)
	}
}

// TestAHiddenHarnessPrintsTheSameSubmitLineAsAVisibleOne is a run from a run code whose
// documents have submit reply-draft at end_of_run, and whose composed harness has a
// different submit, once with harness_hidden true and once false: the line is the same.
// It shows the harness's type and send, which are values of the process.yaml and not
// addresses.
func TestAHiddenHarnessPrintsTheSameSubmitLineAsAVisibleOne(t *testing.T) {
	want := "qory run: submit: the run documents' reply-draft at end_of_run replaces the harness's reply-draft at after_gate\n"
	for _, c := range []struct {
		name  string
		shown bool
	}{{"a hidden harness", false}, {"a visible harness", true}} {
		t.Run(c.name, func(t *testing.T) {
			r := codeHarness{change: withSubmit("reply-draft", "end_of_run"), files: harnessSubmit("reply-draft", "after_gate"), shown: c.shown}.run(t)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if lines := submitLines(r.out); len(lines) != 1 || lines[0] != want {
				t.Errorf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
			}
		})
	}
}
