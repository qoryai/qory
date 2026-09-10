package compose_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
)

// TestFilesCollideByPathAndAnExcludeResolvesIt is two modules shipping one runtime path:
// the compose refuses with the modules named, and an exclude naming the path in one of
// them keeps the other's, recorded like any exclude.
func TestFilesCollideByPathAndAnExcludeResolvesIt(t *testing.T) {
	tree := map[string]string{
		"qory-stack.yaml":                      twoModules,
		"modules/a/qory-module.yaml":           manifest("a", "HARNESS_HOME", "."),
		"modules/a/files/claude/rules/web.md":  "a\n",
		"modules/b/qory-module.yaml":           "apiVersion: qory.ai/v1alpha1\nname: b\n",
		"modules/b/files/claude/rules/web.md":  "b\n",
		"modules/b/files/cursor/rules/web.mdc": "b\n",
	}
	_, err := composeTree(t, tree)
	var collision *compose.CollisionError
	if !errors.As(err, &collision) || !strings.Contains(err.Error(), "files/claude/rules/web.md is provided by 2 modules: a@working-tree, b@working-tree") {
		t.Fatalf("err = %v", err)
	}
	tree["qory-stack.yaml"] = strings.Replace(twoModules, "  - name: b\n", "  - name: b\n    exclude: {files: [claude/rules/web.md]}\n", 1)
	res, err := composeTree(t, tree)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Kind+"/"+e.Name+" "+e.Module)
	}
	if got := strings.Join(names, "\n"); got != "files/claude/rules/web.md a\nfiles/cursor/rules/web.mdc b" {
		t.Errorf("entries:\n%s", got)
	}
	if len(res.Excludes) != 1 || res.Excludes[0].Kind != "files" || res.Excludes[0].Name != "claude/rules/web.md" || res.Excludes[0].Module != "b" {
		t.Errorf("excludes: %+v", res.Excludes)
	}
}
