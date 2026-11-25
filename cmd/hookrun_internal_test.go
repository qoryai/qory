package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/hookrun"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
)

// hookStep is a host step that a [hookFakeRun] started: its kind, name, lines and end.
type hookStep struct {
	kind, name string
	lines      []string
	state      string
	code       int
}

func (s *hookStep) Log(b []byte) error { s.lines = append(s.lines, string(b)); return nil }
func (s *hookStep) Exit(state string, code int) error {
	s.state, s.code = state, code
	return nil
}

// hookFakeRun is an opened run with a fake run API, the host steps it started, and a setup
// container that RemoveSetup removes.
type hookFakeRun struct {
	apiRun
	steps   *[]*hookStep
	removed *int
	kept    bool
	// image is the run's image, "" for the image of [apiRun].
	image string
	// remove, when not nil, is called by RemoveSetup, which returns its error.
	remove func(context.Context) error
}

func (r hookFakeRun) StartStep(kind, name string) (hostStep, error) {
	s := &hookStep{kind: kind, name: name}
	*r.steps = append(*r.steps, s)
	return s, nil
}
func (r hookFakeRun) Image() session.Image {
	if r.image != "" {
		return session.Image{Ref: r.image}
	}
	return r.apiRun.Image()
}
func (r hookFakeRun) RemoveSetup(ctx context.Context) error {
	*r.removed++
	if r.remove != nil {
		return r.remove(ctx)
	}
	return nil
}
func (r hookFakeRun) ContainerKept() bool { return r.kept }

// limitCtx is a context whose deadline a test ends with end, as the setup's limit ends a run's
// context: Err is [context.DeadlineExceeded] from then on.
type limitCtx struct {
	context.Context
	done chan struct{}
}

func newLimitCtx() *limitCtx {
	return &limitCtx{Context: context.Background(), done: make(chan struct{})}
}
func (c *limitCtx) end()                  { close(c.done) }
func (c *limitCtx) Done() <-chan struct{} { return c.done }
func (c *limitCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// hookCase is a session with hooks: a checkout with a commit, a home with hooks.json, a run
// with a fake run API, and the containers that the hooks ran on this machine in place of the
// engine.
type hookCase struct {
	checkout, commit string
	api              *fakeAPI
	steps            []*hookStep
	removed          int
	ran              []gitContainer
	stderr           bytes.Buffer
	said             []string
	setup            hookSetup
}

// hookGit runs git in dir with a configuration of the test's alone, and returns its output.
func hookGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// hookFile writes content to path below dir, mode 0755, with its folders.
func hookFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newHookCase is a walled session whose checkout has files committed, whose home's hooks.json
// has hooks, and whose agent wrote no verdict. Each hook's container runs its program on this
// machine, in the checkout, as [runHookContainer] would run it in the run's image.
func newHookCase(t *testing.T, files map[string]string, hooks ...compose.Hook) *hookCase {
	t.Helper()
	c := &hookCase{checkout: t.TempDir(), api: newFakeAPI(t)}
	hookGit(t, c.checkout, "init", "--quiet")
	for path, content := range files {
		hookFile(t, c.checkout, path, content)
	}
	hookGit(t, c.checkout, "add", "-A")
	hookGit(t, c.checkout, "commit", "--quiet", "--allow-empty", "-m", "first")
	c.commit = hookGit(t, c.checkout, "rev-parse", "HEAD")
	home := t.TempDir()
	data, err := json.Marshal(render.Hooks{Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, render.HooksFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	was := runHookContainer
	t.Cleanup(func() { runHookContainer = was })
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		c.ran = append(c.ran, g)
		cmd := exec.CommandContext(ctx, g.Session.Command, g.Session.Args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = g.Session.Dir, g.Output, g.Output
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return -1, ctx.Err()
		case errors.As(err, &exit):
			return exit.ExitCode(), nil
		case err != nil:
			return -1, err
		}
		return 0, nil
	}
	run := hookFakeRun{apiRun: apiRun{api: c.api, dir: t.TempDir()}, steps: &c.steps, removed: &c.removed}
	c.setup = hookSetup{
		run: run, spec: &runSpec{}, runID: "r1", walled: true, home: home, checkout: c.checkout,
		workflow: "review-changes", number: 1, commit: c.commit, start: c.commit, stderr: &c.stderr,
		said: func(line string) { c.said = append(c.said, line) },
	}
	return c
}

// open is [openHooks] for the case's session, before its join.
func (c *hookCase) open(t *testing.T) *sessionHooks {
	t.Helper()
	h, err := openHooks(context.Background(), c.setup)
	if err != nil || h == nil {
		t.Fatalf("openHooks: %v, %v", h, err)
	}
	return h
}

// end is [sessionHooks.after] for the case's session that succeeded, with no stop and no limit.
func (c *hookCase) end(t *testing.T, h *sessionHooks) {
	t.Helper()
	if got, err := h.after(context.Background(), context.Background(), time.Time{}, succeeded, false); err != nil || got != succeeded {
		t.Fatalf("outcome %+v, error %v, want the session's", got, err)
	}
}

// records is the records file of the case's session.
func (c *hookCase) records(t *testing.T) []hookrun.Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.setup.run.Dir(), hookrun.Folder, fmt.Sprintf("session-%d.json", c.setup.number)))
	if err != nil {
		t.Fatal(err)
	}
	var got hookrun.Records
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got.Hooks
}

// logged is every line of the case's steps and stderr.
func (c *hookCase) logged() string {
	var all []string
	for _, s := range c.steps {
		all = append(all, s.lines...)
	}
	return strings.Join(all, "") + c.stderr.String()
}

// checkHook is a required check hook at after-review-round, the end of review-changes, that
// runs run.
func checkHook(index int, name, run string) compose.Hook {
	return compose.Hook{
		Workflow: "review-changes", At: "after-review-round", PointAt: hookrun.PointEnd, Kind: "check",
		Name: name, Run: strings.Fields(run), Required: true, TimeoutSeconds: 600, Index: index,
	}
}

// red is a script that prints a line and exits 1.
const redScript = "#!/bin/sh\necho checking\nexit 1\n"

// verdictOf is a verdict version of the run r1 with content.
func verdictOf(content string) fakeVersion {
	return fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: content}
}

const hookPointRed = `{"text":"The hook check.sh failed: it exited with status 1.","action":"run_again"}`

// TestARedHookAfterAReviewRoundEndsTheSessionChangesRequested: a check hook at
// after-review-round that exits 1, after a review round whose agent posted passed, or posted
// nothing, posts changes-requested with the hook's point; over the agent's changes-requested it
// adds its point to the agent's. The run goes on as the session ended, and the hook's record
// is red with its finding.
func TestARedHookAfterAReviewRoundEndsTheSessionChangesRequested(t *testing.T) {
	agentPoint := `{"text":"Fix the parser.","action":"run_again"}`
	for _, c := range []struct{ name, agent, want string }{
		{"passed", `{"verdict":"passed"}`, `{"verdict":"changes-requested","points":[` + hookPointRed + `]}`},
		{"no verdict", "", `{"verdict":"changes-requested","points":[` + hookPointRed + `]}`},
		{"changes-requested", `{"verdict":"changes-requested","points":[` + agentPoint + `]}`,
			`{"verdict":"changes-requested","points":[` + agentPoint + `,` + hookPointRed + `]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
			if c.agent != "" {
				hc.api.add(verdictOf(c.agent))
			}
			h := hc.open(t)
			if len(hc.ran) != 0 {
				t.Fatal("a hook at the end ran before the join")
			}
			hc.end(t, h)
			if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != c.want {
				t.Errorf("posted %q, want %s", posted, c.want)
			}
			records := hc.records(t)
			if len(records) != 1 || records[0].Verdict != hookrun.Red || records[0].ExitCode != 1 || records[0].Reviewer != "hook-0" {
				t.Fatalf("records %+v", records)
			}
			if f := records[0].Findings; len(f) != 1 || f[0].Proof != "failing check: hook check.sh exited 1" || f[0].ID == "" || f[0].Severity != "blocking" {
				t.Errorf("findings %+v", f)
			}
			if hc.removed != 1 {
				t.Errorf("the setup container was removed %d times before the hooks, want 1", hc.removed)
			}
			step := hc.steps[len(hc.steps)-1]
			if step.kind != event.StepBuild || step.name != "hook check.sh at after-review-round" || step.state != event.StateFailed || step.code != 1 {
				t.Errorf("the step %+v", step)
			}
			if !strings.Contains(strings.Join(step.lines, ""), "checking\n") {
				t.Errorf("the step's lines %q have no output of the hook", step.lines)
			}
		})
	}
}

// TestARedHookLeavesEscalateEscalate: over the agent's escalate, a red hook adds its point and
// the verdict stays escalate.
func TestARedHookLeavesEscalateEscalate(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	question := `{"text":"Pick one.","action":"answer","question":"q1"}`
	hc.api.add(verdictOf(`{"verdict":"escalate","points":[` + question + `]}`))
	hc.end(t, hc.open(t))
	want := `{"verdict":"escalate","points":[` + question + `,` + hookPointRed + `]}`
	if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAGreenHookPostsNothing: a hook that exits 0 is green, and the verdict stays.
func TestAGreenHookPostsNothing(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.end(t, hc.open(t))
	if posted := hc.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Green {
		t.Errorf("records %+v", r)
	}
}

// TestAHookOverItsLimitIsRed: a hook with a timeout of 1 second whose program sleeps 5 ends at
// its limit, red, and its point has the limit.
func TestAHookOverItsLimitIsRed(t *testing.T) {
	hook := checkHook(0, "slow.sh", "scripts/slow.sh")
	hook.TimeoutSeconds = 1
	hc := newHookCase(t, map[string]string{"scripts/slow.sh": "#!/bin/sh\nexec sleep 5\n"}, hook)
	h := hc.open(t)
	start := time.Now()
	hc.end(t, h)
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("the hook ran %v, past its limit", took)
	}
	r := hc.records(t)
	if len(r) != 1 || r[0].Verdict != hookrun.Red || !strings.Contains(r[0].Cause, "limit of 1 seconds") {
		t.Fatalf("records %+v", r)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"The hook slow.sh failed: it ran past its limit of 1 seconds.","action":"run_again"}]}`
	if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
	if f := r[0].Findings[0]; f.Fix != "Change the work so that the hook slow.sh passes." || f.Proof != "failing check: hook slow.sh ran past its limit of 1 seconds" {
		t.Errorf("the finding's fix %q and proof %q", f.Fix, f.Proof)
	}
}

// TestAHookWithNoTimeoutHasTenMinutes: a hook whose timeout_seconds is 0 runs with the limit
// of 10 minutes.
func TestAHookWithNoTimeoutHasTenMinutes(t *testing.T) {
	hook := checkHook(0, "check.sh", "scripts/check.sh")
	hook.TimeoutSeconds = 0
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, hook)
	var left time.Duration
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		deadline, _ := ctx.Deadline()
		left = time.Until(deadline)
		return 0, nil
	}
	hc.end(t, hc.open(t))
	if left <= 9*time.Minute || left > 10*time.Minute {
		t.Errorf("the hook had %v, want 10 minutes", left)
	}
}

// TestAHookWhoseContainerStaysEndsTheRunWithNoPush: a hook past its limit whose container's
// removal failed may still write the checkout: no hook runs after it, no push follows, and the
// session ends failed with no end code. The line has the cause alone, with docker rm's output
// when there is one, else its error.
func TestAHookWhoseContainerStaysEndsTheRunWithNoPush(t *testing.T) {
	daemon := &removeError{name: "qory-hook-r1", err: errors.New("exit status 1"), out: "Error response from daemon: device or resource busy"}
	for _, c := range []struct {
		name    string
		removal error
		wants   string
	}{
		{"docker printed a line", daemon,
			"qory run: Hook slow.sh's container was not removed, so no push follows.\n  docker rm --force qory-hook-r1 failed: Error response from daemon: device or resource busy\n"},
		{"docker printed nothing", &removeError{name: "qory-hook-r1", err: errors.New("signal: killed")},
			"qory run: Hook slow.sh's container was not removed, so no push follows.\n  docker rm --force qory-hook-r1 failed: signal: killed\n"},
		{"another error", errors.New("docker rm failed"),
			"qory run: Hook slow.sh's container was not removed, so no push follows.\n  docker rm --force qory-hook-r1 failed: docker rm failed\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hook := checkHook(0, "slow.sh", "scripts/slow.sh")
			hook.TimeoutSeconds = 1
			second := checkHook(1, "second.sh", "scripts/slow.sh")
			second.TimeoutSeconds = 1
			hc := newHookCase(t, map[string]string{"scripts/slow.sh": "#!/bin/sh\n"}, hook, second)
			ran := 0
			runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
				ran++
				<-ctx.Done()
				return -1, errors.Join(ctx.Err(), c.removal)
			}
			h := hc.open(t)
			got, err := h.after(context.Background(), context.Background(), time.Time{}, succeeded, false)
			if err == nil || got.State != event.StateFailed || got.Reason != "" || h.restored() {
				t.Errorf("outcome %+v, error %v, restored %v: want a failed session with no push", got, err, h.restored())
			}
			if ran != 1 {
				t.Errorf("%d containers ran, want 1", ran)
			}
			if lines := hc.logged(); !strings.Contains(lines, c.wants) {
				t.Errorf("the lines:\n%s", lines)
			}
		})
	}
}

// TestAStopDuringAHookAtTheStartStartsNoSession: a stop of qory run while a hook at the start
// runs returns the stop's error from openHooks, so the run ends as stopped before the join.
func TestAStopDuringAHookAtTheStartStartsNoSession(t *testing.T) {
	hook := compose.Hook{
		Workflow: "review-changes", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "check.sh", Run: []string{"scripts/check.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
	}
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\n"}, hook)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runHookContainer = func(c context.Context, g gitContainer) (int, error) {
		cancel()
		return -1, c.Err()
	}
	h, err := openHooks(ctx, hc.setup)
	if h != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("openHooks: %v, %v, want the stop", h, err)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeStopped {
		t.Errorf("records %+v", r)
	}
}

// TestAHooksOutputGoesToItsStepAlone: the output of a hook goes into its step through a writer
// that writes no line to the status descriptor, and a container that did not start, whose
// last line the program can print, gets a fixed cause; the line is in the step's log.
func TestAHooksOutputGoesToItsStepAlone(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	fake := "qory run: posted the verdict passed"
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		if lines, ok := g.Output.(*stepLines); !ok || !lines.program {
			t.Errorf("the container's output goes to %T, want a program's step lines", g.Output)
		}
		return -1, errors.New(fake)
	}
	hc.end(t, hc.open(t))
	r := hc.records(t)
	if len(r) != 1 || r[0].Cause != causeNoStart || strings.Contains(r[0].Findings[0].Point, "posted") {
		t.Fatalf("records %+v", r)
	}
	if lines := hc.logged(); !strings.Contains(lines, fake+"\n") || !strings.Contains(lines, "qory run: the hook check.sh is red.\n  "+causeNoStart+"\n") {
		t.Errorf("the lines:\n%s", lines)
	}
}

// TestAStepKeepsTheStartOfALongLine: a line of a step's output keeps its first 64 KiB in the log,
// with the mark of the cut, and the lines after it are whole.
func TestAStepKeepsTheStartOfALongLine(t *testing.T) {
	step := &hookStep{}
	w := &stepLines{stderr: io.Discard, step: step, program: true}
	long := strings.Repeat("x", maxStepLine+100)
	fmt.Fprint(w, long[:1000])
	fmt.Fprint(w, long[1000:]+"\nnext\nlast")
	w.flush()
	want := []string{long[:maxStepLine] + textLineCut + "\n", "next\n", "last"}
	if !slices.Equal(step.lines, want) {
		t.Errorf("%d lines, of %d, %d and %d bytes", len(step.lines), len(step.lines[0]), len(want[0]), len(step.lines)-1)
	}
	if len(w.buf) != 0 || cap(w.buf) > 2*maxStepLine {
		t.Errorf("the buffer has %d bytes and room for %d", len(w.buf), cap(w.buf))
	}
}

// TestAValueAcrossTheCutOfALineIsMasked: a value of the mask list that starts before the cut of
// a long line of a program's output and ends past it leaves none of its bytes in the log, and
// *** before the mark of the cut.
func TestAValueAcrossTheCutOfALineIsMasked(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	secret := "token-" + strings.Repeat("k", 100)
	secretmask.Default().Add(secret)
	step := &hookStep{}
	w := &stepLines{stderr: io.Discard, step: step, program: true}
	start := strings.Repeat("a", maxStepLine-50)
	fmt.Fprint(w, start+secret+"tail\n")
	w.flush()
	if want := []string{start + secretmask.Placeholder + textLineCut + "\n"}; !slices.Equal(step.lines, want) {
		t.Errorf("%d lines; the first ends %q", len(step.lines), step.lines[0][max(0, len(step.lines[0])-80):])
	}
}

// textOf is n bytes of text that does not repeat, each one of chars, from the SHA-256 hashes
// of seed and a count.
func textOf(seed, chars string, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		for _, c := range sha256.Sum256([]byte(fmt.Sprint(seed, i))) {
			if b.Len() < n {
				b.WriteByte(chars[int(c)%len(chars)])
			}
		}
	}
	return b.String()
}

// TestALongLineKeepsNoByteOfASecretPastTheCut: a long line of a program's output keeps, masked,
// its first 64 KiB, with no byte of a secret that crosses the cut or starts past it.
func TestALongLineKeepsNoByteOfASecretPastTheCut(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const hex = "0123456789abcdef"
	a, b := textOf("a", hex, secretmask.MaxValueLength), textOf("b", hex, secretmask.MaxValueLength)
	// Each byte of c is one that JSON escapes as \u00XX, so its JSON-escaped form is the longest.
	c := textOf("c", "<>&", secretmask.MaxValueLength)
	for _, v := range []string{a, b, c, "fakevalue123", `fake"value123`, "%fakevalue1"} {
		secretmask.Default().Add(v)
	}
	// A query value with the JSON form of fake"value123 across the cut, then copies of that
	// form up to 12 bytes into one at the end of the buffer.
	const quoted = `fake\"value123`
	query := strings.Repeat("y", maxStepLine-17) + "?token=" + quoted
	query += strings.Repeat("z", (maxStepLine+lookahead-len(query)-12)%len(quoted))
	query += strings.Repeat(quoted, (maxStepLine+lookahead-len(query))/len(quoted)+1) + " end"
	jsonC := strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(c)
	percentC := strings.NewReplacer("<", "%3C", ">", "%3E", "&", "%26").Replace(c)
	pad := func(n int) string { return strings.Repeat(".", n) }
	colours := strings.Repeat("\x1b[31m", 2000)
	const stars = secretmask.Placeholder
	// The mask of each empty sig value adds ***, so the kept part is longer than 64 KiB.
	lengthens, longer := strings.Repeat("?sig=&", 5000), strings.Repeat("?sig=***&", 5000)
	for _, tc := range []struct{ name, line, want string }{
		{"two secrets, the second past the cut",
			a + pad(maxStepLine+secretmask.MaxValueLength-100-len(a)) + b, stars + pad(maxStepLine-len(a))},
		{"two secrets, the second across the cut",
			a + pad(maxStepLine-100-len(a)) + b + "tail", stars + pad(maxStepLine-100-len(a)) + stars},
		{"escape sequences before a secret past the cut",
			colours + pad(maxStepLine+10-len(colours)) + b, pad(maxStepLine - len(colours))},
		{"escape sequences before a secret across the cut",
			colours + pad(maxStepLine-100-len(colours)) + b + "tail", pad(maxStepLine-100-len(colours)) + stars},
		{"a JSON-escaped secret across the cut",
			pad(maxStepLine-100) + jsonC + "tail", pad(maxStepLine-100) + stars},
		{"a percent-encoded secret across the cut",
			pad(maxStepLine-100) + percentC + "tail", pad(maxStepLine-100) + stars},
		{"a bearer token across the cut",
			pad(maxStepLine-50) + "Bearer " + textOf("t", hex, 200) + " tail", pad(maxStepLine-50) + stars},
		{"a token query value across the cut",
			pad(maxStepLine-50) + "https://h.example/x?token=" + textOf("q", hex, 200) + "&b=1",
			pad(maxStepLine-50) + "https://h.example/x?token=" + stars},
		{"a secret across the cut of a line that the masks make longer",
			lengthens + pad(maxStepLine-100-len(lengthens)) + b + "tail", (longer + pad(maxStepLine))[:maxStepLine]},
		{"escape sequences after the cut move it back",
			pad(maxStepLine-100) + b + strings.Repeat("\x1b[0m", 6000) + "tail", pad(maxStepLine - 100 + len(b) - lookahead)},
		{"stars before a secret across the cut",
			pad(maxStepLine-102) + "**" + b + "tail", pad(maxStepLine-102) + stars},
		{"a secret split by an escape sequence with : across the cut",
			pad(maxStepLine-6) + "fake\x1b[38:5:1mvalue123" + "tail", pad(maxStepLine-6) + stars},
		{"a secret split by nested escape sequences across the cut",
			pad(maxStepLine-10) + "fake\x1b[\x1b[\x1b[0m1m1mvalue123" + "tail", pad(maxStepLine-10) + stars},
		{"a secret right after the start of an escape sequence across the cut",
			pad(maxStepLine-5) + "\x1b[fakevalue123" + " end", pad(maxStepLine - 5)},
		{"a query value across the cut that the mask of a value makes reach the end of the buffer",
			query, strings.Repeat("y", maxStepLine-17) + "?token=" + stars},
		{"a secret split by a title that ends past the buffer",
			pad(maxStepLine-4) + "fake\x1b]0;" + pad(lookahead) + "\x07value123", pad(maxStepLine - lookahead)},
		{"a sequence that key shapes keep open from before the cut past the buffer",
			pad(maxStepLine-100) + "fake\x1b[" + strings.Repeat("AKIA"+strings.Repeat("0", 256), 100) + "Xvalue123 end",
			pad(maxStepLine - 96 - lookahead)},
		{"a sequence that masks keep open up to a copy that the end of the buffer cuts",
			pad(maxStepLine-106) + "fake\x1b[" + strings.Repeat("%25fakevalue1", 2000) + "mvalue123 end",
			pad(maxStepLine - 102 - lookahead)},
		{"a secret across the cut split by a sequence that a copy at the end of the buffer ends",
			pad(maxStepLine-4) + "fakeva\x1b[" + strings.Repeat("%25fakevalue1", 2000) + "mlue123 end",
			pad(maxStepLine + 2 - lookahead)},
		{"an escape sequence across the cut",
			pad(maxStepLine-2) + "\x1b[31m" + "tail", pad(maxStepLine - 2)},
	} {
		step := &hookStep{}
		w := &stepLines{stderr: io.Discard, step: step, program: true}
		fmt.Fprint(w, tc.line+"\n")
		w.flush()
		if want := []string{tc.want + textLineCut + "\n"}; !slices.Equal(step.lines, want) {
			got := strings.Join(step.lines, "")
			t.Errorf("%s: %d lines of %d bytes, want %d bytes; the end is %q", tc.name, len(step.lines), len(got), len(want[0]), got[max(0, len(got)-60):])
		}
	}
}

// TestALongLineWithNoSequenceOpenKeeps64KiB: a line of more than 88 KiB with no escape sequence
// and no mask across the cut keeps its first 64 KiB in the log, the masks after the cut aside.
func TestALongLineWithNoSequenceOpenKeeps64KiB(t *testing.T) {
	start := strings.Repeat("y", maxStepLine)
	for _, after := range []string{strings.Repeat("z", 30_000), strings.Repeat("AKIA"+strings.Repeat("0", 256), 120)} {
		step := &hookStep{}
		w := &stepLines{stderr: io.Discard, step: step, program: true}
		fmt.Fprint(w, start+after+"\n")
		if want := []string{start + textLineCut + "\n"}; !slices.Equal(step.lines, want) {
			t.Errorf("%d lines; the first has %d bytes, want %d", len(step.lines), len(strings.Join(step.lines, "")), len(want[0]))
		}
	}
}

// TestALongLineShowsTheStartOfASecretWithStarsThatMatchesPuts pins the shortcut in keptLine: a
// secret with a run of * matches the puts of 100 tokens, and the log keeps its start: fake.
func TestALongLineShowsTheStartOfASecretWithStarsThatMatchesPuts(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	secretmask.Default().Add("fake" + strings.Repeat("*", 300) + "value123")
	start := "\x1b[0m" + strings.Repeat("y", maxStepLine-9) + "fake"
	line := start + strings.Repeat("AKIA"+strings.Repeat("0", 256), 100) + "value123 end"
	if whole := string(logText([]byte(line))); strings.Contains(whole, "fake") {
		t.Fatalf("the whole line masked keeps fake")
	}
	step := &hookStep{}
	w := &stepLines{stderr: io.Discard, step: step, program: true}
	fmt.Fprint(w, line+"\n")
	if want := []string{start[4:] + secretmask.Placeholder + textLineCut + "\n"}; !slices.Equal(step.lines, want) {
		t.Errorf("%d lines; the first has %d bytes, want %d", len(step.lines), len(strings.Join(step.lines, "")), len(want[0]))
	}
}

// TestAStepsLinesTakeWritesFromTwoGoroutines: a program's stdout and stderr, which os/exec
// copies from two goroutines, write each line of one write whole into the step's log, once.
func TestAStepsLinesTakeWritesFromTwoGoroutines(t *testing.T) {
	step := &hookStep{}
	w := &stepLines{stderr: io.Discard, step: step, program: true}
	lines := []string{strings.Repeat("a", 100) + "\n", strings.Repeat("b", 100) + "\n"}
	var wg sync.WaitGroup
	for _, line := range lines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				fmt.Fprint(w, line)
			}
		}()
	}
	wg.Wait()
	w.flush()
	got := map[string]int{}
	for _, l := range step.lines {
		got[l]++
	}
	if len(step.lines) != 400 || got[lines[0]] != 200 || got[lines[1]] != 200 {
		t.Errorf("%d lines, %d of a and %d of b, want 200 each", len(step.lines), got[lines[0]], got[lines[1]])
	}
}

// TestAStepsLogHasNoByteOfASecretInEscapeSequences: an escape sequence in a secret or right
// before it leaves no byte of the secret in a step's log, unless the sequence ends with the
// first byte of a secret that another sequence splits: the rest of the secret stays.
func TestAStepsLogHasNoByteOfASecretInEscapeSequences(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	secretmask.Default().Add("fakevalue123")
	for _, c := range []struct{ name, line, want string }{
		{"a parameter with :", "fake\x1b[38:5:1mvalue123", "***"},
		{"three nested sequences", "fake\x1b[\x1b[\x1b[0m1m1mvalue123", "***"},
		{"a sequence that ends with the secret's first byte", "\x1b[fakevalue123", "[***"},
		{"a sequence that ends with the first byte of a split secret", "\x1b[fake\x1b[0mvalue123", "akevalue123"},
	} {
		for _, program := range []bool{true, false} {
			step := &hookStep{}
			w := &stepLines{stderr: io.Discard, step: step, program: program}
			fmt.Fprint(w, c.line+"\n")
			if want := []string{c.want + "\n"}; !slices.Equal(step.lines, want) {
				t.Errorf("%s, program %v: the log %q, want %q", c.name, program, step.lines, want)
			}
		}
	}
}

// TestALongLineShowsNoSecretsStart: the bytes at the cut of a long line go into the log as they
// are whether or not they start a value of the mask list, so the log does not show a value's
// start.
func TestALongLineShowsNoSecretsStart(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	secretmask.Default().Add("fakevalue123")
	start, after := strings.Repeat(".", maxStepLine-5), strings.Repeat(".", 100)
	for _, probe := range []string{"fakev", "fakex"} {
		step := &hookStep{}
		w := &stepLines{stderr: io.Discard, step: step, program: true}
		fmt.Fprint(w, start+probe+after+"\n")
		if want := []string{start + probe + textLineCut + "\n"}; !slices.Equal(step.lines, want) {
			got := strings.Join(step.lines, "")
			t.Errorf("%s: the line ends %q", probe, got[max(0, len(got)-60):])
		}
	}
}

// TestAHooksEditToProductCodeIsRestored: a hook that changes a tracked file, removes one and
// adds files, then exits 0, leaves the checkout as the agent left it: the agent's change that
// is not committed stays, and the push would commit nothing of the hook's.
func TestAHooksEditToProductCodeIsRestored(t *testing.T) {
	script := "#!/bin/sh\necho hook > src/main.go\nrm README.md\necho x > added.go\nmkdir -p gen && echo y > gen/z.go\n"
	hc := newHookCase(t, map[string]string{"scripts/fix.sh": script, "src/main.go": "package main\n", "README.md": "readme\n"},
		checkHook(0, "fix.sh", "scripts/fix.sh"))
	h := hc.open(t)
	// The agent's work, left for the push step.
	hookFile(t, hc.checkout, "src/main.go", "package main // agent\n")
	hookFile(t, hc.checkout, "notes.txt", "agent\n")
	before := hookGit(t, hc.checkout, "status", "--porcelain", "--untracked-files=all")
	hc.end(t, h)
	if len(hc.ran) != 1 {
		t.Fatalf("%d containers ran, want 1", len(hc.ran))
	}
	if after := hookGit(t, hc.checkout, "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Errorf("git status after the hook:\n%s\nwant:\n%s", after, before)
	}
	if got, _ := os.ReadFile(filepath.Join(hc.checkout, "src", "main.go")); string(got) != "package main // agent\n" {
		t.Errorf("src/main.go is %q", got)
	}
	if !h.restored() {
		t.Error("the hooks report the checkout as not restored")
	}
}

// nestedScript is a hook that leaves a repository in the checkout, which git clean -fd does not
// remove, so the restore fails, for root too.
const nestedScript = "#!/bin/sh\ngit init -q nested && echo x > nested/f\n"

// TestARestoreThatFailsEndsTheRunWithNoPush: a hook that leaves what the restore cannot remove
// ends the session failed with no end code, no hook runs after it, and no push follows; the line
// has the cause alone.
func TestARestoreThatFailsEndsTheRunWithNoPush(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/lock.sh": nestedScript}, checkHook(0, "lock.sh", "scripts/lock.sh"), checkHook(1, "second.sh", "scripts/lock.sh"))
	h := hc.open(t)
	got, err := h.after(context.Background(), context.Background(), time.Time{}, succeeded, false)
	if err == nil || got.State != event.StateFailed || got.Reason != "" || h.restored() {
		t.Errorf("outcome %+v, error %v, restored %v: want a failed session with no push", got, err, h.restored())
	}
	if len(hc.ran) != 1 {
		t.Errorf("%d hooks ran, want none after the restore that failed", len(hc.ran))
	}
	want := "qory could not undo what hook lock.sh changed; no push follows.\n  the checkout has a repository in nested"
	if !slices.Contains(hc.said, want) {
		t.Errorf("the lines %q, want %q", hc.said, want)
	}
}

// TestARepositoryThatTheAgentLeftMakesTheHooksRed: a repository in the checkout before the hooks,
// which is no submodule entry, makes each hook red with the folder's path; none runs.
func TestARepositoryThatTheAgentLeftMakesTheHooksRed(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	h := hc.open(t)
	hookGit(t, hc.checkout, "init", "--quiet", "nested")
	hc.end(t, h)
	if len(hc.ran) != 0 {
		t.Errorf("%d hooks ran, want none", len(hc.ran))
	}
	if r := hc.records(t); len(r) != 1 || r[0].Cause != "the checkout has a repository in nested" ||
		r[0].Findings[0].Point != "The hook check.sh did not run: the checkout has a repository in nested." {
		t.Errorf("records %+v", r)
	}
	want := "qory run: Hook check.sh did not run, so it is red.\n  the checkout has a repository in nested\n"
	if !strings.Contains(hc.logged(), want) {
		t.Errorf("the lines:\n%s\nwant %q", hc.logged(), want)
	}
}

// TestAHookThatWritesInASubmodulesFolderEndsTheRunWithNoPush: a hook that writes a file in the
// folder of the submodule entry sub fails the restore, so the session ends failed and no push
// follows; a hook that leaves sub as it was passes.
func TestAHookThatWritesInASubmodulesFolderEndsTheRunWithNoPush(t *testing.T) {
	for _, c := range []struct {
		script string
		fails  bool
	}{
		{"#!/bin/sh\nmkdir -p sub/d && echo x > sub/x && echo z > sub/d/z\n", true},
		{"#!/bin/sh\nexit 0\n", false},
	} {
		hc := newHookCase(t, map[string]string{"scripts/plant.sh": c.script}, checkHook(0, "plant.sh", "scripts/plant.sh"))
		hookGit(t, hc.checkout, "update-index", "--add", "--cacheinfo", "160000,"+hc.commit+",sub")
		hookGit(t, hc.checkout, "commit", "--quiet", "-m", "a submodule")
		if err := os.Mkdir(filepath.Join(hc.checkout, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		h := hc.open(t)
		got, err := h.after(context.Background(), context.Background(), time.Time{}, succeeded, false)
		if c.fails != (err != nil) || c.fails != (got.State == event.StateFailed) || c.fails == h.restored() {
			t.Errorf("%q: outcome %+v, error %v, restored %v", c.script, got, err, h.restored())
		}
		undone := slices.ContainsFunc(hc.said, func(s string) bool {
			return s == "qory could not undo what hook plant.sh changed; no push follows.\n  a hook changed the folder sub of a submodule entry"
		})
		if undone != c.fails {
			t.Errorf("%q: the lines %q", c.script, hc.said)
		}
	}
}

// TestARestoreAfterAHookAtTheStartThatFailsStartsNoSession: openHooks returns the error that
// ends the run before the join, and the records are written.
func TestARestoreAfterAHookAtTheStartThatFailsStartsNoSession(t *testing.T) {
	hook := compose.Hook{
		Workflow: "review-changes", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "lock.sh", Run: []string{"scripts/lock.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
	}
	hc := newHookCase(t, map[string]string{"scripts/lock.sh": nestedScript}, hook)
	h, err := openHooks(context.Background(), hc.setup)
	if h != nil || err == nil || err.Error() != "The session did not start: qory could not undo what a hook at its start changed." {
		t.Errorf("openHooks: %v, %v", h, err)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Green {
		t.Errorf("records %+v", r)
	}
}

// TestAnAgentsEditToTheHookFileLeavesTheOriginalToRun: the checkout has an edit of the hook's
// program that no commit has before the join, and the agent of the session changes the program
// and commits it; the hook that runs after the session is the program as the session's start
// commit has it.
func TestAnAgentsEditToTheHookFileLeavesTheOriginalToRun(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\necho original\nexit 1\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	hookFile(t, hc.checkout, "scripts/check.sh", "#!/bin/sh\necho uncommitted\nexit 0\n")
	h := hc.open(t)
	hookFile(t, hc.checkout, "scripts/check.sh", "#!/bin/sh\necho edited\nexit 0\n")
	hookGit(t, hc.checkout, "commit", "--quiet", "-am", "the agent's edit")
	hookFile(t, hc.checkout, "scripts/check.sh", "#!/bin/sh\necho left\nexit 0\n")
	hc.end(t, h)
	logged := hc.logged()
	if !strings.Contains(logged, "original\n") || strings.Contains(logged, "uncommitted") || strings.Contains(logged, "edited") || strings.Contains(logged, "left") {
		t.Errorf("the hook printed:\n%s", logged)
	}
	if len(hc.ran) != 1 || strings.HasPrefix(hc.ran[0].Session.Command, hc.checkout) {
		t.Errorf("the hook ran %q, want the program in the run directory", hc.ran[0].Session.Command)
	}
}

// TestARunEntryThatIsALinkOnTheStartCommitIsRefusedAndRed: a run path whose entry on the start
// commit is a link, or that passes a linked folder, starts no container; the hook is red with
// the cause, and the verdict changes.
func TestARunEntryThatIsALinkOnTheStartCommitIsRefusedAndRed(t *testing.T) {
	for _, c := range []struct{ name, run, cause string }{
		{"a link", "scripts/check.sh", "scripts/check.sh is a link in git at "},
		{"a linked folder", "linked/check.sh", "linked/check.sh passes through the link linked in git at "},
	} {
		t.Run(c.name, func(t *testing.T) {
			hc := newHookCase(t, map[string]string{"tools/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", c.run))
			if err := os.MkdirAll(filepath.Join(hc.checkout, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../tools/check.sh", filepath.Join(hc.checkout, "scripts", "check.sh")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("tools", filepath.Join(hc.checkout, "linked")); err != nil {
				t.Fatal(err)
			}
			hookGit(t, hc.checkout, "add", "-A")
			hookGit(t, hc.checkout, "commit", "--quiet", "-m", "links")
			hc.setup.commit = hookGit(t, hc.checkout, "rev-parse", "HEAD")
			hc.end(t, hc.open(t))
			if len(hc.ran) != 0 {
				t.Errorf("%d containers ran, want none", len(hc.ran))
			}
			r := hc.records(t)
			if len(r) != 1 || r[0].Verdict != hookrun.Red || !strings.Contains(r[0].Cause, c.cause) {
				t.Errorf("records %+v, want red with %q", r, c.cause)
			}
			if posted := hc.api.verdicts(); len(posted) != 1 || !strings.Contains(posted[0], `"verdict":"changes-requested"`) ||
				!strings.Contains(posted[0], `"action":"run_again"`) {
				t.Errorf("posted %q", posted)
			}
			if !strings.Contains(hc.logged(), "Hook check.sh is red: its program is not a file in the commit.\n") {
				t.Errorf("the lines:\n%s", hc.logged())
			}
		})
	}
}

// TestAProgramOver64MiBIsRed: a program of more than 64 MiB starts no container, and the hook is
// red with its line and the cause.
func TestAProgramOver64MiBIsRed(t *testing.T) {
	hc := newHookCase(t, map[string]string{"big.sh": strings.Repeat("\n", hookrun.MaxProgramBytes+1)}, checkHook(0, "big.sh", "big.sh"))
	hc.end(t, hc.open(t))
	r := hc.records(t)
	if len(hc.ran) != 0 || len(r) != 1 || !strings.HasSuffix(r[0].Cause, " has more than 67108864 bytes") {
		t.Fatalf("%d hooks ran, records %+v", len(hc.ran), r)
	}
	if want := "qory run: Hook big.sh is red: its program is over 64 MiB.\n  " + r[0].Cause + "\n"; !strings.Contains(hc.logged(), want) {
		t.Errorf("the lines:\n%s\nwant %q", hc.logged(), want)
	}
}

// TestTheHooksFolderThatCannotBeMadeStartsNoSession: with a file in place of the hooks' folder in
// the run directory, openHooks returns the line with the cause.
func TestTheHooksFolderThatCannotBeMadeStartsNoSession(t *testing.T) {
	hc := newHookCase(t, nil, checkHook(0, "check.sh", "scripts/check.sh"))
	hookFile(t, hc.setup.run.Dir(), hookrun.Folder, "")
	h, err := openHooks(context.Background(), hc.setup)
	if want := "qory could not make the hooks' folder.\n  "; h != nil || err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("openHooks: %v, %v, want %q", h, err, want)
	}
}

// TestRecordsThatCannotBeWrittenAreALine: with a folder in place of the records' file, the hooks
// run, and a line has the cause.
func TestRecordsThatCannotBeWrittenAreALine(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	h := hc.open(t)
	if err := os.MkdirAll(filepath.Join(hc.setup.run.Dir(), hookrun.Folder, "session-1.json", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	hc.end(t, h)
	if len(hc.ran) != 1 || !slices.ContainsFunc(hc.said, func(s string) bool { return strings.HasPrefix(s, "qory could not write the hooks' records.\n  ") }) {
		t.Errorf("%d hooks ran, the lines %q", len(hc.ran), hc.said)
	}
}

// TestAnAgentHookEntryIsRedWithItsLine: an agent hook of a kind other than review does not run
// and is not skipped: it is red, with the line that agent hooks are not built yet and the fix
// to remove it, and the verdict changes with a run_again point.
func TestAnAgentHookEntryIsRedWithItsLine(t *testing.T) {
	agent := compose.Hook{
		Workflow: "review-changes", At: "after-review-round", PointAt: hookrun.PointEnd, Kind: "enrich",
		Name: "licence-reviewer", Agent: "licence-reviewer", Required: true, TimeoutSeconds: 600, Index: 0,
	}
	hc := newHookCase(t, nil, agent)
	hc.end(t, hc.open(t))
	if len(hc.ran) != 0 {
		t.Errorf("%d containers ran", len(hc.ran))
	}
	r := hc.records(t)
	if len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != "agent hooks are not built yet" ||
		r[0].Findings[0].Fix != "Remove the agent hook licence-reviewer from the stack's hooks." {
		t.Errorf("records %+v", r)
	}
	if strings.Contains(textHookAgent, "\n") {
		t.Errorf("the agent hook's text %q has more than one line", textHookAgent)
	}
	if !strings.Contains(hc.logged(), "qory run: Agent hook licence-reviewer did not run: agent hooks are not built yet.\n") {
		t.Errorf("the lines:\n%s", hc.logged())
	}
	if posted := hc.api.verdicts(); len(posted) != 1 || !strings.Contains(posted[0], `"verdict":"changes-requested"`) ||
		!strings.Contains(posted[0], `"action":"run_again"`) {
		t.Errorf("posted %q", posted)
	}
}

// TestAReadTargetIsReadOnlyToAHook: a read target is below .qory, which the hook's container binds
// read-only, and nothing below .qory has a bind, also with a link there.
func TestAReadTargetIsReadOnlyToAHook(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	readTarget(t, hc.checkout)
	hc.end(t, hc.open(t))
	if len(hc.ran) != 1 {
		t.Fatalf("%d hooks ran", len(hc.ran))
	}
	checkout := hc.checkout
	want := []wall.Mount{
		{Path: checkout}, {Path: filepath.Join(checkout, ".git"), ReadOnly: true},
		{Path: filepath.Join(checkout, ".qory"), ReadOnly: true}, {Path: hc.ran[0].Session.Command, ReadOnly: true},
	}
	if !slices.Equal(hc.ran[0].Mounts, want) {
		t.Errorf("the binds %+v, want %+v", hc.ran[0].Mounts, want)
	}
}

// readTarget makes a read target, a read-only repository with lib.go committed, in the checkout
// at dir, as the preparation fetches one, and returns its folder.
func readTarget(t *testing.T, dir string) string {
	t.Helper()
	hookFile(t, dir, ".git/info/exclude", preparation.ExcludeLine+"\n")
	lib := filepath.Join(dir, filepath.FromSlash(preparation.Repositories), "acme", "lib")
	hookFile(t, lib, "lib.go", "package lib\n")
	hookGit(t, lib, "init", "--quiet")
	hookGit(t, lib, "add", "-A")
	hookGit(t, lib, "commit", "--quiet", "-m", "lib")
	return lib
}

// TestAReviewHookIsRedWithItsLine: a review hook, with run, which compose accepts, or with agent,
// is red with the line that review hooks are not built yet and the fix to remove it: its program
// is not read and does not run, and the verdict changes with a run_again point.
func TestAReviewHookIsRedWithItsLine(t *testing.T) {
	withRun := checkHook(0, "review.sh", "scripts/review.sh")
	withRun.Kind = "review"
	withAgent := compose.Hook{
		Workflow: "review-changes", At: "after-review-round", PointAt: hookrun.PointEnd, Kind: "review",
		Name: "licence-reviewer", Agent: "licence-reviewer", Required: true, TimeoutSeconds: 600, Index: 0,
	}
	for _, hook := range []compose.Hook{withRun, withAgent} {
		t.Run(hook.Name, func(t *testing.T) {
			hc := newHookCase(t, map[string]string{"scripts/review.sh": "#!/bin/sh\nexit 0\n"}, hook)
			hc.end(t, hc.open(t))
			if _, err := os.Lstat(filepath.Join(hc.setup.run.Dir(), hookrun.Folder, "1", "0")); len(hc.ran) != 0 || err == nil {
				t.Errorf("%d containers ran, the program read: %v", len(hc.ran), err == nil)
			}
			if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeReview ||
				r[0].Findings[0].Fix != "Remove the review hook "+hook.Name+" from the stack's hooks." {
				t.Errorf("records %+v", r)
			}
			if strings.Contains(textHookReview, "\n") {
				t.Errorf("the review hook's text %q has more than one line", textHookReview)
			}
			if want := "qory run: Review hook " + hook.Name + " did not run: review hooks are not built yet.\n"; !strings.Contains(hc.logged(), want) {
				t.Errorf("the lines:\n%s", hc.logged())
			}
			if posted := hc.api.verdicts(); len(posted) != 1 || !strings.Contains(posted[0], `"verdict":"changes-requested"`) {
				t.Errorf("posted %q", posted)
			}
		})
	}
}

// TestARunWithNoWallMakesEachHookRed: no hook runs on this machine: each is red, with its line,
// and no container starts.
func TestARunWithNoWallMakesEachHookRed(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"},
		checkHook(0, "check.sh", "scripts/check.sh"), checkHook(1, "other.sh", "scripts/check.sh"))
	hc.setup.walled = false
	hc.end(t, hc.open(t))
	if len(hc.ran) != 0 || hc.removed != 0 {
		t.Errorf("%d containers ran, %d removals", len(hc.ran), hc.removed)
	}
	r := hc.records(t)
	if len(r) != 2 || r[0].Verdict != hookrun.Red || r[1].Verdict != hookrun.Red {
		t.Fatalf("records %+v", r)
	}
	for _, name := range []string{"check.sh", "other.sh"} {
		if want := "Hook " + name + " is red: this stage runs with no wall.\n"; !strings.Contains(hc.logged(), want) {
			t.Errorf("no line %q in:\n%s", want, hc.logged())
		}
	}
	if _, err := os.Stat(filepath.Join(hc.setup.run.Dir(), hookrun.Folder, "1", "0")); err == nil {
		t.Error("the program was read from git")
	}
}

// TestAHookAtTheStartRunsBeforeTheJoin: a hook at before-matrix, the start of verify-a-ticket,
// runs at session 1's openHooks, before the join, with its program from the run's start commit, and its red
// result changes the session's verdict once the session ended.
func TestAHookAtTheStartRunsBeforeTheJoin(t *testing.T) {
	hook := compose.Hook{
		Workflow: "verify-a-ticket", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "matrix.sh", Run: []string{"scripts/matrix.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
	}
	hc := newHookCase(t, map[string]string{"scripts/matrix.sh": "#!/bin/sh\necho start\nexit 3\n"}, hook)
	start := hc.commit
	hookFile(t, hc.checkout, "scripts/matrix.sh", "#!/bin/sh\necho later\nexit 0\n")
	hookGit(t, hc.checkout, "commit", "--quiet", "-am", "a later session's change")
	hc.setup.workflow, hc.setup.start, hc.setup.commit = "verify-a-ticket", start, hookGit(t, hc.checkout, "rev-parse", "HEAD")
	h := hc.open(t)
	if len(hc.ran) != 1 {
		t.Fatalf("%d hooks ran before the join, want 1", len(hc.ran))
	}
	if hc.removed != 0 {
		t.Error("the setup container was removed before the join")
	}
	hc.end(t, h)
	if !strings.Contains(hc.logged(), "start\n") {
		t.Errorf("the hook printed:\n%s", hc.logged())
	}
	want := `{"verdict":"changes-requested","points":[{"text":"The hook matrix.sh failed: it exited with status 3.","action":"run_again"}]}`
	if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAHookThatIsNotRequiredChangesNoVerdict: a red hook with required false is recorded with
// a non-blocking finding, and nothing is posted.
func TestAHookThatIsNotRequiredChangesNoVerdict(t *testing.T) {
	hook := checkHook(0, "check.sh", "scripts/check.sh")
	hook.Required = false
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, hook)
	hc.end(t, hc.open(t))
	if posted := hc.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Findings[0].Severity != "non-blocking" {
		t.Errorf("records %+v", r)
	}
}

// TestASessionOfAnotherWorkflowOrThatFailedRunsNoHook: hooks of another workflow are not the
// session's, and a session that did not succeed runs none of its hooks at the end.
func TestASessionOfAnotherWorkflowOrThatFailedRunsNoHook(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.setup.workflow = "implement-changes"
	if h, err := openHooks(context.Background(), hc.setup); h != nil || err != nil {
		t.Errorf("openHooks of another workflow: %v, %v", h, err)
	}
	hc.setup.workflow = "review-changes"
	failed := succeeded
	failed.State, failed.ExitCode = event.StateFailed, 1
	if got, err := hc.open(t).after(context.Background(), context.Background(), time.Time{}, failed, false); err != nil || got != failed {
		t.Errorf("outcome %+v, %v", got, err)
	}
	if len(hc.ran) != 0 || len(hc.api.verdicts()) != 0 {
		t.Errorf("%d hooks ran, posted %q", len(hc.ran), hc.api.verdicts())
	}
}

// TestTheSetupsLimitEndsTheHooksAtSession1sStart: the hooks at session 1's start run until the
// setup's limit, which ends the running hook and keeps the next from running: both are red with
// the cause of the limit, and openHooks returns the limit's error, so the run ends at the limit.
func TestTheSetupsLimitEndsTheHooksAtSession1sStart(t *testing.T) {
	first := compose.Hook{
		Workflow: "review-changes", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "slow.sh", Run: []string{"scripts/slow.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
	}
	second := first
	second.Name, second.Index = "next.sh", 1
	hc := newHookCase(t, map[string]string{"scripts/slow.sh": "#!/bin/sh\n"}, first, second)
	// The limit ends while the first hook runs, after the snapshot.
	ctx := newLimitCtx()
	runHookContainer = func(c context.Context, g gitContainer) (int, error) {
		hc.ran = append(hc.ran, g)
		ctx.end()
		<-c.Done()
		return -1, c.Err()
	}
	h, err := openHooks(ctx, hc.setup)
	if h != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("openHooks: %v, %v, want the limit's error", h, err)
	}
	if len(hc.ran) != 1 {
		t.Errorf("%d hooks ran, want 1", len(hc.ran))
	}
	if r := hc.records(t); len(r) != 2 || r[0].Cause != causeSetupLimit || r[1].Cause != causeSetupLimit {
		t.Errorf("records %+v", r)
	}
	if want := "qory run: the hook slow.sh is red.\n  " + causeSetupLimit + "\n"; !strings.Contains(hc.logged(), want) {
		t.Errorf("the lines:\n%s\nwant %q", hc.logged(), want)
	}
}

// TestTheSetupsLimitBeforeTheSnapshotCutsTheHooksAtSession1sStart: the setup's limit, ended before
// the read of the programs and the snapshot, makes each hook at session 1's start red with the
// limit's cause, runs none, and openHooks returns the limit's error.
func TestTheSetupsLimitBeforeTheSnapshotCutsTheHooksAtSession1sStart(t *testing.T) {
	hook := compose.Hook{
		Workflow: "review-changes", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "check.sh", Run: []string{"scripts/check.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
	}
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\n"}, hook)
	ctx := newLimitCtx()
	ctx.end()
	h, err := openHooks(ctx, hc.setup)
	if h != nil || !errors.Is(err, context.DeadlineExceeded) || len(hc.ran) != 0 {
		t.Errorf("openHooks: %v, %v, %d hooks ran, want the limit's error and none", h, err, len(hc.ran))
	}
	if r := hc.records(t); len(r) != 1 || r[0].Cause != causeSetupLimit {
		t.Errorf("records %+v", r)
	}
}

// TestAStopDuringTheSetupsRemovalPostsNothing: a stop of qory run during the removal of the
// setup container before the hooks at the end runs no hook and posts no verdict over the agent's
// passed; each hook is red, stopped, and the run closes cancelled.
func TestAStopDuringTheSetupsRemovalPostsNothing(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.api.add(verdictOf(`{"verdict":"passed"}`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := hc.setup.run.(hookFakeRun)
	run.remove = func(c context.Context) error {
		cancel()
		return c.Err()
	}
	hc.setup.run = run
	h := hc.open(t)
	got, err := h.after(ctx, context.Background(), time.Time{}, succeeded, false)
	if err == nil || got.State != event.StateCancelled || len(hc.ran) != 0 {
		t.Errorf("outcome %+v, %v, %d hooks ran, want cancelled and none", got, err, len(hc.ran))
	}
	if posted := hc.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Cause != causeStopped {
		t.Errorf("records %+v", r)
	}
}

// TestARestoreCutShortFails: the restore after a hook ends at a second stop of qory run, and at
// its limit; a restore that ended before it was done failed: the line has its cause, no verdict
// is posted, and the session's outcome is failed, so no push and no next session follow.
func TestARestoreCutShortFails(t *testing.T) {
	for _, c := range []struct{ name, cause string }{
		{"a second stop", causeRestoreStopped},
		{"the limit", "the restore ran past its limit of 1ns"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\necho changed > README.md\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
			again, stopAgain := context.WithCancel(context.Background())
			defer stopAgain()
			hc.setup.again = again
			if c.cause == causeRestoreStopped {
				was := runHookContainer
				runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
					stopAgain()
					return was(ctx, g)
				}
			} else {
				was := hookRestoreLimit
				t.Cleanup(func() { hookRestoreLimit = was })
				hookRestoreLimit = time.Nanosecond
			}
			h := hc.open(t)
			got, err := h.after(context.Background(), context.Background(), time.Time{}, succeeded, false)
			if err == nil || got.State != event.StateFailed || h.restored() {
				t.Errorf("outcome %+v, %v, restored %v, want failed", got, err, h.restored())
			}
			want := "qory could not undo what hook check.sh changed; no push follows.\n  " + c.cause
			if !slices.Contains(hc.said, want) {
				t.Errorf("the lines %q, want %q", hc.said, want)
			}
			if posted := hc.api.verdicts(); len(posted) != 0 {
				t.Errorf("posted %q", posted)
			}
		})
	}
}

// TestALaterSessionReadsTheProgramsAtItsEndAfterTheSetupsRemoval: in session 2 of a shared run,
// openHooks reads no program of a hook at the end, since the setup container may still write
// .git; the program is read once the setup container is removed, and the hook runs it.
func TestALaterSessionReadsTheProgramsAtItsEndAfterTheSetupsRemoval(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.setup.number = 2
	program := filepath.Join(hc.setup.run.Dir(), hookrun.Folder, "2", "0")
	removed := 0
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		removed = hc.removed
		hc.ran = append(hc.ran, g)
		return 1, nil
	}
	h := hc.open(t)
	if _, err := os.Lstat(program); err == nil {
		t.Error("openHooks read the program")
	}
	hc.end(t, h)
	if _, err := os.Lstat(program); err != nil || len(hc.ran) != 1 || removed != 1 || hc.ran[0].Session.Command != program {
		t.Errorf("the program %v, %d hooks ran after %d removals, the command %v", err, len(hc.ran), removed, hc.ran)
	}
}

// TestAStopDuringAHookAtTheEndPostsNothing: a stop of qory run while a hook at the end runs
// closes the run cancelled and posts no verdict; the hook's record is red, stopped.
func TestAStopDuringAHookAtTheEndPostsNothing(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.api.add(verdictOf(`{"verdict":"passed"}`))
	h := hc.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runHookContainer = func(c context.Context, g gitContainer) (int, error) {
		cancel()
		return -1, c.Err()
	}
	got, err := h.after(ctx, context.Background(), time.Time{}, succeeded, false)
	if err == nil || got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted {
		t.Errorf("outcome %+v, %v, want cancelled", got, err)
	}
	if posted := hc.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	if !slices.Contains(hc.said, textHooksCut) {
		t.Errorf("the lines %q", hc.said)
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeStopped {
		t.Errorf("records %+v", r)
	}
}

// TestAHookAtALaterSessionsStartIsRedAndDoesNotRun: in session 2 of a shared run, a hook at
// before-matrix is red at openHooks with the line of the missing pause, and its program does not
// run. Once the session ended, the red hook changes the verdict as any red hook does:
// changes-requested over passed, and escalate stays escalate.
func TestAHookAtALaterSessionsStartIsRedAndDoesNotRun(t *testing.T) {
	point := `{"text":"The hook matrix.sh did not run: ` + causeLater + `.","action":"run_again"}`
	question := `{"text":"Pick one.","action":"answer","question":"q1"}`
	for _, c := range []struct{ name, agent, want string }{
		{"passed", `{"verdict":"passed"}`, `{"verdict":"changes-requested","points":[` + point + `]}`},
		{"escalate", `{"verdict":"escalate","points":[` + question + `]}`, `{"verdict":"escalate","points":[` + question + `,` + point + `]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			hook := compose.Hook{
				Workflow: "verify-a-ticket", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
				Name: "matrix.sh", Run: []string{"scripts/matrix.sh"}, Required: true, TimeoutSeconds: 600, Index: 0,
			}
			marker := filepath.Join(t.TempDir(), "ran")
			hc := newHookCase(t, map[string]string{"scripts/matrix.sh": "#!/bin/sh\ntouch " + marker + "\n"}, hook)
			hc.setup.workflow, hc.setup.number = "verify-a-ticket", 2
			hc.api.add(verdictOf(c.agent))
			h := hc.open(t)
			if len(hc.ran) != 0 || hc.removed != 0 {
				t.Fatalf("%d hooks ran, the setup container was removed %d times", len(hc.ran), hc.removed)
			}
			if !strings.Contains(hc.logged(), "qory run: "+textHookLater+"\n") {
				t.Errorf("the lines:\n%s", hc.logged())
			}
			hc.end(t, h)
			if _, err := os.Lstat(marker); err == nil {
				t.Error("the program ran")
			}
			if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeLater {
				t.Errorf("records %+v", r)
			}
			if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != c.want {
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// stopAfterGreen is a run whose steps call stop when they end succeeded. In a session whose hooks
// at the end are all program hooks, the first such step is that of the first hook that passed:
// the stop comes after its container ended and before the next hook.
type stopAfterGreen struct {
	hookFakeRun
	stop func()
}

func (r stopAfterGreen) StartStep(kind, name string) (hostStep, error) {
	s, err := r.hookFakeRun.StartStep(kind, name)
	return greenThen{s, r.stop}, err
}

// greenThen is a step that calls then after it ends succeeded.
type greenThen struct {
	hostStep
	then func()
}

func (s greenThen) Exit(state string, code int) error {
	err := s.hostStep.Exit(state, code)
	if state == event.StateSucceeded {
		s.then()
	}
	return err
}

// TestAStopBetweenTwoHooksStartsNoSecondHook: a stop of qory run after the first of two hooks
// at the end passed and before the second starts starts no second container and posts no
// verdict; the second hook's record is red, stopped.
func TestAStopBetweenTwoHooksStartsNoSecondHook(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\n"},
		checkHook(0, "check.sh", "scripts/check.sh"), checkHook(1, "second.sh", "scripts/check.sh"))
	hc.api.add(verdictOf(`{"verdict":"passed"}`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc.setup.run = stopAfterGreen{hc.setup.run.(hookFakeRun), cancel}
	h := hc.open(t)
	got, err := h.after(ctx, context.Background(), time.Time{}, succeeded, false)
	if err == nil || got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted {
		t.Errorf("outcome %+v, %v, want cancelled", got, err)
	}
	if len(hc.ran) != 1 {
		t.Errorf("%d containers started, want 1", len(hc.ran))
	}
	if posted := hc.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	r := hc.records(t)
	if len(r) != 2 || r[0].Verdict != hookrun.Green || r[1].Verdict != hookrun.Red || r[1].Cause != causeStopped {
		t.Errorf("records %+v", r)
	}
}

// TestAStoppedSessionRunsNoHookAndPostsNothing: after a stop of qory run, a session whose work
// the push step pushes runs no hook at its end and posts no verdict; each hook is red, stopped.
func TestAStoppedSessionRunsNoHookAndPostsNothing(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	h := hc.open(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	if got, err := h.after(ctx, context.Background(), time.Time{}, stopped, true); err != nil || got != stopped {
		t.Errorf("outcome %+v, %v, want the session's", got, err)
	}
	if len(hc.ran) != 0 || hc.removed != 0 || len(hc.api.verdicts()) != 0 || len(hc.steps) != 0 {
		t.Errorf("%d hooks ran, %d removals, %d steps, posted %q", len(hc.ran), hc.removed, len(hc.steps), hc.api.verdicts())
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeStopped {
		t.Errorf("records %+v", r)
	}
}

// TestAStartHookWhoseContainerStaysStartsNoSession: a hook at the start past its limit whose
// container's removal failed ends the run before the join with the line of the removal.
func TestAStartHookWhoseContainerStaysStartsNoSession(t *testing.T) {
	hook := compose.Hook{
		Workflow: "review-changes", At: "before-matrix", PointAt: hookrun.PointStart, Kind: "check",
		Name: "slow.sh", Run: []string{"scripts/slow.sh"}, Required: true, TimeoutSeconds: 1, Index: 0,
	}
	hc := newHookCase(t, map[string]string{"scripts/slow.sh": "#!/bin/sh\n"}, hook)
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		<-ctx.Done()
		return -1, errors.Join(ctx.Err(), errors.New("docker rm failed"))
	}
	h, err := openHooks(context.Background(), hc.setup)
	if h != nil || err == nil || err.Error() != "Hook slow.sh's container was not removed, so no push follows.\n  docker rm --force qory-hook-r1 failed: docker rm failed" {
		t.Errorf("openHooks: %v, %v", h, err)
	}
}

// TestAQoryLineOfAStepKeepsItsLength: a line of a step that has no program's output keeps every
// byte in the log.
func TestAQoryLineOfAStepKeepsItsLength(t *testing.T) {
	step := &hookStep{}
	w := &stepLines{stderr: io.Discard, step: step}
	long := strings.Repeat("x", maxStepLine+100)
	fmt.Fprint(w, long+"\n")
	if len(step.lines) != 1 || step.lines[0] != long+"\n" {
		t.Errorf("%d lines, the first of %d bytes", len(step.lines), len(step.lines[0]))
	}
}

// TestAFailedSessionWhoseWorkIsPushedRunsItsHooks: a session that failed, whose work the push
// step still pushes, runs its hooks at the end; a red hook posts changes-requested over the
// agent's passed, and the outcome stays the session's.
func TestAFailedSessionWhoseWorkIsPushedRunsItsHooks(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.api.add(verdictOf(`{"verdict":"passed"}`))
	failed := succeeded
	failed.State, failed.ExitCode = event.StateFailed, 1
	if got, err := hc.open(t).after(context.Background(), context.Background(), time.Time{}, failed, true); err != nil || got != failed {
		t.Errorf("outcome %+v, %v", got, err)
	}
	if len(hc.ran) != 1 {
		t.Errorf("%d hooks ran, want 1", len(hc.ran))
	}
	want := `{"verdict":"changes-requested","points":[` + hookPointRed + `]}`
	if posted := hc.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestTheHookContainerHasNoNetworkAndReadOnlyGit: the container of a hook has no network and no
// capability, the checkout bound for writing with its .git and .qory read-only over it, and the
// program read-only, named qory-hook-<run id> with the run's label.
func TestTheHookContainerHasNoNetworkAndReadOnlyGit(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	host := runHookContainer
	runHookContainer = func(ctx context.Context, g gitContainer) (int, error) {
		if info, err := os.Lstat(filepath.Join(hc.checkout, ".qory")); err != nil || !info.IsDir() {
			t.Errorf("the checkout's .qory is %v, %v, want the folder that qory made", info, err)
		}
		return host(ctx, g)
	}
	hc.end(t, hc.open(t))
	if len(hc.ran) != 1 {
		t.Fatalf("%d containers ran", len(hc.ran))
	}
	args, err := hc.ran[0].args()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	program := filepath.Join(hc.setup.run.Dir(), "hooks", "1", "0")
	for _, want := range []string{
		"--network none", "--cap-drop ALL", "--name qory-hook-r1", "--label dev.qory.run=r1",
		"--mount type=bind,src=" + hc.checkout + ",dst=" + hc.checkout + " ",
		"--mount type=bind,src=" + hc.checkout + "/.git,dst=" + hc.checkout + "/.git,readonly",
		"--mount type=bind,src=" + hc.checkout + "/.qory,dst=" + hc.checkout + "/.qory,readonly",
		"--mount type=bind,src=" + program + ",dst=" + program + ",readonly",
		"--workdir " + hc.checkout + " --entrypoint " + program + " example.test/build:1",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("docker %s\nhas no %q", line, want)
		}
	}
	if strings.Contains(line, "--env QORY") {
		t.Errorf("docker %s has a run variable", line)
	}
}

// TestACheckoutWhoseQoryIsALinkRunsNoHook: with .qory a link, no hook runs and each is red,
// since the link's folder would not be read-only in the container.
func TestACheckoutWhoseQoryIsALinkRunsNoHook(t *testing.T) {
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	if err := os.Symlink(t.TempDir(), filepath.Join(hc.checkout, ".qory")); err != nil {
		t.Fatal(err)
	}
	hc.end(t, hc.open(t))
	if len(hc.ran) != 0 {
		t.Errorf("%d containers ran, want none", len(hc.ran))
	}
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeNoQory {
		t.Errorf("records %+v", r)
	}
}

// TestHooksThatCannotBeReadAreHarnessInvalid: a hooks.json that does not read is
// harness_invalid, with the cause alone.
func TestHooksThatCannotBeReadAreHarnessInvalid(t *testing.T) {
	hc := newHookCase(t, nil, checkHook(0, "check.sh", "scripts/check.sh"))
	if err := os.WriteFile(filepath.Join(hc.setup.home, render.HooksFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := openHooks(context.Background(), hc.setup)
	var ended *reasonError
	first, _, _ := strings.Cut(fmt.Sprint(err), "\n")
	if h != nil || !errors.As(err, &ended) || ended.code != string(contracts.EndCodeHarnessInvalid) || first != "qory could not read the run's hooks." {
		t.Errorf("hooks %v, error %q", h, err)
	}
}

// TestAFailedPostOfTheHooksVerdictClosesWithRunAPIFailed: a red required hook whose verdict
// post gets 500 closes the run failed with run_api_failed, as the suite's post does; a second
// stop that cuts the post closes it failed with no end code, since no call failed.
func TestAFailedPostOfTheHooksVerdictClosesWithRunAPIFailed(t *testing.T) {
	was := submissionWait
	submissionWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { submissionWait = was })
	for _, c := range []struct {
		name    string
		stopped bool
		reason  string
	}{
		{"the run API answers 500", false, string(contracts.EndCodeRunAPIFailed)},
		{"a second stop cuts the post", true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			hc := newHookCase(t, map[string]string{"scripts/check.sh": redScript}, checkHook(0, "check.sh", "scripts/check.sh"))
			hc.api.verdictStatus = http.StatusInternalServerError
			again, stop := context.WithCancel(context.Background())
			defer stop()
			if c.stopped {
				stop()
			}
			got, err := hc.open(t).after(context.Background(), again, time.Time{}, succeeded, false)
			if got.State != event.StateFailed || got.ExitCode != 1 || got.Reason != c.reason || ExitCode(err) != 1 {
				t.Errorf("outcome %+v, error %v, want failed with 1 and the reason %q", got, err, c.reason)
			}
			if !slices.ContainsFunc(hc.said, func(line string) bool {
				return strings.HasPrefix(line, "qory could not post the hooks' verdict.")
			}) {
				t.Errorf("the lines %q", hc.said)
			}
		})
	}
}

// TestARedHookWithMoreFindingsThanAVerdictHasPointsStillPostsChangesRequested: past 20 red
// hooks, the verdict has the first 20 points, and it changes from passed.
func TestARedHookWithMoreFindingsThanAVerdictHasPointsStillPostsChangesRequested(t *testing.T) {
	hc := newHookCase(t, nil)
	hc.api.add(verdictOf(`{"verdict":"passed"}`))
	h := &sessionHooks{s: hc.setup}
	for i := range 25 {
		h.records = append(h.records, h.red(checkHook(i, fmt.Sprintf("c%d.sh", i), "c.sh"), 1, "it exited with status 1", ""))
	}
	if err := h.post(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	posted := hc.api.verdicts()
	if len(posted) != 1 || !strings.HasPrefix(posted[0], `{"verdict":"changes-requested"`) || strings.Count(posted[0], `"action":"run_again"`) != 20 {
		t.Errorf("posted %q", posted)
	}
	if want := "posted the verdict changes-requested for the red hooks"; !slices.Contains(hc.said, want) {
		t.Errorf("the lines %q, want %q", hc.said, want)
	}
}

// TestHookCommitsAreTheWorkBranchsOrHEADBeforeTheFirstJoin: with a work branch, the hooks at the
// end take the target's commit and those at the start the run's; with none, both take HEAD as
// the first call read it.
func TestHookCommitsAreTheWorkBranchsOrHEADBeforeTheFirstJoin(t *testing.T) {
	branch := &preparation.WorkBranch{Repositories: []preparation.WorkRepository{{Dir: ".", Commit: "c2", Start: "c1"}}}
	var pinned hookPin
	if commit, start := hookCommits(branch, "", &pinned); commit != "c2" || start != "c1" {
		t.Errorf("commits %s, %s", commit, start)
	}
	repo := workspaceRepo(t)
	head := hookGit(t, repo, "rev-parse", "HEAD")
	if commit, start := hookCommits(nil, repo, &pinned); commit != head || start != head {
		t.Errorf("commits %s, %s, want %s", commit, start, head)
	}
	hookGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "the agent's")
	if commit, _ := hookCommits(nil, repo, &pinned); commit != head {
		t.Errorf("a later session's commit %s, want %s", commit, head)
	}
	// No git reads HEAD, so a configuration that git cannot read changes nothing.
	head = hookGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[not a configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if commit, _ := hookCommits(nil, repo, new(hookPin)); commit != head {
		t.Errorf("commit %q, want %s", commit, head)
	}
}

// TestAHEADThatCouldNotBeReadMakesTheHooksRed: with no work branch and a checkout whose HEAD has
// no commit before session 1, a later session takes no commit either, after an agent committed,
// and each program hook is red with the cause and does not run.
func TestAHEADThatCouldNotBeReadMakesTheHooksRed(t *testing.T) {
	empty := t.TempDir()
	hookGit(t, empty, "init", "--quiet")
	var pinned hookPin
	if commit, start := hookCommits(nil, empty, &pinned); commit != "" || start != "" {
		t.Errorf("commits %q, %q, want none", commit, start)
	}
	hookGit(t, empty, "commit", "--quiet", "--allow-empty", "-m", "the agent's")
	if commit, _ := hookCommits(nil, empty, &pinned); commit != "" {
		t.Errorf("a later session's commit %s, want none", commit)
	}
	hc := newHookCase(t, map[string]string{"scripts/check.sh": "#!/bin/sh\nexit 0\n"}, checkHook(0, "check.sh", "scripts/check.sh"))
	hc.setup.commit = ""
	hc.end(t, hc.open(t))
	if r := hc.records(t); len(r) != 1 || r[0].Verdict != hookrun.Red || r[0].Cause != causeNoHead || len(hc.ran) != 0 {
		t.Errorf("records %+v, %d hooks ran", r, len(hc.ran))
	}
}

// TestAHookFindingHasTheFormOfAReviewReport: the finding of a red hook, as the reviewer
// hook-<index>, has the fix of its case, passes the review report's schema, and each point
// passes the verdict format.
func TestAHookFindingHasTheFormOfAReviewReport(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	path, err := filepath.Abs(filepath.Join("..", "internal", "reviewround", "review-report.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &sessionHooks{s: hookSetup{said: func(string) {}}}
	long := strings.Repeat("n", 64)
	review := checkHook(4, "review.sh", "scripts/review.sh")
	review.Kind = "review"
	for _, r := range []struct {
		hook       compose.Hook
		exit       int
		cause, fix string
	}{
		{checkHook(0, "check.sh", "scripts/check.sh --strict"), 1, "it exited with status 1", "Change the work so that the hook check.sh passes."},
		{checkHook(2, long, "scripts/check.sh"), -1, causeNoWall, "Run the hook " + long + " again in a run where it can start."},
		{compose.Hook{Name: "rev", Agent: "rev", Index: 3, Required: true}, -1, causeAgent, "Remove the agent hook rev from the stack's hooks."},
		{review, -1, causeReview, "Remove the review hook review.sh from the stack's hooks."},
	} {
		record := h.red(r.hook, r.exit, r.cause, "")
		entry := record.Entry()
		if got := entry.Findings[0].Fix; got != r.fix {
			t.Errorf("%s: the fix %q, want %q", r.hook.Name, got, r.fix)
		}
		data, _ := json.Marshal(struct {
			Reviewer string `json:"reviewer"`
			Findings any    `json:"findings"`
		}{entry.Reviewer, entry.Findings})
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Errorf("%s: %v\n%s", r.hook.Name, err, data)
		}
		if p := entry.Findings[0].Point; !validPoint(runapi.Point{Text: p, Action: runapi.ActionRunAgain}) {
			t.Errorf("the point %q is not valid", p)
		}
	}
}

// TestACauseIsOneLine: an error of several lines is one line of its trimmed lines, cut after 300
// characters with "...".
func TestACauseIsOneLine(t *testing.T) {
	if got := causeLine(errors.New("Fix it.\n  The cause.\n\n  More.")); got != "Fix it. The cause. More." {
		t.Errorf("the cause %q", got)
	}
	if got := causeLine(errors.New(strings.Repeat("x", 400))); got != strings.Repeat("x", maxCauseLength)+"..." {
		t.Errorf("a cause of %d characters", len(got))
	}
}

// TestTheHookLinesHaveTheCauseAloneInAtMost100Characters: no hook line starts with the verb of
// a fix or has the fix to take the work item in again, since a hook runs only in a run from a
// run code, and every first line has at most 100 characters with "qory run: ", the longest name
// a line shows, and a container's name of a run id of 36 characters.
func TestTheHookLinesHaveTheCauseAloneInAtMost100Characters(t *testing.T) {
	name := shownHookName(strings.Repeat("n", 64))
	if len([]rune(name)) != maxShownHookName {
		t.Fatalf("the shown name %q", name)
	}
	container := "qory-hook-" + strings.Repeat("r", 36)
	fixes := []string{"Add", "Change", "Check", "Commit", "Define", "Give", "Make", "Move", "Remove", "Run", "Set", "Start", "Take", "Update", "Use"}
	lines := []string{
		fmt.Sprintf(textHookStep, name, "after-implementation-round"),
		fmt.Sprintf(textHookPassed, name),
		fmt.Sprintf(textHookExited, name, 255),
		fmt.Sprintf(textHookTimedOut, name, 600),
		fmt.Sprintf(textHookStopped, name),
		fmt.Sprintf(textHookAgent, name),
		fmt.Sprintf(textHookReview, name),
		fmt.Sprintf(textHookNoWall, name),
		fmt.Sprintf(textHookOptional, name),
		fmt.Sprintf(textHooksPosted, "changes-requested"),
		textHooksNoRoom,
		textHooksCut,
		textHooksNoJoin,
		textHookLater,
	}
	for _, cause := range []string{
		causeAgent, causeReview, causeNoWall, causeStopped, causeSetupLimit, causeUnrestored, causeNoStart, causeNoQory,
		causeNoHead, causeLater, fmt.Sprintf(causeRestoreLimit, limitText(hookRestoreLimit)), causeRestoreStopped,
		"the checkout has a repository in " + strings.Repeat("d", 100), strings.Repeat("x", maxCauseLength),
	} {
		lines = append(lines,
			fmt.Sprintf(textHookNoStart, name, cause),
			fmt.Sprintf(textHookProgram, name, cause),
			fmt.Sprintf(textHookTooLarge, name, cause),
			fmt.Sprintf(textHookNoRun, name, cause),
			fmt.Sprintf(textHooksNoPost, cause),
			fmt.Sprintf(textHookUndoFail, name, cause),
			fmt.Sprintf(textHookKept, name, container, cause),
			fmt.Sprintf(textHooksUnread, cause),
			fmt.Sprintf(textHooksNoRecord, cause),
			fmt.Sprintf(textHooksNoFolder, cause),
		)
	}
	for _, line := range lines {
		first, _, _ := strings.Cut(line, "\n")
		if n := len([]rune("qory run: " + first)); n > 100 {
			t.Errorf("%d characters: %q", n, first)
		}
		if verb, _, _ := strings.Cut(first, " "); slices.Contains(fixes, verb) || strings.Contains(line, "in again") {
			t.Errorf("the line %q starts with a fix", line)
		}
	}
}
