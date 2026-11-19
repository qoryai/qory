package cmd

import (
	"testing"

	"github.com/qoryai/integrations/contracts"
)

// TestTheSubmitContractNamesAreThePinnedOnes is the names, limits and reasons of the submit
// program in cmd/submitcontract.go and cmd/submitprogram.go against the integrations module
// that go.mod pins (contracts/submit_program.go): a pin that renames or changes one fails here.
func TestTheSubmitContractNamesAreThePinnedOnes(t *testing.T) {
	for _, c := range []struct{ name, got, want string }{
		{"line prefix", fmtPrefix(textProgramLine), contracts.SubmitLinePrefix},
	} {
		if c.got != c.want {
			t.Errorf("%s is %q, the pinned contract has %q", c.name, c.got, c.want)
		}
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		// The limit of commits is a copy of the contract's 100: the pinned package does not
		// export it, so a pin that changes it is read in the README's table, not here.
		{"commits", maxSubmitCommits, 100},
		{"printed lines", maxProgramLines, contracts.SubmitPrintedLines},
		{"time limit seconds", int(programLimit.Seconds()), int(contracts.SubmitTimeLimit.Seconds())},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d, the pinned contract has %d", c.name, c.got, c.want)
		}
	}
}

// TestTheMarkerLinesAreThePinnedOnes is the marker lines of the draft's description in the pinned
// contract: a description with one is dropped. qory has no copy of the login line that the
// program prints (SubmitLoginLine): it prints the program's standard error as the program wrote it.
func TestTheMarkerLinesAreThePinnedOnes(t *testing.T) {
	for _, marker := range []string{contracts.SubmitMarkerStart, contracts.SubmitMarkerEnd} {
		if got := markerLineAt("text\n" + marker + "\n"); got != 2 {
			t.Errorf("a description with the line %q has the marker at line %d, want 2", marker, got)
		}
	}
}

// fmtPrefix is the text of a line format up to its first verb.
func fmtPrefix(format string) string {
	for i := range format {
		if format[i] == '%' {
			return format[:i]
		}
	}
	return format
}
