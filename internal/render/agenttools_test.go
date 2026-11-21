package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// TestBuildWritesNoBashIntoReviewGroupAgents builds the fixture workflow-review-group
// for claude: code-reviewer, test-reviewer and verify-reviewer are members of the groups
// that the review_group of two workflows names, and explorer is in none. In both
// outputs, the plugin's agents and .claude/agents, a member has no Bash in tools and has
// disallowedTools with Bash once, a member with no tools gets disallowedTools alone, a
// member with a disallowedTools keeps its entry, and explorer stays as written.
func TestBuildWritesNoBashIntoReviewGroupAgents(t *testing.T) {
	hermetic(t)
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/workflow-review-group/qory.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.Compose(file)
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
	home := filepath.Join(t.TempDir(), ".qory", "harness")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"code-reviewer":   "---\nname: code-reviewer\ndescription: code-reviewer\ntools: Read, Grep, Write\ndisallowedTools: Bash\n---\n\nReviews a change.\n",
		"test-reviewer":   "---\nname: test-reviewer\ndescription: test-reviewer\ntools: Read\ndisallowedTools: Write, Bash\n---\n\nReviews a change.\n",
		"verify-reviewer": "---\nname: verify-reviewer\ndescription: verify-reviewer\ndisallowedTools: Bash\n---\n\nReviews a change.\n",
		"explorer":        "---\nname: explorer\ndescription: explorer\ntools: Read, Bash\n---\n\nFinds files.\n",
	}
	for _, dir := range []string{"claude/agents", "claude/plugin/agents"} {
		for name, text := range want {
			got, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(dir), name+".md"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != text {
				t.Errorf("%s/%s.md:\n%s\nwant:\n%s", dir, name, got, text)
			}
		}
	}
}

func TestWithoutBash(t *testing.T) {
	const body = "\nBody.\n"
	for name, c := range map[string]struct{ in, want string }{
		"tools with a rule":    {"---\ntools: Read, Bash(git log *), Grep\n---\n", "---\ntools: Read, Grep\ndisallowedTools: Bash\n---\n"},
		"a rule with a comma":  {"---\ntools: Read, Bash(a, b)\n---\n", "---\ntools: Read\ndisallowedTools: Bash\n---\n"},
		"Bash only":            {"---\ntools: Bash\n---\n", "---\ntools: Bash\ndisallowedTools: Bash\n---\n"},
		"already denied":       {"---\ntools: Read\ndisallowedTools: Bash\n---\n", "---\ntools: Read\ndisallowedTools: Bash\n---\n"},
		"denied as a list":     {"---\ndisallowedTools:\n  - Edit\n---\n", "---\ndisallowedTools: Edit, Bash\n---\n"},
		"no tools, no denied":  {"---\nname: a\n---\n", "---\nname: a\ndisallowedTools: Bash\n---\n"},
		"no frontmatter":       {"", "---\ndisallowedTools: Bash\n---\n"},
		"a tool named Bashful": {"---\ntools: Bashful, Read\n---\n", "---\ntools: Bashful, Read\ndisallowedTools: Bash\n---\n"},
	} {
		if got := string(render.WithoutBash([]byte(c.in + body))); got != c.want+body {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, c.want+body)
		}
	}
}
