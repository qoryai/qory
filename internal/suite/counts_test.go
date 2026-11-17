package suite_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

func TestParseJUnitCountsTestcases(t *testing.T) {
	for _, c := range []struct {
		name, xml string
		want      suite.Counts
	}{
		{"all pass", `<testsuite tests="2"><testcase name="a"/><testcase name="b"></testcase></testsuite>`, suite.Counts{Tests: 2}},
		{"failure and error", `<testsuites><testsuite tests="3" failures="1" errors="1"><testcase name="a"><failure message="x"/></testcase><testcase name="b"><error/></testcase><testcase name="c"/></testsuite></testsuites>`, suite.Counts{Tests: 3, Failed: 2}},
		{"skipped", `<testsuite tests="3" skipped="2"><testcase name="a"/><testcase name="b"><skipped/></testcase><testcase name="c"><skipped message="s"/></testcase></testsuite>`, suite.Counts{Tests: 3, Skipped: 2}},
		{"failed wins over skipped", `<testsuite><testcase><skipped/><failure/></testcase></testsuite>`, suite.Counts{Tests: 1, Failed: 1}},
		{"no tests", `<testsuite tests="0"></testsuite>`, suite.Counts{}},
		{"a declared encoding", `<?xml version="1.0" encoding="ISO-8859-1"?><testsuite><testcase name="a"/></testsuite>`, suite.Counts{Tests: 1}},
		{"commented and CDATA elements count as none", `<testsuite><!-- <testcase/> --><testcase name="a"><system-out><![CDATA[<failure/>]]></system-out></testcase></testsuite>`, suite.Counts{Tests: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := suite.ParseJUnit([]byte(c.xml))
			if err != nil || got != c.want {
				t.Errorf("ParseJUnit = %+v, %v, want %+v", got, err, c.want)
			}
		})
	}
}

func TestParseJUnitRefusesWhatItCannotCount(t *testing.T) {
	for _, c := range []struct {
		name, xml string
		want      error
	}{
		{"not XML", `tests: 4 passed`, suite.ErrNotJUnit},
		{"another document", `<html><body/></html>`, suite.ErrNotJUnit},
		{"cut XML", `<testsuite><testcase name="a">`, suite.ErrNotJUnit},
		{"more tests stated", `<testsuite tests="5"><testcase/></testsuite>`, suite.ErrMismatch},
		{"more failures stated", `<testsuite tests="1" failures="1"><testcase/></testsuite>`, suite.ErrMismatch},
		{"failure outside a testcase", `<testsuite><failure/><testcase/></testsuite>`, suite.ErrMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := suite.ParseJUnit([]byte(c.xml)); !errors.Is(err, c.want) {
				t.Errorf("ParseJUnit error = %v, want %v", err, c.want)
			}
		})
	}
}

func TestCleanResultPath(t *testing.T) {
	for file, want := range map[string]bool{
		"junit.xml": true, "reports/a-b_c.d/junit.xml": true, "": false, "/etc/passwd": false,
		"../x.xml": false, "a/../b.xml": false, "./a.xml": false, "a//b.xml": false, "-a.xml": false,
		"a b.xml": false, "a\nb.xml": false, "a/": false,
	} {
		if got := suite.CleanResultPath(file); got != want {
			t.Errorf("CleanResultPath(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestReadResultReadsOnlyARegularFileBelowTheFolder(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.xml")
	write(t, outside, "<testsuite/>")
	write(t, filepath.Join(dir, "ok.xml"), "<testsuite/>")
	write(t, filepath.Join(dir, "big.xml"), strings.Repeat("x", suite.MaxResultBytes+1))
	if err := os.Symlink(outside, filepath.Join(dir, "link.xml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok.xml", filepath.Join(dir, "inside.xml")); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "x.xml"), "<testsuite/>")
	if err := os.Symlink(elsewhere, filepath.Join(dir, "folder")); err != nil {
		t.Fatal(err)
	}
	if data := suite.ReadResult(dir, "ok.xml"); string(data) != "<testsuite/>" {
		t.Errorf("ok.xml: data %q", data)
	}
	for _, file := range []string{"link.xml", "inside.xml", "big.xml", "missing.xml", "folder/x.xml", "../outside.xml", "/etc/passwd"} {
		if data := suite.ReadResult(dir, file); data != nil {
			t.Errorf("ReadResult(%q) read a file: %d bytes", file, len(data))
		}
	}
}

func write(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
