//go:build unix

package cmd_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/cmd"
)

// reviewerWithBash is the agent file of the review group's member in
// [TestARealWallShowsAReviewerNoBashAndALockedCopy]: it lists Bash with a rule in tools,
// and denies another Bash rule.
const reviewerWithBash = "---\nname: code-reviewer\ndescription: Review code.\ntools: Read, Grep, Bash(git log *), Write\n" +
	"disallowedTools: Bash(git push *)\n---\n\nReview.\n"

// reviewWallAgent is the program of the agent's container in
// [TestARealWallShowsAReviewerNoBashAndALockedCopy], in place of Claude Code. A session
// whose working folder is writable commits a file. A session whose working folder is
// read-only prints the two agent files of the harness home, and the result of a write into
// the copy of the commit and what `ls` of the copy lists.
const reviewWallAgent = `#!/bin/sh
if [ -w . ]; then
	echo "agent: a write session"
	echo text > "file-$(date +%s)-$$.txt"
	git add -A
	git -c user.name=agent -c user.email=agent@example.invalid commit -q -m "Add a file"
	exit 0
fi
echo "agent: a review session"
for f in "$QORY_HARNESS_HOME/claude/agents/code-reviewer.md" "$QORY_HARNESS_HOME/claude/plugin/agents/code-reviewer.md"; do
	if [ -f "$f" ]; then
		echo "agent: the file ${f#"$QORY_HARNESS_HOME"/}"
		sed 's/^/agent: | /' "$f"
	else
		echo "agent: no file ${f#"$QORY_HARNESS_HOME"/}"
	fi
done
copy=$(sed -n 's/.*"commit_folder": *"\([^"]*\)".*/\1/p' "$QORY_HARNESS_HOME/inputs.json")
if touch "$copy/written-by-the-agent" 2>/dev/null; then
	echo "agent: the write into the copy worked"
else
	echo "agent: the write into the copy failed"
fi
if ls "$copy" | grep -q '^file-.*\.txt$'; then
	echo "agent: ls of the copy lists the files of the commit"
else
	echo "agent: ls of the copy lists no file of the commit"
fi
`

// TestARealWallShowsAReviewerNoBashAndALockedCopy is a write run behind a real wall on
// this machine's engine, with a review session between two write sessions. The program in
// the agent's container, a shell script, prints what the review session sees. The agent
// file of the review group's member, in the harness home at claude/agents and
// claude/plugin/agents, is the frontmatter that qory writes: no Bash in tools, and
// disallowedTools with the bare Bash first and then the rule the file denied. A write
// into the copy of the commit fails, and `ls` of the copy lists the files of the commit.
// It runs only with QORY_TEST_DOCKER=1 and docker on the PATH, and not as root. The wall
// runs as the uid and gid of this process, which the run takes as qory's, as
// [walledAsThisUser] does.
//
// shortcut: this test runs a shell stand-in, not Claude Code, so it does not show that a
// reviewer cannot run a command; that is checked on the cloud in the first run.
func TestARealWallShowsAReviewerNoBashAndALockedCopy(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if os.Geteuid() == 0 {
		t.Skip("a walled run takes the uid of qory from a user that is not root")
	}
	t.Cleanup(cmd.SetQoryUID(os.Geteuid()))
	helper, image := realWallImage(t, "review-agent", reviewWallAgent)
	files := maps.Clone(reviewFiles)
	files["modules/hello/agents/code-reviewer.md"] = reviewerWithBash
	p, _ := sharedPushRepoWith(t, files, reviewWorkflows, answerFor(2, "review-changes"), answerFor(3, "hello"))
	p.real = true
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	executableScope(t, p)
	change := p.change
	p.change = func(d map[string]any) {
		change(d)
		d["type_settings"] = map[string]any{"full_suite": true}
	}
	suiteServedPlan(t, p, nil, "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n"+
		"| full | `printf '%s' '"+suiteGreen+"' > junit.xml` | `junit.xml` | s |\n")
	p.api.setRound(1, 2)
	fakeLaunch(t, "/usr/local/bin/review-agent")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \""+strconv.Itoa(os.Geteuid())+":"+strconv.Itoa(os.Getegid())+"\"\n  image: "+image+"\n")
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.out, "agent: a review session", "agent: the write into the copy failed", "agent: ls of the copy lists the files of the commit")
	lacks(t, r.out, "agent: the write into the copy worked", "agent: no file", "agent: ls of the copy lists no file", "Bash(git log")
	// Each of the two agent files has the tools without Bash and the denied entries with the
	// bare Bash first.
	const tools = "agent: | tools:\nagent: |   - Read\nagent: |   - Grep\nagent: |   - Write\n"
	const denied = "agent: | disallowedTools:\nagent: |   - Bash\nagent: |   - \"Bash(git push *)\"\n"
	if n := strings.Count(r.out, tools); n != 2 {
		t.Errorf("%d agent files have the tools Read, Grep and Write, want 2\n%s", n, r.out)
	}
	if n := strings.Count(r.out, "agent: | tools:"); n != 2 {
		t.Errorf("%d tools lines, want 2\n%s", n, r.out)
	}
	if n := strings.Count(r.out, denied); n != 2 {
		t.Errorf("%d agent files have disallowedTools with the bare Bash first, want 2\n%s", n, r.out)
	}
	if n := strings.Count(r.out, "agent: | disallowedTools:"); n != 2 {
		t.Errorf("%d disallowedTools lines, want 2\n%s", n, r.out)
	}
}
