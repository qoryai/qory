package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// codeDocuments are the documents of an answer to a run code: the contract's run/v1
// documents fixture explore-workflow.json, the workflow hello, harness_hidden true and
// a target_version among them, with change applied to its members.
func codeDocuments(t *testing.T, change func(map[string]any)) json.RawMessage {
	t.Helper()
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/documents/explore-workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestARunCodeTakesNoStackRuntimeOrArguments is a run code beside what the answer to the
// code sets: -f, a runtime, a compose flag, arguments after -- and --timeout. Each is
// refused as input, exit 2, before the code is sent.
func TestARunCodeTakesNoStackRuntimeOrArguments(t *testing.T) {
	root := runKeyCheckout(t)
	g := newCodeRelay(t)
	file := codeFile(t, g.code())
	from := "leave out --model, --effort, --tier and --tier-effort: a run from a run code takes them from Qory Apiary's response to the code"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-f", "https://code.example.com/acme/stacks#main:hello"}, "leave out -f: a run from a run code composes the harness in Qory Apiary's response to the code, or the checkout's qory.yaml or harness.yaml"},
		{[]string{"claude"}, "leave out the runtime claude: a run from a run code takes its runtime from Qory Apiary's response to the code"},
		{[]string{"--model", "example-large"}, from},
		{[]string{"--effort", "high"}, from},
		{[]string{"--tier", "light=example-small"}, from},
		{[]string{"--tier-effort", "light=low"}, from},
		{[]string{"--", "-p", "hi"}, "leave out the arguments after --: a run from a run code starts its workflow with the arguments qory sets"},
		{[]string{"--timeout", "1h"}, "leave out --timeout: a run from a run code takes its time limit from Qory Apiary's response to the code, else from its workflow"},
	} {
		args := []string{"run"}
		rest := []string{"--run-code-file", file, "--gateway", g.URL}
		if c.args[0] == "claude" {
			args = append(append(args, c.args...), rest...)
		} else {
			args = append(append(args, rest...), c.args...)
		}
		_, err := run(t, args...)
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v (exit %d), want %q", c.args, err, cmd.ExitCode(err), c.want)
		}
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("refused runs sent the code %d times", n)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// TestARunCodeWhoseAnswerQoryDoesNotRunStops is a redemption whose documents qory does
// not run: none, documents that break the schema, a stack or a stack_reference, which
// the schema does not allow, a step other than explore and prebuild, a prebuild on a
// machine whose forager.yaml has no wall, no workflow, a runtime with no headless start,
// and a runtime qory does not render.
// Each stops the run after the one redemption, before it opens, with a line that starts
// with its fix and ends "; the code is used": exit 2 for an answer with no documents,
// which Forager refuses, and exit 1 for the rest.
func TestARunCodeWhoseAnswerQoryDoesNotRunStops(t *testing.T) {
	const checks = "update qory, or ask Qory Apiary's instance admin: the response to the run code does not pass qory's checks: "
	const broken = checks + "it breaks documents.schema.json; the code is used"
	preDocs, err := fs.ReadFile(contracts.RunFS, "fixtures/documents/prebuild.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		documents json.RawMessage
		want      string
	}{
		{"none", nil, "use another run code: the enrolment answer has no documents"},
		{"an unknown member", codeDocuments(t, func(d map[string]any) { d["prompt"] = "x" }), broken},
		{"a time limit under 15 minutes", codeDocuments(t, func(d map[string]any) {
			d["run_settings"] = map[string]any{"runtime": "claude", "time_limit_minutes": 14}
		}), broken},
		{"a run id", codeDocuments(t, func(d map[string]any) { d["run_id"] = keyRunID }), broken},
		{"another domain in inputs", codeDocuments(t, func(d map[string]any) { d["inputs"].(map[string]any)["domain"] = "marketing" }),
			checks + "its inputs.domain is not its domain; the code is used"},
		{"another step", json.RawMessage(`{"step":"triage","domain":"software","inputs":{"version":1}}`),
			"update qory: the response to the run code is for the step triage, and qory runs a run code's run for the steps explore and prebuild only; the code is used"},
		{"prebuild with no wall", preDocs, "set wall.image, pinned by its digest, wall.user and wall.helper in forager.yaml: a prebuild from a run code builds with them; the code is used"},
		{"no workflow", codeDocuments(t, func(d map[string]any) { delete(d, "workflow"); delete(d, "harness_hidden") }),
			"set a workflow for the workspace in Qory Apiary, then take the work item in again: the response to the run code has no workflow; the code is used"},
		{"a stack", codeDocuments(t, func(d map[string]any) { d["stack"] = "core" }), broken},
		{"a stack reference", codeDocuments(t, func(d map[string]any) {
			d["stack"], d["stack_reference"] = "core", "https://code.example.com/team/stacks#release-7:core"
		}), broken},
		{"a runtime with no headless start", codeDocuments(t, func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "goose"} }),
			"set another runtime for the workspace in Qory Apiary, then take the work item in again: qory starts a run code's workflow with a runtime's headless start, and has none for goose; the code is used"},
		{"a runtime qory does not render", codeDocuments(t, func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "example"} }),
			"set another runtime for the workspace in Qory Apiary, then take the work item in again: qory starts a run code's workflow with a runtime's headless start, and has none for example; the code is used"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := runKeyCheckout(t)
			g := newCodeRelay(t)
			g.documents = c.documents
			exit := 1
			if c.documents == nil {
				exit = cmd.ExitInput
			}
			out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
			if err == nil || err.Error() != c.want || cmd.ExitCode(err) != exit {
				t.Errorf("%v (exit %d), want %q, exit %d\n%s", err, cmd.ExitCode(err), c.want, exit, out)
			}
			if n := len(g.sent()); n != 1 {
				t.Errorf("the code was sent %d times", n)
			}
			lacks(t, out, "qory run: through the gateway")
			if ids := recorded(t, root); len(ids) != 0 {
				t.Errorf("records %v", ids)
			}
			key := g.runKey()
			lacks(t, out+err.Error(), key, key[4:40], "F1XT")
		})
	}
}

// codeRun is what a run from a run code came to, opened nowhere: its output and error,
// the run's spec at the open, the session's spec at the join, the inputs.json and the
// modes the join found, the folder of the inputs, the URL of the stack's repository,
// which the output has as <url>, the stack's commit in the documents, and the calls the
// run got.
type codeRun struct {
	out      string
	err      error
	open     session.OpenSpec
	spec     session.SessionSpec
	joined   bool
	inputs   string
	modes    [2]fs.FileMode
	inputDir string
	url      string
	commit   string
	calls    []string
}

// codeFetchImage is the FetchImage of each run [runFromCodeGiven] opens; nil is one
// that succeeds. A test sets it.
var codeFetchImage func(context.Context) error

// runFromCode runs qory run from a run code whose documents start the workflow of a
// harness document that extends the stack examples/hello of a repository that the
// run's stack proxy serves, with the stack file changed by stackFile and the documents
// by change, and opens the run nowhere.
func runFromCode(t *testing.T, stackFile func(string) string, change func(map[string]any)) codeRun {
	t.Helper()
	return runFromCodeWith(t, nil, nil, stackFile, change)
}

// runFromCodeWith is [runFromCode] with before run first, once the checkout is made,
// with the run code, and the flags args, then the flags before returns, after the run
// code's.
func runFromCodeWith(t *testing.T, before func(code string) []string, args []string, stackFile func(string) string, change func(map[string]any)) codeRun {
	t.Helper()
	return runFromCodeLinks(t, before, args, stackFile, change, nil, nil)
}

// runFromCodeLinks is [runFromCodeWith] for a repository that has the files extra and
// links as well: each key of extra is a file's path in the repository and its value the
// file's text, and each key of links a link's path and its value the link's target.
func runFromCodeLinks(t *testing.T, before func(code string) []string, args []string, stackFile func(string) string, change func(map[string]any), extra, links map[string]string) codeRun {
	t.Helper()
	return runFromCodeGiven(t, before, args, stackFile, change, extra, links, nil)
}

// runFromCodeGiven is [runFromCodeLinks] with the run code given by given, which gets
// the code and returns the --run-code-file and the command's stdin; with given nil, the
// code is in a file of a new temporary directory, and stdin is that of [run].
func runFromCodeGiven(t *testing.T, before func(code string) []string, args []string, stackFile func(string) string, change func(map[string]any), extra, links map[string]string, given func(code string) (string, io.Reader)) codeRun {
	t.Helper()
	files := helloFiles(t)
	maps.Copy(files, extra)
	if stackFile != nil {
		files["examples/hello/qory-stack.yaml"] = stackFile(files["examples/hello/qory-stack.yaml"])
	}
	files["examples/hello/qory-stack.yaml"] += extendable
	h := newHostFetch(t, "", "main")
	commit := h.server.RepositoryWithLinks(t, "example-org/hello", "main", files, links)
	url := h.server.URL("example-org/hello")
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		d["harness"] = map[string]any{"harness": map[string]any{"extends": map[string]any{"git": url, "ref": commit, "path": "examples/hello"}}}
		if change != nil {
			change(d)
		}
	})
	r := codeRun{commit: commit}
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) {
		r.spec, r.joined = s, true
		if i := slices.Index(s.Args, "--add-dir"); i >= 0 && i+1 < len(s.Args) {
			r.inputDir = s.Args[i+1]
			b, _ := os.ReadFile(filepath.Join(r.inputDir, "inputs.json"))
			r.inputs = string(b)
			for j, p := range []string{r.inputDir, filepath.Join(r.inputDir, "inputs.json")} {
				if st, err := os.Stat(p); err == nil {
					r.modes[j] = st.Mode().Perm()
				}
			}
		}
	}))
	t.Setenv("QORY_RUN_KEY", "")
	if before != nil {
		args = append(slices.Clip(args), before(g.code())...)
	}
	_, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Proxy.URL, StackCABundle: h.server.Proxy.CABundle,
		OnOpen:     func(s session.OpenSpec) { r.open = s },
		FetchImage: codeFetchImage,
	})
	defer restore()
	if given == nil {
		r.out, r.err = run(t, append([]string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}, args...)...)
	} else {
		file, stdin := given(g.code())
		r.out, r.err = runReading(t, stdin, append([]string{"run", "--run-code-file", file, "--gateway", g.URL}, args...)...)
	}
	r.calls = calls()
	r.out, r.url = strings.ReplaceAll(r.out, url, "<url>"), url
	return r
}

// TestARunCodeStartsItsWorkflow is a run from a run code whose answer contains a harness
// document that extends the stack examples/hello at a commit, the workflow hello and a
// model: qory composes the fetched harness with the model, prints which workflow of
// which harness starts, and starts the
// command hello, by the name Claude Code registers it under, with the path of
// inputs.json, which contains the documents' inputs as they came, mode 0600 in a folder
// of mode 0700, which the agent may read and qory removes once the run ends. The
// workflow's time_limit, 7h, is the session's time limit.
func TestARunCodeStartsItsWorkflow(t *testing.T) {
	want := codeDocuments(t, nil)
	var doc struct {
		Inputs json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(want, &doc); err != nil {
		t.Fatal(err)
	}
	r := runFromCode(t, func(s string) string { return s + "workflows:\n  hello:\n    time_limit: 7h\n" }, func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "claude", "model": "example-large", "provider_models": []any{map[string]any{"id": "example-large"}}}
	})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: the run "+keyRunID+" starts the workflow hello of the workspace's harness on claude\n", "qory run: composed ")
	if r.inputDir == "" {
		t.Fatalf("no --add-dir in %q", r.spec.Args)
	}
	tail := []string{"--output-format", "stream-json", "--verbose", "--add-dir", r.inputDir, "-p", "/harness:hello " + filepath.Join(r.inputDir, "inputs.json")}
	if got := r.spec.Args[len(r.spec.Args)-len(tail):]; !slices.Equal(got, tail) {
		t.Errorf("the arguments end %q, want %q\n%q", got, tail, r.spec.Args)
	}
	if !slices.Contains(r.spec.Args, "--strict-mcp-config") {
		t.Errorf("not a dispatched run's arguments: %q", r.spec.Args)
	}
	if r.inputs != string(doc.Inputs) || r.modes != [2]fs.FileMode{0o700, 0o600} {
		t.Errorf("inputs.json %q, modes %v", r.inputs, r.modes)
	}
	if _, err := os.Stat(r.inputDir); !os.IsNotExist(err) {
		t.Errorf("the inputs are left after the run: %v", err)
	}
	if r.spec.Timeout <= 7*time.Hour-time.Minute || r.spec.Timeout > 7*time.Hour {
		t.Errorf("the time limit left is %v, want 7h less the time before the join", r.spec.Timeout)
	}
	if i := slices.Index(r.spec.Args, "--settings"); i < 0 || i+1 >= len(r.spec.Args) || !strings.Contains(r.spec.Args[i+1], `"model": "example-large"`) {
		t.Errorf("Claude Code's settings have no model example-large: %q", r.spec.Args)
	}
}

// TestARunCodeWithoutAWorkflowTimeLimitRunsThreeHours is a run from a run code whose
// base sets no time_limit for its workflow: the session's time limit is 3 hours.
func TestARunCodeWithoutAWorkflowTimeLimitRunsThreeHours(t *testing.T) {
	r := runFromCode(t, nil, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.spec.Timeout <= 3*time.Hour-time.Minute || r.spec.Timeout > 3*time.Hour {
		t.Errorf("the time limit left is %v, want 3h less the time before the join", r.spec.Timeout)
	}
}

// TestARunCodeWhoseHarnessHasNoWorkflowStops is a run from a run code whose workflow is
// no skill or command of the composed harness: the run stops before the session joins,
// exit 1, with a line that starts with its fix.
func TestARunCodeWhoseHarnessHasNoWorkflowStops(t *testing.T) {
	r := runFromCode(t, nil, func(d map[string]any) { d["workflow"] = "deploy" })
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out+r.err.Error(), "add a skill or command deploy to the workspace's harness, or set another workflow in Qory Apiary: the composed harness has no skill or command deploy; the code is used")
}

// TestAWalledRunCodeMountsItsInputsReadOnly is a walled run from a run code: the folder
// of inputs.json is mounted into the container read-only, at the same path.
func TestAWalledRunCodeMountsItsInputsReadOnly(t *testing.T) {
	r := runFromCodeWith(t, func(string) []string {
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !slices.Contains(r.spec.Mounts, wall.Mount{Path: r.inputDir, ReadOnly: true}) {
		t.Errorf("the mounts %+v have no read-only %s", r.spec.Mounts, r.inputDir)
	}
}

// walledBy writes a forager.yaml whose wall runs docker as its docker program.
func walledBy(t *testing.T, docker string) {
	t.Helper()
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: example.com/agent:1\n")
}

// TestARunCodeDescriptorIsClosedBeforeTheRunStartsAProgram is a run code that its
// starter passes as qory run pool does: in an unlinked file on an inherited descriptor
// without close-on-exec, given as /dev/fd/N. qory reads the code and closes the
// descriptor, so git, which the run starts to fetch its stack, finds it closed.
func TestARunCodeDescriptorIsClosedBeforeTheRunStartsAProgram(t *testing.T) {
	marks := filepath.Join(tempDir(t), "marks")
	var fd int
	r := runFromCodeWith(t, func(code string) []string {
		file := filepath.Join(tempDir(t), "runcode")
		writeFile(t, file, code)
		n, err := syscall.Open(file, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fd = n
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := tempDir(t)
		writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nif cat /dev/fd/"+strconv.Itoa(n)+" >/dev/null 2>&1; then echo open; else echo closed; fi >> "+marks+"\nexec "+git+" \"$@\"\n")
		if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return []string{"--run-code-file", "/dev/fd/" + strconv.Itoa(n)}
	}, nil, nil, nil)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	b, err := os.ReadFile(marks)
	if err != nil {
		t.Fatalf("the run started no git: %v", err)
	}
	if got := strings.Fields(string(b)); len(got) == 0 || slices.Contains(got, "open") {
		syscall.Close(fd)
		t.Errorf("git found the run code's descriptor %d open: %q", fd, got)
	}
}
