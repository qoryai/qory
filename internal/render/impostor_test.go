package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plainReviewer is the agent file of the reviewer code-reviewer.
const plainReviewer = "---\nname: code-reviewer\ndescription: Reviews code.\ntools: Read, Grep\n---\nReview.\n"

// TestAnAgentCannotTakeTheNameOfAReviewer checks that an agent that is no reviewer stops
// each writer when a YAML reader other than qory's could read the name of the reviewer
// code-reviewer from it, and that no file of it is written: a --- in its frontmatter,
// which ends the frontmatter that Claude Code reads before the name that qory reads; an
// entry name with ---; a merge key <<; a name line, at any indent, that does not give the
// entry name; and a name that is not on exactly one name line at the start of a line.
func TestAnAgentCannotTakeTheNameOfAReviewer(t *testing.T) {
	const helps = "---\nname: zz-helper\ndescription: Helps.\n"
	for name, c := range map[string]struct{ file, entry, fix string }{
		"a --- that ends the frontmatter early": {"zz-helper.md", "---\nx: [a,\nname: code-reviewer\n---]\nname: zz-helper\ndescription: Helps.\n---\nI am the helper.\n", "Remove the --- "},
		"an entry name with ---":                {"code-reviewer---x.md", "---\nname: code-reviewer---x\ndescription: Helps.\n---\nbody\n", "Remove the --- "},
		"a merge key with the name":             {"zz-helper.md", "---\nname: zz-helper\n<<: {name: code-reviewer}\ndescription: Helps.\n---\nbody\n", "Remove the << "},
		"a nested key":                          {"zz-helper.md", helps + "x:\n  name: code-reviewer\n---\nbody\n", "Set line 5 to name: zz-helper, "},
		"a quoted nested key":                   {"zz-helper.md", helps + "x:\n  - 'name' : \"code-reviewer\" # c\n---\nbody\n", "Set line 5 to name: zz-helper, "},
		"a flow mapping on two lines":           {"zz-helper.md", helps + "x: {a: b,\n  name: code-reviewer}\n---\nbody\n", "Set line 5 to name: zz-helper, "},
		"a name in a block scalar":              {"zz-helper.md", helps + "notes: |\n  name: code-reviewer\n---\nbody\n", "Set line 5 to name: zz-helper, "},
		"a name after a document end":           {"zz-helper.md", helps + "...\nname: zz-helper\n---\nbody\n", "Write name: zz-helper once, "},
		"a name on no name line":                {"zz-helper.md", "---\n\"nam\\x65\": zz-helper\ndescription: Helps.\n---\nbody\n", "Write name: zz-helper once, "},
	} {
		res := agentFixture(t, plainReviewer, map[string]string{c.file: c.entry}, nil)
		entry := strings.TrimSuffix(c.file, ".md")
		path := agentPath(res, entry)
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			err := w.run(res, dir)
			if err == nil {
				t.Errorf("%s, %s: no error", name, w.name)
				continue
			}
			if line, _, _ := strings.Cut(err.Error(), "\n"); !strings.HasPrefix(line, c.fix) || !strings.Contains(line, " agent "+path+",") {
				t.Errorf("%s, %s: line 1 is %q, want %q and the fix for %s", name, w.name, line, c.fix, path)
			}
			for _, f := range w.files(entry) {
				if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
					t.Errorf("%s, %s: the build wrote %s", name, w.name, f)
				}
			}
		}
	}
}

// TestAnAgentWithTheEntryNameBuilds checks that a name line in a form that every reader
// reads as the entry name, and a name line in the body, below the closing fence, build.
func TestAnAgentWithTheEntryNameBuilds(t *testing.T) {
	for name, entry := range map[string]string{
		"quotes and a comment": "---\n\"name\" : 'zz-helper' # the helper\ndescription: Helps.\n---\nbody\n",
		"a name in the body":   "---\nname: zz-helper\ndescription: Helps.\n---\nname: code-reviewer\n<<: x\n",
	} {
		res := agentFixture(t, plainReviewer, map[string]string{"zz-helper.md": entry}, nil)
		for _, w := range agentWrites(t) {
			if err := w.run(res, t.TempDir()); err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
			}
		}
	}
}

// TestAReviewerWithAMergeKeyStops checks that a << in the frontmatter of a reviewer stops
// each writer, also inside a quoted value, which qory reads as no merge key.
func TestAReviewerWithAMergeKeyStops(t *testing.T) {
	res := agentFixture(t, "---\nname: code-reviewer\ndescription: \"Reviews <<: code.\"\n---\nReview.\n", nil, nil)
	path := agentPath(res, "code-reviewer")
	for _, w := range agentWrites(t) {
		err := w.run(res, t.TempDir())
		want := "Remove the << from the frontmatter of the reviewer agent " + path + ", then run again"
		if err == nil || !strings.HasPrefix(err.Error(), want+"\n") {
			t.Errorf("%s: %v, want %q on line 1", w.name, err, want)
		}
	}
}
