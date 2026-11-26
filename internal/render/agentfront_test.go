package render_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// agentFixture composes the fixture workflow-review-group with the file of the agent
// code-reviewer written as text, and the agents of extra written beside it, by file name.
// When tiers is not nil, it maps the tiers.
func agentFixture(t *testing.T, text string, extra map[string]string, tiers map[string]stack.Tier) *compose.Result {
	t.Helper()
	hermetic(t)
	src, err := filepath.Abs("../../contracts/harness/v1/fixtures/workflow-review-group")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(dir, "stack", "modules", "core", "agents")
	if err := os.WriteFile(filepath.Join(agents, "code-reviewer.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range extra {
		if err := os.WriteFile(filepath.Join(agents, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := config.Compose(filepath.Join(dir, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base, Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// agentWrite is one way to write the agent files of Claude Code: its name, what it
// returns, and the files that it writes for the agent called name, relative to its folder.
type agentWrite struct {
	name  string
	run   func(res *compose.Result, dir string) error
	files func(name string) []string
}

var agentWrites = func(t *testing.T) []agentWrite {
	return []agentWrite{
		{"PlaceEntries", func(res *compose.Result, dir string) error {
			return render.PlaceEntries(lookup(t, "claude"), res, dir, render.PluginAddress, "agents")
		}, func(n string) []string { return []string{"agents/" + n + ".md"} }},
		{"WritePlugin", func(res *compose.Result, dir string) error {
			return render.WritePlugin(lookup(t, "claude"), res, dir, ".claude-plugin", nil)
		}, func(n string) []string { return []string{"agents/" + n + ".md"} }},
		{"Build", func(res *compose.Result, dir string) error {
			return render.Build(res, dir, lookup(t, "claude"))
		}, func(n string) []string {
			return []string{"claude/agents/" + n + ".md", "claude/plugin/agents/" + n + ".md"}
		}},
	}
}

// agentPath is the path of the entry of the agent name.
func agentPath(res *compose.Result, name string) string {
	for _, e := range res.Entries {
		if e.Kind == "agents" && e.Name == name {
			return e.Path
		}
	}
	return ""
}

// jsSpace is what JavaScript's \s matches.
const jsSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

// claudeFront is how Claude Code 2.1.332 takes the frontmatter of an agent file.
var claudeFront = regexp.MustCompile(`(?s)^---` + jsSpace + `*\n(.*?)---` + jsSpace + `*\n?`)

// claudeTools is a tool list as the function Zf of Claude Code 2.1.332 reads it: split
// on commas and U+0020 outside parentheses, with no count of how deep, each item trimmed
// as JavaScript's trim() trims and an empty one dropped.
func claudeTools(items ...string) []string {
	jsSpace := regexp.MustCompile("^" + jsSpace + "$")
	trim := func(s string) string {
		return strings.TrimFunc(s, func(r rune) bool { return jsSpace.MatchString(string(r)) })
	}
	var out []string
	for _, s := range items {
		inside, cur := false, ""
		flush := func() {
			if trim(cur) != "" {
				out = append(out, trim(cur))
			}
			cur = ""
		}
		for _, r := range s {
			switch {
			case r == '(':
				inside, cur = true, cur+"("
			case r == ')':
				inside, cur = false, cur+")"
			case !inside && r == ',':
				flush()
			case !inside && r == ' ':
				if trim(cur) != "" {
					flush()
				}
			case cur != "" || trim(string(r)) != "":
				cur += string(r)
			}
		}
		flush()
	}
	return out
}

// listOf is the entries of a tools value that YAML decoded: a string or a list.
func listOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, i := range x {
			out = append(out, i.(string))
		}
		return out
	}
	return nil
}

// claudeReads fails the test unless Claude Code reads the agent file text with no Bash
// and no * in its tools, and Bash and Agent as the first two elements of
// disallowedTools, with no *, and returns the keys.
func claudeReads(t *testing.T, label, text string) map[string]any {
	t.Helper()
	m := claudeFront.FindStringSubmatch(text)
	if m == nil {
		t.Errorf("%s: Claude Code reads no frontmatter in\n%s", label, text)
		return nil
	}
	var keys map[string]any
	if err := yaml.Unmarshal([]byte(m[1]), &keys); err != nil {
		t.Errorf("%s: the frontmatter does not parse: %v\n%s", label, err, m[1])
		return nil
	}
	for _, view := range [][]string{claudeTools(listOf(keys["tools"])...), claudeTools(strings.Join(listOf(keys["tools"]), ","))} {
		for _, tool := range view {
			if tool == "Bash" || tool == "*" || strings.HasPrefix(tool, "Bash(") {
				t.Errorf("%s: Claude Code gives the agent the tool %q in\n%s", label, tool, m[1])
			}
		}
	}
	denied := listOf(keys["disallowedTools"])
	if got := claudeTools(denied...); len(got) < 2 || got[0] != "Bash" || got[1] != "Agent" || slices.Contains(got, "*") {
		t.Errorf("%s: Claude Code reads disallowedTools as %q, want Bash and Agent first and no * in\n%s", label, got, m[1])
	}
	if len(denied) < 2 || denied[0] != "Bash" || denied[1] != "Agent" {
		t.Errorf("%s: disallowedTools is %q, want Bash and Agent as the first items in\n%s", label, denied, m[1])
	}
	return keys
}

const reviewerBody = "\nReviews a change.\n"

// reviewerFormsThatStop are code-reviewer files whose frontmatter qory does not rewrite:
// each form lets Claude Code end the frontmatter earlier than qory does, or reads it in
// another way than qory does.
var reviewerFormsThatStop = map[string]string{
	"--- inside a value":        "---\nname: code-reviewer\ndescription: Reviews a change --- strictly\ntools: Read, Bash\n---\n" + reviewerBody,
	"--- in a comment":          "---\nname: code-reviewer\n# a note --- here\ntools: Read, Bash\n---\n" + reviewerBody,
	"--- in a block":            "---\nname: code-reviewer\ndescription: |\n  Reviews a change\n  ---\n  strictly\ntools: Read, Bash\n---\n" + reviewerBody,
	"an indented ---":           "---\nname: code-reviewer\ndescription: >\n  Reviews\n  ---\ntools: Read, Bash\n---\n" + reviewerBody,
	"a flow mapping":            "---\n{name: code-reviewer, description: x, tools: \"Read, Bash\"}\n---\n" + reviewerBody,
	"a ... document":            "---\nname: code-reviewer\ntools: Read\n...\ntools: Read, Bash\n---\n" + reviewerBody,
	"a merge key":               "---\nname: code-reviewer\ntools: Read\n<<: {disallowedTools: Bash}\n---\n" + reviewerBody,
	"a key written twice":       "---\nname: code-reviewer\nname: x\ntools: Read, Bash\n---\n" + reviewerBody,
	"not closed":                "---\nname: code-reviewer\ntools: Read, Bash\n" + reviewerBody,
	"a list value":              "---\nname: code-reviewer\ntools: Read\ncolor:\n  k: v\n---\n" + reviewerBody,
	"tools only Bash":           "---\nname: code-reviewer\ntools: Bash\n---\n" + reviewerBody,
	"a null value":              "---\nname: code-reviewer\ncolor:\ntools: Read\n---\n" + reviewerBody,
	"a float value":             "---\nname: code-reviewer\nmaxTurns: 1.5\ntools: Read\n---\n" + reviewerBody,
	"an anchor":                 "---\nname: code-reviewer\ncolor: &a x\ntools: Read\n---\n" + reviewerBody,
	"spaces after a fence":      "--- \nname: code-reviewer\ntools: Read, Bash\n---\n" + reviewerBody,
	"a spaced fence with paths": "--- \nname: code-reviewer\npaths: [\"**/*.go\"]\ntools: Read, Bash\n---\n" + reviewerBody,
	"a tab after a fence":       "---\t\nname: code-reviewer\ntools: Read, Bash\n---\n" + reviewerBody,
	"another name":              "---\nname: other\ntools: Read\n---\n" + reviewerBody,
	"no frontmatter":            reviewerBody,
	"an unclosed rule":          "---\nname: code-reviewer\ndisallowedTools:\n  - Grep(a\n---\n" + reviewerBody,
	"a comma in a rule":         "---\nname: code-reviewer\ntools: Read, Bash(a, b)\n---\n" + reviewerBody,
	"a * in a rule":             "---\nname: code-reviewer\ntools: Read, Bash(git log *)\n---\n" + reviewerBody,
	"a denied *":                "---\nname: code-reviewer\ndisallowedTools: \"Write, *\"\n---\n" + reviewerBody,
	"a tab":                     "---\nname: code-reviewer\ntools:\tRead\n---\n" + reviewerBody,
	"a character beyond ASCII":  "---\nname: code-reviewer\ndescription: Prüft\n---\n" + reviewerBody,
	"a comment beyond ASCII":    "---\nname: code-reviewer\n# Prüft\n---\n" + reviewerBody,
	"a tab in a comment":        "---\nname: code-reviewer\n# a\tb\n---\n" + reviewerBody,
	"an escape beyond ASCII":    "---\nname: code-reviewer\ndescription: \"a\\u0085b\"\n---\n" + reviewerBody,
	"an escaped letter":         "---\nname: code-reviewer\ndescription: \"caf\\u00e9\"\n---\n" + reviewerBody,
	"an int with a zero":        "---\nname: code-reviewer\nmaxTurns: 007\n---\n" + reviewerBody,
	"a hex int":                 "---\nname: code-reviewer\nmaxTurns: 0x1F\n---\n" + reviewerBody,
	"a bool as yes":             "---\nname: code-reviewer\ncolor: yes\n---\n" + reviewerBody,
	"a bool as True":            "---\nname: code-reviewer\ncolor: True\n---\n" + reviewerBody,
}

// TestReviewerFrontmatterFormsStopTheBuild checks that PlaceEntries, WritePlugin and Build
// each stop for a reviewer file of each form, with the fix on line 1 and the path of the
// agent in it, and write no file of that agent.
func TestReviewerFrontmatterFormsStopTheBuild(t *testing.T) {
	for name, text := range reviewerFormsThatStop {
		res := agentFixture(t, text, nil, threeTiers)
		path := agentPath(res, "code-reviewer")
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			err := w.run(res, dir)
			if err == nil {
				t.Errorf("%s, %s: no error", name, w.name)
				continue
			}
			line, _, _ := strings.Cut(err.Error(), "\n")
			wants := []string{
				"Fix the frontmatter of the reviewer agent " + path + ", then run again",
				"Remove the spaces after --- on line 1 of the reviewer agent " + path + ", then run again",
				"Set name to code-reviewer in the agent " + path + ", or rename the file, then run again",
			}
			found := false
			for _, want := range wants {
				found = found || line == want
			}
			if !found {
				t.Errorf("%s, %s: line 1 is %q, want a fix and the path %s", name, w.name, line, path)
			}
			if len(line) > 100+len(path) {
				t.Errorf("%s, %s: line 1 has %d characters", name, w.name, len(line))
			}
			for _, f := range w.files("code-reviewer") {
				if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
					t.Errorf("%s, %s: the build wrote %s", name, w.name, f)
				}
			}
		}
	}
}

// TestReviewerFrontmatterThatQoryWritesHasNoBash checks that qory writes the frontmatter
// of a reviewer: Claude Code reads no Bash in tools and Bash first in disallowedTools,
// each other key keeps its value, and the body is unchanged.
func TestReviewerFrontmatterThatQoryWritesHasNoBash(t *testing.T) {
	const body = "\nReviews a change.\n\n--- is a rule in the body.\n"
	for name, c := range map[string]struct{ in, front string }{
		"tools split by spaces": {
			"---\nname: code-reviewer\ntools: Read Bash Grep\n---\n",
			"name: code-reviewer\ntools:\n  - Read\n  - Grep\ndisallowedTools:\n  - Bash\n  - Agent",
		},
		"a Bash rule denied": {
			"---\nname: code-reviewer\ntools: Read, Bash(git), Grep\ndisallowedTools: Bash(push)\n---\n",
			"name: code-reviewer\ntools:\n  - Read\n  - Grep\ndisallowedTools:\n  - Bash\n  - Agent\n  - \"Bash(push)\"",
		},
		"a tool named Bashful": {
			"---\nname: code-reviewer\ntools: [Bashful, Read]\ndisallowedTools: [Bash, Edit, Bash]\n---\n",
			"name: code-reviewer\ntools:\n  - Bashful\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n  - Edit",
		},
		"no frontmatter keys but a name": {
			"---\nname: code-reviewer\n---\n",
			"name: code-reviewer\ndisallowedTools:\n  - Bash\n  - Agent",
		},
		"no name": {
			"---\ndescription: \"Reviews: a change\"\ntools: Read\n---\n",
			"name: code-reviewer\ndescription: \"Reviews: a change\"\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent",
		},
		"a spaced fence at the end": {
			"---\nname: code-reviewer\ntools: Read, Bash\n--- \n",
			"name: code-reviewer\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent",
		},
	} {
		res := agentFixture(t, c.in+body, nil, nil)
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			if err := w.run(res, dir); err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
				continue
			}
			for _, f := range w.files("code-reviewer") {
				got, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				want := "---\n" + c.front + "\n---\n" + body
				if string(got) != want {
					t.Errorf("%s, %s: %s is\n%s\nwant\n%s", name, w.name, f, got, want)
				}
				claudeReads(t, name+", "+w.name+", "+f, string(got))
			}
		}
	}
}

// TestEveryClaudeAgentHasItsNameOrStopsTheBuild checks that an agent, reviewer or not,
// with a frontmatter name other than its entry name stops each writer, and that an
// agent with no name gets its entry name.
func TestEveryClaudeAgentHasItsNameOrStopsTheBuild(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	for name, c := range map[string]struct {
		other string
		stop  bool
	}{
		"the name of a reviewer":  {"---\nname: code-reviewer\ndescription: o\ntools: Read, Bash\n---\nBody.\n", true},
		"a name that is a number": {"---\nname: 5\n---\nBody.\n", true},
		"its name":                {"---\nname: other\ndescription: o\n---\nBody.\n", false},
		"no name":                 {"---\ndescription: o\n---\nBody.\n", false},
		"no frontmatter":          {"Body.\n", false},
	} {
		res := agentFixture(t, reviewer, map[string]string{"other.md": c.other}, nil)
		path := agentPath(res, "other")
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			err := w.run(res, dir)
			want := "Set name to other in the agent " + path + ", or rename the file, then run again"
			if c.stop {
				if err == nil {
					t.Errorf("%s, %s: no error", name, w.name)
				} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
					t.Errorf("%s, %s: line 1 is %q, want %q", name, w.name, line, want)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
				continue
			}
			for _, f := range w.files("other") {
				got, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				m := claudeFront.FindStringSubmatch(string(got))
				var keys map[string]any
				if m == nil || yaml.Unmarshal([]byte(m[1]), &keys) != nil || strings.TrimSpace(keys["name"].(string)) != "other" {
					t.Errorf("%s, %s: %s has no name other:\n%s", name, w.name, f, got)
				}
			}
		}
	}
}

// TestReviewerKeepsItsOtherKeys checks that a reviewer keeps description, effort,
// maxTurns and color as the source has them, and the model: a tier placeholder is
// expanded as for any agent, to the mapped model id, before the frontmatter is written,
// and a model id stays.
func TestReviewerKeepsItsOtherKeys(t *testing.T) {
	const keys = "description: \"Reviews: a change, \\\"fully\\\" - it's #1\"\neffort: high\nmaxTurns: 12\ncolor: blue\n"
	for name, c := range map[string]struct {
		model, want string
	}{
		"a tier":     {"${qory:thinking}", "example-big-model"},
		"a model id": {"example-fixed-model", "example-fixed-model"},
	} {
		src := "---\nname: code-reviewer\n" + keys + "model: " + c.model + "\ntools: Read, Bash\n---\n" + reviewerBody
		res := agentFixture(t, src, nil, threeTiers)
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			if err := w.run(res, dir); err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
				continue
			}
			for _, f := range w.files("code-reviewer") {
				data, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				got := claudeReads(t, name+", "+w.name+", "+f, string(data))
				var source map[string]any
				if err := yaml.Unmarshal([]byte(strings.SplitN(src, "---\n", 3)[1]), &source); err != nil {
					t.Fatal(err)
				}
				for _, k := range []string{"description", "effort", "maxTurns", "color"} {
					if !reflect.DeepEqual(got[k], source[k]) {
						t.Errorf("%s, %s, %s: %s is %#v, want %#v", name, w.name, f, k, got[k], source[k])
					}
				}
				if got["model"] != c.want {
					t.Errorf("%s, %s, %s: model is %#v, want %q", name, w.name, f, got["model"], c.want)
				}
				if f := strings.Contains(string(data), "${qory:"); f {
					t.Errorf("%s, %s: the file keeps a placeholder:\n%s", name, w.name, data)
				}
			}
		}
	}
}

// TestAgentWithASpacedFenceStopsTheBuild checks that an agent that is no reviewer, with
// spaces after its opening fence, stops each writer: qory reads no frontmatter in it and
// would write a second one in front, which Claude Code reads in place of the first.
func TestAgentWithASpacedFenceStopsTheBuild(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	res := agentFixture(t, reviewer, map[string]string{"other.md": "--- \ndescription: o\ntools: Read\n---\nBody.\n"}, nil)
	path := agentPath(res, "other")
	for _, w := range agentWrites(t) {
		err := w.run(res, t.TempDir())
		want := "Remove the spaces after --- on line 1 of the agent " + path + ", then run again"
		if err == nil {
			t.Errorf("%s: no error", w.name)
		} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
			t.Errorf("%s: line 1 is %q, want %q", w.name, line, want)
		}
	}
}

// TestAnAgentThatNeedsNoChangeStaysALink checks that PlaceEntries links an agent whose
// file has its entry name, with no tier and no paths, and writes one with no name.
func TestAnAgentThatNeedsNoChangeStaysALink(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	res := agentFixture(t, reviewer, map[string]string{
		"named.md":   "---\nname: named\n---\nBody.\n",
		"unnamed.md": "---\ndescription: o\n---\nBody.\n",
	}, nil)
	dir := t.TempDir()
	if err := render.PlaceEntries(lookup(t, "claude"), res, dir, render.PluginAddress, "agents"); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]bool{"named": true, "unnamed": false, "explorer": true, "code-reviewer": false} {
		fi, err := os.Lstat(filepath.Join(dir, "agents", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode()&os.ModeSymlink != 0; got != link {
			t.Errorf("agents/%s.md is a link: %v, want %v", name, got, link)
		}
	}
}

// TestAnAgentWithAJavaScriptSpaceAfterTheFenceStopsTheBuild checks that an agent, reviewer
// or not, whose line 1 is the fence followed by a character that JavaScript's \s matches
// stops each writer with the fix for a spaced fence: Claude Code reads the frontmatter
// after that line, and qory reads none.
func TestAnAgentWithAJavaScriptSpaceAfterTheFenceStopsTheBuild(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	for name, after := range map[string]string{
		"a no-break space":     "\u00a0",
		"a vertical tab":       "\v",
		"a form feed":          "\f",
		"a line separator":     "\u2028",
		"a byte order mark":    "\ufeff",
		"a space before CR LF": " \r",
		"two CRs":              "\r\r",
	} {
		for _, c := range []struct{ kind, entry, file, code string }{
			{"agent", "other", "---" + after + "\ndescription: o\ntools: Read\n---\nBody.\n", reviewer},
			{"reviewer agent", "code-reviewer", "", "---" + after + "\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody},
		} {
			extra := map[string]string{}
			if c.file != "" {
				extra["other.md"] = c.file
			}
			res := agentFixture(t, c.code, extra, nil)
			path := agentPath(res, c.entry)
			for _, w := range agentWrites(t) {
				err := w.run(res, t.TempDir())
				want := "Remove the spaces after --- on line 1 of the " + c.kind + " " + path + ", then run again"
				if err == nil {
					t.Errorf("%s, %s, %s: no error", name, c.kind, w.name)
				} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
					t.Errorf("%s, %s, %s: line 1 is %q, want %q", name, c.kind, w.name, line, want)
				}
			}
		}
	}
}

// TestAClaudeAgentHasOneFrontmatterForEveryReader checks the agents that are no reviewer:
// one whose line 1 starts with --- and has more on it, one with a --- in its frontmatter
// or in its name, and one whose entry name has a --- stop each writer with the fix on
// line 1. A file with no frontmatter, one with two byte order marks in front of the fence
// and one with CRLF line ends build, and Claude Code reads the name that qory reads.
func TestAClaudeAgentHasOneFrontmatterForEveryReader(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	line1 := func(path string) string { return "Write --- alone on line 1 of the agent " + path + ", then run again" }
	inFront := func(path string) string {
		return "Remove the --- from the frontmatter of the agent " + path + ", then run again"
	}
	for name, c := range map[string]struct {
		file, text string
		stop       func(path string) string
	}{
		"text after the fence":      {"other.md", "---x\ndescription: o\n---\nBody.\n", line1},
		"four dashes":               {"other.md", "----\ndescription: o\n---\nBody.\n", line1},
		"a line separator and text": {"other.md", "---\u2028name: code-reviewer\ndescription: o\n---\nBody.\n", line1},
		"a --- in a value":          {"other.md", "---\ndescription: a --- b\n---\nBody.\n", inFront},
		"a --- in a comment":        {"other.md", "---\n# ---\ndescription: o\n---\nBody.\n", inFront},
		"a --- that ends it early":  {"other.md", "---\nx: [a,\nname: code-reviewer\n---]\nname: other\n---\nBody.\n", inFront},
		"a --- in an escaped name":  {"o---x.md", "---\nname: \"o\\x2d--x\"\n---\nBody.\n", inFront},
		"a --- in the entry name": {"o---x.md", "---\ndescription: o\n---\nBody.\n", func(path string) string {
			return "Rename the agent " + path + " to a name with no ---, then run again"
		}},
		"no frontmatter":       {"other.md", "# Title\n\nBody.\n", nil},
		"two byte order marks": {"other.md", "\ufeff\ufeff---\nname: code-reviewer\n---\nBody.\n", nil},
		"CRLF line ends":       {"other.md", "---\r\ndescription: o\r\n---\r\nBody.\r\n", nil},
	} {
		res := agentFixture(t, reviewer, map[string]string{c.file: c.text}, nil)
		entry := strings.TrimSuffix(c.file, ".md")
		path := agentPath(res, entry)
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			err := w.run(res, dir)
			if c.stop != nil {
				if err == nil {
					t.Errorf("%s, %s: no error", name, w.name)
				} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != c.stop(path) {
					t.Errorf("%s, %s: line 1 is %q, want %q", name, w.name, line, c.stop(path))
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
				continue
			}
			for _, f := range w.files(entry) {
				got, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				m := claudeFront.FindStringSubmatch(strings.TrimPrefix(string(got), "\ufeff"))
				var keys map[string]any
				if m == nil || yaml.Unmarshal([]byte(m[1]), &keys) != nil || keys["name"] != entry {
					t.Errorf("%s, %s: Claude Code reads no name %s in %s:\n%q", name, w.name, entry, f, got)
				}
			}
		}
	}
}

// TestAnAgentFrontmatterOver64KiBStopsTheBuild checks the cap of the frontmatter, which
// stops before the parse: a frontmatter of exactly 64 KiB passes each writer, for the
// reviewer and for any other agent, and SplitAgent stops one byte more with the kind of
// the file. The compose stops such a file first, see module.MaxAgentFront.
func TestAnAgentFrontmatterOver64KiBStopsTheBuild(t *testing.T) {
	front := func(size int) string {
		line := "description: "
		return "---\n" + line + strings.Repeat("a", size-len(line)) + "\n---\n" + reviewerBody
	}
	src, err := render.SplitAgent([]byte(front(render.MaxAgentFront)), "agents/a.md", render.AgentFile)
	if err != nil || len(src.Front) != render.MaxAgentFront {
		t.Fatalf("a frontmatter of 64 KiB: %d bytes, %v", len(src.Front), err)
	}
	// A frontmatter that is no YAML gets the error of the cap, which comes before the parse.
	noYAML := "---\nx: [no YAML\n" + strings.TrimPrefix(front(render.MaxAgentFront+1-len("x: [no YAML\n")), "---\n")
	for _, kind := range []string{render.AgentFile, render.ReviewerAgent} {
		for name, text := range map[string]string{"YAML": front(render.MaxAgentFront + 1), "no YAML": noYAML} {
			_, err := render.SplitAgent([]byte(text), "agents/a.md", kind)
			want := "Shorten the frontmatter of the " + kind + " agents/a.md to 64 KiB or less, then run again"
			if err == nil {
				t.Errorf("%s, %s over 64 KiB: no error", kind, name)
			} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
				t.Errorf("%s, %s over 64 KiB: line 1 is %q, want %q", kind, name, line, want)
			}
		}
	}
	if _, err := render.SplitAgent([]byte(strings.Replace(noYAML, strings.Repeat("a", 10), "", 1)), "agents/a.md", render.AgentFile); err == nil ||
		!strings.HasPrefix(err.Error(), "Fix the frontmatter of the agent agents/a.md") {
		t.Errorf("no YAML within 64 KiB: %v, want the error of the parse", err)
	}
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	for name, c := range map[string]struct {
		reviewer string
		extra    map[string]string
	}{
		"an agent of 64 KiB":   {reviewer, map[string]string{"other.md": front(render.MaxAgentFront)}},
		"a reviewer of 64 KiB": {front(render.MaxAgentFront), nil},
	} {
		res := agentFixture(t, c.reviewer, c.extra, nil)
		for _, w := range agentWrites(t) {
			if err := w.run(res, t.TempDir()); err != nil {
				t.Errorf("%s, %s: %v", name, w.name, err)
			}
		}
	}
}

// noGuard is a runtime that cannot deny an agent its commands: it is no
// [render.CommandGuard].
type noGuard struct{}

func (noGuard) Name() string                                 { return "no-guard" }
func (noGuard) Render(*compose.Result, string, string) error { return nil }
func (noGuard) Links(*compose.Result) []render.Link          { return nil }
func (noGuard) Skips() []string                              { return nil }
func (noGuard) Reserved() []render.Reserved                  { return nil }

// TestAReviewerForARuntimeWithNoGuardStopsTheBuild checks that PlaceEntries, for a
// runtime that is no guard, stops at a reviewer: qory cannot write it so that it runs no
// command.
func TestAReviewerForARuntimeWithNoGuardStopsTheBuild(t *testing.T) {
	res := agentFixture(t, "---\nname: code-reviewer\ntools: Read\n---\n"+reviewerBody, nil, nil)
	err := render.PlaceEntries(noGuard{}, res, t.TempDir(), render.PluginAddress, "agents")
	want := "Compose the reviewer agent " + agentPath(res, "code-reviewer") + " for a runtime that can deny it commands, then run again"
	if err == nil {
		t.Fatal("no error")
	}
	if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
		t.Errorf("line 1 is %q, want %q", line, want)
	}
}

// TestAReviewerGetsItsLineForTheStopsOfEveryAgent checks the line 1 that a reviewer gets
// for each form that the stops of every agent cover: the checks of a reviewer run first,
// so a line 1 that is not ---, a --- and a merge key get the line of a reviewer, and the
// other stops keep their line, with reviewer agent in place of agent.
func TestAReviewerGetsItsLineForTheStopsOfEveryAgent(t *testing.T) {
	res := agentFixture(t, "---\nname: code-reviewer\n---\n"+reviewerBody, nil, nil)
	var entry compose.Entry
	for _, e := range res.Entries {
		if e.Kind == "agents" && e.Name == "code-reviewer" {
			entry = e
		}
	}
	path := entry.Path
	fix := "Fix the frontmatter of the reviewer agent " + path + ", then run again"
	for name, c := range map[string]struct{ entry, in, line1 string }{
		"spaces after the fence": {"", "---\t\nname: code-reviewer\n---\n", "Remove the spaces after --- on line 1 of the reviewer agent " + path + ", then run again"},
		"a no-break space":       {"", "--- \nname: code-reviewer\n---\n", "Remove the spaces after --- on line 1 of the reviewer agent " + path + ", then run again"},
		"text after the fence":   {"", "---x\nname: code-reviewer\n---\n", fix},
		"a --- in a value":       {"", "---\nname: code-reviewer\ndescription: a --- b\n---\n", fix},
		"a --- in the entry":     {"o---x", "---\ndescription: d\n---\n", fix},
		"a merge key":            {"", "---\n<<: {tools: Read}\n---\n", fix},
		"a << in a value":        {"", "---\nname: code-reviewer\ndescription: \"a << b\"\n---\n", "Remove the << from the frontmatter of the reviewer agent " + path + ", then run again"},
		"an indented name line":  {"", "---\nname: code-reviewer\ndescription: |\n  name: other\n---\n", "Set line 4 to name: code-reviewer, or remove it, in the reviewer agent " + path + ", then run again"},
		"no top name line":       {"", "---\n? name\n: code-reviewer\n---\n", "Write name: code-reviewer once, not indented, in the reviewer agent " + path + ", then run again"},
		"another name":           {"", "---\nname: other\n---\n", "Set name to code-reviewer in the agent " + path + ", or rename the file, then run again"},
	} {
		e := entry
		r := *res
		if c.entry != "" {
			e.Name = c.entry
			r.Groups = map[string][]string{}
			for k, v := range res.Groups {
				r.Groups[k] = append(slices.Clone(v), c.entry)
			}
		}
		if !render.IsReviewAgent(&r, e) {
			t.Fatalf("%s: the entry is no reviewer", name)
		}
		_, err := render.AgentFront(lookup(t, "claude"), &r, e, []byte(c.in+reviewerBody))
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != c.line1 {
			t.Errorf("%s: line 1 is %q, want %q", name, line, c.line1)
		}
	}
}

// TestAReviewerWithPathsBuilds checks that a reviewer may have paths, also as a quoted
// or an escaped key, which qory reads and leaves out of the file that it writes, while
// another key that a reviewer does not take stops each writer.
func TestAReviewerWithPathsBuilds(t *testing.T) {
	for _, key := range []string{"paths", `"paths"`, "'paths'", "paths ", `"pa\x74hs"`} {
		res := agentFixture(t, "---\nname: code-reviewer\n"+key+": [\"**/*.py\"]\ntools: Read\n---\n"+reviewerBody, nil, nil)
		for _, w := range agentWrites(t) {
			dir := t.TempDir()
			if err := w.run(res, dir); err != nil {
				t.Errorf("%s, %s: %v", key, w.name, err)
				continue
			}
			for _, f := range w.files("code-reviewer") {
				data, err := os.ReadFile(filepath.Join(dir, f))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "**/*.py") {
					t.Errorf("%s, %s, %s: the file keeps paths:\n%s", key, w.name, f, data)
				}
			}
		}
	}
	res := agentFixture(t, "---\nname: code-reviewer\nskills: [a]\ntools: Read\n---\n"+reviewerBody, nil, nil)
	want := "Remove the key skills from the reviewer agent " + agentPath(res, "code-reviewer") + ", then run again"
	for _, w := range agentWrites(t) {
		err := w.run(res, t.TempDir())
		if err == nil {
			t.Errorf("%s: no error", w.name)
		} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
			t.Errorf("%s: line 1 is %q, want %q", w.name, line, want)
		}
	}
}

// TestAPermissionModeThatAReviewSessionDeniesStopsTheBuild checks that each writer stops
// at an agent with permissionMode bypassPermissions in a stack with a review session,
// with the fix on line 1, and writes an agent with plan.
func TestAPermissionModeThatAReviewSessionDeniesStopsTheBuild(t *testing.T) {
	const reviewer = "---\nname: code-reviewer\ntools: Read\n---\n" + reviewerBody
	res := agentFixture(t, reviewer, map[string]string{"other.md": "---\ndescription: o\npermissionMode: bypassPermissions\n---\nBody.\n"}, nil)
	want := "Set permissionMode to default, dontAsk or plan in the agent " + agentPath(res, "other") + ", or remove it, then run again"
	for _, w := range agentWrites(t) {
		err := w.run(res, t.TempDir())
		if err == nil {
			t.Errorf("%s: no error", w.name)
		} else if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
			t.Errorf("%s: line 1 is %q, want %q", w.name, line, want)
		}
	}
	res = agentFixture(t, reviewer, map[string]string{"other.md": "---\ndescription: o\npermissionMode: plan\n---\nBody.\n"}, nil)
	for _, w := range agentWrites(t) {
		if err := w.run(res, t.TempDir()); err != nil {
			t.Errorf("%s, plan: %v", w.name, err)
		}
	}
}
