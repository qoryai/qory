package cmd_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/places"
)

// runEvent is one event of a run's record: its type and its data.
type runEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// sessionRecord is the session's record, session.jsonl, of the one run of the checkout
// root, in order.
func sessionRecord(t *testing.T, root string) []runEvent {
	t.Helper()
	runs, err := os.ReadDir(runsDir(t, root))
	if err != nil || len(runs) != 1 {
		t.Fatalf("the runs: %v, %v", runs, err)
	}
	b, err := os.ReadFile(filepath.Join(runsDir(t, root), runs[0].Name(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []runEvent
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		var ev runEvent
		if err := json.Unmarshal(s.Bytes(), &ev); err != nil {
			t.Fatalf("%s: %v", s.Bytes(), err)
		}
		out = append(out, ev)
	}
	return out
}

// typesOf is the types of the events of rec that start or end the run or a session, in
// order.
func typesOf(rec []runEvent) []string {
	var out []string
	for _, ev := range rec {
		switch ev.Type {
		case "dev.qory.run.started", "dev.qory.session.started", "dev.qory.session.exited", "dev.qory.run.exited":
			out = append(out, strings.TrimPrefix(ev.Type, "dev.qory."))
		}
	}
	return out
}

// dataOf is the data of the first event of rec of type typ, nil when there is none.
func dataOf(rec []runEvent, typ string) map[string]any {
	for _, ev := range rec {
		if ev.Type == typ {
			return ev.Data
		}
	}
	return nil
}

// endOf is the state, the reason and the exit code of data, an event's data that ends
// the run or a session.
func endOf(data map[string]any) (state, reason string, exitCode float64) {
	state, _ = data["state"].(string)
	reason, _ = data["reason"].(string)
	exitCode, _ = data["exit_code"].(float64)
	return state, reason, exitCode
}

// TestRunOpensComposesJoinsAndCloses is a run whose agent exits 0: the run opens, the
// session joins and exits, and the run closes with qory's outcome, succeeded and 0, in
// that order in the record.
func TestRunOpensComposesJoinsAndCloses(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run", "claude")
	if err != nil {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: composed 8 entries from 2 modules for claude\n", "✓ claude exited 0\n")
	rec := sessionRecord(t, root)
	if got, want := typesOf(rec), []string{"run.started", "session.started", "session.exited", "run.exited"}; !slices.Equal(got, want) {
		t.Errorf("the record's events are %q, want %q", got, want)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.run.exited")); state != "succeeded" || reason != "" || exit != 0 {
		t.Errorf("run.exited is %s, %q, %v", state, reason, exit)
	}
	// The fixture's modules are path sources, which have no commit.
	if h, ok := dataOf(rec, "dev.qory.session.started")["harness"]; ok {
		t.Errorf("session.started lists the harness %v", h)
	}
}

// TestRunClosesARunWhoseComposeFails is a checkout whose stack lists a module that is not
// there: the run opened, so it closes failed with no session started, qory names the
// record after the error, and the exit status is the compose's, 2.
func TestRunClosesARunWhoseComposeFails(t *testing.T) {
	root := newCheckout(t)
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - source: {path: ./missing}\n")
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(out, "\nqory run: the record is in ") {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "hello from", "composed")
	rec := sessionRecord(t, root)
	if got, want := typesOf(rec), []string{"run.started", "run.exited"}; !slices.Equal(got, want) {
		t.Errorf("the record's events are %q, want %q", got, want)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.run.exited")); state != "failed" || reason != "no_session" || exit != -1 {
		t.Errorf("run.exited is %s, %q, %v", state, reason, exit)
	}
}

// TestRunCancelsARunAtItsLimitBeforeTheJoin is a run whose time limit passes after the
// compose and before the session joins: the limit counts from the open, so the run is
// cancelled with the reason timeout, no agent starts, and qory exits 124.
func TestRunCancelsARunAtItsLimitBeforeTheJoin(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	defer cmd.SetBeforeJoin(func() { time.Sleep(1200 * time.Millisecond) })()
	out, err := run(t, "run", "claude", "--timeout", "1s")
	line := "✗ give the run a longer --timeout: the run was cancelled at its time limit of 1s, before claude started\nqory run: the record is in "
	if cmd.ExitCode(err) != 124 || !strings.Contains(out, line) {
		t.Fatalf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), line, out)
	}
	lacks(t, out, "hello from", "exited")
	rec := sessionRecord(t, root)
	if got, want := typesOf(rec), []string{"run.started", "run.exited"}; !slices.Equal(got, want) {
		t.Errorf("the record's events are %q, want %q", got, want)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.run.exited")); state != "cancelled" || reason != "timeout" || exit != 124 {
		t.Errorf("run.exited is %s, %q, %v", state, reason, exit)
	}
}

// TestRunPassesTheJoinTheTimeLeft is a run with a limit of 1h: the session joins with the
// time left of it, which the compose took a part of.
func TestRunPassesTheJoinTheTimeLeft(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	defer cmd.SetBeforeJoin(func() { time.Sleep(100 * time.Millisecond) })()
	var left []time.Duration
	defer cmd.SetJoinSession(func(s session.SessionSpec) { left = append(left, s.Timeout) })()
	out, err := run(t, "run", "claude", "--timeout", "1h")
	if cmd.ExitCode(err) != 3 || len(left) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(left), err, cmd.ExitCode(err), out)
	}
	if left[0] >= time.Hour-100*time.Millisecond || left[0] < time.Hour-time.Minute {
		t.Errorf("the join's time limit is %s, want what is left of 1h", left[0])
	}
}

// TestRunCancelsARunStoppedBeforeTheJoin is SIGTERM to qory run after the compose and
// before the session joins: the run is cancelled with the reason interrupted and -1, no
// agent starts, and qory names the signal and exits 1.
func TestRunCancelsARunStoppedBeforeTheJoin(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	defer cmd.SetBeforeJoin(func() {
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
	})()
	out, err := run(t, "run", "claude")
	line := "✗ the run was cancelled: qory run got SIGTERM before claude started\nqory run: the record is in "
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, line) {
		t.Fatalf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), line, out)
	}
	lacks(t, out, "hello from", "exited")
	rec := sessionRecord(t, root)
	if got, want := typesOf(rec), []string{"run.started", "run.exited"}; !slices.Equal(got, want) {
		t.Errorf("the record's events are %q, want %q", got, want)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.run.exited")); state != "cancelled" || reason != "interrupted" || exit != -1 {
		t.Errorf("run.exited is %s, %q, %v", state, reason, exit)
	}
}

// TestRunClosesARunWhoseJoinCheckFails is a walled run with a read-only mount inside the
// checkout, which the container sees writable: the join check fails, the session records
// its session.exited with the code mount_mode_conflict, and the run closes failed with
// no reason and -1, since a join started; qory says why, names the record and exits 1.
func TestRunClosesARunWhoseJoinCheckFails(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	vendor := filepath.Join(root, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run", "claude", "--mount", vendor+":ro")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "(mount_mode_conflict)\nqory run: the record is in ") {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	rec := sessionRecord(t, root)
	if got, want := typesOf(rec), []string{"run.started", "session.exited", "run.exited"}; !slices.Equal(got, want) {
		t.Errorf("the record's events are %q, want %q", got, want)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.session.exited")); state != "failed" || reason != "mount_mode_conflict" || exit != -1 {
		t.Errorf("session.exited is %s, %q, %v", state, reason, exit)
	}
	if state, reason, exit := endOf(dataOf(rec, "dev.qory.run.exited")); state != "failed" || reason != "" || exit != -1 {
		t.Errorf("run.exited is %s, %q, %v", state, reason, exit)
	}
}

// TestRunRecordsTheHarnessCommits is a stack with a module from a git repository at a
// tag and a module from a path: session.started lists the git source alone, as the
// report writes it, with the full commit the compose used.
func TestRunRecordsTheHarnessCommits(t *testing.T) {
	root := newCheckout(t)
	remote := tempDir(t)
	runGit(t, remote, "init", "--quiet", "--initial-branch=main")
	runGit(t, remote, "config", "user.name", "Tester")
	runGit(t, remote, "config", "user.email", "tester@example.com")
	writeManifest(t, filepath.Join(remote, "modules", "core"), "core")
	writeFile(t, filepath.Join(remote, "modules", "core", "AGENTS.md"), "# Core\n")
	runGit(t, remote, "add", "-A")
	runGit(t, remote, "commit", "-q", "-m", "first")
	runGit(t, remote, "tag", "v1")
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = remote
	head, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	writeManifest(t, filepath.Join(root, "local"), "local")
	url := "file://" + remote
	fakeLaunch(t, fakeRuntime(t))
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: "+url+", ref: v1, path: modules/core}\n  - source: {path: ./local}\n")
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	harness, _ := dataOf(sessionRecord(t, root), "dev.qory.session.started")["harness"].([]any)
	want := []any{map[string]any{"source": url + "#v1:modules/core", "commit": commit}}
	if b, w := mustJSON(t, harness), mustJSON(t, want); b != w {
		t.Errorf("session.started lists the harness %s, want %s", b, w)
	}
}

// firstLine is the first line of out.
func firstLine(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	return line
}

// mustJSON is v as JSON.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestARunKeySaysItsGatewayBeforeTheCompose is a run given a run key: the line that names
// the gateway and the run comes first, the compose's line after it, and the run goes on
// to the join.
func TestARunKeySaysItsGatewayBeforeTheCompose(t *testing.T) {
	runKeyCheckout(t)
	out, _, n := inCheckoutRun(t)
	lines := strings.SplitN(out, "\n", 3)
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "qory run: through the gateway ") || !strings.HasPrefix(lines[1], "qory run: composed ") {
		t.Errorf("the first two lines are not the gateway's and the compose's:\n%s", out)
	}
	if !n.joined {
		t.Errorf("the run did not go on to the join\n%s", out)
	}
}

// TestRunLeavesOutTheWalledRunItOpened is a run opened nowhere, unwalled and then
// walled. Between the open and the join, the places' checks leave out the walled run's
// id, which its open listed among the walled runs going, and no id for the unwalled
// run, which Forager lists nowhere. Once the run ends, the checks leave out no run.
func TestRunLeavesOutTheWalledRunItOpened(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	_, restore := cmd.OpenNowhere()
	defer restore()
	var seen []string
	defer cmd.SetBeforeJoin(func() { seen = append(seen, places.ThisRun()) })()
	const unwalled, walled = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e01", "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e02"
	if out, err := run(t, "run", "claude", "--run-id", unwalled); !errors.Is(err, cmd.ErrJoinedNowhere) {
		t.Fatalf("the unwalled run: %v\n%s", err, out)
	}
	walledSection(t)
	if out, err := run(t, "run", "claude", "--run-id", walled); !errors.Is(err, cmd.ErrJoinedNowhere) {
		t.Fatalf("the walled run: %v\n%s", err, out)
	}
	if want := []string{"", walled}; !slices.Equal(seen, want) {
		t.Errorf("the checks left out %q before the joins, want %q", seen, want)
	}
	if id := places.ThisRun(); id != "" {
		t.Errorf("after the runs, the checks leave out %q", id)
	}
}
