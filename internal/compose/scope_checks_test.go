package compose_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// planStack is a stack over module core whose workflow fix has access write and the plan
// type change-plan.
const planStack = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  fix:\n    access: write\n    plan: change-plan\n"

const scopeManifest = "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n  change-plan:\n    - [checks/check.js, scope, --plan]\n    - [checks/check.js, freeze, --plan]\n"

// TestAPlanWithAScopeCheckComposes is a workflow that names a plan type and a module of the
// stack that declares the scope check for it: the compose passes, and the result has the
// commands in the order of the manifest, each program under $QORY_HARNESS_HOME.
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
	want := map[string]compose.ScopeCheck{"change-plan": {Module: "core", Commands: [][]string{
		{"$QORY_HARNESS_HOME/modules/core/checks/check.js", "scope", "--plan"},
		{"$QORY_HARNESS_HOME/modules/core/checks/check.js", "freeze", "--plan"},
	}}}
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
	const want = "Add a scope check for change-plan to a module's scope_checks, or remove `plan: change-plan` from fix in the stack."
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
	for _, access := range []string{"", "    access: read\n", "    access: write-no-push\n"} {
		const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\nworkflows:\n  fix:\n"
		_, err := composeTree(t, map[string]string{
			stack.FileName:                     head + access + "    plan: change-plan\n",
			"modules/core/skills/fix/SKILL.md": "text\n",
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/checks/check.js":     "#!/usr/bin/env node\n",
		})
		if want := "Remove `plan` from fix, or set its access to write."; err == nil || err.Error() != want {
			t.Errorf("access %q: %v, want %q", access, err, want)
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
			"modules/web/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: web\nscope_checks:\n  change-plan:\n    - [check.sh]\n",
			"modules/web/check.sh":             "x\n",
			"modules/core/skills/fix/SKILL.md": "text\n",
		}, "Remove the scope check for change-plan from module core or module web.\n  A type has one scope check, and both modules declare one."},
		{"a program that is no file", map[string]string{
			stack.FileName:                     planStack,
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/skills/fix/SKILL.md": "text\n",
		}, "Add checks/check.js to module core, or change its scope check for change-plan.\n  Module core has no file at checks/check.js."},
		{"a program that is a directory", map[string]string{
			stack.FileName:                     planStack,
			"modules/core/qory-module.yaml":    scopeManifest,
			"modules/core/skills/fix/SKILL.md": "text\n",
			"modules/core/checks/check.js/":    "",
		}, "Set the scope check for change-plan of module core to a program file.\n  checks/check.js is not a file."},
		{"a program left out by an exclude", map[string]string{
			stack.FileName:                       strings.Replace(planStack, "source: {path: modules/core}", "source: {path: modules/core}\n    exclude: {skills: [tools]}", 1),
			"modules/core/qory-module.yaml":      strings.ReplaceAll(scopeManifest, "checks/check.js", "skills/tools/check.js"),
			"modules/core/skills/fix/SKILL.md":   "text\n",
			"modules/core/skills/tools/SKILL.md": "text\n",
			"modules/core/skills/tools/check.js": "x\n",
		}, "Keep skills/tools in module core, or move skills/tools/check.js out of it.\n  The program of the scope check for change-plan is in skills/tools, and an exclude or only of module core leaves skills/tools out."},
	} {
		_, err := composeTree(t, c.files)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.name, err, c.want)
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
	const checkoutDeclares = "apiVersion: qory.dev/v1alpha1\nname: app\nscope_checks:\n  change-plan:\n    - [checks/check.js, scope, --plan]\n"
	const baseDeclares = "apiVersion: qory.dev/v1alpha1\nname: a\nscope_checks:\n  change-plan:\n    - [checks/check.js, scope, --plan]\n"
	const baseSilent = "apiVersion: qory.dev/v1alpha1\nname: a\n"
	_, err := composeCheckout(t, checkoutScopeTree(baseSilent, checkoutDeclares))
	const want = "Add a scope check for change-plan to a module's scope_checks, or remove `plan: change-plan` from fix in the stack."
	if err == nil || err.Error() != want {
		t.Errorf("checkout only: %v, want %q", err, want)
	}
	res, err := composeCheckout(t, checkoutScopeTree(baseDeclares, checkoutDeclares))
	if err != nil {
		t.Fatalf("both: %v", err)
	}
	defer res.Close()
	got := res.ScopeChecks["change-plan"]
	if got.Module != "a" || len(got.Commands) != 1 || got.Commands[0][0] != "$QORY_HARNESS_HOME/modules/a/checks/check.js" {
		t.Errorf("both: scope check %+v, want the one of module a", got)
	}
	// The checkout's program is not read: a file that is not there does not fail the compose.
	files := checkoutScopeTree(baseDeclares, strings.ReplaceAll(checkoutDeclares, "checks/check.js", "gone.js"))
	res2, err := composeCheckout(t, files)
	if err != nil {
		t.Fatalf("a program the checkout lacks: %v", err)
	}
	res2.Close()
}
