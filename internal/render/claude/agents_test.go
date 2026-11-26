package claude_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/render/claude"
)

// TestWithoutBashStopsOnAStar checks that a * in tools or disallowedTools stops the
// build: Claude Code reads a disallowedTools with a * as no deny list at all, and a
// tools with a * as every tool.
func TestWithoutBashStopsOnAStar(t *testing.T) {
	for name, in := range map[string]string{
		"a denied *":        "---\nname: rev\ndescription: d\ndisallowedTools: \"*\"\n---\nbody\n",
		"a * in both":       "---\nname: rev\ndescription: d\ntools: \"*\"\ndisallowedTools: [\"*\"]\n---\nbody\n",
		"a * after a comma": "---\nname: rev\ndescription: d\ndisallowedTools: \"Write, *\"\n---\nbody\n",
	} {
		got, err := claude.WithoutBash([]byte(in), "agents/rev.md", "rev")
		if err == nil {
			t.Errorf("%s: no error, wrote\n%s", name, got)
			continue
		}
		want := "Fix the frontmatter of the reviewer agent agents/rev.md, then run again"
		if line, _, _ := strings.Cut(err.Error(), "\n"); line != want {
			t.Errorf("%s: line 1 is %q, want %q", name, line, want)
		}
	}
}

// TestWithoutBashStopsOrWritesNoBashForTheProbes checks the other reviewer files of the
// security probes: each stops the build, except a byte order mark before Bash, which
// JavaScript's trim() removes, so qory reads that entry as Bash and leaves it out of
// tools.
func TestWithoutBashStopsOrWritesNoBashForTheProbes(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"a tab between two tools":   {"---\nname: rev\ndescription: d\ntools: \"Read\\tBash\"\n---\nbody\n", ""},
		"CRLF line ends":            {"---\r\nname: rev\r\ndescription: d\r\ntools: Read\r\n---\r\nbody\r\n", ""},
		"a line separator and ---":  {"---\nname: rev\ndescription: \"a\\u2028---\\nb\"\n---\nbody\n", ""},
		"a NUL":                     {"---\nname: rev\ndescription: \"a\\0b\"\n---\nbody\n", ""},
		"an unclosed parenthesis":   {"---\nname: rev\ndescription: d\ntools: \"Read(x, Bash\"\n---\nbody\n", ""},
		"a byte order mark in Bash": {"---\nname: rev\ndescription: d\ntools: \"Read, \\uFEFFBash\"\n---\nbody\n", "---\nname: rev\ndescription: d\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n---\nbody\n"},
	} {
		got, err := claude.WithoutBash([]byte(c.in), "agents/rev.md", "rev")
		switch {
		case c.want == "" && err == nil:
			t.Errorf("%s: no error, wrote\n%s", name, got)
		case c.want != "" && (err != nil || string(got) != c.want):
			t.Errorf("%s: wrote\n%s\n(%v), want\n%s", name, got, err, c.want)
		}
	}
}

func TestWithoutBash(t *testing.T) {
	const body = "\nBody.\n"
	for name, c := range map[string]struct{ in, want string }{
		"tools with a rule":    {"---\ntools: Read, Bash(git), Grep\n---\n", "---\nname: r\ntools:\n  - Read\n  - Grep\ndisallowedTools:\n  - Bash\n  - Agent\n---\n"},
		"Agent denied":         {"---\ntools: Read\ndisallowedTools: Agent, Edit\n---\n", "---\nname: r\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n  - Edit\n---\n"},
		"already denied":       {"---\ntools: Read\ndisallowedTools: Bash\n---\n", "---\nname: r\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n---\n"},
		"denied as a list":     {"---\ndisallowedTools:\n  - Edit\n---\n", "---\nname: r\ndisallowedTools:\n  - Bash\n  - Agent\n  - Edit\n---\n"},
		"no tools, no denied":  {"---\nname: r\n---\n", "---\nname: r\ndisallowedTools:\n  - Bash\n  - Agent\n---\n"},
		"a tool named Bashful": {"---\ntools: Bashful, Read\n---\n", "---\nname: r\ntools:\n  - Bashful\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n---\n"},
		// A denied Bash rule leaves every other Bash command to the agent, so the bare
		// tool comes first, ahead of an entry that may leave a parenthesis open.
		"a Bash rule denied":         {"---\nname: r\ndescription: d\ndisallowedTools: Bash(git)\n---\n", "---\nname: r\ndescription: d\ndisallowedTools:\n  - Bash\n  - Agent\n  - \"Bash(git)\"\n---\n"},
		"a Bash rule denied as list": {"---\nname: r\ndescription: d\ndisallowedTools:\n  - Bash(rm)\n---\n", "---\nname: r\ndescription: d\ndisallowedTools:\n  - Bash\n  - Agent\n  - \"Bash(rm)\"\n---\n"},
		"a Bash rule, then Bash":     {"---\ndisallowedTools: Bash(rm), Bash\n---\n", "---\nname: r\ndisallowedTools:\n  - Bash\n  - Agent\n  - \"Bash(rm)\"\n---\n"},
	} {
		got, err := claude.WithoutBash([]byte(c.in+body), "agents/r.md", "r")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != c.want+body {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, c.want+body)
		}
	}
}

// reviewAgentFailures are reviewer files that WithoutBash cannot make safe: the file
// would reach Claude Code with Bash, or with a second frontmatter in front.
var reviewAgentFailures = map[string]struct{ in, line1 string }{
	"a key written twice":  {"---\nname: r\nname: s\ntools: Read, Bash\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"not closed":           {"---\nname: r\ntools: Read, Bash\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"spaces after a fence": {"--- \nname: r\ntools: Read, Bash\n---\nBody.\n", "Remove the spaces after --- on line 1 of the reviewer agent agents/r.md, then run again"},
	"a tab after a fence":  {"---\t\nname: r\n---\nBody.\n", "Remove the spaces after --- on line 1 of the reviewer agent agents/r.md, then run again"},
	"--- in a value":       {"---\ndescription: a --- b\ntools: Read, Bash\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"an indented ---":      {"---\ndescription: |\n  a\n  ---\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a flow mapping":       {"---\n{tools: \"Read, Bash\"}\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a ... line":           {"---\ntools: Read\n...\ntools: Read, Bash\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a merge key":          {"---\n<<: {disallowedTools: Bash}\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"only Bash in tools":   {"---\ntools: Bash(a)\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a mapping value":      {"---\nmodel: {a: b}\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a null value":         {"---\nmodel:\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a float value":        {"---\nmodel: 1.5\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"an alias":             {"---\ndescription: &x b\nmodel: *x\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a tag":                {"---\nmodel: !!binary dGVzdA==\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a key with a space":   {"---\n\"a b\": c\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"no frontmatter":       {"\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a byte order mark":    {"\ufeff---\ntools: Read\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"CRLF":                 {"---\r\ntools: Read\r\n---\r\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a rule with a space":  {"---\ntools: Read, Bash(git log)\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a rule with a comma":  {"---\ntools: Read, Bash(a, b)\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
	"a rule with a *":      {"---\ndisallowedTools: Bash(rm *)\n---\nBody.\n", "Fix the frontmatter of the reviewer agent agents/r.md, then run again"},
}

func TestWithoutBashFailsClosed(t *testing.T) {
	for name, c := range reviewAgentFailures {
		got, err := claude.WithoutBash([]byte(c.in), "agents/r.md", "r")
		if err == nil {
			t.Errorf("%s: got\n%s\nwant an error", name, got)
			continue
		}
		if line, _, _ := strings.Cut(err.Error(), "\n"); line != c.line1 {
			t.Errorf("%s: line 1 is %q, want %q", name, line, c.line1)
		}
	}
}

// TestWithoutBashErrorsHideASecretInThePath checks that each error of WithoutBash prints
// a path with an access key secret as the phrase that hides it.
func TestWithoutBashErrorsHideASecretInThePath(t *testing.T) {
	for name, in := range map[string]string{
		"a spaced fence": "--- \nname: r\n---\n",
		"bad YAML":       "---\na: [\n---\n",
		"a ---":          "---\na: b --- c\n---\n",
	} {
		_, err := claude.WithoutBash([]byte(in), "agents/qak_secret.md", "r")
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if strings.Contains(err.Error(), "qak_secret") {
			t.Errorf("%s: the error prints the secret:\n%v", name, err)
		}
		if line, _, _ := strings.Cut(err.Error(), "\n"); !strings.Contains(line, "(a name that contains an access key secret)") {
			t.Errorf("%s: line 1 is %q, want the phrase for the secret", name, line)
		}
	}
}

// TestWithoutBashNamesTheCauseOfAMergeKeyAndATag checks the cause on line 2 of a merge key
// and of a tagged key: each stops in a check that runs before the check of a plain word.
func TestWithoutBashNamesTheCauseOfAMergeKeyAndATag(t *testing.T) {
	for name, c := range map[string]struct{ in, line2 string }{
		"a merge key":  {"---\n<<: {disallowedTools: Bash}\n---\nBody.\n", "  the frontmatter has a merge key <<; write the keys out"},
		"a tagged key": {"---\n!!str tools: Read\n---\nBody.\n", "  the key \"tools\" has a tag; remove the tag"},
	} {
		_, err := claude.WithoutBash([]byte(c.in), "agents/r.md", "r")
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if lines := strings.Split(err.Error(), "\n"); len(lines) < 2 || lines[1] != c.line2 {
			t.Errorf("%s: the error is %q, want line 2 %q", name, err, c.line2)
		}
	}
}

// TestWithoutBashTakesOnlyTheReviewerKeys checks the allow-list of a reviewer: each
// allowed key keeps its value, and any other key stops with the key on line 1 and the
// allowed keys on line 2.
func TestWithoutBashTakesOnlyTheReviewerKeys(t *testing.T) {
	const in = "---\nname: r\ndescription: d\nmodel: example-model\neffort: high\nmaxTurns: 12\ncolor: blue\ntools: Read\ndisallowedTools: Edit\n---\nBody.\n"
	const want = "---\nname: r\ndescription: d\ncolor: blue\neffort: high\nmaxTurns: 12\nmodel: example-model\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n  - Edit\n---\nBody.\n"
	if got, err := claude.WithoutBash([]byte(in), "agents/r.md", "r"); err != nil || string(got) != want {
		t.Errorf("the allowed keys: wrote\n%s\n(%v), want\n%s", got, err, want)
	}
	const line2 = "  A reviewer takes only the keys name, description, model, effort, maxTurns, color, tools and disallowedTools"
	for _, key := range []string{"permissionMode", "hooks", "mcpServers", "skills", "sandbox", "paths", "Tools", "_x"} {
		_, err := claude.WithoutBash([]byte("---\nname: r\n"+key+": x\n---\nBody.\n"), "agents/r.md", "r")
		want := "Remove the key " + key + " from the reviewer agent agents/r.md, then run again\n" + line2
		if err == nil || err.Error() != want {
			t.Errorf("%s: the error is %v, want\n%s", key, err, want)
		}
	}
}

// TestPermissionModeInAStackWithAReviewSession checks each value of permissionMode for an
// agent that is no reviewer: absent, default, dontAsk and plan pass, every other value
// stops, as qory reads the frontmatter and on any line with the key, and a stack with no
// review session checks nothing. A U+2028 before the key stops the build, as a line break
// that the line checks do not read, also after a ..., where yaml.v3 ends the document and
// reads no key; an em dash builds.
func TestPermissionModeInAStackWithAReviewSession(t *testing.T) {
	rt, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	guard := rt.(render.CommandGuard)
	check := func(front string, session bool) error {
		data := []byte("---\nname: a\n" + front + "---\nBody.\n")
		src, err := render.SplitAgent(data, "agents/a.md", render.AgentFile)
		if err != nil {
			t.Fatalf("%q: %v", front, err)
		}
		return guard.CheckAgent(render.AgentCheck{Data: data, Source: src, Path: "agents/a.md", Name: "a", ReviewSession: session})
	}
	const keyStop = "Set permissionMode to default, dontAsk or plan in the agent agents/a.md, or remove it, then run again"
	for front, want := range map[string]string{
		"":                                                        "",
		"permissionMode: default\n":                               "",
		"permissionMode: dontAsk\n":                               "",
		"permissionMode: plan\n":                                  "",
		"\"permissionMode\": plan # a\n":                          "",
		"permissionMode: acceptEdits\n":                           keyStop,
		"permissionMode: bypassPermissions\n":                     keyStop,
		"permissionMode: auto\n":                                  keyStop,
		"permissionMode: Plan\n":                                  keyStop,
		"permissionMode: \"\"\n":                                  keyStop,
		"permissionMode:\n":                                       keyStop,
		"permissionMode: 1\n":                                     keyStop,
		"permissionMode: [plan]\n":                                keyStop,
		"? permissionMode\n: bypassPermissions\n":                 keyStop,
		"description: |\n  permissionMode: bypassPermissions\n":   "Set line 4 to permissionMode: default, dontAsk or plan in the agent agents/a.md, then run again",
		"description: x\u2028permissionMode: bypassPermissions\n": "Remove the character U+2028 from line 3 of the agent agents/a.md, then run again",
		"description: x\u2028...\u2028permissionMode: bypassPermissions\n": "Remove the character U+2028 from line 3 of the agent agents/a.md, then run again",
		"description: a \u2014 b\n":                                        "",
	} {
		err := check(front, true)
		line := ""
		if err != nil {
			line, _, _ = strings.Cut(err.Error(), "\n")
		}
		if line != want {
			t.Errorf("%q: line 1 is %q, want %q", front, line, want)
		}
	}
	if err := check("permissionMode: bypassPermissions\n", false); err != nil {
		t.Errorf("a stack with no review session: %v", err)
	}
}
