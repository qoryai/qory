package pool_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The lines of the machine log's upload.
const (
	lineLogUploaded    = "uploaded qory run's output as the dispatch's machine log"
	lineLogNotUploaded = "qory run's output could not be uploaded as the dispatch's machine log"
)

// machineLogs are the bodies of the machine logs sent for the dispatch id, in order,
// with the status each got.
func (h *harness) machineLogs(id string) (logs []contracts.MachineLog, bodies [][]byte, statuses []int) {
	h.t.Helper()
	for _, c := range h.srv.Calls() {
		if c.Route != provisionertest.RouteMachineLog || c.DispatchID != id {
			continue
		}
		var ml contracts.MachineLog
		if err := json.Unmarshal(c.Body, &ml); err != nil {
			h.t.Fatal(err)
		}
		logs, bodies, statuses = append(logs, ml), append(bodies, c.Body), append(statuses, c.Status)
	}
	return logs, bodies, statuses
}

// TestTheOutputIsTheMachineLog runs one dispatch whose qory run prints two lines, one
// with the run code, and lines that contain qak_, qrk_ and qecr_ in other cases: once
// qory run ended, the pool uploads its output as the dispatch's machine log, once,
// after its end's line. Each line is as the pool's log has it, without the dispatch id,
// so each line with what starts a secret is the pool's fixed line. The log is not
// truncated, and the fake recorded it. The fake got claim, dispatched and the machine
// log, in that order, and after them only the pool's next claims, which may come at any
// time before it stops.
func TestTheOutputIsTheMachineLog(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Say: []string{"key QAK~abc", "run key qRk~abc.def", "code QECR~ABC", "bye"}})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	h.stop()
	d := h.only()
	logs, _, statuses := h.machineLogs(d.ID)
	if len(logs) != 1 || statuses[0] != 204 {
		t.Fatalf("%d machine logs were sent, answered %v", len(logs), statuses)
	}
	want := "hello\n" + strings.Repeat(withheld+"\n", 4) + "bye\n"
	if logs[0].Log != want || logs[0].Truncated {
		t.Errorf("the machine log is %q, truncated %v, want %q", logs[0].Log, logs[0].Truncated, want)
	}
	if got, _ := h.srv.Dispatch(d.ID); got.MachineLog == nil || got.MachineLog.Log != want {
		t.Errorf("the fake recorded %v", got.MachineLog)
	}
	routes := h.srv.Routes()
	i := slices.Index(routes, provisionertest.RouteMachineLog)
	order := []provisionertest.Route{provisionertest.RouteClaim, provisionertest.RouteDispatched, provisionertest.RouteMachineLog}
	if i < 0 || !slices.Equal(routes[:i+1], order) || slices.ContainsFunc(routes[i+1:], func(r provisionertest.Route) bool { return r != provisionertest.RouteClaim }) {
		t.Errorf("the fake was sent %v, want %v, then claims alone", routes, order)
	}
	if m := h.log.with(lineLogUploaded); len(m) != 1 || m[0]["dispatch_id"] != d.ID {
		t.Errorf("the upload's lines are %v", m)
	}
	all := h.log.all()
	ended, uploaded := -1, -1
	for i, m := range all {
		switch m["msg"] {
		case lineEnded:
			ended = i
		case lineLogUploaded:
			uploaded = i
		}
	}
	if ended < 0 || uploaded < ended {
		t.Errorf("the upload is not after qory run's end:\n%s", h.log.text())
	}
}

// TestAQuietRunGetsAnEmptyMachineLog uploads the empty log of a qory run that printed
// nothing: the contract asks for the log of every machine, and an empty text is one.
func TestAQuietRunGetsAnEmptyMachineLog(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Quiet: true})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	h.stop()
	logs, _, statuses := h.machineLogs(h.only().ID)
	if len(logs) != 1 || statuses[0] != 204 || logs[0].Log != "" || logs[0].Truncated {
		t.Errorf("the machine logs are %+v, answered %v", logs, statuses)
	}
}

// TestALongOutputIsCutToItsBounds prints 2 MiB of lines after the first lines: the
// machine log is the end of the output, from the start of a line, at most
// provisioner.MaxLog bytes in a body of at most a mebibyte, valid UTF-8, and truncated.
func TestALongOutputIsCutToItsBounds(t *testing.T) {
	const n = 2 << 20 / 101
	h := newHarness(t, 1)
	h.script(script{Bulk: n})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	h.stop()
	logs, bodies, statuses := h.machineLogs(h.only().ID)
	if len(logs) != 1 || statuses[0] != 204 {
		t.Fatalf("%d machine logs were sent, answered %v", len(logs), statuses)
	}
	got, out := logs[0].Log, bulkOutput(n)
	switch {
	case !logs[0].Truncated:
		t.Error("the machine log is not truncated")
	case len(got) > provisioner.MaxLog || len(bodies[0]) > contracts.MaxRequest || !utf8.ValidString(got):
		t.Errorf("%d bytes of log, a body of %d bytes", len(got), len(bodies[0]))
	case len(got) < provisioner.MaxLog-101:
		t.Errorf("only %d bytes of log were kept", len(got))
	case !strings.HasSuffix(out, got) || !strings.HasSuffix(out[:len(out)-len(got)], "\n"):
		t.Errorf("the machine log is not the end of the output from a line's start: %.200q", got)
	}
}

// TestAMachineLogThatFailsChangesNoEnd answers the machine log with each refusal and
// with a server error on every try: the pool logs that it could not upload the log,
// with the error, does not send it again, and the dispatch ends as it would: qory run's
// end is logged with its exit status, nothing is released, and the journal is empty.
func TestAMachineLogThatFailsChangesNoEnd(t *testing.T) {
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		want    string
	}{
		{"machine_log_exists", []provisionertest.Reply{{Status: 409, Code: "machine_log_exists"}}, "409 machine_log_exists"},
		{"forbidden", []provisionertest.Reply{{Status: 403, Code: "forbidden"}}, "403 forbidden"},
		{"rate_limited", []provisionertest.Reply{{Status: 429, Code: "rate_limited", RetryAfter: "5"}}, "429 rate_limited"},
		{"server error", []provisionertest.Reply{{Status: 503}, {Status: 503}, {Status: 503}, {Status: 503}, {Status: 503}}, "503"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.script(script{Exit: 3})
			h.srv.Script(provisionertest.RouteMachineLog, c.replies...)
			h.srv.Offer(provisionertest.Run{})
			h.start()
			h.settled(1)
			h.stop()
			d := h.only()
			logs, _, _ := h.machineLogs(d.ID)
			if len(logs) != len(c.replies) {
				t.Errorf("%d machine logs were sent, want %d", len(logs), len(c.replies))
			}
			m := h.log.with(lineLogNotUploaded)
			if len(m) != 1 || m[0]["dispatch_id"] != d.ID || !strings.Contains(m[0]["error"].(string), c.want) {
				t.Errorf("the failed upload's lines are %v", m)
			}
			if len(h.log.with(lineLogUploaded)) != 0 {
				t.Error("the upload is logged as done")
			}
			if m := h.log.with(lineEnded); len(m) != 1 || m[0]["exit_code"] != float64(3) {
				t.Errorf("the end's lines are %v", m)
			}
			h.notReleased()
			if lines := journalOf(t, h.state); len(lines) != 0 {
				t.Errorf("the journal is %q", lines)
			}
		})
	}
}
