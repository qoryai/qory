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
// approved version unless the newest version is a withdrawal, else none.
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
// the stage wrote is false, with an approval or not. A run whose newest version that is not
// cancelled was not written by its stage has the approved version, so a Build run stays at it
// too. Of two types, the version with the later time wins, and the result is true when it is
// the approved one.
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
		"a newer version of another stage leaves the approved version": {
			stageSnapshot(), approvedAs(branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_a"}), "av_1"), "av_1", true,
		},
		"a newer version of another stage, with no approval, is no start": {
			stageSnapshot(), branchIndex([2]string{"av_1", "st_b"}, [2]string{"av_2", "st_a"}), "", false,
		},
		"no version": {stageSnapshot(), branchIndex([2]string{"av_1", "st_a"}), "", false},
		"of two types, the stage's later version wins over the approved one": {
			stageSnapshot(), twoTypes("2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z"), "av_2", false,
		},
		"of two types, the approved later version wins over the stage's": {
			stageSnapshot(), twoTypes("2026-11-02T10:00:00Z", "2026-11-02T09:00:00Z"), "av_1", true,
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
