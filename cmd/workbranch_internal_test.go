package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runapi"
)

// branchIndex is an index with the versions of the artifact branch, name main, each given
// as its id and the stage that wrote it, numbered from 1.
func branchIndex(versions ...[2]string) *runapi.Index {
	artifact := runapi.Artifact{Type: branchType, Name: defaultName, NewestVersion: len(versions)}

	for i, version := range versions {
		stage := version[1]
		artifact.Versions = append(artifact.Versions, runapi.Version{ID: version[0], Number: i + 1, StageID: &stage})
	}

	return &runapi.Index{Artifacts: []runapi.Artifact{artifact}}
}

// stageSnapshot is a snapshot of the stage st_b, with the given inputs.
func stageSnapshot(inputs ...runapi.Input) *runapi.Snapshot {
	stage := "st_b"
	snapshot := &runapi.Snapshot{Inputs: inputs}
	snapshot.Stage.ID = &stage

	return snapshot
}

// TestChosenBranch is each rule of the commit a run starts at.
func TestChosenBranch(t *testing.T) {
	approved := func(id string) runapi.Input {
		return runapi.Input{Type: branchType, Version: &runapi.VersionRef{ID: id}}
	}

	for name, c := range map[string]struct {
		snapshot *runapi.Snapshot
		index    *runapi.Index
		want     string
	}{
		"a second round takes the stage's newest": {
			stageSnapshot(approved("av_1")),
			branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"}),
			"av_3",
		},
		"a first run takes the approved input": {
			stageSnapshot(approved("av_1")),
			branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}),
			"av_1",
		},
		"moved back keeps the later stage's commits": {
			stageSnapshot(),
			branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_c"}),
			"av_2",
		},
		"an input with no approved version takes the newest": {
			stageSnapshot(runapi.Input{Type: branchType}),
			branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}),
			"av_2",
		},
		"another input changes nothing": {
			stageSnapshot(runapi.Input{Type: "mockup", Version: &runapi.VersionRef{ID: "av_1"}}),
			branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}),
			"av_2",
		},
		"a work item in no stage takes the newest": {
			&runapi.Snapshot{},
			branchIndex([2]string{"av_1", "st_a"}),
			"av_1",
		},
		"an approved version with no artifact main": {
			stageSnapshot(approved("av_9")),
			&runapi.Index{Artifacts: []runapi.Artifact{
				{Type: branchType, Name: "other", Versions: []runapi.Version{{ID: "av_9", Number: 1}}},
			}},
			"av_9",
		},
		"an approved version of another artifact name": {
			stageSnapshot(approved("av_9")),
			&runapi.Index{Artifacts: []runapi.Artifact{
				branchIndex([2]string{"av_1", "st_a"}).Artifacts[0],
				{Type: branchType, Name: "other", Versions: []runapi.Version{{ID: "av_9", Number: 1}}},
			}},
			"av_9",
		},
		"no version":  {stageSnapshot(), branchIndex(), ""},
		"no artifact": {stageSnapshot(), &runapi.Index{}, ""},
		"another artifact name": {
			stageSnapshot(),
			&runapi.Index{Artifacts: []runapi.Artifact{{Type: branchType, Name: "other", Versions: []runapi.Version{{ID: "av_1", Number: 1}}}}},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := chosenBranch(c.snapshot, c.index)
			if err != nil {
				t.Fatal(err)
			}

			switch {
			case c.want == "" && got != nil:
				t.Errorf("got %s", got.ID)
			case c.want != "" && (got == nil || got.ID != c.want):
				t.Errorf("got %+v, want %s", got, c.want)
			}
		})
	}
}

// TestChosenBranchStopsOnAnApprovedVersionOutsideTheIndex keeps an approved version from
// becoming the newest one.
func TestChosenBranchStopsOnAnApprovedVersionOutsideTheIndex(t *testing.T) {
	snapshot := stageSnapshot(runapi.Input{Type: branchType, Version: &runapi.VersionRef{ID: "av_9", Number: 9}})

	_, err := chosenBranch(snapshot, branchIndex([2]string{"av_1", "st_a"}))
	if err == nil || !strings.HasPrefix(err.Error(), "Report this run to the instance admin of Qory Apiary.") {
		t.Fatalf("err = %v", err)
	}
}
