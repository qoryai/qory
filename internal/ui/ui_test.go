package ui_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// plain returns a UI writing to buf with colour off, so an assertion compares the text and
// not the escape sequences a terminal would get.
func plain(t *testing.T, buf *bytes.Buffer) *ui.UI {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	return ui.New(buf)
}

// TestMarkedReportsWhetherATitleWasPrinted runs before every other test in this package,
// because the flag Marked reads is package state that no call resets: the first Title in the
// test binary sets it for good.
func TestMarkedReportsWhetherATitleWasPrinted(t *testing.T) {
	if ui.Marked() {
		t.Fatal("a title was printed before this test ran")
	}
	var buf bytes.Buffer
	plain(t, &buf).Title("qory")
	if !ui.Marked() {
		t.Fatal("Marked is false after a title was printed")
	}
}

// TestTitlePrintsTheMarkTheNameAndTheRest covers the middle dot: it appears once before the
// rest, and not at all when the rest is empty.
func TestTitlePrintsTheMarkTheNameAndTheRest(t *testing.T) {
	cases := []struct {
		name string
		rest []string
		want string
	}{
		{"the rest follows a middle dot", []string{"claude opus"}, ui.Mark + " app · claude opus\n"},
		{"an empty rest prints no dot", nil, ui.Mark + " app\n"},
		{"an empty part prints no dot", []string{""}, ui.Mark + " app\n"},
		{"several parts share one dot", []string{"claude", "opus"}, ui.Mark + " app · claude opus\n"},
		{"empty parts are skipped", []string{"", "claude", ""}, ui.Mark + " app · claude\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			plain(t, &buf).Title("app", c.rest...)
			if got := buf.String(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestHelpersRenderTheirLineShape pins the shape of every line qory writes: the leading
// icon, the indent, and the alignment.
func TestHelpersRenderTheirLineShape(t *testing.T) {
	cases := []struct {
		name  string
		print func(*ui.UI)
		want  string
	}{
		{
			"Success starts with a check mark",
			func(u *ui.UI) { u.Success("linked %d files", 3) },
			"✓ linked 3 files\n",
		},
		{
			"Fail starts with a cross",
			func(u *ui.UI) { u.Fail(errors.New("no profile")) },
			"✗ no profile\n",
		},
		{
			"Fail aligns the later lines of an error under the first",
			func(u *ui.UI) { u.Fail(errors.New("collision\nskills/review\nkeep one")) },
			"✗ collision\n  skills/review\n  keep one\n",
		},
		{
			"Heading prints the text on its own line",
			func(u *ui.UI) { u.Heading("Layers") },
			"Layers\n",
		},
		{
			"Fields aligns the keys",
			func(u *ui.UI) { u.Fields([][2]string{{"file", "harness-compose.yaml"}, {"checkout", "app"}}) },
			"  file      harness-compose.yaml\n  checkout  app\n",
		},
		{
			"Table aligns every column and drops the trailing padding",
			func(u *ui.UI) { u.Table([][]string{{"core", "working-tree"}, {"reviewers", "dirty"}}) },
			"  core       working-tree\n  reviewers  dirty\n",
		},
		{
			"Table takes a short row",
			func(u *ui.UI) { u.Table([][]string{{"core", "working-tree"}, {"reviewers"}}) },
			"  core       working-tree\n  reviewers\n",
		},
		{
			"Text indents prose by two",
			func(u *ui.UI) { u.Text("one", "two") },
			"  one\n  two\n",
		},
		{
			"Code indents a pasteable block by four",
			func(u *ui.UI) { u.Code("qory harness compose") },
			"    qory harness compose\n",
		},
		{
			"Blank prints one empty line",
			func(u *ui.UI) { u.Blank() },
			"\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			c.print(plain(t, &buf))
			if got := buf.String(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestShortShortensAPathAgainstARoot covers the three answers Short gives: relative to the
// base, under ~, or the path as it came.
func TestShortShortensAPathAgainstARoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		name string
		path string
		base string
		want string
	}{
		{"a path under the base is relative to it", "/work/app/.qory/harness", "/work/app", ".qory/harness"},
		{"the base itself is a dot", "/work/app", "/work/app", "."},
		{"an unrelated path is left alone", "/elsewhere/layers/core", "/work/app", "/elsewhere/layers/core"},
		{"an empty base skips the base", "/work/app", "", "/work/app"},
		{"a path under the home directory shows a tilde", filepath.Join(home, "code", "app"), "", "~" + string(filepath.Separator) + filepath.Join("code", "app")},
		{"the home directory itself is left alone", home, "", home},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ui.Short(c.path, c.base); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestMarkIsStable guards the three marks the output and the docs both name.
func TestMarkIsStable(t *testing.T) {
	if ui.Mark != "🐝" || ui.Jar != "🫙" || ui.Pot != "🍯" {
		t.Errorf("marks changed: %q %q %q", ui.Mark, ui.Jar, ui.Pot)
	}
}
