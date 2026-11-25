package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// startTarget is a repository target of the documents, and startName the name of its
// artifact of a delivered type.
var (
	startTarget = &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "git.example.com", Path: "acme/shop"}}
	startName   = outputName(exports.ProcessType{Send: "after_gate"}, startTarget)
)

// branchIndex is an index with the versions of the artifact pull-request of [startTarget]'s
// collection, each given as its id and the stage that wrote it, numbered from 1.
func branchIndex(versions ...[2]string) *runapi.Index {
	artifact := runapi.Artifact{Type: "pull-request", Name: startName, NewestVersion: len(versions)}

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

// approvedAs sets the approved version of the artifact of [startTarget] in index to id, and
// returns index.
func approvedAs(index *runapi.Index, id string) *runapi.Index {
	for i := range index.Artifacts {
		if index.Artifacts[i].Name == startName {
			index.Artifacts[i].ApprovedVersion = &id
		}
	}

	return index
}

// TestStartVersion is each rule of the version a run starts at: the newest version of the
// target's artifact that the work item's stage wrote and that is not cancelled, else the
// approved version, else none. A version before a withdrawal that is not cancelled counts for
// neither.
func TestStartVersion(t *testing.T) {
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
		// A version that a member cancelled is skipped wherever it is. A run that the time
		// limit stopped is no member's cancel: the index marks only a member's cancel, so
		// its version has no mark and counts.
		"a cancelled version between two is skipped and the newest is taken": {
			stageSnapshot(),
			cancelledAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"}), "av_2"),
			"av_3",
		},
		"two cancelled newest versions leave the oldest": {
			stageSnapshot(),
			cancelledAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"}), "av_2", "av_3"),
			"av_1",
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
		"a withdrawn artifact of another name leaves the approved version of the target's": {
			stageSnapshot(),
			withdrawnAt(&runapi.Index{Artifacts: []runapi.Artifact{
				approvedAs(branchIndex([2]string{"av_1", "st_a"}), "av_1").Artifacts[0],
				{Type: "pull-request", Name: "other", Versions: []runapi.Version{{ID: "av_9", Number: 1}, {ID: "av_10", Number: 2}}},
			}}, "av_10"),
			"av_1",
		},
		"no version":  {stageSnapshot(), branchIndex(), ""},
		"no artifact": {stageSnapshot(), &runapi.Index{}, ""},
		"another artifact name": {
			stageSnapshot(),
			&runapi.Index{Artifacts: []runapi.Artifact{{Type: "pull-request", Name: "other", Versions: []runapi.Version{{ID: "av_1", Number: 1}}}}},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, artifact, _ := startVersion(c.snapshot, c.index, startTarget)
			if got == nil && artifact != "" {
				t.Fatalf("the approved version of %s is not in the index", artifact)
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

// TestStartVersionFindsNoApprovedVersionOutsideTheIndex is an approved version that the index
// does not have: no version, with the artifact's type for the stop of the run.
func TestStartVersionFindsNoApprovedVersionOutsideTheIndex(t *testing.T) {
	got, artifact, _ := startVersion(stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_a"}), "av_9"), startTarget)
	if got != nil || artifact != "pull-request" {
		t.Fatalf("got %+v of %q, want none of pull-request", got, artifact)
	}
}

// TestStartVersionReportsTheApproval is the third result of [startVersion]: true exactly when
// the version is the one that the approval lists, so the run stays at its commit. A version that
// the stage wrote is false, with an approval or not, even when another stage wrote a newer
// version. The approved version is taken only when no type has a version of the stage. Of two
// types, a version of the stage wins over the approved one whatever their times.
func TestStartVersionReportsTheApproval(t *testing.T) {
	// twoTypes is two artifacts of [startTarget]'s collection: the pull-request one has av_1 of
	// st_a, approved, at pullRequestAt, and the notes one has av_2 of st_b at notesAt.
	twoTypes := func(pullRequestAt, notesAt string) *runapi.Index {
		a, b, approved := "st_a", "st_b", "av_1"
		return &runapi.Index{Artifacts: []runapi.Artifact{
			{Type: "pull-request", Name: startName, NewestVersion: 1, ApprovedVersion: &approved, Versions: []runapi.Version{{ID: "av_1", Number: 1, StageID: &a, At: pullRequestAt}}},
			{Type: "notes", Name: startName, NewestVersion: 1, Versions: []runapi.Version{{ID: "av_2", Number: 1, StageID: &b, At: notesAt}}},
		}}
	}
	for name, c := range map[string]struct {
		snapshot *runapi.Snapshot
		index    *runapi.Index
		want     string
		approved bool
	}{
		"the stage's newest, with no approval": {
			stageSnapshot(), branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}), "av_2", false,
		},
		"the stage's newest, with an approval": {
			stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}), "av_1"), "av_2", false,
		},
		"a stage that wrote nothing takes the approved version": {
			stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}), "av_1"), "av_1", true,
		},
		"a work item in no stage takes the approved version": {
			&runapi.Snapshot{}, approvedAs(branchIndex([2]string{"av_1", "st_a"}), "av_1"), "av_1", true,
		},
		"every version of the stage cancelled leaves the approved version": {
			stageSnapshot(), approvedAs(cancelledAt(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_b"}), "av_2"), "av_1"), "av_1", true,
		},
		"a newer version of another stage leaves the stage's newest, approved or not": {
			stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_a"}), "av_1"), "av_1", false,
		},
		"a newer version of another stage, with no approval, leaves the stage's newest": {
			stageSnapshot(), branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_a"}), "av_1", false,
		},
		"no version": {stageSnapshot(), branchIndex([2]string{"av_1", "st_a"}), "", false},
		"of two types, the stage's later version wins over the approved one": {
			stageSnapshot(), twoTypes("2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z"), "av_2", false,
		},
		"of two types, the stage's earlier version wins over a later approved one": {
			stageSnapshot(), twoTypes("2026-11-02T10:00:00Z", "2026-11-02T09:00:00Z"), "av_2", false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, approved := startVersion(c.snapshot, c.index, startTarget)
			if (got == nil) != (c.want == "") || (got != nil && got.ID != c.want) || approved != c.approved {
				t.Errorf("got %+v, approved %v; want %q, approved %v", got, approved, c.want, c.approved)
			}
		})
	}
}

// TestStartVersionCountsNoVersionBeforeAWithdrawal is av_1 of the stage, av_2 its withdrawal and
// av_3 of another stage: av_1 counts for no start, and the run starts at the approved version av_3,
// or at none when no version is approved. When a member cancelled av_2, av_1 counts again. An
// approved version before the withdrawal counts for no start either.
func TestStartVersionCountsNoVersionBeforeAWithdrawal(t *testing.T) {
	versions := func() *runapi.Index {
		return withdrawnAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_a"}), "av_2")
	}
	for name, c := range map[string]struct {
		index    *runapi.Index
		want     string
		approved bool
	}{
		"the approved version after the withdrawal":                         {approvedAs(versions(), "av_3"), "av_3", true},
		"no approved version":                                               {versions(), "", false},
		"an approved version before the withdrawal":                         {approvedAs(versions(), "av_1"), "", false},
		"an approved version that the index lacks, in a withdrawn artifact": {approvedAs(withdrawnAt(branchIndex([2]string{"av_1", "st_a"}, [2]string{"av_2", "st_a"}), "av_2"), "av_9"), "", false},
		"a cancelled withdrawal leaves the stage's version":                 {cancelledAt(approvedAs(versions(), "av_3"), "av_2"), "av_1", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, artifact, approved := startVersion(stageSnapshot(), c.index, startTarget)
			if (got == nil) != (c.want == "") || (got != nil && got.ID != c.want) || approved != c.approved || artifact != "" && got == nil {
				t.Errorf("got %+v of %q, approved %v; want %q, approved %v", got, artifact, approved, c.want, c.approved)
			}
		})
	}
}

// TestStartVersionWithTwoTypesAndApprovals is two artifacts of [startTarget]'s collection, each with
// one version and an approval: a version of the stage in either type wins over an approved
// version that the index lacks (av_9) and over the other type's approved version, whatever their
// times. With no version of the stage, the approved version that the index lacks stops the run
// with the type that lacks it, also when the other type has its approved version, and of two
// approved versions the later wins, in either order of the types.
func TestStartVersionWithTwoTypesAndApprovals(t *testing.T) {
	// part is the one version of a type: id, the stage that wrote it, its time, and the approved
	// version of the type, "" for none.
	type part struct{ id, stage, at, approved string }
	index := func(first, second part) *runapi.Index {
		var artifacts []runapi.Artifact
		for i, p := range []part{first, second} {
			stage := p.stage
			artifact := runapi.Artifact{Type: []string{"pull-request", "notes"}[i], Name: startName, NewestVersion: 1,
				Versions: []runapi.Version{{ID: p.id, Number: 1, StageID: &stage, At: p.at}}}
			if p.approved != "" {
				approved := p.approved
				artifact.ApprovedVersion = &approved
			}
			artifacts = append(artifacts, artifact)
		}
		return &runapi.Index{Artifacts: artifacts}
	}
	const early, late = "2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z"
	for name, c := range map[string]struct {
		index    *runapi.Index
		want     string
		artifact string
		approved bool
	}{
		"a version of the stage in the second type wins over a lost approved version": {
			index(part{"av_1", "st_a", late, "av_9"}, part{"av_2", "st_b", early, ""}), "av_2", "notes", false,
		},
		"a version of the stage in the type with the lost approved version wins": {
			index(part{"av_1", "st_b", early, "av_9"}, part{"av_2", "st_a", late, "av_2"}), "av_1", "pull-request", false,
		},
		"a version of the stage wins over the other type's later approved version": {
			index(part{"av_1", "st_a", late, "av_1"}, part{"av_2", "st_b", early, ""}), "av_2", "notes", false,
		},
		"a lost approved version of the second type stops the run": {
			index(part{"av_1", "st_a", late, "av_1"}, part{"av_2", "st_a", early, "av_9"}), "", "notes", false,
		},
		"a lost approved version of the first type stops the run": {
			index(part{"av_1", "st_a", early, "av_9"}, part{"av_2", "st_a", late, "av_2"}), "", "pull-request", false,
		},
		"the later of two approved versions wins, the first type": {
			index(part{"av_1", "st_a", late, "av_1"}, part{"av_2", "st_a", early, "av_2"}), "av_1", "pull-request", true,
		},
		"the later of two approved versions wins, the second type": {
			index(part{"av_1", "st_a", early, "av_1"}, part{"av_2", "st_a", late, "av_2"}), "av_2", "notes", true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, artifact, approved := startVersion(stageSnapshot(), c.index, startTarget)
			switch {
			case c.want == "" && (got != nil || artifact != c.artifact):
				t.Errorf("got %+v of %q, want none of %q", got, artifact, c.artifact)
			case c.want != "" && (got == nil || got.ID != c.want || artifact != c.artifact || approved != c.approved):
				t.Errorf("got %+v of %q, approved %v; want %s of %q, approved %v", got, artifact, approved, c.want, c.artifact, c.approved)
			}
		})
	}
}

// TestAWithdrawalOfOneTypeAppliesToNoOtherType is two artifacts of [startTarget]'s collection,
// the pull-request one with av_1 of the stage and av_2 its withdrawal, and the notes one with a
// version numbered 1, av_3: the withdrawal leaves no version of the pull-request type, and av_3 counts,
// as a version of the stage and as the approved version.
func TestAWithdrawalOfOneTypeAppliesToNoOtherType(t *testing.T) {
	for name, c := range map[string]struct {
		stage    string
		approved bool
	}{
		"a version of the stage": {"st_b", false},
		"the approved version":   {"st_a", true},
	} {
		t.Run(name, func(t *testing.T) {
			pullRequest := withdrawnAt(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}), "av_2").Artifacts[0]
			stage := c.stage
			notes := runapi.Artifact{Type: "notes", Name: startName, NewestVersion: 1, Versions: []runapi.Version{{ID: "av_3", Number: 1, StageID: &stage}}}
			if c.approved {
				approved := "av_3"
				notes.ApprovedVersion = &approved
			}
			index := &runapi.Index{Artifacts: []runapi.Artifact{pullRequest, notes}}
			got, artifact, approved := startVersion(stageSnapshot(), index, startTarget)
			if got == nil || got.ID != "av_3" || artifact != "notes" || approved != c.approved {
				t.Errorf("got %+v of %q, approved %v; want av_3 of notes, approved %v", got, artifact, approved, c.approved)
			}
		})
	}
}

// TestStartVersionTakesTheLatestOfTwoTypes is two delivered types of the target's collection,
// each with a version of the stage: the version with the later time wins, with its type. A
// target with no address has no start.
func TestStartVersionTakesTheLatestOfTwoTypes(t *testing.T) {
	stage := "st_b"
	index := &runapi.Index{Artifacts: []runapi.Artifact{
		{Type: "pull-request", Name: startName, Versions: []runapi.Version{{ID: "av_1", Number: 1, StageID: &stage, At: "2026-11-02T09:00:00Z"}}},
		{Type: "notes", Name: startName, Versions: []runapi.Version{{ID: "av_2", Number: 1, StageID: &stage, At: "2026-11-02T10:00:00Z"}}},
	}}
	if got, artifact, _ := startVersion(stageSnapshot(), index, startTarget); got == nil || got.ID != "av_2" || artifact != "notes" {
		t.Errorf("got %+v of %q, want av_2 of notes", got, artifact)
	}
	if got, _, _ := startVersion(stageSnapshot(), index, &contracts.Target{Collection: "R_1001"}); got != nil {
		t.Errorf("a target with no address starts at %s", got.ID)
	}
}

// TestStartCommitIsTheNewestConfirmedCommitOfThePickedVersionsRun is the version and the commit
// that a run starts at, from [startVersion] and then [startCommit] as [codeRun.readBranch]
// calls them. Run 1 pushed A, run 2 pushed B and a member cancelled it, run 3 pushed C and then
// D, and its version confirms C only. The version of run 3 has no cancelled mark, as for a run
// that the time limit stopped. The start is the newest commit that the run of the picked version
// pushed and that a version of that run confirms: C with the version of run 3, A when a member
// cancelled it, and never D, B or the newest commit of another run. skipped is the entry of D
// alone, so no entry of another run is. A stage that wrote nothing, as Verify, starts at the
// version that the approval lists, whose run 4 pushed E and then F, and confirmed E only,
// though the index has a newer version of run 5, which pushed G.
func TestStartCommitIsTheNewestConfirmedCommitOfThePickedVersionsRun(t *testing.T) {
	commit := func(c string) string { return strings.Repeat(c, 40) }
	confirms := map[string]string{"av_1": commit("a"), "av_2": commit("b"), "av_3": commit("c"), "av_4": commit("e"), "av_5": commit("g")}
	file := func(id string) string {
		return `{"target":{"collection":"R_1001"},"head":{"collection":"R_1001","address":{"system":"git.example.com","path":"` + confirms[id] + `"}}}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for id := range confirms {
			if strings.HasPrefix(r.URL.Path, "/v1/runs/r/artifacts/"+id+"/files/") {
				io.WriteString(w, file(id))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	// withRuns sets the writer and the file of each version of index: av_N is by run_N.
	withRuns := func(index *runapi.Index) *runapi.Index {
		versions := index.Artifacts[0].Versions
		for i := range versions {
			id := versions[i].ID
			sum := sha256.Sum256([]byte(file(id)))
			versions[i].Writer = runapi.Writer{Kind: "run", RunID: "run_" + strings.TrimPrefix(id, "av_")}
			versions[i].Files = []runapi.File{{Filename: "f.json", Size: int64(len(file(id))), SHA256: hex.EncodeToString(sum[:])}}
		}

		return index
	}
	entry := func(run, c string, cancelled bool) runapi.PushedEntry {
		return runapi.PushedEntry{System: "git.example.com", Path: "acme/shop", Commit: commit(c), RunID: run, Cancelled: cancelled}
	}
	pushed := []runapi.PushedEntry{
		entry("run_1", "a", false), entry("run_2", "b", true), entry("run_3", "c", false), entry("run_3", "d", false),
		entry("run_4", "e", false), entry("run_4", "f", false), entry("run_5", "g", false),
	}
	build := func() *runapi.Index {
		return branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_b"}, [2]string{"av_3", "st_b"})
	}
	verify := stageSnapshot()
	*verify.Stage.ID = "st_c"

	for name, c := range map[string]struct {
		snapshot *runapi.Snapshot
		index    *runapi.Index
		version  string
		start    string
		skipped  []int
	}{
		"a run at its time limit counts, its unconfirmed push does not": {
			stageSnapshot(), withRuns(cancelledAt(build(), "av_2")), "av_3", commit("c"), []int{3},
		},
		"a cancelled newest version leaves the version before": {
			stageSnapshot(), withRuns(cancelledAt(build(), "av_2", "av_3")), "av_1", commit("a"), nil,
		},
		"a stage that wrote nothing takes the approved version's confirmed commit": {
			verify, withRuns(approvedAs(branchIndex([2]string{"av_4", "st_b"}, [2]string{"av_5", "st_b"}), "av_4")), "av_4", commit("e"), []int{5},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if len(c.snapshot.Outputs) != 0 {
				t.Fatalf("the snapshot lists outputs %v", c.snapshot.Outputs)
			}
			version, typ, _ := startVersion(c.snapshot, c.index, startTarget)
			if version == nil || version.ID != c.version {
				t.Fatalf("the version is %+v, want %s", version, c.version)
			}
			got, skipped, err := startCommit(t.Context(), client, version, typ, sameRunVersions(c.index, startTarget, version), pushed, startTarget)
			if err != nil || got != c.start || !slices.Equal(skipped, c.skipped) {
				t.Errorf("start %q, skipped %v, %v; want %s and %v", got, skipped, err, c.start, c.skipped)
			}
		})
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

// TestPushedStartsKeepsEachCommitOfARunNobodyCancelled is a record with two commits of the
// repository acme/shop by runs that nobody cancelled, one of a cancelled run, and one of
// acme/lib: Pushed has the three commits of the runs that nobody cancelled, in the order of the
// record, so the push check sees each one, and Cancelled has the fourth.
func TestPushedStartsKeepsEachCommitOfARunNobodyCancelled(t *testing.T) {
	commit := func(c string) string { return strings.Repeat(c, 40) }
	entry := func(path, c string, cancelled bool) runapi.PushedEntry {
		return runapi.PushedEntry{System: "git.example.com", Path: path, Commit: commit(c), Cancelled: cancelled}
	}
	branch := &preparation.WorkBranch{}
	err := pushedStarts(branch, []runapi.PushedEntry{entry("acme/shop", "a", false), entry("acme/lib", "b", false), entry("acme/shop", "c", true), entry("acme/shop", "d", false)})
	var got []string
	for _, l := range branch.Pushed {
		got = append(got, l.Path+" "+l.Commit[:1])
	}
	if err != nil || !slices.Equal(got, []string{"acme/shop a", "acme/lib b", "acme/shop d"}) || len(branch.Cancelled) != 1 || branch.Cancelled[0].Commit != commit("c") {
		t.Errorf("err %v, pushed %v, cancelled %+v", err, got, branch.Cancelled)
	}
}

// TestStartCommitIsConfirmedByAnotherVersionOfTheSameRun is a run that stored notes, the
// newest version, with no reference to its commit, and pull-request with one: the start is
// confirmed by pull-request. With no other version, notes alone stops with 0 references.
func TestStartCommitIsConfirmedByAnotherVersionOfTheSameRun(t *testing.T) {
	commit := strings.Repeat("c", 40)
	files := map[string]string{
		"av_notes": `{"text":"No reference."}`,
		"av_pr":    `{"target":{"collection":"R_1001"},"head":{"collection":"R_1001","address":{"system":"git.example.com","path":"` + commit + `"}}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for id, content := range files {
			if strings.HasPrefix(r.URL.Path, "/v1/runs/r/artifacts/"+id+"/files/") {
				io.WriteString(w, content)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	version := func(id, at string) runapi.Version {
		sum := sha256.Sum256([]byte(files[id]))
		return runapi.Version{ID: id, Number: 1, At: at, Writer: runapi.Writer{Kind: "run", RunID: "run_1"},
			Files: []runapi.File{{Filename: "f.json", Size: int64(len(files[id])), SHA256: hex.EncodeToString(sum[:])}}}
	}
	index := &runapi.Index{Artifacts: []runapi.Artifact{
		{Type: "notes", Name: startName, Versions: []runapi.Version{version("av_notes", "2026-11-02T10:00:00.5Z")}},
		{Type: "pull-request", Name: startName, Versions: []runapi.Version{version("av_pr", "2026-11-02T10:00:00Z")}},
		{Type: "pull-request", Name: "main", Versions: []runapi.Version{version("av_pr", "2026-11-02T09:00:00Z")}},
	}}
	pushed := []runapi.PushedEntry{{System: "git.example.com", Path: "acme/shop", Commit: commit, RunID: "run_1"}}
	chosen := &index.Artifacts[0].Versions[0]
	others := sameRunVersions(index, startTarget, chosen)
	if len(others) != 1 || others[0].version.ID != "av_pr" {
		t.Fatalf("the other versions are %+v, want av_pr alone", others)
	}
	if got, _, err := startCommit(t.Context(), client, chosen, "notes", others, pushed, startTarget); err != nil || got != commit {
		t.Errorf("start %q, %v, want %s", got, err, commit)
	}
	if _, _, err := startCommit(t.Context(), client, chosen, "notes", nil, pushed, startTarget); err == nil || !strings.Contains(err.Error(), "has 0 top-level references") {
		t.Errorf("notes alone: %v", err)
	}
}

// TestAVersionBeforeAWithdrawalConfirmsNoCommit is one run that stored pull-request av_1, which
// confirms its first commit A, and notes av_2, which confirms its second commit B, and then a
// withdrawal of notes, av_3: av_2 has a later withdrawal of its artifact, so it confirms
// nothing, and the start is A with B skipped.
func TestAVersionBeforeAWithdrawalConfirmsNoCommit(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	file := func(commit string) string {
		return `{"target":{"collection":"R_1001"},"head":{"collection":"R_1001","address":{"system":"git.example.com","path":"` + commit + `"}}}`
	}
	files := map[string]string{"av_1": file(a), "av_2": file(b)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for id, content := range files {
			if strings.HasPrefix(r.URL.Path, "/v1/runs/r/artifacts/"+id+"/files/") {
				io.WriteString(w, content)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	version := func(id string, number int) runapi.Version {
		v := runapi.Version{ID: id, Number: number, Writer: runapi.Writer{Kind: "run", RunID: "run_1"}}
		if content, ok := files[id]; ok {
			sum := sha256.Sum256([]byte(content))
			v.Files = []runapi.File{{Filename: "f.json", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}}
		}
		return v
	}
	index := &runapi.Index{Artifacts: []runapi.Artifact{
		{Type: "pull-request", Name: startName, Versions: []runapi.Version{version("av_1", 1)}},
		{Type: "notes", Name: startName, Versions: []runapi.Version{version("av_2", 1), version("av_3", 2)}},
	}}
	index.Artifacts[1].Versions[1].Withdrawn = true
	pushed := []runapi.PushedEntry{
		{System: "git.example.com", Path: "acme/shop", Commit: a, RunID: "run_1"},
		{System: "git.example.com", Path: "acme/shop", Commit: b, RunID: "run_1"},
	}
	chosen := &index.Artifacts[0].Versions[0]
	others := sameRunVersions(index, startTarget, chosen)
	if len(others) != 0 {
		t.Fatalf("the other versions are %+v, want none", others)
	}
	if got, skipped, err := startCommit(t.Context(), client, chosen, "pull-request", others, pushed, startTarget); err != nil || got != a || !slices.Equal(skipped, []int{1}) {
		t.Errorf("start %q, skipped %v, %v; want %s and [1]", got, skipped, err, a)
	}
}

// TestSameRunVersionsCountsWithdrawalsPerArtifact is the versions of one run in two artifacts of
// [startTarget]'s collection: a withdrawal that a member cancelled leaves the versions before it,
// and a withdrawal of one artifact leaves the versions of the other.
func TestSameRunVersionsCountsWithdrawalsPerArtifact(t *testing.T) {
	version := func(id string, number int, withdrawn, cancelled bool) runapi.Version {
		return runapi.Version{ID: id, Number: number, Withdrawn: withdrawn, Cancelled: cancelled, Writer: runapi.Writer{Kind: "run", RunID: "run_1"}}
	}
	for name, c := range map[string]struct {
		pullRequest, notes []runapi.Version
		want               []string
	}{
		"a cancelled withdrawal leaves the version before it": {
			[]runapi.Version{version("av_1", 1, false, false)},
			[]runapi.Version{version("av_2", 1, false, false), version("av_3", 2, true, true)},
			[]string{"av_2"},
		},
		"a withdrawal of the other artifact leaves the version": {
			[]runapi.Version{version("av_1", 1, false, false), version("av_4", 3, true, false)},
			[]runapi.Version{version("av_2", 1, false, false)},
			[]string{"av_2"},
		},
		"a withdrawal that counts leaves no version before it": {
			[]runapi.Version{version("av_1", 1, false, false)},
			[]runapi.Version{version("av_2", 1, false, false), version("av_3", 2, true, false)},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			index := &runapi.Index{Artifacts: []runapi.Artifact{
				{Type: "pull-request", Name: startName, Versions: c.pullRequest},
				{Type: "notes", Name: startName, Versions: c.notes},
			}}
			var got []string
			for _, o := range sameRunVersions(index, startTarget, &index.Artifacts[0].Versions[0]) {
				got = append(got, o.version.ID)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("the other versions are %v, want %v", got, c.want)
			}
		})
	}
}

// TestStartCommitWalksTheRunsCommitsNewestFirst is a run that pushed A, B and C to the
// target, with an entry of acme/lib and one of another run between them, and stored one
// version: with the head A, B and C are skipped and A is the start; with the head B, C is
// skipped; with the head C, nothing is. A version that confirms none stops with the newest
// commit.
func TestStartCommitWalksTheRunsCommitsNewestFirst(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	head := ""
	file := func() string {
		return `{"target":{"collection":"R_1001"},"head":{"collection":"R_1001","address":{"system":"git.example.com","path":"` + head + `"}}}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, file())
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	entry := func(path, commit, run string) runapi.PushedEntry {
		return runapi.PushedEntry{System: "git.example.com", Path: path, Commit: commit, RunID: run}
	}
	pushed := []runapi.PushedEntry{
		entry("acme/shop", a, "run_1"), entry("acme/lib", b, "run_1"), entry("acme/shop", b, "run_1"),
		entry("acme/shop", strings.Repeat("e", 40), "run_2"), entry("acme/shop", c, "run_1"),
	}
	version := func() *runapi.Version {
		sum := sha256.Sum256([]byte(file()))
		return &runapi.Version{ID: "av_1", Number: 1, Writer: runapi.Writer{Kind: "run", RunID: "run_1"},
			Files: []runapi.File{{Filename: "f.json", Size: int64(len(file())), SHA256: hex.EncodeToString(sum[:])}}}
	}
	for name, c := range map[string]struct {
		head, want string
		skipped    []int
	}{
		"the oldest commit": {a, a, []int{2, 4}},
		"a middle commit":   {b, b, []int{4}},
		"the newest commit": {c, c, nil},
	} {
		t.Run(name, func(t *testing.T) {
			head = c.head
			got, skipped, err := startCommit(t.Context(), client, version(), "pull-request", nil, pushed, startTarget)
			if err != nil || got != c.want || !slices.Equal(skipped, c.skipped) {
				t.Errorf("start %q, skipped %v, %v; want %s and %v", got, skipped, err, c.want, c.skipped)
			}
		})
	}
	head = strings.Repeat("d", 40)
	if _, _, err := startCommit(t.Context(), client, version(), "pull-request", nil, pushed, startTarget); err == nil ||
		!strings.Contains(err.Error(), "has 0 top-level references to commit "+c[:12]+" of git.example.com/acme/shop, not 1.") {
		t.Errorf("no confirmed commit: %v", err)
	}
}

// TestCompareAtOrdersTimesWithAndWithoutFractions is two times of one second, one with a
// fraction: the fraction is later, which a comparison of the strings gets wrong.
func TestCompareAtOrdersTimesWithAndWithoutFractions(t *testing.T) {
	if compareAt("2026-11-02T10:00:00.5Z", "2026-11-02T10:00:00Z") <= 0 || compareAt("2026-11-02T10:00:00Z", "2026-11-02T10:00:00.5Z") >= 0 {
		t.Error("10:00:00.5 is not after 10:00:00")
	}
	if compareAt("not a time", "2026-11-02T10:00:00Z") >= 0 {
		t.Error("a time that does not parse is not the earliest")
	}
}
