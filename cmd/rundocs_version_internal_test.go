package cmd

import (
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
)

// TestARunCodesTargetIsWhatItsFetchTakes is documents with a target and a
// target_version, of any form the schema allows, and documents with neither: the run
// passes the target's system and path and the version to the fetch, which uses the
// version only for the target's repository, and a run not from a run code passes none.
func TestARunCodesTargetIsWhatItsFetchTakes(t *testing.T) {
	settings := &contracts.RunSettings{Runtime: "claude"}
	for _, v := range []string{"0123456789abcdef0123456789abcdef01234567", "2026-10-24.3", ""} {
		r, err := codeRunOf(contracts.Documents{Workflow: "example", Target: &contracts.Target{System: "code.example.com", Path: "acme/shop"}, TargetVersion: v, RunSettings: settings})
		want := preparation.Target{System: "code.example.com", Path: "acme/shop", Version: v}
		if err != nil || r.fetchTarget() != want {
			t.Errorf("%q: %v, the target %+v, want %+v", v, err, r.fetchTarget(), want)
		}
	}
	r, err := codeRunOf(contracts.Documents{Workflow: "example", RunSettings: settings})
	if err != nil || r.fetchTarget() != (preparation.Target{}) {
		t.Errorf("no target: %v, the target %+v", err, r.fetchTarget())
	}
	var none *codeRun
	if none.fetchTarget() != (preparation.Target{}) {
		t.Error("a run not from a run code has a target")
	}
}
