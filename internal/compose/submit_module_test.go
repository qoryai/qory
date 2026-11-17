package compose_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// forgeRepo is a git repository with a module forge at forge/ that ships a submit program,
// committed twice: the first commit has the skill a, the second adds the skill b. It
// returns the file URL and the two full commit ids.
func forgeRepo(t *testing.T) (url, first, second string) {
	t.Helper()
	url = gitRepo(t, "", map[string]string{
		"forge/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: forge\nsubmit_program:\n  command: [bin/open-draft]\n",
		"forge/bin/open-draft":         "#!/bin/sh\nexit 0\n",
		"forge/skills/a/SKILL.md":      "---\nname: a\ndescription: a\n---\n",
		"other/qory-module.yaml":       "apiVersion: qory.dev/v1alpha1\nname: other\n",
		"other/skills/o/SKILL.md":      "---\nname: o\ndescription: o\n---\n",
		"stack/" + stack.FileName:      "apiVersion: qory.dev/v1alpha1\nname: webstack\nmodules:\n  - name: forge\n    source: {path: ../forge}\n",
		"stack/other/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: unused\n",
	})
	dir := strings.TrimPrefix(url, "file://")
	rev := func() string {
		out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	first = rev()
	if err := os.MkdirAll(filepath.Join(dir, "forge/skills/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge/skills/b/SKILL.md"), []byte("---\nname: b\ndescription: b\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "second"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return url, first, rev()
}

// submitStack is a qory.yaml-shaped stack that lists the modules, YAML lines of the modules key.
func submitStack(t *testing.T, modules string) *stack.Stack {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "qory.yaml")
	if err := os.WriteFile(file, []byte("apiVersion: qory.dev/v1alpha1\nname: checkout\nmodules:\n"+modules), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := stack.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = stack.Runtimes{"claude"}
	return p
}

// composeSubmit composes p with the forge module of url at commit as the submit module.
func composeSubmit(t *testing.T, p *stack.Stack, url, commit string) (*compose.Result, error) {
	t.Helper()
	sub := &compose.SubmitModule{Source: stack.Source{Git: url, Ref: commit, Path: "forge"}}
	return compose.ComposeWith(p, compose.Options{Cache: t.TempDir(), Memo: source.NewMemo(), Submit: sub})
}

func moduleNames(res *compose.Result) string {
	var names []string
	for _, m := range res.Modules {
		names = append(names, m.Name)
	}
	return strings.Join(names, ",")
}

// TestSubmitModuleIsAppendedAfterTheComposedModules appends the submit module when no
// composed module has its name, and a compose with no submit module is unchanged.
func TestSubmitModuleIsAppendedAfterTheComposedModules(t *testing.T) {
	isolateAll(t)
	url, _, second := forgeRepo(t)
	list := "  - name: other\n    source: {git: " + url + ", ref: " + second + ", path: other}\n"
	res, err := composeSubmit(t, submitStack(t, list), url, second)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if got := moduleNames(res); got != "other,forge" {
		t.Errorf("modules %s, want other,forge", got)
	}
	if res.Replaced != nil {
		t.Errorf("replaced %+v, want none", res.Replaced)
	}
	if res.SubmitProgram == nil || res.SubmitProgram.Module != "forge" {
		t.Errorf("submit program %+v, want the one of forge", res.SubmitProgram)
	}
	plain, err := compose.ComposeWith(submitStack(t, list), compose.Options{Cache: t.TempDir(), Memo: source.NewMemo()})
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if got := moduleNames(plain); got != "other" || plain.SubmitProgram != nil {
		t.Errorf("a compose with no submit module has %s and program %+v", got, plain.SubmitProgram)
	}
}

// TestSubmitModuleReplacesTheRefOfTheSameModule reads a listed module of the same name and
// git source at the commit Qory Apiary pins, in the listed module's place, and says so.
func TestSubmitModuleReplacesTheRefOfTheSameModule(t *testing.T) {
	isolateAll(t)
	url, first, second := forgeRepo(t)
	list := "  - name: other\n    source: {git: " + url + ", ref: " + second + ", path: other}\n" +
		"  - name: forge\n    source: {git: " + url + ", ref: " + first + ", path: forge}\n"
	res, err := composeSubmit(t, submitStack(t, list), url, second)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if got := moduleNames(res); got != "other,forge" {
		t.Errorf("modules %s, want other,forge", got)
	}
	forge := res.Modules[1]
	if forge.Commit != second {
		t.Errorf("forge is at %s, want the pinned %s", forge.Commit, second)
	}
	var skills []string
	for _, e := range res.Entries {
		if e.Module == "forge" {
			skills = append(skills, e.Name)
		}
	}
	if strings.Join(skills, ",") != "a,b" {
		t.Errorf("forge ships %v, want the skills of the second commit", skills)
	}
	want := "forge from " + url + "@" + second + ", in place of " + first + " in qory.yaml"
	if res.Replaced == nil || res.Replaced.Line() != want {
		t.Errorf("replaced %+v, line %q, want %q", res.Replaced, res.Replaced.Line(), want)
	}
	// A listed ref that is the pinned commit is left as it is, with no line.
	list = "  - name: forge\n    source: {git: " + url + ", ref: " + second + ", path: forge}\n"
	same, err := composeSubmit(t, submitStack(t, list), url, second)
	if err != nil {
		t.Fatal(err)
	}
	defer same.Close()
	if same.Replaced != nil || moduleNames(same) != "forge" {
		t.Errorf("the pinned ref gives modules %s and replaced %+v, want forge and none", moduleNames(same), same.Replaced)
	}
}

// TestSubmitModuleRefusesTheSameNameFromAnotherSource fails a listed module of the same
// name from another git source, or a path, with the fix first, and moves the cause to the
// second line when the first line would pass 100 characters.
func TestSubmitModuleRefusesTheSameNameFromAnotherSource(t *testing.T) {
	isolateAll(t)
	url, _, second := forgeRepo(t)
	otherURL, _, _ := forgeRepo(t)
	dir := strings.TrimPrefix(url, "file://")
	for _, c := range []struct{ name, list, want string }{
		{"another git source", "  - name: forge\n    source: {git: " + otherURL + ", ref: main, path: forge}\n",
			"Remove module forge from qory.yaml: the run adds it from " + url + "@" + second + "."},
		{"a path", "  - name: forge\n    source: {path: " + filepath.Join(dir, "forge") + "}\n",
			"Remove module forge from qory.yaml: the run adds it from " + url + "@" + second + "."},
	} {
		_, err := composeSubmit(t, submitStack(t, c.list), url, second)
		if err == nil {
			t.Errorf("%s: composed, want a refusal", c.name)
			continue
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		if len(c.want) <= 100 {
			if first != c.want {
				t.Errorf("%s: first line %q, want %q", c.name, first, c.want)
			}
		} else if first != "Remove module forge from qory.yaml." || !strings.Contains(err.Error(), "\n  The run adds it from "+url+"@"+second+".") {
			t.Errorf("%s: refusal %q, want the fix alone in line 1 and the cause in line 2", c.name, err)
		}
		if !strings.Contains(err.Error(), "The module in qory.yaml is at ") {
			t.Errorf("%s: refusal %q has no line for the source in the file", c.name, err)
		}
	}
}
