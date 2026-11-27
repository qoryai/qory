package cmd

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
)

// prVersion is a version of an artifact of the fake run API of [pullRequestClient], with
// one file that has content. writer is the kind of the writer, "run" or "person", and run
// the id of the run of a run's version.
type prVersion struct {
	id                   string
	typ                  string // the artifact type, pull-request when empty
	writer, run          string
	cancelled, withdrawn bool
	content              string
	// unreadable makes the download of the version's file fail.
	unreadable bool
}

// prFile is the file of a version whose head and base are the commits.
func prFile(head, base string) string {
	ref := func(commit string) string {
		return fmt.Sprintf(`{"collection":"R_1001","address":{"system":"forge","path":%q}}`, commit)
	}
	return `{"title":"A change","head":` + ref(head) + `,"base":` + ref(base) + `}`
}

// pullRequestClient is a run API client whose index lists versions, in the order of their
// numbers and times, as artifacts that have the name of a delivered type's artifact for
// the target, one for each type of the versions, each written in the stage st_b, and whose
// file downloads return the content of the version.
func pullRequestClient(t *testing.T, versions ...prVersion) *runapi.Client {
	t.Helper()
	var types []string
	lists := map[string][]string{}
	content := map[string]string{}
	unreadable := map[string]bool{}
	for i, v := range versions {
		unreadable[v.id] = v.unreadable
		sum := sha256.Sum256([]byte(v.content))
		who := fmt.Sprintf(`{"kind":%q}`, v.writer)
		if v.run != "" {
			who = fmt.Sprintf(`{"kind":%q,"run_id":%q}`, v.writer, v.run)
		}
		typ := cmp.Or(v.typ, "pull-request")
		if _, ok := lists[typ]; !ok {
			types = append(types, typ)
		}
		lists[typ] = append(lists[typ], fmt.Sprintf(`{"id":%q,"number":%d,"writer":%s,"cancelled":%v,"withdrawn":%v,"stage_id":"st_b","note":null,"at":"2026-11-25T10:00:0%dZ","files":[{"filename":"pull-request.json","content_type":"application/json","size":%d,"sha256":"%s"}],"approvals":[]}`,
			v.id, i+1, who, v.cancelled, v.withdrawn, i, len(v.content), hex.EncodeToString(sum[:])))
		content[v.id] = v.content
	}
	var artifacts []string
	for _, typ := range types {
		artifacts = append(artifacts, `{"type":"`+typ+`","name":"`+startName+`","stage_id":null,"approved_version":null,"newest_version":9,"changed_after_approval":false,"versions":[`+
			strings.Join(lists[typ], ",")+`]}`)
	}
	index := `{"version":1,"artifacts":[` + strings.Join(artifacts, ",") + `]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/runs/r")
		if path == "/artifacts" {
			_, _ = w.Write([]byte(index))
			return
		}
		if id, ok := strings.CutPrefix(path, "/artifacts/"); ok {
			id, _, _ = strings.Cut(id, "/")
			if unreadable[id] {
				http.Error(w, "unreadable", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(content[id]))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// pushedBy is an entry of the work item's pushed commits: commit pushed by run to the
// repository acme/shop on forge.
func pushedBy(run, commit string) runapi.PushedEntry {
	return runapi.PushedEntry{System: "forge", Path: "acme/shop", Commit: commit, RunID: run}
}

// TestPushedBaseOfSkipsAPersonsVersionAndACancelledOrWithdrawnOne: of the inputs of the
// stage, a version that a person wrote, one whose run has no id, a cancelled one and a
// withdrawn one never give the base. With another version that has the head the base is
// that version's, and with none the run stops with the cause.
func TestPushedBaseOfSkipsAPersonsVersionAndACancelledOrWithdrawnOne(t *testing.T) {
	head, base, other := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	good := prVersion{id: "av_1", writer: "run", run: "run_1", content: prFile(head, base)}
	// elsewhere has the head of the review and another base.
	elsewhere := func(v prVersion) prVersion {
		v.id, v.content = "av_2", prFile(head, other)
		return v
	}
	types := []exports.ProcessType{pullRequestSchemaType(t)}
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	const none = "no input of the stage has a version with a base and the head " + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, c := range []struct {
		name     string
		versions []prVersion
		want     string
		err      string
	}{
		{"a run's version", []prVersion{good}, base, ""},
		{"a version that a person wrote", []prVersion{{id: "av_1", writer: "person", content: good.content}}, "", none},
		{"a version of a run with no id", []prVersion{{id: "av_1", writer: "run", content: good.content}}, "", none},
		{"a cancelled version", []prVersion{func() prVersion { v := good; v.cancelled = true; return v }()}, "", none},
		{"a withdrawn version", []prVersion{func() prVersion { v := good; v.withdrawn = true; return v }()}, "", none},
		{"a cancelled version beside one with another base", []prVersion{good, elsewhere(prVersion{writer: "run", run: "run_1", cancelled: true})}, base, ""},
		{"a withdrawn version beside one with another base", []prVersion{good, elsewhere(prVersion{writer: "run", run: "run_1", withdrawn: true})}, base, ""},
		{"a person's version beside one with another base", []prVersion{good, elsewhere(prVersion{writer: "person"})}, base, ""},
		{"a run's version beside one with another base", []prVersion{good, elsewhere(prVersion{writer: "run", run: "run_2"})}, "", "the inputs of the stage have 2 versions with the head"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The target version is the base of good, and run_2 started at the commit that run_0
			// pushed, which is other.
			snapshot := &runapi.Snapshot{PushedCommits: []runapi.PushedEntry{
				pushedBy("run_0", other), pushedBy("run_2", other),
			}}
			for _, v := range c.versions {
				snapshot.Inputs = append(snapshot.Inputs, runapi.Input{Type: "pull-request", Version: &runapi.VersionRef{ID: v.id}})
			}
			// The version of run_0 confirms the commit that run_0 pushed, the start of run_2.
			listed := append([]prVersion{{id: "av_0", writer: "run", run: "run_0", content: prFile(other, base)}}, c.versions...)
			got, err := pushedBaseOf(t.Context(), pullRequestClient(t, listed...), snapshot, types, target, base, head)
			switch {
			case c.err == "" && (err != nil || got != c.want):
				t.Errorf("pushedBaseOf returned %q and %v, want %q", got, err, c.want)
			case c.err != "" && !strings.Contains(flatStop(err), c.err):
				t.Errorf("pushedBaseOf returned %q and %v, want the stop %q", got, err, c.err)
			}
		})
	}
}

// TestPushedBaseOfAcceptsOnlyAStartOfTheWritersRun: of the base that a pull-request
// version records for the head, only the target version and the start that a write run
// would have taken from the versions before the version's run pass. That start is the
// newest commit of a run before that a version which a person did not cancel confirms.
// Any other commit stops the run with the cause: the head, which cuts the diff to nothing,
// a commit that no run pushed, an older commit of the run before, a commit of an earlier
// run that no version confirms, a commit of a run that a person cancelled, a commit that
// the writer or a later run pushed, and a commit that a run pushed to a repository of the
// same path on another system.
func TestPushedBaseOfAcceptsOnlyAStartOfTheWritersRun(t *testing.T) {
	c := func(d byte) string { return strings.Repeat(string(d), 40) }
	head, version, a1, a2, w1, later, unlisted, cancelled, failed := c('a'), c('b'), c('2'), c('3'), c('4'), c('6'), c('7'), c('8'), c('9')
	types := []exports.ProcessType{pullRequestSchemaType(t)}
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	other := pushedBy("run_1", a1)
	other.Path = "acme/other"
	// otherSystem is a commit of the path of the target on another system.
	otherSystem := pushedBy("run_1", c('5'))
	otherSystem.System = "other-forge"
	const stopped = "The base of the pull request is not the start of its run, so the review did not start"

	// run_1 pushed a1 and then a2, and run_w pushed w1 and then the head.
	before := []runapi.PushedEntry{pushedBy("run_1", a1), pushedBy("run_1", a2)}
	writer := []runapi.PushedEntry{pushedBy("run_w", w1), pushedBy("run_w", head)}
	// run_2 pushed the commit cancelled, and a person cancelled run_2.
	cancelledRun := []runapi.PushedEntry{pushedBy("run_2", cancelled)}
	cancelledRun[0].Cancelled = true
	// run_e pushed the commit failed and stored no version, as a run that failed does.
	failedRun := []runapi.PushedEntry{pushedBy("run_e", failed)}
	join := func(lists ...[]runapi.PushedEntry) []runapi.PushedEntry {
		var out []runapi.PushedEntry
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	}
	// confirms is a version of run that has one reference to commit, as a run's version has
	// one to the commit that it pushed, and one to the target version.
	confirms := func(id, run, commit string) prVersion {
		return prVersion{id: id, writer: "run", run: run, content: prFile(commit, version)}
	}
	confirmsA1, confirmsA2 := confirms("av_a1", "run_1", a1), confirms("av_a2", "run_1", a2)
	// notDelivered is a version of run of the type notes, which the process does not deliver,
	// that a session stored under the name of the delivered artifact.
	notDelivered := func(id, run, commit string) prVersion {
		v := confirms(id, run, commit)
		v.typ = "notes"
		return v
	}
	cancelledA2 := confirmsA2
	cancelledA2.cancelled = true
	confirmsCancelled := confirms("av_c", "run_2", cancelled)
	for _, tc := range []struct {
		name    string
		pushed  []runapi.PushedEntry
		earlier []prVersion
		base    string
		err     string
	}{
		{"the target version", join(before, writer), []prVersion{confirmsA2}, version, ""},
		{"the newest commit of the run before, confirmed by a version", join(before, writer), []prVersion{confirmsA2}, a2, ""},
		{"the head as the base", join(before, writer), []prVersion{confirmsA2}, head, stopped + " Version 1 of pull-request, written by run run_w, has the base " + head + ", and the run started at the target version " + version + " or the commit " + a2 + " of an earlier run"},
		{"a commit that no run pushed", join(before, writer), []prVersion{confirmsA2}, unlisted, stopped + " Version 1 of pull-request, written by run run_w, has the base " + unlisted + ", and the run started at the target version " + version + " or the commit " + a2 + " of an earlier run"},
		{"an older commit of the run before", join(before, writer), []prVersion{confirmsA2}, a1, stopped},
		{"the newest commit that a version of the run before confirms", join(before, writer), []prVersion{confirmsA1}, a1, ""},
		{"a newer commit of the run before that no version confirms", join(before, writer), []prVersion{confirmsA1}, a2, stopped},
		{"the newest commit of the run before, with no version", join(before, writer), nil, a2, stopped + " Version 1 of pull-request, written by run run_w, has the base " + a2 + ", and the run started at the target version " + version},
		{"the target version, with no version before", join(before, writer), nil, version, ""},
		{"the newest commit of the run before, confirmed by a version of a type that is not delivered", join(before, writer), []prVersion{confirmsA1, notDelivered("av_nd", "run_1", a2)}, a2, stopped + " Version 3 of pull-request, written by run run_w, has the base " + a2 + ", and the run started at the target version " + version + " or the commit " + a1 + " of an earlier run"},
		{"the newest commit of the run before, with only a version of a type that is not delivered", join(before, writer), []prVersion{notDelivered("av_nd", "run_1", a2)}, a2, stopped + " Version 2 of pull-request, written by run run_w, has the base " + a2 + ", and the run started at the target version " + version},
		{"the commit that a delivered version confirms, beside a version of a type that is not delivered", join(before, writer), []prVersion{confirmsA1, notDelivered("av_nd", "run_1", a2)}, a1, ""},
		{"the target version, with a version before that confirms no commit", join(before, writer), []prVersion{confirms("av_n", "run_1", unlisted)}, version, ""},
		{"the newest commit of the run before, with a version that confirms no commit", join(before, writer), []prVersion{confirms("av_n", "run_1", unlisted)}, a2, stopped + " Version 2 of pull-request, written by run run_w, has the base " + a2 + ", and the run started at the target version " + version},
		{"the newest commit of the run before, with only a cancelled version", join(before, writer), []prVersion{cancelledA2}, a2, stopped + " Version 2 of pull-request, written by run run_w, has the base " + a2 + ", and the run started at the target version " + version},
		{"the target version, with only a cancelled version before", join(before, writer), []prVersion{cancelledA2}, version, ""},
		{"a commit of a run that stored no version", join(before, failedRun, writer), []prVersion{confirmsA2}, failed, stopped + " Version 2 of pull-request, written by run run_w, has the base " + failed + ", and the run started at the target version " + version + " or the commit " + a2 + " of an earlier run"},
		{"a commit of a run that stored no version, with no version before", join(failedRun, writer), nil, failed, stopped},
		{"the commit of the run before a run that stored no version", join(before, failedRun, writer), []prVersion{confirmsA2}, a2, ""},
		{"the target version, beside a run that stored no version", join(failedRun, writer), nil, version, ""},
		{"the target version with no run before", writer, nil, version, ""},
		{"a commit of the writer with no run before", writer, nil, w1, stopped + " Version 1 of pull-request, written by run run_w, has the base " + w1 + ", and the run started at the target version " + version},
		{"a commit between the writer's pushes", join(before, writer), []prVersion{confirmsA2}, w1, stopped},
		{"a commit that a later run pushed", join(before, writer, []runapi.PushedEntry{pushedBy("run_3", later)}), []prVersion{confirmsA2, confirms("av_l", "run_3", later)}, later, stopped},
		{"the newest commit of the run before, with no commit of the writer", before, []prVersion{confirmsA2}, a2, stopped},
		{"the target version, with no commit of the writer", before, []prVersion{confirmsA2}, version, ""},
		{"a commit of another repository, that a version confirms", join([]runapi.PushedEntry{other}, writer), []prVersion{confirmsA1}, a1, stopped},
		{"the same path on another system, that a version confirms", join(before, []runapi.PushedEntry{otherSystem}, writer), []prVersion{confirmsA2, confirms("av_o", "run_1", otherSystem.Commit)}, otherSystem.Commit, stopped},
		{"the newest commit of the target beside the same path on another system", join(before, []runapi.PushedEntry{otherSystem}, writer), []prVersion{confirmsA2}, a2, ""},
		{"the target version, beside another repository", join([]runapi.PushedEntry{other}, writer), nil, version, ""},
		{"the target version, beside the same path on another system", join(before, []runapi.PushedEntry{otherSystem}, writer), []prVersion{confirmsA2}, version, ""},
		{"a commit of a cancelled run that a version confirms", join(before, cancelledRun, writer), []prVersion{confirmsA2, confirmsCancelled}, cancelled, stopped},
		{"the newest commit of the run before a cancelled run", join(before, cancelledRun, writer), []prVersion{confirmsA2, confirmsCancelled}, a2, ""},
		{"the target version, beside a cancelled run", join(before, cancelledRun, writer), []prVersion{confirmsA2}, version, ""},
		{"the commit of the repository before another one", join(before, []runapi.PushedEntry{other}, writer), []prVersion{confirmsA2}, a2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := prVersion{id: "av_1", writer: "run", run: "run_w", content: prFile(head, tc.base)}
			snapshot := &runapi.Snapshot{PushedCommits: tc.pushed, Inputs: []runapi.Input{{Type: "pull-request", Version: &runapi.VersionRef{ID: "av_1"}}}}
			got, err := pushedBaseOf(t.Context(), pullRequestClient(t, append(slices.Clone(tc.earlier), v)...), snapshot, types, target, strings.ToUpper(version), head)
			// The version of the writer is listed after the versions before it.
			want := strings.ReplaceAll(tc.err, "Version 1 ", fmt.Sprintf("Version %d ", len(tc.earlier)+1))
			switch {
			case tc.err == "" && (err != nil || got != tc.base):
				t.Errorf("pushedBaseOf returned %q and %v, want %q", got, err, tc.base)
			case tc.err != "" && !hasStop(err, want):
				t.Errorf("pushedBaseOf returned %q and %v, want the stop %q", got, err, want)
			case tc.err != "" && got != "":
				t.Errorf("pushedBaseOf returned the base %q with a stop", got)
			}
			// The first line of a stop is at most 100 characters with "qory run: ".
			if first, _, _ := strings.Cut(fmt.Sprint(err), "\n"); err != nil && len("qory run: ")+len(first) > 100 {
				t.Errorf("the first line of the stop %q has %d characters", first, len("qory run: ")+len(first))
			}
		})
	}
}

// TestPushedBaseOfNeedsAMemberThatQoryFillsWithTheCommits: a version before the writer
// confirms the start commit of an earlier run only when its type is delivered and has a
// required member that qory fills with the pushed commits, as the pull-request type of the
// software factory does. A delivered type with no such member, or one whose member is
// optional, confirms no commit, because a session cannot set a member that qory fills but
// can leave it out. A base with that commit stops the run, and the target version stays
// valid.
func TestPushedBaseOfNeedsAMemberThatQoryFillsWithTheCommits(t *testing.T) {
	head, version, a1 := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("2", 40)
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	factory := pullRequestSchemaType(t)
	var schema map[string]any
	if err := json.Unmarshal(factory.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["required"] = slices.DeleteFunc(schema["required"].([]any), func(name any) bool { return name == "commits" })
	optional := factory
	var err error
	if optional.Schema, err = json.Marshal(schema); err != nil {
		t.Fatal(err)
	}
	// A root key that differs from "required" in case lists nothing as required.
	var whole map[string]any
	if err := json.Unmarshal(factory.Schema, &whole); err != nil {
		t.Fatal(err)
	}
	whole["Required"] = whole["required"]
	delete(whole, "required")
	capitalised := factory
	if capitalised.Schema, err = json.Marshal(whole); err != nil {
		t.Fatal(err)
	}
	noSchema := factory
	noSchema.Schema = nil
	noCommits := exports.ProcessType{Name: "pull-request", Send: "delivered", Marks: map[string]string{"head": fillHead, "base": fillBase}}
	noMarks := exports.ProcessType{Name: "note", Send: "delivered"}
	for _, tc := range []struct {
		name string
		// input is the type of the version that the stage reads, typ the type of the version
		// of the earlier run.
		input, typ exports.ProcessType
		base       string
		stops      bool
	}{
		{"the pull-request type of the software factory confirms the commit", factory, factory, a1, false},
		{"a delivered type with no member filled with the commits does not", noCommits, noCommits, a1, true},
		{"a delivered type whose member filled with the commits is optional does not", optional, optional, a1, true},
		{"a delivered type with no mark does not", factory, noMarks, a1, true},
		{"a delivered type with the commits mark and no schema does not", noSchema, noSchema, a1, true},
		{"a schema whose root key is Required in capitals does not", capitalised, capitalised, a1, true},
		{"the target version, with a type that confirms no commit", noCommits, noCommits, version, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &runapi.Snapshot{
				PushedCommits: []runapi.PushedEntry{pushedBy("run_1", a1), pushedBy("run_w", head)},
				Inputs:        []runapi.Input{{Type: "pull-request", Version: &runapi.VersionRef{ID: "av_1"}}},
			}
			earlier := prVersion{id: "av_a1", typ: tc.typ.Name, writer: "run", run: "run_1", content: prFile(a1, version)}
			v := prVersion{id: "av_1", writer: "run", run: "run_w", content: prFile(head, tc.base)}
			types := []exports.ProcessType{tc.input}
			if tc.typ.Name != tc.input.Name {
				types = append(types, tc.typ)
			}
			got, err := pushedBaseOf(t.Context(), pullRequestClient(t, earlier, v), snapshot, types, target, version, head)
			switch {
			case !tc.stops && (err != nil || got != tc.base):
				t.Errorf("pushedBaseOf returned %q and %v, want %q", got, err, tc.base)
			case tc.stops && (err == nil || got != ""):
				t.Errorf("pushedBaseOf returned %q and %v, want a stop", got, err)
			}
		})
	}
}

// TestPushedBaseOfStopsWhenTheTargetMovedAfterTheBuild: the first Build run of a work item
// has the target version at its start as the base. When a merge moves the target version
// before Verify runs, that base is neither the target version nor the start that an
// earlier run gives, and the run stops with the cause.
func TestPushedBaseOfStopsWhenTheTargetMovedAfterTheBuild(t *testing.T) {
	head, t1, t2 := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	types := []exports.ProcessType{pullRequestSchemaType(t)}
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	snapshot := &runapi.Snapshot{
		PushedCommits: []runapi.PushedEntry{pushedBy("run_w", strings.Repeat("4", 40)), pushedBy("run_w", head)},
		Inputs:        []runapi.Input{{Type: "pull-request", Version: &runapi.VersionRef{ID: "av_1"}}},
	}
	client := pullRequestClient(t, prVersion{id: "av_1", writer: "run", run: "run_w", content: prFile(head, t1)})
	if got, err := pushedBaseOf(t.Context(), client, snapshot, types, target, t1, head); err != nil || got != t1 {
		t.Errorf("with the target at %s pushedBaseOf returned %q and %v, want that base", t1, got, err)
	}
	want := "The base of the pull request is not the start of its run, so the review did not start" +
		" Version 1 of pull-request, written by run run_w, has the base " + t1 + ", and the run started at the target version " + t2
	if got, err := pushedBaseOf(t.Context(), client, snapshot, types, target, t2, head); !hasStop(err, want) || got != "" {
		t.Errorf("with the target at %s pushedBaseOf returned %q and %v, want the stop %q", t2, got, err, want)
	}
}

// TestPushedBaseOfStopsWhenTheFileOfAVersionBeforeIsUnreadable: a start is a version that
// confirms a commit, so a version file that cannot be read stops the run with the cause
// of the failed read, and the run does not treat it as a version that confirms nothing.
func TestPushedBaseOfStopsWhenTheFileOfAVersionBeforeIsUnreadable(t *testing.T) {
	head, version, a1 := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("2", 40)
	types := []exports.ProcessType{pullRequestSchemaType(t)}
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	snapshot := &runapi.Snapshot{
		PushedCommits: []runapi.PushedEntry{pushedBy("run_1", a1), pushedBy("run_w", head)},
		Inputs:        []runapi.Input{{Type: "pull-request", Version: &runapi.VersionRef{ID: "av_1"}}},
	}
	client := pullRequestClient(t,
		prVersion{id: "av_0", writer: "run", run: "run_1", content: prFile(a1, version), unreadable: true},
		prVersion{id: "av_1", writer: "run", run: "run_w", content: prFile(head, version)})
	const want = "Reading a version of an earlier run failed, so the review did not start"
	if got, err := pushedBaseOf(t.Context(), client, snapshot, types, target, version, head); !hasStop(err, want) || got != "" {
		t.Errorf("pushedBaseOf returned %q and %v, want the stop %q", got, err, want)
	}
}

// TestPushedBaseOfStopsWhenTheFileOfThePullRequestVersionIsUnreadable: the version of the
// stage's input that records the base is read first, and a failed read of its file stops
// the run with the line for the pull request version, not the line for an earlier run.
func TestPushedBaseOfStopsWhenTheFileOfThePullRequestVersionIsUnreadable(t *testing.T) {
	head, version := strings.Repeat("a", 40), strings.Repeat("b", 40)
	types := []exports.ProcessType{pullRequestSchemaType(t)}
	target := &contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	snapshot := &runapi.Snapshot{
		PushedCommits: []runapi.PushedEntry{pushedBy("run_w", head)},
		Inputs:        []runapi.Input{{Type: "pull-request", Version: &runapi.VersionRef{ID: "av_1"}}},
	}
	client := pullRequestClient(t, prVersion{id: "av_1", writer: "run", run: "run_w", content: prFile(head, version), unreadable: true})
	const want = "Fetching the pull request version failed, so the review did not start"
	if got, err := pushedBaseOf(t.Context(), client, snapshot, types, target, version, head); !hasStop(err, want) || got != "" {
		t.Errorf("pushedBaseOf returned %q and %v, want the stop %q", got, err, want)
	}
}
