package cmd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// runWith executes one qory command line with stdin and returns everything it printed,
// the way [run] does.
func runWith(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

// rawAbouts reads the events of the one run recorded under the checkout and returns the
// about of each event that has one, by type, as the record holds it.
func rawAbouts(t *testing.T, root string) map[string]string {
	t.Helper()
	dir, _ := events(t, root)
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	abouts := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Type string                     `json:"type"`
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if about, ok := ev.Data["about"]; ok {
			abouts[ev.Type] = string(about)
		}
	}
	return abouts
}

// TestRunSaysWhatItIsAbout is a run with all four flags: run.started carries about, the
// subjects in their order, a subject's title with its commas and = as given, a type of
// two words, and the details compacted with < escaped, and no other event carries it. A
// run without the flags, or with every one empty and details of {}, has no about.
func TestRunSaysWhatItIsAbout(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	writeFile(t, filepath.Join(root, "details.json"), "{\n  \"queue\": \"nightly\",\n  \"attempt\": 2,\n  \"note\": \"a<b\"\n}\n")
	out, err := run(t, "run", "--kind", "review", "--title", "Review the parser, then fix it",
		"--subject", "type=ticket,ref=7,url=https://tickets.example.com/7",
		"--subject", "type=pull request,ref=42,title=Parser drops the last line, sometimes; a=b",
		"--details", "details.json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := `{"kind":"review","title":"Review the parser, then fix it","subjects":[` +
		`{"type":"ticket","ref":"7","url":"https://tickets.example.com/7"},` +
		`{"type":"pull request","ref":"42","title":"Parser drops the last line, sometimes; a=b"}],` +
		`"details":{"queue":"nightly","attempt":2,"note":"a\u003cb"}}`
	abouts := rawAbouts(t, root)
	if abouts["dev.qory.run.started"] != want {
		t.Errorf("run.started about\n%s\nwant\n%s", abouts["dev.qory.run.started"], want)
	}
	delete(abouts, "dev.qory.run.started")
	if len(abouts) != 0 {
		t.Errorf("other events carry about: %v", abouts)
	}

	for _, args := range [][]string{
		{"run"},
		{"run", "--kind", "", "--title", "", "--details", "-"},
	} {
		if err := os.RemoveAll(runsDir(t, root)); err != nil {
			t.Fatal(err)
		}
		if out, err := runWith(t, strings.NewReader("{ }\n"), args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if abouts := rawAbouts(t, root); len(abouts) != 0 {
			t.Errorf("%v: about %v", args, abouts)
		}
		if _, evs := events(t, root); len(evs["dev.qory.run.started"]) != 1 {
			t.Errorf("%v: run.started %v", args, evs["dev.qory.run.started"])
		}
	}
}

// TestRunAboutStaysOffTheRunConfigurationRequest is a run with a server: the run
// configuration request is asked with the labels alone, as it is without about, and the
// server's run.started carries about.
func TestRunAboutStaysOffTheRunConfigurationRequest(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"enforce","allow":["api.example"]}}`)
	serverFile(t, srv, "")
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run", "--label", "issue=77", "--kind", "fix", "--title", "Fix the parser", "--subject", "type=ticket,ref=7")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if srv.refused != 0 || strings.Join(srv.queries, " ") != "forge=git.example.com&issue=77&repository=acme%2Fapp" {
		t.Errorf("the server refused %d requests and was asked %q", srv.refused, srv.queries)
	}
	got := srv.byType()
	started := got["dev.qory.run.started"]
	if len(started) != 1 {
		t.Fatalf("the server's run.started: %v", started)
	}
	about, _ := json.Marshal(started[0]["about"])
	if want := `{"kind":"fix","subjects":[{"ref":"7","type":"ticket"}],"title":"Fix the parser"}`; string(about) != want {
		t.Errorf("the server's run.started about %s, want %s", about, want)
	}
	for typ, evs := range got {
		for _, ev := range evs {
			if _, ok := ev["about"]; ok && typ != "dev.qory.run.started" {
				t.Errorf("the server's %s carries about: %v", typ, ev)
			}
		}
	}
}

// TestRunRefusesAnAboutItCannotCarry pins every refusal of --kind, --title, --subject
// and --details that a run can reach, word for word: an input error, exit status 2,
// before anything starts, naming the flag and the value as given. --details - from a
// pipe is read like a file.
func TestRunRefusesAnAboutItCannotCarry(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	file := filepath.Join(root, "details.json")
	var seventeen []string
	for i := 1; i <= 17; i++ {
		seventeen = append(seventeen, "--subject", fmt.Sprintf("type=ticket,ref=%d", i))
	}
	words := "the type is not words of a-z and 0-9, each joined to the next by one space, underscore, dot or dash"
	for _, c := range []struct {
		args []string
		// details is what details.json holds; with none, there is no such file.
		details *string
		stdin   string
		want    string
	}{
		{args: []string{"--subject", ""}, want: "--subject is empty"},
		{args: []string{"--subject", "type=ticket,7"}, want: "--subject type=ticket,7: 7 is not key=value"},
		{args: []string{"--subject", "type=ticket,ref=7,owner=dev"}, want: "--subject type=ticket,ref=7,owner=dev: owner is not type, ref, url or title"},
		{args: []string{"--subject", "type=ticket,ref=7,ref=8"}, want: "--subject type=ticket,ref=7,ref=8 sets ref twice"},
		{args: []string{"--subject", "ref=7"}, want: "--subject ref=7 has no type"},
		{args: []string{"--subject", "type=ticket"}, want: "--subject type=ticket has no ref"},
		{args: []string{"--subject", "title=Crash,type=ticket,ref=7"}, want: "--subject title=Crash,type=ticket,ref=7 has no type; title= takes the rest of the value, so it comes last"},
		{args: []string{"--subject", "type=ticket,ref=7", "--subject", "type=ticket,ref=7,title=Crash"}, want: "--subject type=ticket,ref=7 and --subject type=ticket,ref=7,title=Crash have the same type and ref"},
		{args: seventeen, want: "--subject is given 17 times; a run carries at most 16"},
		{args: []string{"--details", "details.json"}, want: "--details details.json: no such file or directory"},
		{args: []string{"--details", "details.json"}, details: text(`{"a":"` + strings.Repeat("x", 1<<20) + `"}`), want: "--details details.json is larger than 1 MiB; the details are at most 8192 bytes compacted"},
		{args: []string{"--details", "details.json"}, details: text(`{"queue": "ab", }`), want: "--details details.json is not JSON at byte 17: invalid character '}' looking for beginning of object key string"},
		{args: []string{"--details", "details.json"}, details: text(""), want: "--details details.json is not JSON at byte 0: unexpected end of JSON input"},
		{args: []string{"--details", "-"}, stdin: "not json", want: "--details - is not JSON at byte 2: invalid character 'o' in literal null (expecting 'u')"},
		{args: []string{"--kind", strings.Repeat("k", 65)}, want: "--kind is longer than 64 bytes"},
		{args: []string{"--title", strings.Repeat("t", 257)}, want: "--title is longer than 256 bytes"},
		{args: []string{"--title", "Crash\x01"}, want: "--title contains a control character"},
		{args: []string{"--subject", "type=Ticket,ref=7"}, want: "--subject type=Ticket,ref=7: " + words},
		{args: []string{"--subject", "type=,ref=7"}, want: "--subject type=,ref=7: " + words},
		{args: []string{"--subject", "type=ticket,ref="}, want: "--subject type=ticket,ref=: the ref is empty"},
		{args: []string{"--subject", "type=ticket,ref=7,url=example.com/7"}, want: "--subject type=ticket,ref=7,url=example.com/7: the url is not an absolute http or https URL"},
		{args: []string{"--subject", "type=ticket,ref=7,url=HTTPS://tickets.example.com/7"}, want: "--subject type=ticket,ref=7,url=HTTPS://tickets.example.com/7: the url is not an absolute http or https URL"},
		{args: []string{"--subject", "type=ticket,ref=7,url=https://bot:pw@tickets.example.com/7"}, want: "--subject type=ticket,ref=7,url=https://bot:pw@tickets.example.com/7: the url contains a user name or password"},
		{args: []string{"--details", "details.json"}, details: text(`[{"queue":"nightly"}]`), want: "--details details.json is not a JSON object"},
		{args: []string{"--details", "details.json"}, details: text(`{"a":{"b":{"c":{"d":{}}}}}`), want: "--details details.json nests deeper than 4 levels"},
		{args: []string{"--details", "details.json"}, details: text(`{"": 1}`), want: "--details details.json has a key that is empty or longer than 64 bytes"},
		// 1411 bytes as given, each < six bytes in the event.
		{args: []string{"--details", "details.json"}, details: text(`{"note":"` + strings.Repeat("<", 1400) + `"}`), want: "--details details.json is 8411 bytes compacted; at most 8192"},
	} {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if c.details != nil {
			writeFile(t, file, *c.details)
		}
		args := append([]string{"run"}, c.args...)
		out, err := runWith(t, strings.NewReader(c.stdin), args...)
		if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != c.want {
			t.Errorf("%.120q: %v (exit %d)\nwant %s and exit %d\n%s", args, err, cmd.ExitCode(err), c.want, cmd.ExitInput, out)
		}
		if ids := recorded(t, root); len(ids) != 0 {
			t.Fatalf("%.120q: a refused run left a record", args)
		}
	}

	// From a pipe that holds an object, the details are the run's.
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := runWith(t, strings.NewReader("{\"queue\": \"nightly\"}\n"), "run", "--details", "-"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if about := rawAbouts(t, root)["dev.qory.run.started"]; about != `{"details":{"queue":"nightly"}}` {
		t.Errorf("--details - from a pipe: about %s", about)
	}
}

// text is a pointer to s, for a row that has a file of that text.
func text(s string) *string { return &s }
