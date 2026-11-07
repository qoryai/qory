package compose_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestTheSameSkillInTwoModulesCollidesWhenGrouped is one module with skills/deploy and one
// with a group that has it: the collision of today, on the flat name, whose exclude
// resolves it by that name.
func TestTheSameSkillInTwoModulesCollidesWhenGrouped(t *testing.T) {
	files := func(body string) map[string]string {
		return map[string]string{
			stack.FileName:                        body,
			"modules/a/skills/deploy/SKILL.md":    "a\n",
			"modules/b/skills/ci/deploy/SKILL.md": "b\n",
		}
	}
	_, err := composeTree(t, files(twoModules))
	var c *compose.CollisionError
	if !errors.As(err, &c) || !strings.Contains(err.Error(), "skills/deploy is provided by 2 modules") || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "exclude: {skills: [deploy]}") {
		t.Fatalf("got %v", err)
	}
	res, err := composeTree(t, files(strings.Replace(twoModules, "path: modules/a\n", "path: modules/a\n    exclude: {skills: [deploy]}\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if len(res.Entries) != 1 || res.Entries[0].Module != "b" || !strings.HasSuffix(res.Entries[0].Path, "modules/b/skills/ci/deploy") {
		t.Errorf("entries %+v", res.Entries)
	}
}

// TestAGroupedSkillIsSelectedByItsFlatName is exclude, only, requires, bind and provides
// on skills in a group: each uses the last folder's name, and a name with the group
// selects nothing.
func TestAGroupedSkillIsSelectedByItsFlatName(t *testing.T) {
	const module = "modules/core/skills/group/"
	base := map[string]string{module + "keep/SKILL.md": "k\n", module + "drop/SKILL.md": "d\n"}
	with := func(block string, extra map[string]string) map[string]string {
		m := map[string]string{stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n" + block}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	names := func(res *compose.Result) string {
		var out []string
		for _, e := range res.Entries {
			out = append(out, e.Kind+"/"+e.Name)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"exclude", with("    exclude: {skills: [drop]}\n", nil), "skills/keep"},
		{"only", with("    only: {skills: [keep]}\n", nil), "skills/keep"},
		{"only with requires", with("    only: {skills: [keep]}\n", map[string]string{
			"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\nrequires:\n  - skill: keep\n    skills: [drop]\n",
		}), "skills/drop skills/keep"},
		{"bind", with("bind:\n  skills:\n    role: keep\n", map[string]string{
			"modules/core/agents/x.md": "${qory:skills/role}\n",
		}), "agents/x skills/drop skills/keep"},
		{"provides", with("", map[string]string{
			"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\nprovides:\n  skills:\n    role: keep\n",
			"modules/core/agents/x.md":      "${qory:skills/role}\n",
		}), "agents/x skills/drop skills/keep"},
	} {
		res, err := composeTree(t, c.files)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := names(res); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		if c.name == "bind" || c.name == "provides" {
			if res.Bind["skills/role"] != "keep" {
				t.Errorf("%s: bind %v", c.name, res.Bind)
			}
		}
		res.Close()
	}
	if _, err := composeTree(t, with("    exclude: {skills: [group/drop]}\n", nil)); err == nil {
		t.Error("an exclude with the group's name composed")
	}
}

// TestACheckoutExcludeAndExtendingUseTheFlatName is a checkout on a base with a grouped
// skill: the exclude of the base's module lists the last folder's name, and a module of
// the checkout that ships the same name is checked by extending as any skill is.
func TestACheckoutExcludeAndExtendingUseTheFlatName(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:                 "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [skills]\nmodules:\n  - name: a\n    source: {path: modules/a}\n",
		"base/modules/a/qory-module.yaml":        "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"base/modules/a/skills/group/x/SKILL.md": "base\n",
		"app/" + config.FileName:                 "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: a\n      exclude: {skills: [x]}\n    - name: app\n      source: {path: harness}\n",
		"app/harness/qory-module.yaml":           "apiVersion: qory.dev/v1alpha1\nname: app\n",
		"app/harness/skills/mine/x/SKILL.md":     "app\n",
	})
	p, err := config.Compose(dir + "/app/" + config.FileName)
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
	defer res.Close()
	if len(res.Entries) != 1 || res.Entries[0].Module != "app" || res.Entries[0].Name != "x" {
		t.Errorf("entries %+v", res.Entries)
	}
	if len(res.Excludes) != 1 || res.Excludes[0].Module != "a" || res.Excludes[0].Name != "x" || !strings.HasSuffix(res.Excludes[0].Path, "skills/group/x") {
		t.Errorf("excludes %+v", res.Excludes)
	}
}
