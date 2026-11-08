package cmd_test

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
)

// runsFixture is a fixture of the run list contract, by its path under
// contracts/diagnostics/v1.
func runsFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(contracts.DiagnosticsFS, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// listKeyed starts a server that answers the run list with the fixture name, signed, and
// stores a diagnostics key for it.
func listKeyed(t *testing.T, name string) *diagnosticsServer {
	t.Helper()
	s, _ := diagnoseKeyed(t)
	s.set(http.StatusOK, runsFixture(t, name), false)
	return s
}

// runList runs qory apiary run list with args.
func runList(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runApart(t, append([]string{"apiary", "run", "list"}, args...)...)
}

// sentQuery is the query of the server's last request.
func sentQuery(t *testing.T, s *diagnosticsServer) url.Values {
	t.Helper()
	q, err := url.ParseQuery(s.query)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// TestApiaryRunListPrintsOneLinePerRun is a page with a next page: one line per run in
// aligned columns on stdout after the title, the command for the next page on stderr,
// and limit 25 in the request.
func TestApiaryRunListPrintsOneLinePerRun(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/page-with-next.json")
	out, errOut, err := runList(t)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	want := "🐝 " + hostOf(t) + " · runs\n" +
		"  rn_0123456789abcdeh  42  example-org/example  hello  7  completed  2026-11-01T10:02:10Z  13m32s\n" +
		"  rn_0123456789abcdeg  41  example-org/example  hello  6  failed     2026-11-01T09:01:00Z  1m35s\n"
	if out != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
	if want := "Next page: qory apiary run list --before rn_0123456789abcdeg\n"; errOut != want {
		t.Errorf("stderr %q, want %q", errOut, want)
	}
	if s.path != "/v1/runs" || s.query != "limit=25" || s.requests != 1 {
		t.Errorf("%d requests, path %q, query %q", s.requests, s.path, s.query)
	}
}

// TestApiaryRunListShowsADashForAMissingValue is a run with no work item and no
// started_at, and a run with no workflow: a - in each of those columns.
func TestApiaryRunListShowsADashForAMissingValue(t *testing.T) {
	listKeyed(t, "fixtures/runs/running-and-pending.json")
	out, _, err := runList(t)
	if err != nil {
		t.Fatal(err)
	}
	want := "  rn_0123456789abcdem  45  example-org/example  hello  -  pending  -                     -\n" +
		"  rn_0123456789abcdek  44  example-org/example  hello  8  running  2026-11-01T11:01:30Z  0s\n"
	if _, rows, _ := strings.Cut(out, "\n"); rows != want {
		t.Errorf("rows:\n%s\nwant:\n%s", rows, want)
	}
	listKeyed(t, "fixtures/runs/without-work-item.json")
	out, _, err = runList(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, rows, _ := strings.Cut(out, "\n"); rows != "  rn_0123456789abcdej  43  example-org/example  -  -  completed  2026-11-01T10:31:00Z  1m00s\n" {
		t.Errorf("rows %q", rows)
	}
}

// TestApiaryRunListShowsAnUnknownStateAsItIs is a state that is not one of the six.
func TestApiaryRunListShowsAnUnknownStateAsItIs(t *testing.T) {
	listKeyed(t, "fixtures/runs/state-not-listed.json")
	out, errOut, err := runList(t)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	if _, rows, _ := strings.Cut(out, "\n"); rows != "  rn_0123456789abcden  46  example-org/example  hello  7  waiting  -  -\n" {
		t.Errorf("rows %q", rows)
	}
	if errOut != "" {
		t.Errorf("stderr %q on the last page", errOut)
	}
}

// TestApiaryRunListShortensADuration is a duration under a minute, under an hour, and of
// hours, in seconds, minutes and seconds, and hours and minutes.
func TestApiaryRunListShortensADuration(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	row := func(id string, seconds string) string {
		return `{"id":"` + id + `","number":1,"organisation":{"slug":"acme"},"workspace":{"id":"ws_0123456789abcdef","slug":"web"},` +
			`"state":"completed","created_at":"2026-11-01T10:00:00Z","started_at":"2026-11-01T10:00:00Z","duration_seconds":` + seconds + `}`
	}
	s.set(http.StatusOK, []byte(`{"runs":[`+row("rn_0000000000000003", "45")+`,`+row("rn_0000000000000002", "252")+`,`+
		row("rn_0000000000000001", "3725")+`,`+row("rn_0000000000000000", "93784")+`],"next":null}`), false)
	out, errOut, err := runList(t)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Fields(line)
		got = append(got, f[len(f)-1])
	}
	if strings.Join(got, " ") != "45s 4m12s 1h02m 26h03m" {
		t.Errorf("durations %q\n%s", got, out)
	}
}

// TestApiaryRunListSendsEachFlag is every flag that selects runs: each reaches the query
// once, --since in UTC, and the command for the next page keeps each flag, the old
// --before replaced.
func TestApiaryRunListSendsEachFlag(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/page-with-next.json")
	_, errOut, err := runList(t, "--workspace", "acme/web", "--work-item", "7", "--workflow", "hello", "--state", "failed",
		"--since", "2026-11-01T10:00:00.5+02:00", "--limit", "2", "--before", "rn_0123456789abcdez")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	q := sentQuery(t, s)
	want := url.Values{
		"workspace": {"acme/web"}, "work_item": {"7"}, "workflow": {"hello"}, "state": {"failed"},
		"since": {"2026-11-01T08:00:00.5Z"}, "limit": {"2"}, "before": {"rn_0123456789abcdez"},
	}
	if q.Encode() != want.Encode() {
		t.Errorf("query %q, want %q", q.Encode(), want.Encode())
	}
	line := "Next page: qory apiary run list --workspace acme/web --work-item 7 --workflow hello --state failed " +
		"--since 2026-11-01T08:00:00.5Z --limit 2 --before rn_0123456789abcdeg\n"
	if errOut != line {
		t.Errorf("stderr %q, want %q", errOut, line)
	}
}

// TestApiaryRunListCountsADurationSinceBackFromNow is --since 1h: the time one hour
// before the command ran, in UTC to the second, and that time in the next page's command.
func TestApiaryRunListCountsADurationSinceBackFromNow(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/page-with-next.json")
	before := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, errOut, err := runList(t, "--since", "1h")
	after := time.Now().UTC().Add(-time.Hour)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	since := sentQuery(t, s).Get("since")
	at, err := time.Parse(time.RFC3339, since)
	if err != nil || !strings.HasSuffix(since, "Z") || at.Before(before) || at.After(after) || at.Nanosecond() != 0 {
		t.Errorf("since %q, want between %s and %s, in UTC to the second", since, before, after)
	}
	if want := "Next page: qory apiary run list --since " + since + " --before rn_0123456789abcdeg\n"; errOut != want {
		t.Errorf("stderr %q, want %q", errOut, want)
	}
}

// TestApiaryRunListTakesALimitFrom1To100 is the bounds of --limit, sent as given.
func TestApiaryRunListTakesALimitFrom1To100(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/last-page.json")
	for _, limit := range []string{"1", "100"} {
		if _, errOut, err := runList(t, "--limit", limit); err != nil {
			t.Fatalf("--limit %s: %v\n%s", limit, err, errOut)
		}
		if s.query != "limit="+limit {
			t.Errorf("--limit %s: query %q", limit, s.query)
		}
	}
}

// TestApiaryRunListStopsAtAWrongValue is each flag with a value outside its rule: qory
// stops with exit 2 before any request, with the fix on the first line and the value on
// the next.
func TestApiaryRunListStopsAtAWrongValue(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/last-page.json")
	fixes := map[string]string{
		"--workspace": "Pass --workspace as <org>/<workspace>, such as acme/web: two slugs of a-z, 0-9 and -.",
		"--work-item": "Pass --work-item as the work item's number in the workspace, such as 7.",
		"--workflow":  "Pass --workflow as a workflow's name, such as hello: a-z, then a-z, 0-9 and -.",
		"--state":     "Pass --state as one of pending, running, completed, failed, cancelled or lost.",
		"--since":     "Pass --since as a duration, such as 1h, or a time, such as 2026-11-01T09:00:00Z.",
		"--limit":     "Pass --limit as a number from 1 to 100.",
		"--before":    "Pass --before as a run id, rn_ and 16 characters.",
	}
	for _, c := range []struct{ flag, value string }{
		{"--workspace", "acme"}, {"--workspace", "Acme/web"}, {"--workspace", "acme/web/x"}, {"--workspace", "-acme/web"},
		{"--workspace", "acme/" + strings.Repeat("w", 41)}, {"--workspace", ""},
		{"--work-item", "0"}, {"--work-item", "07"}, {"--work-item", "9223372036854775808"}, {"--work-item", "#7"},
		{"--workflow", "Hello"}, {"--workflow", "1hello"}, {"--workflow", "h" + strings.Repeat("e", 55)},
		{"--state", "Failed"}, {"--state", "waiting"}, {"--state", ""},
		{"--since", "yesterday"}, {"--since", "-1h"}, {"--since", "0s"}, {"--since", "2026-11-01"}, {"--since", "1d"},
		{"--since", "9999-12-31T23:00:00-05:00"},
		{"--limit", "0"}, {"--limit", "101"}, {"--limit", "01"}, {"--limit", "ten"}, {"--limit", ""},
		{"--before", "rn_0123"}, {"--before", "42"},
	} {
		args := []string{c.flag, c.value}
		if c.flag == "--work-item" {
			args = append(args, "--workspace", "acme/web")
		}
		out, _, err := runList(t, args...)
		if err == nil {
			t.Fatalf("%s %q succeeded", c.flag, c.value)
		}
		if cmd.ExitCode(err) != cmd.ExitInput || out != "" {
			t.Errorf("%s %q: exit %d, stdout %q", c.flag, c.value, cmd.ExitCode(err), out)
		}
		if got := firstLine(err.Error()); got != fixes[c.flag] {
			t.Errorf("%s %q: first line %q, want %q", c.flag, c.value, got, fixes[c.flag])
		}
		wantIn(t, err.Error(), "\nYou passed ")
	}
	if s.received != 0 {
		t.Errorf("a wrong value reached the server %d times", s.received)
	}
}

func TestApiaryRunListStopsAtAWorkItemWithoutAWorkspace(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/last-page.json")
	_, _, err := runList(t, "--work-item", "7")
	if want := "Add --workspace <org>/<workspace>: a work item's number is unique only in its workspace."; err == nil || firstLine(err.Error()) != want {
		t.Errorf("--work-item alone: %v, want %q", err, want)
	}
	if cmd.ExitCode(err) != cmd.ExitInput || s.received != 0 {
		t.Errorf("exit %d, %d requests", cmd.ExitCode(err), s.received)
	}
}

func TestApiaryRunListTakesNoArgument(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/last-page.json")
	_, _, err := runList(t, "rn_0123456789abcdef")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput || s.received != 0 {
		t.Errorf("list with an argument: %v, exit %d, %d requests", err, cmd.ExitCode(err), s.received)
	}
}

// TestApiaryRunListPrintsTheJSONAsSent is --json on a page with a next page: stdout is the
// body byte for byte, and stderr has no line for the next page.
func TestApiaryRunListPrintsTheJSONAsSent(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/page-with-next.json")
	out, errOut, err := runList(t, "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, errOut)
	}
	if out != string(s.body) || errOut != "" {
		t.Errorf("stdout is not the body as sent, or stderr %q:\n%s", errOut, out)
	}
	if s.query != "limit=25" {
		t.Errorf("query %q", s.query)
	}
	// A body without a line ending gets one, and an empty list prints no line on stderr.
	s.set(http.StatusOK, []byte(`{"runs":[],"next":null}`), false)
	out, errOut, err = runList(t, "--json")
	if err != nil || out != `{"runs":[],"next":null}`+"\n" || errOut != "" {
		t.Errorf("empty list --json: %v, stdout %q, stderr %q", err, out, errOut)
	}
}

// TestApiaryRunListPrintsNoNextPageOnTheLastPage is the last page: the runs and nothing
// on stderr.
func TestApiaryRunListPrintsNoNextPageOnTheLastPage(t *testing.T) {
	listKeyed(t, "fixtures/runs/last-page.json")
	out, errOut, err := runList(t, "--workspace", "example-org/example", "--limit", "1")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "rn_0123456789abcdef  40  example-org/example  hello  6  cancelled  -  -\n") || errOut != "" {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
}

// TestApiaryRunListSaysWhenNoRunMatches is an empty list, with flags and without: one
// line on stderr, nothing on stdout, exit 0.
func TestApiaryRunListSaysWhenNoRunMatches(t *testing.T) {
	listKeyed(t, "fixtures/runs/empty.json")
	out, errOut, err := runList(t, "--state", "failed", "--limit", "5")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	if want := "Change or remove a flag to list more runs: no run on " + hostOf(t) + " matches --state failed.\n"; out != "" || errOut != want {
		t.Errorf("stdout %q, stderr %q, want %q", out, errOut, want)
	}
	out, errOut, err = runList(t)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	if want := "No action needed: " + hostOf(t) + " has no runs.\n"; out != "" || errOut != want {
		t.Errorf("stdout %q, stderr %q, want %q", out, errOut, want)
	}
}

// TestApiaryRunListRefusals is each signed refusal of the run list: the first line names
// the flag of the parameter, or follows the line of qory apiary run diagnose.
func TestApiaryRunListRefusals(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	host := hostOf(t)
	for _, c := range []struct {
		body string
		args []string
		want string
	}{
		{`{"error":"invalid_request","parameter":"workspace"}`, []string{"--workspace", "acme/web"},
			"Check --workspace acme/web: the request to " + host + " failed with invalid_request (400) for that value."},
		{`{"error":"invalid_request","parameter":"before"}`, []string{"--before", "rn_0123456789abcdef"},
			"Check --before rn_0123456789abcdef: the request to " + host + " failed with invalid_request (400) for that value."},
		{`{"error":"invalid_request","parameter":"since"}`, []string{"--since", "2026-11-01T09:00:00+01:00"},
			"Check --since 2026-11-01T08:00:00Z: the request to " + host + " failed with invalid_request (400) for that value."},
		{string(runsFixture(t, "fixtures/runs-refusal/invalid-request.json")), nil,
			"Check --limit 25: the request to " + host + " failed with invalid_request (400) for that value."},
		{`{"error":"invalid_request","parameter":"page"}`, nil,
			"Check the run list with the instance admin of " + host + ": the request was not accepted: invalid_request (400): \"page\"."},
		{`{"error":"bad_request"}`, nil,
			"Check the run list with the instance admin of " + host + ": the request was not accepted: bad_request (400)."},
		{`{"error":"unsupported_contract_version"}`, nil,
			"Update qory to a release that works with " + host + ": the request was not accepted: unsupported_contract_version (400)."},
	} {
		s.set(http.StatusBadRequest, []byte(c.body), false)
		out, _, err := runList(t, c.args...)
		if err == nil {
			t.Fatalf("%s: list succeeded", c.body)
		}
		if got := firstLine(err.Error()); got != c.want {
			t.Errorf("%s: first line %q, want %q", c.body, got, c.want)
		}
		if out != "" || cmd.ExitCode(err) != 1 {
			t.Errorf("%s: stdout %q, exit %d", c.body, out, cmd.ExitCode(err))
		}
	}
}

func TestApiaryRunListUnauthorized(t *testing.T) {
	emptyDir(t)
	// The server verifies under another key: the stored one gets the unsigned 401.
	s := newDiagnosticsServer(t, newKey(t))
	dir := storeDiagnosticsKey(t, s.URL, s.pin(), newKey(t))
	out, _, err := runList(t)
	if err == nil {
		t.Fatal("list with a key the server does not accept succeeded")
	}
	want := "Remove " + dir + ", then run `qory apiary enrol " + s.URL + " <code>` again: " + hostOf(t) + " did not accept the diagnostics key " + diagnosticsKeyID + " (401)."
	if firstLine(err.Error()) != want || out != "" {
		t.Errorf("first line %q, want %q; stdout %q", firstLine(err.Error()), want, out)
	}
}

// TestApiaryRunListStopsAtAListOutsideTheSchema is each invalid run list of the contract's
// fixtures, signed: qory prints nothing, with or without --json.
func TestApiaryRunListStopsAtAListOutsideTheSchema(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	names, err := fs.Glob(contracts.DiagnosticsFS, "fixtures/invalid/runs-*.json")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, name := range names {
		if strings.HasPrefix(name, "fixtures/invalid/runs-refusal-") {
			continue
		}
		n++
		s.set(http.StatusOK, runsFixture(t, name), false)
		for _, args := range [][]string{nil, {"--json"}} {
			out, _, err := runList(t, args...)
			if err == nil || out != "" {
				t.Fatalf("%s %v: err %v, stdout %q", name, args, err, out)
			}
			if want := "Check the run list with the instance admin of " + hostOf(t) + ": the response does not follow runs.schema.json, so qory printed nothing."; firstLine(err.Error()) != want {
				t.Errorf("%s: first line %q, want %q", name, firstLine(err.Error()), want)
			}
		}
	}
	if n < 10 {
		t.Errorf("%d invalid run lists among the fixtures", n)
	}
}

// TestApiaryRunListUnsignedPrintsNothing is a run list without a signature, and one
// signed under another key.
func TestApiaryRunListUnsignedPrintsNothing(t *testing.T) {
	s := listKeyed(t, "fixtures/runs/page-with-next.json")
	s.set(http.StatusOK, s.body, true)
	for _, args := range [][]string{nil, {"--json"}} {
		out, errOut, err := runList(t, args...)
		if err == nil || out != "" || errOut != "" {
			t.Fatalf("%v: unsigned list: err %v, stdout %q, stderr %q", args, err, out, errOut)
		}
		wantIn(t, firstLine(err.Error()), "is not signed with the pinned server key, so qory printed nothing")
	}
	s.mu.Lock()
	s.signer, s.unsigned = newKey(t), false
	s.mu.Unlock()
	out, _, err := runList(t)
	if err == nil || out != "" {
		t.Fatalf("list signed under another key: err %v, stdout %q", err, out)
	}
	wantIn(t, firstLine(err.Error()), "is not signed with the pinned server key")
}

// TestApiaryRunListSaysAnInstanceIDForThisProcess is a server directory where the
// instance id cannot be written: the line about it starts with the command's name, and
// the runs follow on stdout.
func TestApiaryRunListSaysAnInstanceIDForThisProcess(t *testing.T) {
	s, dir := diagnoseKeyed(t)
	s.set(http.StatusOK, runsFixture(t, "fixtures/runs/last-page.json"), false)
	unwritableInstanceID(t, dir)
	out, errOut, err := runList(t)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, errOut)
	}
	if !strings.HasPrefix(errOut, "qory apiary run list: instance ") || !strings.Contains(out, "rn_0123456789abcdef") {
		t.Errorf("stderr %q, stdout %q", errOut, out)
	}
}
