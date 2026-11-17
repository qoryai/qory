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
