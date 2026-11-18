package cmd

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTheTextsOfTheSubmitStepHaveAShortLineOne is each text of the hand-off file, the submit
// program and the push step's reads that qory prints: line 1 has at most 100 characters with the
// prefix "qory run: ". The texts of the push step's reads and of the local submit step start with
// the fix, as an instruction. The other texts state the cause and start with no verb of an
// instruction.
func TestTheTextsOfTheSubmitStepHaveAShortLineOne(t *testing.T) {
	fixFirst := map[string]string{
		"textPushNoReadProxy":   "Take ",
		"textPushChecksMissing": "Push ",
		"textLocalNoCommits":    "Open ",
		"textPushRefused":       "Fix ",
		"textLocalPushFailed":   "Fix ",
		"textLocalNoHistory":    "Fix ",
		"textLocalNoRemote":     "Set ",
		"textLocalPushURLs":     "Remove ",
	}
	texts := map[string]string{
		"textHandoffIgnored":    textHandoffIgnored,
		"textHandoffDropped":    textHandoffDropped,
		"textProgramFailed":     textProgramFailed,
		"textProgramTimeout":    textProgramTimeout,
		"textProgramNoStart":    textProgramNoStart,
		"textProgramTooMany":    textProgramTooMany,
		"textProgramNoDraft":    textProgramNoDraft,
		"textProgramLeftOut":    textProgramLeftOut,
		"textProgramNoFolder":   textProgramNoFolder,
		"textPushNoReadProxy":   "qory run: " + textPushNoReadProxy,
		"textPushChecksMissing": textPushChecksMissing,
		"textPushRefused":       textPushRefused,
		"textLocalPushFailed":   textLocalPushFailed,
		"textLocalNoHistory":    textLocalNoHistory,
		"textLocalNoCommits":    textLocalNoCommits,
		"textLocalNoRemote":     textLocalNoRemote,
		"textLocalPushURLs":     textLocalPushURLs,
	}
	for name, text := range texts {
		line, _, _ := strings.Cut(text, "\n")
		if n := utf8.RuneCountInString(line); n > 100 {
			t.Errorf("%s: line 1 has %d characters, want at most 100: %q", name, n, line)
		}
		first := strings.TrimPrefix(line, "qory run: ")
		if fix, ok := fixFirst[name]; ok {
			if !strings.HasPrefix(first, fix) {
				t.Errorf("%s: line 1 is %q, want the fix first, starting with %q", name, line, fix)
			}
			continue
		}
		for _, advice := range []string{"Check ", "Read ", "Run ", "Shorten ", "Set ", "Let ", "Take "} {
			if strings.HasPrefix(first, advice) {
				t.Errorf("%s: line 1 starts with the advice %q", name, advice)
			}
		}
	}
}

// TestADroppedStepsAreListedByCommitUpToAMaximum is seven steps over the limit: five get a
// line with their commit, in the order of the commits, and one line counts the other two.
func TestADroppedStepsAreListedByCommitUpToAMaximum(t *testing.T) {
	h := &handoff{Steps: map[string]string{}}
	for i := 1; i <= 7; i++ {
		h.Steps[fmt.Sprintf("%040d", i)] = strings.Repeat("s", maxStep+i)
	}
	h.limit()
	if len(h.Steps) != 0 || len(h.dropped) != maxDroppedSteps+1 {
		t.Fatalf("%d steps are left and %d lines, want 0 and %d: %v", len(h.Steps), len(h.dropped), maxDroppedSteps+1, h.dropped)
	}
	if want := fmt.Sprintf(textDroppedStep, fmt.Sprintf("%040d", 1), maxStep+1, maxStep); h.dropped[0] != want {
		t.Errorf("the first line is %q, want %q", h.dropped[0], want)
	}
	if want := fmt.Sprintf(textDroppedFurther, 2, maxStep); h.dropped[maxDroppedSteps] != want {
		t.Errorf("the last line is %q, want %q", h.dropped[maxDroppedSteps], want)
	}
}
