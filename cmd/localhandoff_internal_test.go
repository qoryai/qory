package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// handoffPath is the pull-request output of the copy at root, which the local submit step reads.
func handoffPath(root string) string { return filepath.Join(root, handoffRel) }

// handoffIn writes content as the pull-request output of a new copy and returns the copy.
func handoffIn(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(handoffPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoffPath(root), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestThePullRequestOutputIsReadFromARegularFile is the pull-request output in each state: no
// path and no file are no output, a file with a title, a text and steps is read, its other
// members are read and not used, and a link, a directory, invalid JSON and a file over the
// limit are errors that leave no output.
func TestThePullRequestOutputIsReadFromARegularFile(t *testing.T) {
	if h, err := readHandoff(""); h != nil || err != nil {
		t.Errorf("no path: %v, %v", h, err)
	}
	if h, err := readHandoff(t.TempDir()); h != nil || err != nil {
		t.Errorf("no file: %v, %v", h, err)
	}
	good := `{"title":"Add a page","text":"The page lists orders.","steps":[{"commit":"` + commitA + `","step":"implement"}],"draft":true}`
	h, err := readHandoff(handoffIn(t, good))
	if err != nil || h == nil || h.text() == nil || h.Text.Title != "Add a page" || h.Text.Description != "The page lists orders." ||
		h.stepOf(commitA) != "implement" || h.stepOf(commitB) != stepUnreviewed || h.dropped != nil {
		t.Errorf("the file: %+v, %v", h, err)
	}
	linked := handoffIn(t, "{}")
	if err := os.Remove(handoffPath(linked)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "real.json"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(linked, "real.json"), handoffPath(linked)); err != nil {
		t.Fatal(err)
	}
	if h, err := readHandoff(linked); h != nil || err == nil {
		t.Errorf("a link: %v, %v", h, err)
	}
	dir := handoffIn(t, "{}")
	if err := os.Remove(handoffPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(handoffPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if h, err := readHandoff(dir); h != nil || err == nil {
		t.Errorf("a directory: %v, %v", h, err)
	}
	if h, err := readHandoff(handoffIn(t, "{")); h != nil || err == nil {
		t.Errorf("invalid JSON: %v, %v", h, err)
	}
	big := handoffIn(t, `{"title":"`+strings.Repeat("x", handoffMax)+`"}`)
	if h, err := readHandoff(big); h != nil || err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a file over the limit: %v, %v", h, err)
	}
}

// TestThePullRequestOutputReadsItsMembersInTheirCase is an output whose members are in
// another case: case:strict leaves each unread, so the output has no text and no steps.
func TestThePullRequestOutputReadsItsMembersInTheirCase(t *testing.T) {
	h, err := readHandoff(handoffIn(t, `{"Title":"Add a page","TEXT":"d","Steps":[{"Commit":"`+commitA+`","Step":"implement"}]}`))
	if err != nil || h == nil || h.text() != nil || h.stepOf(commitA) != stepUnreviewed {
		t.Errorf("the output is %+v, %v, want no text and no steps", h, err)
	}
	h, err = readHandoff(handoffIn(t, `{"title":"Add a page","steps":[{"commit":"`+commitA+`","STEP":"implement"}]}`))
	if err != nil || h == nil || h.stepOf(commitA) != stepUnreviewed {
		t.Errorf("a step member in another case: %+v, %v", h, err)
	}
}

// TestALinkedOutboxIsNotFollowed is a .qory/outbox and a .qory that the agent made a link to a
// folder of the host: the host's pull-request.json in that folder is not read.
func TestALinkedOutboxIsNotFollowed(t *testing.T) {
	for name, link := range map[string]string{"outbox": filepath.Join(".qory", "outbox"), ".qory": ".qory"} {
		root := t.TempDir()
		target := t.TempDir()
		inner := filepath.Join(target, "pull-request")
		if name == ".qory" {
			inner = filepath.Join(target, "outbox", "pull-request")
		}
		if err := os.MkdirAll(inner, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inner, "pull-request.json"), []byte(`{"title":"From the host"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
		if h, err := readHandoff(root); h != nil || err == nil {
			t.Errorf("a linked %s: %+v, %v, want an error and no output", name, h, err)
		}
	}
}

// TestAPullRequestTextThatTheDraftDoesNotAllowIsDropped is a title over its limit, a title of
// spaces, a description with a marker line of the draft's block, and a step key that is not a
// full lower-case commit id: each part is dropped with its line, and the rest stays.
func TestAPullRequestTextThatTheDraftDoesNotAllowIsDropped(t *testing.T) {
	for name, c := range map[string]struct{ file, line string }{
		"long title":  {`{"title":"` + strings.Repeat("é", maxTitle+1) + `","text":"d"}`, "Title: 257 characters, over the limit of 256. qory dropped the text."},
		"blank title": {`{"title":"  ‍","text":"d"}`, textDroppedBlank},
		"marker line": {`{"title":"T","text":"a\n<!-- qory:end -->\nb"}`, "Description: line 2 is a marker line of the draft's block. qory dropped the text."},
		"upper key":   {`{"title":"T","text":"d","steps":[{"commit":"` + strings.ToUpper(commitA) + `","step":"implement"}]}`, "1 step keys are not a full commit id of 40 or 64 lower-case hexadecimal digits. qory dropped them."},
	} {
		h, err := readHandoff(handoffIn(t, c.file))
		if err != nil || h == nil || len(h.dropped) != 1 || h.dropped[0] != c.line {
			t.Errorf("%s: %+v, %v, want the line %q", name, h, err, c.line)
		}
		if name == "upper key" && h != nil && h.text() == nil {
			t.Errorf("%s: the text is dropped with the step", name)
		}
	}
}
