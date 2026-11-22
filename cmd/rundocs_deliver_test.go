package cmd_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/cmd"
)

// withDeliver sets the documents' deliver to one entry of typ and send.
func withDeliver(typ, send string) func(map[string]any) {
	return func(d map[string]any) {
		d["deliver"] = []any{map[string]any{"type": typ, "send": send}}
	}
}

// harnessDeliver is the files of the base's repository with a process.yaml in the folder
// of the module hello whose artifact type typ has deliver at send.
func harnessDeliver(typ, send string) map[string]string {
	return map[string]string{"examples/hello/modules/hello/process.yaml": deliverText(typ, send)}
}

// deliverText is a process.yaml whose artifact type typ has deliver at send.
func deliverText(typ, send string) string {
	return "artifact_types:\n  - name: " + typ + "\n    deliver: {send: " + send + "}\n"
}

// deliverRun runs qory run from a run code whose documents are changed by change, with
// the base's files, and fails the test unless the session joins. It returns the run.
func deliverRun(t *testing.T, change func(map[string]any), files map[string]string) harnessRun {
	t.Helper()
	r := codeHarness{change: change, files: files}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	return r
}

// deliverLines is the lines of out that start with the deliver line's prefix.
func deliverLines(out string) []string {
	var lines []string
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "qory run: deliver: ") {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestARunKeepsTheDocumentsDeliverWhenTheHarnessHasNoProcess is a run from a run code
// whose documents deliver reply-draft at end_of_run, and whose composed harness has no
// process.yaml: the run's deliver is the documents', and the run prints no deliver line.
func TestARunKeepsTheDocumentsDeliverWhenTheHarnessHasNoProcess(t *testing.T) {
	change := withDeliver("reply-draft", "end_of_run")
	r := deliverRun(t, change, nil)
	if lines := deliverLines(r.out); len(lines) != 0 {
		t.Errorf("the run prints %q, want no deliver line\n%s", lines, r.out)
	}
	deliver, err := cmd.DeliverOf(codeDocuments(t, change))
	if err != nil {
		t.Fatal(err)
	}
	if len(deliver) != 1 || deliver[0].Type != "reply-draft" || deliver[0].Send != "end_of_run" {
		t.Errorf("the run's deliver is %+v, want reply-draft at end_of_run", deliver)
	}
}

// TestARunPrintsOneLineForEachTypeWhoseDeliverDiffers is a run from a run code whose
// documents deliver reply-draft at end_of_run, or nothing, and whose composed harness has
// a process.yaml in the module hello with a deliver: a type at another send, a type the
// harness does not deliver, and a type the documents do not list each get one line in the
// compose step, in that order, and the same deliver gets none. The run's deliver is the
// documents'.
func TestARunPrintsOneLineForEachTypeWhoseDeliverDiffers(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(map[string]any)
		files  map[string]string
		want   string
	}{
		{"another send", withDeliver("reply-draft", "end_of_run"), harnessDeliver("reply-draft", "after_gate"),
			"qory run: deliver: reply-draft at end_of_run from the documents; harness has after_gate\n"},
		{"a type the harness does not deliver", withDeliver("reply-draft", "end_of_run"), harnessDeliver("bid-changes", "after_gate"),
			"qory run: deliver: reply-draft at end_of_run from the documents; harness has none\n" +
				"qory run: deliver: no bid-changes from the documents; harness has after_gate\n"},
		{"no deliver in the documents", nil, harnessDeliver("reply-draft", "after_gate"),
			"qory run: deliver: no reply-draft from the documents; harness has after_gate\n"},
		{"the same deliver", withDeliver("reply-draft", "end_of_run"), harnessDeliver("reply-draft", "end_of_run"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := deliverRun(t, c.change, c.files)
			lines := deliverLines(r.out)
			switch {
			case c.want == "" && len(lines) != 0:
				t.Errorf("the run prints %q, want no deliver line\n%s", lines, r.out)
			case c.want != "" && strings.Join(lines, "") != c.want:
				t.Errorf("the run prints %q, want %q\n%s", lines, c.want, r.out)
			case c.want != "" && !strings.Contains(r.logged(), c.want):
				t.Errorf("the compose step's lines have no deliver line:\n%s", r.logged())
			}
		})
	}
}

// TestARunTakesTheHarnessDeliverFromTheFirstModuleWithAProcess is a run whose composed
// harness has a process.yaml in both modules: the first module's, which delivers nothing,
// is the one the lines compare, so the second module's deliver gets no line.
func TestARunTakesTheHarnessDeliverFromTheFirstModuleWithAProcess(t *testing.T) {
	files := map[string]string{
		"examples/hello/modules/hello/process.yaml": "summary: no types here\n",
		"examples/hello/modules/world/process.yaml": deliverText("reply-draft", "after_gate"),
	}
	r := deliverRun(t, nil, files)
	if lines := deliverLines(r.out); len(lines) != 0 {
		t.Errorf("the run prints %q, want no deliver line\n%s", lines, r.out)
	}
}

// TestARunBelowWritePrintsNoDeliverLine is a run from a run code whose documents have
// access read or write-no-push, or whose stack lowers the workflow below write, and whose
// harness delivers a type: the run prints no deliver line.
func TestARunBelowWritePrintsNoDeliverLine(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(map[string]any)
		access string
	}{
		{"documents with access read", lowered, ""},
		{"documents with access write-no-push", func(d map[string]any) { d["access"] = "write-no-push" }, ""},
		{"a workflow at write-no-push", nil, "write-no-push"},
		{"a workflow at read", nil, "read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := codeHarness{change: c.change, files: harnessDeliver("reply-draft", "after_gate")}
			if c.access != "" {
				h.stack = func(s string) string { return s + "workflows:\n  hello:\n    access: " + c.access + "\n" }
			}
			r := h.run(t)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if lines := deliverLines(r.out); len(lines) != 0 {
				t.Errorf("the run prints %q, want no deliver line\n%s", lines, r.out)
			}
		})
	}
}

// TestADeliverLineCutsALongTypeTo19Characters is a run whose documents and harness deliver
// a type of 64 characters at different sends: the line has the type's first 19 characters
// and at most 100 characters with its "qory run: ".
func TestADeliverLineCutsALongTypeTo19Characters(t *testing.T) {
	long := strings.Repeat("abcdefgh-", 7) + "x"
	r := deliverRun(t, withDeliver(long, "end_of_run"), harnessDeliver(long, "after_gate"))
	want := "qory run: deliver: " + long[:19] + " at end_of_run from the documents; harness has after_gate\n"
	lines := deliverLines(r.out)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("the run prints %q, want the one line %q\n%s", lines, want, r.out)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(lines[0], "\n")); n > 100 {
		t.Errorf("the line has %d characters, want at most 100", n)
	}
}

// TestDocumentsThatDeliverATypeTwiceStopAsBrokenDocuments is the run code's documents
// whose deliver lists pull-request twice: they pass the schema, and the reader stops them
// with the line of documents that break the schema, and the rule on line 2.
func TestDocumentsThatDeliverATypeTwiceStopAsBrokenDocuments(t *testing.T) {
	change := func(d map[string]any) {
		d["deliver"] = []any{
			map[string]any{"type": "pull-request", "send": "after_gate"},
			map[string]any{"type": "pull-request", "send": "end_of_run"},
		}
	}
	_, err := cmd.DeliverOf(codeDocuments(t, change))
	want := "update qory, or ask Qory Apiary's instance admin: the response to the run code does not pass qory's checks: it breaks documents.schema.json; the code is used\n  deliver lists pull-request twice"
	if err == nil || err.Error() != want {
		t.Errorf("the error is %v, want %q", err, want)
	}
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d, want 1", cmd.ExitCode(err))
	}
}
