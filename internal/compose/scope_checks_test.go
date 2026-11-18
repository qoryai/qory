package compose_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// planStack is a stack over module core whose workflow fix has access write and the plan
// type change-plan.
const planStack = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  fix:\n    access: write\n    plan: change-plan\n"

const scopeManifest = "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n  change-plan: [checks/check.js, scope, --plan]\n"

// TestAPlanWithAScopeCheckComposes is a workflow that names a plan type and a module of the
// stack that declares the scope check for it: the compose passes, and the result has the
// command of the manifest, its program under $QORY_HARNESS_HOME.
func TestAPlanWithAScopeCheckComposes(t *testing.T) {
	res, err := composeTree(t, map[string]string{
		stack.FileName:                     planStack,
		"modules/core/skills/fix/SKILL.md": "text\n",
		"modules/core/qory-module.yaml":    scopeManifest,
		"modules/core/checks/check.js":     "#!/usr/bin/env node\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	want := map[string]compose.ScopeCheck{"change-plan": {Module: "core",
		Command: []string{"$QORY_HARNESS_HOME/modules/core/checks/check.js", "scope", "--plan"}}}
	if !reflect.DeepEqual(res.ScopeChecks, want) {
		t.Errorf("scope checks %+v, want %+v", res.ScopeChecks, want)
	}
	if res.PushChecks != nil {
		t.Errorf("push checks %+v, want none", res.PushChecks)
	}
}

// TestAPlanNoModuleCheckedIsRefused is a workflow that names a plan type that no module
// declares a scope check for, with no module that declares one and with a module that
// declares one for another type: the compose fails with the one fix line.
func TestAPlanNoModuleCheckedIsRefused(t *testing.T) {
	const want = "Add a scope check for change-plan to a module's scope_checks.\n" +
		"  You can also remove `plan: change-plan` from fix in the stack.\n" +
		"  No module of the stack declares a scope check for change-plan.\n" +
		"  workflows.fix is in qory-stack.yaml."
	for name, manifest := range map[string]string{
		"no scope_checks": "apiVersion: qory.dev/v1alpha1\nname: core\n",
		"another type":    strings.Replace(scopeManifest, "change-plan", "build-plan", 1),
		"a push check":    "apiVersion: qory.dev/v1alpha1\nname: core\npush_checks:\n  - type: change-plan\n    command: [checks/check.js]\n",
	} {
		_, err := composeTree(t, map[string]string{
			stack.FileName:                     planStack,
			"modules/core/skills/fix/SKILL.md": "text\n",
			"modules/core/qory-module.yaml":    manifest,
			"modules/core/checks/check.js":     "#!/usr/bin/env node\n",
		})
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
}

// TestAStackWithNoPlanComposesAsBefore is a workflow with access write and no plan, and a
// module with no scope check: the compose passes with no scope checks. A module that
// declares one for a type no workflow names also passes, and the result has it.
func TestAStackWithNoPlanComposesAsBefore(t *testing.T) {
	const noPlan = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  fix:\n    access: write\n"
	res, err := composeTree(t, map[string]string{stack.FileName: noPlan, "modules/core/skills/fix/SKILL.md": "text\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ScopeChecks != nil {
		t.Errorf("scope checks %+v, want none", res.ScopeChecks)
	}
	res.Close()
	res, err = composeTree(t, map[string]string{
		stack.FileName: noPlan, "modules/core/skills/fix/SKILL.md": "text\n",
		"modules/core/qory-module.yaml": scopeManifest, "modules/core/checks/check.js": "#!/usr/bin/env node\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if _, ok := res.ScopeChecks["change-plan"]; !ok {
		t.Errorf("scope checks %+v, want change-plan", res.ScopeChecks)
	}
}

// TestAPlanOnAWorkflowThatDoesNotWriteIsRefused is a plan on a workflow whose access is
// read, write-no-push, or left out: the stack is refused, though a module declares the
// scope check.
func TestAPlanOnAWorkflowThatDoesNotWriteIsRefused(t *testing.T) {
	for _, c := range []struct{ access, level string }{{"", "read"}, {"    access: read\n", "read"}, {"    access: write-no-push\n", "write-no-push"}} {
		const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  fix:\n"
		dir := writeTree(t, map[string]string{
			stack.FileName:                     head + c.access + "    plan: change-plan\n",
			"modules/core/skills/fix/SKILL.md": "text\n",
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/checks/check.js":     "#!/usr/bin/env node\n",
		})
		file := filepath.Join(dir, stack.FileName)
		_, err := loadFor(file, "claude")
		want := "Remove `plan` from fix, or set its access to write.\n" +
			"  Only a workflow with access write has a plan, and the access of fix is " + c.level + ".\n" +
			"  workflows.fix is in " + file + "."
		if err == nil || err.Error() != want {
			t.Errorf("access %q:\n%v\nwant:\n%s", c.access, err, want)
		}
	}
}

// TestScopeCheckProblemsOfAModuleAreRefused is two modules of the stack that
// declare a scope check for one type, a program that is not a file in its module, and a
// program in an entry that the module's exclude leaves out.
func TestScopeCheckProblemsOfAModuleAreRefused(t *testing.T) {
	const two = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n  - name: web\n    source: {path: modules/web}\nworkflows:\n  fix:\n    access: write\n    plan: change-plan\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"one type twice", map[string]string{
			stack.FileName:                     two,
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/checks/check.js":     "x\n",
			"modules/web/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: web\nscope_checks:\n  change-plan: [check.sh]\n",
			"modules/web/check.sh":             "x\n",
			"modules/core/skills/fix/SKILL.md": "text\n",
		}, "Remove the scope check for change-plan from module core or web.\n  A type has one scope check, and both modules declare one."},
		{"a program that is no file", map[string]string{
			stack.FileName:                     planStack,
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/skills/fix/SKILL.md": "text\n",
		}, "Add the program of the scope check to its module, or change its path.\n  Module core has no file at checks/check.js, the program of the scope check for change-plan."},
		{"a program that is a directory", map[string]string{
			stack.FileName:                     planStack,
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/skills/fix/SKILL.md": "text\n",
			"modules/core/checks/check.js/":    "",
		}, "Set the program of the scope check to a file in its module.\n  checks/check.js of module core is not a file, and it is the program of the scope check for change-plan."},
		{"a program left out by an exclude", map[string]string{
			stack.FileName:                       strings.Replace(planStack, "source: {path: modules/core}", "source: {path: modules/core}\n    exclude: {skills: [tools]}", 1),
			"modules/core/qory-module.yaml":      strings.ReplaceAll(scopeManifest, "checks/check.js", "skills/tools/check.js"),
			"modules/core/skills/fix/SKILL.md":   "text\n",
			"modules/core/skills/tools/SKILL.md": "text\n",
			"modules/core/skills/tools/check.js": "x\n",
		}, "Keep the program of the scope check in its module, or move it out of the left-out entry.\n  The program skills/tools/check.js of the scope check for change-plan is in skills/tools, and an exclude or only of module core leaves skills/tools out."},
	} {
		_, err := composeTree(t, c.files)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.name, err, c.want)
		}
	}
}

// TestTheDuplicateScopeCheckRefusalFixFitsInAHundredCharacters is two modules that declare
// a scope check for one type, with names and a type of 3 lengths: the first line of the
// refusal is the fix and has at most 100 characters, and the full text has both module
// names and the type, whole.
func TestTheDuplicateScopeCheckRefusalFixFitsInAHundredCharacters(t *testing.T) {
	for _, c := range []struct {
		name         string
		first, other string
		typ          string
	}{
		{"short names", "core", "web", "build-plan"},
		{"20 characters", strings.Repeat("a", 20), strings.Repeat("b", 20), "build-plan"},
		{"a first line of 98 characters", strings.Repeat("a", 22), strings.Repeat("b", 21), "build-plan"},
		{"a first line of 99 characters", strings.Repeat("a", 22), strings.Repeat("b", 22), "build-plan"},
		{"24 characters", strings.Repeat("a", 24), strings.Repeat("b", 24), "build-plan"},
		{"40 characters", strings.Repeat("a", 40), strings.Repeat("b", 40), "build-plan"},
		{"24 characters and a 64-character type", strings.Repeat("a", 24), strings.Repeat("b", 24), strings.Repeat("p", 64)},
		{"40 characters and a 64-character type", strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("p", 64)},
	} {
		manifest := func(name string) string {
			return "apiVersion: qory.dev/v1alpha1\nname: " + name + "\nscope_checks:\n  " + c.typ + ": [check.sh]\n"
		}
		_, err := composeTree(t, map[string]string{
			stack.FileName:                  "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + c.first + "\n    source: {path: modules/a}\n  - name: " + c.other + "\n    source: {path: modules/b}\nworkflows:\n  fix:\n    access: write\n    plan: " + c.typ + "\n",
			"modules/a/qory-module.yaml":    manifest(c.first),
			"modules/a/check.sh":            "x\n",
			"modules/a/skills/fix/SKILL.md": "text\n",
			"modules/b/qory-module.yaml":    manifest(c.other),
			"modules/b/check.sh":            "x\n",
		})
		if err == nil {
			t.Errorf("%s: composed", c.name)
			continue
		}
		text := err.Error()
		first, _, _ := strings.Cut(text, "\n")
		if n := printedLength(first); n > 100 || !strings.HasPrefix(first, "Remove the scope check") {
			t.Errorf("%s: first line %q has %d characters", c.name, first, n)
		}
		// With 98 characters or fewer in the full fix, line 1 has the type and both names.
		// The whole text is compared.
		full := "Remove the scope check for " + c.typ + " from module " + c.first + " or " + c.other + "."
		want := full + "\n  A type has one scope check, and both modules declare one."
		if printedLength(full) > 100 {
			want = "Remove the scope check for the type from one of the two modules.\n" +
				"  Modules " + c.first + " and " + c.other + " both declare one for " + c.typ + ", and a type has one scope check."
		}
		if text != want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, text, want)
		}
	}
}

// checkoutScopeTree is a base stack with the write workflow fix, which names the plan type
// change-plan, over module a, and a checkout that extends it with module app. The manifests
// of the two modules are the arguments.
func checkoutScopeTree(baseManifest, checkoutManifest string) map[string]string {
	return map[string]string{
		"base/" + stack.FileName:                      "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [skills]\nmodules:\n  - name: a\n    source: {path: modules/a}\nworkflows:\n  fix:\n    access: write\n    plan: change-plan\n",
		"base/modules/a/qory-module.yaml":             baseManifest,
		"base/modules/a/skills/workflow-fix/SKILL.md": "base\n",
		"base/modules/a/checks/check.js":              "x\n",
		"app/" + config.FileName:                      "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: claude\n  modules:\n    - name: app\n      source: {path: harness}\n",
		"app/harness/qory-module.yaml":                checkoutManifest,
		"app/harness/checks/check.js":                 "x\n",
	}
}

func composeCheckout(t *testing.T, files map[string]string) (*compose.Result, error) {
	t.Helper()
	dir := writeTree(t, files)
	p, err := config.Compose(dir + "/app/" + config.FileName)
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return compose.ComposeWith(merged, compose.Options{Base: base})
}

// TestACheckoutModulesScopeCheckDoesNotCount is the repository under work, a checkout
// that extends a base, shipping a module that declares the scope check for the plan type of
// the base's workflow: the base stack is refused as if no module declared it. The module's
// scope check has no effect when a base module declares one for the type either: the
// result has the base's, and the compose reports no second declaration.
func TestACheckoutModulesScopeCheckDoesNotCount(t *testing.T) {
	const checkoutDeclares = "apiVersion: qory.dev/v1alpha1\nname: app\nscope_checks:\n  change-plan: [checks/check.js, scope, --plan]\n"
	const baseDeclares = "apiVersion: qory.dev/v1alpha1\nname: a\nscope_checks:\n  change-plan: [checks/check.js, scope, --plan]\n"
	const baseSilent = "apiVersion: qory.dev/v1alpha1\nname: a\n"
	t.Run("checkout only is refused", func(t *testing.T) {
		_, err := composeCheckout(t, checkoutScopeTree(baseSilent, checkoutDeclares))
		const want = "Add a scope check for change-plan to a module's scope_checks.\n" +
			"  You can also remove `plan: change-plan` from fix in the stack.\n" +
			"  No module of the stack declares a scope check for change-plan.\n" +
			"  workflows.fix is in the qory-stack.yaml of the base stack core@working-tree."
		if err == nil || err.Error() != want {
			t.Errorf("%v, want %q", err, want)
		}
	})
	t.Run("the base's check is the result", func(t *testing.T) {
		res, err := composeCheckout(t, checkoutScopeTree(baseDeclares, checkoutDeclares))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		got := res.ScopeChecks["change-plan"]
		if got.Module != "a" || !slices.Equal(got.Command, []string{"$QORY_HARNESS_HOME/modules/a/checks/check.js", "scope", "--plan"}) {
			t.Errorf("scope check %+v, want the one of module a", got)
		}
	})
	// With no workflow that names a plan, the refusal cannot catch a check of the
	// checkout: the result must have none of it.
	t.Run("no plan gives no scope check", func(t *testing.T) {
		files := checkoutScopeTree(baseSilent, checkoutDeclares)
		files["base/"+stack.FileName] = strings.Replace(files["base/"+stack.FileName], "    plan: change-plan\n", "", 1)
		res, err := composeCheckout(t, files)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if res.ScopeChecks != nil {
			t.Errorf("scope checks %+v, want none: only the module of the checkout declares one", res.ScopeChecks)
		}
	})
	// The checkout's program is not read: a file that is not there does not fail the compose.
	t.Run("a program the checkout lacks", func(t *testing.T) {
		files := checkoutScopeTree(baseDeclares, strings.ReplaceAll(checkoutDeclares, "checks/check.js", "gone.js"))
		res, err := composeCheckout(t, files)
		if err != nil {
			t.Fatal(err)
		}
		res.Close()
	})
}

// TestThePlanRefusalIsOneLineWhenItFits is a plan type and a workflow name short enough
// for the fix and the alternative in 98 characters, which are 100 under the "✗ " that qory
// prints before them: the refusal is one line. A line of 98 characters stays one line, one
// of 99 moves the alternative to a second line, and with the longest names the first line
// is the fix, at most 98 characters.
func TestThePlanRefusalIsOneLineWhenItFits(t *testing.T) {
	refuse := func(plan, workflow string) string {
		t.Helper()
		_, err := composeTree(t, map[string]string{
			stack.FileName: "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  " + workflow + ":\n    access: write\n    plan: " + plan + "\n",
			"modules/core/skills/" + workflow + "/SKILL.md": "text\n",
		})
		if err == nil {
			t.Fatal("composed")
		}
		return err.Error()
	}
	const want = "Add a scope check for a1 to a module's scope_checks, or remove `plan: a1` from x in the stack.\n" +
		"  No module of the stack declares a scope check for a1.\n" +
		"  workflows.x is in qory-stack.yaml."
	if got := refuse("a1", "x"); got != want {
		t.Errorf("short names: %q, want %q", got, want)
	}
	const at98 = "Add a scope check for abcd to a module's scope_checks, or remove `plan: abcd` from x in the stack.\n" +
		"  No module of the stack declares a scope check for abcd.\n" +
		"  workflows.x is in qory-stack.yaml."
	if got := refuse("abcd", "x"); got != at98 || printedLength(strings.SplitN(got, "\n", 2)[0]) != 100 {
		t.Errorf("98 characters: %q, want %q", got, at98)
	}
	const at99 = "Add a scope check for abcd to a module's scope_checks.\n" +
		"  You can also remove `plan: abcd` from xy in the stack.\n" +
		"  No module of the stack declares a scope check for abcd.\n" +
		"  workflows.xy is in qory-stack.yaml."
	if got := refuse("abcd", "xy"); got != at99 {
		t.Errorf("99 characters: %q, want %q", got, at99)
	}
	plan, workflow := strings.Repeat("p", 64), "w"+strings.Repeat("o", 54)
	wantLong := "Add a scope check for ppppppppp…pppppppppp to a module's scope_checks.\n" +
		"  You can also remove `plan: " + plan + "` from " + workflow + " in the stack.\n" +
		"  No module of the stack declares a scope check for " + plan + ".\n" +
		"  workflows." + workflow + " is in qory-stack.yaml."
	if long := refuse(plan, workflow); long != wantLong || printedLength(strings.SplitN(long, "\n", 2)[0]) > 100 {
		t.Errorf("longest names:\n%s\nwant:\n%s", long, wantLong)
	}
}

// TestAProgramRefusalHasNoValueInLine1 is each refusal about the program of a scope check,
// and the refusal about the program of a push check that is no file, with the longest
// values: a module name of 40 characters, a type of 64 characters and the path of the
// program in the software-factory module, 54 characters. Line 1 has the fix alone, at
// most 100 characters under the "✗ ", and the values are on line 2, whole.
func TestAProgramRefusalHasNoValueInLine1(t *testing.T) {
	module := strings.Repeat("m", 40)
	typ := strings.Repeat("p", 64)
	program := "skills/running-a-software-workflow/workflow-checks.cjs"
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: %s\n    source: {path: modules/core}\n%sworkflows:\n  fix:\n    access: write\n    plan: %s\n"
	manifest := func(program string) string {
		return "apiVersion: qory.dev/v1alpha1\nname: " + module + "\nscope_checks:\n  " + typ + ": [" + program + ", scope, --plan]\n"
	}
	folder := "checks/running-a-software-workflow/workflow-checks.cjs"
	for _, c := range []struct {
		name     string
		files    map[string]string
		program  string
		excluded string
		want     string
	}{
		{"no file", map[string]string{"modules/core/skills/fix/SKILL.md": "text\n"}, program, "",
			"Add the program of the scope check to its module, or change its path.\n" +
				"  Module " + module + " has no file at " + program + ", the program of the scope check for " + typ + "."},
		{"not a file", map[string]string{"modules/core/skills/fix/SKILL.md": "text\n", "modules/core/" + folder + "/x": "x\n"}, folder, "",
			"Set the program of the scope check to a file in its module.\n" +
				"  " + folder + " of module " + module + " is not a file, and it is the program of the scope check for " + typ + "."},
		{"left out", map[string]string{"modules/core/skills/fix/SKILL.md": "text\n", "modules/core/" + program: "x\n", "modules/core/skills/running-a-software-workflow/SKILL.md": "text\n"}, program,
			"exclude: {skills: [running-a-software-workflow]}\n",
			"Keep the program of the scope check in its module, or move it out of the left-out entry.\n" +
				"  The program " + program + " of the scope check for " + typ + " is in skills/running-a-software-workflow, and an exclude or only of module " + module + " leaves skills/running-a-software-workflow out."},
	} {
		files := c.files
		files["modules/core/qory-module.yaml"] = manifest(c.program)
		extra := ""
		if c.excluded != "" {
			extra = "    " + c.excluded
		}
		files[stack.FileName] = fmt.Sprintf(head, module, extra, typ)
		_, err := composeTree(t, files)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s:\n%v\nwant:\n%s", c.name, err, c.want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); printedLength(first) > 100 {
			t.Errorf("%s: line 1 has %d characters with the cross: %s", c.name, printedLength(first), first)
		}
	}
	// A push check.
	files := map[string]string{
		stack.FileName:                     "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {path: modules/core}\n",
		"modules/core/qory-module.yaml":    "apiVersion: qory.dev/v1alpha1\nname: " + module + "\npush_checks:\n  - type: " + typ + "\n    command: [" + program + ", push]\n",
		"modules/core/skills/fix/SKILL.md": "text\n",
	}
	want := "Add the program of the push check to its module, or change its path.\n" +
		"  Module " + module + " has no file at " + program + ", the program of the push check for " + typ + "."
	if _, err := composeTree(t, files); err == nil || err.Error() != want {
		t.Errorf("push check:\n%v\nwant:\n%s", err, want)
	}
}

// TestPushCheckRefusalsAboutAProgramAndATypeKeepLine1ShortWithLongValues is the refusal for
// one type that two modules declare a push check for, the one for a program that is no
// file and the one for a program in an entry that an exclude leaves out, with a module name
// and a type of 24 characters and with a module name of 40 characters and a type of 64:
// line 1 has at most 100 characters under the "✗ ", and each text is compared whole.
func TestPushCheckRefusalsAboutAProgramAndATypeKeepLine1ShortWithLongValues(t *testing.T) {
	for _, c := range []struct{ module, typ int }{{24, 24}, {40, 64}} {
		first, second := strings.Repeat("a", c.module), strings.Repeat("b", c.module)
		typ := strings.Repeat("p", c.typ)
		manifest := func(name, command string) string {
			return "apiVersion: qory.dev/v1alpha1\nname: " + name + "\npush_checks:\n  - type: " + typ + "\n    command: [" + command + "]\n"
		}
		module := func(name string, extra string) string {
			return "  - name: " + name + "\n    source: {path: modules/" + name[:1] + "}\n" + extra
		}
		skill := func(m string) map[string]string {
			return map[string]string{"modules/" + m + "/skills/s/SKILL.md": "text\n"}
		}
		merge := func(parts ...map[string]string) map[string]string {
			out := map[string]string{}
			for _, p := range parts {
				for k, v := range p {
					out[k] = v
				}
			}
			return out
		}
		const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n"
		// Two modules that declare the type.
		full := "Remove the push check for " + typ + " from module " + first + " or module " + second + "."
		wantDup := full + "\n  A type has one push check, and both modules declare one."
		if printedLength(full) > 100 {
			wantDup = "Remove the push check for the type from one of the two modules.\n" +
				"  Modules " + first + " and " + second + " both declare one for " + typ + ", and a type has one push check."
		}
		_, err := composeTree(t, merge(skill("a"), map[string]string{
			stack.FileName:                head + module(first, "") + module(second, ""),
			"modules/a/qory-module.yaml":  manifest(first, "check.sh"),
			"modules/a/check.sh":          "x\n",
			"modules/b/qory-module.yaml":  manifest(second, "check.sh"),
			"modules/b/check.sh":          "x\n",
			"modules/b/skills/t/SKILL.md": "text\n",
		}))
		if err == nil || err.Error() != wantDup {
			t.Errorf("%d/%d two modules:\n%v\nwant:\n%s", c.module, c.typ, err, wantDup)
		}
		// A program that is a folder.
		wantFolder := "Set the program of the push check to a file in its module.\n" +
			"  checks/check.js of module " + first + " is not a file, and it is the program of the push check for " + typ + "."
		_, err = composeTree(t, merge(skill("a"), map[string]string{
			stack.FileName:                head + module(first, ""),
			"modules/a/qory-module.yaml":  manifest(first, "checks/check.js"),
			"modules/a/checks/check.js/x": "x\n",
		}))
		if err == nil || err.Error() != wantFolder {
			t.Errorf("%d/%d folder:\n%v\nwant:\n%s", c.module, c.typ, err, wantFolder)
		}
		// A program in an entry that the exclude leaves out.
		wantExcluded := "Keep the program of the push check in its module, or move it out of the left-out entry.\n" +
			"  The program skills/tools/check.js of the push check for " + typ + " is in skills/tools, and an exclude or only of module " + first + " leaves skills/tools out."
		_, err = composeTree(t, merge(skill("a"), map[string]string{
			stack.FileName:                    head + module(first, "    exclude: {skills: [tools]}\n"),
			"modules/a/qory-module.yaml":      manifest(first, "skills/tools/check.js"),
			"modules/a/skills/tools/SKILL.md": "text\n",
			"modules/a/skills/tools/check.js": "x\n",
		}))
		if err == nil || err.Error() != wantExcluded {
			t.Errorf("%d/%d excluded:\n%v\nwant:\n%s", c.module, c.typ, err, wantExcluded)
		}
		for _, text := range []string{wantDup, wantFolder, wantExcluded} {
			if line, _, _ := strings.Cut(text, "\n"); printedLength(line) > 100 {
				t.Errorf("%d/%d: line 1 has %d characters with the cross: %s", c.module, c.typ, printedLength(line), line)
			}
		}
	}
}
