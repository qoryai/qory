package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestComposeReadsTheHooks reads harness.hooks beside extends and under compose -f, where
// hooks alone are a document, with each hook's program and timeout set.
func TestComposeReadsTheHooks(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "app", "qory.yaml")
	hooks := "  hooks:\n    - {at: after-review-round, kind: check, run: scripts/check-licences.sh --all, timeout: 2m}\n    - {at: after-review-round, kind: review, agent: licence-reviewer, required: false}\n"
	writeRaw(t, path, "harness:\n  extends: {path: ../base}\n"+hooks)
	p, err := config.Compose(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hooks) != 2 || strings.Join(p.Hooks[0].Command, " ") != "scripts/check-licences.sh --all" || p.Hooks[0].Timeout.String() != "2m0s" || p.Hooks[1].Agent != "licence-reviewer" || p.Hooks[1].Required() {
		t.Fatalf("hooks: %+v", p.Hooks)
	}

	base := filepath.Join(tmp, "tree", "software", stack.FileName)
	writeRaw(t, base, stackDoc)
	writeRaw(t, path, "harness:\n"+hooks)
	p, _, err = config.OnBase(path, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hooks) != 2 {
		t.Fatalf("under -f: hooks %+v", p.Hooks)
	}
}

// TestComposeRefusesHooksOutsideAnExtension refuses harness.hooks in a section that
// defines a stack, hook points in the workflows of such a stack, a key a hook does not
// have, a key present with an empty value and a timeout under a second, so that nothing
// a file sets is left unread. The path of qory.yaml is in the cause line, after the fix.
func TestComposeRefusesHooksOutsideAnExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qory.yaml")
	for _, tc := range []struct{ body, want string }{
		{"harness:\n  target: {runtime: claude}\n  modules: [{name: app}]\n  hooks:\n    - {at: after-plan, kind: check, run: x.sh}\n",
			"Remove harness.hooks from qory.yaml, or extend a stack that allows them.\nA hook extends a workflow of a base stack, and " + path + " defines a stack."},
		{"harness:\n  target: {runtime: claude}\n  modules: [{name: app}]\n  workflows:\n    build:\n      hook_points: {after-plan: {at: end, kinds: [check]}}\n",
			"Remove hook_points from the workflows in qory.yaml.\nA checkout adds workflow hooks to a stack it extends, so only a qory-stack.yaml declares hook points."},
		{"harness:\n  extends: {path: ../base}\n  hooks:\n    - {at: after-plan, kind: check, run: x.sh, when: always}\n",
			path + `: line 4: key "when" is not one qory.yaml reads`},
		{"harness:\n  extends: {path: ../base}\n  hooks:\n    - {at: after-plan, kind: review, run: x.sh}\n",
			"Set hooks[0].agent in place of hooks[0].run.\nA review hook runs an agent, and hooks[0] in " + path + " runs a program."},
		{"harness:\n  extends: {path: ../base}\n  hooks:\n    - {at: after-plan, kind: review, agent: licence-reviewer, run: \"\"}\n",
			"Remove hooks[0].run, or give it a value.\n" + path + " sets hooks[0].run to an empty value."},
		{"harness:\n  extends: {path: ../base}\n  hooks:\n    - {at: after-plan, kind: check, run: x.sh, timeout: 500ms}\n",
			"Set hooks[0].timeout to whole seconds, 1s or more, such as 90s or 5m.\n" + path + ` sets "500ms".`},
	} {
		writeRaw(t, path, tc.body)
		_, err := config.Compose(path)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s:\nerr = %v\nwant %s", tc.body, err, tc.want)
		}
	}
}
