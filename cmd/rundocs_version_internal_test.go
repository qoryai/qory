package cmd

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
)

// TestARunCodesTargetIsWhatItsFetchTakes is documents with a target and a
// target_version, of any form the schema allows, documents whose target has no address,
// and documents with neither: the run passes the system and path of the target's address
// and the version to the fetch, which uses the version only for the target's repository,
// passes none for a target with no address, and a run not from a run code passes none.
func TestARunCodesTargetIsWhatItsFetchTakes(t *testing.T) {
	settings := &contracts.RunSettings{Runtime: "claude"}
	for _, v := range []string{"0123456789abcdef0123456789abcdef01234567", "2026-10-24.3", ""} {
		r, err := codeRunOf(contracts.Documents{Inputs: json.RawMessage(`{}`), Workflow: "example", Target: &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "code.example.com", Path: "acme/shop"}}, TargetVersion: v, RunSettings: settings})
		want := preparation.Target{System: "code.example.com", Path: "acme/shop", Version: v, FromCode: true}
		if err != nil || !reflect.DeepEqual(r.fetchTarget(), want) {
			t.Errorf("%q: %v, the target %+v, want %+v", v, err, r.fetchTarget(), want)
		}
	}
	r, err := codeRunOf(contracts.Documents{Inputs: json.RawMessage(`{}`), Workflow: "example", Target: &contracts.Target{Collection: "profile-7"}, TargetVersion: "2026-10-24.3", RunSettings: settings})
	if err != nil || !reflect.DeepEqual(r.fetchTarget(), preparation.Target{FromCode: true}) {
		t.Errorf("a target with no address: %v, the target %+v", err, r.fetchTarget())
	}
	r, err = codeRunOf(contracts.Documents{Inputs: json.RawMessage(`{}`), Workflow: "example", RunSettings: settings})
	if err != nil || !reflect.DeepEqual(r.fetchTarget(), preparation.Target{FromCode: true}) {
		t.Errorf("no target: %v, the target %+v", err, r.fetchTarget())
	}
	var none *codeRun
	if !reflect.DeepEqual(none.fetchTarget(), preparation.Target{}) {
		t.Error("a run not from a run code has a target")
	}
}

// TestARunCodesReadTargetsAreWhatItsFetchReads is documents with a target and three read
// targets, one of them with no address: the fetch gets the two with an address,
// in their order, and a read run that fetches its target alone for its harness gets none.
func TestARunCodesReadTargetsAreWhatItsFetchReads(t *testing.T) {
	settings := &contracts.RunSettings{Runtime: "claude"}
	on := func(collection, path string) contracts.Target {
		return contracts.Target{Collection: collection, Address: &contracts.TargetAddress{System: "code.example.com", Path: path}}
	}
	doc := contracts.Documents{Inputs: json.RawMessage(`{}`), Workflow: "example", RunSettings: settings, Access: "write",
		Target:      &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "code.example.com", Path: "acme/shop"}},
		ReadTargets: []contracts.Target{on("R_1002", "acme/api"), {Collection: "queue-7"}, on("R_1003", "acme/docs")}}
	r, err := codeRunOf(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := []preparation.Read{{System: "code.example.com", Path: "acme/api"}, {System: "code.example.com", Path: "acme/docs"}}
	if got := r.fetchTarget(); !reflect.DeepEqual(got.Reads, want) || got.Path != "acme/shop" {
		t.Errorf("the fetch gets %+v, want the reads %+v", got, want)
	}
	doc.Access, doc.Inputs = "read", json.RawMessage(`{"destinations": [{"id": "tg_0a1b2c3d4e5f6g7h", "target": {"collection": "R_1001"}}]}`)
	if r, err = codeRunOf(doc); err != nil {
		t.Fatal(err)
	}
	if got := r.fetchTarget(); got.Reads != nil || got.Path != "acme/shop" {
		t.Errorf("a read run that fetches its target alone gets %+v", got)
	}
}
