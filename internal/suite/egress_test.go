package suite_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

// eventsFile writes lines as the events file: each is one JSON object.
func eventsFile(t *testing.T, lines ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "events.jsonl")
	write(t, file, strings.Join(lines, "\n")+"\n")
	return file
}

func started(seq, step int, kind string) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.step_started","sequence":"%010d","data":{"step":%d,"name":"after","kind":%q}}`, seq, step, kind)
}

func exited(seq, step int) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.step_exited","sequence":"%010d","data":{"step":%d,"state":"failed","exit_code":1}}`, seq, step)
}

func egress(seq int, host, decision, channel string) string {
	extra := ""
	if channel != "" {
		extra = fmt.Sprintf(`,"channel":%q`, channel)
	}
	return fmt.Sprintf(`{"type":"dev.qory.run.egress","sequence":"%010d","data":{"host":%q,"port":443,"method":"CONNECT","decision":%q%s}}`, seq, host, decision, extra)
}

func TestReadRefusedTakesOnlyTheRefusalsOfTheAfterStep(t *testing.T) {
	file := eventsFile(t,
		egress(3, "before.example", "denied", ""),
		started(4, 2, "after"),
		egress(5, "registry.example", "denied", ""),
		egress(6, "allowed.example", "allowed", ""),
		egress(7, "stack.example", "denied", "stack"),
		egress(8, "registry.example", "denied", ""),
		egress(9, "cdn.example", "denied", ""),
		exited(10, 2),
		egress(11, "after.example", "denied", ""),
	)
	got := suite.ReadRefused(file, 2)
	if !got.Local || got.Denied != 3 || !slices.Equal(got.Hosts, []string{"registry.example", "cdn.example"}) {
		t.Errorf("ReadRefused = %+v", got)
	}
}

func TestReadRefusedReadsTheStepOfTheNumberAsked(t *testing.T) {
	file := eventsFile(t,
		started(2, 3, "setup"), egress(3, "setup.example", "denied", ""), exited(4, 3),
		started(5, 7, "after"), egress(6, "one.example", "denied", ""), exited(7, 7),
		started(8, 9, "after"), egress(9, "two.example", "denied", ""), exited(10, 9),
	)
	if got := suite.ReadRefused(file, 9); !slices.Equal(got.Hosts, []string{"two.example"}) {
		t.Errorf("step 9: %+v", got)
	}
	if got := suite.ReadRefused(file, 3); got.Local {
		t.Errorf("a step of another kind counts as the after step: %+v", got)
	}
}

func TestReadRefusedRunsToTheEndOfAFileWithNoExit(t *testing.T) {
	file := eventsFile(t, started(1, 2, "after"), egress(2, "late.example", "denied", ""))
	if got := suite.ReadRefused(file, 2); !got.Local || !slices.Equal(got.Hosts, []string{"late.example"}) {
		t.Errorf("ReadRefused = %+v", got)
	}
}

func TestReadRefusedLeavesOutAHostThatIsNoNameOrAddress(t *testing.T) {
	file := eventsFile(t,
		started(1, 2, "after"),
		egress(2, "Bad Host\\nAllow evil", "denied", ""),
		egress(3, "a_b.example", "denied", ""),
		egress(4, "2001:db8::1", "denied", ""),
		egress(5, "192.0.2.7", "denied", ""),
		egress(6, strings.Repeat("a", 254), "denied", ""),
		egress(7, "fe80::1%eth0", "denied", ""),
		egress(8, "fe80::1%a\nb", "denied", ""),
		egress(9, "fe80::1%a b", "denied", ""),
		egress(10, "fe80::1%<b>", "denied", ""),
		exited(11, 2),
	)
	got := suite.ReadRefused(file, 2)
	if got.Denied != 9 || !slices.Equal(got.Hosts, []string{"2001:db8::1", "192.0.2.7"}) {
		t.Errorf("ReadRefused = %+v", got)
	}
}

func TestReadRefusedIsNotLocalWithNoFileOrNoStep(t *testing.T) {
	if got := suite.ReadRefused(filepath.Join(t.TempDir(), "missing.jsonl"), 2); got.Local || got.Denied != 0 {
		t.Errorf("a missing file: %+v", got)
	}
	file := eventsFile(t, egress(1, "a.example", "denied", ""))
	if got := suite.ReadRefused(file, 2); got.Local || got.Hosts != nil {
		t.Errorf("a file with no after step: %+v", got)
	}
}

func TestReadRefusedOrdersTheWindowBySequenceNotByLine(t *testing.T) {
	file := eventsFile(t,
		egress(8, "second.example", "denied", ""),
		started(2, 2, "after"),
		egress(5, "first.example", "denied", ""),
		exited(9, 2),
		egress(12, "out.example", "denied", ""),
	)
	if got := suite.ReadRefused(file, 2); !slices.Equal(got.Hosts, []string{"first.example", "second.example"}) {
		t.Errorf("ReadRefused = %+v", got)
	}
}

func TestReadRefusedReadsPastAnEventWhoseDataIsOfAnotherForm(t *testing.T) {
	file := eventsFile(t,
		`{"type":"dev.qory.run.log","sequence":"0000000001","data":"a line"}`,
		started(2, 2, "after"),
		`{"type":"dev.qory.run.log","sequence":"0000000003","data":["a","b"]}`,
		`{"type":"dev.qory.run.egress","sequence":"0000000004","data":"not an object"}`,
		egress(5, "registry.example", "denied", ""),
		exited(6, 2),
	)
	if got := suite.ReadRefused(file, 2); !slices.Equal(got.Hosts, []string{"registry.example"}) || got.Denied != 1 {
		t.Errorf("ReadRefused = %+v", got)
	}
}
