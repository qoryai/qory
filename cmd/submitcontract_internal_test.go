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
		{"system", submitEnvSystem, contracts.SubmitEnvSystem},
		{"path", submitEnvPath, contracts.SubmitEnvPath},
		{"branch", submitEnvBranch, contracts.SubmitEnvBranch},
		{"base branch", submitEnvBaseBranch, contracts.SubmitEnvBaseBranch},
		{"base commit", submitEnvBaseCommit, contracts.SubmitEnvBaseCommit},
		{"commits", submitEnvCommits, contracts.SubmitEnvCommits},
		{"draft", submitEnvDraft, contracts.SubmitEnvDraft},
		{"title", submitEnvTitle, contracts.SubmitEnvTitle},
		{"description file", submitEnvDescriptionFile, contracts.SubmitEnvDescriptionFile},
		{"draft open", submitDraftOpen, contracts.DraftOpen},
		{"draft none", submitDraftNone, contracts.SubmitDraftNone},
		{"draft number", submitKeyNumber, contracts.SubmitKeyDraftNumber},
		{"draft url", submitKeyURL, contracts.SubmitKeyDraftURL},
		{"draft title", submitKeyTitle, contracts.SubmitKeyDraftTitle},
		{"draft state", submitKeyState, contracts.SubmitKeyDraftState},
		{"draft action", submitKeyAction, contracts.SubmitKeyDraftAction},
		{"commit url", submitKeyCommitURL, contracts.SubmitKeyCommitURL},
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
		{"exit done", submitExitDone, contracts.SubmitExitDone},
		{"exit forge", submitExitForge, contracts.SubmitExitForge},
		{"exit input", submitExitInput, contracts.SubmitExitInput},
		{"exit login", submitExitNoLogin, contracts.SubmitExitLogin},
		{"commits", maxSubmitCommits, 100},
		{"title", maxSubmitTitle, 256},
		{"address", maxSubmitURL, 2048},
		{"printed lines", maxProgramLines, contracts.SubmitPrintedLines},
		{"time limit seconds", int(programLimit.Seconds()), int(contracts.SubmitTimeLimit.Seconds())},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d, the pinned contract has %d", c.name, c.got, c.want)
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
