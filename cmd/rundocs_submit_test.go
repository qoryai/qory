package cmd_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestARunKeepsTheDocumentsSubmitWhenTheHarnessHasNone is a run from a run code whose
// documents have submit reply-draft at end_of_run and a submit_module, and whose
// composed harness has no process.yaml: the run's submit and submit_module are the
// documents', as they are, and the run prints no submit line.
func TestARunKeepsTheDocumentsSubmitWhenTheHarnessHasNone(t *testing.T) {
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

// TestARunWhoseHarnessHasAnotherSubmitKeepsTheDocumentsSubmitAndPrintsBoth is a run from
// a run code whose documents have submit reply-draft at end_of_run, and whose composed
// harness has a process.yaml in the module hello with submit reply-draft at after_gate:
// the run's submit is the documents', and the run prints one line, in the compose step,
// with both values. With the same submit in both, the run prints no submit line.
func TestARunWhoseHarnessHasAnotherSubmitKeepsTheDocumentsSubmitAndPrintsBoth(t *testing.T) {
	change := withSubmit("reply-draft", "end_of_run")
	t.Run("another submit", func(t *testing.T) {
		r := submitRun(t, change, harnessSubmit("reply-draft", "after_gate"))
		want := "qory run: submit: documents' reply-draft at end_of_run replaces harness's reply-draft at after_gate\n"
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

// TestARunWhoseDocumentsHaveNoSubmitHasNoSubmitSettings is a run from a run code whose
// documents have no submit, and whose composed harness has a process.yaml in the module
// hello with submit reply-draft at after_gate: the run's submit and submit_module are
// nil, and the run prints one line that shows the harness's submit as unused.
func TestARunWhoseDocumentsHaveNoSubmitHasNoSubmitSettings(t *testing.T) {
	r := submitRun(t, nil, harnessSubmit("reply-draft", "after_gate"))
	want := "qory run: submit: documents have no submit, so harness's reply-draft at after_gate is unused\n"
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
// process.yaml in the module hello with a submit: the contract allows the documents of
// a run with that access no submit, and the run prints no submit line.
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

// TestARunWhoseHarnessSubmitDiffersInOneValueKeepsTheDocumentsSubmitAndPrintsBoth is a
// run from a run code whose documents have submit reply-draft at end_of_run, and whose
// composed harness has a submit that differs only in type, or only in send: the run
// prints the one line with both values.
func TestARunWhoseHarnessSubmitDiffersInOneValueKeepsTheDocumentsSubmitAndPrintsBoth(t *testing.T) {
	for _, c := range []struct{ name, typ, send string }{
		{"only the type", "other-draft", "end_of_run"},
		{"only the send", "reply-draft", "after_gate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := submitRun(t, withSubmit("reply-draft", "end_of_run"), harnessSubmit(c.typ, c.send))
			want := "qory run: submit: documents' reply-draft at end_of_run replaces harness's " + c.typ + " at " + c.send + "\n"
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
	want := "qory run: submit: documents have no submit, so harness's reply-draft at after_gate is unused\n"
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
	want := "qory run: submit: documents' reply-draft at end_of_run replaces harness's reply-draft at after_gate\n"
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

// The longest artifact type names the run contract allows, 64 characters each, for the
// documents' submit and for the harness's.
var (
	longDocsType    = strings.Repeat("abcdefgh-", 7) + "x"
	longHarnessType = strings.Repeat("stuvwxyz-", 7) + "y"
)

// TestTheSubmitLineHasAtMost100CharactersAndTheFullTypesOnASecondLine is a run from a
// run code whose documents have a submit and whose composed harness has a different one,
// with the types of reply-draft, then 64 characters in the documents' only, in the
// harness's only, and in both; and a run whose documents have no submit and whose
// harness's type has 64 characters. Line 1, with its "qory run: ", has at most 100
// characters, a type that does not fit is shortened there, and a second line has the
// full types. With reply-draft, line 1 is whole and there is no second line.
func TestTheSubmitLineHasAtMost100CharactersAndTheFullTypesOnASecondLine(t *testing.T) {
	for _, c := range []struct {
		name, docs, harness string
		full                string
	}{
		{"types of reply-draft", "reply-draft", "reply-draft", ""},
		{"a long type in the documents", longDocsType, "reply-draft", "The full types are " + longDocsType + " in the documents and reply-draft in the harness."},
		{"a long type in the harness", "reply-draft", longHarnessType, "The full types are reply-draft in the documents and " + longHarnessType + " in the harness."},
		{"a long type in both", longDocsType, longHarnessType, "The full types are " + longDocsType + " in the documents and " + longHarnessType + " in the harness."},
		{"a long type in the harness and no submit in the documents", "", longHarnessType, "The full type is " + longHarnessType + "."},
	} {
		t.Run(c.name, func(t *testing.T) {
			var change func(map[string]any)
			if c.docs != "" {
				change = withSubmit(c.docs, "end_of_run")
			}
			r := submitRun(t, change, harnessSubmit(c.harness, "after_gate"))
			lines := submitLines(r.out)
			if len(lines) != 1 {
				t.Fatalf("the run prints %q, want one submit line\n%s", lines, r.out)
			}
			if n := utf8.RuneCountInString(strings.TrimSuffix(lines[0], "\n")); n > 100 {
				t.Errorf("line 1 has %d characters, want at most 100:\n%s", n, lines[0])
			}
			if c.full == "" {
				if strings.Contains(r.out, "The full type") {
					t.Errorf("the run prints a second line for types that fit:\n%s", r.out)
				}
				return
			}
			if !strings.Contains(r.out, "\n  "+c.full+"\n") {
				t.Errorf("the output has no second line %q:\n%s", c.full, r.out)
			}
			for _, long := range []string{longDocsType, longHarnessType} {
				if strings.Contains(lines[0], long) {
					t.Errorf("line 1 has the whole type %s:\n%s", long, lines[0])
				}
			}
			for _, name := range []string{c.docs, c.harness} {
				if len(name) > 24 && (!strings.Contains(lines[0], name[:3]) || !strings.Contains(lines[0], name[len(name)-3:])) {
					t.Errorf("line 1 keeps fewer than the first 3 and the last 3 characters of %s:\n%s", name, lines[0])
				}
			}
		})
	}
}

// TestASubmitLineOf100CharactersStaysWholeAndOneOf101IsShortened is a run from a run
// code whose composed harness has a submit whose type makes line 1, with its
// "qory run: ", exactly 100 characters, or 101: the line of 100 characters is whole and
// has no second line, and the one of 101 is shortened to 100 and has a second line with
// the full type. It is so for documents with no submit, and for documents with a
// different submit of reply-draft.
func TestASubmitLineOf100CharactersStaysWholeAndOneOf101IsShortened(t *testing.T) {
	for _, c := range []struct {
		name, docs, harness string
		whole               bool
	}{
		{"no submit in the documents, 100 characters", "", strings.Repeat("a", 19), true},
		{"no submit in the documents, 101 characters", "", strings.Repeat("a", 20), false},
		{"a different submit, 100 characters", "reply-draft", strings.Repeat("a", 12), true},
		{"a different submit, 101 characters", "reply-draft", strings.Repeat("a", 13), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var change func(map[string]any)
			if c.docs != "" {
				change = withSubmit(c.docs, "end_of_run")
			}
			r := submitRun(t, change, harnessSubmit(c.harness, "after_gate"))
			lines := submitLines(r.out)
			if len(lines) != 1 {
				t.Fatalf("the run prints %q, want one submit line\n%s", lines, r.out)
			}
			if n := utf8.RuneCountInString(strings.TrimSuffix(lines[0], "\n")); n != 100 {
				t.Errorf("line 1 has %d characters, want 100:\n%s", n, lines[0])
			}
			if whole := strings.Contains(lines[0], c.harness); whole != c.whole {
				t.Errorf("line 1 has the whole type: %v, want %v:\n%s", whole, c.whole, lines[0])
			}
			if second := strings.Contains(r.out, "\n  The full type"); second == c.whole {
				t.Errorf("the output has a second line: %v, want %v:\n%s", second, !c.whole, r.out)
			}
		})
	}
}

// TestARunWhoseWorkflowLowersItBelowWritePrintsNoSubmitLine is a run from a run
// code whose documents have access write, with a submit or with none, whose composed
// harness has a different submit, and whose stack declares the workflow hello
// write-no-push, read or write. With write, the run prints the one line, whether the
// documents have a submit or none. With write-no-push or read, the run's level is below
// write, and it prints no submit line.
func TestARunWhoseWorkflowLowersItBelowWritePrintsNoSubmitLine(t *testing.T) {
	for _, c := range []struct {
		name, access string
		docsSubmit   bool
		write        bool
	}{
		{"a submit in the documents, workflow write", "write", true, true},
		{"a submit in the documents, workflow write-no-push", "write-no-push", true, false},
		{"a submit in the documents, workflow read", "read", true, false},
		{"no submit in the documents, workflow write", "write", false, true},
		{"no submit in the documents, workflow write-no-push", "write-no-push", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var change func(map[string]any)
			if c.docsSubmit {
				change = withSubmit("reply-draft", "end_of_run")
			}
			stack := func(s string) string { return s + "workflows:\n  hello:\n    access: " + c.access + "\n" }
			r := codeHarness{change: change, files: harnessSubmit("reply-draft", "after_gate"), stack: stack}.run(t)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			want := 0
			if c.write {
				want = 1
			}
			if lines := submitLines(r.out); len(lines) != want {
				t.Errorf("the run prints %q, want %d submit lines\n%s", lines, want, r.out)
			}
		})
	}
}
