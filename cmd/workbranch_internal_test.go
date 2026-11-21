package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// branchIndex is an index with the versions of the artifact submission, name main, each given
// as its id and the stage that wrote it, numbered from 1.
func branchIndex(versions ...[2]string) *runapi.Index {
	artifact := runapi.Artifact{Type: submissionType, Name: defaultName, NewestVersion: len(versions)}

	for i, version := range versions {
		stage := version[1]
		artifact.Versions = append(artifact.Versions, runapi.Version{ID: version[0], Number: i + 1, StageID: &stage})
	}

	return &runapi.Index{Artifacts: []runapi.Artifact{artifact}}
}

// cancelledAt marks the versions of index with the given ids cancelled, and returns index.
func cancelledAt(index *runapi.Index, ids ...string) *runapi.Index {
	for i := range index.Artifacts {
		for j := range index.Artifacts[i].Versions {
			if slices.Contains(ids, index.Artifacts[i].Versions[j].ID) {
				index.Artifacts[i].Versions[j].Cancelled = true
			}
		}
	}

	return index
}

// withdrawnAt marks the versions of index with the given ids withdrawn, and returns index.
func withdrawnAt(index *runapi.Index, ids ...string) *runapi.Index {
	for i := range index.Artifacts {
		for j := range index.Artifacts[i].Versions {
			if slices.Contains(ids, index.Artifacts[i].Versions[j].ID) {
				index.Artifacts[i].Versions[j].Withdrawn = true
			}
		}
	}

	return index
}

// stageSnapshot is a snapshot of the stage st_b.
func stageSnapshot() *runapi.Snapshot {
	stage := "st_b"
	snapshot := &runapi.Snapshot{}
	snapshot.Stage.ID = &stage

	return snapshot
}

// approvedAs sets the approved version of the artifact main of index to id, and returns index.
func approvedAs(index *runapi.Index, id string) *runapi.Index {
	for i := range index.Artifacts {
		if index.Artifacts[i].Name == defaultName {
			index.Artifacts[i].ApprovedVersion = &id
		}
	}

	return index
}

// TestChosenSubmission is each rule of the version a run starts at: the newest version of
// the artifact main that the work item's stage wrote and that is not cancelled, else the
// approved version unless the newest version is a withdrawal, else none.
func TestChosenSubmission(t *testing.T) {
	for name, c := range map[string]struct {
		snapshot *runapi.Snapshot
		index    *runapi.Index
		want     string
	}{
		"a second round takes the stage's newest": {
			stageSnapshot(),
			approvedAs(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"}), "av_1"),
			"av_3",
		},
		"a first run takes the approved version": {
			stageSnapshot(),
			approvedAs(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}), "av_1"),
			"av_1",
		},
		"another stage's newest is no start with no approval": {
			stageSnapshot(),
			branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}),
			"",
		},
		"a work item in no stage takes the approved version": {
			&runapi.Snapshot{},
			approvedAs(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}), "av_1"),
			"av_1",
		},
		"a work item in no stage and no approval has no start": {
			&runapi.Snapshot{},
			branchIndex([2]string{"av_1", "st_a"}),
			"",
		},
		"a cancelled newest version is skipped": {
			stageSnapshot(),
			cancelledAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"}), "av_3"),
			"av_2",
		},
		"a cancelled version of the stage leaves the approved version": {
			stageSnapshot(),
			approvedAs(cancelledAt(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}), "av_2"), "av_1"),
			"av_1",
		},
		"every version cancelled is no version": {
			stageSnapshot(),
			cancelledAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}), "av_1", "av_2"),
			"",
		},
		"every version cancelled leaves the approved version": {
			stageSnapshot(),
			approvedAs(cancelledAt(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}), "av_1", "av_2"), "av_1"),
			"av_1",
		},
		"a withdrawn newest version is no version": {
			stageSnapshot(),
			withdrawnAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}), "av_2"),
			"",
		},
		"a withdrawn artifact has no start, approved or not": {
			stageSnapshot(),
			approvedAs(withdrawnAt(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}), "av_2"), "av_1"),
			"",
		},
		"a withdrawn artifact of another name leaves the approved version of main": {
			stageSnapshot(),
			withdrawnAt(&runapi.Index{Artifacts: []runapi.Artifact{
				approvedAs(branchIndex([2]string{"av_1", "st_a"}), "av_1").Artifacts[0],
				{Type: submissionType, Name: "other", Versions: []runapi.Version{{ID: "av_9", Number: 1}, {ID: "av_10", Number: 2}}},
			}}, "av_10"),
			"av_1",
		},
		"no version":  {stageSnapshot(), branchIndex(), ""},
		"no artifact": {stageSnapshot(), &runapi.Index{}, ""},
		"another artifact name": {
			stageSnapshot(),
			&runapi.Index{Artifacts: []runapi.Artifact{{Type: submissionType, Name: "other", Versions: []runapi.Version{{ID: "av_1", Number: 1}}}}},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := chosenSubmission(c.snapshot, c.index)
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

// TestChosenSubmissionStopsOnAnApprovedVersionOutsideTheIndex keeps an approved version from
// becoming the newest one.
func TestChosenSubmissionStopsOnAnApprovedVersionOutsideTheIndex(t *testing.T) {
	_, err := chosenSubmission(stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_a"}), "av_9"))
	if err == nil || !strings.HasPrefix(err.Error(), "Report this run to the instance admin of Qory Apiary.") {
		t.Fatalf("err = %v", err)
	}
}

// TestPushedStartsChecksEachCommit is a record of pushed commits with a ref name, an option, a
// short id and a good id: the read ends before git gets any value that is not a full commit id,
// and a good record sets the branch.
func TestPushedStartsChecksEachCommit(t *testing.T) {
	good := strings.Repeat("a", 40)

	for _, commit := range []string{"refs/heads/main", "--upload-pack=x", "abc1234", strings.ToUpper(good)} {
		branch := &preparation.WorkBranch{}
		entries := []runapi.PushedEntry{{System: "git.example.com", Path: "acme/shop", Commit: good}, {System: "git.example.com", Path: "acme/lib", Commit: commit, Cancelled: true}}

		err := pushedStarts(branch, entries)
		if err == nil || !strings.HasPrefix(err.Error(), "Report this run to the instance admin of Qory Apiary.\n") || !strings.Contains(err.Error(), commit) {
			t.Errorf("commit %q: err = %v", commit, err)
		}
	}

	branch := &preparation.WorkBranch{}
	if err := pushedStarts(branch, []runapi.PushedEntry{{System: "git.example.com", Path: "acme/shop", Commit: good}}); err != nil || len(branch.Pushed) != 1 || branch.Pushed[0].Commit != good {
		t.Errorf("good id: err = %v, pushed = %v", err, branch.Pushed)
	}
}
