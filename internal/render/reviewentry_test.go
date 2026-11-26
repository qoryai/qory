package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// reviewStackWith composes the stack workflow-review-group, which has a review session,
// with files, paths below its module core and their text, added.
func reviewStackWith(t *testing.T, files map[string]string) *compose.Result {
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
	for name, text := range files {
		path := filepath.Join(dir, "stack", "modules", "core", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := config.Compose(filepath.Join(dir, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if !render.HasReviewSession(res) {
		t.Fatal("the stack has no review session")
	}
	return res
}

// TestASkillOrCommandOfAReviewStackSetsOnlyTheAllowedKeys: in a stack with a review
// session, a skill or a command whose frontmatter sets a key other than name,
// description, argument-hint, paths, notes_for and allowed-tools stops the build for
// Claude Code, and so does an allowed-tools with a tool other than Read, Grep and Glob, a
// line at any indent with allowed-tools, context, agent or hooks, a spaced fence, a ---
// in the frontmatter, an allowed-tools whose text is more than those tools, commas
// and spaces, or lines of - and one tool, an explicit key with ?, a flow mapping, or an
// allowed-tools that the parse reads from no plain line, such as a key with an escape.
// name and description are strings, and argument-hint, paths and notes_for strings or
// lists of strings: a map as a value stops the build too. A line that starts with ...,
// which ends the YAML document, stops it, with LF and CRLF line ends, and so does a line
// break other than a line feed: a lone CR, U+0085, U+2028 or U+2029. So do a byte order
// mark inside the frontmatter, a format character such as U+200B, a byte that is not UTF-8
// and a UTF-16 byte order mark. An em dash builds.
// Claude Code adds the tools of allowed-tools to the allow rules of the session once the
// skill runs.
func TestASkillOrCommandOfAReviewStackSetsOnlyTheAllowedKeys(t *testing.T) {
	const skill, command = "skills/probe/SKILL.md", "commands/probe.md"
	skillPath := func(file string) string {
		return filepath.Join("stack", "modules", "core", file)
	}
	for name, c := range map[string]struct {
		file, front string
		// line1 is the start of line 1 of the error, "" for a build that passes.
		line1 string
	}{
		"name and description":          {skill, "name: probe\ndescription: d\n", ""},
		"an argument-hint":              {command, "description: d\nargument-hint: <file>\n", ""},
		"allowed-tools Read, Grep":      {skill, "name: probe\nallowed-tools: Read, Grep\n", ""},
		"allowed-tools as a block list": {command, "allowed-tools:\n  - Read\n\n  - Glob\n", ""},
		"allowed-tools with spaces":     {skill, "name: probe\nallowed-tools: Read Grep,Glob\n", ""},
		"context fork":                  {skill, "name: probe\ncontext: fork\n", "Remove the key context from the skill "},
		"hooks":                         {skill, "name: probe\nhooks:\n  Stop: []\n", "Remove the key hooks from the skill "},
		"agent":                         {skill, "name: probe\nagent: general-purpose\n", "Remove the key agent from the skill "},
		"an unknown key":                {command, "description: d\nmodel: x\n", "Remove the key model from the command "},
		"allowed-tools Bash":            {skill, "name: probe\nallowed-tools: Bash, Write\n", "Set allowed-tools to Read, Grep or Glob in the skill "},
		"allowed-tools of a command": {command, "allowed-tools:\n  - Read\n  - Bash(git status:*)\n",
			"Set allowed-tools to Read, Grep or Glob in the command "},
		"context in a block": {skill, "name: probe\ndescription: |\n  context: fork\n", "Remove the key context from line 4 of the skill "},
		"allowed-tools in a block": {command, "description: |\n  allowed-tools: Bash\n",
			"Remove the key allowed-tools from line 3 of the command "},
		"a spaced fence": {skill, "@name: probe\nallowed-tools: Bash\n", "Remove the spaces after --- on line 1 of the skill "},
		"allowed-tools on a line of a quoted value": {skill, "name: probe\ndescription: \"a\nallowed-tools: Bash\"\n",
			"Remove the key allowed-tools from line 4 of the skill "},
		// Claude Code ends the frontmatter at the ---, and its retry could read Bash.
		"a --- in a value": {skill, "name: probe\ndescription: \"x---y\"\nallowed-tools: Read\n",
			"Remove the --- from the frontmatter of the skill "},
		"a # on the allowed-tools line": {skill, "name: probe\nallowed-tools: Read #, Bash\n",
			"Write allowed-tools on line 3 of the skill "},
		"a quoted allowed-tools": {command, "allowed-tools: \"Read, Grep\"\n", "Write allowed-tools on line 2 of the command "},
		"a flow list":            {command, "allowed-tools: [Read, Glob]\n", "Write allowed-tools on line 2 of the command "},
		"a flow list with Bash": {command, "allowed-tools: [Read, Bash]\n",
			"Set allowed-tools to Read, Grep or Glob in the command "},
		"a # in a block list": {skill, "name: probe\nallowed-tools:\n  - Read # x\n", "Write allowed-tools on line 4 of the skill "},
		"a comment line in a block list": {skill, "name: probe\nallowed-tools:\n# x\n  - Read\n",
			"Write allowed-tools on line 4 of the skill "},
		"an explicit key": {skill, "name: probe\n? allowed-tools\n: Read #, Bash\n", "Remove the ? from line 3 of the skill "},
		"an explicit key in a list": {command, "description:\n  - ? allowed-tools\n    : Bash\n",
			"Remove the ? from line 3 of the command "},
		"a flow mapping": {skill, "{name: probe, allowed-tools: Read}\n", "Write the frontmatter of the skill "},
		"a key with an escape": {skill, "name: probe\n\"allowed\\x2dtools\": Read #, Bash\n",
			"Write allowed-tools: once, plain, at the start of a line in the skill "},
		"an explicit key with CRLF": {skill, "name: probe\n? allowed-tools\n: Read\n", "Remove the ? from line 3 of the skill "},
		"a bare ? with CRLF":        {skill, "name: probe\n?\n  allowed-tools\n: Read\n", "Remove the ? from line 3 of the skill "},
		"allowed-tools with CRLF":   {skill, "name: probe\nallowed-tools: Read, Grep\n", ""},
		"a map as the description": {skill, "name: probe\ndescription: &a {allowed-tools: Bash}\n",
			"Set description to a string in the skill "},
		"a number as the name":      {skill, "name: 7\n", "Set name to a string in the skill "},
		"a list as the name":        {skill, "name: [a]\n", "Set name to a string in the skill "},
		"a list as the description": {skill, "name: probe\ndescription: [a]\n", "Set description to a string in the skill "},
		"argument-hint in brackets": {command, "argument-hint: [message]\n", ""},
		"argument-hint with a map":  {command, "argument-hint: [{a: b}]\n", "Set argument-hint to a string or a list of strings in the command "},
		"paths as a list":           {skill, "name: probe\npaths: [\"**/*.go\"]\n", ""},
		"paths with a map":          {skill, "name: probe\npaths: [{a: b}]\n", "Set paths to a string or a list of strings in the skill "},
		"a key after ...":           {skill, "name: probe\n...\n\"allowed\\x2dtools\": Bash\n", "Remove the ... from line 3 of the skill "},
		"a model after ...":         {command, "description: d\n... \nmodel: x\n", "Remove the ... from line 3 of the command "},
		"a key after ... and a tab": {skill, "name: probe\n...\t\n\"allowed\\x2dtools\": Bash\n", "Remove the ... from line 3 of the skill "},
		"a key after ... with CRLF": {skill, "name: probe\n...\nmodel: x\n", "Remove the ... from line 3 of the skill "},
		"a description with ...":    {skill, "name: probe\ndescription: wait...\n", ""},
		"a lone CR":                 {skill, "name: probe\rmodel: x\n", "Remove the character U+000D from line 2 of the skill "},
		"a U+0085":                  {skill, "name: probe\ndescription: x\u0085...\n", "Remove the character U+0085 from line 3 of the skill "},
		"a U+2028 before ...": {skill, "name: probe\ndescription: x\u2028...\n\"allowed\\x2dtools\": Bash\n",
			"Remove the character U+2028 from line 3 of the skill "},
		"a U+2029":   {command, "description: x\u2029model: y\n", "Remove the character U+2029 from line 2 of the command "},
		"an em dash": {skill, "name: probe\ndescription: a \u2014 b\n", ""},
		"a second byte order mark": {skill, "\ufeff\ufeffname: probe\n#description: '\n\"allowed\\x2dtools\": Bash\n#'\n",
			"Remove the character U+FEFF from line 2 of the skill "},
		"a zero-width space":       {skill, "name: probe\ndescription: a\u200bb\n", "Remove the character U+200B from line 3 of the skill "},
		"a byte that is not UTF-8": {skill, "name: probe\ndescription: a\xffb\n", "Fix the frontmatter of the skill "},
		"a UTF-16 byte order mark": {command, "\xff\xfedescription: d\n", "Fix the frontmatter of the command "},
		"a value on the next line": {skill, "name: probe\nallowed-tools: Read\n  Grep\n", "Write allowed-tools on line 4 of the skill "},
	} {
		t.Run(name, func(t *testing.T) {
			text := "---\n" + c.front + "---\nBody.\n"
			if front, ok := strings.CutPrefix(c.front, "@"); ok {
				text = "--- \n" + front + "---\nBody.\n"
			}
			if strings.HasSuffix(name, "with CRLF") {
				text = strings.ReplaceAll(text, "\n", "\r\n")
			}
			res := reviewStackWith(t, map[string]string{c.file: text})
			err := render.Build(res, filepath.Join(t.TempDir(), "home"), lookup(t, "claude"))
			if c.line1 == "" {
				if err != nil {
					t.Errorf("the build stopped: %v", err)
				}
				return
			}
			line, _, _ := strings.Cut(errString(err), "\n")
			if !strings.HasPrefix(line, c.line1) || !strings.Contains(line, skillPath(c.file)) {
				t.Errorf("line 1 is %q, want %q and the file %s", line, c.line1, c.file)
			}
		})
	}
}

// errString is the text of err, "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
