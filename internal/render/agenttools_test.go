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
// outputs, the plugin's agents and .claude/agents, a member has a frontmatter that qory
// writes: no Bash in tools and disallowedTools with Bash and Agent first and once, a
// member with no
// tools gets disallowedTools alone, a member with a disallowedTools keeps its entry, and
// explorer stays as written.
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
		"code-reviewer":   "---\nname: code-reviewer\ndescription: code-reviewer\ntools:\n  - Read\n  - Grep\n  - Write\ndisallowedTools:\n  - Bash\n  - Agent\n---\n\nReviews a change.\n",
		"test-reviewer":   "---\nname: test-reviewer\ndescription: test-reviewer\ntools:\n  - Read\ndisallowedTools:\n  - Bash\n  - Agent\n  - Write\n---\n\nReviews a change.\n",
		"verify-reviewer": "---\nname: verify-reviewer\ndescription: verify-reviewer\ndisallowedTools:\n  - Bash\n  - Agent\n---\n\nReviews a change.\n",
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
