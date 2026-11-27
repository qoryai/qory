package reviewround

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The fixture reports in testdata.
const (
	codeReport     = "testdata/code-reviewer.json"
	securityReport = "testdata/security-reviewer.json"
)

// finding is a finding of a report with the given members; the others are valid.
func finding(t *testing.T, over map[string]any) string {
	t.Helper()
	f := map[string]any{"severity": "non-blocking", "quote": "q", "problem": "p", "fix": "f",
		"proof": "broken rule: r", "point": "p"}
	for k, v := range over {
		if v == nil {
			delete(f, k)
		} else {
			f[k] = v
		}
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// report is the report of the reviewer code-reviewer with the findings and closes, which
// are JSON objects.
func report(findings []string, closes ...string) string {
	text := `{"reviewer":"code-reviewer","findings":[` + strings.Join(findings, ",") + `]`
	if closes != nil {
		text += `,"closes":[` + strings.Join(closes, ",") + `]`
	}
	return text + `}`
}

func TestParseReadsFindingsRepeatsAndCloses(t *testing.T) {
	data, err := os.ReadFile(securityReport)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse("security-reviewer", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].ID != "f2" || r.Findings[0].Severity != NonBlocking ||
		r.Findings[0].Point != "The token is read on each call." {
		t.Errorf("the findings are %+v", r.Findings)
	}
	want := []Close{
		{ID: "f3", State: Fixed, Reason: "The check of the token now runs before the first read."},
		{ID: "f4", State: Dismissed, Reason: "The builder showed that the file is a test fixture."},
	}
	if fmt.Sprint(r.Closes) != fmt.Sprint(want) {
		t.Errorf("the closes are %+v, want %+v", r.Closes, want)
	}
	data, err = os.ReadFile(codeReport)
	if err != nil {
		t.Fatal(err)
	}
	r, err = Parse("code-reviewer", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 || r.Findings[0].ID != "" || r.Findings[0].File != "internal/batch/batch.go" ||
		r.Findings[0].Line != 12 || len(r.Closes) != 0 {
		t.Errorf("the report is %+v", r)
	}
}

func TestAReportWithNoClosesIsValid(t *testing.T) {
	r, err := Parse("code-reviewer", []byte(`{"reviewer":"code-reviewer","findings":[]}`), nil)
	if err != nil || len(r.Findings) != 0 || len(r.Closes) != 0 {
		t.Errorf("the report is %+v, the error %v", r, err)
	}
}

func TestAnIdHasUpToNineDigitsAndNoLeadingZero(t *testing.T) {
	for id, valid := range map[string]bool{"f1": true, "f123456789": true, "f1234567890": false, "f0": false,
		"f01": false, "F1": false, "1": false, "f": false} {
		for where, text := range map[string]string{
			"a repeat": report([]string{finding(t, map[string]any{"id": id})}),
			"a close":  report(nil, `{"id":"`+id+`","state":"fixed","reason":"r"}`),
		} {
			_, err := Parse("code-reviewer", []byte(text), nil)
			if (err == nil) != valid {
				t.Errorf("%s with the id %q: the error is %v, valid is %v", where, id, err, valid)
			}
		}
	}
}

func TestAReportThatBreaksTheFormatIsRefusedWithItsCause(t *testing.T) {
	const format = "Reviewer code-reviewer wrote a report that does not match the report format: "
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct{ name, text, want string }{
		{"not JSON", `{"reviewer":`, "Reviewer code-reviewer wrote a report that is not one JSON object: unexpected EOF."},
		{"two values", `{"reviewer":"code-reviewer","findings":[]} {}`, "Reviewer code-reviewer wrote a report that is not one JSON object: the file continues after the JSON value."},
		{"not UTF-8", "{\"reviewer\":\"\xff\"}", "Reviewer code-reviewer wrote a report that is not UTF-8 text."},
		{"a member twice", `{"reviewer":"code-reviewer","findings":[],"findings":[]}`, "Reviewer code-reviewer wrote a report with a member twice in one object"},
		{"a member twice in a close", report(nil, `{"id":"f1","state":"fixed","state":"dismissed","reason":"r"}`), "Reviewer code-reviewer wrote a report with a member twice in one object"},
		{"result word", `{"reviewer":"code-reviewer","findings":[],"result":"clean"}`, format},
		{"bad severity", report([]string{finding(t, map[string]any{"severity": "minor"})}), format + "at '/findings/0/severity'"},
		{"blocking on a broken rule", report([]string{finding(t, map[string]any{"severity": "blocking"})}), format},
		{"a member of a finding that the format has not", report([]string{finding(t, map[string]any{"result": "x"})}), format},
		{"a file of 301 characters", report([]string{finding(t, map[string]any{"file": long(301)})}), format + "at '/findings/0/file'"},
		{"a file of two lines", report([]string{finding(t, map[string]any{"file": "a\nb"})}), format + "at '/findings/0/file'"},
		{"a NUL in the quote", report([]string{finding(t, map[string]any{"quote": "a\x00b"})}), format + "at '/findings/0/quote'"},
		{"a NUL as the first character of the proof", report([]string{finding(t, map[string]any{"proof": "broken rule: \x00a"})}), format + "at '/findings/0/proof'"},
		{"a NUL in the proof", report([]string{finding(t, map[string]any{"proof": "broken rule: a\x00b"})}), format + "at '/findings/0/proof'"},
		{"closes that is no list", `{"reviewer":"code-reviewer","findings":[],"closes":{}}`, format + "at '/closes'"},
		{"a close with no reason", report(nil, `{"id":"f1","state":"fixed"}`), format + "at '/closes/0'"},
		{"a close with an empty reason", report(nil, `{"id":"f1","state":"fixed","reason":""}`), format + "at '/closes/0/reason'"},
		{"a close with a reason of 1001 characters", report(nil, `{"id":"f1","state":"fixed","reason":"`+long(1001)+`"}`), format + "at '/closes/0/reason'"},
		{"a close with a NUL in the reason", report(nil, `{"id":"f1","state":"fixed","reason":"a\u0000b"}`), format + "at '/closes/0/reason'"},
		{"a close with the state accepted", report(nil, `{"id":"f1","state":"accepted","reason":"r"}`), format + "at '/closes/0/state'"},
		{"a close with another member", report(nil, `{"id":"f1","state":"fixed","reason":"r","by":"x"}`), format + "at '/closes/0'"},
		{"101 findings", report(many(101, finding(t, nil))), format + "at '/findings'"},
		{"101 closes", report(nil, many(101, `{"id":"f1","state":"fixed","reason":"r"}`)...), format + "at '/closes'"},
		{"other reviewer", `{"reviewer":"security-reviewer","findings":[]}`, "Reviewer code-reviewer wrote a report for the reviewer security-reviewer."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("code-reviewer", []byte(c.text), nil)
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Fatalf("the error is %v, want a text that starts %q", err, c.want)
			}
			if strings.ContainsAny(err.Error(), "\n\r") {
				t.Errorf("the error has more than one line: %q", err.Error())
			}
		})
	}
}

func TestTheErrorOfAProofThatBreaksTheFormatHasNoPartOfTheProof(t *testing.T) {
	// The key stands across the 300th character of the text that qory posts, so that a cut
	// of the text would leave a part of it that no mask of a whole key matches.
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	for _, proof := range []string{
		"nothing: " + key,
		"nothing: " + strings.Repeat("x", 200) + key + strings.Repeat("y", 100),
		"broken rule:" + key,
	} {
		_, err := Parse("code-reviewer", []byte(report([]string{finding(t, map[string]any{"proof": proof})})), nil)
		if err == nil {
			t.Fatalf("the proof %q is valid", proof)
		}
		for _, part := range []string{"sk-ant", "abcdefghij", "xxxxxxxxxx", "yyyyyyyyyy"} {
			if strings.Contains(err.Error(), part) {
				t.Errorf("the error for the proof %q has %q: %s", proof, part, err)
			}
		}
		if !strings.Contains(err.Error(), "at '/findings/0/proof': ") || !strings.Contains(err.Error(), "does not match pattern") ||
			!strings.Contains(err.Error(), "<value>") {
			t.Errorf("the error is %q, want the place, the pattern and <value> for the value", err)
		}
	}
}

// TestTheErrorOfAMemberNameThatCarriesASecretHasNoPartOfTheSecret is a report whose root
// has a member of 250 "a" and then a secret of 37 characters. The error quotes the name,
// and its cut of 300 characters falls inside the secret: the mask runs before the cut.
func TestTheErrorOfAMemberNameThatCarriesASecretHasNoPartOfTheSecret(t *testing.T) {
	const secret = "Zq7mK2vXr9TnB4wLp8Yc3HdJ6sFg1UeAo5Nit"
	if len(secret) != 37 {
		t.Fatalf("the secret has %d characters, want 37", len(secret))
	}
	data := `{"reviewer":"code-reviewer","findings":[],"` + strings.Repeat("a", 250) + secret + `":1}`
	mask := strings.NewReplacer(secret, "***").Replace
	_, err := Parse("code-reviewer", []byte(data), mask)
	if err == nil || !strings.Contains(err.Error(), "additional properties") {
		t.Fatalf("the error is %v, want one for an additional property", err)
	}
	for n := 4; n <= len(secret); n++ {
		if strings.Contains(err.Error(), secret[:n]) {
			t.Errorf("the error has the first %d characters of the secret: %s", n, err)
			break
		}
	}
	// Without the mask the cut leaves the start of the secret: the test would fail.
	_, bare := Parse("code-reviewer", []byte(data), nil)
	if bare == nil || !strings.Contains(bare.Error(), secret[:8]) {
		t.Errorf("the error with no mask is %v, want one with the start of the secret", bare)
	}
}

// many is n copies of text.
func many(n int, text string) []string {
	list := make([]string, n)
	for i := range list {
		list[i] = text
	}
	return list
}

func TestTheLimitsOfAReportIncludeTheirLastCharacter(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	full := report(many(100, finding(t, nil)), many(100, `{"id":"f1","state":"fixed","reason":"r"}`)...)
	if _, err := Parse("code-reviewer", []byte(full), nil); err != nil {
		t.Errorf("100 findings and 100 closes: %v", err)
	}
	text := report([]string{finding(t, map[string]any{"file": long(300), "quote": long(2000)})},
		`{"id":"f1","state":"dismissed","reason":"`+long(1000)+`"}`)
	if _, err := Parse("code-reviewer", []byte(text), nil); err != nil {
		t.Errorf("a file of 300 characters, a quote of 2000 and a reason of 1000: %v", err)
	}
}

func TestReadReadsTheFileBelowItsRootAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("good.json", report([]string{finding(t, nil)}))
	write("big.json", `{"reviewer":"code-reviewer","findings":[],"x":"`+strings.Repeat("a", 1<<20)+`"}`)
	if err := os.Symlink(filepath.Join(dir, "good.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := Read(dir, "good.json", "code-reviewer", nil)
	if err != nil || len(r.Findings) != 1 {
		t.Fatalf("the report is %+v, the error %v", r, err)
	}
	for name, c := range map[string]struct{ rel, want string }{
		"missing":                  {"none.json", "Reviewer code-reviewer wrote no report."},
		"link":                     {"link.json", "Reviewer code-reviewer wrote no valid report: the report file is a link, or is below a folder that is a link."},
		"folder":                   {"folder", "Reviewer code-reviewer wrote no valid report: the report file is no regular file."},
		"big":                      {"big.json", "Reviewer code-reviewer wrote no valid report: the report file has more than 1048576 bytes."},
		"a path out of the folder": {"../good.json", "Reviewer code-reviewer wrote no valid report: the report file is no clean path below an absolute, clean folder."},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Read(dir, c.rel, "code-reviewer", nil)
			if err == nil || err.Error() != c.want {
				t.Errorf("the error is %v, want %q", err, c.want)
			}
		})
	}
}

func TestCheckRefusesWhatIsNoFindingOfTheReviewer(t *testing.T) {
	owned := map[string]bool{"f1": true, "f2": true, "f3": true}
	repeat := func(id string) string { return finding(t, map[string]any{"id": id}) }
	closeOf := func(id string) string { return `{"id":"` + id + `","state":"fixed","reason":"r"}` }
	for name, c := range map[string]struct {
		text string
		want string
	}{
		"new findings only":             {report([]string{repeat("f1"), finding(t, nil)}), ""},
		"a repeat and a close":          {report([]string{repeat("f1")}, closeOf("f2"), closeOf("f3")), ""},
		"no finding at all":             {report(nil), ""},
		"a repeat of another's":         {report([]string{repeat("f9")}), "Reviewer code-reviewer's report repeats f9, which is no finding of that reviewer."},
		"a close of another's":          {report(nil, closeOf("f9")), "Reviewer code-reviewer's report closes f9, which is no finding of that reviewer."},
		"the same id repeated, closed":  {report([]string{repeat("f3")}, closeOf("f3")), "Reviewer code-reviewer's report both repeats and closes f3."},
		"an id repeated twice":          {report([]string{repeat("f1"), repeat("f1")}), "Reviewer code-reviewer's report repeats f1 more than once."},
		"an id closed twice":            {report(nil, closeOf("f2"), closeOf("f2")), "Reviewer code-reviewer's report closes f2 more than once."},
		"a close of a missing id first": {report([]string{repeat("f1")}, closeOf("f9"), closeOf("f1")), "Reviewer code-reviewer's report closes f9, which is no finding of that reviewer."},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Parse("code-reviewer", []byte(c.text), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if err := r.Check(owned); err != nil {
				got = err.Error()
			}
			if got != c.want {
				t.Errorf("the error is %q, want %q", got, c.want)
			}
		})
	}
	// A report with an id and an empty set of owned ids is invalid.
	r, err := Parse("code-reviewer", []byte(report([]string{repeat("f1")})), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Check(nil) == nil {
		t.Error("a repeat of f1 with no owned id is valid")
	}
}

func TestAPointOfSpacesIsNoValidReport(t *testing.T) {
	// The format stops a point of ASCII spaces and a point with a character that a line
	// has no room for, such as a tab or U+200B. Parse stops one of Unicode spaces or of
	// characters that the harness's plain() removes and the format passes.
	const format, trimmed = "does not match pattern", "the point has no character that shows"
	for name, c := range map[string]struct{ point, cause string }{
		"a space": {" ", format}, "a tab": {"\t", format}, "U+00A0": {"\u00A0", trimmed}, "U+00A0 and a space": {"\u00A0 ", trimmed},
		"U+200B": {"\u200B", format}, "U+3164": {"\u3164", trimmed}, "U+034F": {"\u034F", trimmed},
		"U+FEFF": {"\uFEFF", format}, "U+200B, U+3164, U+034F and U+FEFF": {"\u200B \u3164\u034F\uFEFF", format},
		"U+3164 and U+034F": {"\u3164\u034F", trimmed},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("code-reviewer", []byte(report([]string{finding(t, map[string]any{"point": c.point})})), nil)
			want := "Reviewer code-reviewer wrote a report that does not match the report format: at '/findings/0/point'"
			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), c.cause) {
				t.Errorf("the error is %v", err)
			}
		})
	}
	// The spaces around a point's text go.
	r, err := Parse("code-reviewer", []byte(report([]string{finding(t, map[string]any{"point": "\u00A0 one point \u00A0"})})), nil)
	if err != nil || len(r.Findings) != 1 || r.Findings[0].Point != "one point" {
		t.Errorf("the report is %+v, the error %v", r, err)
	}
}

// TestAPointHasTheCharacterRuleOfALine checks that a plain point and one with U+200D in
// an emoji sequence pass, and that one with U+200B and one with the tag character U+E0041
// fail the report format, as any text that is not one line of text does.
func TestAPointHasTheCharacterRuleOfALine(t *testing.T) {
	for _, c := range []struct {
		name, point string
		valid       bool
	}{
		{"a plain point", "The check is missing", true},
		{"U+200D in an emoji", "Fix \U0001F468\u200D\U0001F469 first", true},
		{"U+200B", "Fix\u200Bit", false},
		{"U+E0041", "Fix\U000E0041it", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Parse("code-reviewer", []byte(report([]string{finding(t, map[string]any{"point": c.point})})), nil)
			if c.valid && (err != nil || r.Findings[0].Point != c.point) {
				t.Errorf("the report is %+v, the error %v", r, err)
			}
			if !c.valid && (err == nil || !strings.Contains(err.Error(), "at '/findings/0/point'")) {
				t.Errorf("the error is %v, want a point that breaks the format", err)
			}
		})
	}
}

// TestPlainLineLeavesNoStoppedCharacter: for every code point that is not a surrogate, the
// line that plainLine makes of a text with it has no character that the verdict's $defs
// line stops, so the text passes the verdict schema.
func TestPlainLineLeavesNoStoppedCharacter(t *testing.T) {
	data, err := os.ReadFile("../../contracts/harness/v1/verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://qory.dev/contracts/harness/v1/verdict.schema.json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	line, err := c.Compile(url + "#/$defs/line")
	if err != nil {
		t.Fatal(err)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		if got := plainLine("a"+string(r)+"b", 500, nil); line.Validate(got) != nil {
			t.Errorf("%U: plainLine leaves %q, which the verdict line stops", r, got)
		}
	}
}

func TestHarnessPlainRemovesWhatTheHarnessRemoves(t *testing.T) {
	for in, want := range map[string]string{
		" a\u200Bb\u3164c\u034Fd\uFEFF ": "abcd", "\u2028x\u2029": "x", "a\tb": "a b",
		"\uFE0F\U000e0041\u00AD\u202E\uFFFC": "", "\u00A0point\u00A0": "point",
	} {
		if got := harnessPlain(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestHarnessPlainRemovesEachRefusedCharacterInsideText(t *testing.T) {
	if got := harnessPlain("a\u2028b\u2029c"); got != "abc" {
		t.Errorf("the line and paragraph separators inside text: %q", got)
	}
	// Each character of REFUSED_SET, inside text, as the harness's plain() removes it.
	var set []rune
	for _, span := range [][2]rune{{0x061c, 0x061c}, {0x200e, 0x200f}, {0x202a, 0x202e}, {0x2066, 0x2069},
		{0x00ad, 0x00ad}, {0x180e, 0x180e}, {0x200b, 0x200b}, {0x2060, 0x2064}, {0xfeff, 0xfeff},
		{0xfff9, 0xfffb}, {0x2028, 0x2029}} {
		for r := span[0]; r <= span[1]; r++ {
			set = append(set, r)
		}
	}
	for _, r := range set {
		if got := harnessPlain("a" + string(r) + "b"); got != "ab" {
			t.Errorf("%U inside text: %q", r, got)
		}
		if !refused(r) {
			t.Errorf("refused(%U) is false", r)
		}
	}
	// A point of refused characters alone is empty. U+2028 and U+2029 are also spaces to
	// strings.TrimSpace, and the others are Cf, so here refused() is not alone in emptying
	// it.
	_, err := Parse("code-reviewer", []byte(report([]string{finding(t, map[string]any{"point": "\u2028\u202E\u2029"})})), nil)
	if err == nil {
		t.Error("a point of refused characters is valid")
	}
}

// TestEmbeddedSchemaIsTheContract keeps the package's copy of the report format equal to
// the contract's file, byte for byte.
func TestEmbeddedSchemaIsTheContract(t *testing.T) {
	const contract = "../../contracts/harness/v1/review-report.schema.json"
	want, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("review-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/reviewround/review-report.schema.json differs from %s", contract)
	}
	if !bytes.Contains(want, []byte(`"$id": "`+reportSchemaURL+`"`)) {
		t.Errorf("%s has no $id %s", contract, reportSchemaURL)
	}
}

// TestTheReaderLinksNothingOfForager holds the package apart from Forager, so a program
// that runs no Forager session can read a report: the test binary, which links the package
// and its tests, has no module of Forager.
func TestTheReaderLinksNothingOfForager(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("the test binary has no build information")
	}
	for _, dep := range info.Deps {
		if strings.Contains(dep.Path, "forager") {
			t.Errorf("the test binary links %s", dep.Path)
		}
	}
}
