package provisioner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// hiddenLine is the text that replaces a machine log's line with what looks like a
// secret.
const hiddenLine = "[hidden: the line contained what looks like an access key secret, a run key, a run code or an enrolment code]"

// machineLogOf is the machine log the fake recorded for the dispatch id, and the body
// of the last request.
func machineLogOf(t *testing.T, srv *provisionertest.Server, id string) (contracts.MachineLog, []byte) {
	t.Helper()
	d, ok := srv.Dispatch(id)
	if !ok || d.MachineLog == nil {
		t.Fatalf("%s has no machine log", id)
	}
	return *d.MachineLog, last(t, srv).Body
}

// TestAMachineLogIsTheFixture sends the log of each machine-log fixture at the
// fixture's time, truncated as the fixture is: the body sent is the fixture.
func TestAMachineLogIsTheFixture(t *testing.T) {
	for _, name := range []string{"machine-log/whole.json", "machine-log/truncated.json"} {
		srv, c, clk, _ := setup(t)
		cl := claimed(t, srv, c, provisionertest.Run{})
		var f contracts.MachineLog
		if err := json.Unmarshal(fixture(t, name), &f); err != nil {
			t.Fatal(err)
		}
		tm, err := time.Parse(time.RFC3339, f.Time)
		if err != nil {
			t.Fatal(err)
		}
		clk.set(tm)
		if err := c.MachineLog(context.Background(), cl.DispatchID, f.Log, f.Truncated); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := last(t, srv); got.Route != provisionertest.RouteMachineLog || got.Target != "/v1/dispatches/"+cl.DispatchID+"/machine-log" || !sameJSON(t, got.Body, fixture(t, name)) {
			t.Errorf("%s: sent %s to %s\nwant\n%s", name, got.Body, got.Target, fixture(t, name))
		}
	}
}

// TestAnEmptyMachineLogIsSent sends the log of a machine that printed nothing: the
// body has an empty log, which the schema takes, and is not truncated.
func TestAnEmptyMachineLogIsSent(t *testing.T) {
	srv, c, _, _ := setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	if err := c.MachineLog(context.Background(), cl.DispatchID, "", false); err != nil {
		t.Fatal(err)
	}
	if got, body := machineLogOf(t, srv, cl.DispatchID); got.Log != "" || got.Truncated || strings.Contains(string(body), "truncated") {
		t.Errorf("sent %s", body)
	}
}

// TestAMachineLogIsCutToItsBounds uploads logs over each bound: 800 KiB of plain lines,
// over MaxLog; 700 KiB of characters that JSON writes as 6 bytes each, over the
// request's mebibyte once escaped; and one line with no line feed, whose cut at MaxLog
// bytes falls inside a 2-byte character. Each sent log is the end of the log, starts at
// a line when it has one and at a whole character otherwise, is valid UTF-8, is within
// both bounds, and is marked truncated. A log within its bounds is sent whole,
// truncated as the caller set it.
func TestAMachineLogIsCutToItsBounds(t *testing.T) {
	line := "2026-10-15T12:01:05Z machine step done\n"
	plain := strings.Repeat(line, 800<<10/len(line)+1)
	escaped := strings.Repeat("<<<<<<<<<\n", 70<<10)
	// The last character is 1 byte and every other 2, so a cut at MaxLog bytes from the
	// end, an even number, falls inside a character.
	oneLine := strings.Repeat(string(rune(0xe9)), 500<<10) + "x"
	for name, log := range map[string]string{"plain": plain, "escaped": escaped, "one line": oneLine} {
		srv, c, _, _ := setup(t)
		cl := claimed(t, srv, c, provisionertest.Run{})
		if err := c.MachineLog(context.Background(), cl.DispatchID, log, false); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, body := machineLogOf(t, srv, cl.DispatchID)
		switch {
		case !got.Truncated || !strings.HasSuffix(log, got.Log) || len(got.Log) > provisioner.MaxLog || len(body) > contracts.MaxRequest ||
			!utf8.ValidString(got.Log) || !utf8.Valid(body) || strings.ContainsRune(got.Log, utf8.RuneError):
			t.Errorf("%s: truncated %v, %d bytes of log, a body of %d bytes", name, got.Truncated, len(got.Log), len(body))
		case name != "one line" && !strings.HasSuffix(log[:len(log)-len(got.Log)], "\n"):
			t.Errorf("%s: the log does not start at a line", name)
		case len(got.Log) < provisioner.MaxLog/2 && name != "escaped":
			t.Errorf("%s: only %d bytes kept", name, len(got.Log))
		}
	}
	for _, truncated := range []bool{false, true} {
		srv, c, _, _ := setup(t)
		cl := claimed(t, srv, c, provisionertest.Run{})
		if err := c.MachineLog(context.Background(), cl.DispatchID, "a line\n", truncated); err != nil {
			t.Fatal(err)
		}
		got, body := machineLogOf(t, srv, cl.DispatchID)
		if got.Log != "a line\n" || got.Truncated != truncated || strings.Contains(string(body), "truncated") != truncated {
			t.Errorf("truncated %v: sent %s", truncated, body)
		}
	}
}

// TestAMachineLogIsCleaned uploads a log with lines that contain an access key secret,
// a run key and a run code in any case, an enrolment code of each prefix, qecn_, qecp_,
// qecg_ and qeci_, in mixed case, a U+0000 and a run of 3 bytes that are not UTF-8: each
// such line is replaced with a fixed text, the U+0000 with U+FFFD, and the run of bytes
// with one U+FFFD.
func TestAMachineLogIsCleaned(t *testing.T) {
	srv, c, _, _ := setup(t)
	cl := claimed(t, srv, c, provisionertest.Run{})
	log := "start\nkey qak_abc\nrun key QRK_abc.def\ncode qecr_ABC\nn QeCn_A\np qECP_A\ng QecG_A\ni qEcI_A\na\x00b\nc\xff\xfe\xfdd\nend"
	if err := c.MachineLog(context.Background(), cl.DispatchID, log, false); err != nil {
		t.Fatal(err)
	}
	got, _ := machineLogOf(t, srv, cl.DispatchID)
	hidden := hiddenLine + "\n"
	r := string(utf8.RuneError)
	if want := "start\n" + strings.Repeat(hidden, 7) + "a" + r + "b\nc" + r + "d\nend"; got.Log != want || got.Truncated {
		t.Errorf("sent %q, want %q", got.Log, want)
	}
}

// TestAMachineLogOverItsBoundHasNoSecret uploads a log with lines of the run code the
// fake handed out, a run key and the pool's access key secret, in upper and lower case,
// at its start and at its end, around lines that JSON escapes to more than a mebibyte.
// The body sent is within the request's bound, truncated is set, the log is the end of
// the log with each such line replaced, and no request and no line the client logged
// contains qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in any case, or a secret.
func TestAMachineLogOverItsBoundHasNoSecret(t *testing.T) {
	srv, c, _, log := setup(t)
	run := srv.Offer(provisionertest.Run{})[0]
	cl, ok, err := c.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	cl.RunCode.Clear()
	key := srv.Pool.Secret()
	markers := "run code " + run.RunCode + "\nrun key QRK_ABCDEFGHIJKLMNOP\nkey " + strings.ToUpper(key) + "\nkey " + key + "\n"
	sent := markers + strings.Repeat("<<<<<<<<<\n", 70<<10) + markers + "end\n"
	if err := c.MachineLog(context.Background(), cl.DispatchID, sent, false); err != nil {
		t.Fatal(err)
	}
	got, body := machineLogOf(t, srv, cl.DispatchID)
	switch {
	case !got.Truncated || len(body) > contracts.MaxRequest || len(got.Log) > provisioner.MaxLog:
		t.Errorf("truncated %v, a body of %d bytes", got.Truncated, len(body))
	case !strings.HasSuffix(got.Log, "<<<<<<<<<\n"+strings.Repeat(hiddenLine+"\n", 4)+"end\n"):
		t.Error("the end of the log is not the log's end with each secret's line replaced")
	}
	texts := []string{log.String()}
	for _, call := range srv.Calls() {
		texts = append(texts, string(call.Body))
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		if accesskey.ContainsSecret(text) || strings.Contains(lower, "qecr_") || strings.Contains(text, run.RunCode[5:]) || strings.Contains(text, key[4:]) {
			t.Errorf("a secret was sent or logged: %.200q", text)
		}
	}
}

// TestTheMachineLogsRefusals walks the fake's machine logs through the contract's
// refusals: a log of an unknown dispatch is 403 forbidden; a second log of a dispatch
// is 409 machine_log_exists, and the first one stays; a log of a dispatch that ended,
// at its release or at Qory Apiary, is taken, as the contract takes one before or after
// the end.
func TestTheMachineLogsRefusals(t *testing.T) {
	srv, c, _, _ := setup(t)
	ctx := context.Background()
	if err := c.MachineLog(ctx, "dp_0000000000000000", "a line\n", false); !errors.Is(err, provisioner.ErrForbidden) {
		t.Errorf("an unknown dispatch: %v", err)
	}
	cl := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Dispatched(ctx, cl.DispatchID); err != nil {
		t.Fatal(err)
	}
	if err := c.MachineLog(ctx, cl.DispatchID, "first\n", false); err != nil {
		t.Fatal(err)
	}
	err := c.MachineLog(ctx, cl.DispatchID, "second\n", false)
	if !errors.Is(err, provisioner.ErrMachineLogExists) || errors.Is(err, provisioner.ErrMachineExists) {
		t.Errorf("a second machine log: %v", err)
	}
	if want := "the machine log of " + cl.DispatchID + ": Qory Apiary returned 409 machine_log_exists"; err == nil || err.Error() != want {
		t.Errorf("a second machine log: %v, want %s", err, want)
	}
	if got, _ := srv.Dispatch(cl.DispatchID); got.MachineLog == nil || got.MachineLog.Log != "first\n" {
		t.Errorf("the machine log kept is %v", got.MachineLog)
	}

	released := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Release(ctx, released.DispatchID, "a reason"); err != nil {
		t.Fatal(err)
	}
	if err := c.MachineLog(ctx, released.DispatchID, "after the release\n", false); err != nil {
		t.Errorf("a machine log after the release: %v", err)
	}
	ended := claimed(t, srv, c, provisionertest.Run{})
	if err := c.Dispatched(ctx, ended.DispatchID); err != nil {
		t.Fatal(err)
	}
	srv.End(ended.DispatchID)
	if err := c.Dispatched(ctx, ended.DispatchID); !errors.Is(err, provisioner.ErrGone) {
		t.Errorf("dispatched after the end: %v", err)
	}
	if err := c.MachineLog(ctx, ended.DispatchID, "after the end\n", false); err != nil {
		t.Errorf("a machine log after the end: %v", err)
	}
	if err := c.MachineLog(ctx, "dp_x", "a line\n", false); err == nil || !strings.Contains(err.Error(), "the dispatch id is not dp_ and 16 lower-case Crockford base32 characters") {
		t.Errorf("a dispatch id outside its shape: %v", err)
	}
}
