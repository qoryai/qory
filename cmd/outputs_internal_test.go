package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/runapi"
)

// pullRequestSchemaType is a delivered structured type with a title and every mark, as the
// software factory's pull-request type has them.
func pullRequestSchemaType(t *testing.T) exports.ProcessType {
	t.Helper()
	ref := map[string]any{"$ref": primitives.URL + "#/$defs/reference"}
	schema := map[string]any{
		"type":     "object",
		"required": []any{"target", "title", "commits", "head", "base", "branch", "base_branch"},
		"properties": map[string]any{
			"target":          ref,
			"title":           map[string]any{"$ref": primitives.URL + "#/$defs/title"},
			"commits":         map[string]any{"type": "array", "items": ref, "x-qory-fill": "pushed-commits"},
			"head":            map[string]any{"$ref": primitives.URL + "#/$defs/reference", "x-qory-fill": "pushed-head"},
			"base":            map[string]any{"$ref": primitives.URL + "#/$defs/reference", "x-qory-fill": "pushed-base"},
			"branch":          map[string]any{"type": "string", "x-qory-fill": "pushed-branch"},
			"base_branch":     map[string]any{"type": "string", "x-qory-fill": "pushed-base-branch"},
			"earlier_commits": map[string]any{"type": "integer", "x-qory-fill": "pushed-commits-left-out"},
		},
	}
	raw, err := json.Marshal(exports.InlinePrimitives(schema))
	if err != nil {
		t.Fatal(err)
	}
	return exports.ProcessType{Name: "pull-request", Send: "after_gate", Schema: raw, Marks: map[string]string{
		"commits": "pushed-commits", "head": "pushed-head", "base": "pushed-base", "branch": "pushed-branch",
		"base_branch": "pushed-base-branch", "earlier_commits": "pushed-commits-left-out",
	}}
}

// shopTarget is a repository target of the documents.
var shopTarget = &contracts.Target{Collection: "R_kgDOExample1", Address: &contracts.TargetAddress{System: "code.example.com", Path: "acme/shop"}}

// shopPush is a push of two commits to shopTarget.
var shopPush = &pushRecord{
	System: "code.example.com", Path: "acme/shop", Collection: "R_kgDOExample1",
	Branch: "qory/wi_1", Head: strings.Repeat("c", 40), Base: strings.Repeat("a", 40), BaseBranch: "main",
	Commits: []string{strings.Repeat("b", 40), strings.Repeat("c", 40)},
}

// TestAnOutputGetsItsTargetAndEachMarkFromThePush is a pull-request outbox file with a title:
// qory sets target to the documents' target and fills each marked member from the push, the
// commits as references oldest first, and leaves pushed-commits-left-out alone; the result
// passes the type's schema.
func TestAnOutputGetsItsTargetAndEachMarkFromThePush(t *testing.T) {
	typ := pullRequestSchemaType(t)
	filled, err := fillOutput([]byte(`{"title":"Fix the cart"}`), typ, shopTarget, shopPush)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(filled, &got); err != nil {
		t.Fatal(err)
	}
	ref := func(commit string) any {
		return map[string]any{"collection": "R_kgDOExample1", "address": map[string]any{"system": "code.example.com", "path": commit}}
	}
	want := map[string]any{
		"title":       "Fix the cart",
		"target":      map[string]any{"collection": "R_kgDOExample1", "address": map[string]any{"system": "code.example.com", "path": "acme/shop"}},
		"commits":     []any{ref(strings.Repeat("b", 40)), ref(strings.Repeat("c", 40))},
		"head":        ref(strings.Repeat("c", 40)),
		"base":        ref(strings.Repeat("a", 40)),
		"branch":      "qory/wi_1",
		"base_branch": "main",
	}
	if g, w := mustMarshal(t, got), mustMarshal(t, want); g != w {
		t.Errorf("the filled output is\n%s\nwant\n%s", g, w)
	}
	if err := checkOutput(filled, typ); err != nil {
		t.Errorf("the filled output breaks its schema: %v", err)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAnOutputThatHasAFilledMemberStops is an outbox file with target, with a marked member,
// and with the member that Qory Apiary sets: each stops with the fix first, line 1 at most 100
// characters with "qory run: ". A file that is no JSON object stops too.
func TestAnOutputThatHasAFilledMemberStops(t *testing.T) {
	typ := pullRequestSchemaType(t)
	for file, want := range map[string]string{
		`{"title":"a","target":{"collection":"x"}}`: "Remove target from the pull-request output: qory fills it at the push.",
		`{"title":"a","head":{"collection":"x"}}`:   "Remove head from the pull-request output: qory fills it at the push.",
		`{"title":"a","earlier_commits":3}`:         "Remove earlier_commits from the pull-request output: qory fills it at the push.",
		`[1]`:                                       "Write the pull-request output as one JSON object: the file is not one.",
		`null`:                                      "Write the pull-request output as one JSON object: the file is not one.",
	} {
		_, err := fillOutput([]byte(file), typ, shopTarget, shopPush)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", file, err, want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); utf8.RuneCountInString("qory run: "+first) > 100 {
			t.Errorf("%s: line 1 has more than 100 characters: %s", file, first)
		}
	}
}

// TestAnOutputWithMarksAndNoPushStops is a marked type of a run that pushed nothing to the
// target: there is nothing to fill the marks from, and the output stops.
func TestAnOutputWithMarksAndNoPushStops(t *testing.T) {
	_, err := fillOutput([]byte(`{"title":"a"}`), pullRequestSchemaType(t), shopTarget, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "Fix the pull-request output so that qory can read it.\n  the run pushed no repository") {
		t.Errorf("%v", err)
	}
}

// TestAnOutputThatIsNotDeliveredGetsNoTarget is a type with no deliver and no marks: qory
// sets no target, and a target the session wrote stays, for the schema to judge.
func TestAnOutputThatIsNotDeliveredGetsNoTarget(t *testing.T) {
	typ := exports.ProcessType{Name: "notes", Schema: json.RawMessage(`{"type":"object"}`)}
	for file, want := range map[string]string{`{"a":1}`: `{"a":1}`, `{"target":2}`: `{"target":2}`} {
		got, err := fillOutput([]byte(file), typ, shopTarget, nil)
		if err != nil || string(got) != want {
			t.Errorf("%s: %s, %v, want %s", file, got, err, want)
		}
	}
}

// TestAnOutputThatBreaksItsSchemaStopsWithThePlace is a filled pull-request whose title breaks
// the primitive title: the error has the place /title; a long place is cut, and line 1 with a
// type of 19 characters and "qory run: " stays within 100 characters.
func TestAnOutputThatBreaksItsSchemaStopsWithThePlace(t *testing.T) {
	typ := pullRequestSchemaType(t)
	filled, err := fillOutput([]byte(`{"title":""}`), typ, shopTarget, shopPush)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOutput(filled, typ); err == nil || err.Error() != "Fix the pull-request output: it breaks its schema at /title." {
		t.Errorf("%v", err)
	}
	long := exports.ProcessType{Name: strings.Repeat("a", 64), Schema: json.RawMessage(`{"type":"object","properties":{"` + strings.Repeat("k", 90) + `":{"type":"string"}}}`)}
	err = checkOutput([]byte(`{"`+strings.Repeat("k", 90)+`":1}`), long)
	if err == nil {
		t.Fatal("no error")
	}
	if n := utf8.RuneCountInString("qory run: " + err.Error()); n > 100 {
		t.Errorf("line 1 has %d characters: %s", n, err)
	}
	if !strings.HasPrefix(err.Error(), "Fix the "+strings.Repeat("a", 19)+" output: it breaks its schema at /kkk") || !strings.HasSuffix(err.Error(), "….") {
		t.Errorf("%v", err)
	}
}

// TestADeliveredOutputGoesToTheArtifactOfItsCollection is the name rule: a delivered type's
// artifact is t- and 32 hexadecimal digits of the SHA-256 of the collection, the same for the
// same collection and another for another; a type that is not delivered, or a run with no
// target, uses main.
func TestADeliveredOutputGoesToTheArtifactOfItsCollection(t *testing.T) {
	delivered := exports.ProcessType{Name: "pull-request", Send: "after_gate"}
	if got := outputName(delivered, shopTarget); got != "t-e047707709a59e682f6b3df0e394d0bb" {
		t.Errorf("the name is %s", got)
	}
	other := &contracts.Target{Collection: "R_kgDOExample2"}
	if a, b := outputName(delivered, shopTarget), outputName(delivered, other); a == b || len(b) != 34 {
		t.Errorf("two collections give %s and %s", a, b)
	}
	if got := outputName(exports.ProcessType{Name: "notes"}, shopTarget); got != "main" {
		t.Errorf("a type that is not delivered goes to %s", got)
	}
	if got := outputName(delivered, nil); got != "main" {
		t.Errorf("a run with no target goes to %s", got)
	}
}

// TestReadOutputReadsTheTypesEntry is the outbox entry of a structured type, a file, and of a
// type with no schema, a folder of files: each is read; a missing entry is not found; a link,
// a file over the size limit and a file in place of the folder are errors.
func TestReadOutputReadsTheTypesEntry(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reply := exports.ProcessType{Name: "reply", Schema: json.RawMessage(`{}`), SizeLimit: 16}
	if _, found, err := readOutput(root, reply); found || err != nil {
		t.Errorf("an empty outbox: found %v, %v", found, err)
	}
	write(".qory/outbox/reply/reply.json", `{"a":1}`)
	files, found, err := readOutput(root, reply)
	if err != nil || !found || len(files) != 1 || files[0].Filename != "reply.json" || string(files[0].Content) != `{"a":1}` || files[0].ContentType != "application/json" {
		t.Errorf("reply: %+v, %v, %v", files, found, err)
	}
	write(".qory/outbox/reply/reply.json", strings.Repeat("a", 17))
	if _, _, err := readOutput(root, reply); err == nil {
		t.Error("a file over the size limit is read")
	}
	notes := exports.ProcessType{Name: "notes"}
	write(".qory/outbox/notes/a.md", "# A\n")
	write(".qory/outbox/notes/b.md", "# B\n")
	files, found, err = readOutput(root, notes)
	if err != nil || !found || len(files) != 2 || files[0].Filename != "a.md" || files[1].Filename != "b.md" {
		t.Errorf("notes: %+v, %v, %v", files, found, err)
	}
	if err := os.Symlink(filepath.Join(root, ".qory/outbox/notes/a.md"), filepath.Join(root, ".qory/outbox/notes/c.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOutput(root, notes); err == nil {
		t.Error("a link in the folder is read")
	}
	write(".qory/outbox/plain", "x")
	if _, _, err := readOutput(root, exports.ProcessType{Name: "plain"}); err == nil {
		t.Error("a file in place of the folder is read")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "linked.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".qory/outbox/linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "linked.json"), filepath.Join(root, ".qory/outbox/linked/linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOutput(root, exports.ProcessType{Name: "linked", Schema: json.RawMessage(`{}`)}); err == nil {
		t.Error("a link out of the checkout is read")
	}
	// A type folder that is a link inside the checkout, to the folder of reply, is not read.
	if err := os.Symlink(filepath.Join(root, ".qory/outbox/reply"), filepath.Join(root, ".qory/outbox/answer")); err != nil {
		t.Fatal(err)
	}
	write(".qory/outbox/reply/answer.json", `{}`)
	if _, _, err := readOutput(root, exports.ProcessType{Name: "answer", Schema: json.RawMessage(`{}`)}); err == nil {
		t.Error("a type folder that is a link is read")
	}
}

// TestMarksCommitsFindsThePushedCommitsMark is a type with and one without pushed-commits.
func TestMarksCommitsFindsThePushedCommitsMark(t *testing.T) {
	if !marksCommits(pullRequestSchemaType(t)) || marksCommits(exports.ProcessType{Marks: map[string]string{"head": "pushed-head"}}) {
		t.Error("marksCommits is wrong")
	}
}

// TestTheRecordMatchesARepositoryOnGitHubInAnyCase is a push to Acme/Shop on github.com for
// the target acme/shop: github.com names are not case-sensitive, so the record is the push's.
// On another system the case differs, so there is no record.
func TestTheRecordMatchesARepositoryOnGitHubInAnyCase(t *testing.T) {
	for system, want := range map[string]bool{preparation.ForgeGitHub: true, "git.example.com": false} {
		branch := &preparation.WorkBranch{Version: []preparation.Repository{{System: system, Path: "Acme/Shop", Commit: strings.Repeat("a", 40), Base: &preparation.Base{Commit: strings.Repeat("b", 40), Branch: "main"}}}}
		target := &contracts.Target{Collection: "R_1", Address: &contracts.TargetAddress{System: system, Path: "acme/shop"}}
		if got := recordOf(branch, target) != nil; got != want {
			t.Errorf("%s: a record %v, want %v", system, got, want)
		}
	}
}

// TestClearOutboxRemovesTheOutboxAlone is a checkout whose outbox has a file and a link to a
// folder outside, beside a file in .qory: the outbox goes, and the file beside it and the
// folder outside stay. An outbox that is a link goes as the link. A checkout with no .qory
// has nothing to clear, and a .qory that is a link is an error that removes nothing.
func TestClearOutboxRemovesTheOutboxAlone(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkdir := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		t.Helper()
		mkdir(filepath.Dir(p))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		t.Helper()
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	root := t.TempDir()
	write(filepath.Join(root, ".qory", "outbox", "reply", "reply.json"))
	link(outside, filepath.Join(root, ".qory", "outbox", "out"))
	write(filepath.Join(root, ".qory", "kept.txt"))
	if err := clearOutbox(root); err != nil || exists(filepath.Join(root, ".qory", "outbox")) {
		t.Errorf("the outbox: %v, still there: %v", err, exists(filepath.Join(root, ".qory", "outbox")))
	}
	if !exists(filepath.Join(root, ".qory", "kept.txt")) || !exists(filepath.Join(outside, "keep.txt")) {
		t.Error("a file beside the outbox or outside is gone")
	}

	linked := t.TempDir()
	mkdir(filepath.Join(linked, ".qory"))
	link(outside, filepath.Join(linked, ".qory", "outbox"))
	if err := clearOutbox(linked); err != nil || exists(filepath.Join(linked, ".qory", "outbox")) || !exists(filepath.Join(outside, "keep.txt")) {
		t.Errorf("an outbox that is a link: %v", err)
	}

	if err := clearOutbox(t.TempDir()); err != nil {
		t.Errorf("no .qory: %v", err)
	}

	qoryLink := t.TempDir()
	mkdir(filepath.Join(outside, "outbox"))
	write(filepath.Join(outside, "outbox", "reply.json"))
	link(outside, filepath.Join(qoryLink, ".qory"))
	err := clearOutbox(qoryLink)
	if err == nil || !strings.HasPrefix(err.Error(), "Remove .qory/outbox, then start the run again: ") || !exists(filepath.Join(outside, "outbox", "reply.json")) {
		t.Errorf("a .qory that is a link: %v", err)
	}
}

// TestATypeWhoseSchemaDoesNotConvertStopsTheRun is a composed harness whose type notes has a
// json_schema that does not convert: a run whose stage's outputs or documents' deliver list
// notes stops with the accepted text, and a run that lists only reply goes on, as does a run
// with no run code.
func TestATypeWhoseSchemaDoesNotConvertStopsTheRun(t *testing.T) {
	types := []exports.ProcessType{
		{Name: "reply", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "notes", Unread: "A JSON key is a string"},
	}
	want := "Report the run to the instance admin of Qory Apiary: an output's schema does not convert.\n" +
		"  The json_schema of artifact type notes is not a JSON Schema that qory can read: A JSON key is a string. The run code is used up."
	for name, r := range map[string]*codeRun{
		"a stage output": {snapshot: &runapi.Snapshot{Outputs: []runapi.Output{{Type: "reply"}, {Type: "notes"}}}},
		"a deliver":      {snapshot: &runapi.Snapshot{}, deliver: []contracts.Deliver{{Type: "notes", Send: "after_gate"}}},
		"no snapshot":    {deliver: []contracts.Deliver{{Type: "notes", Send: "after_gate"}}},
	} {
		if err := r.checkOutputTypes(types); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	listsReply := &codeRun{snapshot: &runapi.Snapshot{Outputs: []runapi.Output{{Type: "reply"}}}}
	if err := listsReply.checkOutputTypes(types); err != nil {
		t.Errorf("a run that lists reply alone: %v", err)
	}
	if err := (*codeRun)(nil).checkOutputTypes(types); err != nil {
		t.Errorf("a run with no run code: %v", err)
	}
}

// TestDeliveredTypesTakeTheSendOfTheDocuments is a harness that defines pull-request, with a
// send that the run ignores, and notes: with the documents' deliver of pull-request, the types
// are the harness's with the documents' send; a deliver of a type that the harness does not
// define, cut to 19 characters on the line, is the fix-first error.
func TestDeliveredTypesTakeTheSendOfTheDocuments(t *testing.T) {
	types := []exports.ProcessType{{Name: "pull-request", Send: "end_of_run"}, {Name: "notes", Send: "send"}}
	r := &codeRun{deliver: []contracts.Deliver{{Type: "pull-request", Send: "after_gate"}}}
	got, err := r.deliveredTypes(types)
	if err != nil || len(got) != 2 || got[0].Send != "after_gate" || got[1].Send != "" || types[0].Send != "end_of_run" {
		t.Errorf("the types are %+v, %v", got, err)
	}
	if got, err := (*codeRun)(nil).deliveredTypes(types); err != nil || len(got) != 2 || got[0].Send != "" {
		t.Errorf("no run code: %+v, %v", got, err)
	}
	long := strings.Repeat("abcdefgh-", 7) + "x"
	r.deliver = append(r.deliver, contracts.Deliver{Type: long, Send: "after_gate"})
	want := "Define " + long[:19] + " in the harness's process.yaml: the run documents deliver it.\n" +
		"  The composed harness defines no artifact type " + long[:19] + ", so qory cannot check or fill its output. The run code is used up."
	if got, err := r.deliveredTypes(types); err == nil || err.Error() != want || got != nil {
		t.Errorf("an undefined type: %+v, %v, want %q", got, err, want)
	}
}

// TestADeliveredOutputOverTheStartLimitStops is a delivered reply whose file, once target is
// filled, has 786432 bytes, which is stored, and one byte more, which stops with the fix. A
// reply that is not delivered has no such limit.
func TestADeliveredOutputOverTheStartLimitStops(t *testing.T) {
	reply := exports.ProcessType{Name: "reply", Send: "after_gate", Schema: json.RawMessage(`{"type":"object"}`)}
	file := func(n int) []byte { return []byte(`{"text":"` + strings.Repeat("a", n) + `"}`) }
	filled, err := fillOutput(file(0), reply, shopTarget, nil)
	if err != nil {
		t.Fatal(err)
	}
	fits := maxStartFile - len(filled)
	for name, c := range map[string]struct {
		typ  exports.ProcessType
		n    int
		want string
	}{
		"at the limit":  {reply, fits, ""},
		"one byte over": {reply, fits + 1, "Cut the reply output to at most 786432 bytes: the next run starts from it.\n  With target and the members that qory fills, reply.json has 786433 bytes."},
		"not delivered": {exports.ProcessType{Name: "reply", Schema: reply.Schema}, maxStartFile, ""},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, ".qory", "outbox", "reply")
			if err := os.MkdirAll(entry, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(entry, "reply.json"), file(c.n), 0o600); err != nil {
				t.Fatal(err)
			}
			o := &runOutputs{root: root, stage: []runapi.Output{{Type: "reply"}}, types: []exports.ProcessType{c.typ}, target: shopTarget}
			out, err := o.ready(io.Discard, nil, "")
			switch {
			case c.want == "" && (err != nil || len(out) != 1):
				t.Errorf("%d ready, %v, want one", len(out), err)
			case c.want != "" && (err == nil || err.Error() != c.want):
				t.Errorf("%v, want %q", err, c.want)
			}
		})
	}
}

// TestRemoveStoredLeavesWhatTheVersionDoesNotHave is the folder of pull-request with
// pull-request.json, which the version has, and notes.md beside it: notes.md and the folder
// stay. A folder whose files the version has goes.
func TestRemoveStoredLeavesWhatTheVersionDoesNotHave(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, ".qory", "outbox", "pull-request")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pull-request.json", "notes.md"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stored := readyOutput{push: runapi.Push{Type: "pull-request", Files: []runapi.PushFile{{Filename: "pull-request.json"}}}, folder: ".qory/outbox/pull-request"}
	if err := removeStored(root, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(folder, "pull-request.json")); !os.IsNotExist(err) {
		t.Errorf("the stored file stays: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "notes.md")); err != nil {
		t.Errorf("the file beside it is gone: %v", err)
	}
	if err := os.Remove(filepath.Join(folder, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "pull-request.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStored(root, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the empty folder stays: %v", err)
	}
}

// TestOnlyARunWithNoRepositorySaysSo is a stored reply of a run with no repository, which
// prints that the run has no repository to push, and of a run with repositories and no
// branch, which prints nothing.
func TestOnlyARunWithNoRepositorySaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"version":1,"artifact":{"type":"reply","name":"main","version":{"id":"av_1","number":1}}}`)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for noRepository, want := range map[bool]string{true: "qory run: stored the reply output; the run has no repository to push.\n", false: ""} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".qory", "outbox", "reply"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".qory", "outbox", "reply", "reply.md"), []byte("Ships Monday."), 0o600); err != nil {
			t.Fatal(err)
		}
		o := &runOutputs{root: root, stage: []runapi.Output{{Type: "reply"}}, noRepository: noRepository}
		var lines strings.Builder
		if err := o.store(t.Context(), &lines, client, "run_1", "", nil, &runPushChecks{}); err != nil || lines.String() != want {
			t.Errorf("no repository %v: %q, %v, want %q", noRepository, lines.String(), err, want)
		}
	}
}

// TestATypeThatMarksNothingQoryFillsIsStoredWithNoPush is a run that pushed nothing to the
// target: a type whose one mark is pushed-commits-left-out, which Qory Apiary sets, or a mark
// that qory does not know, gets its version, with the member left out; a type that marks
// pushed-head gets none, and the line.
func TestATypeThatMarksNothingQoryFillsIsStoredWithNoPush(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"earlier":{"type":"integer"}}}`)
	for mark, stored := range map[string]bool{"pushed-commits-left-out": true, "pushed-elsewhere": true, "pushed-head": false} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".qory", "outbox", "summary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".qory", "outbox", "summary", "summary.json"), []byte(`{"title":"a"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		typ := exports.ProcessType{Name: "summary", Send: "after_gate", Schema: schema, Marks: map[string]string{"earlier": mark}}
		o := &runOutputs{root: root, stage: []runapi.Output{{Type: "summary"}}, types: []exports.ProcessType{typ}, target: shopTarget}
		var lines strings.Builder
		out, err := o.ready(&lines, nil, "")
		if err != nil || (len(out) == 1) != stored || (lines.Len() == 0) != stored {
			t.Errorf("%s: %d ready, %v, lines %q; want stored %v", mark, len(out), err, lines.String(), stored)
		}
		if stored && strings.Contains(string(out[0].push.Files[0].Content), "earlier") {
			t.Errorf("%s: the member is filled: %s", mark, out[0].push.Files[0].Content)
		}
	}
}

// TestAFolderOfTooManyFilesStopsBeforeTheRead is the folder of notes with 100 files and a
// link: the count, 101, stops the read before any file is read, so the error is the count's
// and not the link's.
func TestAFolderOfTooManyFilesStopsBeforeTheRead(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, ".qory", "outbox", "notes")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range runapi.MaxFiles {
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("n%03d.md", i)), []byte("#\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := exports.ProcessType{Name: "notes"}
	if files, _, err := readOutput(root, notes); err != nil || len(files) != runapi.MaxFiles {
		t.Fatalf("100 files: %d, %v", len(files), err)
	}
	if err := os.Symlink(filepath.Join(folder, "n000.md"), filepath.Join(folder, "a-link.md")); err != nil {
		t.Fatal(err)
	}
	_, found, err := readOutput(root, notes)
	if want := "Push at most 100 files in one version of notes. This version has 101."; !found || err == nil || err.Error() != want {
		t.Errorf("101 entries: found %v, %v, want %q", found, err, want)
	}
}
