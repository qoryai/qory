package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
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
// a target_version among them, with access set to write, so the workflow's access in the
// stack decides whether the run reads only, and change applied to its members.
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
	doc["access"] = "write"
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
		{"a time limit under 15 minutes", codeDocuments(t, func(d map[string]any) { d["time_limit_minutes"] = 14 }), broken},
		{"a time limit in the run settings", codeDocuments(t, func(d map[string]any) {
			d["run_settings"] = map[string]any{"runtime": "claude", "time_limit_minutes": 45}
		}), broken},
		{"a run id", codeDocuments(t, func(d map[string]any) { d["run_id"] = keyRunID }), broken},
		{"another domain in inputs", codeDocuments(t, func(d map[string]any) { d["inputs"].(map[string]any)["domain"] = "marketing" }),
			checks + "its inputs.domain is not its domain; the code is used"},
		{"another step", json.RawMessage(`{"step":"triage","domain":"software","inputs":{"version":1}}`),
			"update qory: the response to the run code is for the step triage, and qory runs a run code's run for the steps explore and prebuild only; the code is used"},
		{"prebuild with no wall", preDocs, "set wall.image, pinned by its digest, wall.user and wall.prebuild_image in forager.yaml: a prebuild from a run code builds with them; the code is used"},
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
// which the output has as <url>, the stack's commit in the documents, the calls the run
// got, and the outcomes it closed with.
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
	closes   []session.Outcome
	// image is the last value the run's Image returned, the zero Image when nothing read it.
	image session.Image
}

// codeSetup is the Setup of each run [runFromCodeGiven] opens; nil is one that succeeds. A
// test sets it.
var codeSetup func(session.SetupSpec) error

// codeFetchImage is the FetchImage of each run [runFromCodeGiven] opens; nil is one
// that succeeds. A test sets it.
var codeFetchImage func(context.Context) error

// codeAtGateway, when set, has [runFromCodeGiven] open each run at a gateway of
// Forager's in this process as well, as [cmd.Nowhere] AtGateway does. A test sets it.
var codeAtGateway bool

// codeRunAPI, when set, is the base URL of the run API of each run that [runFromCodeGiven] opens.
var codeRunAPI string

// codePolicyImage is the image the policy of each run [runFromCodeGiven] opens selects,
// as [cmd.Nowhere] PolicyImage is; "" selects none. A test sets it.
var codePolicyImage string

// codeRepositories is the run configuration's repositories of each run
// [runFromCodeGiven] opens, as [cmd.Nowhere] Repositories is; "" lists none. A test sets
// it.
var codeRepositories string

// codeReal, when set, has each run [runFromCodeGiven] opens use a real wall for its
// setup, the removal of its setup container and its join, as [cmd.Nowhere] Real does. A
// test sets it.
var codeReal bool

// codeRemoveSetup is the RemoveSetup of each run [runFromCodeGiven] opens; nil is one
// that succeeds. A test sets it.
var codeRemoveSetup func(context.Context) error

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
	files["examples/hello/qory-stack.yaml"] = withExtendable(files["examples/hello/qory-stack.yaml"])
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
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: codeRepositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Proxy.URL, StackCABundle: h.server.Proxy.CABundle,
		OnOpen:      func(s session.OpenSpec) { r.open = s },
		FetchImage:  codeFetchImage,
		Setup:       codeSetup,
		RemoveSetup: codeRemoveSetup,
		Real:        codeReal,
		AtGateway:   codeAtGateway,
		PolicyImage: codePolicyImage,
		OnImage:     func(i session.Image) { r.image = i },
		RunAPI:      codeRunAPI,
	})
	defer restore()
	if given == nil {
		r.out, r.err = run(t, append([]string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}, args...)...)
	} else {
		file, stdin := given(g.code())
		r.out, r.err = runReading(t, stdin, append([]string{"run", "--run-code-file", file, "--gateway", g.URL}, args...)...)
	}
	r.calls, r.closes = calls(), closes()
	r.out, r.url = strings.ReplaceAll(r.out, url, "<url>"), url
	return r
}

// TestARunCodeStartsItsWorkflow is a run from a run code whose answer contains a harness
// document that extends the stack examples/hello at a commit, the workflow hello and a
// model: qory composes the fetched harness with the model, prints which workflow of
// which harness starts, and starts the
// command hello, by the name Claude Code registers it under, with the path of
// inputs.json, which contains the documents' inputs as they came and the member qory that
// qory adds, with no harness extensions, mode 0600 in a folder
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
	r := runFromCodeWith(t, behindAWall(t), nil, func(s string) string { return s + "workflows:\n  hello:\n    time_limit: 7h\n    access: write\n" }, func(d map[string]any) {
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
	inputs := strings.TrimSuffix(string(doc.Inputs), "}") + `,"qory":{"harness_extensions":{}}}` + "\n"
	if r.inputs != inputs || r.modes != [2]fs.FileMode{0o700, 0o600} {
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

// TestARunCodeMapsTheTierBalanced is a run from a run code whose run settings map the
// tiers light, balanced and thinking: the run composes with each tier as a --tier flag,
// and the agent whose model is ${qory:balanced} is written with the model that the
// run settings give the tier balanced.
func TestARunCodeMapsTheTierBalanced(t *testing.T) {
	// The test reads the run's home once the run is over.
	t.Cleanup(cmd.KeepRunHomes())
	stackFile := func(s string) string {
		return s + "  - name: tiers\n    source: {path: modules/tiers}\nworkflows:\n  hello:\n    access: write\n"
	}
	extra := map[string]string{
		"examples/hello/modules/tiers/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: tiers\n",
		"examples/hello/modules/tiers/agents/mid.md":    "---\nname: mid\ndescription: Works in the middle tier.\nmodel: ${qory:balanced}\n---\n\nWorks.\n",
	}
	r := runFromCodeLinks(t, behindAWall(t), nil, stackFile, func(d map[string]any) {
		d["run_settings"] = map[string]any{
			"runtime": "claude", "model": "example-large",
			"provider_models": []any{map[string]any{"id": "example-large"}, map[string]any{"id": "example-small"}},
			"tiers":           map[string]any{"light": "example-large", "balanced": "example-small", "thinking": "example-large"},
		}
	}, extra, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	data, err := os.ReadFile(filepath.Join(r.spec.HarnessHome, "claude", "agents", "mid.md"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "model: example-small")
}

// TestARunCodeWithoutAWorkflowTimeLimitRunsThreeHours is a run from a run code whose
// base sets no time_limit for its workflow: the session's time limit is 3 hours.
func TestARunCodeWithoutAWorkflowTimeLimitRunsThreeHours(t *testing.T) {
	r := runFromCodeWith(t, behindAWall(t), nil, writesHello, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.spec.Timeout <= 3*time.Hour-time.Minute || r.spec.Timeout > 3*time.Hour {
		t.Errorf("the time limit left is %v, want 3h less the time before the join", r.spec.Timeout)
	}
}

// TestARunCodesTimeLimitMinutesIsTheSessionsTimeLimit is a run from a run code whose
// documents set time_limit_minutes at the top: the session's time limit is that many
// minutes, whether or not the workflow sets a time_limit, which applies only when the
// documents set none.
func TestARunCodesTimeLimitMinutesIsTheSessionsTimeLimit(t *testing.T) {
	for _, c := range []struct {
		name  string
		stack func(string) string
	}{
		{"no workflow time limit", writesHello},
		{"a workflow time limit of 7h", func(s string) string { return s + "workflows:\n  hello:\n    time_limit: 7h\n    access: write\n" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runFromCodeWith(t, behindAWall(t), nil, c.stack, func(d map[string]any) { d["time_limit_minutes"] = 45 })
			if !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if r.spec.Timeout <= 45*time.Minute-time.Minute || r.spec.Timeout > 45*time.Minute {
				t.Errorf("the time limit left is %v, want 45m less the time before the join", r.spec.Timeout)
			}
		})
	}
}

// TestARunCodeWithSharedRunAndFullSuiteRunsOneSession is a run from a run code whose
// documents set shared_run and full_suite to true: qory takes the documents and starts
// the run as it does without them, with the time limit of the documents.
func TestARunCodeWithSharedRunAndFullSuiteRunsOneSession(t *testing.T) {
	r := runFromCodeWith(t, behindAWall(t), nil, writesHello, func(d map[string]any) {
		d["time_limit_minutes"], d["shared_run"], d["full_suite"] = 60, true, true
	})
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.spec.Timeout <= 60*time.Minute-time.Minute || r.spec.Timeout > 60*time.Minute {
		t.Errorf("the time limit left is %v, want 60m less the time before the join", r.spec.Timeout)
	}
}

// TestARunCodeWithFullSuiteSetsTheAfterStep: a run from a run code behind a wall, whose documents
// have full_suite true and whose work item has a build plan with a full row, gives its session
// the after step with the suite's cap as its time limit. Documents with full_suite false, or with
// none, give none. No test seam sets the input.
func TestARunCodeWithFullSuiteSetsTheAfterStep(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(map[string]any)
		after  bool
	}{
		{"full_suite true", func(d map[string]any) { d["full_suite"] = true }, true},
		{"full_suite false", func(d map[string]any) { d["full_suite"] = false }, false},
		{"no full_suite", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(cmd.SetPlan("fix", "build-plan"))
			api := newSuiteAPI(t, http.StatusCreated)
			codeRunAPI = api.srv.URL + "/v1/runs/r1"
			t.Cleanup(func() { codeRunAPI = "" })
			r := runFromCodeWith(t, behindAWall(t), nil, writesHello, c.change)
			if !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if got := len(r.spec.After) > 0; got != c.after {
				t.Fatalf("After = %q, want it set: %v", r.spec.After, c.after)
			}
			if c.after {
				if r.spec.AfterTimeout != 60*time.Minute || r.spec.After[4] == "" || r.spec.After[5] != "junit.xml" {
					t.Errorf("After = %q, AfterTimeout = %v", r.spec.After, r.spec.AfterTimeout)
				}
			} else if r.spec.AfterTimeout != 0 {
				t.Errorf("AfterTimeout = %v with no After", r.spec.AfterTimeout)
			}
		})
	}
}

// TestARunCodeWhoseHarnessHasNoWorkflowStops is a run from a run code whose workflow is
// no skill or command of the composed harness: the run stops before the session joins,
// exit 1, with a line that starts with its fix, and closes failed with the reason
// workflow_not_in_harness.
func TestARunCodeWhoseHarnessHasNoWorkflowStops(t *testing.T) {
	r := runFromCode(t, nil, func(d map[string]any) { d["workflow"] = "deploy" })
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if want := []session.Outcome{{State: "failed", Reason: "workflow_not_in_harness", ExitCode: -1}}; !slices.Equal(r.closes, want) {
		t.Errorf("the run closed with %v, want %v", r.closes, want)
	}
	wants(t, r.out+r.err.Error(), "add a skill workflow-deploy to the workspace's harness, or set another workflow in Qory Apiary: the composed harness has no skill or command workflow-deploy or deploy (workflow_not_in_harness); the code is used")
}

// TestAGatewayTakesTheCloseOfARunCodeWhoseHarnessHasNoWorkflow is the run of
// [TestARunCodeWhoseHarnessHasNoWorkflowStops] opened and closed at a gateway of
// Forager's in this process: exit 1 before the join, and the gateway accepts the close,
// so the last run.exited of the gateway's record, events.jsonl, and of the session's,
// session.jsonl, is failed with workflow_not_in_harness and -1, and neither record
// contains batch_refused.
func TestAGatewayTakesTheCloseOfARunCodeWhoseHarnessHasNoWorkflow(t *testing.T) {
	codeAtGateway = true
	t.Cleanup(func() { codeAtGateway = false })
	r := runFromCode(t, nil, func(d map[string]any) { d["workflow"] = "deploy" })
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out+r.err.Error(), "(workflow_not_in_harness); the code is used")
	dir := filepath.Join(r.open.RunsDir, r.open.RunID)
	for _, file := range []string{"events.jsonl", "session.jsonl"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if got := lastExited(t, b); got != "failed/workflow_not_in_harness/-1" {
			t.Errorf("%s ends the run %s, want failed/workflow_not_in_harness/-1\n%s", file, got, b)
		}
		lacks(t, string(b), "batch_refused")
	}
	lacks(t, r.out, "batch_refused")
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

// TestAWalledRunCodeLinksClaudeCodesTranscripts is a walled run of Claude Code from a
// run code: its agent starts through qory run agent-env, and the verb's words between
// the kept names' file and -- are the folder projects of the agent's home and the place
// .claude/projects below HOME, where Forager binds the run's transcript folder.
func TestAWalledRunCodeLinksClaudeCodesTranscripts(t *testing.T) {
	r := runFromCodeWith(t, func(string) []string {
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	i := slices.Index(r.spec.Args, "agent-env")
	if r.spec.Command != wall.HelperPath || i < 0 || len(r.spec.Args) < i+5 || !slices.Equal(r.spec.Args[i+2:i+5], []string{"projects", ".claude/projects", "--"}) {
		t.Errorf("the launch is %s %q", r.spec.Command, r.spec.Args)
	}
}

// walledBy writes a forager.yaml whose wall runs docker as its docker program.
func walledBy(t *testing.T, docker string) {
	t.Helper()
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: example.com/agent:1\n")
}

// behindAWall is a before of [runFromCodeWith] for a run that may write, which needs a
// wall: it puts the wall of the fake docker in forager.yaml, and returns --home with a
// home outside the checkout.
func behindAWall(t *testing.T) func(string) []string {
	t.Helper()
	return func(string) []string {
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		return []string{"--home", filepath.Join(tempDir(t), "home")}
	}
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
		writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nif cat /dev/fd/"+strconv.Itoa(n)+" >/dev/null 2>&1; then echo open; else echo closed; fi >> "+marks+"\nexec "+shellQuote(git)+" \"$@\"\n")
		if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return append(behindAWall(t)(code), "--run-code-file", "/dev/fd/"+strconv.Itoa(n))
	}, nil, writesHello, nil)
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

// TestARunCodeStartsTheWorkflowsPrefixedSkill is a whole run from a run code whose harness
// ships the skill workflow-deploy for the workflow deploy: the run starts that skill, and
// the start line has the workflow's name without the prefix. The stack's settings of
// the workflow are under deploy, and access write there lets the run start behind a wall.
func TestARunCodeStartsTheWorkflowsPrefixedSkill(t *testing.T) {
	declares := func(s string) string { return s + "workflows:\n  deploy:\n    access: write\n" }
	r := runFromCodeLinks(t, behindAWall(t), nil, declares, func(d map[string]any) { d["workflow"] = "deploy" }, map[string]string{
		"examples/hello/modules/hello/skills/workflow-deploy/SKILL.md": "---\nname: workflow-deploy\ndescription: Deploys.\n---\nDeploy.\n",
	}, nil)
	if !r.joined {
		t.Fatalf("%v, want a join\n%s", r.err, r.out)
	}
	if prompt := strings.Join(r.spec.Args, " "); !strings.Contains(prompt, "/harness:workflow-deploy ") {
		t.Errorf("the start has %q, want the prefixed skill", prompt)
	}
	wants(t, r.out, "starts the workflow deploy of ")
	lacks(t, r.out, "the workflow workflow-deploy")
}

// TestARunCodeReadsThePlanTypeOfItsWorkflowFromTheComposedStack is the plan type read through the
// real stack, with no [cmd.SetPlan]: the base stack's workflow hello has access write and the plan
// type change-plan, and its module declares the scope check for it. The suite command is the full
// row of the work item's change-plan. A workflow with access write and no plan gets the after step
// that only checks whether HEAD moved, and a workflow that only reads gets no after step.
func TestARunCodeReadsThePlanTypeOfItsWorkflowFromTheComposedStack(t *testing.T) {
	const manifest = "apiVersion: qory.dev/v1alpha1\nname: hello\nscope_checks:\n  change-plan: [checks/check.js, scope, --plan]\n"
	fullSuite := func(d map[string]any) { d["full_suite"] = true }
	for _, c := range []struct {
		name      string
		stackFile func(string) string
		extra     map[string]string
		after     bool
		command   string
	}{
		{"a plan type", func(f string) string {
			return f + "workflows:\n  hello:\n    access: write\n    plan: change-plan\n"
		}, map[string]string{
			"examples/hello/modules/hello/qory-module.yaml": manifest,
			"examples/hello/modules/hello/checks/check.js":  "#!/usr/bin/env node\n",
		}, true, "printf"},
		{"write and no plan", writesHello, nil, true, "true"},
		{"read and no plan", func(f string) string { return f + "workflows:\n  hello:\n    access: read\n" }, nil, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newSuiteAPIOf(t, http.StatusCreated, "change-plan", suitePlan)
			codeRunAPI = api.srv.URL + "/v1/runs/r1"
			t.Cleanup(func() { codeRunAPI = "" })
			r := runFromCodeLinks(t, behindAWall(t), nil, c.stackFile, fullSuite, c.extra, nil)
			if !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if got := len(r.spec.After) > 0; got != c.after {
				t.Fatalf("After = %q, want it set: %v", r.spec.After, c.after)
			}
			if !c.after {
				if r.spec.AfterTimeout != 0 {
					t.Errorf("AfterTimeout = %v with no After", r.spec.AfterTimeout)
				}
				return
			}
			if !strings.HasPrefix(r.spec.After[4], c.command) || (c.command == "true") != (r.spec.After[4] == "true") {
				t.Errorf("the command is %q, want one that starts %q", r.spec.After[4], c.command)
			}
		})
	}
}

// TestARunWithFullSuiteAndAWorkflowThatCannotPlanStops: a run whose documents set full_suite true
// and whose workflow has write-no-push access stops after the compose, before the image's fetch,
// the setup and the join, exit 1, with the line that says the code is used, and closes failed with no reason.
// A workflow with write access, a run with full_suite false, a workflow that has write-no-push
// access and a run that only reads the checkout are not refused.
func TestARunWithFullSuiteAndAWorkflowThatCannotPlanStops(t *testing.T) {
	const line = "Remove full_suite from the run settings of workflow hello.\n  The run did not start: hello has write-no-push access, so it has no plan to read the full suite from; the code is used."
	suite := func(on bool) func(map[string]any) {
		return func(d map[string]any) { d["full_suite"] = on }
	}
	for _, c := range []struct {
		name     string
		declared string
		change   func(map[string]any)
		stops    bool
	}{
		{"write-no-push, full_suite true", "write-no-push", suite(true), true},
		{"write-no-push with documents write-no-push", "write-no-push", func(d map[string]any) { suite(true)(d); noPush(d) }, true},
		{"write, full_suite true", "write", suite(true), false},
		{"write, documents write-no-push", "write", func(d map[string]any) { suite(true)(d); noPush(d) }, false},
		{"write-no-push, full_suite false", "write-no-push", suite(false), false},
		{"read, full_suite true", "read", suite(true), false},
		{"write-no-push, documents read", "write-no-push", func(d map[string]any) { suite(true)(d); lowered(d) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runFromCodeWith(t, behindAWall(t), nil, declares(c.declared), c.change)
			if c.stops {
				checkStopped(t, r, "", "")
				wants(t, r.out+r.err.Error(), line)
				return
			}
			if strings.Contains(r.out, "full_suite") || r.err != nil && strings.Contains(r.err.Error(), "full_suite") {
				t.Errorf("the run is refused: %v\n%s", r.err, r.out)
			}
		})
	}
}
